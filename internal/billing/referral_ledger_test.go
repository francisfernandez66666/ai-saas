// FIX-1（2026-09-29 审计批）回归锁：付费推荐奖励的**台账行落库失败不得被吞**。
//
// 钉住的事故形态：旧实现 `_ = tx.Create(&RewardClaim{GrantType:"referral_paid"...})`，
// 余额已加而台账缺席时——
//   - ClawbackPaidReferralReward 的回收腿靠 grant_type='referral_paid' 台账行定位
//     "该向谁收多少"，找不到行就只重置闸门不动 token ⇒ 这 50 万**永久收不回**；
//   - 现在改成"台账写不进 = 回冲余额 + 重置闸门 + 上抛"，事务路径整笔回滚、
//     对账器下轮按"paid 缺台账"补发。
//
// 反证口径：把检错改回 `_ =` 吞掉，TestRewardPaidReferral_LedgerFault_* 的
// "err 必回 + 余额必回原状 + 闸门必重置"三条会当场红其中两条（吞错版返回 nil
// 且余额留着）；TestGrantEntitlement_ReferralFaultRollback 在吞错版里台账照发、
// 权益照放、err 为 nil——三条断言全红。注错机器本身有前置自证（TestTeeth）。
package billing

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

var (
	errRefLedgerInjected = errors.New("UT注入：推荐奖励台账写入失败")
	refLedgerFail        atomic.Bool
)

const refLedgerHookName = "ut:ref_ledger_fail"

// installRefLedgerFault 注册"仅 referral_paid 台账行写入失败"的注错回调。
// 必须只打 referral_paid：GrantOrderEntitlement 同事务先写 order_entitlement 台账
// （同为 RewardClaim 行），无差别拦截会把幂等短路也炸掉，测的就不是本缺陷了。
func installRefLedgerFault(t *testing.T) {
	t.Helper()
	if db.DB == nil {
		t.Fatalf("db.DB 未初始化（SetupTestDB 未跑？）")
	}
	if err := db.DB.Callback().Create().Before("gorm:create").Register(refLedgerHookName, func(tx *gorm.DB) {
		if !refLedgerFail.Load() {
			return
		}
		if c, ok := tx.Statement.Dest.(*model.RewardClaim); ok && c.GrantType == "referral_paid" {
			tx.AddError(errRefLedgerInjected) // GORM v2：回调内 AddError 即中止本次 create
		}
	}); err != nil {
		t.Fatalf("注册台账注错回调失败: %v", err)
	}
	t.Cleanup(func() {
		refLedgerFail.Store(false)
		_ = db.DB.Callback().Create().Remove(refLedgerHookName)
	})
}

// mkReferralPair 装配 邀请人+受邀人（绑定关系已落、闸门 false、受邀人有邮箱）。
func mkReferralPair(t *testing.T) (inviterID, invitedID uint) {
	t.Helper()
	inviterID = testutil.CreateTenant(t)
	invitedID = testutil.CreateTenant(t)
	t.Cleanup(func() {
		testutil.CleanupTenant(t, inviterID)
		testutil.CleanupTenant(t, invitedID)
	})
	if err := db.DB.Model(&model.Tenant{}).Where("id = ?", invitedID).Updates(map[string]any{
		"invited_by_tenant_id":   inviterID,
		"referral_paid_rewarded": false,
		"contact_email":          "ut-refledg@example.com",
	}).Error; err != nil {
		t.Fatalf("装配邀请关系失败: %v", err)
	}
	return inviterID, invitedID
}

func refBalance(t *testing.T, tid uint) int64 {
	t.Helper()
	var bal int64
	if err := db.DB.Model(&model.Tenant{}).Where("id = ?", tid).
		Select("COALESCE(token_balance,0)").Scan(&bal).Error; err != nil {
		t.Fatalf("读邀请人余额失败: %v", err)
	}
	return bal
}

func refGate(t *testing.T, tid uint) bool {
	t.Helper()
	var g bool
	if err := db.DB.Model(&model.Tenant{}).Where("id = ?", tid).
		Select("referral_paid_rewarded").Scan(&g).Error; err != nil {
		t.Fatalf("读闸门失败: %v", err)
	}
	return g
}

