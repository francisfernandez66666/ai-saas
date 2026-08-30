// Package model 业务领域模型与 GORM 表结构定义（SaaS 多租户，tenant_id 隔离，写入经自动盖章回调）。
package model

// ============================================================
// 奖励领取台账（P1.5 防薅v2，2026-08-26）
//
// 身份语义（定稿）：
//   账户层：tenant.id 主键不可变；email 是可换绑的"外键"（验证新旧邮箱后可改）
//   奖励层：id 与 email 双唯一——撞库一次（任一维度曾领取过同类型奖励）即不再发放
//
// 堵住的套利路径：
//   1. 同账号换绑新邮箱后再领 → tenant_id 维度唯一拦截
//   2. 旧邮箱释放后被他人注册新号再领 → email 维度唯一拦截
//   3. 同一受邀人反复触发邀请付费奖 → (grant_type, ref_tenant_id) 唯一拦截
// ============================================================

import (
	"time"
)

// RewardClaim 奖励领取记录
type RewardClaim struct {
	ID        uint      `gorm:"primaryKey" json:"id"`                            // 注释：主键ID
	GrantType string    `gorm:"size:30;index:idx_reward_type" json:"grant_type"` // signup_trial / referral_signup / referral_paid
	TenantID  uint      `gorm:"index" json:"tenant_id"`                          // 受益账户
	Email     string    `gorm:"size:100;index:idx_reward_email" json:"email"`    // 参与邮箱（外键维度；内测态可空）
	RefID     *uint     `gorm:"index" json:"ref_id"`                             // 关联对方（受邀人ID）——referral类防重锚
	Note      string    `gorm:"size:255" json:"note"`                            // 注释：备注
	CreatedAt time.Time `json:"created_at"`                                      // 注释：创建时间
}

// TableName 指定表名
func (RewardClaim) TableName() string { return "reward_claims" }

// 奖励类型枚举
const (
	RewardSignupTrial = "signup_trial" // 注册赠送③免费桶
)
