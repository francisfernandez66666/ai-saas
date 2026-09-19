// UsageSink 实时计量单测：影子余额 seed/扣减、批量落库幂等、Invalidate 失效重读。
// 依赖 DB：经 testutil.SetupTestDB 统一初始化（不可用则自动跳过）。
package billing

import (
	"ai-scrm/internal/runtimecfg"
	"sync"
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// TestSinkInvalidateShadow 验证失效后重新 seed（充值/发放后影子不残留旧值）
func TestSinkInvalidateShadow(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: 跳过 DB 依赖测试")
	}
	testutil.SetupTestDB(t)

	tenant := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tenant)

	// 初始影子：手工 seed（模拟已有一次调用后的影子）
	DefaultUsageSink.mu.Lock()
	DefaultUsageSink.shadow[tenant] = 100
	DefaultUsageSink.shadowOk[tenant] = true
	DefaultUsageSink.mu.Unlock()

	InvalidateShadow(tenant)
	DefaultUsageSink.mu.Lock()
	_, ok := DefaultUsageSink.shadowOk[tenant]
	DefaultUsageSink.mu.Unlock()
	if ok {
		t.Fatalf("InvalidateShadow 后 shadowOk 仍存在 tenant=%d", tenant)
	}
}

// TestTenantTokenRemain 三桶可用合计读取（免费桶过期不计、月度剩余、永久余额）
func TestTenantTokenRemain(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: 跳过 DB 依赖测试")
	}
	testutil.SetupTestDB(t)

	tenant := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tenant)

	// 免费桶 100 + 月度配额 1000-200 + 永久 50 = 950
	db.DB.Model(&model.Tenant{}).Where("id = ?", tenant).Updates(map[string]interface{}{
		"free_token_balance":    100,
		"monthly_token_quota":   1000,
		"monthly_token_used":    200,
		"token_balance":         50,
		"free_token_expires_at": nil,
	})
	got := tenantTokenRemain(tenant)
	if got != 950 {
		t.Fatalf("tenantTokenRemain=%d want 950", got)
	}
}

// TestSinkFlushBatch 批量落库：SinkRecordUsage 多租户投递，flush 后每租户一次扣减
// 强制计费开启时验证三桶递减；未开启时验证 no-op（不 panic）。
func TestSinkFlushBatch(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: 跳过 DB 依赖测试")
	}
	testutil.SetupTestDB(t)

	tenant := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tenant)

	// 关闭强制计费：SinkRecordUsage 应 no-op（token_billing_enabled=false 直接返回）
	old := runtimecfg.DefaultSystemConfigService
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(map[string]string{}, nil)
	defer func() { runtimecfg.DefaultSystemConfigService = old }()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			SinkRecordUsage(tenant, 100) // 总闸未开：不 panic、不入账
		}()
	}
	wg.Wait()
	// 不 flush 也不 panic 即为通过（总闸关 = 兼容现状）

	// 开引擎但灰度未强制：仅留痕不扣减
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(map[string]string{"token_billing_enabled": "true"}, nil)
	SinkRecordUsage(tenant, 100)
	DefaultUsageSink.flush() // 不应 panic；灰度路径 DeductTokensActual 内仅日志
}

// ============================================================
// P0-1（2026-09-20 审计批）：写前挂账 usage_flush_retry + 同事务核销
// ============================================================

// setEnforcedBilling 临时开启"引擎总闸+强制计费"双开关，返回还原函数
func setEnforcedBilling(t *testing.T) func() {
	t.Helper()
	old := runtimecfg.DefaultSystemConfigService
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(map[string]string{
		"token_billing_enabled": "true",
		"billing_enforced":      "true",
	}, nil)
	return func() { runtimecfg.DefaultSystemConfigService = old }
}

// seedTenantBuckets 置租户三桶为已知值：免费100 + 月度(1000-200) + 永久50 = 可用950
func seedTenantBuckets(t *testing.T, tenant uint) {
	t.Helper()
	db.DB.Model(&model.Tenant{}).Where("id = ?", tenant).Updates(map[string]interface{}{
		"free_token_balance":    100,
		"monthly_token_quota":   1000,
		"monthly_token_used":    200,
		"token_balance":         50,
		"free_token_expires_at": nil,
	})
}

// TestSettleRetryRows 挂账核销正常路径：多行同租户一次事务结清，三桶按序扣减，行删除
func TestSettleRetryRows(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: 跳过 DB 依赖测试")
	}
	testutil.SetupTestDB(t)
	tenant := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tenant)
	seedTenantBuckets(t, tenant)
	defer InvalidateShadow(tenant)

	rows := []model.UsageFlushRetry{{TenantID: tenant, Tokens: 100}, {TenantID: tenant, Tokens: 150}}
	if err := db.DB.Create(&rows).Error; err != nil {
		t.Fatalf("插入挂账行失败: %v", err)
	}
	ids := []uint{rows[0].ID, rows[1].ID}
	left := settleRetryRows(ids)
	if len(left) != 0 {
		t.Fatalf("核销应全部成功，剩余 %v", left)
	}
	var cnt int64
	db.DB.Model(&model.UsageFlushRetry{}).Where("id IN ?", ids).Count(&cnt)
	if cnt != 0 {
		t.Fatalf("核销后挂账行应删除，仍剩 %d", cnt)
	}
	// 250 tokens：免费100 先扣光，再扣月度150 → 可用合计 950-250=700
	if got := tenantTokenRemain(tenant); got != 700 {
		t.Fatalf("三桶扣减后可用=%d want 700", got)
	}
}

