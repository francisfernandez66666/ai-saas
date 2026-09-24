// 退款计费单测：覆盖按比例退款、多订单份额一致性与包月窗口回退。
package billing

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

// TestRefundIncrementFullyConsumed 覆盖 RefundIncrementFullyConsumed 相关行为与边界。
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

// TestRefundMultiOrdersSharesConsistent 覆盖 RefundMultiOrdersSharesConsistent 相关行为与边界。
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

// TestRefundPaidProportional 覆盖 RefundPaidProportional 相关行为与边界。
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
	// R12(2026-09-11)：退款窗口按订单自身生效窗口（paid_at→+30d）计算，
	// 夹具如实还原"已生效10天"：paid_at 设为 10 天前（此前漏设 PaidAt 靠租户级 expired_at 兜底）
	paidAt := time.Now().Add(-10 * 24 * time.Hour)
	o.PaidAt = &paidAt
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
	// 2026-09-24 口径变更：包月按「未消耗积分比例」退，零消耗即全额退 9900 分
	// （旧天数口径在此会退 6600=20/30 天，与用量无关，是白嫖路径）
	if po.RefundAmountCents != 9900 {
		t.Fatalf("零消耗包月应全额退9900分, got %d", po.RefundAmountCents)
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

// TestRefundPaidMultiOrderShrink R12(2026-09-11)：两笔包月叠加时退旧单——
// 只按旧单自身窗口比例退款、到期日回退旧单剩余天数，不得整体摘除新订阅、不得按租户级
// expired_at（含新单续期）超退。
func TestRefundPaidMultiOrderShrink(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	pkg := &model.Package{Code: fmt.Sprintf("ut_ms_%d", time.Now().UnixNano()), Name: "多单包月",
		PType: "paid", TokenAmount: 1000000, PriceCents: 9900, DurationDays: 30, Enabled: true}
	if err := db.DB.Create(pkg).Error; err != nil {
		t.Fatalf("建包失败: %v", err)
	}
	mkOrder := func(paidDaysAgo int) uint {
		paidAt := time.Now().Add(-time.Duration(paidDaysAgo) * 24 * time.Hour)
		o := &model.BillingOrder{
			OrderNo:  fmt.Sprintf("UTMS%d%d", time.Now().UnixNano(), paidDaysAgo),
			TenantID: &tid, PackageID: pkg.ID,
			AmountCents: pkg.PriceCents, OriginalAmountCents: pkg.PriceCents,
			Status: "paid", Channel: "mock", Period: "monthly", PaidAt: &paidAt,
		}
		if err := db.DB.Create(o).Error; err != nil {
			t.Fatalf("建订单失败: %v", err)
		}
		return o.ID
	}
	old := mkOrder(10) // 旧单窗口剩 20 天
	mkOrder(5)         // 新单窗口剩 25 天（租户 expired_at 被续到 +25d）
	exp := time.Now().Add(25 * 24 * time.Hour)
	if err := db.DB.Model(&model.Tenant{}).Where("id=?", tid).
		Updates(map[string]any{"expired_at": exp, "monthly_token_quota": pkg.TokenAmount,
			"monthly_token_used": 400000, "status": "active"}).Error; err != nil {
		t.Fatalf("设订阅失败: %v", err)
	}

	o, flowed, err := MarkOrderRefunded(old)
	if err != nil || !flowed {
		t.Fatalf("退旧单应成功 flowed=%v err=%v", flowed, err)
	}
	// 2026-09-24 口径变更：钱按未消耗积分比例（当期已用 40 万/100 万 → 退 60%）= 5940 分；
	// 日期仍按本单窗口回退。旧"全部剩余天"超退口径由下面的 expired_at 断言继续封堵。
	if o.RefundAmountCents != 5940 {
		t.Fatalf("退旧单应为5940分(未消耗积分六成), got %d", o.RefundAmountCents)
	}
	var tnt model.Tenant
	if err := db.DB.First(&tnt, tid).Error; err != nil {
		t.Fatalf("读租户失败: %v", err)
	}
	// 到期日回退 20 天：+25d → +5d；订阅仍生效（新单未被误伤），月配额不清零
	if tnt.ExpiredAt == nil {
		t.Fatalf("expired_at 不应为空")
	}
	leftDays := time.Until(*tnt.ExpiredAt).Hours() / 24
	if leftDays < 4.5 || leftDays > 5.5 {
		t.Fatalf("到期日应回退至约+5天, got %.1f", leftDays)
	}
	if tnt.MonthlyTokenQuota != pkg.TokenAmount {
		t.Fatalf("非最新单退款不应清零月配额, got %d", tnt.MonthlyTokenQuota)
	}
}

// mkPaidSubOrder 造一笔"已到账 N 天"的包月订单 + 生效中的订阅配额（退款口径用例共用夹具）
func mkPaidSubOrder(t *testing.T, tid uint, pkg *model.Package, daysAgo int) uint {
	t.Helper()
	paidAt := time.Now().Add(-time.Duration(daysAgo) * 24 * time.Hour)
	o := &model.BillingOrder{
		OrderNo:             fmt.Sprintf("UTPSO%d%d", time.Now().UnixNano(), daysAgo),
		TenantID:            &tid,
		PackageID:           pkg.ID,
		AmountCents:         pkg.PriceCents,
		OriginalAmountCents: pkg.PriceCents,
		Status:              "paid",
		Channel:             "mock",
		Period:              "monthly",
		PaidAt:              &paidAt,
	}
	if err := db.DB.Create(o).Error; err != nil {
		t.Fatalf("建包月订单失败: %v", err)
	}
	return o.ID
}

// TestRefundPaidFullyConsumedRejected 2026-09-24 口径「不退已消耗积分」的核心断言：
// ①桶当期额度用满即拒退（409 语义由 ErrRefundNoRemaining 映射），且订阅权益不得被摘除——
// 旧天数比例口径在这里会退 28/30≈93% 现金，正是"用满一个月再来退款"的白嫖路径。
func TestRefundPaidFullyConsumedRejected(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	pkg := &model.Package{Code: fmt.Sprintf("ut_pfull_%d", time.Now().UnixNano()), Name: "满消耗包月",
		PType: "paid", TokenAmount: 1000000, PriceCents: 9900, DurationDays: 30, Enabled: true}
	if err := db.DB.Create(pkg).Error; err != nil {
		t.Fatalf("建包失败: %v", err)
	}
	oid := mkPaidSubOrder(t, tid, pkg, 2) // 生效 2 天，窗口还剩 28 天
	exp := time.Now().Add(28 * 24 * time.Hour)
	if err := db.DB.Model(&model.Tenant{}).Where("id=?", tid).
		Updates(map[string]any{"expired_at": exp, "monthly_token_quota": pkg.TokenAmount,
			"monthly_token_used": pkg.TokenAmount, "status": "active"}).Error; err != nil {
		t.Fatalf("设订阅失败: %v", err)
	}

	_, flowed, err := MarkOrderRefunded(oid)
	if !errors.Is(err, ErrRefundNoRemaining) {
		t.Fatalf("额度用满应拒退 ErrRefundNoRemaining, got err=%v", err)
	}
	if flowed {
		t.Fatalf("拒退时不得流转订单状态")
	}
	var tnt model.Tenant
	if err := db.DB.First(&tnt, tid).Error; err != nil {
		t.Fatalf("读租户失败: %v", err)
	}
	if tnt.MonthlyTokenQuota != pkg.TokenAmount {
		t.Fatalf("拒退后月配额不得被清零, got %d", tnt.MonthlyTokenQuota)
	}
	var st string
	var amt int64
	db.DB.Model(&model.BillingOrder{}).Where("id = ?", oid).Select("status").Row().Scan(&st)
	db.DB.Model(&model.BillingOrder{}).Where("id = ?", oid).Select("refund_amount_cents").Row().Scan(&amt)
	if st != "paid" || amt != 0 {
		t.Fatalf("拒退后订单应保持 paid 且无退款金额, got status=%s refund=%d", st, amt)
	}
}

// TestRefundPaidMultiPeriodPack 跨期包月（90 天 = 3 期月度额度）：已过整月的期次额度随月
// 重置作废、计为全额消耗，只有"当期剩余 + 未到期数"折成可退份额——
// 第 65 天、当期零消耗 → 可退 1 期/共 3 期 = 29700×1/3 = 9900 分。
// 若按天数比例会退 25/90≈8250；若忽略期数会退全额，两者都不符合"不退已消耗"。
func TestRefundPaidMultiPeriodPack(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	pkg := &model.Package{Code: fmt.Sprintf("ut_p90_%d", time.Now().UnixNano()), Name: "季付包月",
		PType: "paid", TokenAmount: 1000000, PriceCents: 29700, DurationDays: 90, Enabled: true}
	if err := db.DB.Create(pkg).Error; err != nil {
		t.Fatalf("建包失败: %v", err)
	}
	oid := mkPaidSubOrder(t, tid, pkg, 65)
	exp := time.Now().Add(25 * 24 * time.Hour)
	if err := db.DB.Model(&model.Tenant{}).Where("id=?", tid).
		Updates(map[string]any{"expired_at": exp, "monthly_token_quota": pkg.TokenAmount,
			"monthly_token_used": 250000, "status": "active"}).Error; err != nil {
		t.Fatalf("设订阅失败: %v", err)
	}

	o, flowed, err := MarkOrderRefunded(oid)
	if err != nil || !flowed {
		t.Fatalf("季付退款应成功 flowed=%v err=%v", flowed, err)
	}
	// 未消耗 = (3-2)期×100万 - 当期已用25万 = 75万；分母 = 3期×100万 = 300万
	// 应退 = 29700 × 75万/300万 = 7425 分
	if o.RefundAmountCents != 7425 {
		t.Fatalf("季付退款应为7425分(未消耗75万/发放300万), got %d", o.RefundAmountCents)
	}
}

// TestRefundPaidLegacyCountPack 次数制 legacy 包（token_amount=0，无积分维度可核）
// 回退剩余天数比例——新口径不得把这类历史订单一律判成"零可退"而拒掉正常退款。
func TestRefundPaidLegacyCountPack(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	pkg := &model.Package{Code: fmt.Sprintf("ut_plegacy_%d", time.Now().UnixNano()), Name: "legacy次数包",
		PType: "paid", TokenAmount: 0, AICalls: 1000, PriceCents: 9900, DurationDays: 30, Enabled: true}
	if err := db.DB.Create(pkg).Error; err != nil {
		t.Fatalf("建包失败: %v", err)
	}
	oid := mkPaidSubOrder(t, tid, pkg, 10) // 生效 10 天 → 剩 20 天
	exp := time.Now().Add(20 * 24 * time.Hour)
	if err := db.DB.Model(&model.Tenant{}).Where("id=?", tid).
		Updates(map[string]any{"expired_at": exp, "status": "active"}).Error; err != nil {
		t.Fatalf("设订阅失败: %v", err)
	}

	o, flowed, err := MarkOrderRefunded(oid)
	if err != nil || !flowed {
		t.Fatalf("legacy 次数包退款应成功 flowed=%v err=%v", flowed, err)
	}
	if o.RefundAmountCents != 6600 {
		t.Fatalf("legacy 次数包应按天数退6600分(20/30), got %d", o.RefundAmountCents)
	}
}
