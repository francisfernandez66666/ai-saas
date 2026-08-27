// Token 三桶扣减引擎：③免费→①订阅→②余额优先级，灰度双开关与防薅双唯一。
package service

// ============================================================
// Token 三桶扣减引擎（P1.5 Token统一计费，2026-08-26）
//
// 商业模式：token 是唯一硬通货（剃刀-刀片：包免费送，充值收费）
//
// 三桶与扣减优先级（临期/清零资产优先消耗，对客户最有利）：
//   ③ 免费体验桶 FreeTokenBalance（未过期）—— 注册赠送+邀请奖励
//   ① 月度订阅额度 MonthlyTokenQuota-Used —— paid 套餐，月底清零
//   ② 预充值余额 TokenBalance —— 充值包+永久邀请奖励
//   都没有 → 降级规则话术（调用方负责）
//
// 灰度双开关：
//   token_billing_enabled(平台键,默认false) —— 引擎总闸；false=完全不启用（现状兼容）
//   billing_enforced(既有)                  —— false=只记不拦不扣（防误伤语义沿用）
// 旧"次"计量（CheckAIQuota/RecordAICall）并行保留，过渡期双轨，展示层逐步切换。
// ============================================================

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"

	"gorm.io/gorm"
)

// TokenBillingEnabled 引擎总闸（平台级热开关）
func TokenBillingEnabled() bool {
	if DefaultSystemConfigService == nil {
		return false
	}
	return DefaultSystemConfigService.GetBool("token_billing_enabled", false)
}

// billingEnforced 计费强制（false=超额不停服仅留痕，沿用商业化M5灰度语义）
// H4 双开关合一：计费强制必须同时开启 token 引擎总闸，避免"开了 billing_enforced
// 却忘了开 token_billing_enabled"导致三桶从不扣减、灰度语义形同虚设。
func billingEnforced() bool {
	if !TokenBillingEnabled() {
		return false
	}
	return DefaultSystemConfigService.GetBool("billing_enforced", false)
}

// CheckTokenAvailability 前置可用性检查：三桶任一有可用余额即放行。
// 返回 false 时调用方应降级规则话术（不发起模型请求）。
// 未启用引擎 / 未开启强制 → 恒放行（灰度安全）。
func CheckTokenAvailability(tenantID uint) bool {
	if tenantID == 0 || !TokenBillingEnabled() || !billingEnforced() {
		return true
	}
	var t model.Tenant
	if err := db.DB.Select("free_token_balance", "free_token_expires_at",
		"monthly_token_quota", "monthly_token_used", "token_balance").
		First(&t, tenantID).Error; err != nil {
		return true // 读不到租户不阻断主链路（fail-open，扣减侧另有日志）
	}
	now := time.Now()
	freeAvail := int64(0)
	if t.FreeTokenExpiresAt == nil || t.FreeTokenExpiresAt.After(now) {
		freeAvail = t.FreeTokenBalance
	}
	monthlyAvail := t.MonthlyTokenQuota - t.MonthlyTokenUsed
	return freeAvail > 0 || monthlyAvail > 0 || t.TokenBalance > 0
}

// DeductResult 扣减明细（供日志/回执）
type DeductResult struct {
	FromFree    int64 `json:"from_free"`
	FromMonthly int64 `json:"from_monthly"`
	FromBalance int64 `json:"from_balance"`
}

// String 可读输出
func (r DeductResult) String() string {
	return fmt.Sprintf("免费桶%d+月度%d+余额%d", r.FromFree, r.FromMonthly, r.FromBalance)
}

// DeductTokensActual 按实际用量三桶顺序扣减（事务 + 行锁，原子）
// tokens = 本次请求真实消耗（usage.TotalTokens）；任何前置闸门未开时为 no-op。
func DeductTokensActual(tenantID uint, tokens int64) {
	if tenantID == 0 || tokens <= 0 || !TokenBillingEnabled() {
		return
	}
	if !billingEnforced() {
		log.Printf("[TokenBilling] 灰度未强制，仅留痕 tenant=%d tokens=%d", tenantID, tokens)
		return
	}
	err := db.DB.Transaction(func(tx *gorm.DB) error {
		var t model.Tenant
		if err := tx.Set("gorm:query_option", "FOR UPDATE").
			Select("id, free_token_balance, free_token_expires_at, monthly_token_quota, monthly_token_used, token_balance").
			First(&t, tenantID).Error; err != nil {
			return err
		}
		now := time.Now()
		freeAvail := int64(0)
		if t.FreeTokenExpiresAt == nil || t.FreeTokenExpiresAt.After(now) {
			freeAvail = t.FreeTokenBalance
		} else if t.FreeTokenBalance > 0 {
			t.FreeTokenBalance = 0 // 已过期：顺手清零避免僵尸余额
		}
		r := DeductResult{}
		r.FromFree = minInt64(freeAvail, tokens)
		rem := tokens - r.FromFree
		if rem > 0 {
			monthlyAvail := t.MonthlyTokenQuota - t.MonthlyTokenUsed
			if monthlyAvail < 0 {
				monthlyAvail = 0
			}
			r.FromMonthly = minInt64(monthlyAvail, rem)
			rem -= r.FromMonthly
		}
		if rem > 0 {
			r.FromBalance = minInt64(t.TokenBalance, rem)
			rem -= r.FromBalance
		}
		total := r.FromFree + r.FromMonthly + r.FromBalance
		if total <= 0 {
			log.Printf("[TokenBilling] 租户%d 三桶均无余额，本次 %d tokens 未扣（欠账留痕）", tenantID, tokens)
			return nil // 不产生负余额；前置检查本应拦截，此处兜底
		}
		res := tx.Model(&model.Tenant{}).Where("id = ?", tenantID).Updates(map[string]interface{}{
			"free_token_balance": t.FreeTokenBalance - r.FromFree,
			"monthly_token_used": t.MonthlyTokenUsed + r.FromMonthly,
			"token_balance":      t.TokenBalance - r.FromBalance,
		})
		if res.Error != nil {
			return res.Error
		}
		if rem > 0 {
			log.Printf("[TokenBilling] 租户%d 余额不足欠账 %d tokens（建议触发充值触达）", tenantID, rem)
		}
		log.Printf("[TokenBilling] 扣减完成 tenant=%d tokens=%d [%s]", tenantID, tokens, r)
		return nil
	})
	if err != nil {
		log.Printf("[TokenBilling] 扣减失败 tenant=%d tokens=%d: %v", tenantID, tokens, err)
	}
}

