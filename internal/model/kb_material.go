package model

// ============================================================
// 数据飞轮素材池（P3，2026-08-26）
//
// 定位：优质销售回复的资产回收——真人顾问回复与AI回复对照沉淀，
// 经两层评分（人工评分+成交结果 / AI evals 自动评估，互不干扰），
// 人工审核通过后进入行业包迭代素材库（打新版 .aipack 分发）。
//
// 写入点：
//   - 顾问人工回复（advisor send）→ source=human
//   - AI 回复落库 → source=ai（同会话形成对照对）
// 隐私：content 为脱敏责任字段——写入侧沿用聊天脱敏规则（手机号掩码）
// ============================================================

import (
	"time"
)

// KbFeedbackMaterial 素材池条目
type KbFeedbackMaterial struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	TenantID     uint      `gorm:"index" json:"tenant_id"`
	Conversation uint      `gorm:"index" json:"conversation_id"`
	MessageID    uint      `gorm:"index" json:"message_id"`
	Source       string    `gorm:"size:10;index" json:"source"`   // ai / human
	Content      string    `gorm:"type:text" json:"content"`      // 脱敏后的回复内容
	HumanScore   *int      `json:"human_score"`                   // 人工评分1-5（结果导向）
	DealClosed   bool      `gorm:"default:false" json:"deal_closed"` // 关联成交结果标记
	AiScore      *float64  `json:"ai_score"`                      // AI evals 评分0-5
	AiEvalNote   string    `gorm:"size:500" json:"ai_eval_note"`  // AI 评估摘要
	Status       string    `gorm:"size:20;default:pending;index" json:"status"` // pending/approved/rejected
	PackCode     string    `gorm:"size:32;index" json:"pack_code"` // 审核通过时归入的行业包code
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// TableName 指定表名
func (KbFeedbackMaterial) TableName() string { return "kb_feedback_materials" }
