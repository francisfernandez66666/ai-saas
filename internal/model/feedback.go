// Package model 业务领域模型与 GORM 表结构定义（SaaS 多租户，tenant_id 隔离，写入经自动盖章回调）。
package model

import (
	"time"
)

// ============================================================
// 用户反馈（商业化第二批 M2，借鉴翻译助手四期 §五）
//
// 场景：顾问对 AI 话术一键反馈（可附带脱敏上下文），直达超管处理；
// 新反馈触发企微群催办；20条/天/用户限流防骚扰
// ============================================================

// Feedback 反馈表
type Feedback struct {
	ID         uint       `gorm:"primaryKey" json:"id"`                        // 主键
	TenantID   uint       `gorm:"index" json:"-"`                              // 租户
	UserID     uint       `gorm:"index" json:"user_id"`                        // 提交人
	TargetType string     `gorm:"size:20;default:ai_reply" json:"target_type"` // ai_reply|feature|other
	RefID      uint       `json:"ref_id"`                                      // 关联对象ID（AI消息ID等，可0）
	Context    string     `gorm:"type:text" json:"context"`                    // JSON 上下文（勾选同意才写入，手机号已脱敏）
	Content    string     `gorm:"type:text;not null" json:"content"`           // 意见 ≤1000字
	Status     string     `gorm:"size:20;default:open;index" json:"status"`    // open|resolved
	HandleNote string     `gorm:"type:text" json:"handle_note"`                // 处理备注
	HandledBy  uint       `json:"handled_by"`                                  // 处理人
	CreatedAt  time.Time  `json:"created_at"`                                  // 注释：创建时间
	HandledAt  *time.Time `json:"handled_at"`                                  // 注释：处理时间
}

// TableName 指定表名
func (Feedback) TableName() string {
	return "feedbacks"
}
