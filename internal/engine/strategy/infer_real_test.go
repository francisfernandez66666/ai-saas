// G-8 策略主函数 Infer 真实行为单测（2026-09-24 欠账批一）
//
// 为什么单独一份：`behavior_test.go` 测的是 Step1~Step3 的**单步函数**
// （Step2_5_StageCeiling / Step3_SoftDowngrade / CalcAnchorScoresPromoteLocked …），
// 它们各自正确不等于 Infer 把它们**按顺序、按条件**串起来了——
// 七步公式历史上真出过的三类问题都在"串联"这一层，不在单步里：
//   - P2-57：CurrentStage 越界直接把 [6]int 的 StageAnchorCeiling 打 panic；
//   - P2-57：展示层 AnchorScores 在天花板压制**之前**赋值，界面分数与决策不一致；
//   - M1：召回不做租户过滤，A 家私有话术进 B 家的 AI 召回池（跨租户泄露）。
//
// 本文件全部打在 `(*Engine).Infer` 上，且模板池用内存快照自造，
// 因此"召回了哪一条"只可能由过滤规则决定，不受库里种子数据影响。
package strategy

import (
	"strings"
	"testing"

	"ai-scrm/config"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/testutil"
)

// inferTestEnv 装齐 Infer 依赖的两个全局配置，返回恢复函数。
// 两个键都显式关掉：kb_cross_dept_fallback 关→ 召回可见域不查部门绑定表（用例不造部门数据）；
// template_bandit_enabled 关→ 择臂层退回"规则序"，选中结果只由优先级决定，断言才可复现。
func inferTestEnv() func() {
	oldCfg := config.GlobalConfig
	oldRC := runtimecfg.DefaultSystemConfigService
	config.GlobalConfig = &config.Config{Strategy: config.StrategyConfig{Tau: 1.0, SimThresh: 0.2}}
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(map[string]string{
		"kb_cross_dept_fallback":  "false",
		"template_bandit_enabled": "false",
	}, nil)
	return func() {
		config.GlobalConfig = oldCfg
		runtimecfg.DefaultSystemConfigService = oldRC
	}
}

// newInferEngine 用内存快照装一台引擎（不查库）：模板/卖点完全由用例决定。
// 走 e.data.Store 与 LoadData 发布的是同一个字段，读侧 snapshot() 行为一致。
func newInferEngine(templates []model.Template, features []model.Feature) *Engine {
	e := &Engine{}
	e.data.Store(&cachedData{templates: templates, features: features})
	return e
}

// templatesForAllAnchors 给某个租户铺满 7 个锚类型的启用模板。
// 目的：不论 Step1~3 最终选中哪个锚，池子里都有一条"该租户的"和"别家的"同锚模板可比，
// 租户隔离断言因此与锚打分结果解耦。
func templatesForAllAnchors(tenantID uint, prefix string, priority int) []model.Template {
	out := make([]model.Template, 0, AnchorCount)
	for a := 0; a < AnchorCount; a++ {
		out = append(out, model.Template{
			ID:             prefix + "_" + anchorSuffix(a),
			TenantID:       tenantID,
			AnchorType:     a,
			Name:           prefix + "模板" + anchorSuffix(a),
			PromptTemplate: prefix + "话术正文",
			HookTemplate:   prefix + "钩子正文",
			Priority:       priority,
			Status:         1,
		})
	}
	return out
}

// anchorSuffix 锚类型转字符串后缀（1→"1"，用于拼模板 ID）
func anchorSuffix(a int) string {
	return string(rune('0' + a%10))
}

// baseInferInput 一份"什么都不缺"的输入基线：调用方只覆盖自己要考察的字段。
func baseInferInput(tid uint) StrategyInput {
	tv := [32]float64{}
	tv[0] = 0.8 // 意向
	tv[1] = 0.7 // 信任
	return StrategyInput{
		TVector:        tv,
		State:          model.SessionState{CurrentStage: 5}, // 放到最高心智阶段，避免阶段锁干扰别的判据
		CustomerInput:  "四驱和两驱在雪地里差多少，我平时通勤为主",
		CustomerID:     1001,
		ConversationID: 2001,
		CanPromote:     true,
		TenantID:       tid,
	}
}

