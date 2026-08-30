// Package model 业务领域模型与 GORM 表结构定义（SaaS 多租户，tenant_id 隔离，写入经自动盖章回调）。
package model

import (
	"time"
)

// ============================================================
// 消息中心幂等与审计（SAAS_PLAN §2.5）
// ============================================================

// InboxEvent 收件箱幂等表：event_id 唯一，防重复消费
// 消费侧先 EnsureProcessed 抢占，处理失败可标记重试
type InboxEvent struct {
	EventID     string    `gorm:"primaryKey;size:64" json:"event_id"` // 事件唯一ID（来自 Header）
	TenantID    uint      `gorm:"index;not null;default:0" json:"tenant_id"` // 注释：租户ID
	OneID       string    `gorm:"size:64;index" json:"one_id"` // 注释：OneID
	Topic       string    `gorm:"size:100;index" json:"topic"` // 注释：主题
	Status      string    `gorm:"size:20;default:pending" json:"status"` // pending/done/failed
	ProcessedAt time.Time `json:"processed_at"` // 注释：处理时间
	CreatedAt   time.Time `json:"created_at"` // 注释：创建时间
}

// TableName 指定表名
func (InboxEvent) TableName() string {
	return "inbox_events"
}

// MessageEventRecord 事件记录表：持久化所有发布事件（审计/重放用）
type MessageEventRecord struct {
	ID        uint      `gorm:"primaryKey" json:"id"` // 注释：主键ID
	TenantID  uint      `gorm:"index;not null;default:0" json:"tenant_id"` // 来自 Header（0=系统事件）
	OneID     string    `gorm:"size:64;index" json:"one_id"` // 注释：OneID
	EventID   string    `gorm:"size:64;uniqueIndex" json:"event_id"` // 注释：事件ID
	EventType string    `gorm:"size:50" json:"event_type"` // 注释：事件类型
	Topic     string    `gorm:"size:100" json:"topic"` // 注释：主题
	Key       string    `gorm:"size:100" json:"key"` // 分区键
	Payload   string    `gorm:"type:text" json:"payload"` // 注释：事件内容
	Status    string    `gorm:"size:20;default:sent" json:"status"` // created/sent/failed
	CreatedAt time.Time `json:"created_at"` // 注释：创建时间
}

// TableName 指定表名
func (MessageEventRecord) TableName() string {
	return "message_event_records"
}
