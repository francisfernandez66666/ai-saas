// 邀请推广：注册/邀请/付费三档奖励发放，三桶 token 与幂等闸门防重。
package service

// ============================================================
// 邀请推广服务（M-R，2026-08-25）
//
// 业务规则（多邀多得、单邀单个）：
//   1. 注册赠送：新租户注册即得免费 token（trial_token_amount 默认30万），
//      存入③免费体验桶，有效期 trial_token_valid_days（默认14天）
//   2. 邀请注册奖：受邀人通过邀请码注册成功 → 邀请人免费桶增量叠加
//      （referral_bonus_tokens 默认30万）且有效期顺延（referral_extend_days 默认14天）
//   3. 邀请付费奖：受邀人购买 paid 包月套餐且订单确认到账（首笔）→
//      邀请人获永久 token（referral_paid_bonus_tokens 默认50万，入②余额桶）
//   4. 绑定唯一：受邀租户的 InvitedByTenantID 仅注册时写入一次，重复绑定无效；
//      单个受邀人的付费奖励以 ReferralPaidRewarded 幂等闸门限发一次；
//      邀请人可邀多人，奖励逐人叠加
//
// Token 三桶与扣减优先级见 model/tenant.go 注释；本文件只负责发放，
// 消费扣减在 P1.5 扣费引擎接入（优先级 ③→①→②→降级）。
// ============================================================

import (
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"

	"gorm.io/gorm"
)

// cfgInt 读计费类配置（nil 安全兜底）
func cfgInt(key string, def int) int {
	if DefaultSystemConfigService == nil {
		return def
	}
	return DefaultSystemConfigService.GetInt(key, def)
}

// GenerateInviteCode 生成8位大写字母数字邀请码（去除易混淆字符 0/O/1/I）
func GenerateInviteCode() (string, error) {
	// alphabet 邀请码字符集：去除易混淆字符 0/O/1/I，仅留大写字母数字，降低人工抄错率
	const alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	out := make([]byte, 8)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		out[i] = alphabet[n.Int64()]
	}
	return string(out), nil
}

// EnsureInviteCode 幂等获取租户邀请码：无则生成并落库（存量租户首次访问时补发）
func EnsureInviteCode(tenantID uint) (string, error) {
	var t model.Tenant
	if err := db.DB.Select("id", "invite_code").First(&t, tenantID).Error; err != nil {
		return "", err
	}
	if t.InviteCode != "" {
		return t.InviteCode, nil
	}
	for i := 0; i < 5; i++ { // 唯一索引冲突重试（概率极低）
		code, err := GenerateInviteCode()
		if err != nil {
			return "", err
		}
		res := db.DB.Model(&model.Tenant{}).
			Where("id = ? AND (invite_code IS NULL OR invite_code = '')", tenantID).
			Update("invite_code", code)
		if res.Error != nil {
			continue // 唯一冲突换一个再来
		}
		if res.RowsAffected == 0 { // 并发下别人已生成：读回现值
			var t2 model.Tenant
			db.DB.Select("invite_code").First(&t2, tenantID)
			return t2.InviteCode, nil
		}
		return code, nil
	}
	return "", fmt.Errorf("邀请码生成失败 tenant=%d", tenantID)
}

