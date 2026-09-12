// 商业化资金安全修复批次（2026-09-11/12）回归单测：
//   - R10 发放台账：GrantOrderEntitlement 台账先行，重复调用幂等不二次发放
//   - R2  对账器：以"paid 缺台账"为唯一补发信号（旧 expired_at IS NULL 判定对增量单恒假阳性=白送）
//   - R5  迟到到账：closed 单仅真钱通道可复活，mock 一律拒绝，二次复活幂等
//   - R7  推荐奖励回收：受邀人付费单退款后邀请人 bonus 扣回、台账删除、闸门重置
//   - R9  受邀注册礼：与 GrantTrialBucket 同走 signup_trial 台账，同邮箱二次受邀不双发
//   - R11 升级防并发：同一基数单不允许并存两笔 pending 升级抵扣单
//   - R16 计量缓冲：UsageSink 超上限丢弃留痕，不无界堆积
package service

import (
	"fmt"
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"

	"gorm.io/gorm"
)

// mkPaidOrder 直接落一笔指定状态的订单（绕过支付流程，聚焦发放/对账/退款逻辑本身）
func mkPaidOrder(t *testing.T, tid uint, pkg *model.Package, status string, backdate time.Duration) *model.BillingOrder {
	t.Helper()
	o := &model.BillingOrder{
		OrderNo: fmt.Sprintf("UTLGR%d", time.Now().UnixNano()), TenantID: &tid,
		PackageID: pkg.ID, AmountCents: pkg.PriceCents, OriginalAmountCents: pkg.PriceCents,
		Status: status, Channel: "mock", Period: "once",
	}
	if err := db.DB.Create(o).Error; err != nil {
		t.Fatalf("建订单失败: %v", err)
	}
	if backdate > 0 {
		if err := db.DB.Model(&model.BillingOrder{}).Where("id = ?", o.ID).
			Update("created_at", time.Now().Add(-backdate)).Error; err != nil {
			t.Fatalf("回填 created_at 失败: %v", err)
		}
	}
	return o
}

// TestGrantEntitlementLedgerIdempotent R10：同一订单重复发放只生效一次（台账唯一索引短路）
func TestGrantEntitlementLedgerIdempotent(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	pkg := &model.Package{Code: fmt.Sprintf("ut_lgr_%d", time.Now().UnixNano()), Name: "台账增量包",
		PType: model.PackageTypeIncrement, TokenAmount: 1000000, PriceCents: 9900, Enabled: true}
	if err := db.DB.Create(pkg).Error; err != nil {
		t.Fatalf("建包失败: %v", err)
	}
	order := mkPaidOrder(t, tid, pkg, "paid", 0)

	for i := 0; i < 3; i++ {
		if err := GrantOrderEntitlement(nil, order); err != nil {
			t.Fatalf("第%d次发放应成功（幂等短路不算错）: %v", i+1, err)
		}
	}
	var tnt model.Tenant
	if err := db.DB.First(&tnt, tid).Error; err != nil {
		t.Fatalf("读租户失败: %v", err)
	}
	if tnt.TokenBalance != 1000000 {
		t.Fatalf("重复发放3次②桶应只入100万, got %d", tnt.TokenBalance)
	}
	var claims int64
	db.DB.Model(&model.RewardClaim{}).Where("grant_type = ? AND ref_id = ?", model.RewardOrderEntitlement, order.ID).Count(&claims)
	if claims != 1 {
		t.Fatalf("order_entitlement 台账应恰1行, got %d", claims)
	}
}

