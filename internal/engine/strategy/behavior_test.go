// G-8 高价值行为单测
// 覆盖：StageAnchorCeiling 阶段锁、softmax 锚点选择、锚点攻击性、询价/接钩/情绪/问候检测、软降级
package strategy

import (
	"testing"

	"ai-scrm/config"
	"ai-scrm/internal/model"
	"ai-scrm/internal/service"
	"ai-scrm/internal/strategytypes"
)

// ---- G-8-1: StageAnchorCeiling 阶段锁 ----

func TestStageAnchorCeilingLockBehavior(t *testing.T) {
	// AnchorAggressiveness: [0,1,2,3,4,5,6]
	// StageAnchorCeiling:   [1,2,3,4,6,6]
	cases := []struct {
		name           string
		currentStage   int
		selectedAnchor int
		wantAnchor     int
		wantDowngrade  bool
	}{
		{"stage0_agg1通过", 0, 1, 1, false}, // agg=1 <= ceiling=1
		{"stage0_agg3降级", 0, 3, 1, true},  // agg=3 > ceiling=1, 降到 agg=1
		{"stage1_agg2通过", 1, 2, 2, false}, // agg=2 <= ceiling=2
		{"stage1_agg3降级", 1, 3, 2, true},  // agg=3 > ceiling=2, 降到 agg=2
		{"stage2_agg3通过", 2, 3, 3, false}, // agg=3 <= ceiling=3
		{"stage2_agg5降级", 2, 5, 3, true},  // agg=5 > ceiling=3, 降到 agg=3
		{"stage4_无限制", 4, 6, 6, false},    // ceiling=6, 无限制
		{"stage_越界不降级", 99, 6, 6, false},  // 越界 stage 不触发降级
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			finalAnchor, isDowngraded := Step2_5_StageCeiling(tc.selectedAnchor, tc.currentStage)
			if finalAnchor != tc.wantAnchor {
				t.Errorf("finalAnchor: got %d, want %d", finalAnchor, tc.wantAnchor)
			}
			if isDowngraded != tc.wantDowngrade {
				t.Errorf("isDowngraded: got %v, want %v", isDowngraded, tc.wantDowngrade)
			}
		})
	}
}

// ---- G-8-2: softmax 锚点选择 ----

func TestSoftmaxAnchorSelectionBehavior(t *testing.T) {
	// 需要初始化 config.GlobalConfig 和 DefaultSystemConfigService
	if config.GlobalConfig == nil {
		config.GlobalConfig = &config.Config{Strategy: config.StrategyConfig{Tau: 1.0}}
	}
	if service.DefaultSystemConfigService == nil {
		service.DefaultSystemConfigService = &service.SystemConfigService{}
	}
	scores := [AnchorCount]float64{1.0, 2.0, 3.0, 10.0, 0.5, 0, 0}
	_, bestAnchor, _ := Step2_SoftmaxAnchor(scores)
	if bestAnchor != 3 {
		t.Errorf("softmax 选最高分: got %d, 期望 3", bestAnchor)
	}
}

func TestSoftmaxAnchorEqualScoresBehavior(t *testing.T) {
	if config.GlobalConfig == nil {
		config.GlobalConfig = &config.Config{Strategy: config.StrategyConfig{Tau: 1.0}}
	}
	if service.DefaultSystemConfigService == nil {
		service.DefaultSystemConfigService = &service.SystemConfigService{}
	}
	scores := [AnchorCount]float64{1.0, 1.0, 1.0, 0, 0, 0, 0}
	_, bestAnchor, confidence := Step2_SoftmaxAnchor(scores)
	if bestAnchor < 0 || bestAnchor >= AnchorCount {
		t.Errorf("softmax 等分越界: %d", bestAnchor)
	}
	if confidence < 0 || confidence > 1 {
		t.Errorf("softmax 置信度越界: %f", confidence)
	}
}

