// AI 贡献度指标口径单测（D2）。
// 重点不在"算得对"，而在"边界不炸、口径不被悄悄改"：
// 零数据新租户、除零、人工/AI 对照组、消息占比分母都为 0 的形态都必须给出确定值。
package analytics

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

// fixedTime 固定时间，避免测试依赖真实时钟。
func fixedTime() (time.Time, time.Time) {
	since := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	return since, since.Add(30 * 24 * time.Hour)
}

// TestBuildZeroDataNoNaN 零数据（新租户/无对话）必须全 0，不得出现 NaN/Inf。
func TestBuildZeroDataNoNaN(t *testing.T) {
	since, until := fixedTime()
	got := Build(ContributionRaw{}, 30, since, until)

	for name, v := range map[string]float64{
		"AIServeShare":     got.AIServeShare,
		"HandoffRate":      got.HandoffRate,
		"AILeadRate":       got.AILeadRate,
		"AssistedLeadRate": got.AssistedLeadRate,
		"AIArriveRate":     got.AIArriveRate,
		"AIOrderRate":      got.AIOrderRate,
		"AIMessageShare":   got.AIMessageShare,
	} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Errorf("%s = %v，出现 NaN/Inf（0 除 0 未兜底）", name, v)
		}
		if v != 0 {
			t.Errorf("%s = %v，零数据应为 0", name, v)
		}
	}
	// JSON 序列化必须成功（NaN 会让整个响应体写入失败，前端拿到空体）
	if _, err := json.Marshal(got); err != nil {
		t.Fatalf("零数据响应 JSON 序列化失败: %v", err)
	}
	if got.Notes == nil || len(got.Notes) == 0 {
		t.Error("口径说明 Notes 必须随响应下发（否则客户会按对自己有利的方式解读）")
	}
}

// TestBuildRates 比率口径逐项钉住。
func TestBuildRates(t *testing.T) {
	since, until := fixedTime()
	raw := ContributionRaw{
		NewConversations:     12,
		ActiveConversations:  140,
		AIServedCustomers:    70,
		HumanServedCustomers: 30,
		AILeads:              21,
		AssistedLeads:        15,
		AIArrived:            7,
		AIOrdered:            3,
		AIMessages:           700,
		HumanMessages:        100,
		CustomerMessages:     500,
		PendingHandoffNow:    4,
	}
	got := Build(raw, 30, since, until)

	cases := []struct {
		name string
		got  float64
		want float64
	}{
		{"AIServeShare", got.AIServeShare, 0.70},
		{"HandoffRate", got.HandoffRate, 0.30},
		{"AILeadRate", got.AILeadRate, 0.30},                  // 21/70
		{"AssistedLeadRate", got.AssistedLeadRate, 0.50},      // 15/30
		{"AIArriveRate", got.AIArriveRate, 0.10},              // 7/70
		{"AIOrderRate", got.AIOrderRate, 0.04285714285714286}, // 3/70
		{"AIMessageShare", got.AIMessageShare, 0.875},         // 700/800
	}
	for _, c := range cases {
		if math.Abs(c.got-c.want) > 1e-9 {
			t.Errorf("%s = %.6f，期望 %.6f", c.name, c.got, c.want)
		}
	}

	// AI 独立接待占比 + 人机切换率 必须为 1（互补定义，防某一侧漏算）
	if math.Abs(got.AIServeShare+got.HandoffRate-1.0) > 1e-9 {
		t.Errorf("AIServeShare(%.4f)+HandoffRate(%.4f) 应为 1", got.AIServeShare, got.HandoffRate)
	}
	if got.PeriodDays != 30 || got.Since == "" || got.Until == "" {
		t.Errorf("窗口字段缺失: days=%d since=%q until=%q", got.PeriodDays, got.Since, got.Until)
	}
	if got.NewConversations != 12 || got.ActiveConversations != 140 || got.PendingHandoffNow != 4 {
		t.Errorf("透传计数被改动: new=%d active=%d pending=%d",
			got.NewConversations, got.ActiveConversations, got.PendingHandoffNow)
	}
	if got.Notes[0] == "" {
		t.Error("口径说明为空串")
	}
}

