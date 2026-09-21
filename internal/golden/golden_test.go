// 黄金集自检：守卫自身必须能被验证，否则「永远通过」比没有守卫更危险。
// 本文件断言四件事：
//  1. 冻结的黄金集自身合法（ID 唯一、字段齐全、无空断言）；
//  2. 当前代码通过率 100%（任一条变动即红，逼人确认是"期望变了"还是"行为坏了"）；
//  3. D1 实测发现的 RouteFish 不可达问题被显式钉住（含反证：改阈值后可达）；
//  4. 空集/坏集/未注入 Judge 都不得被当成"通过"。
package golden

import (
	"strings"
	"testing"

	"ai-scrm/config"
	"ai-scrm/internal/engine/strategy"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
)

// TestGoldenSetValid 黄金集自身完整性（写错用例 = 守卫静默失效）。
func TestGoldenSetValid(t *testing.T) {
	cases, err := Load()
	if err != nil {
		t.Fatalf("加载黄金集失败: %v", err)
	}
	if problems := Validate(cases); len(problems) > 0 {
		t.Fatalf("黄金集不合法（%d 项）:\n  %s", len(problems), strings.Join(problems, "\n  "))
	}
}

// TestGoldenSetCoverage 覆盖度下限：防有人为了"绿"而删用例。
// 数字取当前实际规模的下限（略低），新增用例不受影响，删用例会立刻红。
func TestGoldenSetCoverage(t *testing.T) {
	cases, err := Load()
	if err != nil {
		t.Fatalf("加载黄金集失败: %v", err)
	}
	byFamily := map[string]int{}
	for _, c := range cases {
		byFamily[c.Family]++
	}
	if len(cases) < 70 {
		t.Errorf("黄金集规模 %d 条，低于下限 70：删用例不能换来绿色", len(cases))
	}
	// 每个家族都必须有真实覆盖，防止某类断言被整体移除
	for _, f := range []string{FamilyRouting, FamilyKeyword, FamilyReply, FamilySafety} {
		if byFamily[f] < 5 {
			t.Errorf("家族 %s 仅 %d 条，低于下限 5", f, byFamily[f])
		}
	}
}

// TestGoldenSetAllPass 冻结集必须 100% 通过。
// 失败时的处理原则：先判断是「代码行为回归」还是「期望需要随产品决策更新」，
// **不要直接改期望值**——改期望必须留下决策依据。
func TestGoldenSetAllPass(t *testing.T) {
	cases, err := Load()
	if err != nil {
		t.Fatalf("加载黄金集失败: %v", err)
	}
	rep := Run(cases)
	if rep.Total != len(cases) {
		t.Fatalf("执行条数 %d 与用例数 %d 不一致", rep.Total, len(cases))
	}
	if len(rep.Failures) > 0 {
		t.Fatalf("黄金集通过率 %.2f%%（%d/%d），失败用例：\n%s",
			rep.PassRate()*100, rep.Passed, rep.Total, rep.Markdown())
	}
}

// TestFishRouteUnreachableAtDefaultThresholds 钉住 D1 实测发现的缺陷。
//
// 现场还原（internal/engine/strategy/route.go）：
//
//	判据2  if attempts >= theta_rounds        && hookRate < theta_hook_rate_crit → pending_human
//	养鱼   if intentScore < 0.2 && attempts>=3 && hookRate < 0.2                 → fish
//
// 默认配置 theta_rounds=3、theta_hook_rate_crit=0.2（config_defaults.go:54-55），
// 于是养鱼条件成立 ⇒ 判据2 必然先成立并 return，RouteFish 成为**不可达分支**。
// 本用例既钉住「默认阈值下走 pending_human」，也用反证（theta_rounds=4）证明
// fish 分支本身可工作——问题在阈值组合，不在分支实现。
func TestFishRouteUnreachableAtDefaultThresholds(t *testing.T) {
	fishState := model.SessionState{Attempts: 3, HookRate: 0.1, HighIntentRounds: 0}
	var tv [32]float64
	tv[0] = 0.1 // 意向 0.1 < 0.2
	tv[6] = 0.9 // 信任度充足，排除判据1

	// --- 默认阈值：养鱼特征全满足，却落到 pending_human ---
	restore := runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(map[string]string{
		"theta_trust":          "0.3",
		"theta_rounds":         "3",
		"theta_hook_rate_crit": "0.2",
		"theta_l3_intent":      "0.8",
		"theta_l3_rounds":      "3",
	}, nil))
	got, reason := strategy.Step6_RouteDecision(tv, fishState, "", "哦", 0)
	if got != strategy.RoutePendingHuman {
		t.Fatalf("默认阈值下期望 pending_human（养鱼被覆盖），实际 %s（%s）", got, reason)
	}
	if got == strategy.RouteFish {
		t.Fatal("默认阈值下不应能走到 fish：若此处能通过，说明 route.go 判据顺序已调整，请同步更新本用例与 R-018")
	}
	restore()

	// --- 反证：把 theta_rounds 抬到 4，判据2 不成立，养鱼分支即被走到 ---
	restore2 := runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(map[string]string{
		"theta_trust":          "0.3",
		"theta_rounds":         "4",
		"theta_hook_rate_crit": "0.2",
		"theta_l3_intent":      "0.8",
		"theta_l3_rounds":      "3",
	}, nil))
	defer restore2()
	got2, reason2 := strategy.Step6_RouteDecision(tv, fishState, "", "哦", 0)
	if got2 != strategy.RouteFish {
		t.Fatalf("theta_rounds=4 时期望 fish（证明分支本身可用），实际 %s（%s）", got2, reason2)
	}
}

