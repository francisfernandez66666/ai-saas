// Package ai LLM 调用统一入口：Prompt 构建、智谱/硅基流动双客户端、多模型降级路由与 Token 计量。
package ai

import (
	"ai-scrm/config"
	"fmt"
	"log"
	"sync"
	"time"
)

// ============================================================
// AI模型路由层 - 多模型降级调度
//
// 降级链路（v6：干掉智谱，硅基流动做主力）：
//   1. AI 网关（云端枢纽转发，本地不持有厂商Key时优先）
//   2. 硅基流动 DeepSeek V4 Flash（主力，限流宽松、响应快）
//   3. 硅基流动 GLM-4-9B（同平台免费兜底，9B以下永久免费）
//   4. 模板兜底（最后防线）
//
// 设计原则：
//   - 每次对话都从主模型开始尝试（不永久切走）
//   - 单条消息内失败自动降级到下一个模型
//   - 降级有冷却时间，避免每个请求都全链路试一遍浪费资源
// ============================================================

// ModelProvider 模型提供商标识
type ModelProvider string

// 常量/变量定义块（自动补注释）。
const (
	ProviderZhipu       ModelProvider = "zhipu"       // 智谱GLM
	ProviderSiliconFlow ModelProvider = "siliconflow" // 硅基流动
	ProviderGateway     ModelProvider = "gateway"     // AI 网关（云端枢纽转发）
	ProviderFallback    ModelProvider = "fallback"    // 模板兜底
)

// ModelState 单个模型的状态
type ModelState struct {
	Provider         ModelProvider // 提供方
	ModelName        string        // 模型名
	Available        bool          // 当前是否可用
	LastFailTime     time.Time     // 最后一次失败时间
	ConsecutiveFails int           // 连续失败次数
}

// AIRouter AI路由器
type AIRouter struct {
	mu          sync.RWMutex  // 并发保护
	models      []*ModelState // 模型列表（按优先级排序）
	coolDownSec int           // 失败后冷却时间（秒）
}

// Router 默认路由器实例
var Router *AIRouter

// InitRouter 初始化AI路由器
// 模型优先级：硅基流动GLM-4-9B(免费免费) → 硅基流动DeepSeek → 模板兜底
// 智谱已移除（频繁触发429限流，无法正常使用）
func InitRouter() {
	models := make([]*ModelState, 0)

	cfg := config.GlobalConfig.AI

	// 0. AI 网关（云端枢纽）优先：本地部署/SaaS实例无厂商Key时统一转发
	//    网关持有平台厂商Key出网，本地仅做租户三桶计费；网关失败自动降级到直连链
	InitGatewayClient()
	if DefaultGatewayClient != nil {
		models = append(models, &ModelState{
			Provider:         ProviderGateway,
			ModelName:        DefaultGatewayClient.Model,
			Available:        true,
			ConsecutiveFails: 0,
		})
		log.Println("[AI路由] 已注入网关模型，reply 阶段优先经网关转发")
	}

	// 1. 硅基流动 GLM-4-9B（默认模型）
	// 9B以下模型在硅基流动平台永久免费，几乎不会挂
	if cfg.SiliconFlow.APIKey != "" {
		models = append(models, &ModelState{
			Provider:         ProviderSiliconFlow,
			ModelName:        cfg.SiliconFlow.Model,
			Available:        true,
			ConsecutiveFails: 0,
		})

		// 2. 硅基流动 DeepSeek V4 Flash（备用）
		if cfg.SiliconFlow.ModelBackup != "" && cfg.SiliconFlow.ModelBackup != cfg.SiliconFlow.Model {
			models = append(models, &ModelState{
				Provider:         ProviderSiliconFlow,
				ModelName:        cfg.SiliconFlow.ModelBackup,
				Available:        true,
				ConsecutiveFails: 0,
			})
		}
	}

	// 智谱已移除（频繁429限流，重试8-16秒也大概率继续429）
	// 如需恢复，在 stage_models 配置中指定 provider=zhipu 即可（callProvider 仍保留兼容）

	Router = &AIRouter{
		models:      models,
		coolDownSec: 300, // 5分钟冷却，避免每条消息都全链路重试
	}

	log.Println("========================================")
	log.Println("[AI路由] 多模型降级链路初始化完成")
	log.Println("========================================")
	for i, m := range models {
		status := "✓ 可用"
		if !m.Available {
			status = "✗ 未启用"
		}
		log.Printf("[AI路由]   %d. [%-12s] %-40s %s", i+1, m.Provider, m.ModelName, status)
	}
	log.Println("========================================")
}

// GenerateText 统一生成文本入口
// 自动按优先级尝试各模型，失败则降级
// 返回：回复内容, 使用的模型名, 错误
func (r *AIRouter) GenerateText(messages []ChatMessage, temperature float64) (string, string, error) {
	reply, _, model, _, err := r.GenerateTextForStage("", 0, messages, temperature)
	return reply, model, err
}

// GenerateTextForStage 带阶段语义的生成入口（M3）
// stage_models 配置了该阶段专属模型时优先使用（失败自动回退全局降级链），
// 并透传 token 用量供 usage_ledger 落账。
// tenantID 用于网关转发时还原租户做 fail-closed 计量（0=平台内部调用）。
// 返回：回复内容, provider, 模型名, 用量, 错误
func (r *AIRouter) GenerateTextForStage(stage string, tenantID uint, messages []ChatMessage, temperature float64) (string, string, string, Usage, error) {
	// 阶段覆盖优先：配置的专属模型先行尝试
	if provStr, model, ok := ResolveStageModel(stage); ok {
		reply, usage, err := r.callProvider(ModelProvider(provStr), model, messages, temperature, tenantID, stage)
		if err == nil && reply != "" {
			log.Printf("[AI路由] 阶段[%s]使用stage_models覆盖模型: [%s] %s", stage, provStr, model)
			return reply, provStr, model, usage, nil
		}
		log.Printf("[AI路由] 阶段[%s]覆盖模型调用失败(%v)，回退全局降级链", stage, err)
	}
	reply, provider, model, usage, err := r.GenerateTextWithUsage(messages, temperature, tenantID, stage)
	return reply, provider, model, usage, err
}