// GrantTrialBucket 注册赠送免费桶 —— 防薅v2 双唯一版（2026-08-26）
//
// 领取语义（撞库即拒）：
//
//	账号维度：同一 tenant_id 一生仅一次
//	邮箱维度：同一 email 一生仅一次（换绑新邮箱无法重领；旧邮箱释放后他人也无法重领）
//
// 内测态(无邮箱)仅账号维度兜底。台账 reward_claims + 部分唯一索引硬约束双保险。
func GrantTrialBucket(tx *gorm.DB, tenantID uint, email string) {
	if tx == nil {
		tx = db.DB
	}
	// ---- 撞库检查：任一维度命中即拒 ----
	var cnt int64
	tx.Model(&model.RewardClaim{}).
		Where("grant_type = ? AND tenant_id = ?", model.RewardSignupTrial, tenantID).Count(&cnt)
	if cnt > 0 {
		log.Printf("[TokenBilling] 租户%d 已领过注册礼(账号维度)，跳过", tenantID)
		return
	}
	email = strings.TrimSpace(strings.ToLower(email))
	if email != "" {
		tx.Model(&model.RewardClaim{}).
			Where("grant_type = ? AND email = ?", model.RewardSignupTrial, email).Count(&cnt)
		if cnt > 0 {
			log.Printf("[TokenBilling] 邮箱%s 曾参与注册礼领取，撞库拦截(账号%d)", email, tenantID)
			return
		}
	}
	amount := int64(cfgInt("trial_token_amount", 300000))
	days := cfgInt("trial_token_valid_days", 14)
	expiry := time.Now().AddDate(0, 0, days)
	// 台账先行（部分唯一索引兜底并发）
	if err := tx.Create(&model.RewardClaim{
		GrantType: model.RewardSignupTrial, TenantID: tenantID, Email: email,
	}).Error; err != nil {
		log.Printf("[TokenBilling] 奖励台账写入失败(疑似并发撞库) tenant=%d: %v", tenantID, err)
		return
	}
	res := tx.Model(&model.Tenant{}).
		Where("id = ?", tenantID).
		Updates(map[string]interface{}{
			"free_token_balance":    gorm.Expr("COALESCE(free_token_balance,0)+?", amount),
			"free_token_expires_at": expiry,
		})
	if res.Error != nil {
		log.Printf("[TokenBilling] 注册免费桶发放失败 tenant=%d: %v", tenantID, res.Error)
		return
	}
	log.Printf("[TokenBilling] 注册赠送 %d token(%d天有效) → 租户%d", amount, days, tenantID)
}

// minInt64 取两 int64 较小值（三桶扣减分配时限制单次扣减上限用）
func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// ReportAuditIncrement 把 lastID 之后的租户调参/包操作审计增量回传云端 collector
// （数据飞轮：所有租户实际把参数往哪调 = 行业包迭代的免费标注数据）
// 返回是否成功上报（成功才推进 lastID，失败保留以重试，避免增量审计数据漏传）
func ReportAuditIncrement(lastID uint) bool {
	if DefaultSystemConfigService == nil {
		return false
	}
	url := DefaultSystemConfigService.GetString("feedback_collector_url", "")
	if url == "" {
		return false // 未配置云端地址：关闭
	}
	var rows []map[string]any
	if err := db.DB.Table("tenant_audit_logs").
		Where("id > ? AND (action LIKE ? OR action LIKE ? OR action LIKE ?)",
			lastID, "config%", "rollback", "pack%").
		Limit(500).Find(&rows).Error; err != nil || len(rows) == 0 {
		return false
	}
	payload, _ := json.Marshal(map[string]any{"type": "audit_increment", "items": rows})
	resp, err := http.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		log.Printf("[Feedback] 回流上报失败: %v", err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("[Feedback] 回流上报非成功状态码: %d", resp.StatusCode)
		return false
	}
	log.Printf("[Feedback] 调参/包操作审计增量已回传 %d 条 → %s", len(rows), url)
	return true
}