// TestReconcileBillingLedgerDriven R2：对账只补"paid 缺台账"，已发放（有台账）绝不重发
func TestReconcileBillingLedgerDriven(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	pkg := &model.Package{Code: fmt.Sprintf("ut_rec_%d", time.Now().UnixNano()), Name: "对账增量包",
		PType: model.PackageTypeIncrement, TokenAmount: 2000000, PriceCents: 19900, Enabled: true}
	if err := db.DB.Create(pkg).Error; err != nil {
		t.Fatalf("建包失败: %v", err)
	}

	// 场景A：paid 但发放未落地（缺台账，且超 10 分钟在途窗口）→ 对账补发一次
	missed := mkPaidOrder(t, tid, pkg, "paid", 30*time.Minute)
	// 场景B：paid 且台账已存在（正常单）→ 对账必须跳过（旧实现按 expired_at IS NULL 判定
	// 对 increment 单恒假阳性，每轮白送一份额度——本用例即该 bug 的回归锁）
	granted := mkPaidOrder(t, tid, pkg, "paid", 30*time.Minute)
	if err := GrantOrderEntitlement(nil, granted); err != nil {
		t.Fatalf("场景B预发放失败: %v", err)
	}

	ReconcileBilling()

	var tnt model.Tenant
	if err := db.DB.First(&tnt, tid).Error; err != nil {
		t.Fatalf("读租户失败: %v", err)
	}
	// B 预发放 200万 + A 对账补发 200万 = 400万；若对账器误重发 B 则为 600万
	if tnt.TokenBalance != 4000000 {
		t.Fatalf("对账后②桶应=预发200万+补发200万=400万（已发放单不得重发）, got %d", tnt.TokenBalance)
	}
	var claims int64
	db.DB.Model(&model.RewardClaim{}).Where("grant_type = ? AND ref_id IN ?",
		model.RewardOrderEntitlement, []uint{missed.ID, granted.ID}).Count(&claims)
	if claims != 2 {
		t.Fatalf("两单应各恰1条台账, got %d", claims)
	}

	// 再跑一轮：全部有台账 → 零动作
	before := tnt.TokenBalance
	if n := ReconcileBilling(); n != 0 {
		t.Logf("对账第二轮补发 %d 笔（可能含其他测试残留单），校验本租户余额不变", n)
	}
	if err := db.DB.First(&tnt, tid).Error; err != nil {
		t.Fatalf("读租户失败: %v", err)
	}
	if tnt.TokenBalance != before {
		t.Fatalf("第二轮对账不得再动本租户余额: before=%d after=%d", before, tnt.TokenBalance)
	}
}

// TestReopenClosedOrderPaid R5：迟到到账复活——mock 拒绝、真钱通道放行且幂等
func TestReopenClosedOrderPaid(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	pkg := &model.Package{Code: fmt.Sprintf("ut_rpn_%d", time.Now().UnixNano()), Name: "迟到到账包",
		PType: model.PackageTypeIncrement, TokenAmount: 500000, PriceCents: 9900, Enabled: true}
	if err := db.DB.Create(pkg).Error; err != nil {
		t.Fatalf("建包失败: %v", err)
	}
	order := mkPaidOrder(t, tid, pkg, "closed", 0)

	// mock 通道（自助接口）绝不允许复活——否则 0 元白嫖
	if _, ok, err := ReopenClosedOrderPaid(order.ID, "mock"); err != nil || ok {
		t.Fatalf("mock 通道复活必须拒绝: ok=%v err=%v", ok, err)
	}
	// manual（超管人工核实到账）允许复活一次
	o, ok, err := ReopenClosedOrderPaid(order.ID, "manual")
	if err != nil || !ok {
		t.Fatalf("manual 通道应复活成功: ok=%v err=%v", ok, err)
	}
	if o.Status != "paid" || o.PaidAt == nil {
		t.Fatalf("复活后应为 paid 且有 paid_at, got %s", o.Status)
	}
	// 二次复活（并发回调场景）幂等拒绝
	if _, ok2, _ := ReopenClosedOrderPaid(order.ID, "manual"); ok2 {
		t.Fatalf("二次复活必须幂等拒绝")
	}
}