// TestNewAndActiveConversationsAreDistinctFields 两个会话数必须是独立字段且各自透传。
//
// 这条为 2026-09-21 实测缺陷而立：改造前只有一个 Conversations 字段，
// 语义在"窗口内新建"与"窗口内活跃"之间摇摆，实测出现 conversations=0 而 ai_messages=261
// （窗口内有历史消息往来、无新建会话）。若后人把两个字段再合并回去，本测试会立刻变红——
// 这正是"会话接待量为 0 所以 AI 没干活"这类误读的来源。
func TestNewAndActiveConversationsAreDistinctFields(t *testing.T) {
	since, until := fixedTime()
	// 只有活跃、没有新建：接待量必须照常给出，不得被"新建会话数为 0"带走
	got := Build(ContributionRaw{
		NewConversations:    0,
		ActiveConversations: 9,
		AIServedCustomers:   9,
		AIMessages:          261,
	}, 30, since, until)

	if got.NewConversations != 0 {
		t.Errorf("NewConversations 应为 0，实际 %d", got.NewConversations)
	}
	if got.ActiveConversations != 9 {
		t.Errorf("新建会话为 0 时 ActiveConversations 仍应为 9（窗口内有消息往来），实际 %d", got.ActiveConversations)
	}
	if got.AIServedCustomers != 9 || got.AIMessages != 261 {
		t.Errorf("活跃窗口内接待量/消息量被丢: served=%d msgs=%d", got.AIServedCustomers, got.AIMessages)
	}
	if got.AIServeShare != 1 {
		t.Errorf("仅 AI 接待时 AIServeShare 应为 1，实际 %v", got.AIServeShare)
	}
}

// TestBuildOnlyHuman 全人工租户：AI 占比 0、切换率 1，且不得除零。
func TestBuildOnlyHuman(t *testing.T) {
	since, until := fixedTime()
	got := Build(ContributionRaw{HumanServedCustomers: 12, AssistedLeads: 6}, 7, since, until)
	if got.AIServeShare != 0 {
		t.Errorf("全人工时 AIServeShare 应为 0，实际 %v", got.AIServeShare)
	}
	if got.HandoffRate != 1 {
		t.Errorf("全人工时 HandoffRate 应为 1，实际 %v", got.HandoffRate)
	}
	if got.AILeadRate != 0 {
		t.Errorf("AI 无接待时 AILeadRate 应为 0，实际 %v", got.AILeadRate)
	}
	if math.Abs(got.AssistedLeadRate-0.5) > 1e-9 {
		t.Errorf("AssistedLeadRate 应为 0.5，实际 %v", got.AssistedLeadRate)
	}
}

// TestBuildOnlyAI 全 AI 租户：切换率 0、AI 占比 1，消息全来自 AI 时占比 1。
func TestBuildOnlyAI(t *testing.T) {
	since, until := fixedTime()
	got := Build(ContributionRaw{AIServedCustomers: 5, AILeads: 5, AIMessages: 40, CustomerMessages: 40}, 30, since, until)
	if got.AIServeShare != 1 || got.HandoffRate != 0 {
		t.Errorf("全 AI：AIServeShare=%v(期望 1) HandoffRate=%v(期望 0)", got.AIServeShare, got.HandoffRate)
	}
	if got.AILeadRate != 1 {
		t.Errorf("AILeadRate 应为 1，实际 %v", got.AILeadRate)
	}
	if got.AIMessageShare != 1 {
		t.Errorf("无人工消息时 AIMessageShare 应为 1，实际 %v", got.AIMessageShare)
	}
}

// TestStageSetsAreNested 阶段码集合必须自洽：留资 ⊃ 到店 ⊃ 成交。
// 这三个集合是 SQL 过滤条件的事实来源，集合关系写错会导致「成交数 > 留资数」这种自相矛盾的看板。
func TestStageSetsAreNested(t *testing.T) {
	in := func(set []string, code string) bool {
		for _, s := range set {
			if s == code {
				return true
			}
		}
		return false
	}
	for _, code := range OrderedStages {
		if !in(ArrivedStages, code) {
			t.Errorf("阶段 %s 在成交集但不在到店集：集合非包含关系", code)
		}
	}
	for _, code := range ArrivedStages {
		if !in(LeadStages, code) {
			t.Errorf("阶段 %s 在到店集但不在留资集：集合非包含关系", code)
		}
	}
	for _, set := range [][]string{LeadStages, ArrivedStages, OrderedStages} {
		if len(set) == 0 {
			t.Error("阶段集合不得为空（空集合会让 SQL IN () 报错或恒假）")
		}
	}
}

// TestNotesCoverAttributionLimits 口径说明必须显式包含归因限制与非事件级说明，
// 防止后人"精简文案"时把这句最关键的免责/边界说明删掉。
func TestNotesCoverAttributionLimits(t *testing.T) {
	joined := strings.Join(ContributionNotes, "\n")
	for _, want := range []string{
		"交叉判定",
		"不是事件级时间归因",
		"last_human_reply_at",
		"瞬时值",
		// 2026-09-21：窗口口径说明必须留下——它是"会话数与消息数不得错位"的对外承诺，
		// 也是 last_human_reply_at 不随窗口重置这一已知近似的披露。
		"消息发生时间",
		"同窗同源",
		"不随统计窗口重置",
		// 数据范围口径：改造前会话/接待带范围、消息量不带，销售账号看到
		// 「会话 0 + 消息 261」的自相矛盾卡片。这条文案是"四项计数同范围"的对外承诺。
		"数据范围裁剪",
		"同范围",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("口径说明缺少关键限定 %q", want)
		}
	}
}
