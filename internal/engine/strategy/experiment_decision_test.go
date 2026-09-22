// 批五 C 判优层单测：阈值边界 / 功效不足不下结论 / 对称不显著 / 显著方向 / 置信度单调 /
// 门槛按指标分取 / L1 建议卡片 / L2 晋升计划。全部纯函数，不碰 DB。
package strategy

import (
	"testing"
	"time"
)

// TestDecideExactlyAtMinSamplesBoundary 恰好达到 min_samples 的边界：可判；差一条即功效不足
func TestDecideExactlyAtMinSamplesBoundary(t *testing.T) {
	a := ExperimentArm{Successes: 100, Trials: 2200}
	b := ExperimentArm{Successes: 220, Trials: 2200}
	if got := DecideExperiment(a, b, "lead", 2200, 400, 0.95).Decision; got != DecisionWinnerB {
		t.Errorf("恰好达标应可判优，实得 %v", got)
	}
	below := ExperimentArm{Successes: 220, Trials: 2199}
	v := DecideExperiment(a, below, "lead", 2200, 400, 0.95)
	if v.Decision != DecisionInsufficientSamples || v.Confidence != 0 {
		t.Errorf("差一条样本必须 insufficient 且零置信，实得 %+v", v)
	}
}

// TestDecideInsufficientNeverConcludes 功效不足绝不输出判优结论——哪怕差异"看起来"巨大
func TestDecideInsufficientNeverConcludes(t *testing.T) {
	a := ExperimentArm{Successes: 1, Trials: 60}
	b := ExperimentArm{Successes: 50, Trials: 60} // 1.7% vs 83%，样本不够也不许判
	v := DecideExperiment(a, b, "lead", 2200, 400, 0.95)
	if v.Decision != DecisionInsufficientSamples {
		t.Errorf("小样本巨大差异也必须 insufficient，实得 %v", v)
	}
	if v.MinSamples != 2200 {
		t.Errorf("门槛回显错误: %d", v.MinSamples)
	}
}

// TestDecideSymmetricNotSignificant 对称样本（同率同量）→ not_significant，置信 0.5
func TestDecideSymmetricNotSignificant(t *testing.T) {
	a := ExperimentArm{Successes: 110, Trials: 2200}
	b := ExperimentArm{Successes: 110, Trials: 2200}
	v := DecideExperiment(a, b, "lead", 2200, 400, 0.95)
	if v.Decision != DecisionNotSignificant {
		t.Errorf("对称样本应不显著，实得 %v", v)
	}
	if v.ProbBOverA < 0.49 || v.ProbBOverA > 0.51 {
		t.Errorf("对称时 P(B>A) 应≈0.5，实得 %v", v.ProbBOverA)
	}
	// 双端同值极端（全 0 / 全 1）方差塌缩：不显著不 NaN
	degen := DecideExperiment(ExperimentArm{0, 3000}, ExperimentArm{0, 3000}, "lead", 2200, 400, 0.95)
	if degen.Decision != DecisionNotSignificant || degen.Confidence != 0.5 {
		t.Errorf("方差塌缩应 0.5 不显著，实得 %+v", degen)
	}
	all1 := DecideExperiment(ExperimentArm{3000, 3000}, ExperimentArm{3000, 3000}, "lead", 2200, 400, 0.95)
	if all1.Decision != DecisionNotSignificant {
		t.Errorf("全 1 对称应不显著，实得 %v", all1.Decision)
	}
}

