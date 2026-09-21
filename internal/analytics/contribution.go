// Package analytics 经营指标的纯函数计算层（无 DB、无 gin、无网络依赖）。
//
// 为什么单独成包：指标口径最容易出的问题是"分子分母各写一遍、改一处漏一处"，
// 以及"没有测试所以没人敢改"。把口径收敛成纯函数后，单测可以直接钉住每个比率的定义，
// API 层只负责聚合原始计数并调用 Build。
package analytics

import (
	"time"

	"ai-scrm/internal/schema"
)

// ContributionRaw AI 贡献度所需的原始计数（由 API 层按「租户 + 数据范围 + 时间窗」聚合后传入）。
// 全部是计数，不含任何客户个人数据。
//
// 两个"会话数"字段刻意分开，不要合并：它们回答的是两个不同问题，
// 合起来会让"新租户窗口内无新建会话"被读成"没有业务发生"。
//   - NewConversations   ：窗口内**新建**的会话 → 获客流入速度
//   - ActiveConversations：窗口内有**消息往来**的会话 → 接待量与归因的口径边界
type ContributionRaw struct {
	NewConversations     int64 // 窗口内新建会话数（增速观察）
	ActiveConversations  int64 // 窗口内有消息往来的会话数（接待量与归因的口径边界）
	AIServedCustomers    int64 // 窗口内会话从无人工回复的客户数（AI 独立接待）
	HumanServedCustomers int64 // 窗口内会话出现过人工回复的客户数
	AILeads              int64 // AI 独立接待且当前阶段 ≥ 已留资的客户数
	AssistedLeads        int64 // 人工参与且当前阶段 ≥ 已留资的客户数
	AIArrived            int64 // AI 独立接待且当前阶段 ≥ 已到店
	AIOrdered            int64 // AI 独立接待且当前阶段 ≥ 已成交
	AIMessages           int64 // 窗口内 AI 发出的消息数
	HumanMessages        int64 // 窗口内人工发出的消息数
	CustomerMessages     int64 // 窗口内客户发出的消息数
	PendingHandoffNow    int64 // 当前待接管会话数（瞬时值）
}

// ContributionNotes 指标口径说明，随响应一起下发。
//
// 设计取舍：这套指标是**阶段 × 接待归属的交叉归因**，不是事件级时间归因
// （即"留资这一刻是谁在接待"）。原因是事件级归因需要额外的事件时间轴，
// 当前数据模型没有独立记录"留资事件发生时刻"，强行近似只会给出看起来精确、
// 实际误导的数字。把限制显式写出来，好过让客户自行揣测。
var ContributionNotes = []string{
	"归因口径：以「客户当前旅程阶段」×「窗口内会话是否出现过人工回复」交叉判定，不是事件级时间归因。",
	"时间窗边界统一取「消息发生时间」：接待量与归因的客户集合 = 窗口内有消息往来的会话所涉客户，与消息量指标同窗同源，不会出现「会话数为 0 但消息数不为 0」的口径错位。",
	"全部指标按登录者的数据范围裁剪（部门管理员/只读只看本部门子树名下会话，销售只看自己名下会话，租户与平台管理员看全租户）；消息量同样裁剪，故四项计数永远同范围，不混用两种口径。",
	"AI 独立接待 = 该客户的会话没有人工回复记录（会话级 last_human_reply_at 为空；该字段不随统计窗口重置，属已知近似，会低估人工曾在更早时间介入过的客户）。",
	"转化率分母为对应接待类型的客户数；样本量小时（<30）建议只作趋势参考，不作为投放/考核结论。",
	"到店/成交以客户旅程阶段码为准（arrived/ordered/delivered），代表 CRM 记录状态，非财务口径营收。",
	"待接管数为瞬时值，与统计窗口无关；同样按登录者数据范围裁剪。",
}

// Build 计算 AI 贡献度指标。纯函数：相同 raw/days/since 必得相同结果。
// until 由调用方给出（便于测试固定时间），不由函数内部取 time.Now()。
func Build(raw ContributionRaw, days int, since, until time.Time) schema.AIContribution {
	served := raw.AIServedCustomers + raw.HumanServedCustomers

	out := schema.AIContribution{
		PeriodDays:           days,
		Since:                since.Format(time.RFC3339),
		Until:                until.Format(time.RFC3339),
		NewConversations:     raw.NewConversations,
		ActiveConversations:  raw.ActiveConversations,
		AIServedCustomers:    raw.AIServedCustomers,
		HumanServedCustomers: raw.HumanServedCustomers,
		AIServeShare:         rate(raw.AIServedCustomers, served),
		HandoffRate:          rate(raw.HumanServedCustomers, served),
		AILeads:              raw.AILeads,
		AssistedLeads:        raw.AssistedLeads,
		AILeadRate:           rate(raw.AILeads, raw.AIServedCustomers),
		AssistedLeadRate:     rate(raw.AssistedLeads, raw.HumanServedCustomers),
		AIArrived:            raw.AIArrived,
		AIOrdered:            raw.AIOrdered,
		AIArriveRate:         rate(raw.AIArrived, raw.AIServedCustomers),
		AIOrderRate:          rate(raw.AIOrdered, raw.AIServedCustomers),
		AIMessages:           raw.AIMessages,
		HumanMessages:        raw.HumanMessages,
		CustomerMessages:     raw.CustomerMessages,
		AIMessageShare:       rate(raw.AIMessages, raw.AIMessages+raw.HumanMessages),
		PendingHandoffNow:    raw.PendingHandoffNow,
		Notes:                ContributionNotes,
	}
	return out
}

// LeadStages 视为"已留资"及之后的阶段码集合（与 journey_stage 取值口径一致）。
var LeadStages = []string{"lead_captured", "arrived", "ordered", "delivered"}

// ArrivedStages 视为"已到店"及之后的阶段码集合。
var ArrivedStages = []string{"arrived", "ordered", "delivered"}

// OrderedStages 视为"已成交"及之后的阶段码集合（delivered 含在内：已交车必然是已成交）。
var OrderedStages = []string{"ordered", "delivered"}

// InStageSet 判断阶段码是否属于给定集合（空码/空集合均返回 false）。
// 抽出来的原因：三个阶段集合会被 API 层的 SQL/Go 双侧共用，
// 判定逻辑若各写一遍，"成交数 > 留资数"这类自相矛盾的看板就会悄悄出现。
func InStageSet(stage string, set []string) bool {
	if stage == "" {
		return false
	}
	for _, s := range set {
		if s == stage {
			return true
		}
	}
	return false
}

// rate 计算比率：分母为 0 时返回 0（而非 NaN/Inf）。
// 这一点必须是显式的——Go 里 0/0 是 NaN，直接放进 JSON 会序列化失败
// 或让前端图表整块空白，属"新租户零数据"必然踩到的形态。
func rate(numerator, denominator int64) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}
