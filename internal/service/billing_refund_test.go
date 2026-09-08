package service

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"

	"github.com/joho/godotenv"
)

// 退款比例闭环单测（2026-09-08 商业化审计修复）：
//   - increment 增量包：按"未消耗份额"比例退款并回收②桶余额
//   - 全消耗 → ErrRefundNoRemaining（已消费不能退，状态保持 paid）
//   - 多单公平份额：多笔增量包退款顺序无关、总量不超购入量
//
// 依赖 dev 库（testutil.SetupTestDB，DB 不可用自动跳过）。
func TestRefundIncrementProportional(t *testing.T) {
	testutil.SetupTestDB(t)
	_ = godotenv.Load("../../.env")
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	// 增量包：300万 token / ¥199.00（分=19900）
	pkg := &model.Package{Code: fmt.Sprintf("ut_inc_%d", time.Now().UnixNano()), Name: "UAT增量",
		PType: "increment", TokenAmount: 3000000, PriceCents: 19900, Enabled: true}
	if err := db.DB.Create(pkg).Error; err != nil {
		t.Fatalf("建包失败: %v", err)
	}
	// 订单到账 + 全额发桶
	oid := createPaidIncrementOrder(t, tid, pkg)
	if err := db.DB.Model(&model.Tenant{}).Where("id=?", tid).
		Update("token_balance", 3000000).Error; err != nil {
		t.Fatalf("设②桶失败: %v", err)
	}
	// 模拟已消费 100万 → 剩 200万
	if err := db.DB.Model(&model.Tenant{}).Where("id=?", tid).
		Update("token_balance", 2000000).Error; err != nil {
		t.Fatalf("模拟消费失败: %v", err)
	}

	o, flowed, err := MarkOrderRefunded(oid)
	if err != nil {
		t.Fatalf("退款应成功: %v", err)
	}
	if !flowed {
		t.Fatalf("首次退款应流转")
	}
	// 退款 = 19900 × 200万/300万 = 13267分（四舍五入）
	if o.RefundAmountCents != 13266 && o.RefundAmountCents != 13267 {
		t.Fatalf("退款金额应为13266/13267分, got %d", o.RefundAmountCents)
	}
	if o.Status != "refunded" {
		t.Fatalf("订单应 refunded, got %s", o.Status)
	}
	// ②桶回收剩余 200万 → 0
	var tnt model.Tenant
	if err := db.DB.First(&tnt, tid).Error; err != nil {
		t.Fatalf("读租户失败: %v", err)
	}
	if tnt.TokenBalance != 0 {
		t.Fatalf("②桶应清零(回收200万), got %d", tnt.TokenBalance)
	}

	// 幂等：二次退款 flowed=false
	_, flowed2, err := MarkOrderRefunded(oid)
	if err != nil || flowed2 {
		t.Fatalf("二次退款应 flowed=false 且无错, flowed=%v err=%v", flowed2, err)
	}
}

func TestRefundIncrementFullyConsumed(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	pkg := &model.Package{Code: fmt.Sprintf("ut_inc0_%d", time.Now().UnixNano()), Name: "UAT已耗尽",
		PType: "increment", TokenAmount: 3000000, PriceCents: 19900, Enabled: true}
	if err := db.DB.Create(pkg).Error; err != nil {
		t.Fatalf("建包失败: %v", err)
	}
	oid := createPaidIncrementOrder(t, tid, pkg)
	if err := db.DB.Model(&model.Tenant{}).Where("id=?", tid).
		Update("token_balance", 0).Error; err != nil { // 全部消耗
		t.Fatalf("模拟耗尽失败: %v", err)
	}
	_, _, err := MarkOrderRefunded(oid)
	if !errors.Is(err, ErrRefundNoRemaining) {
		t.Fatalf("全消耗应拒绝退款(ErrRefundNoRemaining), got %v", err)
	}
	var o model.BillingOrder
	if err := db.DB.First(&o, oid).Error; err != nil {
		t.Fatalf("读订单失败: %v", err)
	}
	if o.Status != "paid" {
		t.Fatalf("拒绝退款不应改状态, got %s", o.Status)
	}
}

