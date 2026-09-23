// AI 贡献度的客户级归属判定与下钻谓词（D4，2026-09-23）。
//
// 为什么单独一个文件：看板的六项客户级计数与「点数字看客户名单」的下钻列表必须给出
// 同一批人。这件事只要写成两套 SQL，就一定会出现"卡片说 AI 留资 8 个、
// 点进去列表 20 行"——那比没有下钻更糟，因为它把口径矛盾摆到了客户面前
// （D2 当天就抓到过一次同类错位：会话数与消息量不同窗）。
//
// 所以判据在这里只写一遍：
//   - MatchContributionMetric 是唯一谓词
//   - ClassifyContributionCounts 直接调用它来累加计数，而不是另写一遍 if
//   - API 层无论计数还是取名单，都先经 contributionCustomers 拿同一份归属表
package analytics

// ContributionCustomer 一个客户在本次「窗口 + 数据范围」内的归属判定结果。
//
// 两个字段各自的答案：
//   - HasHuman：窗口内该客户的可见会话是否出现过人工回复（true=人工参与，false=AI 独立接待）
//   - Stage：客户当前旅程阶段码（归因看的是 CRM 现状，不是事件时刻——见 ContributionNotes）
type ContributionCustomer struct {
	CustomerID uint
	HasHuman   bool
	Stage      string
}

// 下钻指标码（稳定字面量：前端按它发请求、后端按它出文案，两侧都不要自造）。
const (
	MetricAIServed     = "ai_served"     // AI 独立接待客户
	MetricHumanServed  = "human_served"  // 人工参与客户
	MetricAILead       = "ai_lead"       // AI 独立接待且已留资及之后
	MetricAssistedLead = "assisted_lead" // 人工参与且已留资及之后
	MetricAIArrived    = "ai_arrived"    // AI 独立接待且已到店及之后
	MetricAIOrdered    = "ai_ordered"    // AI 独立接待且已成交及之后
)

// ContributionCounts 六项客户级计数（看板卡片上的六个可点数字）。
type ContributionCounts struct {
	AIServed      int64
	HumanServed   int64
	AILeads       int64
	AssistedLeads int64
	AIArrived     int64
	AIOrdered     int64
}

// MatchContributionMetric 判断客户是否命中某下钻指标；metric 未知返回 false。
//
// 刻意把"接待归属"与"阶段门槛"两条都放进来：AI 侧三个结果指标必须先满足
// 「窗口内没有人工回复」，否则人工跟进出来的到店会被记到 AI 头上。
func MatchContributionMetric(metric string, c ContributionCustomer) bool {
	byAI := !c.HasHuman
	switch metric {
	case MetricAIServed:
		return byAI
	case MetricHumanServed:
		return c.HasHuman
	case MetricAILead:
		return byAI && InStageSet(c.Stage, LeadStages)
	case MetricAssistedLead:
		return c.HasHuman && InStageSet(c.Stage, LeadStages)
	case MetricAIArrived:
		return byAI && InStageSet(c.Stage, ArrivedStages)
	case MetricAIOrdered:
		return byAI && InStageSet(c.Stage, OrderedStages)
	}
	return false
}

// ClassifyContributionCounts 按同一谓词累加六项计数。
//
// 这里**必须**调用 MatchContributionMetric，而不是重新写一遍 if——
// 谓词与计数分开维护，就是"数字与名单不一致"缺陷的标准成因。
func ClassifyContributionCounts(cs []ContributionCustomer) ContributionCounts {
	var out ContributionCounts
	for _, c := range cs {
		if MatchContributionMetric(MetricAIServed, c) {
			out.AIServed++
		}
		if MatchContributionMetric(MetricHumanServed, c) {
			out.HumanServed++
		}
		if MatchContributionMetric(MetricAILead, c) {
			out.AILeads++
		}
		if MatchContributionMetric(MetricAssistedLead, c) {
			out.AssistedLeads++
		}
		if MatchContributionMetric(MetricAIArrived, c) {
			out.AIArrived++
		}
		if MatchContributionMetric(MetricAIOrdered, c) {
			out.AIOrdered++
		}
	}
	return out
}

// FilterContributionCustomers 取出命中某指标的客户（保持入参顺序，调用方决定排序）。
func FilterContributionCustomers(cs []ContributionCustomer, metric string) []ContributionCustomer {
	out := make([]ContributionCustomer, 0, len(cs))
	for _, c := range cs {
		if MatchContributionMetric(metric, c) {
			out = append(out, c)
		}
	}
	return out
}

// ContributionMetricCodes 六个客户级指标码（顺序即展示顺序）。
var ContributionMetricCodes = []string{
	MetricAIServed, MetricHumanServed, MetricAILead, MetricAssistedLead, MetricAIArrived, MetricAIOrdered,
}

// ContributionMetricLabels 指标码 → 中文口径名（随响应下发，前端不复写第二套文案）。
var ContributionMetricLabels = map[string]string{
	MetricAIServed:     "AI 独立接待客户",
	MetricHumanServed:  "人工参与客户",
	MetricAILead:       "AI 留资客户",
	MetricAssistedLead: "人工留资客户",
	MetricAIArrived:    "AI 到店客户",
	MetricAIOrdered:    "AI 成交客户",
}

// ContributionMetricDefinition 指标是否可下钻的说明（供前端与 smoke 判定）。
type ContributionMetricDefinition struct {
	Metric string `json:"metric"`
	Label  string `json:"label"`
}

// ContributionDrillMetrics 可下钻指标清单（响应里回带，前端据此决定哪些数字可点）。
// 会话级/消息级指标（活跃会话、消息量、待接管）刻意不在其中：它们的单位是"会话/条"，
// 与客户名单不同量纲，硬做下钻就会让"列表条数 ≠ 卡片数字"。
var ContributionDrillMetrics = func() []ContributionMetricDefinition {
	out := make([]ContributionMetricDefinition, 0, len(ContributionMetricCodes))
	for _, m := range ContributionMetricCodes {
		out = append(out, ContributionMetricDefinition{Metric: m, Label: ContributionMetricLabels[m]})
	}
	return out
}()