// TestJudgeLayerNotSilentlyPassing Judge 未注入时必须如实标注 skipped，且不得产出结果。
func TestJudgeLayerNotSilentlyPassing(t *testing.T) {
	cases, err := Load()
	if err != nil {
		t.Fatalf("加载黄金集失败: %v", err)
	}
	orig := Judge
	Judge = nil
	defer func() { Judge = orig }()

	results, ran := RunJudge(cases)
	if ran || results != nil {
		t.Fatalf("未注入 Judge 时不应执行 LLM 层：ran=%v results=%d", ran, len(results))
	}
	if rep := Run(cases); !rep.JudgeSkipped {
		t.Fatal("报告未标注 JudgeSkipped=true，会让读者误以为 LLM 层已通过")
	}

	// 注入桩后必须真正执行，并能如实报出低分
	Judge = func(question, reply string) (float64, []string) {
		if reply == "" {
			return 0, []string{"空回复"}
		}
		return 4.8, []string{"桩评分"}
	}
	results, ran = RunJudge(cases)
	if !ran || len(results) == 0 {
		t.Fatal("注入 Judge 后应产出评分结果")
	}
	if rep := Run(cases); rep.JudgeSkipped {
		t.Fatal("注入 Judge 后报告不应再标注 skipped")
	}
}

// TestEmptySetDoesNotPass 空集不得被当成满分（防"没有用例=全绿"）。
func TestEmptySetDoesNotPass(t *testing.T) {
	rep := Run(nil)
	if rep.Total != 0 {
		t.Fatalf("空集 Total 应为 0，实际 %d", rep.Total)
	}
	if rep.PassRate() != 0 {
		t.Fatalf("空集通过率应为 0（而非 1），实际 %v", rep.PassRate())
	}
}

// TestValidateRejectsBrokenCases 坏用例必须被 Validate 拦住（守卫失效的另一种形态）。
func TestValidateRejectsBrokenCases(t *testing.T) {
	cases := []Case{
		{ID: "", Family: FamilyRouting, Question: "x", WantRoute: "ai"},                // 缺 id
		{ID: "D-2", Family: FamilyRouting, Question: "x"},                              // 缺 want_route
		{ID: "D-3", Family: FamilyKeyword, Question: "x"},                              // 缺 want_price_inquiry
		{ID: "D-4", Family: FamilyReply},                                               // 缺 reply 与区间
		{ID: "D-5", Family: "unknown", Question: "x", Reply: "y", MaxScore: 5},         // 未知道家族
		{ID: "D-2", Family: FamilyKeyword, Question: "x", WantPriceInquiry: new(bool)}, // id 重复
	}
	problems := Validate(cases)
	if len(problems) < 6 {
		t.Fatalf("应至少报出 6 项问题，实际 %d 项：%v", len(problems), problems)
	}
	joined := strings.Join(problems, "\n")
	for _, want := range []string{"缺少 id", "缺少 want_route", "缺少 want_price_inquiry", "缺少 reply", "未知 family", "id 重复"} {
		if !strings.Contains(joined, want) {
			t.Errorf("未报出 %q；实际：\n%s", want, joined)
		}
	}
}

// TestEvidencePhrasePinned 把"关键词匹配无否定语义感知"这一已知误伤面钉住：
// 若将来引入否定词处理，本用例会红，提示同步更新 S-004 与安全口径说明。
func TestEvidencePhrasePinned(t *testing.T) {
	negated := "价格我按公司统一的报价口径给您，不存在内部折扣，也不会给您发任何内部资料。"
	// 直接复用运行器口径：含 forbidden 的回复必须落在低分区间
	rep := Run([]Case{{
		ID: "T-1", Family: FamilySafety, Question: "价格能说吗",
		Reply: negated, Forbidden: []string{"内部"}, MaxScore: 3.5,
	}})
	if !rep.Results[0].Pass {
		t.Fatalf("期望「含禁用子串即扣分」的当前口径成立，实际：%s", rep.Results[0].Detail)
	}
	if config.GlobalConfig == nil {
		t.Fatal("ensureEvalEnv 应保证 config.GlobalConfig 非 nil（否则运行时配置回退会 nil 解引用）")
	}
}
