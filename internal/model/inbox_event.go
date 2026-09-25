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
	EventID     string    `gorm:"primaryKey;size:64" json:"event_id"`          // 事件唯一ID（来自 Header）
	TenantID    uint      `gorm:"index;not null;default:0" json:"tenant_id"`   // 租户ID
	OneID       string    `gorm:"size:64;index" json:"one_id"`                 // OneID
	Topic       string    `gorm:"size:100;index" json:"topic"`                 // 主题
	Status      string    `gorm:"size:20;index;default:pending" json:"status"` // pending/done/failed（P1-37 加状态索引：daily 清理按 pending 定位与故障排查）
	ProcessedAt time.Time `json:"processed_at"`                                // 处理时间
	CreatedAt   time.Time `json:"created_at"`                                  // 创建时间
}

// TableName 指定表名
func (InboxEvent) TableName() string {
	return "inbox_events"
}

// MessageEventRecord 事件记录表：事件全生命周期的一条台账（审计/重放/死信留痕）
//
// G-15① 收口批（2026-09-24）：这张表以前只记**发布阶段**（created/sent/failed），
// 消费侧无论成功、还是重试耗尽被丢弃，台账一个字都不改——于是"sent"看起来永远等于
// "已妥投"，观测面是不真实的。现在补第二阶段终态 consumed / dead_letter，
// 并带 trace_id + err_msg 让"这条事件在哪条链路上、为什么被丢掉"可回溯。
//
// ⚠ 清理器（service.CleanupMQTables）**不得删 dead_letter 行**：重试耗尽后 Kafka offset
// 已提交、进程内总线也无重投，这一行就是那条事件的**唯一副本**，按天数清掉等于
// 把"我们丢过什么"这件事一起丢掉。
type MessageEventRecord struct {
	ID        uint      `gorm:"primaryKey" json:"id"`                      // 主键ID
	TenantID  uint      `gorm:"index;not null;default:0" json:"tenant_id"` // 来自 Header（0=系统事件）
	OneID     string    `gorm:"size:64;index" json:"one_id"`               // OneID
	EventID   string    `gorm:"size:64;uniqueIndex" json:"event_id"`       // 事件ID
	EventType string    `gorm:"size:50" json:"event_type"`                 // 事件类型
	Topic     string    `gorm:"size:100" json:"topic"`                     // 主题
	Key       string    `gorm:"size:100" json:"key"`                       // 分区键
	Payload   string    `gorm:"type:text" json:"payload"`                  // 事件内容
	Status    string    `gorm:"size:20;index;default:sent" json:"status"`  // created/sent/failed/consumed/dead_letter
	TraceID   string    `gorm:"size:64;index" json:"trace_id"`             // 链路追踪 ID（Header 原样落库，发布/消费同 trace）
	ErrMsg    string    `gorm:"size:512" json:"err_msg"`                   // 失败/死信原因（consumed 时为空）
	CreatedAt time.Time `json:"created_at"`                                // 创建时间
	UpdatedAt time.Time `json:"updated_at"`                                // 终态回写时间（与 created_at 差即"在途多久"）
}

// TableName 指定表名
func (MessageEventRecord) TableName() string {
	return "message_event_records"
}
