// 换包升级差额抵扣单测（2026-09-09）：
//   - 已有生效付费订阅 + 换订不同 paid 包 → 差额订单抵扣（应付=新价-旧剩余价值，可为0）
//   - 新包从今天起算生效（GrantPackageUpgrade 替换语义，不顺延旧到期日）
//   - 同包续订不抵扣（走原顺延语义）
//   - 无生效订阅 = 全新购买全价
//   - 旧单被升级抵扣后不可再退（防「退旧单+白拿新包」双重回收）
package service

import (
	"fmt"
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// buildUpgradeTenant 构造升级场景：租户已购旧 paid 包并生效（expired_at=now+30天）
func buildUpgradeTenant(t *testing.T) (uint, *model.Package, uint) {
	t.Helper()
	tid := testutil.CreateTenant(t)

	oldPkg := &model.Package{Code: fmt.Sprintf("ut_old_%d", time.Now().UnixNano()), Name: "旧包月",
		PType: model.PackageTypePaid, TokenAmount: 1000000, PriceCents: 9900, DurationDays: 30, Enabled: true}
	if err := db.DB.Create(oldPkg).Error; err != nil {
		t.Fatalf("建旧包失败: %v", err)
	}
	paidAt := time.Now().Add(-24 * time.Hour)
	oldOrder := &model.BillingOrder{
		OrderNo: fmt.Sprintf("UTUPG%d", time.Now().UnixNano()), TenantID: &tid,
		PackageID: oldPkg.ID, AmountCents: oldPkg.PriceCents,
		OriginalAmountCents: oldPkg.PriceCents, Status: "paid",
		Channel: "mock", Period: "monthly", PaidAt: &paidAt,
	}
	if err := db.DB.Create(oldOrder).Error; err != nil {
		t.Fatalf("建旧订单失败: %v", err)
	}
	// 旧订阅生效：expired_at=now+30天，月配额=旧包额度
	exp := time.Now().AddDate(0, 0, 30)
	if err := db.DB.Model(&model.Tenant{}).Where("id = ?", tid).Updates(map[string]any{
		"expired_at": exp, "monthly_token_quota": oldPkg.TokenAmount, "status": "active",
	}).Error; err != nil {
		t.Fatalf("设置旧订阅生效失败: %v", err)
	}
	return tid, oldPkg, oldOrder.ID
}

// TestUpgradeOffsetDifferential 升级差额抵扣：新价 > 旧剩余价值 → 应付差额
func TestUpgradeOffsetDifferential(t *testing.T) {
	testutil.SetupTestDB(t)
	tid, _, _ := buildUpgradeTenant(t)
	defer testutil.CleanupTenant(t, tid)

	newPkg := &model.Package{Code: fmt.Sprintf("ut_new_%d", time.Now().UnixNano()), Name: "新包月",
		PType: model.PackageTypePaid, TokenAmount: 3000000, PriceCents: 19900, DurationDays: 30, Enabled: true}
	if err := db.DB.Create(newPkg).Error; err != nil {
		t.Fatalf("建新包失败: %v", err)
	}

	order, err := CreateOrderForPackage(tid, newPkg)
	if err != nil {
		t.Fatalf("下单失败: %v", err)
	}
	if !order.ReplaceSub {
		t.Fatalf("换包升级订单应置 ReplaceSub=true")
	}
	if order.UpgradeBaseOrderID == 0 {
		t.Fatalf("升级订单应记录旧单 ID")
	}
	// 旧包剩余价值 = 9900 × 30/30 = 9900 分；应付 = 19900 - 9900 = 10000 分
	if order.AmountCents != 10000 {
		t.Fatalf("应付应为差额 10000 分, got %d (offset=%d)", order.AmountCents, order.UpgradeOffsetCents)
	}
	if order.UpgradeOffsetCents != 9900 {
		t.Fatalf("抵扣应为 9900 分, got %d", order.UpgradeOffsetCents)
	}
	if order.OriginalAmountCents != 19900 {
		t.Fatalf("原价应保留 19900 分, got %d", order.OriginalAmountCents)
	}
}

// TestUpgradeOffsetZeroOrLess 升级差额抵扣：新价 ≤ 旧剩余价值 → 应付 0（不多退现金）
func TestUpgradeOffsetZeroOrLess(t *testing.T) {
	testutil.SetupTestDB(t)
	tid, _, _ := buildUpgradeTenant(t)
	defer testutil.CleanupTenant(t, tid)

	cheapPkg := &model.Package{Code: fmt.Sprintf("ut_cheap_%d", time.Now().UnixNano()), Name: "更便宜包月",
		PType: model.PackageTypePaid, TokenAmount: 500000, PriceCents: 5000, DurationDays: 30, Enabled: true}
	if err := db.DB.Create(cheapPkg).Error; err != nil {
		t.Fatalf("建便宜包失败: %v", err)
	}
	order, err := CreateOrderForPackage(tid, cheapPkg)
	if err != nil {
		t.Fatalf("下单失败: %v", err)
	}
	if !order.ReplaceSub {
		t.Fatalf("换包升级订单应置 ReplaceSub=true")
	}
	if order.AmountCents != 0 {
		t.Fatalf("新价(5000) ≤ 旧剩余价值(9900) 应付应为 0, got %d", order.AmountCents)
	}
	if order.UpgradeOffsetCents != 9900 {
		t.Fatalf("抵扣应记录完整旧剩余价值 9900, got %d", order.UpgradeOffsetCents)
	}
}

// TestUpgradeSamePackageRenewal 同包续订：不抵扣，走原顺延语义（全额订单 + ReplaceSub=false）
func TestUpgradeSamePackageRenewal(t *testing.T) {
	testutil.SetupTestDB(t)
	tid, oldPkg, _ := buildUpgradeTenant(t)
	defer testutil.CleanupTenant(t, tid)

	order, err := CreateOrderForPackage(tid, oldPkg)
	if err != nil {
		t.Fatalf("下单失败: %v", err)
	}
	if order.ReplaceSub {
		t.Fatalf("同包续订不应置 ReplaceSub")
	}
	if order.AmountCents != oldPkg.PriceCents {
		t.Fatalf("同包续订应全价, got %d want %d", order.AmountCents, oldPkg.PriceCents)
	}
}

// TestUpgradeGrantReplacesExpiry 换包升级发放：新包从今天起算（替换语义），而非旧到期日顺延
func TestUpgradeGrantReplacesExpiry(t *testing.T) {
	testutil.SetupTestDB(t)
	tid, oldPkg, _ := buildUpgradeTenant(t)
	defer testutil.CleanupTenant(t, tid)

	newPkg := &model.Package{Code: fmt.Sprintf("ut_new_grant_%d", time.Now().UnixNano()), Name: "新包月",
		PType: model.PackageTypePaid, TokenAmount: 3000000, PriceCents: 19900, DurationDays: 60, Enabled: true}
	if err := db.DB.Create(newPkg).Error; err != nil {
		t.Fatalf("建新包失败: %v", err)
	}
	order, err := CreateOrderForPackage(tid, newPkg)
	if err != nil {
		t.Fatalf("下单失败: %v", err)
	}
	// 到账 → 发放（走替换语义）
	if err := GrantOrderEntitlement(nil, order); err != nil {
		t.Fatalf("发放失败: %v", err)
	}
	var tnt model.Tenant
	if err := db.DB.First(&tnt, tid).Error; err != nil {
		t.Fatalf("读租户失败: %v", err)
	}
	// 旧到期日 = now+30，新包 60 天若顺延则 ≈ now+90；替换语义应 ≈ now+60
	expected := time.Now().AddDate(0, 0, 60)
	if tnt.ExpiredAt == nil {
		t.Fatalf("发放后应有到期日")
	}
	if d := tnt.ExpiredAt.Sub(expected); d < -2*time.Hour || d > 2*time.Hour {
		t.Fatalf("换包升级应从今天起算60天, got %v (expect≈%v)", tnt.ExpiredAt, expected)
	}
	if tnt.MonthlyTokenQuota != newPkg.TokenAmount {
		t.Fatalf("月配额应切换为新包额度 %d, got %d", newPkg.TokenAmount, tnt.MonthlyTokenQuota)
	}
	// 旧包不再是基础：不应再顺延（验证替换）
	_ = oldPkg
}

// TestUpgradeOldOrderNotRefundable 升级后被抵扣的旧订单不可再退（防双重回收）
func TestUpgradeOldOrderNotRefundable(t *testing.T) {
	testutil.SetupTestDB(t)
	tid, _, oldOrderID := buildUpgradeTenant(t)
	defer testutil.CleanupTenant(t, tid)

	newPkg := &model.Package{Code: fmt.Sprintf("ut_new_nr_%d", time.Now().UnixNano()), Name: "新包月",
		PType: model.PackageTypePaid, TokenAmount: 3000000, PriceCents: 19900, DurationDays: 30, Enabled: true}
	if err := db.DB.Create(newPkg).Error; err != nil {
		t.Fatalf("建新包失败: %v", err)
	}
	order, err := CreateOrderForPackage(tid, newPkg)
	if err != nil {
		t.Fatalf("下单失败: %v", err)
	}
	// 新单到账并发放
	order, _, err = MarkOrderPaid(order.ID, "mock")
	if err != nil {
		t.Fatalf("到账失败: %v", err)
	}
	if err := GrantOrderEntitlement(nil, order); err != nil {
		t.Fatalf("发放失败: %v", err)
	}
	// 旧订单（升级抵扣基础）退款必须被拒绝（ErrRefundNoRemaining 或 状态机拒绝）
	_, flowed, err := MarkOrderRefunded(oldOrderID)
	if err == nil || flowed {
		t.Fatalf("被升级抵扣的旧单退款必须被拒绝, err=%v flowed=%v", err, flowed)
	}
}

// TestUpgradeIncrementNoOffset 增量包（increment）不参与换包抵扣
func TestUpgradeIncrementNoOffset(t *testing.T) {
	testutil.SetupTestDB(t)
	tid, _, _ := buildUpgradeTenant(t)
	defer testutil.CleanupTenant(t, tid)

	incPkg := &model.Package{Code: fmt.Sprintf("ut_inc_%d", time.Now().UnixNano()), Name: "加油包",
		PType: model.PackageTypeIncrement, TokenAmount: 500000, PriceCents: 9900, DurationDays: 0, Enabled: true}
	if err := db.DB.Create(incPkg).Error; err != nil {
		t.Fatalf("建增量包失败: %v", err)
	}
	order, err := CreateOrderForPackage(tid, incPkg)
	if err != nil {
		t.Fatalf("下单失败: %v", err)
	}
	if order.ReplaceSub || order.UpgradeOffsetCents != 0 {
		t.Fatalf("增量包不应参与换包抵扣 (ReplaceSub=%v offset=%d)", order.ReplaceSub, order.UpgradeOffsetCents)
	}
	if order.AmountCents != incPkg.PriceCents {
		t.Fatalf("增量包应全价, got %d", order.AmountCents)
	}
}
