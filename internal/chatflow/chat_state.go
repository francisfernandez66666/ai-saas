// Package chatflow 聊天流模块：延迟取消/留资检测与 OneID 合并/会话状态维护/业务驱动消费
package chatflow

import (
	"ai-scrm/config"
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/strategytypes"
	"log"
	"time"
)

// ============================================================
// 会话状态维护（Phase C 自 chat.go 下沉）
// 人工超时接管判定 / 待交接话术 / 会话状态S更新
// ============================================================

// CheckHumanTimeout 检查人工是否超时
// 销售3分钟未回复，AI自动接管
// 同时检查软接管超时：pending_handoff状态下销售3分钟未接管，自动回退到纯AI模式
// D4 修复(2026-09-16B，AUDIT_UAT_VERIFY_2026-09-16B)：两个超时分支旧行为只改内存对象——
// 调用方（chat_main 仅硬超时分支回写会话）挂掉/异常时接管状态不落库，下轮请求从 DB 重载
// 又回到"待接管/人工"态，超时判定形同虚设。现在判定即持久化（定向 Updates 只写接管相关列，
// 不破 P1-9"末尾整行 Save 覆写接管态"防回潮口径；where 带 tenant_id 双保险）。
func CheckHumanTimeout(conversation *model.Conversation) bool {
	// 软接管超时检查
	if conversation.PendingHandoff && conversation.HandoffNotifiedAt != nil {
		since := time.Since(*conversation.HandoffNotifiedAt)
		// 修复：从SystemConfigService读取超时时间，后台调参即时生效
		timeout := time.Duration(runtimecfg.DefaultSystemConfigService.GetInt("human_timeout_seconds", config.GlobalConfig.Strategy.HumanTimeoutSeconds)) * time.Second
		if since > timeout {
			log.Printf("[对话] 软接管超时，AI完全接管会话: %d", conversation.ID)
			conversation.PendingHandoff = false
			conversation.HandoffNotifiedAt = nil
			// D4：软超时同样落库——旧实现只改内存，请求结束即丢，销售池永远挂着死线索
			if err := db.DB.Model(&model.Conversation{}).
				Where("id = ? AND tenant_id = ?", conversation.ID, conversation.TenantID).
				Updates(map[string]interface{}{
					"pending_handoff":     false,
					"handoff_notified_at": nil,
				}).Error; err != nil {
				log.Printf("[对话-告警] 会话%d 软接管超时落库失败(下轮将重复判定): %v", conversation.ID, err)
			}
			return false // 继续AI模式，不需要切人工
		}
	}

	if conversation.Mode != "human" {
		return false
	}
	if conversation.IsHumanLocked {
		return false
	}
	timedOut := false
	if conversation.LastHumanReplyAt == nil {
		// 还没有人工回复过，检查最后消息时间
		if conversation.LastMessageAt != nil {
			since := time.Since(*conversation.LastMessageAt)
			timeout := time.Duration(runtimecfg.DefaultSystemConfigService.GetInt("human_timeout_seconds", config.GlobalConfig.Strategy.HumanTimeoutSeconds)) * time.Second
			timedOut = since > timeout
		}
	} else {
		since := time.Since(*conversation.LastHumanReplyAt)
		timeout := time.Duration(runtimecfg.DefaultSystemConfigService.GetInt("human_timeout_seconds", config.GlobalConfig.Strategy.HumanTimeoutSeconds)) * time.Second
		timedOut = since > timeout
	}
	if timedOut {
		// D4：硬超时（AI 接管）落库，防调用方中途异常导致"接管"只在内存生效一次
		conversation.Mode = "ai"
		conversation.IsHumanLocked = false
		if err := db.DB.Model(&model.Conversation{}).
			Where("id = ? AND tenant_id = ?", conversation.ID, conversation.TenantID).
			Updates(map[string]interface{}{
				"mode":            "ai",
				"is_human_locked": false,
				"pending_handoff": false,
			}).Error; err != nil {
			log.Printf("[对话-告警] 会话%d 人工超时AI接管落库失败(下轮将重复判定): %v", conversation.ID, err)
		}
	}
	return timedOut
}