func refClaims(t *testing.T, invitedID uint) int64 {
	t.Helper()
	var n int64
	if err := db.DB.Model(&model.RewardClaim{}).
		Where("grant_type = ? AND ref_id = ?", "referral_paid", invitedID).Count(&n).Error; err != nil {
		t.Fatalf("数台账行失败: %v", err)
	}
	return n
}

// TestRefLedgerFaultTeeth 前置自证：注错机器真的会炸（本仓反复踩的假绿形态——
// 注入不生效时，下面所有"失败路径"断言都在成功路径上空转）。
func TestRefLedgerFaultTeeth(t *testing.T) {
	testutil.SetupTestDB(t)
	installRefLedgerFault(t)
	_, invitedID := mkReferralPair(t)

	refLedgerFail.Store(true)
	err := db.DB.Create(&model.RewardClaim{
		GrantType: "referral_paid", TenantID: invitedID, RefID: &invitedID, Note: "ut-teeth",
	}).Error
	if err == nil {
		t.Fatalf("注错开关开时 referral_paid 台账写入必须失败（注入没生效=后面全空转）")
	}
	// 开关开也不能误伤别的台账类型（order_entitlement 同表不同 grant_type）
	if err := db.DB.Create(&model.RewardClaim{
		GrantType: model.RewardOrderEntitlement, TenantID: invitedID, RefID: &invitedID, Note: "ut-notarget",
	}).Error; err != nil {
		t.Fatalf("注错不得误伤非 referral_paid 台账: %v", err)
	}
	refLedgerFail.Store(false)
	if err := db.DB.Create(&model.RewardClaim{
		GrantType: "referral_paid", TenantID: invitedID, RefID: &invitedID, Note: "ut-off",
	}).Error; err != nil {
		t.Fatalf("开关关时台账应正常可写: %v", err)
	}
	db.DB.Where("note IN ? AND grant_type = 'referral_paid'", []string{"ut-teeth", "ut-off"}).Delete(&model.RewardClaim{})
	db.DB.Where("note = ? AND grant_type = ?", "ut-notarget", model.RewardOrderEntitlement).Delete(&model.RewardClaim{})
}

// TestRewardPaidReferralPositiveNoFault 正向对照：无注入时三件套同时成立——
// 余额 +bonus、referral_paid 台账在、闸门 true（回收腿从此找得到行）。
func TestRewardPaidReferralPositiveNoFault(t *testing.T) {
	testutil.SetupTestDB(t)
	installRefLedgerFault(t)
	inviterID, invitedID := mkReferralPair(t)

	base := refBalance(t, inviterID)
	if err := RewardPaidReferral(db.DB, invitedID); err != nil {
		t.Fatalf("无注入时发放应成功: %v", err)
	}
	got := refBalance(t, inviterID)
	if got <= base {
		t.Fatalf("余额应增加: base=%d got=%d", base, got)
	}
	if n := refClaims(t, invitedID); n != 1 {
		t.Fatalf("referral_paid 台账应恰 1 行，实得 %d", n)
	}
	if !refGate(t, invitedID) {
		t.Fatalf("发放成功后闸门应置 true")
	}
	// 回收腿可用：台账行能按 ref_id 被 Clawback 定位（金额从 Note 解出）
	var claim model.RewardClaim
	if err := db.DB.Where("grant_type = ? AND ref_id = ?", "referral_paid", invitedID).First(&claim).Error; err != nil {
		t.Fatalf("回收腿按台账定位失败: %v", err)
	}
	var bonus int64
	if _, err := fmt.Sscanf(claim.Note, "bonus:%d", &bonus); err != nil || bonus <= 0 {
		t.Fatalf("台账 Note 应带实发金额，实得 %q", claim.Note)
	}
	db.DB.Delete(&model.RewardClaim{}, claim.ID)
	db.DB.Model(&model.Tenant{}).Where("id = ?", inviterID).
		Update("token_balance", gorm.Expr("COALESCE(token_balance,0) - ?", bonus))
}