// TestDecideSignificantDirection 显著时方向正确：winner_b / winner_a 各一条
func TestDecideSignificantDirection(t *testing.T) {
	// 5% vs 7%，各 5000 样本：z≈4，双方向都该过 0.95
	up := DecideExperiment(ExperimentArm{250, 5000}, ExperimentArm{350, 5000}, "lead", 2200, 400, 0.95)
	if up.Decision != DecisionWinnerB || up.Confidence < 0.95 {
		t.Errorf("B 高应 winner_b，实得 %+v", up)
	}
	down := DecideExperiment(ExperimentArm{350, 5000}, ExperimentArm{250, 5000}, "lead", 2200, 400, 0.95)
	if down.Decision != DecisionWinnerA || down.Confidence < 0.95 {
		t.Errorf("A 高应 winner_a，实得 %+v", down)
	}
	if up.Confidence != down.Confidence {
		t.Errorf("镜像数据置信度应对称: %v vs %v", up.Confidence, down.Confidence)
	}
}

// TestDecideConfidenceMonotonic 差异越大置信度越高（单调），且中等差异落在 not_significant
func TestDecideConfidenceMonotonic(t *testing.T) {
	a := ExperimentArm{Successes: 110, Trials: 2200} // 5%
	prev := 0.0
	for _, sb := range []int64{110, 121, 132, 154, 176} { // 5%→5.5%→6%→7%→8%
		v := DecideExperiment(a, ExperimentArm{sb, 2200}, "lead", 2200, 400, 0.95)
		if v.ProbBOverA < prev {
			t.Fatalf("P(B>A) 随差异非单调: %+v", v)
		}
		prev = v.ProbBOverA
	}
	mid := DecideExperiment(a, ExperimentArm{121, 2200}, "lead", 2200, 400, 0.95)
	if mid.Decision != DecisionNotSignificant {
		t.Errorf("5%%→5.5%%（n=2200）应不够显著（需≈2200判5→7），实得 %+v", mid)
	}
	big := DecideExperiment(a, ExperimentArm{154, 2200}, "lead", 2200, 400, 0.95)
	if big.Decision != DecisionWinnerB {
		t.Errorf("5%%→7%%（n=2200）应显著，实得 %+v", big)
	}
}

// TestDecideMinSamplesPerMetric 门槛按指标分取：hook 用 400 小门槛，lead/arrive/deal 用大门槛；
// 未知指标退 lead
func TestDecideMinSamplesPerMetric(t *testing.T) {
	a := ExperimentArm{120, 500}
	b := ExperimentArm{180, 500}
	if v := DecideExperiment(a, b, "hook", 2200, 400, 0.95); v.Decision == DecisionInsufficientSamples {
		t.Errorf("hook 门槛 400，500 样本应可判: %+v", v)
	}
	for _, m := range []string{"lead", "arrive", "deal", "intent_after" /* 未知退 lead */} {
		v := DecideExperiment(a, b, m, 2200, 400, 0.95)
		if v.Decision != DecisionInsufficientSamples || v.MinSamples != 2200 {
			t.Errorf("指标 %q 应吃 lead 门槛 2200: %+v", m, v)
		}
	}
}

// TestTemplateStatInputRewardRate 建议层取率白名单：四键各归各列，未知（含 intent/eval 字面量）退 lead
func TestTemplateStatInputRewardRate(t *testing.T) {
	s := TemplateStatInput{HookRate: 0.3, LeadRate: 0.05, ArriveRate: 0.02, DealRate: 0.01}
	cases := map[string]float64{"hook": 0.3, "lead": 0.05, "arrive": 0.02, "deal": 0.01, "intent_after": 0.05, "eval_score": 0.05, "": 0.05}
	for m, want := range cases {
		if got := s.RewardRate(m); got != want {
			t.Errorf("metric=%q 应取 %v，实得 %v", m, want, got)
		}
	}
}

// suggestionRow 构造建议层输入行
func suggestionRow(tid uint, tmpl string, anchor int, n int64, leadRate float64) TemplateStatInput {
	return TemplateStatInput{
		TenantID: tid, PackCode: "pk", PackVersion: "v1",
		TemplateID: tmpl, AnchorType: anchor, SampleCount: n, LeadRate: leadRate,
	}
}