// callProvider 定向调用指定 provider+model（阶段覆盖专用）
func (r *AIRouter) callProvider(provider ModelProvider, modelName string, messages []ChatMessage, temperature float64, tenantID uint, stage string) (string, Usage, error) {
	switch provider {
	case ProviderZhipu:
		DefaultClient.SetModel(modelName)
		return DefaultClient.GenerateTextWithUsage(messages, temperature)
	case ProviderSiliconFlow:
		SiliconFlowDefaultClient.SetModel(modelName)
		return SiliconFlowDefaultClient.GenerateTextWithUsage(messages, temperature)
	case ProviderGateway:
		if DefaultGatewayClient == nil {
			return "", Usage{}, fmt.Errorf("网关未初始化")
		}
		return DefaultGatewayClient.GenerateTextWithUsage(messages, temperature, tenantID, stage)
	}
	return "", Usage{}, fmt.Errorf("未知provider: %s", string(provider))
}

// GenerateTextWithUsage 生成并透传 token 用量（M3 计量底座）
// tenantID/stage 透传网关（本地直连时忽略）。
// 返回：回复内容, provider, 使用的模型名, 用量, 错误
func (r *AIRouter) GenerateTextWithUsage(messages []ChatMessage, temperature float64, tenantID uint, stage string) (string, string, string, Usage, error) {
	r.mu.RLock()
	models := make([]*ModelState, len(r.models))
	copy(models, r.models)
	r.mu.RUnlock()

	var lastErr error
	now := time.Now()
	triedCount := 0

	log.Printf("[AI路由] ===== 开始尝试模型，共%d个候选模型", len(models))

	// 按优先级依次尝试每个模型
	for idx, model := range models {
		// 如果模型不可用（冷却中或从未启用），跳过
		r.mu.RLock()
		available := model.Available
		inCoolDown := !model.LastFailTime.IsZero() &&
			now.Sub(model.LastFailTime) < time.Duration(r.coolDownSec)*time.Second
		r.mu.RUnlock()

		if !available {
			log.Printf("[AI路由]   跳过[%d] [%s] %s（不可用）", idx+1, model.Provider, model.ModelName)
			continue
		}

		// 冷却中也尝试一次（万一恢复了呢），但如果刚失败（1分钟内）就跳过
		if inCoolDown && model.ConsecutiveFails >= 3 {
			log.Printf("[AI路由]   跳过[%d] [%s] %s（冷却中，连续失败%d次）",
				idx+1, model.Provider, model.ModelName, model.ConsecutiveFails)
			continue
		}

		triedCount++
		log.Printf("[AI路由] → 尝试第%d个模型: [%s] %s", idx+1, model.Provider, model.ModelName)

		reply, usage, err := r.callProvider(model.Provider, model.ModelName, messages, temperature, tenantID, stage)

		if err == nil && reply != "" {
			// 成功，重置失败计数
			r.markSuccess(model)
			log.Printf("[AI路由] ✓ 第%d个模型调用成功: [%s] %s", idx+1, model.Provider, model.ModelName)
			return reply, string(model.Provider), model.ModelName, usage, nil
		}

		lastErr = err
		log.Printf("[AI路由] ✗ 第%d个模型失败: [%s] %s, 错误: %v", idx+1, model.Provider, model.ModelName, err)
		r.markFailure(model)

		// 还有下一个模型，打印降级提示
		if idx < len(models)-1 {
			log.Printf("[AI路由] ⬇  自动降级到下一个模型...")
		}
	}

	// 所有模型都失败了，返回最后一个错误
	log.Printf("[AI路由] ===== 全部%d个模型均调用失败，走模板兜底，最后错误: %v", triedCount, lastErr)
	return "", "", "", Usage{}, lastErr
}

// markSuccess 标记模型调用成功
func (r *AIRouter) markSuccess(model *ModelState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	model.ConsecutiveFails = 0
	model.Available = true
	model.LastFailTime = time.Time{} // 清零
}

// markFailure 标记模型调用失败
func (r *AIRouter) markFailure(model *ModelState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	model.ConsecutiveFails++
	model.LastFailTime = time.Now()
	// 连续失败5次以上标记为不可用，等冷却后再试
	if model.ConsecutiveFails >= 5 {
		model.Available = false
		log.Printf("[AI路由] 模型 [%s] %s 连续失败%d次，暂时标记不可用，%d秒后自动恢复",
			model.Provider, model.ModelName, model.ConsecutiveFails, r.coolDownSec)
	}
}

// GetModels 获取所有模型列表（用于日志排查）
func (r *AIRouter) GetModels() []*ModelState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.models
}

// RecoverCoolingModels 定时恢复冷却中的模型
// 每5分钟检查一次，把冷却到期的模型恢复为可用
func (r *AIRouter) RecoverCoolingModels() {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	for _, model := range r.models {
		if !model.Available && !model.LastFailTime.IsZero() {
			if now.Sub(model.LastFailTime) >= time.Duration(r.coolDownSec)*time.Second {
				model.Available = true
				log.Printf("[AI路由] 模型 [%s] %s 冷却期结束，恢复可用", model.Provider, model.ModelName)
			}
		}
	}
}
