// 批五 B 择臂层单测：冷启动退规则 / 样本不足 / 高转化胜出 / 开关往返 / fail-open 全家桶 /
// 只改序不改集合 / 并列确定 / thompson 可复现 / 奖励红线（未知指标退 lead）。
package strategy

import (
	"ai-scrm/config"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"errors"
	"fmt"
	"sort"
	"testing"
)

// banditCandidate 构造一个候选模板（同锚、可控规则分）
func banditCandidate(id string, matchScore float64, priority int) RecalledTemplate {
	return RecalledTemplate{
		Template:   &model.Template{ID: id, AnchorType: 2, Status: 1, Priority: priority},
		Similarity: matchScore,
		MatchScore: matchScore,
	}
}

// banditCfg 装配热配置并返回恢复函数（NewStaticService 覆盖全局单例）
func banditCfg(t *testing.T, kv map[string]string) func() {
	t.Helper()
	if config.GlobalConfig == nil {
		config.GlobalConfig = &config.Config{Strategy: config.StrategyConfig{Tau: 1.0}}
	}
	return runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(kv, nil))
}

// banditLoader 替换全局注入点并返回恢复函数
func banditLoader(t *testing.T, fn BanditStatsLoader) func() {
	t.Helper()
	old := banditStatsLoader
	banditStatsLoader = fn
	return func() { banditStatsLoader = old }
}

// stubStats 固定后验的内存 loader（不碰 DB）
func stubStats(stats map[string]BanditArmStat, err error) BanditStatsLoader {
	return func(tenantID uint, ids []string, metric string) (map[string]BanditArmStat, error) {
		if err != nil {
			return nil, err
		}
		return stats, nil
	}
}

func idsInOrder(rs []RecalledTemplate) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Template.ID)
	}
	return out
}

func idsSet(rs []RecalledTemplate) map[string]int {
	out := map[string]int{}
	for _, r := range rs {
		out[r.Template.ID]++
	}
	return out
}

// TestBanditDisabledZeroBehavior 开关关：ranker 返回规则序（首个最大分头部=旧「取最高」语义），且 loader 根本不被调用
func TestBanditDisabledZeroBehavior(t *testing.T) {
	restore := banditCfg(t, map[string]string{"template_bandit_enabled": "false"})
	defer restore()
	called := 0
	restoreLoader := banditLoader(t, func(uint, []string, string) (map[string]BanditArmStat, error) {
		called++
		return map[string]BanditArmStat{"tpl_b": {SampleCount: 10000, Rate: 0.9}}, nil
	})
	defer restoreLoader()
	cands := []RecalledTemplate{banditCandidate("tpl_a", 0.8, 0), banditCandidate("tpl_b", 0.5, 0), banditCandidate("tpl_c", 0.8, 0)}
	ranked := rankRecallCandidates(cands, 7)
	if ranked[0].Template.ID != "tpl_a" {
		t.Errorf("开关关应取首个最大分 tpl_a，实得 %s", ranked[0].Template.ID)
	}
	if called != 0 {
		t.Errorf("开关关不应读后验，loader 被调用 %d 次", called)
	}
	// 候选集与并列相对序逐位不变
	if got := idsInOrder(ranked); got[0] != "tpl_a" || got[1] != "tpl_c" || got[2] != "tpl_b" {
		t.Errorf("规则序漂移: %v", got)
	}
}

// TestBanditEnabledFalseAndTrueOnlyOrderChanges 开关往返：false↔true 的差异只体现在顺序，集合恒等
func TestBanditEnabledFalseAndTrueOnlyOrderChanges(t *testing.T) {
	stats := map[string]BanditArmStat{
		"tpl_a": {SampleCount: 2000, Rate: 0.02},
		"tpl_b": {SampleCount: 2000, Rate: 0.30},
	}
	restoreLoader := banditLoader(t, stubStats(stats, nil))
	defer restoreLoader()
	mk := func() []RecalledTemplate {
		return []RecalledTemplate{banditCandidate("tpl_a", 0.9, 5), banditCandidate("tpl_b", 0.9, 0)}
	}
	off := banditCfg(t, map[string]string{"template_bandit_enabled": "false"})
	offOrder := idsInOrder(rankRecallCandidates(mk(), 7))
	offSet := idsSet(rankRecallCandidates(mk(), 7))
	on := banditCfg(t, map[string]string{"template_bandit_enabled": "true", "template_bandit_mode": "ucb", "template_bandit_min_samples": "50"})
	onRanked := rankRecallCandidates(mk(), 7)
	onOrder := idsInOrder(onRanked)
	onSet := idsSet(onRanked)
	off()
	on()
	if offOrder[0] != "tpl_a" {
		t.Errorf("关态应规则赢家 tpl_a: %v", offOrder)
	}
	if onOrder[0] != "tpl_b" {
		t.Errorf("开态应统计赢家 tpl_b: %v", onOrder)
	}
	if len(offSet) != len(onSet) {
		t.Fatalf("集合大小漂移: %v vs %v", offSet, onSet)
	}
	for id, n := range offSet {
		if onSet[id] != n {
			t.Errorf("集合被改: %s %d vs %d", id, n, onSet[id])
		}
	}
}

