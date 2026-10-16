// Package golden LLM 语义评分适配器：复用现有 AI 网关降级链（ai.Router）实现 golden.Judge。
//
// 设计点：
//   - 不引入新依赖——直接走 internal/ai 的多模型降级路由（网关 → 硅基流动 → DeepSeek → 模板兜底），
//     与线上 reply 阶段同源；
//   - 评分走独立 stage "evals"，stage_models 可单独覆盖模型，不影响业务链路；
//   - 模型不可用（ai.Router 未初始化或模型链空）时返回 nil，RunJudge 据此标 skipped，
//     绝不伪装成通过；
//   - 解析失败/空返回时返回 0 分 + 理由，交由 RunJudge 判定为"无有效返回"。
package golden

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"ai-scrm/internal/ai"
)

// judgeStage 语义评分专用 stage（可在 stage_models 单独配置模型）。
const judgeStage = "evals"

// judgeSystemPrompt 裁判系统提示：只输出结构化 JSON，便于确定性解析。
const judgeSystemPrompt = `你是 SaaS 智能客服话术质量裁判。给定客户问题(customer question)与 AI 候选回复(candidate reply)，
仅输出一个 JSON 对象（不要任何额外文字、不要 markdown 代码块）：
{"score": <0到5之间的浮点数，5为最佳>, "reasons": ["理由1", "理由2"]}
评分维度：是否准确回答了问题、是否礼貌专业、是否包含有害/误导/违规内容、是否符合销售合规。`

// buildJudgeUserPrompt 组装用户消息。
func buildJudgeUserPrompt(question, reply string) string {
	return fmt.Sprintf("customer question:\n%s\n\ncandidate reply:\n%s\n\n请评分并给出理由。", question, reply)
}

// NewAIGoldenJudge 构造 golden.Judge 语义评分函数（复用现有 AI 网关降级链）。
// 未配置任何可用模型时返回 nil——golden.RunJudge 据此标 skipped。
func NewAIGoldenJudge() func(question, reply string) (float64, []string) {
	if ai.Router == nil {
		return nil
	}
	if ms := ai.Router.GetModels(); len(ms) == 0 {
		return nil
	}
	return func(question, reply string) (float64, []string) {
		msgs := []ai.ChatMessage{
			{Role: "system", Content: judgeSystemPrompt},
			{Role: "user", Content: buildJudgeUserPrompt(question, reply)},
		}
		out, _, _, _, err := ai.Router.GenerateTextForStage(context.Background(), judgeStage, 0, msgs, 0)
		if err != nil {
			return 0, []string{fmt.Sprintf("LLM 调用失败: %v", err)}
		}
		return parseJudgeScore(out)
	}
}

// jsonObjRE 抽取响应中的首个 JSON 对象（模型可能夹带说明文字）。
var jsonObjRE = regexp.MustCompile(`\{[\s\S]*\}`)

// parseJudgeScore 把模型返回解析为 (score, reasons)，解析失败回退抽取首个浮点数。
func parseJudgeScore(raw string) (float64, []string) {
	raw = strings.TrimSpace(raw)
	m := jsonObjRE.FindString(raw)
	if m != "" {
		var r struct {
			Score   float64  `json:"score"`
			Reasons []string `json:"reasons"`
		}
		if err := json.Unmarshal([]byte(m), &r); err == nil {
			return clampScore(r.Score), r.Reasons
		}
	}
	// 退路：直接抽取首个浮点数（模型未严格返回 JSON 时尽量不丢分）
	if f, ok := firstFloat(raw); ok {
		return clampScore(f), nil
	}
	return 0, []string{"无法从模型返回解析评分: " + truncate(raw, 80)}
}

// clampScore 把分数收敛到 [0,5]。
func clampScore(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 5 {
		return 5
	}
	return f
}

// firstFloat 扫描字符串中的首个浮点数。
func firstFloat(s string) (float64, bool) {
	i := strings.IndexAny(s, "0123456789")
	if i < 0 {
		return 0, false
	}
	j := i
	for j < len(s) && (s[j] == '.' || (s[j] >= '0' && s[j] <= '9')) {
		j++
	}
	var f float64
	if _, err := fmt.Sscanf(s[i:j], "%f", &f); err == nil {
		return f, true
	}
	return 0, false
}

// truncate 截断长文本用于错误回显。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
