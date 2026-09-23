// AI 贡献度下钻谓词单测（D4，2026-09-23）。
//
// 这组用例守的不是"数算得对"，而是"看板数字与点进去的名单是同一批人"：
//  1. 计数与筛选必须恒等（ClassifyCounts 的每项 == Filter 的长度）——一旦有人把计数
//     改回独立实现，这条立刻红
//  2. 指标白名单只含客户级指标（会话/消息/瞬时值下钻出来必然条数不符）
//  3. 未知指标一律不命中（前端传错 metric 时是 400 + 空集，不是"整份名单"）
package analytics

import "testing"

// drillFixture 一份手工归属表，覆盖：AI/人工 × 各阶段 × 空阶段 × 战败。
//
// 编号即预期：ai_lead 命中 1、3、7；human_served 命中 2、4……
func drillFixture() []ContributionCustomer {
	return []ContributionCustomer{
		{CustomerID: 1, HasHuman: false, Stage: "lead_captured"}, // AI 留资
		{CustomerID: 2, HasHuman: true, Stage: "lead_captured"},  // 人工留资
		{CustomerID: 3, HasHuman: false, Stage: "arrived"},       // AI 到店（也是 AI 留资口径内）
		{CustomerID: 4, HasHuman: true, Stage: "arrived"},        // 人工到店
		{CustomerID: 5, HasHuman: false, Stage: "ordered"},       // AI 成交
		{CustomerID: 6, HasHuman: false, Stage: "delivered"},     // AI 交车（含在到店与成交口径内）
		{CustomerID: 7, HasHuman: false, Stage: "ai_connected"},  // AI 接待但未留资
		{CustomerID: 8, HasHuman: false, Stage: "lost"},          // AI 接待但战败（不在任何结果口径内）
		{CustomerID: 9, HasHuman: false, Stage: ""},              // 阶段为空：不得命中任何归因指标
	}
}

// TestDrillCountsEqualFilterLength 计数与下钻名单恒等（同源的机器保证）。
func TestDrillCountsEqualFilterLength(t *testing.T) {
	cs := drillFixture()
	counts := ClassifyContributionCounts(cs)

	cases := map[string]int64{
		MetricAIServed:     counts.AIServed,
		MetricHumanServed:  counts.HumanServed,
		MetricAILead:       counts.AILeads,
		MetricAssistedLead: counts.AssistedLeads,
		MetricAIArrived:    counts.AIArrived,
		MetricAIOrdered:    counts.AIOrdered,
	}
	for metric, want := range cases {
		got := int64(len(FilterContributionCustomers(cs, metric)))
		if got != want {
			t.Errorf("%s：看板计数 %d 与下钻名单 %d 条不一致（口径被拆成两套了）", metric, want, got)
		}
	}
}

// TestDrillMetricPredicateValues 六个指标的命中集合逐项核对（防止"看着对"的近似实现）。
func TestDrillMetricPredicateValues(t *testing.T) {
	cs := drillFixture()
	cases := []struct {
		metric string
		want   []uint
	}{
		// AI 独立接待 = 无人工回复的全部（含未留资/战败/空阶段）
		{MetricAIServed, []uint{1, 3, 5, 6, 7, 8, 9}},
		{MetricHumanServed, []uint{2, 4}},
		// 留资口径含 arrived/ordered/delivered（阶段是"及之后"的累计判定）
		{MetricAILead, []uint{1, 3, 5, 6}},
		{MetricAssistedLead, []uint{2, 4}},
		{MetricAIArrived, []uint{3, 5, 6}},
		{MetricAIOrdered, []uint{5, 6}}, // delivered 计入成交：已交车必然已成交
	}
	for _, tc := range cases {
		got := FilterContributionCustomers(cs, tc.metric)
		if len(got) != len(tc.want) {
			t.Errorf("%s 命中 %d 人，期望 %d 人", tc.metric, len(got), len(tc.want))
			continue
		}
		for i, c := range got {
			if c.CustomerID != tc.want[i] {
				t.Errorf("%s 第 %d 位 = %d，期望 %d", tc.metric, i, c.CustomerID, tc.want[i])
			}
		}
	}
}

// TestDrillUnknownMetricMatchesNothing 未知/会话级指标一律不命中（不是"默认返回全集"）。
func TestDrillUnknownMetricMatchesNothing(t *testing.T) {
	cs := drillFixture()
	for _, m := range []string{"", "active_conversations", "new_conversations", "ai_messages", "human_messages", "customer_messages", "pending_handoff", "ai_served_customers"} {
		if MatchContributionMetric(m, ContributionCustomer{CustomerID: 1, Stage: "ordered"}) {
			t.Errorf("未知指标 %q 不应命中任何客户", m)
		}
		if got := len(FilterContributionCustomers(cs, m)); got != 0 {
			t.Errorf("未知指标 %q 筛出 %d 人，应为 0", m, got)
		}
	}
}

// TestDrillMetricRegistryIsCustomerLevelOnly 下钻白名单只放客户级指标，且每个都有中文名。
func TestDrillMetricRegistryIsCustomerLevelOnly(t *testing.T) {
	if len(ContributionDrillMetrics) != 6 {
		t.Fatalf("可下钻指标应为 6 个（接待 2 + 归因 4），实际 %d", len(ContributionDrillMetrics))
	}
	counts := ClassifyContributionCounts(drillFixture())
	nums := map[string]int64{
		MetricAIServed: counts.AIServed, MetricHumanServed: counts.HumanServed,
		MetricAILead: counts.AILeads, MetricAssistedLead: counts.AssistedLeads,
		MetricAIArrived: counts.AIArrived, MetricAIOrdered: counts.AIOrdered,
	}
	for _, def := range ContributionDrillMetrics {
		if def.Label == "" {
			t.Errorf("指标 %s 缺中文名，前端会显示空白按钮", def.Metric)
		}
		if _, ok := nums[def.Metric]; !ok {
			t.Errorf("指标 %s 不在计数结构里，看板没有对应的可点数字", def.Metric)
		}
	}
}

// TestDrillServedPartitionCoversEveryone AI 独立 + 人工参与必须等于全集（互斥且无遗漏），
// 否则卡片上「AI 独立接待 8 / 人工参与 2」相加对不上"本周接待 10 位客户"就会被质疑。
func TestDrillServedPartitionCoversEveryone(t *testing.T) {
	cs := drillFixture()
	counts := ClassifyContributionCounts(cs)
	if counts.AIServed+counts.HumanServed != int64(len(cs)) {
		t.Errorf("接待归属未覆盖全部客户：ai=%d human=%d 全集=%d", counts.AIServed, counts.HumanServed, len(cs))
	}
	if counts.AIArrived > counts.AILeads {
		t.Errorf("到店 %d 不得大于留资 %d（阶段集合必须是嵌套的）", counts.AIArrived, counts.AILeads)
	}
	if counts.AIOrdered > counts.AIArrived {
		t.Errorf("成交 %d 不得大于到店 %d", counts.AIOrdered, counts.AIArrived)
	}
}
