// 黄金集运行器：逐条执行断言、汇总报告、按家族统计通过率。
// 确定性层不联网/不读库/不烧 token，可直接进 CI 门禁；
// LLM 评分层通过 Judge 变量注入（未注入则整层跳过，报告中显式标注 skipped，不伪装成通过）。
package golden

import (
	"fmt"
	"sort"
	"strings"

	"ai-scrm/config"
	"ai-scrm/internal/engine/strategy"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/service"
)

// Judge LLM 语义评分钩子（可选）：输入客户问题与该问题的候选回复，返回 0~5 分与理由。
// 由调用方（服务端组合根 / 带 Key 的发布前流水线）注入；未注入时 LLM 层整体标记为 skipped。
// 不放进本包默认路径的原因：需要真实模型 Key + stage_models 配置，属网络与成本行为，
// 不能出现在每次提交都会跑的 CI 门禁里。
var Judge func(question, reply string) (float64, []string)

// Result 单条用例的执行结果。
type Result struct {
	Case   Case     `json:"case"`
	Pass   bool     `json:"pass"`
	Detail string   `json:"detail,omitempty"` // 失败原因（通过时为空）
	Score  *float64 `json:"score,omitempty"`  // reply/safety 家族的实得离线分
}

// FamilyStat 家族维度统计。
type FamilyStat struct {
	Total  int `json:"total"`
	Passed int `json:"passed"`
}

// Report 整轮黄金集报告。
type Report struct {
	Total    int                   `json:"total"`
	Passed   int                   `json:"passed"`
	ByFamily map[string]FamilyStat `json:"by_family"`
	Results  []Result              `json:"results"`
	Failures []Result              `json:"failures"`
	// JudgeSkipped 为真表示 LLM 评分层未注入、本轮未执行（报告中如实标注）。
	JudgeSkipped bool `json:"judge_skipped"`
}

// PassRate 整体通过率（空集视为 0，避免"没有用例=满分"的静默通过）。
func (r Report) PassRate() float64 {
	if r.Total == 0 {
		return 0
	}
	return float64(r.Passed) / float64(r.Total)
}

// ensureEvalEnv 装配确定性层所需的最小配置环境。
//
// 为什么不用 config.LoadConfig()：该函数在校验到弱 JWT_SECRET 且非显式 debug 时会
// log.Fatalf 直接退出进程——评估脚本不该因为密钥策略被杀。
// 改用内存配置桩把路由阈值显式钉住（与 config.go 的 getEnv 默认值同源口径），
// 同时保证 config.GlobalConfig 非 nil（避免 SafeCfgFloat 退化路径读到 nil 指针）。
func ensureEvalEnv() {
	if config.GlobalConfig == nil {
		config.GlobalConfig = &config.Config{}
	}
	if runtimecfg.DefaultSystemConfigService == nil {
		runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(map[string]string{
			"theta_trust":          "0.3",
			"theta_rounds":         "3",
			"theta_hook_rate_crit": "0.2",
			"theta_l3_intent":      "0.8",
			"theta_l3_rounds":      "3",
		}, nil)
	}
}

// Run 执行整轮黄金集（确定性层 + 可选 LLM 层）。
func Run(cases []Case) Report {
	ensureEvalEnv()
	rep := Report{ByFamily: map[string]FamilyStat{}, JudgeSkipped: Judge == nil}
	for _, c := range cases {
		res := runOne(c)
		rep.Total++
		rep.Results = append(rep.Results, res)
		st := rep.ByFamily[c.Family]
		st.Total++
		if res.Pass {
			st.Passed++
			rep.Passed++
		} else {
			rep.Failures = append(rep.Failures, res)
		}
		rep.ByFamily[c.Family] = st
	}
	return rep
}

// runOne 执行单条用例。
func runOne(c Case) Result {
	switch c.Family {
	case FamilyRouting:
		return runRouting(c)
	case FamilyKeyword:
		return runKeyword(c)
	case FamilyReply, FamilySafety:
		return runReplyScore(c)
	default:
		return Result{Case: c, Pass: false, Detail: "未知 family: " + c.Family}
	}
}

// runRouting 路由决策断言：Step6 结果必须等于 want_route。
// 用租户 0（无行业绑定）以保证判定与 DB 无关，可离线复现。
func runRouting(c Case) Result {
	var tv [32]float64
	tv[0] = c.IntentScore // tVector[0]=意向分
	tv[6] = c.TrustLevel  // tVector[6]=信任度
	state := model.SessionState{
		Attempts:         c.Attempts,
		HookRate:         c.HookRate,
		Emotion:          c.Emotion,
		HighIntentRounds: c.HighIntentRounds,
	}
	got, reason := strategy.Step6_RouteDecision(tv, state, "", c.Question, 0)
	if got != c.WantRoute {
		return Result{Case: c, Pass: false, Detail: fmt.Sprintf("路由=%s(期望 %s) 实际理由=%q", got, c.WantRoute, reason)}
	}
	return Result{Case: c, Pass: true}
}

// runKeyword 询价判定断言。
func runKeyword(c Case) Result {
	got := strategy.IsPriceInquiryForTenant(c.Question, 0)
	if c.WantPriceInquiry == nil {
		return Result{Case: c, Pass: false, Detail: "未设置 want_price_inquiry"}
	}
	if got != *c.WantPriceInquiry {
		return Result{Case: c, Pass: false, Detail: fmt.Sprintf("询价判定=%v(期望 %v)", got, *c.WantPriceInquiry)}
	}
	return Result{Case: c, Pass: true}
}

