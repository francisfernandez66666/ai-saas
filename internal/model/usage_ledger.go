// Package model 业务领域模型与 GORM 表结构定义（SaaS 多租户，tenant_id 隔离，写入经自动盖章回调）。
package model

import (
	"time"
)

// ============================================================
// Token 计量底座（商业化第二批 M3，借鉴翻译助手四期 §四 + SAAS_ROADMAP 阶段四）
//
// 定位：请求级 LLM 用量台账——provider/model/token/成本/延迟逐笔落账，
// 支撑分级用量看板与模型选型决策。
// ⚠️ 边界：本表只记账不扣费——商业包仍按"次"扣减（CheckAIQuota 不变），
// "Token 底层扣费"留待第三批评估
// ============================================================

// 阶段常量（stage_models 分阶段模型配置的键）
const (
	StageIntent   = "intent"   // 意图识别/锚点判定
	StageStrategy = "strategy" // 策略推理
	StageReply    = "reply"    // 话术生成
	StageFallback = "fallback" // 规则兜底（零LLM，仅占位统计）
)

// UsageLedger 用量台账（请求级）
type UsageLedger struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	TenantID         uint      `gorm:"index:idx_ul_tenant_time" json:"tenant_id"`
	CustomerID       uint      `json:"customer_id"`             // 可0（平台内部调用）
	UserID           uint      `json:"user_id"`                 // 触发人（AI自动=0）
	Stage            string    `gorm:"size:20" json:"stage"`    // intent|strategy|reply|fallback
	Provider         string    `gorm:"size:30" json:"provider"` // zhipu|siliconflow
	Model            string    `gorm:"size:80" json:"model"`    // 具体模型名
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	CostMicro        int64     `json:"cost_micro"` // 微元=total_tokens/1000×单价×markup（展示口径，不扣费）
	LatencyMs        int       `json:"latency_ms"`
	CreatedAt        time.Time `gorm:"index:idx_ul_tenant_time" json:"created_at"`
}

// TableName 指定表名
func (UsageLedger) TableName() string {
	return "usage_ledger"
}