// TestClawbackPaidReferralReward R7：受邀人付费单退款 → 邀请人付费推荐奖回收；
// 仍有其它生效付费单时不回收
func TestClawbackPaidReferralReward(t *testing.T) {
	testutil.SetupTestDB(t)
	// 必须 CreateTenantCode 区分（CreateTenant 进程内复用同一租户，会触发自邀防护）
	inviterID := testutil.CreateTenantCode(t, "ut_inviter")
	invitedID := testutil.CreateTenantCode(t, "ut_invited")
	defer testutil.CleanupTenant(t, inviterID)
	defer testutil.CleanupTenant(t, invitedID)

	pkg := &model.Package{Code: fmt.Sprintf("ut_cb_%d", time.Now().UnixNano()), Name: "回收包月",
		PType: model.PackageTypePaid, TokenAmount: 1000000, PriceCents: 9900, DurationDays: 30, Enabled: true}
	if err := db.DB.Create(pkg).Error; err != nil {
		t.Fatalf("建包失败: %v", err)
	}
	// 装配：受邀人绑定邀请人+闸门已置+邀请人已得 50 万永久奖（模拟 RewardPaidReferral 落账后）
	if err := db.DB.Model(&model.Tenant{}).Where("id = ?", invitedID).Updates(map[string]any{
		"invited_by_tenant_id": inviterID, "referral_paid_rewarded": true,
	}).Error; err != nil {
		t.Fatalf("设绑定失败: %v", err)
	}
	if err := db.DB.Model(&model.Tenant{}).Where("id = ?", inviterID).
		Update("token_balance", 800000).Error; err != nil {
		t.Fatalf("设邀请人余额失败: %v", err)
	}
	invited := model.Tenant{ID: invitedID}
	claim := model.RewardClaim{GrantType: "referral_paid", TenantID: inviterID,
		RefID: &invitedID, Note: "bonus:500000"}
	if err := db.DB.Create(&claim).Error; err != nil {
		t.Fatalf("建推荐奖台账失败: %v", err)
	}

	// 分支1：受邀人仍有其它 paid 包月单 → 触发事实未消失，不回收
	paidAt := time.Now().Add(-time.Hour)
	keep := &model.BillingOrder{OrderNo: fmt.Sprintf("UTCBK%d", time.Now().UnixNano()),
		TenantID: &invitedID, PackageID: pkg.ID, AmountCents: 9900, Status: "paid",
		Channel: "mock", Period: "monthly", PaidAt: &paidAt}
	if err := db.DB.Create(keep).Error; err != nil {
		t.Fatalf("建保留单失败: %v", err)
	}
	db.DB.First(&invited, invitedID)
	ClawbackPaidReferralReward(db.DB, invited)
	var inv model.Tenant
	db.DB.First(&inv, inviterID)
	if inv.TokenBalance != 800000 {
		t.Fatalf("仍有其它付费单时不得回收, got %d", inv.TokenBalance)
	}
	db.DB.Delete(&model.BillingOrder{}, keep.ID)

	// 分支2：已无任何 paid 单 → 回收 50 万 + 删台账 + 重置闸门
	db.DB.First(&invited, invitedID)
	ClawbackPaidReferralReward(db.DB, invited)
	if err := db.DB.First(&inv, inviterID).Error; err != nil {
		t.Fatalf("读邀请人失败: %v", err)
	}
	if inv.TokenBalance != 300000 {
		t.Fatalf("回收后邀请人②桶应 80万-50万=30万, got %d", inv.TokenBalance)
	}
	var claims int64
	db.DB.Model(&model.RewardClaim{}).Where("grant_type = ? AND ref_id = ?", "referral_paid", invitedID).Count(&claims)
	if claims != 0 {
		t.Fatalf("回收后 referral_paid 台账应删除, got %d", claims)
	}
	var t2 model.Tenant
	db.DB.First(&t2, invitedID)
	if t2.ReferralPaidRewarded {
		t.Fatalf("回收后幂等闸门应重置（再付费可再获奖）")
	}
}

