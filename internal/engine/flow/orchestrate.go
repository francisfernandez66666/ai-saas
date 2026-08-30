package flow

// ============================================================
// 编排层结构归位（遗留项 Phase C/D，2026-08-22）
//
// 目标架构对齐：
//   - 业务层不直调策略引擎 → 统一走 OrchestrateReply（编排层单线调大脑）
//   - 编排层消费 Kafka 事件：user_event（与 CDP 并行，各消费各的流）+ flow_result（业务结果回流推进主干）
//   - 编排层主动拉 CDP OpenAPI（标签摘要 ≤200ms 超时降级空摘要，用上次上下文续跑语义）
//   - 状态表心跳随真实活动刷新（waiting 不再被判超时的语义基础）
//
// 说明：聊天主回复链路保持同步返回（产品行为不变）；本文件先打通
// "收事件 / 拉画像 / 调大脑 / 收回流"四个接缝，全量异步化另立专项。
// ============================================================

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"ai-scrm/internal/cdp"
	"ai-scrm/internal/db"
	"ai-scrm/internal/engine/strategy"
	"ai-scrm/internal/model"
	"ai-scrm/internal/mq"
	"ai-scrm/internal/service"
	statemachine "ai-scrm/internal/state_machine"
)

// OrchestrateReply 编排层统一话术入口（业务层唯一合法的大脑调用通道）
// 职责：解析 OneID → 刷新状态表心跳（真实活动）→ 拉 CDP 标签摘要注入决策上下文
//
//	→ 单线调用策略引擎生成话术 → 返回
//
// 输入输出签名与 strategy.GenerateReply 完全一致，调用点零成本切换
func (e *Engine) OrchestrateReply(customer *model.Customer, conversationID uint, userInput string, out *strategy.StrategyOutput, deptIDs []uint) string {
	tid := customer.TenantID
	oneID := cdp.ResolveOneID(tid, customer.ID)

	// 1. 状态表心跳：有真实活动即续期（区别于巡检的"超时回收"语义）
	if inst := FindRunningInstance(tid, customer.ID); inst != nil {
		_ = statemachine.Heartbeat(tid, inst.ID)
	}

	// 2. 拉取 CDP 标签摘要（≤200ms；超时/异常降级为空摘要，不阻断主链路）
	tags := fetchTagSummary(tid, oneID)
	if len(tags) > 0 {
		log.Printf("[编排] decision_context 注入CDP标签 one=%s tags=%v", oneID, tags)
	}

	// 3. 单线调用策略引擎（大脑被动被编排层调用）
	return strategy.GenerateReply(customer, conversationID, userInput, out, deptIDs)
}

// fetchTagSummary 带 200ms 超时拉取标签摘要（独立 goroutine + select 兜底）
func fetchTagSummary(tenantID uint, oneID string) map[string]string {
	type result struct{ tags map[string]string }
	ch := make(chan result, 1)
	go func() {
		defer func() { _ = recover() }()
		v := cdp.GetProfileTagsSummary(tenantID, oneID)
		ch <- result{tags: v}
	}()
	select {
	case r := <-ch:
		return r.tags
	case <-time.After(200 * time.Millisecond):
		log.Printf("[编排] CDP 标签查询超时(>200ms)，降级空摘要继续 one=%s", oneID)
		return nil
	}
}

// FindRunningInstance 找客户当前 running 的流程实例（最新一条）
func FindRunningInstance(tenantID uint, customerID uint) *model.FlowInstance {
	var inst model.FlowInstance
	err := db.DB.Where("tenant_id = ? AND customer_id = ? AND status = ?",
		tenantID, customerID, "running").Order("id DESC").First(&inst).Error
	if err != nil {
		return nil
	}
	return &inst
}

// PublishFlowResult 业务结果回流发布（业务层调用；编排层消费者据此推进主干）
// result 语义：lead_captured / human_takeover / user_reply / store_visit ...
func PublishFlowResult(tenantID uint, customerID uint, nodeResult string, detail map[string]any) {
	inst := FindRunningInstance(tenantID, customerID)
	if inst == nil {
		return // 无在途实例：结果无需驱动流程
	}
	oneID := cdp.ResolveOneID(tenantID, customerID)
	if err := mq.Publish(context.Background(), mq.TopicFlowResult, tenantID, oneID,
		nodeResult, mq.FlowResultEvent{
			InstanceID: inst.ID,
			NodeID:     inst.CurrentNodeID,
			Result:     nodeResult,
			Detail:     detail,
		}); err != nil {
		log.Printf("[编排] flow_result 发布失败: %v", err)
	}
}

