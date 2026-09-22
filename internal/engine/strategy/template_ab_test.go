// E4 话术 A/B 实验召回单测（2026-09-19 增强批）：
// 零漂移 / 同客户恒定 / 权重分桶 / 权重0不吃流量 / 全0回退确定性 / 草稿不进召回
package strategy

import (
	"ai-scrm/config"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"testing"
)

// abTestSetup 初始化召回阈值等静态配置，保证 Step4_RecallTemplate 可独立运行
func abTestSetup(t *testing.T) {
	t.Helper()
	if config.GlobalConfig == nil {
		config.GlobalConfig = &config.Config{Strategy: config.StrategyConfig{Tau: 1.0}}
	}
	if runtimecfg.DefaultSystemConfigService == nil {
		runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(nil, nil)
	}
}

// abVariant 构造一个"同类锚 + price 标签满匹配"的实验模板（同组 variant 分数逐位相同）
func abVariant(id, group string, weight, status int) model.Template {
	return model.Template{
		ID:             id,
		AnchorType:     2,
		Status:         status,
		TriggerTags:    `["price"]`,
		PromptTemplate: "话术" + id,
		AbGroup:        group,
		AbWeight:       weight,
	}
}

var abTags = []string{"price"}
var abZeroT [32]float64

func recallFor(t *testing.T, templates []model.Template, customerID uint) string {
	t.Helper()
	got, _ := Step4_RecallTemplate(2, abTags, abZeroT, templates, customerID, 0)
	if got == nil {
		t.Fatalf("召回为空，期望命中模板")
	}
	return got.ID
}

// TestRecallABZeroDrift 无实验组时行为与旧版一致：任何 customerID 都命中同一最高分模板
func TestRecallABZeroDrift(t *testing.T) {
	abTestSetup(t)
	templates := []model.Template{
		abVariant("tpl_plain_low", "", 0, 1),
		abVariant("tpl_plain_high", "", 0, 1),
	}
	// 用优先级拉开分差：high 恒赢，与 customerID 无关
	templates[1].Priority = 10
	for _, cid := range []uint{0, 1, 42, 99999} {
		if id := recallFor(t, templates, cid); id != "tpl_plain_high" {
			t.Errorf("零漂移破坏: customer=%d 命中 %s，期望 tpl_plain_high", cid, id)
		}
	}
}

// TestRecallABStablePerCustomer 同客户恒定同 variant，且 50/50 双 variant 都有流量
func TestRecallABStablePerCustomer(t *testing.T) {
	abTestSetup(t)
	templates := []model.Template{
		abVariant("tpl_a", "exp_price", 50, 1),
		abVariant("tpl_b", "exp_price", 50, 1),
	}
	hits := map[string]int{}
	for cid := uint(1); cid <= 500; cid++ {
		first := recallFor(t, templates, cid)
		// 同客户重复调用必须稳定（哈希确定性，不依赖随机数）
		for i := 0; i < 3; i++ {
			if again := recallFor(t, templates, cid); again != first {
				t.Fatalf("同客户分流漂移: customer=%d %s vs %s", cid, first, again)
			}
		}
		hits[first]++
	}
	if hits["tpl_a"] == 0 || hits["tpl_b"] == 0 {
		t.Errorf("双 variant 应都有流量: %+v", hits)
	}
	if hits["tpl_a"] < 175 || hits["tpl_b"] < 175 {
		t.Errorf("50/50 分流严重失衡: %+v", hits)
	}
}

// TestRecallABWeightedSplit 70/30 权重按份额分配
func TestRecallABWeightedSplit(t *testing.T) {
	abTestSetup(t)
	templates := []model.Template{
		abVariant("tpl_heavy", "exp_w", 70, 1),
		abVariant("tpl_light", "exp_w", 30, 1),
	}
	hits := map[string]int{}
	for cid := uint(1); cid <= 1000; cid++ {
		hits[recallFor(t, templates, cid)]++
	}
	// fnv32 非均匀分布，只断言量级：heavy 显著多于 light，且双方非零
	if hits["tpl_heavy"] <= hits["tpl_light"] {
		t.Errorf("70/30 权重未体现: %+v", hits)
	}
	if hits["tpl_light"] < 200 {
		t.Errorf("light 组流量过少: %+v", hits)
	}
}

// TestRecallABZeroWeightGetsNoTraffic 权重 0 的 variant 只作对照备份，不吃分流流量
func TestRecallABZeroWeightGetsNoTraffic(t *testing.T) {
	abTestSetup(t)
	templates := []model.Template{
		abVariant("tpl_on", "exp_z", 100, 1),
		abVariant("tpl_off", "exp_z", 0, 1),
	}
	for cid := uint(1); cid <= 300; cid++ {
		if id := recallFor(t, templates, cid); id != "tpl_on" {
			t.Fatalf("权重0 variant 抢了流量: customer=%d -> %s", cid, id)
		}
	}
}

// TestRecallABAllZeroWeightFallsBack 组内全 0 权重 = 未配置分流，回退确定性最高分
func TestRecallABAllZeroWeightFallsBack(t *testing.T) {
	abTestSetup(t)
	templates := []model.Template{
		abVariant("tpl_first", "exp_zero", 0, 1),
		abVariant("tpl_second", "exp_zero", 0, 1),
	}
	for cid := uint(1); cid <= 100; cid++ {
		if id := recallFor(t, templates, cid); id != "tpl_first" {
			t.Fatalf("全0权重应确定性命中首位: customer=%d -> %s", cid, id)
		}
	}
}

// TestRecallABDraftExcluded 草稿（status=2）不参与召回——引擎 LoadData 也只加载 status=1，
// 此处在召回层再断言一次谓词，防未来有人放宽 LoadData 时草稿泄漏进线上
func TestRecallABDraftExcluded(t *testing.T) {
	abTestSetup(t)
	draft := abVariant("tpl_draft", "exp_d", 100, 2)
	draft.Priority = 999 // 即使分数碾压也不许被召回
	templates := []model.Template{
		draft,
		abVariant("tpl_live", "", 0, 1),
	}
	for cid := uint(1); cid <= 50; cid++ {
		if id := recallFor(t, templates, cid); id != "tpl_live" {
			t.Fatalf("草稿被召回: customer=%d -> %s", cid, id)
		}
	}
}

// TestRecallABGroupIsolation 不同实验组之间不互相分桶：各自组内择一
func TestRecallABGroupIsolation(t *testing.T) {
	abTestSetup(t)
	// g2 分更高（优先级加权），best 落在 g2 → 只在 g2 内分桶，g1 variant 不参与
	templates := []model.Template{
		abVariant("tpl_g1a", "g1", 50, 1),
		abVariant("tpl_g1b", "g1", 50, 1),
		abVariant("tpl_g2", "g2", 100, 1),
	}
	templates[2].Priority = 10
	for cid := uint(1); cid <= 100; cid++ {
		if id := recallFor(t, templates, cid); id != "tpl_g2" {
			t.Fatalf("跨组误分流: customer=%d -> %s", cid, id)
		}
	}
}

// TestABHashBucketDeterministic 分桶哈希纯函数性质：同入参恒同值，组名参与散列
func TestABHashBucketDeterministic(t *testing.T) {
	if abHashBucket(42, "g1") != abHashBucket(42, "g1") {
		t.Error("哈希不稳定")
	}
	if abHashBucket(42, "g1") == abHashBucket(42, "g2") {
		t.Error("组名未参与散列")
	}
	if abHashBucket(42, "g1") == abHashBucket(43, "g1") {
		t.Error("客户ID未参与散列")
	}
}
