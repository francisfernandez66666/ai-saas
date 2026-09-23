// Package model 业务领域模型与 GORM 表结构定义（SaaS 多租户，tenant_id 隔离，写入经自动盖章回调）。
package model

import "time"

// ============================================================
// 主动触达任务（触达最小闭环 · 表地基，2026-09-23）
//
// 为什么不复用 follow_ups：那张表是**人工跟进留痕**——一行描述"已经做过什么、结果如何"，
// 写入即终态；本表是**待执行的发送计划**——有 scheduled_at、有状态机、由后台 ticker 驱动，
// 混在一张表里既没法按 (tenant,status,scheduled_at) 建调度索引，也会让留痕查询被未执行的计划污染。
//
// 为什么落库再发而不是直接调通道：主动触达的合规判定（48h 窗口、周内频次上限、静默时段）
// 必须在发送前有个可解释的裁决记录，直发一旦失败就查不到"当时为什么没发"。
// 状态机：pending →（调度裁决）→ queued（已进 channel_outbound）→ sent / failed；
// 裁决不通过 → skipped（带 reason）；人工在队列里撤回 → cancelled（仅 pending 可撤）。
// ============================================================

// 触达任务状态机取值（列 size:20，新增取值不得超长）
const (
	OutreachStatusPending   = "pending"   // 已排期，待调度裁决
	OutreachStatusQueued    = "queued"    // 裁决通过并已投递到通道出站队列（关联 outbound_id）
	OutreachStatusSent      = "sent"      // 通道回发成功
	OutreachStatusSkipped   = "skipped"   // 合规/可达性裁决拦下（见 reason），不消耗通道
	OutreachStatusFailed    = "failed"    // 通道发送失败（见 error）
	OutreachStatusCancelled = "cancelled" // 人工撤回（仅 pending 可撤）
)

// 触达被拦下的原因码（稳定字面量：前端按它出文案，smoke 按它断言，勿随意改名）
const (
	OutreachReasonDisabled        = "disabled"          // 租户/平台未开启主动触达
	OutreachReasonNoChannel       = "no_channel"        // 客户没有任何可用活跃通道身份（发不出去）
	OutreachReasonOutOfWindow     = "out_of_window"     // 微信侧 48h 客服窗口已过
	OutreachReasonOverWeeklyLimit = "over_weekly_limit" // 超出该客户周内触达上限
	OutreachReasonChannelInactive = "channel_inactive"  // 命中的通道已被停用/未验证
	OutreachReasonContentFlagged  = "content_flagged"   // 文案命中内容安全 BLOCK 词
	OutreachReasonSendFailed      = "send_failed"       // 已入出站队列但通道终判失败（详情看 error 列）
)

// OutreachTask 主动触达任务表
// 一条 = 一次"计划在某时刻给某客户发一句话"，客户与通道在派发时才最终确定（排期后可能换通道）。
type OutreachTask struct {
	ID         uint   `gorm:"primaryKey" json:"id"`                      // 主键ID
	TenantID   uint   `gorm:"index;not null;default:0" json:"tenant_id"` // 租户ID（SaaS 多租户隔离）
	CustomerID uint   `gorm:"index;not null" json:"customer_id"`         // 目标客户ID
	Content    string `gorm:"type:text;not null" json:"content"`         // 触达正文（本方话术，不含客户 PII）

	// ScheduledAt 计划发送时间。落库前已按静默时段顺延（见 outreach.DeferPastQuiet），
	// 所以调度器只看"到点没到点"，不再二次判断时段。
	ScheduledAt time.Time `gorm:"index;not null" json:"scheduled_at"`
	Status      string    `gorm:"size:20;default:pending;index" json:"status"` // 见上方状态常量
	Reason      string    `gorm:"size:40" json:"reason"`                       // skipped 时的原因码
	Error       string    `gorm:"size:500" json:"error"`                       // failed 时的通道错误摘要

	ChannelID  uint `gorm:"index" json:"channel_id"`   // 实际命中的通道ID（派发时回填，0=尚未解析）
	OutboundID uint `gorm:"index" json:"outbound_id"`  // 关联 channel_outbound.id（出站回执锚点）
	Attempts   int  `gorm:"default:0" json:"attempts"` // 派发尝试次数（防调度空转）

	CreatedBy uint       `gorm:"index" json:"created_by"` // 排期人（tenant_users.id，0=系统）
	SentAt    *time.Time `json:"sent_at"`                 // 发送成功时间
	CreatedAt time.Time  `json:"created_at"`              // 创建时间
	UpdatedAt time.Time  `json:"updated_at"`              // 更新时间
}

// TableName 指定表名
func (OutreachTask) TableName() string {
	return "outreach_tasks"
}
