// Package strategy 判优/晋升参数的**租户生效**断言（2026-09-23 批六）。
//
// 这批测试钉的是一类特定缺陷："键写在租户覆盖层、值读自系统默认层"。
// 该缺陷不报错、不panic，只是后台改了参数而行为不变——上线后没人会发现，
// 直到某天有人对比两个租户的卡片口径才对不上。故用断言把"租户覆盖必须赢"钉死。
// 本文件零 DB（只替换配置单例），与 experiment_promote_sweep_test.go 的连库用例分开。
package strategy

import (
	"testing"
	"time"

	"ai-scrm/internal/runtimecfg"
)

// cfgWithTenants 同时装配系统层与租户覆盖层，返回恢复函数。
// 为什么不用 banditCfg：它只喂系统层（tenants=nil），恰好是本次要封堵的那条读法。
func cfgWithTenants(t *testing.T, system map[string]string, tenants map[uint]map[string]string) func() {
	t.Helper()
	return runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(system, tenants))
}

// TestDecisionParamsTenantOverrideWins 租户覆盖必须赢过系统层，且未覆盖的租户沿用系统层。
func TestDecisionParamsTenantOverrideWins(t *testing.T) {
	restore := cfgWithTenants(t,
		map[string]string{
			"experiment_reward_metric":    `"lead"`,
			"experiment_min_samples_lead": "2200",
			"experiment_min_samples_hook": "400",
			"experiment_confidence":       "0.95",
		},
		map[uint]map[string]string{
			7: {
				"experiment_reward_metric":    `"hook"`,
				"experiment_min_samples_lead": "100",
				"experiment_confidence":       "0.99",
			},
		})
	defer restore()

	p7 := DecisionParamsForTenant(7)
	if p7.Metric != "hook" {
		t.Errorf("租户7 奖励指标应取覆盖值 hook，实际=%s", p7.Metric)
	}
	if p7.MinSamplesLead != 100 {
		t.Errorf("租户7 门槛应取覆盖值 100，实际=%d", p7.MinSamplesLead)
	}
	if p7.Confidence != 0.99 {
		t.Errorf("租户7 置信阈值应取覆盖值 0.99，实际=%v", p7.Confidence)
	}
	p8 := DecisionParamsForTenant(8)
	if p8.Metric != "lead" || p8.MinSamplesLead != 2200 || p8.Confidence != 0.95 {
		t.Errorf("无覆盖的租户8 必须沿用系统层，实际=%+v", p8)
	}
	// 系统层视图（tid=0）与旧的"全局读"口径逐字节等价——保证默认态行为零变化
	p0 := DecisionParamsFromConfig()
	if p0.Metric != "lead" || p0.MinSamplesLead != 2200 || p0.MinSamplesHook != 400 {
		t.Errorf("DecisionParamsFromConfig 应等价于系统层读法，实际=%+v", p0)
	}
}

// TestDecisionParamsClampsBadValues 手填误配置一律钳回可用区间，而不是照用把判优层废掉。
// 三个致命形态：门槛 0（功效检查恒过）、置信 ≤0.5（抛硬币即判优）、奖励指标越白名单（reward hacking）。
func TestDecisionParamsClampsBadValues(t *testing.T) {
	restore := cfgWithTenants(t, nil, map[uint]map[string]string{
		11: {"experiment_min_samples_lead": "0", "experiment_min_samples_hook": "-5", "experiment_confidence": "0.2", "experiment_reward_metric": `"intent"`},
		12: {"experiment_confidence": "1.5"},
	})
	defer restore()

	p := DecisionParamsForTenant(11)
	if p.MinSamplesLead != 1 || p.MinSamplesHook != 1 {
		t.Errorf("门槛必须钳下限 1，实际 lead=%d hook=%d", p.MinSamplesLead, p.MinSamplesHook)
	}
	if p.Confidence != 0.95 {
		t.Errorf("置信 0.2 属误配置，应回落默认 0.95，实际=%v", p.Confidence)
	}
	if p.Metric != "lead" {
		t.Errorf("intent 不在奖励白名单（防 reward hacking），应退 lead，实际=%s", p.Metric)
	}
	if got := DecisionParamsForTenant(12).Confidence; got != 0.95 {
		t.Errorf("置信 1.5 数学上不可达，应回落默认 0.95，实际=%v", got)
	}
}

// sugInput 造一条判优输入：同包同锚，两臂留资率拉开差距（0.30 vs 0.10，n=500 足够显著）。
func sugInput(tid uint, tpl string, leadRate float64, n int64) TemplateStatInput {
	return TemplateStatInput{
		TenantID: tid, PackCode: "pk", PackVersion: "1.0.0", TemplateID: tpl, AnchorType: 2,
		SampleCount: n, HookRate: 0.4, LeadRate: leadRate,
	}
}