// TestBuildSuggestionsInsufficientNoVerdict 样本不足 → 卡片恒为 insufficient_samples，绝不带判优结论
func TestBuildSuggestionsInsufficientNoVerdict(t *testing.T) {
	p := DecisionParams{Metric: "lead", MinSamplesLead: 2200, MinSamplesHook: 400, Confidence: 0.95}
	stats := []TemplateStatInput{
		suggestionRow(1, "t_small", 2, 100, 0.9), // 率极高但样本不足
		suggestionRow(1, "t_big", 2, 5000, 0.05),
	}
	cards := BuildTemplateSuggestions(stats, p)
	if len(cards) != 2 {
		t.Fatalf("每模板一张卡，实得 %d", len(cards))
	}
	small := cards[0]
	if small.TemplateID != "t_small" {
		small = cards[1]
	}
	if small.Status != SuggestionStatusInsufficient || small.Confidence != 0 {
		t.Errorf("样本不足卡必须 insufficient 且零置信: %+v", small)
	}
}

// TestBuildSuggestionsLoserGetsReviewCard 显著低于同锚对照且低于中位数 → suggest_review；
// 显著占优 → leading；不显著 → keep_watching
func TestBuildSuggestionsLoserGetsReviewCard(t *testing.T) {
	p := DecisionParams{Metric: "lead", MinSamplesLead: 2200, MinSamplesHook: 400, Confidence: 0.95}
	stats := []TemplateStatInput{
		suggestionRow(1, "t_bad", 2, 3000, 0.01),
		suggestionRow(1, "t_mid1", 2, 3000, 0.06),
		suggestionRow(1, "t_mid2", 2, 3000, 0.06),
	}
	cards := BuildTemplateSuggestions(stats, p)
	byID := map[string]TemplateSuggestion{}
	for _, c := range cards {
		byID[c.TemplateID] = c
	}
	if byID["t_bad"].Status != SuggestionStatusReview {
		t.Errorf("差臂应 suggest_review: %+v", byID["t_bad"])
	}
	if byID["t_mid1"].Status == SuggestionStatusReview || byID["t_mid2"].Status == SuggestionStatusReview {
		t.Error("对照臂自身不应同时被判下线")
	}
	if byID["t_mid1"].Status != SuggestionStatusWatching && byID["t_mid1"].Status != SuggestionStatusLeading {
		t.Errorf("中位臂应 keep_watching/leading: %+v", byID["t_mid1"])
	}
	if byID["t_bad"].AnchorMedianRate <= byID["t_bad"].RewardRate {
		t.Errorf("卡片应回显中位数供复核: %+v", byID["t_bad"])
	}
}

// TestBuildSuggestionsNoPeerAndTenantIsolation 同锚单模板 → no_peer；跨租户/跨包绝不互判
func TestBuildSuggestionsNoPeerAndTenantIsolation(t *testing.T) {
	p := DecisionParams{Metric: "lead", MinSamplesLead: 2200, MinSamplesHook: 400, Confidence: 0.95}
	stats := []TemplateStatInput{
		suggestionRow(1, "solo", 5, 5000, 0.02),
		suggestionRow(2, "t2x", 2, 5000, 0.02), // 另一租户同锚同版本——不得进同一对照组
		suggestionRow(1, "t1y", 2, 5000, 0.02),
	}
	cards := BuildTemplateSuggestions(stats, p)
	byID := map[string]TemplateSuggestion{}
	for _, c := range cards {
		byID[c.TemplateID] = c
	}
	if byID["solo"].Status != SuggestionStatusNoPeer {
		t.Errorf("孤臂应 no_peer: %+v", byID["solo"])
	}
	// 租户 1 的 t1y 与租户 2 的 t2x 各自同锚组内都是孤臂（组=租户×包×版本×锚）
	if byID["t1y"].Status != SuggestionStatusNoPeer || byID["t2x"].Status != SuggestionStatusNoPeer {
		t.Errorf("跨租户被互判: %+v %+v", byID["t1y"], byID["t2x"])
	}
	// 输出定序确定性：重复构建结果逐位一致
	twice := BuildTemplateSuggestions(stats, p)
	for i := range cards {
		if cards[i].TemplateID != twice[i].TemplateID || cards[i].TenantID != twice[i].TenantID {
			t.Fatalf("输出顺序不稳定（map 遍历泄露）@%d", i)
		}
	}
}