// TestInferGreetingShortCircuit 纯寒暄必须走 Step0.1 前置短路：强制不抛锚、
// 且**不进 Step4 召回**（PromptText 留空、分数/概率全零是本分支独有的指纹）。
//
// 反证同池同画像只换一句非寒暄输入：那一趟必须走完 Step4（PromptText 非空）。
// 缺了这条反向腿，"PromptText 为空"就可能只是因为模板池根本没配上。
func TestInferGreetingShortCircuit(t *testing.T) {
	testutil.SetupTestDB(t)
	defer inferTestEnv()()

	pool := append(templatesForAllAnchors(0, "sys", 3), model.Template{
		ID: "tpl_nothrow_001", TenantID: 0, AnchorType: AnchorNoThrow,
		Name: "不抛锚-安抚倾听", PromptTemplate: "预置安抚话术", Status: 1,
	})
	e := newInferEngine(pool, nil)

	in := baseInferInput(0)
	in.CustomerInput = "你好"
	got := e.Infer(in)
	if got.FinalAnchor != AnchorNoThrow || got.OriginalAnchor != AnchorNoThrow {
		t.Errorf("纯寒暄应强制不抛锚，实际 original=%d final=%d", got.OriginalAnchor, got.FinalAnchor)
	}
	if got.TemplateID != "tpl_nothrow_001" {
		t.Errorf("寒暄应固定挂安抚模板，实际 %q", got.TemplateID)
	}
	if got.PromptText != "" {
		t.Errorf("寒暄走短路，不应执行 Step4 填充，实际 PromptText=%q", got.PromptText)
	}
	for a := 0; a < AnchorCount; a++ {
		if got.AnchorScores[a] != 0 || got.AnchorProbs[a] != 0 {
			t.Fatalf("寒暄跳过了 Step1~2，锚%d 的分数/概率应为 0，实际 %.2f/%.4f", a, got.AnchorScores[a], got.AnchorProbs[a])
		}
	}
	// 短路只跳锚链，Step5~7 照跑：紧迫等级与路由必须仍然有结论
	if got.UrgencyLevel == "" || got.RouteResult == "" {
		t.Errorf("寒暄短路不得跳过 Step5~7，实际 urgency=%q route=%q", got.UrgencyLevel, got.RouteResult)
	}

	// 反向腿：同引擎、同画像，只把输入换成实质问题
	normal := e.Infer(baseInferInput(0))
	if normal.PromptText == "" {
		t.Fatal("前置条件破坏：非寒暄输入也没召回出话术，上面那条『PromptText 为空』即为空转")
	}
	if normal.AnchorScores == got.AnchorScores {
		t.Error("非寒暄路径应真实打分，锚分数与寒暄全零向量完全相同说明 Step1 被跳过")
	}
}

// TestInferRecallTenantIsolation Step4 召回的租户隔离：
// 别家私有话术既不能被选中，其正文也不能出现在 PromptText 里。
//
// 判据设计成"别家优先级最高"（oth=9 > own=5 > sys=3）：一旦过滤失效，
// 稳定降序排序的头名必然是别家，泄露是必然结果而不是概率事件。
// 同时以"租户 2 自己来查时确实命中 oth"作反向腿——缺它，"没选到 oth"
// 可能只是因为 oth 压根没进候选（阈值/锚类型不匹配），护栏就是空转。
func TestInferRecallTenantIsolation(t *testing.T) {
	testutil.SetupTestDB(t)
	defer inferTestEnv()()

	pool := append([]model.Template{}, templatesForAllAnchors(0, "sys", 3)...)
	pool = append(pool, templatesForAllAnchors(1, "own", 5)...)
	pool = append(pool, templatesForAllAnchors(2, "oth", 9)...)
	e := newInferEngine(pool, nil)

	// 正向：租户 1 来推理
	in1 := baseInferInput(1)
	got1 := e.Infer(in1)
	if !strings.HasPrefix(got1.TemplateID, "own_") {
		t.Errorf("租户1 应命中自己的私有模板，实际 %q", got1.TemplateID)
	}
	if strings.Contains(got1.PromptText, "oth话术正文") {
		t.Errorf("跨租户话术正文串进了 PromptText（泄露）：%q", got1.PromptText)
	}

	// 反向腿：租户 2 用同一个引擎推理，必须命中 oth（证明它本可被选中）
	in2 := baseInferInput(2)
	got2 := e.Infer(in2)
	if !strings.HasPrefix(got2.TemplateID, "oth_") {
		t.Fatalf("前置条件破坏：租户2 自己都没命中 oth（实际 %q），上面那条隔离断言即为空转", got2.TemplateID)
	}
	if got1.FinalAnchor != got2.FinalAnchor {
		t.Fatalf("两次推理锚不一致（%d vs %d），隔离结论不可比", got1.FinalAnchor, got2.FinalAnchor)
	}

	// fail-closed：tenant=0（无租户语境）只准看见系统预置
	got0 := e.Infer(baseInferInput(0))
	if !strings.HasPrefix(got0.TemplateID, "sys_") {
		t.Errorf("TenantID=0 应 fail-closed 只见系统预置，实际 %q", got0.TemplateID)
	}
}