// TestBanditColdStartNoStats 冷启动（pack_stats 无行）→ 全臂退规则分
func TestBanditColdStartNoStats(t *testing.T) {
	restore := banditCfg(t, map[string]string{"template_bandit_enabled": "true"})
	defer restore()
	restoreLoader := banditLoader(t, stubStats(map[string]BanditArmStat{}, nil))
	defer restoreLoader()
	cands := []RecalledTemplate{banditCandidate("tpl_a", 0.4, 0), banditCandidate("tpl_b", 0.9, 0)}
	if got := rankRecallCandidates(cands, 7)[0].Template.ID; got != "tpl_b" {
		t.Errorf("无后验应规则赢 tpl_b，实得 %s", got)
	}
}

// TestBanditInsufficientSamplesKeepsRule 样本 < min_samples → 即便率 1.0 也不启用该臂
func TestBanditInsufficientSamplesKeepsRule(t *testing.T) {
	restore := banditCfg(t, map[string]string{"template_bandit_enabled": "true", "template_bandit_min_samples": "50"})
	defer restore()
	restoreLoader := banditLoader(t, stubStats(map[string]BanditArmStat{
		"tpl_new": {SampleCount: 49, Rate: 1.0}, // 差一条也不给翻盘
	}, nil))
	defer restoreLoader()
	cands := []RecalledTemplate{banditCandidate("tpl_old", 0.9, 5), banditCandidate("tpl_new", 0.1, 0)}
	if got := rankRecallCandidates(cands, 7)[0].Template.ID; got != "tpl_old" {
		t.Errorf("样本不足臂不应抢位，实得 %s", got)
	}
}

// TestBanditHighConverterWins 同规则分下高转化臂胜出（UCB 确定性）
func TestBanditHighConverterWins(t *testing.T) {
	restore := banditCfg(t, map[string]string{"template_bandit_enabled": "true", "template_bandit_min_samples": "50"})
	defer restore()
	restoreLoader := banditLoader(t, stubStats(map[string]BanditArmStat{
		"tpl_lo": {SampleCount: 1000, Rate: 0.01},
		"tpl_hi": {SampleCount: 1000, Rate: 0.40},
	}, nil))
	defer restoreLoader()
	cands := []RecalledTemplate{banditCandidate("tpl_lo", 0.5, 0), banditCandidate("tpl_hi", 0.5, 0)}
	for i := 0; i < 3; i++ {
		if got := rankRecallCandidates(cands, 7)[0].Template.ID; got != "tpl_hi" {
			t.Fatalf("高转化臂应胜出，第 %d 次实得 %s", i+1, got)
		}
	}
}

// TestBanditFailOpenLoaderError loader 报错 → 整层回退规则序（fail-open 主路径）
func TestBanditFailOpenLoaderError(t *testing.T) {
	restore := banditCfg(t, map[string]string{"template_bandit_enabled": "true"})
	defer restore()
	restoreLoader := banditLoader(t, stubStats(nil, errors.New("db down")))
	defer restoreLoader()
	cands := []RecalledTemplate{banditCandidate("tpl_a", 0.9, 0), banditCandidate("tpl_b", 0.2, 0)}
	if got := rankRecallCandidates(cands, 7)[0].Template.ID; got != "tpl_a" {
		t.Errorf("loader 失败应规则序 tpl_a，实得 %s", got)
	}
}