// ApplyReferralBinding 注册事务内调用：首绑邀请关系 + 双向发放奖励
//   - 受邀租户：写入 InvitedByTenantID（仅当为空——首绑唯一）+ 发放注册免费桶
//   - 邀请租户：免费桶余额叠加 + 有效期顺延
//
// refCode 无效（不存在/自邀/非 trial|active）时静默忽略（只记日志），不阻断注册。
func ApplyReferralBinding(tx *gorm.DB, newTenant *model.Tenant, refCode, inviteeEmail string) {
	refCode = upperTrim(refCode)
	if refCode == "" || newTenant == nil || newTenant.ID == 0 {
		return
	}
	if tx == nil { // 防御式兜底：非事务语境直接走全局连接
		tx = db.DB
	}
	var inviter model.Tenant
	if err := tx.Where("invite_code = ? AND status IN ?", refCode, []string{"active", "trial"}).
		First(&inviter).Error; err != nil {
		log.Printf("[Referral] 邀请码无效或邀请方状态不可用 code=%s（忽略）", refCode)
		return
	}
	if inviter.ID == newTenant.ID {
		return // 自邀防护（理论上 signup 时 ID 未生成，防御式保留）
	}

	trialTokens := int64(cfgInt("trial_token_amount", 300000))
	trialDays := cfgInt("trial_token_valid_days", 14)
	bonusTokens := int64(cfgInt("referral_bonus_tokens", 300000))
	extendDays := cfgInt("referral_extend_days", 14)
	now := time.Now()

	// ---- 受邀人侧：首绑 + 新客免费桶 ----
	newExp := now.AddDate(0, 0, trialDays)
	if err := tx.Model(&model.Tenant{}).
		Where("id = ? AND invited_by_tenant_id IS NULL", newTenant.ID). // 首绑唯一：并发重复邀请只有第一次生效
		Updates(map[string]interface{}{
			"invited_by_tenant_id":  inviter.ID,
			"free_token_balance":    trialTokens,
			"free_token_expires_at": newExp,
		}).Error; err != nil {
		log.Printf("[Referral] 受邀绑定失败 new=%d: %v（不阻断注册）", newTenant.ID, err)
		return
	}
	newTenant.InvitedByTenantID = &inviter.ID

	// ---- 邀请人侧：免费桶叠加 + 到期顺延（从当前到期日起算；已过期/为空则从现在起算）----
	var base time.Time
	if inviter.FreeTokenExpiresAt != nil && inviter.FreeTokenExpiresAt.After(now) {
		base = *inviter.FreeTokenExpiresAt
	} else {
		base = now
	}
	inviterExp := base.AddDate(0, 0, extendDays)
	// 免费桶可能从未初始化过（老租户）：COALESCE 起步；到期日取 max(现值, 新值) 防回退
	if err := tx.Model(&model.Tenant{}).Where("id = ?", inviter.ID).Updates(map[string]interface{}{
		"free_token_balance":    gorm.Expr("COALESCE(free_token_balance,0) + ?", bonusTokens),
		"free_token_expires_at": gorm.Expr("GREATEST(COALESCE(free_token_expires_at, to_timestamp(0)), ?)", inviterExp),
	}).Error; err != nil {
		log.Printf("[Referral] 邀请人奖励发放失败 inviter=%d: %v", inviter.ID, err)
		return
	}

	// 台账：referral_signup 以受邀人为防重锚（多邀多得；单邀单个由首绑+此锚共同保证）
	if err := tx.Create(&model.RewardClaim{
		GrantType: "referral_signup", TenantID: inviter.ID,
		Email: strings.ToLower(strings.TrimSpace(inviteeEmail)), RefID: &newTenant.ID,
	}).Error; err == nil { /* 台账失败不阻断已发放的权益 */
	}

	log.Printf("[Referral] 绑定成功 受邀=%d(%s) ← 邀请人=%d | 新客+%dtoken/%d天 | 邀请人+%dtoken/顺延%d天",
		newTenant.ID, newTenant.Code, inviter.ID, trialTokens, trialDays, bonusTokens, extendDays)
}