// TestInferPromoteLockWiredIntoScores Step1.5 促单锁在**主函数里真的被调用**：
// CanPromote=false 时稀缺/代价自担锚的分数必须严格低于放开时，且条件交换不得开启。
//
// 只测 CalcAnchorScoresPromoteLocked 的单步用例证明不了"Infer 记得调它"。
func TestInferPromoteLockWiredIntoScores(t *testing.T) {
	testutil.SetupTestDB(t)
	defer inferTestEnv()()

	e := newInferEngine(templatesForAllAnchors(0, "sys", 5), nil)

	locked := baseInferInput(0)
	locked.CanPromote = false
	gotLocked := e.Infer(locked)

	open := baseInferInput(0)
	open.CanPromote = true
	gotOpen := e.Infer(open)

	for _, a := range []int{AnchorScarcity, AnchorSelfPay} {
		if gotLocked.AnchorScores[a] >= gotOpen.AnchorScores[a] {
			t.Errorf("促单锁未生效：锚%d 锁定时 %.3f ≥ 放开时 %.3f",
				a, gotLocked.AnchorScores[a], gotOpen.AnchorScores[a])
		}
	}
	if gotLocked.ExchangeFlag {
		t.Error("CanPromote=false 时条件交换必须被硬锁关闭（代码层拦截，不给 AI 看到）")
	}
	// 展示层与决策层一致：分数里必须已含锁定的压制（P2-57 那条赋值顺序回归的判据）
	if gotLocked.AnchorScores[AnchorScarcity] == 0 && gotLocked.AnchorScores[AnchorSelfPay] == 0 {
		t.Error("AnchorScores 全零：展示层可能在压制前就取了快照")
	}
}

// TestInferJourneyCeilingClampsAggressiveAnchors Step1.6 线索阶段天花板：
// "ai_connected"（上限 agg=1）必须把 agg>1 的锚全压成 -999，最终锚不得越界；
// 反向腿：同一输入去掉线索阶段后，那些锚必须回到可选区（否则上面的 -999 断言是白断）。
func TestInferJourneyCeilingClampsAggressiveAnchors(t *testing.T) {
	testutil.SetupTestDB(t)
	defer inferTestEnv()()

	e := newInferEngine(templatesForAllAnchors(0, "sys", 5), nil)
	ceiling := JourneyStageAggressivenessCeiling["ai_connected"]

	in := baseInferInput(0)
	in.JourneyStage = "ai_connected"
	got := e.Infer(in)
	for a := 0; a < AnchorCount; a++ {
		if AnchorAggressiveness[a] <= ceiling {
			continue
		}
		if got.AnchorScores[a] > -900 {
			t.Errorf("锚%d(agg=%d) 超过阶段上限 %d 却没被压成 -999，实际 %.2f",
				a, AnchorAggressiveness[a], ceiling, got.AnchorScores[a])
		}
		if got.AnchorProbs[a] != 0 {
			t.Errorf("锚%d 被天花板排除后概率应恒 0，实际 %.4f", a, got.AnchorProbs[a])
		}
	}
	if AnchorAggressiveness[got.FinalAnchor] > ceiling {
		t.Errorf("最终锚 %d(agg=%d) 越过线索阶段上限 %d", got.FinalAnchor, AnchorAggressiveness[got.FinalAnchor], ceiling)
	}

	// 反向腿：不带线索阶段时，激进锚必须没有被天花板动过
	free := e.Infer(baseInferInput(0))
	anyAlive := false
	for a := 0; a < AnchorCount; a++ {
		if AnchorAggressiveness[a] > ceiling && free.AnchorScores[a] > -900 {
			anyAlive = true
		}
	}
	if !anyAlive {
		t.Fatal("前置条件破坏：无天花板时激进锚也全是 -999，上面的压制动即为空转")
	}
}

// TestInferStageOutOfRangeNoPanic P2-57 越界防御：CurrentStage 是非法值（负数/超界）时
// Infer 不得 panic，且 StageCeilingAgg 必须落在 clamp 后的合法档位上。
func TestInferStageOutOfRangeNoPanic(t *testing.T) {
	testutil.SetupTestDB(t)
	defer inferTestEnv()()

	e := newInferEngine(templatesForAllAnchors(0, "sys", 5), nil)
	for _, stage := range []int{-1, 99, len(StageAnchorCeiling) + 7} {
		in := baseInferInput(0)
		in.State.CurrentStage = stage
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("CurrentStage=%d 触发 panic（P2-57 回归）: %v", stage, r)
				}
			}()
			got := e.Infer(in)
			if got.StageCeilingAgg != StageAnchorCeiling[len(StageAnchorCeiling)-1] &&
				got.StageCeilingAgg != StageAnchorCeiling[0] {
				t.Errorf("CurrentStage=%d 的锚上限应落在 clamp 后的档位，实际 %d", stage, got.StageCeilingAgg)
			}
		}()
	}
}