// TestSettleRetryRowsFailureKeepsRows 核销失败（租户不存在）：行必须存活待 sweep，不静默吞账
func TestSettleRetryRowsFailureKeepsRows(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: 跳过 DB 依赖测试")
	}
	testutil.SetupTestDB(t)
	ghost := uint(999999999)
	db.DB.Where("tenant_id = ?", ghost).Delete(&model.UsageFlushRetry{}) // 清历史残留
	row := model.UsageFlushRetry{TenantID: ghost, Tokens: 66}
	if err := db.DB.Create(&row).Error; err != nil {
		t.Fatalf("插入挂账行失败: %v", err)
	}
	defer db.DB.Where("tenant_id = ?", ghost).Delete(&model.UsageFlushRetry{})
	left := settleRetryRows([]uint{row.ID})
	if len(left) != 1 || left[0] != row.ID {
		t.Fatalf("失败行应原样返回待重投，got=%v", left)
	}
	var cnt int64
	db.DB.Model(&model.UsageFlushRetry{}).Where("id = ?", row.ID).Count(&cnt)
	if cnt != 1 {
		t.Fatalf("核销失败时挂账行不得被删除")
	}
}

// TestFlushEnforcedWriteAheadSettles 强制计费 flush 全链路：Record→挂账表→同批核销→扣减，
// 表内无残留；灰度（未强制）路径不写挂账表。
func TestFlushEnforcedWriteAheadSettles(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: 跳过 DB 依赖测试")
	}
	testutil.SetupTestDB(t)
	tenant := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tenant)
	seedTenantBuckets(t, tenant)
	defer InvalidateShadow(tenant)

	restore := setEnforcedBilling(t)
	defer restore()

	s := &UsageSink{
		shadow: map[uint]int64{}, shadowOk: map[uint]bool{}, alertedAt: map[uint]time.Time{},
		flushInterval: 2 * time.Second, maxBatch: 200, maxBuf: 50000,
		wake: make(chan struct{}, 1), stop: make(chan struct{}),
	}
	s.Record(usageSinkRecord{Tid: tenant, Tokens: 80})
	s.Record(usageSinkRecord{Tid: tenant, Tokens: 20})
	s.flush()
	if got := tenantTokenRemain(tenant); got != 850 {
		t.Fatalf("flush 核销后可用=%d want 850（950-100）", got)
	}
	var cnt int64
	db.DB.Model(&model.UsageFlushRetry{}).Where("tenant_id = ?", tenant).Count(&cnt)
	if cnt != 0 {
		t.Fatalf("同批核销后挂账表应无残留，仍剩 %d", cnt)
	}

	// 灰度（引擎开、不强制）：仅留痕，不写挂账表不扣减
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(map[string]string{"token_billing_enabled": "true"}, nil)
	before := tenantTokenRemain(tenant)
	s.Record(usageSinkRecord{Tid: tenant, Tokens: 30})
	s.flush()
	if got := tenantTokenRemain(tenant); got != before {
		t.Fatalf("灰度路径不应扣减：before=%d after=%d", before, got)
	}
	db.DB.Model(&model.UsageFlushRetry{}).Where("tenant_id = ?", tenant).Count(&cnt)
	if cnt != 0 {
		t.Fatalf("灰度路径不应写挂账表，仍剩 %d", cnt)
	}
}

// TestSweepSettlesCrashLeftover 模拟进程崩溃遗留挂账行：补扫（启动全量阈值）后扣减到账——
// 即 P0-1 验收口径"模拟扣减连续失败/崩溃→重启后补扣"。
func TestSweepSettlesCrashLeftover(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: 跳过 DB 依赖测试")
	}
	testutil.SetupTestDB(t)
	tenant := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tenant)
	seedTenantBuckets(t, tenant)
	defer InvalidateShadow(tenant)

	restore := setEnforcedBilling(t)
	defer restore()

	if err := db.DB.Create(&model.UsageFlushRetry{TenantID: tenant, Tokens: 150}).Error; err != nil {
		t.Fatalf("插入挂账行失败: %v", err)
	}
	// 周期 sweep 有 30s 年龄阈值，新行不动（防抢其它实例在途批次）
	DefaultUsageSink.sweepRetryRows()
	if got := tenantTokenRemain(tenant); got != 950 {
		t.Fatalf("30s 内新行不应被周期 sweep 核销，got=%d", got)
	}
	// 启动补扫：全量阈值，崩溃遗留即补扣
	DefaultUsageSink.sweepRetryRowsAt(time.Now())
	if got := tenantTokenRemain(tenant); got != 800 {
		t.Fatalf("启动补扫后应补扣 150，可用=%d want 800", got)
	}
	var cnt int64
	db.DB.Model(&model.UsageFlushRetry{}).Where("tenant_id = ?", tenant).Count(&cnt)
	if cnt != 0 {
		t.Fatalf("补扫核销后应无残留，仍剩 %d", cnt)
	}
}
