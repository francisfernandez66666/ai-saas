// Package llm LLM 调用唯一入口：构建 Prompt、多模型降级路由（智谱GLM→硅基流动）、计量与兜底
package llm

import (
	"ai-scrm/internal/ai"
	"ai-scrm/internal/service"
)

// GenerateEvalsText H5修复(2026-08-27)：知识库素材 AI 评分归位 llm 层唯一入口，
// 不再由 api 直连 ai.Router（违反 LLM 调用红线）；调用后落 usage_ledger 计量。
// evals 为后台评估任务，仅成本落账、不计次配额（不触发三桶 DeductTokensActual）。
func GenerateEvalsText(tenantID uint, messages []ai.ChatMessage, temperature float64) (string, error) {
	// 网关模式下计费权上收网关（网关侧做 fail-closed 计量），本地跳过自身落账避免重复
	gatewayMode := ai.DefaultGatewayClient != nil && tenantID != 0
	reply, provider, modelName, usage, err := ai.Router.GenerateTextForStage("evals", tenantID, messages, temperature)
	if err != nil {
		return "", err
	}
	// 落账：按素材所属租户计费（tenantID=0 平台素材在 RecordUsage 内被 0 守卫跳过，属预期）
	if !gatewayMode && (usage.PromptTokens > 0 || usage.CompletionTokens > 0) {
		service.RecordUsage(tenantID, 0, 0, "evals", provider, modelName, usage.PromptTokens, usage.CompletionTokens, 0)
	}
	return reply, nil
}
