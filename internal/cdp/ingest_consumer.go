// Package cdp CDP 数据底座：OneID 身份归并、画像/标签/事件写收口与分群引擎（四维标签体系）。
package cdp

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"ai-scrm/internal/model"
	"ai-scrm/internal/mq"
	"ai-scrm/internal/service"
)

// ============================================================
// CDP 摄入消费者（SAAS_PLAN §16.8 写收口唯一入口）
//
// 数据流：业务层发布 user_event → 本消费者 →
//   Inbox 幂等抢占 → ID-Mapping 锚点归并 → 画像确保 → Raw事件落库
//   → 四维原子标签计算（首批：行为/身份维；态度维严禁行为硬推）
//
// 模式说明：MQ_TYPE=kafka 走真实消费者组；log 模式下 LogCenter 兼作
// 进程内总线（发布即本地分发），两种模式消费者代码完全一致
// ============================================================

// builtinTagDefs 首批原子标签定义（启动时 upsert 进 cdp_tag_definitions）
var builtinTagDefs = []model.CdpTagDefinition{
	{Code: "idm_guest", Name: "访客身份", Category: "identity"},
	{Code: "beh_lead_captured", Name: "已留资", Category: "behavior"},
	{Code: "beh_msg_active", Name: "会话活跃", Category: "behavior"},
	// M2 补齐（2026-08-25）：payment 子事件此前发布后零消费——CDP 记账收口
	{Code: "tran_order_paid", Name: "付费转化", Category: "behavior"},
	// P1-3（2026-08-29）四维补齐：环境维 + 态度维
	// 环境维：由事件携带的渠道/路由/发生时段时间派生（非行为硬推，属上下文信号）
	{Code: "env_route", Name: "交互路由", Category: "environment"},
	{Code: "env_time_slot", Name: "到访时段", Category: "environment"},
	// 态度维：严禁由行为推导——仅当事件属性显式携带 NLP/零方情绪（emotion）时才打，否则不打
	{Code: "att_emotion", Name: "情绪态度", Category: "attitude"},
}

// StartIngestConsumer 注册 user_event 订阅（main 启动时调用一次）
// 标签字典随启动 upsert，保证消费端可写标签
func StartIngestConsumer() {
	for i := range builtinTagDefs {
		def := &builtinTagDefs[i]
		var cnt int64
		gdb().Model(&model.CdpTagDefinition{}).Where("code = ?", def.Code).Count(&cnt)
		if cnt == 0 {
			gdb().Create(def)
		}
	}
	mq.Subscribe(mq.TopicUserEvent, func(ctx context.Context, env mq.Envelope) error {
		return mq.WithInbox(ctx, env, processEvent)
	})
	log.Println("[CDP] IngestConsumer 已订阅 user_event（写收口）")
}

// processEvent 单事件处理全流程
func processEvent(ctx context.Context, env mq.Envelope) error {
	// 信封解包：mq.Publish 包装为 {event_type, data:{UserEvent}}
	var payload struct {
		EventType string `json:"event_type"`
		EventName string `json:"event_name"`
		Data      struct {
			EventType  string         `json:"event_type"`
			EventName  string         `json:"event_name"`
			AnchorType string         `json:"anchor_type"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return err // 脏消息：标记 failed 由审计表回放排查
	}
	// 兼容两层事件名（顶层 eventType 与 UserEvent.event_name）
	eventName := payload.Data.EventName
	if eventName == "" {
		eventName = payload.EventType
	}
	attrCustomerID := toUintAny(payload.Data.Attributes["customer_id"])
	attrPhone, _ := payload.Data.Attributes["phone"].(string)
	attrEmail, _ := payload.Data.Attributes["email"].(string) // 防薅v2：注册邮箱作为身份锚之一

	oneID := env.Header.OneID
	tid := env.Header.TenantID

	// 1) 身份锚点归并（手机号锚存在时建立映射）
	if attrPhone != "" {
		UpsertAnchor(tid, "phone", attrPhone, oneID)
	}
	if attrEmail != "" {
		UpsertAnchor(tid, "email", attrEmail, oneID)
	}

	// 2) 确保画像主体存在
	profile := EnsureProfile(tid, oneID, attrCustomerID)
	if profile == nil {
		return nil
	}

	// 3) Raw Zone：不可变事件日志
	logEvent := model.EventLog{
		TenantID: tid, CustomerID: attrCustomerID,
		EventType: payload.EventType, EventKey: eventName,
		EventValue: string(env.Payload), Source: "scrm",
	}
	if err := gdb().Create(&logEvent).Error; err != nil {
		log.Printf("[CDP] 事件落库失败: %v", err)
	}

	// 4) 原子标签计算（首批规则表；扩展走 cdp_tag_definitions 配置）
	switch eventName {
	case "guest_created":
		ApplyTag(tid, profile.ID, "idm_guest", "1")
	case "lead_captured":
		ApplyTag(tid, profile.ID, "beh_lead_captured", "1")
	case "conversation_msg":
		ApplyTag(tid, profile.ID, "beh_msg_active", "1")
	case "payment":
		// M2（2026-08-25）：付费事实入 CDP——订单确认到账事件此前发布即沉底，
		// 编排层心跳消费者不认此事件名。CDP 定位"记录事实"，流程联动（欢迎流）留待专项。
		ApplyTag(tid, profile.ID, "tran_order_paid", "1")
		log.Printf("[CDP] payment 事件已记账 tenant=%d one=%s", tid, oneID)
	}

	// P1-3 环境维标签：渠道路由 + 到访时段（上下文信号，非行为推导）
	route, _ := payload.Data.Attributes["route"].(string)
	if route != "" {
		ApplyTag(tid, profile.ID, "env_route", route)
	}
	slot := "rest"
	if h := time.Now().Hour(); h >= 9 && h < 18 {
		slot = "work"
	}
	ApplyTag(tid, profile.ID, "env_time_slot", slot)

	// P1-3 态度维标签：仅当事件属性显式携带 NLP/零方情绪（emotion）时打——
	// 红线：严禁由行为硬推态度；chat.go 发布 conversation_msg 时携带 strategy 情绪即合规
	emotion, _ := payload.Data.Attributes["emotion"].(string)
	if emotion != "" {
		ApplyTag(tid, profile.ID, "att_emotion", emotion)
	}

	// P2 collector：CDP 事件进数据飞轮（脱敏在 Collect 内完成；URL 空则丢弃）
	service.Collect("cdp_event", tid, map[string]any{
		"one_id":    oneID,
		"route":     route,
		"time_slot": slot,
		"emotion":   emotion,
	})
	return nil
}

// toUintAny any→uint 宽松转换
func toUintAny(v any) uint {
	switch x := v.(type) {
	case float64:
		return uint(x)
	case uint:
		return x
	case int:
		return uint(x)
	case json.Number:
		n, _ := x.Int64()
		return uint(n)
	}
	return 0
}
