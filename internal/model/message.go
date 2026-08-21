package model

import (
	"time"
)

// ============================================================
// 消息模型 - 单条对话消息
// 为什么要存这么细？每个消息都可能是策略迭代的数据来源
// ============================================================

// Message 消息表
type Message struct {
	ID             uint   `gorm:"primaryKey" json:"id"`
	TenantID       uint   `gorm:"index;not null;default:0" json:"tenant_id"` // 租户ID（SaaS多租户隔离）
	ConversationID uint   `gorm:"index;not null" json:"conversation_id"`     // 会话ID
	CustomerID     uint   `gorm:"index" json:"customer_id"`                  // 客户ID
	SenderType     string `gorm:"size:20;not null" json:"sender_type"`       // 发送方: customer/ai/human/system
	SenderID       uint   `json:"sender_id"`                                 // 发送者ID（销售ID或客户ID）
	Content        string `gorm:"type:text;not null" json:"content"`         // 消息内容
	MessageType    string `gorm:"size:20;default:text" json:"message_type"`  // 消息类型: text/image/card
	// ---- 策略相关字段 ----
	AnchorType  int     `json:"anchor_type"`                 // 本次使用的锚类型
	TemplateID  string  `gorm:"size:50" json:"template_id"`  // 使用的话术模板ID
	RouteResult string  `gorm:"size:50" json:"route_result"` // 路由结果: ai/human/lead_captured_confirmed/store_visit_fast 等
	IntentScore float64 `json:"intent_score"`                // 当时的意向分
	Hooked      *bool   `json:"hooked"`                      // 是否接钩（客户回复后回填）
	Emotion     string  `gorm:"size:20" json:"emotion"`      // 情绪判断
	// ---- 元数据 ----
	Metadata  string    `gorm:"type:text" json:"metadata"` // 扩展元数据(JSON)
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName 指定表名
func (Message) TableName() string {
	return "messages"
}
