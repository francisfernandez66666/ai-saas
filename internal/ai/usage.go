// Package ai LLM 调用统一入口：Prompt 构建、智谱/硅基流动双客户端、多模型降级路由与 Token 计量。
package ai

import (
	"ai-scrm/internal/service"
)

// ============================================================
// Token 用量采集（商业化第二批 M3，借鉴翻译助手四期 §4.3）
//
// 设计：GLM/硅基流动客户端解析 OpenAI 兼容响应中的 usage 字段，
// 调用链向上透传，由 llm 层落账到 usage_ledger。
// 阶段模型覆盖：stage_models 配置的优先级高于全局降级链——
// 便宜模型跑意图识别、强模型跑话术生成（成本优化抓手）
// ============================================================

// Usage 单次 LLM 调用的 token 用量
type Usage struct {
	PromptTokens     int // 输入 token 数
	CompletionTokens int // 输出 token 数
	TotalTokens      int // 总 token 数
}

// IsZero 是否无有效用量（mock/规则兜底路径）
func (u Usage) IsZero() bool { return u.TotalTokens <= 0 }

// stageModelOverride 阶段模型覆盖配置项
type stageModelOverride struct {
	Model    string `json:"model"`
	Provider string `json:"provider"` // 空=按模型名推断：优先 siliconflow（主力通道），含 glm 仅当显式指定 zhipu 才走智谱
}

// ResolveStageModel 解析阶段模型覆盖：stage_models JSON 配置了该阶段且非空则生效
// 返回 (provider, model, 是否覆盖)。配置缺失/脏数据一律回退全局降级链
func ResolveStageModel(stage string) (string, string, bool) {
	if service.DefaultSystemConfigService == nil || stage == "" {
		return "", "", false
	}
	var cfg map[string]stageModelOverride
	if !service.DefaultSystemConfigService.GetJSON("stage_models", &cfg) {
		return "", "", false
	}
	o, ok := cfg[stage]
	if !ok || o.Model == "" {
		return "", "", false
	}
	provider := o.Provider
	if provider == "" {
		// P1-28 修复(2026-09-09)：主力模型恰是硅基流动的 GLM-4-9B，原"含 glm 推断 zhipu"
		// 会把运营配的 {"reply":{"model":"GLM-4-9B"}} 路由到智谱客户端拿硅基模型名调用 → 401
		// 静默回退，阶段覆盖对主力模型永不生效。默认一律 siliconflow；仅显式指定 provider=zhipu 走智谱。
		provider = string(ProviderSiliconFlow)
	}
	return provider, o.Model, true
}

// containsFold 大小写不敏感子串匹配（模型名含glm推断provider用）
func containsFold(s, sub string) bool {
	n := len(sub)
	if n == 0 || len(s) < n {
		return false
	}
	for i := 0; i+n <= len(s); i++ {
		match := true
		for j := 0; j < n; j++ {
			a, b := s[i+j], sub[j]
			if 'A' <= a && a <= 'Z' {
				a += 32
			}
			if 'A' <= b && b <= 'Z' {
				b += 32
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