// RewardPaidReferral 受邀人首笔 paid 套餐到账后，向其邀请人发放永久 token（②余额桶）
// 在发放事务内调用。幂等三闸门：
//  1. ReferralPaidRewarded 条件 UPDATE（false→true，RowsAffected=1 才继续）——单受邀限一次
//  2. invited_by_tenant_id IS NOT NULL —— 无主不发放
//  3. increment/free 等非 paid 包类型由调用方过滤（业务规则：必须冲套餐）
func RewardPaidReferral(tx *gorm.DB, invitedTenantID uint) {
	if invitedTenantID == 0 {
		return
	}
	if tx == nil { // 调用方可能传 nil（如 api/billing.go mock-pay 直调路径），与 GrantPackage 同款兜底
		tx = db.DB
	}
	gate := tx.Model(&model.Tenant{}).
		Where("id = ? AND invited_by_tenant_id IS NOT NULL AND referral_paid_rewarded = ?", invitedTenantID, false).
		Update("referral_paid_rewarded", true)
	if gate.Error != nil {
		log.Printf("[Referral] 付费奖励幂等闸门更新失败 invited=%d: %v", invitedTenantID, gate.Error)
		return
	}
	if gate.RowsAffected == 0 {
		return // 已发放过 / 无邀请关系：静默跳过（正常路径）
	}

	var invited model.Tenant
	if err := tx.Select("id", "invited_by_tenant_id", "contact_email").First(&invited, invitedTenantID).Error; err != nil {
		return
	}
	inviteeEmailLower := strings.ToLower(strings.TrimSpace(invited.ContactEmail))
	bonus := int64(cfgInt("referral_paid_bonus_tokens", 500000))
	if err := tx.Model(&model.Tenant{}).Where("id = ?", *invited.InvitedByTenantID).
		Update("token_balance", gorm.Expr("COALESCE(token_balance,0) + ?", bonus)).Error; err != nil {
		// 发放失败要可见可追溯：回滚幂等标记，让下次到账重试
		tx.Model(&model.Tenant{}).Where("id = ?", invitedTenantID).Update("referral_paid_rewarded", false)
		log.Printf("[Referral] 付费永久奖励发放失败 invited=%d → inviter=%d: %v（已回滚闸门待重试）",
			invitedTenantID, *invited.InvitedByTenantID, err)
		return
	}
	log.Printf("[Referral] 付费永久奖励到账：受邀=%d 的邀请人=%d +%d token(永久)", invitedTenantID, *invited.InvitedByTenantID, bonus)
	_ = tx.Create(&model.RewardClaim{
		GrantType: "referral_paid", TenantID: *invited.InvitedByTenantID,
		Email: inviteeEmailLower, RefID: &invitedTenantID,
	})
}

// ReferralInfo 邀请信息聚合出参
type ReferralInfo struct {
	InviteCode       string     `json:"invite_code"`
	InvitedCount     int64      `json:"invited_count"`       // 累计成功绑定数
	PaidCount        int64      `json:"paid_count"`          // 其中已完成首笔付费数
	BonusPerSignup   int64      `json:"bonus_per_signup"`    // 参数快照：每邀1人token
	ExtDaysPerSignup int        `json:"ext_days_per_signup"` // 参数快照：每邀1人延长天数
	BonusOnPaid      int64      `json:"bonus_on_paid"`       // 参数快照：受邀人付费后得永久token
	TrialTokenAmount int64      `json:"trial_token_amount"`  // 参数快照：注册赠送
	TrialValidDays   int        `json:"trial_valid_days"`    // 参数快照：免费有效天数
	FreeTokenBalance int64      `json:"free_token_balance"`  // 我的③桶现状
	FreeTokenExpires *time.Time `json:"free_token_expires_at"`
	TokenBalance     int64      `json:"token_balance"` // 我的②桶现状
}

// GetReferralInfo 聚合当前租户的邀请信息（EnsureInviteCode 幂等补码）
func GetReferralInfo(tenantID uint) (*ReferralInfo, error) {
	code, err := EnsureInviteCode(tenantID)
	if err != nil {
		return nil, err
	}
	var t model.Tenant
	if err := db.DB.First(&t, tenantID).Error; err != nil {
		return nil, err
	}
	info := &ReferralInfo{
		InviteCode:       code,
		BonusPerSignup:   int64(cfgInt("referral_bonus_tokens", 300000)),
		ExtDaysPerSignup: cfgInt("referral_extend_days", 14),
		BonusOnPaid:      int64(cfgInt("referral_paid_bonus_tokens", 500000)),
		TrialTokenAmount: int64(cfgInt("trial_token_amount", 300000)),
		TrialValidDays:   cfgInt("trial_token_valid_days", 14),
		FreeTokenBalance: t.FreeTokenBalance,
		FreeTokenExpires: t.FreeTokenExpiresAt,
		TokenBalance:     t.TokenBalance,
	}
	db.DB.Model(&model.Tenant{}).Where("invited_by_tenant_id = ?", tenantID).Count(&info.InvitedCount)
	db.DB.Model(&model.Tenant{}).Where("invited_by_tenant_id = ? AND referral_paid_rewarded = ?", tenantID, true).Count(&info.PaidCount)
	return info, nil
}

// upperTrim 邀请码归一化（去空白+转大写）
func upperTrim(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		if c >= 'a' && c <= 'z' {
			c -= 32
		}
		out = append(out, c)
	}
	return string(out)
}