func TestRefundMultiOrdersSharesConsistent(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	// A=100万/¥9900, B=200万/¥19800，两单全额入桶共 300万
	pkgA := &model.Package{Code: fmt.Sprintf("ut_ma_%d", time.Now().UnixNano()), Name: "包A",
		PType: "increment", TokenAmount: 1000000, PriceCents: 9900, Enabled: true}
	pkgB := &model.Package{Code: fmt.Sprintf("ut_mb_%d", time.Now().UnixNano()), Name: "包B",
		PType: "increment", TokenAmount: 2000000, PriceCents: 19800, Enabled: true}
	if err := db.DB.Create(pkgA).Error; err != nil {
		t.Fatalf("建包A失败: %v", err)
	}
	if err := db.DB.Create(pkgB).Error; err != nil {
		t.Fatalf("建包B失败: %v", err)
	}
	oa := createPaidIncrementOrder(t, tid, pkgA)
	ob := createPaidIncrementOrder(t, tid, pkgB)
	if err := db.DB.Model(&model.Tenant{}).Where("id=?", tid).
		Update("token_balance", 3000000).Error; err != nil {
		t.Fatalf("入桶失败: %v", err)
	}

	// 先退 B：份额=min(300万,300万)×200万/300万=200万 → 全额
	oB, flowedB, err := MarkOrderRefunded(ob)
	if err != nil || !flowedB {
		t.Fatalf("退B应成功 flowed=%v err=%v", flowedB, err)
	}
	if oB.RefundAmountCents != 19800 {
		t.Fatalf("退B应为全额19800分, got %d", oB.RefundAmountCents)
	}
	var tnt model.Tenant
	if err := db.DB.First(&tnt, tid).Error; err != nil {
		t.Fatalf("读租户失败: %v", err)
	}
	if tnt.TokenBalance != 1000000 {
		t.Fatalf("退B后②桶应剩100万, got %d", tnt.TokenBalance)
	}
	// 再退 A：剩桶100万，未退购入=100万，份额=min(100万,100万)×100万/100万=100万 → 全额
	oA, flowedA, err := MarkOrderRefunded(oa)
	if err != nil || !flowedA {
		t.Fatalf("退A应成功 flowed=%v err=%v", flowedA, err)
	}
	if oA.RefundAmountCents != 9900 {
		t.Fatalf("退A应为全额9900分, got %d", oA.RefundAmountCents)
	}
	if err := db.DB.First(&tnt, tid).Error; err != nil {
		t.Fatalf("读租户失败: %v", err)
	}
	if tnt.TokenBalance != 0 {
		t.Fatalf("两单全退后②桶应清零, got %d", tnt.TokenBalance)
	}
}

// createPaidIncrementOrder 建一笔已支付增量包订单（含包关联）
func createPaidIncrementOrder(t *testing.T, tid uint, pkg *model.Package) uint {
	t.Helper()
	o := &model.BillingOrder{
		OrderNo:             fmt.Sprintf("UTREF%d", time.Now().UnixNano()),
		TenantID:            &tid,
		PackageID:           pkg.ID,
		AmountCents:         pkg.PriceCents,
		OriginalAmountCents: pkg.PriceCents,
		Status:              "paid",
		Channel:             "mock",
		Period:              "once",
	}
	if err := db.DB.Create(o).Error; err != nil {
		t.Fatalf("建订单失败: %v", err)
	}
	return o.ID
}

func TestRefundPaidProportional(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	// 包月：30天 / ¥99（9900分）
	pkg := &model.Package{Code: fmt.Sprintf("ut_paid_%d", time.Now().UnixNano()), Name: "UAT包月",
		PType: "paid", TokenAmount: 1000000, PriceCents: 9900, DurationDays: 30, Enabled: true}
	if err := db.DB.Create(pkg).Error; err != nil {
		t.Fatalf("建包失败: %v", err)
	}
	o := &model.BillingOrder{
		OrderNo:  fmt.Sprintf("UTPAID%d", time.Now().UnixNano()),
		TenantID: &tid, PackageID: pkg.ID,
		AmountCents: pkg.PriceCents, OriginalAmountCents: pkg.PriceCents,
		Status: "paid", Channel: "mock", Period: "monthly",
	}
	if err := db.DB.Create(o).Error; err != nil {
		t.Fatalf("建订单失败: %v", err)
	}
	// 订阅刚生效10天多 → 剩约20天（expired_at=now+20d）
	exp := time.Now().Add(20 * 24 * time.Hour)
	if err := db.DB.Model(&model.Tenant{}).Where("id=?", tid).
		Updates(map[string]any{"expired_at": exp, "monthly_token_quota": pkg.TokenAmount, "status": "active"}).Error; err != nil {
		t.Fatalf("设订阅失败: %v", err)
	}

	po, flowed, err := MarkOrderRefunded(o.ID)
	if err != nil || !flowed {
		t.Fatalf("包月退款应成功 flowed=%v err=%v", flowed, err)
	}
	// 剩20/30天 → 退款 = 9900×20/30 = 6600分
	if po.RefundAmountCents != 6600 {
		t.Fatalf("包月退款应为6600分(20/30天), got %d", po.RefundAmountCents)
	}
	var tnt model.Tenant
	if err := db.DB.First(&tnt, tid).Error; err != nil {
		t.Fatalf("读租户失败: %v", err)
	}
	// 摘除订阅：到期摘除（expired_at≈now）+ 月配额清零
	if tnt.ExpiredAt == nil || time.Until(*tnt.ExpiredAt) > time.Hour {
		t.Fatalf("退款后订阅应即刻失效, expired_at=%v", tnt.ExpiredAt)
	}
	if tnt.MonthlyTokenQuota != 0 {
		t.Fatalf("退款后月配额应清零, got %d", tnt.MonthlyTokenQuota)
	}
	if tnt.Status != "expired" && tnt.Status != "active" {
		t.Fatalf("订阅状态异常: %s", tnt.Status)
	}
}
