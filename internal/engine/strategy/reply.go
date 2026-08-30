// Package strategy：internal/engine/strategy 模块（自动补包注释）。
package strategy

import (
	"ai-scrm/internal/ai"
	"ai-scrm/internal/llm"
	"ai-scrm/internal/model"
	"ai-scrm/internal/service"
)

// ============================================================
// 策略引擎统一回复出口（P2-B 架构红线落地）
//
// 架构真理（SAAS_PLAN §18）：LLM 话术服务只被策略引擎调用。
// 业务层（api/chatflow）不得直连 internal/llm，一律经由本函数：
//   api → strategy.GenerateReply → llm.GenerateAIReply
// 引擎特性集（Features）在此注入，llm 包保持对引擎零依赖。
// ============================================================

// GenerateReply 基于策略输出生成自然语言回复
// M1 租户隔离修复（2026-08-25）：卖点注入按客户租户过滤后再交 LLM——
// DefaultEngine.Features() 为全量缓存，不过滤则租户A私有卖点会进入
// 租户B客户的 prompt（与 Infer 内 FillTemplate 过滤同规则）。
func GenerateReply(customer *model.Customer, conversationID uint, userInput string, out *StrategyOutput, deptIDs []uint) string {
	scope := service.ResolveRecallScope(customer.TenantID, deptIDs)
	features := featuresForTenant(DefaultEngine.Features(), customer.TenantID, scope)
	return llm.GenerateAIReply(customer, conversationID, userInput, out, features)
}

// GenerateEvals 知识库素材 AI 评分出口（架构红线：业务层经策略引擎桥接 llm，
// 不直接调用 internal/llm）。evals 阶段专属模型由 ai.Router 按 stage_models 解析。
func GenerateEvals(tenantID uint, messages []ai.ChatMessage, temperature float64) (string, error) {
	return llm.GenerateEvalsText(tenantID, messages, temperature)
}