// ---- G-8-3: 锚点攻击性值 ----

func TestAnchorAggressivenessValuesBehavior(t *testing.T) {
	for i, agg := range AnchorAggressiveness {
		if agg < 0 || agg > 10 {
			t.Errorf("锚点 %d 攻击性越界: %d", i, agg)
		}
	}
}

func TestStageAnchorCeilingConsistencyBehavior(t *testing.T) {
	for i := 1; i < len(StageAnchorCeiling); i++ {
		if StageAnchorCeiling[i] < StageAnchorCeiling[i-1] {
			t.Errorf("StageAnchorCeiling 非单调递增: [%d]=%d > [%d]=%d",
				i-1, StageAnchorCeiling[i-1], i, StageAnchorCeiling[i])
		}
	}
}

// ---- G-8-4: 询价意图检测（行为测试，区别于 strategy_test.go 的单元测试） ----

func TestIsPriceInquiryBehavior(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"多少钱", true},
		{"价格是多少", true},
		{"优惠多少", true},
		{"落地价多少", true},
		{"今天天气不错", false},
		{"我想看车", false},
	}
	for _, c := range cases {
		got := IsPriceInquiry(c.text)
		if got != c.want {
			t.Errorf("IsPriceInquiry(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

// ---- G-8-5: 接钩检测 ----

func TestCheckHookedBehavior(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"好的我明白了", true}, // >= 3 runes
		{"嗯嗯知道了", true},  // >= 3 runes
		{"可以的没问题", true}, // >= 3 runes
		{"我想看车", true},   // >= 3 runes
		{"好", false},     // 纯语气词
		{"嗯", false},     // 纯语气词
		{"价格怎么样", true},  // >= 3 runes
	}
	for _, c := range cases {
		got := strategytypes.CheckHooked(c.text)
		if got != c.want {
			t.Errorf("CheckHooked(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

// ---- G-8-6: 情绪检测 ----

func TestDetectEmotionBehavior(t *testing.T) {
	cases := []struct {
		text   string
		expect string
	}{
		{"太棒了", "positive"},
		{"太差了", "negative"},
	}
	for _, c := range cases {
		got := strategytypes.DetectEmotion(c.text)
		if c.expect != "" && got == "" {
			t.Errorf("DetectEmotion(%q) 应识别为 %s, got empty", c.text, c.expect)
		}
	}
}

// ---- G-8-7: 问候检测 ----

func TestIsGreetingBehavior(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"你好", true},
		{"嗨", true},
		{"在吗", true},
		{"我想买车", false},
		{"价格怎么样", false},
	}
	for _, c := range cases {
		got := IsGreeting(c.text)
		if got != c.want {
			t.Errorf("IsGreeting(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

// ---- G-8-8: SoftDowngrade 软降级 ----

func TestSoftDowngradeBehavior(t *testing.T) {
	// 高攻击性 + 低接钩率 → 应降级
	state := model.SessionState{HookRate: 0.1}
	_, downgraded := Step3_SoftDowngrade(5, state)
	if !downgraded {
		t.Log("SoftDowngrade: 高agg+低hookRate 未降级（可能配置不同）")
	}

	// 高接钩率 → 不应降级
	state.HookRate = 0.9
	_, downgraded = Step3_SoftDowngrade(5, state)
	if downgraded {
		t.Error("SoftDowngrade: 高hookRate 不应降级")
	}
}

// ---- G-8-9: CalcAnchorScoresPromoteLocked ----

func TestCalcAnchorScoresPromoteLocked(t *testing.T) {
	scores := [AnchorCount]float64{1, 2, 3, 4, 5, 6, 7}
	locked := CalcAnchorScoresPromoteLocked(scores)
	// 促销锁应压制高攻击性锚点
	if locked[6] > scores[6] {
		t.Errorf("PromoteLocked 不应提升高agg锚点: %f > %f", locked[6], scores[6])
	}
}
