// Package model 业务领域模型与 GORM 表结构定义（SaaS 多租户，tenant_id 隔离，写入经自动盖章回调）。
package model

import (
	"time"
)

// ============================================================
// 跟进记录模型 - 销售跟进客户的记录
// 为什么需要？人机协作中人工介入的操作记录，也是数据来源
// ============================================================

// FollowUp 跟进记录表
type FollowUp struct {
	ID             uint       `gorm:"primaryKey" json:"id"` // 注释：主键ID
	TenantID       uint       `gorm:"index;not null;default:0" json:"tenant_id"` // 租户ID（SaaS多租户隔离）
	CustomerID     uint       `gorm:"index;not null" json:"customer_id"`         // 客户ID
	ConversationID uint       `gorm:"index" json:"conversation_id"`              // 关联会话ID
	UserID         uint       `gorm:"index;not null" json:"user_id"`             // 跟进人ID
	Type           string     `gorm:"size:20;default:manual" json:"type"`        // 类型: manual(手动)/ai_triggered(AI触发)
	Method         string     `gorm:"size:20" json:"method"`                     // 方式: phone/wechat/store/email
	Content        string     `gorm:"type:text" json:"content"`                  // 跟进内容
	Result         string     `gorm:"size:50" json:"result"`                     // 跟进结果
	NextFollowAt   *time.Time `json:"next_follow_at"`                            // 下次跟进时间
	IntentChange   float64    `json:"intent_change"`                             // 意向分变化
	CreatedAt      time.Time  `json:"created_at"` // 注释：创建时间
	UpdatedAt      time.Time  `json:"updated_at"` // 注释：更新时间
}

// TableName 指定表名
func (FollowUp) TableName() string {
	return "follow_ups"
}