// UpdateConversationState 更新会话状态
// 根据本轮对话结果，更新会话状态S
func UpdateConversationState(
	conv *model.Conversation,
	strategyOutput *strategytypes.StrategyOutput,
	customer *model.Customer,
	customerInput string,
) model.SessionState {
	state := conv.GetState()

	// 已抛锚次数（AI回复算一次抛锚）
	state.Attempts++

	// 接钩判断
	if strategytypes.CheckHooked(customerInput) && state.Attempts > 1 {
		state.HookCount++
	}

	// 重新计算接钩率
	if state.Attempts > 0 {
		state.HookRate = float64(state.HookCount) / float64(state.Attempts)
	}

	// 上一次锚类型
	state.LastAnchorType = strategyOutput.FinalAnchor

	// 情绪更新
	state.Emotion = strategytypes.DetectEmotion(customerInput)

	// 高意向持续轮数
	intentScore := customer.IntentScore + strategyOutput.IntentDelta
	// 修复：从SystemConfigService读取L3阈值，后台调参即时生效
	thetaL3Intent := runtimecfg.DefaultSystemConfigService.GetFloat("theta_l3_intent", config.GlobalConfig.Strategy.ThetaL3Intent)
	if intentScore >= thetaL3Intent {
		state.HighIntentRounds++
	} else {
		state.HighIntentRounds = 0
	}

	// 心智阶段推进——严格逐级递进，禁止跳跃
	// 修复：从SystemConfigService读取心智阶段参数，后台调参即时生效
	stageStepEnabled := runtimecfg.DefaultSystemConfigService.GetBool("stage_step_enabled", true)
	stageMaxIncrement := runtimecfg.DefaultSystemConfigService.GetInt("stage_max_increment", 1)
	hookRateStage1Threshold := runtimecfg.DefaultSystemConfigService.GetFloat("hook_rate_stage1_threshold", 0.3)
	forceStage0Attempts := runtimecfg.DefaultSystemConfigService.GetInt("force_stage0_attempts", 1)
	// 修复"平A开大"根因之二：旧代码用IntentScore直接算阶段，一轮对话后
	// intentScore从0.4涨到0.5+，直接从stage=0跳到stage=2甚至3，
	// 导致阶段锁的天花板很高（ceiling=3或4），对比锚完全合法。
	//
	// 新规则（3条硬约束）：
	// 1. 每轮最多只推进1个阶段（0→1→2→3→4→5，不跳跃）
	// 2. 首次对话（Attempts≤1）强制stage=0（新客户就是认知阶段，不管IntentScore多高）
	// 3. Stage≥2需要接钩确认（客户没接钩=策略没生效，不应该推进到更高阶段）
	//
	// 这样客户说"你好"，stage最多=1（兴趣阶段），ceiling=2，
	// 对比锚(aggressiveness=3)必然被阶段锁降级到同类锚(aggressiveness=1)
	oldStage := state.CurrentStage

	// 约束1：首次对话强制认知阶段
	if state.Attempts <= forceStage0Attempts {
		state.CurrentStage = 0 // 认知阶段
	} else {
		// 根据IntentScore计算"目标阶段"（只作参考，不直接赋值）
		targetStage := 0
		if intentScore > 0.8 {
			targetStage = 4 // 决策阶段
		} else if intentScore > 0.6 {
			targetStage = 3 // 促转阶段
		} else if intentScore > 0.4 {
			targetStage = 2 // 考虑阶段
		} else if intentScore > 0.2 {
			targetStage = 1 // 兴趣阶段
		}

		// 约束2：每轮最多推进N个阶段（不跳跃，N=stage_max_increment）
		if stageStepEnabled && targetStage > oldStage+stageMaxIncrement {
			targetStage = oldStage + stageMaxIncrement
		}

		// 约束3：Stage≥2需要接钩确认
		// 接钩率≥hookRateStage1Threshold才允许推进到Stage2及以上
		if targetStage >= 2 && state.HookRate < hookRateStage1Threshold {
			targetStage = 1
			log.Printf("[状态更新] 接钩率=%.2f不够(阈值%.2f)，客户阶段卡在兴趣阶段(Stage1)",
				state.HookRate, hookRateStage1Threshold)
		}

		// 降级保护：如果意图分下降了，阶段也应该回退（客户流失信号）
		if intentScore < customer.IntentScore && targetStage < oldStage {
			// 意向分下降+目标阶段低于当前，允许回退1级
			state.CurrentStage = oldStage - 1
		} else {
			// 正常推进或保持
			state.CurrentStage = targetStage
		}
	}

	log.Printf("[状态更新] 心智阶段: old=%d → new=%d, 意向分=%.3f, 接钩率=%.2f, 抛锚次数=%d",
		oldStage, state.CurrentStage, intentScore, state.HookRate, state.Attempts)

	// 沉默时长重置（有新消息了）
	state.SilentDuration = 0

	return state
}