// TestBanditFailOpenEmptyCandidates 候选为空/单候选：无从择臂，原样返回且不 panic
func TestBanditFailOpenEmptyCandidates(t *testing.T) {
	restore := banditCfg(t, map[string]string{"template_bandit_enabled": "true"})
	defer restore()
	defer banditLoader(t, stubStats(map[string]BanditArmStat{"x": {SampleCount: 999, Rate: 1}}, nil))()
	if got := rankRecallCandidates(nil, 7); len(got) != 0 {
		t.Errorf("空候选集应原样返回空")
	}
	single := []RecalledTemplate{banditCandidate("tpl_only", 0.1, 0)}
	if got := rankRecallCandidates(single, 7); len(got) != 1 || got[0].Template.ID != "tpl_only" {
		t.Errorf("单候选应原样返回")
	}
}

// TestBanditCandidateCap 候选超上限：只对前 32 个重排，尾部原序保留，集合不变（越界防御）
func TestBanditCandidateCap(t *testing.T) {
	restore := banditCfg(t, map[string]string{"template_bandit_enabled": "true", "template_bandit_min_samples": "50"})
	defer restore()
	stats := map[string]BanditArmStat{}
	var cands []RecalledTemplate
	for i := 0; i < banditCandidateCap+8; i++ {
		id := candID(i)
		cands = append(cands, banditCandidate(id, float64(1000-i)/1000, 0))
	}
	// 给队尾（超出钳制上限）一个"逆天"后验——它不该被读到
	stats[candID(banditCandidateCap+7)] = BanditArmStat{SampleCount: 5000, Rate: 0.99}
	restoreLoader := banditLoader(t, func(tenantID uint, ids []string, metric string) (map[string]BanditArmStat, error) {
		for _, id := range ids {
			if id == candID(banditCandidateCap+7) {
				t.Errorf("越界候选 %s 不应进入择臂读取", id)
			}
		}
		return stats, nil
	})
	defer restoreLoader()
	ranked := rankRecallCandidates(cands, 7)
	if len(ranked) != len(cands) {
		t.Fatalf("集合大小变了: %d vs %d", len(ranked), len(cands))
	}
	// 尾部（规则序第 33 名起）逐位原样
	for i := banditCandidateCap; i < len(cands); i++ {
		if ranked[i].Template.ID != cands[i].Template.ID {
			t.Fatalf("尾部序漂移 @%d: %s vs %s", i, ranked[i].Template.ID, cands[i].Template.ID)
		}
	}
}

// candID 生成测试候选 ID（i 递增即唯一）
func candID(i int) string { return fmt.Sprintf("cand-%02d", i) }

// TestBanditTieDeterministic 并列打破确定性：得分逐位相同的臂保持规则序，重复调用零漂移
func TestBanditTieDeterministic(t *testing.T) {
	restore := banditCfg(t, map[string]string{"template_bandit_enabled": "true", "template_bandit_mode": "ucb"})
	defer restore()
	stats := map[string]BanditArmStat{
		"t1": {SampleCount: 800, Rate: 0.10},
		"t2": {SampleCount: 800, Rate: 0.10},
	}
	restoreLoader := banditLoader(t, stubStats(stats, nil))
	defer restoreLoader()
	cands := []RecalledTemplate{banditCandidate("t1", 0.6, 3), banditCandidate("t2", 0.6, 3)}
	first := idsInOrder(rankRecallCandidates(cands, 7))
	for i := 0; i < 5; i++ {
		if got := idsInOrder(rankRecallCandidates(cands, 7)); got[0] != first[0] || got[1] != first[1] {
			t.Fatalf("并列序不稳定: %v vs %v", got, first)
		}
	}
}

// TestBanditThompsonReproducible thompson 注入随机源后同种子可复现；脏随机参数不 panic
func TestBanditThompsonReproducible(t *testing.T) {
	restore := banditCfg(t, map[string]string{"template_bandit_enabled": "true", "template_bandit_mode": "thompson", "template_bandit_min_samples": "50"})
	defer restore()
	stats := map[string]BanditArmStat{
		"ta": {SampleCount: 600, Rate: 0.25},
		"tb": {SampleCount: 600, Rate: 0.20},
	}
	restoreLoader := banditLoader(t, stubStats(stats, nil))
	defer restoreLoader()
	oldSeed := banditRandSeed
	banditRandSeed = func() int64 { return 42 }
	defer func() { banditRandSeed = oldSeed }()
	cands := []RecalledTemplate{banditCandidate("ta", 0.5, 0), banditCandidate("tb", 0.5, 0)}
	first := idsInOrder(rankRecallCandidates(cands, 7))
	for i := 0; i < 5; i++ {
		got := idsInOrder(rankRecallCandidates(cands, 7))
		if got[0] != first[0] || got[1] != first[1] {
			t.Fatalf("同种子 thompson 漂移: %v vs %v", got, first)
		}
	}
	// Beta 采样核：退化参数回均值不 panic
	if v := betaSample(0, 0, nil); v != 0.5 {
		t.Errorf("betaSample(0,0) 应回 0.5，实得 %v", v)
	}
}

