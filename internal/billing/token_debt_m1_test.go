// M1 吞账回归（2026-09-22 全量修复批·批三）：
// 钉住「三桶余额不足时挂账行绝不消失」这一资金红线。
// 旧实现 deductTokensInTx 在扣不动时 log + return nil，而调用方 settleRetryRows 是
// 「锁定→SUM→DELETE→扣减」同事务——return nil 即事务提交，挂账行被删但账没扣，
// 公司侧永久漏收且无人知晓。现改为哨兵错误回滚 / 差额补写新行，两条路径各有断言。
package billing

import (
	"testing"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/testutil"
)

// mkZeroBalanceTenant 建一个三桶全空的租户（免费桶/月额度/余额均为 0），用于制造"扣不动"现场
func mkZeroBalanceTenant(t *testing.T, balance int64) uint {
	t.Helper()
	tid := testutil.CreateTenant(t)
	if err := db.DB.Model(&model.Tenant{}).Where("id = ?", tid).Updates(map[string]interface{}{
		"free_token_balance":  0,
		"monthly_token_quota": 0,
		"monthly_token_used":  0,
		"token_balance":       balance,
	}).Error; err != nil {
		t.Fatalf("置空三桶失败: %v", err)
	}
	return tid
}

// debtRowCount 统计某租户当前存活的挂账行数量与合计 token
func debtRowCount(t *testing.T, tid uint) (int64, int64) {
	t.Helper()
	var cnt, sum int64
	row := db.DB.Model(&model.UsageFlushRetry{}).Where("tenant_id = ?", tid).
		Select("COALESCE(SUM(tokens),0)").Row()
	if err := row.Scan(&sum); err != nil {
		t.Fatalf("读挂账合计失败: %v", err)
	}
	if err := db.DB.Model(&model.UsageFlushRetry{}).Where("tenant_id = ?", tid).Count(&cnt).Error; err != nil {
		t.Fatalf("数挂账行失败: %v", err)
	}
	return cnt, sum
}

// TestSettleEmptyBucketsKeepsDebtRow 三桶全空：核销必须失败回滚，挂账行原样存活（旧实现此处行灭账未扣）
func TestSettleEmptyBucketsKeepsDebtRow(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := mkZeroBalanceTenant(t, 0)
	defer testutil.CleanupTenant(t, tid)

	debt := model.UsageFlushRetry{TenantID: tid, Tokens: 5000}
	if err := db.DB.Create(&debt).Error; err != nil {
		t.Fatalf("插挂账行失败: %v", err)
	}

	left := settleRetryRows([]uint{debt.ID})
	if len(left) != 1 || left[0] != debt.ID {
		t.Fatalf("三桶全空应返回未核销 %d，实际 %v", debt.ID, left)
	}
	cnt, sum := debtRowCount(t, tid)
	if cnt != 1 || sum != 5000 {
		t.Fatalf("挂账行必须存活待补扣，实际行数=%d 合计=%d（期望 1/5000）", cnt, sum)
	}
	// 账单未被扣减：余额仍为 0，没有被扣成负数
	var bal int64
	if err := db.DB.Model(&model.Tenant{}).Where("id = ?", tid).
		Select("token_balance").Row().Scan(&bal); err != nil {
		t.Fatalf("读余额失败: %v", err)
	}
	if bal != 0 {
		t.Fatalf("三桶全空不得产生负余额，实际 token_balance=%d", bal)
	}
}

// TestSettlePartialDeductRewritesDebt 部分可扣：已扣部分结清、差额同事务补写新挂账行，总额守恒
func TestSettlePartialDeductRewritesDebt(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := mkZeroBalanceTenant(t, 1200)
	defer testutil.CleanupTenant(t, tid)

	debt := model.UsageFlushRetry{TenantID: tid, Tokens: 5000}
	if err := db.DB.Create(&debt).Error; err != nil {
		t.Fatalf("插挂账行失败: %v", err)
	}

	left := settleRetryRows([]uint{debt.ID})
	if len(left) != 0 {
		t.Fatalf("本次锁定行应视为已核销（差额转新行），实际 leftover=%v", left)
	}
	var bal int64
	if err := db.DB.Model(&model.Tenant{}).Where("id = ?", tid).
		Select("token_balance").Row().Scan(&bal); err != nil {
		t.Fatalf("读余额失败: %v", err)
	}
	if bal != 0 {
		t.Fatalf("可用余额 1200 应被扣尽，实际 token_balance=%d", bal)
	}
	cnt, sum := debtRowCount(t, tid)
	if cnt != 1 || sum != 3800 {
		t.Fatalf("差额须以新挂账行存活（期望 1 行 / 3800 tokens），实际 %d 行 / %d tokens", cnt, sum)
	}
	// 幂等再核销：余额已空，第二轮不得把差额行删掉（防"越补越丢"）
	var rows []model.UsageFlushRetry
	if err := db.DB.Where("tenant_id = ?", tid).Find(&rows).Error; err != nil {
		t.Fatalf("读挂账行失败: %v", err)
	}
	ids := []uint{rows[0].ID}
	if again := settleRetryRows(ids); len(again) != 1 {
		t.Fatalf("余额耗尽后第二轮应仍未核销，实际 %v", again)
	}
	if c2, s2 := debtRowCount(t, tid); c2 != 1 || s2 != 3800 {
		t.Fatalf("第二轮后挂账须保持 1 行 / 3800，实际 %d 行 / %d", c2, s2)
	}
}

// TestDeductTokensActualNoBalanceKeepsLedger DeductTokensActual 直连路径：三桶全空必须回上错误而非静默 nil
func TestDeductTokensActualNoBalanceKeepsLedger(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := mkZeroBalanceTenant(t, 0)
	defer testutil.CleanupTenant(t, tid)

	// 打开双闸：引擎总闸 + 强制计费，走真实扣减事务
	old := runtimecfg.DefaultSystemConfigService
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(map[string]string{
		"token_billing_enabled": "true",
		"billing_enforced":      "true",
	}, nil)
	defer func() { runtimecfg.DefaultSystemConfigService = old }()

	if err := DeductTokensActual(tid, 700); err == nil {
		t.Fatalf("三桶全空应返回欠账哨兵错误，实际 nil（静默吞账回潮）")
	}
	// 直连路径无挂账行可保，至少必须留痕不产生负余额
	var bal int64
	if err := db.DB.Model(&model.Tenant{}).Where("id = ?", tid).
		Select("token_balance").Row().Scan(&bal); err != nil {
		t.Fatalf("读余额失败: %v", err)
	}
	if bal != 0 {
		t.Fatalf("失败路径不得改余额，实际 %d", bal)
	}
}