// TestPromoteDecisionL2Guards L2 晋升计划：非胜出/冷却中/到上限/步长非法，全部拦下。
// 量纲已统一为 0~100 整数百分点（ab_weight 原生语义），故不再有"小数步长空转"分支。
func TestPromoteDecisionL2Guards(t *testing.T) {
	winner := ExperimentVerdict{Decision: DecisionWinnerB, Confidence: 0.97, Metric: "lead", MinSamples: 2200}
	now := time.Unix(1777000000, 0)

	ok := PromoteDecision("tpl_b", 10, winner, 10, 100, 0, now)
	if !ok.ShouldApply || ok.NewWeight != 20 || ok.PreviousWeight != 10 {
		t.Errorf("步长10应晋升到20: %+v", ok)
	}
	if ok.ComputedAt != now {
		t.Errorf("计划应带计算时刻（冷却判定输入）: %+v", ok)
	}
	if p := PromoteDecision("t", 10, winner, 0, 100, 0, now); p.ShouldApply || p.BlockedReason != "invalid_step" {
		t.Errorf("步长非正不得晋升: %+v", p)
	}
	if p := PromoteDecision("t", 10, ExperimentVerdict{Decision: DecisionNotSignificant}, 10, 100, 0, now); p.ShouldApply || p.BlockedReason != "no_winner_or_insufficient" {
		t.Errorf("不显著不得晋升: %+v", p)
	}
	if p := PromoteDecision("t", 10, winner, 10, 100, time.Hour, now); p.ShouldApply || p.BlockedReason != "cooldown" {
		t.Errorf("冷却中不得晋升: %+v", p)
	}
	if p := PromoteDecision("t", 100, winner, 10, 100, 0, now); p.ShouldApply || p.BlockedReason != "weight_at_max" {
		t.Errorf("到上限不得晋升: %+v", p)
	}
	// 超配置上限钳到上限
	if p := PromoteDecision("t", 95, winner, 50, 100, 0, now); !p.ShouldApply || p.NewWeight != 100 {
		t.Errorf("应钳到配置上限100: %+v", p)
	}
	// 配置被写坏（max=500）也必须顶在语义上限 100：自动晋升不得产出人工入口拒收的权重
	if p := PromoteDecision("t", 90, winner, 100, 500, 0, now); !p.ShouldApply || p.NewWeight != 100 {
		t.Errorf("脏配置应被硬钳到 100，实际 %+v", p)
	}
}

// TestApplyPromoteDecisionDisabledNoWrites experiment_auto_promote 默认 false：
// ApplyPromoteDecision 必须在任何 DB 触达前直接返回（本测试不初始化 DB，若误穿写路径必 panic）
func TestApplyPromoteDecisionDisabledNoWrites(t *testing.T) {
	restore := banditCfg(t, nil) // 空配置：SafeCfgBool 退默认 false
	defer restore()
	if err := ApplyPromoteDecision(7, PromoteDecision("tpl", 1,
		ExperimentVerdict{Decision: DecisionWinnerB}, 10, 100, 0, time.Now())); err != nil {
		t.Errorf("关态应静默跳过而非报错: %v", err)
	}
	restoreOn := banditCfg(t, map[string]string{"experiment_auto_promote": "true"})
	defer restoreOn()
	// 开态但计划未就绪（ShouldApply=false）同样零写入——DB 未初始化仍活着即证明没走写路径
	if err := ApplyPromoteDecision(7, PromotePlan{TemplateID: "tpl"}); err != nil {
		t.Errorf("未就绪计划应跳过: %v", err)
	}
}