// TestBuildSuggestionsByTenantUsesOwnParams ★本批核心断言：同一批输入，两个租户各按自己的门槛出结论。
// 若判优仍走"全局单份参数"，租户 7 的 insufficient_samples 会被系统层 2200 门槛统一判成
// insufficient——它明明把门槛降到了 100 想早点看到结论。这个用例就是让"参数按租户走"不可回退。
func TestBuildSuggestionsByTenantUsesOwnParams(t *testing.T) {
	restore := cfgWithTenants(t,
		map[string]string{"experiment_reward_metric": `"lead"`, "experiment_min_samples_lead": "2200", "experiment_confidence": "0.95"},
		map[uint]map[string]string{7: {"experiment_min_samples_lead": "100"}})
	defer restore()

	inputs := []TemplateStatInput{
		sugInput(7, "t7_good", 0.30, 500), sugInput(7, "t7_bad", 0.10, 500),
		sugInput(8, "t8_good", 0.30, 500), sugInput(8, "t8_bad", 0.10, 500),
	}
	byTid := map[uint][]TemplateSuggestion{}
	for _, s := range BuildTemplateSuggestionsByTenant(inputs) {
		byTid[s.TenantID] = append(byTid[s.TenantID], s)
	}
	if len(byTid[7]) != 2 || len(byTid[8]) != 2 {
		t.Fatalf("两个租户各应出 2 张卡片，实际 t7=%d t8=%d", len(byTid[7]), len(byTid[8]))
	}
	t7 := byTid[7][0]
	if t7.Status == SuggestionStatusInsufficient {
		t.Errorf("租户7 门槛已降到 100（n=500 达标），不得再报功效不足：%+v", byTid[7])
	}
	if t7.MinSamples != 100 {
		t.Errorf("租户7 卡片应回显自己的门槛 100，实际=%d", t7.MinSamples)
	}
	for _, s := range byTid[8] {
		if s.Status != SuggestionStatusInsufficient || s.MinSamples != 2200 {
			t.Errorf("租户8 沿用系统层门槛 2200（n=500 不足）应报 insufficient_samples，实际=%+v", s)
		}
	}
}

// TestBuildSuggestionsByTenantEmptyAndStable 空输入回空数组（非 nil，前端 .map 不炸）；两次调用序一致（卡片不抖动）。
func TestBuildSuggestionsByTenantEmptyAndStable(t *testing.T) {
	restore := cfgWithTenants(t, map[string]string{"experiment_min_samples_lead": "10"}, nil)
	defer restore()

	if got := BuildTemplateSuggestionsByTenant(nil); got == nil || len(got) != 0 {
		t.Errorf("空输入应返回非 nil 空切片，实际=%v", got)
	}
	inputs := []TemplateStatInput{
		sugInput(3, "b", 0.2, 50), sugInput(2, "a", 0.2, 50), sugInput(3, "c", 0.1, 50),
	}
	first := BuildTemplateSuggestionsByTenant(inputs)
	second := BuildTemplateSuggestionsByTenant(inputs)
	if len(first) != len(second) {
		t.Fatalf("同一输入两次调用长度不同：%d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].TenantID != second[i].TenantID || first[i].TemplateID != second[i].TemplateID {
			t.Errorf("第%d项顺序不稳定：%d/%s vs %d/%s", i, first[i].TenantID, first[i].TemplateID, second[i].TenantID, second[i].TemplateID)
		}
	}
}

// TestPromotionKnobsForTenantAndClamp 晋升旋钮按租户取、并按可用区间钳位。
// 不钳的后果：step=0 每轮白写审计、max 超 100 让一次胜出就独占分流、冷却负数拆掉防叠幅保护。
func TestPromotionKnobsForTenantAndClamp(t *testing.T) {
	restore := cfgWithTenants(t,
		map[string]string{
			"experiment_auto_promote":           "false",
			"experiment_weight_step":            "10",
			"experiment_weight_max":             "100",
			"experiment_promote_cooldown_hours": "72",
		},
		map[uint]map[string]string{
			5: {"experiment_auto_promote": "true", "experiment_weight_step": "25"},
			6: {"experiment_weight_step": "0", "experiment_weight_max": "9999", "experiment_promote_cooldown_hours": "-8"},
		})
	defer restore()

	if autoPromoteEnabledFor(5) != true {
		t.Error("租户5 覆盖了开关，必须判为已开启（系统层 false 不该压住它）")
	}
	if autoPromoteEnabledFor(7) != false {
		t.Error("未覆盖的租户必须沿用系统层 false——L2 默认关是本批的投产前置纪律")
	}
	if k := promotionKnobsFor(5); k.Step != 25 || k.MaxWeight != 100 || k.Cooldown != 72*time.Hour {
		t.Errorf("租户5 旋钮：仅 step 被覆盖，其余沿用系统层，实际=%+v", k)
	}
	k6 := promotionKnobsFor(6)
	if k6.Step != 1 {
		t.Errorf("step=0 应钳到 1（每轮至少前进一格，否则白写审计），实际=%d", k6.Step)
	}
	if k6.MaxWeight != abWeightCeil {
		t.Errorf("max=9999 应钳到量纲上限 %d，实际=%d", abWeightCeil, k6.MaxWeight)
	}
	if k6.Cooldown != 0 {
		t.Errorf("冷却负数应钳到 0（不得反向提前），实际=%v", k6.Cooldown)
	}
}