// TestReferralBindingTrialLedgerOnce R9：受邀注册礼走 signup_trial 台账——
// 同邮箱二次受邀开新站不再拿第二份免费桶
func TestReferralBindingTrialLedgerOnce(t *testing.T) {
	testutil.SetupTestDB(t)
	// 必须 CreateTenantCode 区分（CreateTenant 进程内复用同一租户，会触发自邀防护）
	inviterID := testutil.CreateTenantCode(t, "ut_ref_inviter")
	firstID := testutil.CreateTenantCode(t, "ut_ref_first")
	secondID := testutil.CreateTenantCode(t, "ut_ref_second")
	defer testutil.CleanupTenant(t, inviterID)
	defer testutil.CleanupTenant(t, firstID)
	defer testutil.CleanupTenant(t, secondID)

	var inviter model.Tenant
	if err := db.DB.First(&inviter, inviterID).Error; err != nil {
		t.Fatalf("读邀请人失败: %v", err)
	}
	dupEmail := "ut_dup_ref@example.test"

	bind := func(newTID uint) {
		var nt model.Tenant
		if err := db.DB.First(&nt, newTID).Error; err != nil {
			t.Fatalf("读受邀租户失败: %v", err)
		}
		ApplyReferralBinding(nil, &nt, inviter.InviteCode, dupEmail)
	}
	bind(firstID)
	bind(secondID)

	var t1, t2 model.Tenant
	db.DB.First(&t1, firstID)
	db.DB.First(&t2, secondID)
	if t1.FreeTokenBalance != 300000 {
		t.Fatalf("首位受邀人应得 30万注册礼, got %d", t1.FreeTokenBalance)
	}
	if t2.FreeTokenBalance != 0 {
		t.Fatalf("同邮箱二次受邀不得再得注册礼, got %d", t2.FreeTokenBalance)
	}
	// 绑定关系本身仍生效（首绑唯一按租户维度，不受邮箱台账影响）
	if t2.InvitedByTenantID == nil || *t2.InvitedByTenantID != inviterID {
		t.Fatalf("二次受邀的绑定关系应仍成立")
	}
}

// TestUpgradeOrderRejectsConcurrentBase R11：同一基数单不允许并存两笔 pending 升级抵扣单
func TestUpgradeOrderRejectsConcurrentBase(t *testing.T) {
	testutil.SetupTestDB(t)
	tid, _, baseID := buildUpgradeTenant(t)
	defer testutil.CleanupTenant(t, tid)

	newPkg := &model.Package{Code: fmt.Sprintf("ut_updup_%d", time.Now().UnixNano()), Name: "升级新包",
		PType: model.PackageTypePaid, TokenAmount: 3000000, PriceCents: 19900, DurationDays: 30, Enabled: true}
	if err := db.DB.Create(newPkg).Error; err != nil {
		t.Fatalf("建包失败: %v", err)
	}
	// 第一笔升级单：正常创建（ReplaceSub=true，基数=baseID）
	o1, err := CreateOrderForPackage(tid, newPkg)
	if err != nil {
		t.Fatalf("首笔升级单创建失败: %v", err)
	}
	if !o1.ReplaceSub || o1.UpgradeBaseOrderID != baseID {
		t.Fatalf("首笔应为升级抵扣单（基数=%d）, got replace=%v base=%d", baseID, o1.ReplaceSub, o1.UpgradeBaseOrderID)
	}
	// 第二笔并发升级单：同一基数已有 pending 升级引用 → 必须拒绝（防同一份剩余价值抵扣两次）
	if _, err := CreateOrderForPackage(tid, newPkg); err == nil {
		t.Fatalf("同基数第二笔 pending 升级单应被拒绝")
	} else {
		t.Logf("第二笔按预期拒绝: %v", err)
	}
}

// TestUsageSinkBufferCap R16：缓冲超硬上限丢弃留痕，不无界堆积（DB 长故障防 OOM）
func TestUsageSinkBufferCap(t *testing.T) {
	s := DefaultUsageSink
	s.mu.Lock()
	origBuf, origDropped := s.maxBuf, s.dropped
	s.buf = nil
	s.maxBuf = 5
	s.dropped = 0
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.maxBuf, s.dropped = origBuf, origDropped
		s.buf = nil
		s.mu.Unlock()
	}()

	for i := 0; i < 50; i++ {
		s.Record(usageSinkRecord{Tid: uint(999999 + i), Tokens: 10})
	}
	s.mu.Lock()
	n, d := len(s.buf), s.dropped
	s.mu.Unlock()
	if n > 5 {
		t.Fatalf("缓冲应封顶在 maxBuf=5, got %d", n)
	}
	if d != 45 {
		t.Fatalf("超出部分应计数丢弃 45 条, got %d", d)
	}
}

// 编译期防呆：确保测试文件引用 gorm 包（部分分支用不到时也不报 unused）
var _ = gorm.ErrRecordNotFound