// TestBanditUnknownMetricFallsBackLead 奖励红线：未知/自产出指标（intent_after/eval_score 字面量）
// 一律退默认 lead——绝不给策略自身输出当奖励的通道
func TestBanditUnknownMetricFallsBackLead(t *testing.T) {
	rows := model.PackStatSnapshot{LeadRate: 0.5, ArriveRate: 0.01, DealRate: 0.02}
	for _, bad := range []string{"intent_after", "eval_score", "intent_before", "hook", "", "nonsense"} {
		if got := banditRewardRate(rows, bad); got != 0.5 {
			t.Errorf("未知指标 %q 应退 lead(0.5)，实得 %v", bad, got)
		}
	}
	if got := banditRewardRate(rows, "arrive"); got != 0.01 {
		t.Errorf("arrive 应读 ArriveRate，实得 %v", got)
	}
	if got := banditRewardRate(rows, "deal"); got != 0.02 {
		t.Errorf("deal 应读 DealRate，实得 %v", got)
	}
	// 端到端：配置写 intent_after 时择臂行为与 lead 完全一致
	for _, metric := range []string{"intent_after", "lead"} {
		restore := banditCfg(t, map[string]string{"template_bandit_enabled": "true", "experiment_reward_metric": metric})
		restoreLoader := banditLoader(t, func(tenantID uint, ids []string, m string) (map[string]BanditArmStat, error) {
			if sanitizeBanditMetric(m) != "lead" {
				t.Errorf("loader 收到的指标未被收敛: %q", m)
			}
			return map[string]BanditArmStat{"x": {SampleCount: 900, Rate: 0.2}}, nil
		})
		rankRecallCandidates([]RecalledTemplate{banditCandidate("x", 0.5, 0), banditCandidate("y", 0.5, 0)}, 7)
		restoreLoader()
		restore()
	}
}

// TestBanditPriorFromPriority Priority 先验单调且被真实样本压过（"先验与兜底"语义）
func TestBanditPriorFromPriority(t *testing.T) {
	a0, b0 := banditPriorFromPriority(0)
	if a0 <= 1 || a0 >= a0+b0 {
		t.Fatalf("priority=0 先验异常: %v %v", a0, b0)
	}
	mean0 := a0 / (a0 + b0)
	a1, b1 := banditPriorFromPriority(20)
	mean1 := a1 / (a1 + b1)
	if mean1 <= mean0 {
		t.Errorf("高 Priority 先验均值应更高: %v vs %v", mean0, mean1)
	}
	if mean1 >= 1 {
		t.Errorf("先验均值不得到 1（保留翻盘空间）: %v", mean1)
	}
	aNeg, _ := banditPriorFromPriority(-5)
	if aNeg <= 1 {
		t.Errorf("负 Priority 应钳 0 退无信息先验，实得 alpha0=%v", aNeg)
	}
	// 高 priority 但数据极差 → 统计增量不该把臂推到别人之上
	hiPriorLoser := banditArmScore(BanditArmStat{SampleCount: 1000, Rate: 0.0}, true, 50, "ucb", 50, 2000, nil)
	loPriorWinner := banditArmScore(BanditArmStat{SampleCount: 1000, Rate: 0.3}, true, 50, "ucb", 0, 2000, nil)
	if hiPriorLoser >= loPriorWinner {
		t.Errorf("数据应压过先验: 差臂 %v vs 好臂 %v", hiPriorLoser, loPriorWinner)
	}
}

