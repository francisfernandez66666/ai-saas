// Package model 业务领域模型与 GORM 表结构定义（SaaS 多租户，tenant_id 隔离，写入经自动盖章回调）。
package model

import "time"

// ============================================================
// D3 用量预警触达 + dunning 催缴（2026-09-23）
//
// 这两张表解决的是同一个商业断点：**租户要没钱了/已没钱了，我们却不知道，
// 或者知道了却没有任何动作**。此前用量只有事后看板（/admin/usage/summary），
// 到期只有 7d/3d 两档群提醒，过期即刻摘停——续费漏斗完全靠销售人肉盯群。
//
// 为什么是两张表而不是一张：
//   - usage_alerts 是**投递留痕 + 幂等锚**：一行="某租户某指标在某账期越过了某档，已经通知过"。
//     它按 (tenant, metric, threshold, period) 唯一，靠唯一索引本身去重（撞键即已发），
//     不需要状态机——预警是无状态的事后记录。
//   - billing_dunning 是**状态机**：一行=一个租户当前这一轮催缴走到哪、下一次何时、
//     是否已被我们自动封禁、是否已因续费而解除。它有 next_notify_at 驱动调度，
//     必须可被"续费"这个外部事件整体重置，混进预警表就没法表达"序列作废重来"。
//
// 为什么都落库而不是内存去重：多实例部署下进程内 map 各记各的，同一档会重复发信；
// 且运营/客服要能回答"我们到底哪天跟这家说过话"，这属于必须可审计的外呼记录。
// ============================================================

// 用量预警指标口径（列 size:30；改名即断历史行的可读性，新增请开新值勿复用）
const (
	// UsageMetricMonthlyCalls 月度 AI 调用次数配额（used_ai_calls / max_ai_calls_monthly）——「次」旧轨
	UsageMetricMonthlyCalls = "monthly_calls"
	// UsageMetricMonthlyTokens 月度订阅 token 额度（monthly_token_used / monthly_token_quota）——三桶之①
	UsageMetricMonthlyTokens = "monthly_tokens"
	// UsageMetricTokenBalance 预充值永久余额桶绝对水位（token_balance ≤ 阈值）——三桶之②
	// 它没有"分母"，故阈值语义是 token 数下限而非百分比，档位合并进同一张表用 metric 区分。
	UsageMetricTokenBalance = "token_balance"
)

// UsageAlert 用量预警投递留痕（幂等锚：唯一键存在即"这一档这个账期已经说过"）
type UsageAlert struct {
	ID       uint `gorm:"primaryKey" json:"id"`
	TenantID uint `gorm:"uniqueIndex:ux_usage_alert_once,priority:1;not null;default:0" json:"tenant_id"` // 租户ID
	// Metric 指标口径，见 UsageMetric* 常量；Threshold 是该档的触发值
	// （百分比口径存 80/95/100；token_balance 口径存 token 数下限）。
	Metric    string `gorm:"uniqueIndex:ux_usage_alert_once,priority:2;size:30;not null" json:"metric"`
	Threshold int    `gorm:"uniqueIndex:ux_usage_alert_once,priority:3;not null;default:0" json:"threshold"`
	// PeriodKey 账期锚：百分比类指标取 '2006-01' 月串（月度用量会随重置归零，
	// 下个月越过同一档是**新事件**，必须允许再发一次）；余额类指标同取月串
	// （余额桶不月清零，故按"每月最多提示一次同一水位档"限频，防持续欠费时天天轰炸）。
	PeriodKey string `gorm:"uniqueIndex:ux_usage_alert_once,priority:4;size:20;not null" json:"period_key"`

	UsagePct  int    `json:"usage_pct"`               // 触发时已用百分比（余额口径为 0，不参与展示）
	Remaining int64  `json:"remaining"`               // 触发时剩余量（次/token），邮件正文与后台列表都读它
	Channels  string `gorm:"size:50" json:"channels"` // 实际投递通道 "email,group"；空=全部通道不可用，仅日志降级
	Detail    string `gorm:"type:text" json:"detail"` // 触发上下文快照 JSON（used/max/quota），事后复盘口径漂移用

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName 指定表名
func (UsageAlert) TableName() string { return "usage_alerts" }

// 催缴序列状态（列 size:20）
const (
	// DunningStatusRunning 序列进行中：仍有后续档位待发/待封禁
	DunningStatusRunning = "running"
	// DunningStatusResolved 已解决：租户续费到账，序列作废（保留行做审计，不删）
	DunningStatusResolved = "resolved"
	// DunningStatusExhausted 已走完最后档：宽限期封禁已施加，不再重复推档（等续费或超管介入）
	DunningStatusExhausted = "exhausted"
)

// BillingDunning 到期催缴状态机（一租户一行，tenant_id 唯一）
//
// 与 tenants.grace_period_end_at 的关系：那列此前是**死列**（模型有、代码零读写）。
// 本轮把它接成真实语义=本轮宽限期截止（到期日 + dunning_suspend_after_days），
// 后台与超管视图都直接展示它，不再另存一份宽限终点造成双真相源。
type BillingDunning struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	TenantID uint   `gorm:"uniqueIndex;not null;default:0" json:"tenant_id"` // 租户ID（一户一行）
	Status   string `gorm:"size:20;default:running;index" json:"status"`     // 见 DunningStatus*

	// DueAt 本轮锚定的到期日快照。若 tenants.expired_at 被改成别的值（续期/改期），
	// 视为"上一轮作废、开新一轮"，Stage 归零重跑——否则人工改过期日会卡在半途。
	DueAt *time.Time `json:"due_at"`
	Stage int        `json:"stage"` // 已发档位序号（0=还没发过，1=第 1 档已发……）

	// NextNotifyAt 下一档预定时刻（展示与排期参考用）。
	// 注意：sweep 的重复抑制**不看这一列**，只看 Stage 是否前进（planDunning 取"已越过的最高档"，
	// 不大于库里 stage 即不发）——所以即便预定时刻被人工改乱，也不会多发一封信。
	// 封禁施加后此列清 NULL（序列已结束，没有下一封可等）。
	NextNotifyAt   *time.Time `gorm:"index" json:"next_notify_at"`
	LastNotifiedAt *time.Time `json:"last_notified_at"`

	// SuspendedAt 由**本序列**自动施加封禁的时刻（null=从没封过）。
	// 只有它非空时续费才允许自动解除封禁——超管人工封禁走 super_tenant_status 审计，
	// 不写这列，故"续费即恢复"绝不会把一家被人手动封掉的店放回货架。
	SuspendedAt *time.Time `json:"suspended_at"`

	SentTo    string    `gorm:"size:200" json:"sent_to"` // 最近一次触达的收件人（脱敏后逗号串，排障用）
	Detail    string    `gorm:"type:text" json:"detail"` // 最近一次档位上下文 JSON
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName 指定表名
func (BillingDunning) TableName() string { return "billing_dunning" }
