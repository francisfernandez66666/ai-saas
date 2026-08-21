package strategy

import (
	"ai-scrm/internal/llm"
	"ai-scrm/internal/model"
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
func GenerateReply(customer *model.Customer, conversationID uint, userInput string, out *StrategyOutput) string {
	return llm.GenerateAIReply(customer, conversationID, userInput, out, DefaultEngine.Features())
}