// TestBanditArmScoreFailOpenPerArm 臂级 fail-open：负率 / NaN 率 / rng 缺失 thompson 全退 0 或均值
func TestBanditArmScoreFailOpenPerArm(t *testing.T) {
	if v := banditArmScore(BanditArmStat{SampleCount: 100, Rate: -0.5}, true, 50, "ucb", 0, 100, nil); v != 0 {
		t.Errorf("负率臂应退规则分(0)，实得 %v", v)
	}
	if v := banditArmScore(BanditArmStat{SampleCount: 100, Rate: 1.7}, true, 50, "ucb", 0, 100, nil); v <= 0 {
		t.Errorf("率>1 脏值应被钳住仍产出增量，实得 %v", v)
	}
	if v := banditArmScore(BanditArmStat{SampleCount: 100, Rate: 0.2}, true, 50, "thompson", 0, 100, nil); v <= 0 {
		t.Errorf("thompson 无 rng 应退后验均值(>0)，实得 %v", v)
	}
}

// TestBanditStep4Integration 经 Step4 主入口往返：false↔true 只换选中模板，同集合候选全保留可见性
func TestBanditStep4Integration(t *testing.T) {
	abTestSetup(t)
	restoreLoader := banditLoader(t, stubStats(map[string]BanditArmStat{
		"tpl_a": {SampleCount: 1500, Rate: 0.01},
		"tpl_b": {SampleCount: 1500, Rate: 0.35},
	}, nil))
	defer restoreLoader()
	templates := []model.Template{
		{ID: "tpl_a", AnchorType: 2, Status: 1, TriggerTags: `["price"]`, Priority: 9}, // 规则+先验双高
		{ID: "tpl_b", AnchorType: 2, Status: 1, TriggerTags: `["price"]`, Priority: 0},
	}
	off := banditCfg(t, map[string]string{"template_bandit_enabled": "false"})
	got, _ := Step4_RecallTemplate(2, []string{"price"}, [32]float64{}, templates, 5, 7)
	off()
	if got == nil || got.ID != "tpl_a" {
		t.Fatalf("关态应规则赢 tpl_a，实得 %+v", got)
	}
	on := banditCfg(t, map[string]string{"template_bandit_enabled": "true"})
	defer on()
	got2, _ := Step4_RecallTemplate(2, []string{"price"}, [32]float64{}, templates, 5, 7)
	if got2 == nil || got2.ID != "tpl_b" {
		t.Fatalf("开态应统计赢 tpl_b，实得 %+v", got2)
	}
}

// TestBanditStatsNilSafeWhenCfgUnset 配置单例未初始化（Safe*→默认 false）：不发查询不改序
func TestBanditStatsNilSafeWhenCfgUnset(t *testing.T) {
	old := runtimecfg.DefaultSystemConfigService
	runtimecfg.DefaultSystemConfigService = nil
	defer func() { runtimecfg.DefaultSystemConfigService = old }()
	called := 0
	restoreLoader := banditLoader(t, func(uint, []string, string) (map[string]BanditArmStat, error) {
		called++
		return nil, nil
	})
	defer restoreLoader()
	cands := []RecalledTemplate{banditCandidate("m", 0.7, 0), banditCandidate("n", 0.3, 0)}
	ranked := rankRecallCandidates(cands, 7)
	if called != 0 || ranked[0].Template.ID != "m" {
		t.Errorf("配置未初始化应整体退规则: called=%d head=%s", called, ranked[0].Template.ID)
	}
}

// TestBanditSetIdentityRandomStats 随机脏统计下集合恒等（只改序不改集合的强化断言）
func TestBanditSetIdentityRandomStats(t *testing.T) {
	restore := banditCfg(t, map[string]string{"template_bandit_enabled": "true", "template_bandit_mode": "thompson"})
	defer restore()
	restoreLoader := banditLoader(t, stubStats(map[string]BanditArmStat{
		"s1": {SampleCount: -3, Rate: 0.5}, // 脏：负样本
		"s2": {SampleCount: 90, Rate: 0.5},
		"s3": {SampleCount: 90, Rate: 2.0}, // 脏：率>1
	}, nil))
	defer restoreLoader()
	cands := []RecalledTemplate{banditCandidate("s1", 0.9, 1), banditCandidate("s2", 0.5, 1), banditCandidate("s3", 0.1, 1), banditCandidate("s4", 0.7, 0)}
	ranked := rankRecallCandidates(cands, 7)
	before, after := idsInOrder(cands), idsInOrder(ranked)
	sort.Strings(before)
	sort.Strings(after)
	if len(before) != len(after) {
		t.Fatalf("集合大小漂移")
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("集合被改: %v vs %v", before, after)
		}
	}
}
