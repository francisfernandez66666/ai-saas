// Package model 业务领域模型与 GORM 表结构定义（SaaS 多租户，tenant_id 隔离，写入经自动盖章回调）。
package model

import "time"

// ReplyAttribution AI 回复质量归因快照（D9）。
// 一行绑定一条 AI 消息与回复时生效的行业包/模板/锚点，后续事件回填到同一行。
type ReplyAttribution struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	TenantID       uint       `gorm:"index;not null" json:"tenant_id"`
	MessageID      uint       `gorm:"uniqueIndex;not null" json:"message_id"`
	ConversationID uint       `gorm:"index" json:"conversation_id"`
	CustomerID     uint       `gorm:"index" json:"customer_id"`
	PackCode       string     `gorm:"size:32;index" json:"pack_code"`
	PackVersion    string     `gorm:"size:20;index" json:"pack_version"`
	TemplateID     string     `gorm:"size:50;index" json:"template_id"`
	AnchorType     int        `json:"anchor_type"`
	RouteResult    string     `gorm:"size:50" json:"route_result"`
	Provider       string     `gorm:"size:30" json:"provider"`
	ModelName      string     `gorm:"size:80;column:model" json:"model"`
	IntentBefore   float64    `json:"intent_before"`
	IntentAfter    float64    `json:"intent_after"`
	Hooked         bool       `gorm:"default:false;index" json:"hooked"`
	LeadCaptured   bool       `gorm:"default:false;index" json:"lead_captured"`
	PendingHuman   bool       `gorm:"default:false;index" json:"pending_human"`
	EvalScore      int        `gorm:"default:-1" json:"eval_score"`
	EvalCheckedAt  *time.Time `json:"eval_checked_at"`
	// ---- 终局标签（批五 A，2026-09-23 迁移 018）----
	// 与上面三个近端信号的区别：hooked/lead_captured/pending_human 是"这一条回复当轮的效果"，
	// arrived_at/dealt_at 是"这个客户最终有没有走到到店/成交"——AI 销售要优化的是后者。
	// 由小时任务按 customers.journey_stage 回填（analytics.ArrivedStages/OrderedStages 判定），
	// 指针为 nil 表示"尚未发生或未判定"，**不等于失败**：窗口未过的行必须留空（见 outcome_window_days）。
	ArrivedAt        *time.Time `json:"arrived_at"`
	DealtAt          *time.Time `json:"dealt_at"`
	OutcomeStage     string     `gorm:"size:30" json:"outcome_stage"` // 回填当时的旅程阶段码快照
	OutcomeCheckedAt *time.Time `json:"outcome_checked_at"`           // 最近一次巡检时刻（幂等与窗口判断依据）
	CreatedAt        time.Time  `gorm:"index" json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// TableName 指定表名
func (ReplyAttribution) TableName() string { return "reply_attributions" }

// PackStatSnapshot 包/模板效果的小时级物化快照（D9）。
type PackStatSnapshot struct {
	ID          uint    `gorm:"primaryKey" json:"id"`
	TenantID    uint    `gorm:"uniqueIndex:uidx_pack_stats_scope;not null" json:"tenant_id"`
	PackCode    string  `gorm:"size:32;uniqueIndex:uidx_pack_stats_scope;not null;index" json:"pack_code"`
	PackVersion string  `gorm:"size:20;uniqueIndex:uidx_pack_stats_scope;not null;index" json:"pack_version"`
	TemplateID  string  `gorm:"size:50;uniqueIndex:uidx_pack_stats_scope;not null;index" json:"template_id"`
	SampleCount int64   `json:"sample_count"`
	HookRate    float64 `json:"hook_rate"`
	LeadRate    float64 `json:"lead_rate"`
	// 终局率（批五 A）：arrive=到店率、deal=成交率，分母同为 sample_count。
	// 择臂层（template_bandit）与判优层（experiment_decision）按 experiment_reward_metric 选其一当奖励；
	// 二者样本稀疏度远高于 lead，因此样本门槛也更高（见 experiment_min_samples_* 键）。
	ArriveRate     float64   `json:"arrive_rate"`
	DealRate       float64   `json:"deal_rate"`
	PendingRate    float64   `gorm:"column:pending_human_rate" json:"pending_human_rate"`
	AvgIntentDelta float64   `json:"avg_intent_delta"`
	AvgEvalScore   *float64  `json:"avg_eval_score,omitempty"`
	ComputedAt     time.Time `gorm:"index" json:"computed_at"`
}

// TableName 指定表名
func (PackStatSnapshot) TableName() string { return "pack_stats" }

// PackTemplateStat 包/模板维度的聚合效果（D9 看板/接口视图）
type PackTemplateStat struct {
	TenantID    uint    `json:"tenant_id,omitempty"`
	PackCode    string  `json:"pack_code"`
	PackVersion string  `json:"pack_version"`
	TemplateID  string  `json:"template_id"`
	SampleCount int64   `json:"sample_count"`
	HookRate    float64 `json:"hook_rate"`
	LeadRate    float64 `json:"lead_rate"`
	PendingRate float64 `gorm:"column:pending_human_rate" json:"pending_human_rate"`
	// 终局率（批五 A，2026-09-23）：/admin/packs/stats 与 /super/packs/stats 直接序列化本视图，
	// 键名与 pack_stats 列名对齐，只增不改，既有消费方零感知。
	ArriveRate     float64  `json:"arrive_rate"`
	DealRate       float64  `json:"deal_rate"`
	AvgIntentDelta float64  `json:"avg_intent_delta"`
	AvgEvalScore   *float64 `json:"avg_eval_score,omitempty"`
}