// runReplyScore 离线话术评分断言：分数必须落在 [MinScore, MaxScore]。
// safety 家族分两类断言：
//   - 回复**确实含**禁用词 → 必须既落在低分区间，又给出「违规词」理由
//     （防止"分对了但原因不对"的假通过，也保证违规扣分的可解释性）；
//   - 回复**不含**禁用词（正向样本）→ 只按分数区间断言，用于守住「不得误扣」。
func runReplyScore(c Case) Result {
	res := service.ScoreReplyOffline(c.Reply, c.Anchors, c.Forbidden)
	score := res.Score
	r := Result{Case: c, Score: &score}

	if c.MinScore > 0 && score < c.MinScore {
		r.Detail = fmt.Sprintf("得分 %.2f 低于下限 %.2f（理由：%s）", score, c.MinScore, strings.Join(res.Reasons, "；"))
		return r
	}
	if c.MaxScore > 0 && score > c.MaxScore {
		r.Detail = fmt.Sprintf("得分 %.2f 高于上限 %.2f（理由：%s）", score, c.MaxScore, strings.Join(res.Reasons, "；"))
		return r
	}
	if c.Family == FamilySafety && containsAny(c.Reply, c.Forbidden) {
		hit := false
		for _, reason := range res.Reasons {
			if strings.Contains(reason, "违规词") {
				hit = true
				break
			}
		}
		if !hit {
			r.Detail = fmt.Sprintf("回复含禁用词但未给出违规词理由（理由：%s）", strings.Join(res.Reasons, "；"))
			return r
		}
	}
	r.Pass = true
	return r
}

// containsAny 判断文本是否命中任一关键词（空词/空文本安全）。
func containsAny(text string, words []string) bool {
	for _, w := range words {
		if w != "" && strings.Contains(text, w) {
			return true
		}
	}
	return false
}

// JudgeCases 返回需要 LLM 语义评分的用例（reply 家族且有 reply 文本）。
// routing/keyword 家族是确定性判定，不需要也不应让 LLM 复评。
func JudgeCases(cases []Case) []Case {
	var out []Case
	for _, c := range cases {
		if c.Family == FamilyReply && strings.TrimSpace(c.Reply) != "" {
			out = append(out, c)
		}
	}
	return out
}

// JudgeResult 单条 LLM 评分结果。
type JudgeResult struct {
	ID     string   `json:"id"`
	Score  float64  `json:"score"`
	Pass   bool     `json:"pass"`
	Detail string   `json:"detail,omitempty"`
	Reason []string `json:"reason,omitempty"`
}

// RunJudge 用注入的 Judge 对候选回复跑一轮语义评分。
// 未注入 Judge 时返回 nil 与 false（调用方须据此标注 skipped，不得当成通过）。
func RunJudge(cases []Case) ([]JudgeResult, bool) {
	if Judge == nil {
		return nil, false
	}
	targets := JudgeCases(cases)
	out := make([]JudgeResult, 0, len(targets))
	for _, c := range targets {
		score, reasons := Judge(c.Question, c.Reply)
		jr := JudgeResult{ID: c.ID, Score: score, Reason: reasons}
		switch {
		case score <= 0:
			jr.Detail = "LLM 评分无有效返回（未配置 evals 阶段模型/Key）"
		case score < c.MinScore:
			jr.Detail = fmt.Sprintf("LLM 评分 %.2f 低于下限 %.2f", score, c.MinScore)
		default:
			jr.Pass = true
		}
		out = append(out, jr)
	}
	return out, true
}

// Markdown 生成可交付的评估报告（人类可读 + 失败用例可定位）。
func (r Report) Markdown() string {
	var b strings.Builder
	b.WriteString("# AI 黄金问答集评估报告\n\n")
	fmt.Fprintf(&b, "- 用例总数：**%d**，通过：**%d**，通过率：**%.2f%%**\n", r.Total, r.Passed, r.PassRate()*100)
	if r.JudgeSkipped {
		b.WriteString("- LLM 语义评分层：**未注入 Judge，本轮未执行**（确定性层不受影响）\n")
	} else {
		b.WriteString("- LLM 语义评分层：**已注入并执行**\n")
	}
	b.WriteString("\n## 家族维度\n\n| 家族 | 用例 | 通过 | 通过率 |\n|---|---:|---:|---:|\n")
	families := make([]string, 0, len(r.ByFamily))
	for k := range r.ByFamily {
		families = append(families, k)
	}
	sort.Strings(families)
	for _, f := range families {
		st := r.ByFamily[f]
		rate := 0.0
		if st.Total > 0 {
			rate = float64(st.Passed) / float64(st.Total) * 100
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %.1f%% |\n", f, st.Total, st.Passed, rate)
	}
	if len(r.Failures) == 0 {
		b.WriteString("\n## 失败用例\n\n无。\n")
		return b.String()
	}
	b.WriteString("\n## 失败用例\n\n| ID | 家族 | 输入 | 期望 | 实际 |\n|---|---|---|---|---|\n")
	for _, f := range r.Failures {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", f.Case.ID, f.Case.Family, oneLine(f.Case.Question), expectedOf(f.Case), oneLine(f.Detail))
	}
	return b.String()
}

// expectedOf 把用例期望渲染成单行文本（失败表用）。
func expectedOf(c Case) string {
	switch c.Family {
	case FamilyRouting:
		return "route=" + c.WantRoute
	case FamilyKeyword:
		if c.WantPriceInquiry == nil {
			return "price_inquiry=?"
		}
		return fmt.Sprintf("price_inquiry=%v", *c.WantPriceInquiry)
	default:
		return fmt.Sprintf("score∈[%.1f,%.1f]", c.MinScore, c.MaxScore)
	}
}

// oneLine 把可能含换行/竖线的文本压成表格安全的一行。
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.TrimSpace(s)
	if s == "" {
		return "-"
	}
	if rs := []rune(s); len(rs) > 60 {
		return string(rs[:60]) + "…"
	}
	return s
}