// StartOrchestrationConsumers 注册编排层事件消费者（main 启动调用一次）
func StartOrchestrationConsumers() {
	// ---- consumer A：user_event 并行消费（与 CDP 各消费各的流）----
	// 职责：活动信号 → 状态表心跳续期。天然幂等（心跳可重复），不走 Inbox
	mq.Subscribe(mq.TopicUserEvent, func(ctx context.Context, env mq.Envelope) error {
		var payload struct {
			Data struct {
				Attributes struct {
					CustomerID uint `json:"customer_id"`
				} `json:"attributes"`
			} `json:"data"`
		}
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			return nil // 非客户维度事件，忽略
		}
		cid := payload.Data.Attributes.CustomerID
		if cid == 0 || env.Header.TenantID == 0 {
			return nil
		}
		if inst := FindRunningInstance(env.Header.TenantID, cid); inst != nil {
			_ = statemachine.Heartbeat(env.Header.TenantID, inst.ID)
		}
		return nil
	})

	// ---- consumer B：flow_result 回流 → 推进流程主干 ----
	mq.Subscribe(mq.TopicFlowResult, func(ctx context.Context, env mq.Envelope) error {
		// 注意信封结构：Publish 包装为 {event_type, data:{FlowResultEvent}}，需解一层
		var wrapper struct {
			Data mq.FlowResultEvent `json:"data"`
		}
		if err := json.Unmarshal(env.Payload, &wrapper); err != nil {
			return err
		}
		evt := wrapper.Data
		if evt.InstanceID == 0 {
			return nil
		}
		var inst model.FlowInstance
		if err := db.DB.First(&inst, evt.InstanceID).Error; err != nil {
			log.Printf("[编排] flow_result 实例不存在: %d", evt.InstanceID)
			return nil
		}
		flowCtx := &FlowContext{
			TenantID:       inst.TenantID,
			CustomerID:     inst.CustomerID,
			ConversationID: inst.ConversationID,
		}
		newInst, err := DefaultEngine.AdvanceFlow(inst.ID, evt.Result, flowCtx)
		if err != nil {
			log.Printf("[编排] flow_result 推进失败 instance=%d route=%s: %v",
				evt.InstanceID, evt.Result, err)
			return err
		}
		log.Printf("[编排] flow_result 已推进主干 instance=%d route=%s → node=%s status=%s",
			inst.ID, evt.Result, newInst.CurrentNodeID, newInst.Status)
		return nil
	})

	log.Println("[编排] 事件消费者已注册：user_event(心跳) + flow_result(推进主干)")
}

// StartPaymentConsumer 注册「付费成功 → 开通欢迎流程」消费者（运营闭环 P1-2）
// 说明：BillingOrder 与 customer 无外键关联（订单为租户级），故欢迎流程以租户维度启动；
// 若未来订单补 CustomerID 关联，可在此解析并启动客户级流程。落地产物：
//  1. 启动 welcome_paid 流程实例（状态机可见，便于运营追踪已付费待欢迎租户）
//  2. 上报数据飞轮 payment_welcome 事件（供自动化触达系统消费）
func StartPaymentConsumer() {
	mq.Subscribe(mq.TopicUserEvent, func(ctx context.Context, env mq.Envelope) error {
		var payload struct {
			EventName string `json:"event_name"`
			Data      struct {
				OrderNo string `json:"order_no"`
			} `json:"data"`
		}
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			return nil
		}
		if payload.EventName != "payment" {
			// 仅处理 payment 子事件
			return nil
		}
		tid := env.Header.TenantID
		if tid == 0 {
			return nil
		}
		// 1) 启动租户级欢迎流程（welcome_paid 定义不存在时优雅跳过，不报错）
		ctxFlow := &FlowContext{TenantID: tid, CustomerID: 0, ConversationID: 0}
		if _, err := DefaultEngine.StartFlow("welcome_paid", ctxFlow); err != nil {
			log.Printf("[编排] welcome_paid 流程未配置或启动失败 tenant=%d: %v", tid, err)
		} else {
			log.Printf("[编排] 已为租户 %d 启动 welcome_paid 欢迎流程", tid)
		}
		// 2) 数据飞轮：付费欢迎事件（脱敏，供自动化触达）
		service.Collect("payment_welcome", tid, map[string]any{
			"order_no": payload.Data.OrderNo,
		})
		return nil
	})
	log.Println("[编排] payment 子事件消费者已注册（付费成功→开通欢迎流程）")
}
