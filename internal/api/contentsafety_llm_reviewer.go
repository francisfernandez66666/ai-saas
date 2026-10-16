// Package api 内容安全 LLM 机审装配（P2-5）。
//
// 背景：contentsafety.Reviewer（第三方机审）此前仅测试注入，无生产装配点——
// 线上出站回复只过本地词库 DFA，语义级违规（谐音、变体、上下文违规）漏拦。
//
// 本文件补齐生产装配点：基于现有 AI 网关降级链实现 LLMReviewer，fail-open
// （调用/解析异常一律判"不违规"，绝不因外部抖动阻断对话，与 contentsafety 包注释同口径）。
//
// 关键纪律：
//   - 默认不装配（env CONTENTSAFETY_LLM_REVIEWER != "true"）——避免每次出站回复
//     额外一次 LLM 调用带来的成本与延迟；需要 shadow 演练误杀率时再置 env 开启；
//   - 本装配**不改变 contentsafety_mode**（默认 shadow 恒放行只计数）——审核姿态
//     由平台热开关 contentsafety_mode 控制，切 enforce 是产品灰度决策，不在此静默翻转。
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"

	"ai-scrm/internal/ai"
	"ai-scrm/internal/contentsafety"
)

// llmReviewerStage 内容安全机审专用 stage（stage_models 可单独覆盖模型）。
const llmReviewerStage = "content_safety_review"

// reviewJSONRE 抽取模型返回中的首个 JSON 对象。
var reviewJSONRE = regexp.MustCompile(`\{[\s\S]*\}`)

// reviewSystemPrompt 机审系统提示：只输出结构化 JSON，便于确定性解析。
const reviewSystemPrompt = `你是内容安全审核器。判断给定 AI 客服回复是否包含违规内容（涉政、色情、暴力、恐怖、赌博、歧视、虚假宣传、广告绝对化用语等）。
仅输出一个 JSON 对象（无额外文字）：{"violation": true或false, "reason": "不超过30字的简短理由"}`

// LLMReviewer 基于现有 AI 网关的内容安全机审（contentsafety.Reviewer 实现）。
// call 字段供测试注入；生产恒走 ai.Router 降级链（call 为 nil 时）。
type LLMReviewer struct {
	call func(prompt string) (string, error)
}

// Review 实现 contentsafety.Reviewer：调 LLM 判违规，fail-open。
func (r *LLMReviewer) Review(text string) (bool, string, error) {
	if strings.TrimSpace(text) == "" {
		return false, "", nil
	}
	call := r.call
	if call == nil {
		call = r.callViaRouter
	}
	out, err := call(reviewSystemPrompt + "\n\n待审回复：\n" + text)
	if err != nil {
		return false, "", err // 上层 CheckFull 对 err 做 fail-open 放行
	}
	return parseReview(out)
}

// callViaRouter 经 AI 网关降级链调用（tenantID=0 平台内部，不计租户配额）。
func (r *LLMReviewer) callViaRouter(prompt string) (string, error) {
	if ai.Router == nil {
		return "", fmt.Errorf("AI 网关未初始化")
	}
	msgs := []ai.ChatMessage{{Role: "user", Content: prompt}}
	out, _, _, _, err := ai.Router.GenerateTextForStage(context.Background(), llmReviewerStage, 0, msgs, 0)
	return out, err
}

// parseReview 解析模型返回为 (violation, reason)。解析失败返回 error（上层 fail-open）。
func parseReview(raw string) (bool, string, error) {
	m := reviewJSONRE.FindString(strings.TrimSpace(raw))
	if m == "" {
		return false, "", fmt.Errorf("机审返回无 JSON")
	}
	var v struct {
		Violation bool   `json:"violation"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(m), &v); err != nil {
		return false, "", err
	}
	return v.Violation, v.Reason, nil
}

// InitContentsafetyLLMReviewer 装配基于 AI 网关的内容安全机审（P2-5 生产装配点）。
// 默认关闭：env CONTENTSAFETY_LLM_REVIEWER != "true" 不装配（零成本/零行为变更）。
// 需在 ai.InitRouter() 之后调用（依赖 AI 网关降级链）。
func InitContentsafetyLLMReviewer() {
	if os.Getenv("CONTENTSAFETY_LLM_REVIEWER") != "true" {
		return
	}
	if ai.Router == nil || len(ai.Router.GetModels()) == 0 {
		log.Println("[内容安全] CONTENTSAFETY_LLM_REVIEWER=true 但无可用模型，跳过机审装配（fail-open 退化为纯词库）")
		return
	}
	contentsafety.SetReviewer(&LLMReviewer{})
	log.Println("[内容安全] 已装配 LLM 机审（fail-open；contentsafety_mode 仍由平台热开关控制，默认 shadow 只计数）")
}