// TestRewardPaidReferral_LedgerFault_Compensates 核心反证（非事务兜底路径）：
// 台账写失败 → 必须回冲余额、重置闸门、返回 error；库里不留孤儿余额与假成功。
// 旧写法在这里返回 nil 且余额留着——第一腿（err 必回）就先红。
func TestRewardPaidReferral_LedgerFault_Compensates(t *testing.T) {
	testutil.SetupTestDB(t)
	installRefLedgerFault(t)
	inviterID, invitedID := mkReferralPair(t)

	base := refBalance(t, inviterID)
	refLedgerFail.Store(true)
	err := RewardPaidReferral(db.DB, invitedID)
	if err == nil {
		t.Fatalf("台账写失败必须返回 error（吞错=钱收了、回收腿永久失配）")
	}
	if got := refBalance(t, inviterID); got != base {
		t.Fatalf("台账失败后余额必须回冲原状: base=%d got=%d", base, got)
	}
	if refGate(t, invitedID) {
		t.Fatalf("台账失败后闸门必须重置为 false（否则这笔奖励失去重试资格）")
	}
	if n := refClaims(t, invitedID); n != 0 {
		t.Fatalf("失败路径不得留下台账行，实得 %d", n)
	}
	refLedgerFail.Store(false)
	// 闸门重置后下一笔到账可完整重试发放（同受邀人第二次调用应成功）
	if err := RewardPaidReferral(db.DB, invitedID); err != nil {
		t.Fatalf("故障解除后重试应成功: %v", err)
	}
	if refBalance(t, inviterID) != base+int64(cfgInt("referral_paid_bonus_tokens", 500000)) {
		t.Fatalf("重试成功后余额应为 base+bonus")
	}
}

// TestGrantEntitlement_ReferralFaultRollback 事务路径（生产主链）：
// GrantOrderEntitlement 里 referral 台账写失败 → 整笔发放回滚：
// 权益不放、order_entitlement 台账不留、闸门留 false、余额不动——
// 对账器下轮按"paid 缺台账"补发。吞错版此单会"成功发放"且 err==nil，全红。
func TestGrantEntitlement_ReferralFaultRollback(t *testing.T) {
	testutil.SetupTestDB(t)
	installRefLedgerFault(t)
	inviterID, invitedID := mkReferralPair(t)

	pkg := &model.Package{Code: fmt.Sprintf("ut_refroll_%d", time.Now().UnixNano()), Name: "回收回滚包月",
		PType: model.PackageTypePaid, TokenAmount: 800000, PriceCents: 19900, DurationDays: 30, Enabled: true}
	if err := db.DB.Create(pkg).Error; err != nil {
		t.Fatalf("建包失败: %v", err)
	}
	defer db.DB.Delete(pkg)
	// 订单租户=受邀人（首笔 paid 到账），发放链里才会触发 RewardPaidReferral
	order := mkPaidOrder(t, invitedID, pkg, "paid", 0)

	base := refBalance(t, inviterID)
	var invitedBefore model.Tenant
	if err := db.DB.First(&invitedBefore, invitedID).Error; err != nil {
		t.Fatalf("读受邀人失败: %v", err)
	}

	refLedgerFail.Store(true)
	if err := GrantOrderEntitlement(nil, order); err == nil {
		t.Fatalf("referral 台账写失败时整笔发放必须报错（吞错版会静默'成功'）")
	}
	refLedgerFail.Store(false)

	if got := refBalance(t, inviterID); got != base {
		t.Fatalf("整笔回滚后邀请人余额不得变动: base=%d got=%d", base, got)
	}
	if refGate(t, invitedID) {
		t.Fatalf("整笔回滚后闸门不得滞留 true")
	}
	if n := refClaims(t, invitedID); n != 0 {
		t.Fatalf("回滚后不得残留 referral_paid 台账，实得 %d", n)
	}
	var entCnt int64
	db.DB.Model(&model.RewardClaim{}).Where("grant_type = ? AND ref_id = ?", model.RewardOrderEntitlement, order.ID).Count(&entCnt)
	if entCnt != 0 {
		t.Fatalf("回滚后不得残留 order_entitlement 台账（缺台账正是对账器下一轮的补发信号）")
	}
	var invitedAfter model.Tenant
	if err := db.DB.First(&invitedAfter, invitedID).Error; err != nil {
		t.Fatalf("读受邀人失败: %v", err)
	}
	sameExp := (invitedAfter.ExpiredAt == nil) == (invitedBefore.ExpiredAt == nil) &&
		(invitedAfter.ExpiredAt == nil || invitedAfter.ExpiredAt.Equal(*invitedBefore.ExpiredAt))
	if !sameExp {
		t.Fatalf("回滚后受邀人订阅到期不得被 GrantPackage 顺移（事务回滚必须覆盖权益发放）")
	}
}
