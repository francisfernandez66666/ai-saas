// 入站处理桥（W3-5 共用，2026-09-12）
// 渠道回调解密后的 InboundMessage → 归一化为站内一轮对话：
//
//	建/复用客户(OneID 桥)→复用/新建会话→落客户消息→（非人工锁定则）走 strategy 路由+AI 生成
//	→内容安全闸门→落 AI 消息→投递出站队列。
//
// 红线不破：生成经 flow→strategy→llm；本包不直连 llm。人工接管态 AI 不出声（转人工无感知）。
package channel

import "ai-scrm/internal/metrics"

import (
	"ai-scrm/internal/runtimecfg"
	"context"
	"log"
	"time"

	"ai-scrm/internal/attribution"
	"ai-scrm/internal/contentsafety"
	"ai-scrm/internal/db"
	"ai-scrm/internal/engine/flow"
	"ai-scrm/internal/engine/strategy"
	"ai-scrm/internal/model"
	"ai-scrm/internal/service"
)

// ProcessInbound 处理一条已解密的入站消息（适配器解析后调用）。
// ch：来源通道；in：归一化入站。返回错误仅用于回调侧决定是否重试，业务落库失败已尽量降级。
func ProcessInbound(ch *model.Channel, in *InboundMessage) error {
	// 系统事件（change_contact/add_external_contact 等）→ CDP 摄入，不进对话
	if in.IsEvent {
		log.Printf("[通道] channel=%d 事件 %s external=%s（走 CDP，不回复）", ch.ID, in.EventKey, in.ExternalID)
		return nil
	}
	if in.Content == "" {
		return nil // 空文本/暂不支持类型，忽略
	}
	in.ChannelID = ch.ID
	in.TenantID = ch.TenantID

	customerID, err := ResolveOrCreateCustomer(ch.TenantID, ch.ID, in.ExternalID, in.StaffID, "")
	if err != nil {
		return err
	}
	conv, err := ensureConversation(ch.TenantID, customerID, in.ExternalID, ch.Type)
	if err != nil {
		return err
	}

	// 1. 落客户入站消息
	inMsg := model.Message{
		TenantID: ch.TenantID, ConversationID: conv.ID, CustomerID: customerID,
		SenderType: "customer", Content: in.Content, MessageType: "text",
		RouteResult: "channel_inbound",
	}
	db.DB.Create(&inMsg)
	// D9：渠道入站同样回填上一轮 AI 接钩归因。
	_ = attribution.MarkHookedBeforeMessage(ch.TenantID, conv.ID, inMsg.ID)

	db.DB.Model(&model.Conversation{}).Where("id = ?", conv.ID).Update("last_message_at", time.Now())

	// 2. 人工接管态 / AI 关闭：不出声，等顾问手动回复（顾问回复经出站桥回渠道）
	if conv.IsHumanLocked || !conv.IsAiReplyEnabled {
		log.Printf("[通道] 会话%d 人工锁定/AI关闭，AI 不回复", conv.ID)
		return nil
	}

	// 3. 策略推理
	var cust model.Customer
	if err := db.DB.First(&cust, customerID).Error; err != nil {
		return err
	}
	si := strategy.StrategyInput{
		TVector:        cust.BuildBaseTVector(),
		State:          conv.GetState(),
		CustomerInput:  in.Content,
		CustomerID:     cust.ID,
		ConversationID: conv.ID,
		CanPromote:     cust.CanPromote(),
		JourneyStage:   cust.JourneyStage,
		TenantID:       cust.TenantID,
		DeptIDs:        service.DeptChainForUser(conv.AssignedUserID),
	}
	out := strategy.DefaultEngine.Infer(si)

	// 4. 路由：仅 AI 路径生成回复；转人工/接不住→关 AI 等顾问（无感知），不自动发
	if out.RouteResult != strategy.RouteAI {
		log.Printf("[通道] 会话%d 路由=%s，非AI直出", conv.ID, out.RouteResult)
		return nil
	}

	// 5. 生成 AI 回复（红线：flow→strategy→llm）
	reply := flow.DefaultEngine.OrchestrateReply(&cust, conv.ID, in.Content, &out, si.DeptIDs)
	if reply == "" {
		return nil
	}

	// 6. 内容安全闸门（C1 同款规则，本地实现避免 channel→api 环）
	reply = gateReply(reply, ch.TenantID, conv.ID)

	// 7. 落 AI 消息 + 投递出站
	aiMsg := model.Message{
		TenantID: ch.TenantID, ConversationID: conv.ID, CustomerID: customerID,
		SenderType: "ai", Content: reply, MessageType: "text",
		AnchorType: out.FinalAnchor, TemplateID: out.TemplateID, RouteResult: "channel_ai",
	}
	db.DB.Create(&aiMsg)
	// D9：渠道 AI 回复落包/模板归因。
	channelIntentAfter := si.TVector[0] + out.IntentDelta
	if channelIntentAfter < 0 {
		channelIntentAfter = 0
	}
	if channelIntentAfter > 1 {
		channelIntentAfter = 1
	}
	_ = attribution.RecordReply(attribution.RecordReplyInput{
		TenantID:       ch.TenantID,
		MessageID:      aiMsg.ID,
		ConversationID: conv.ID,
		CustomerID:     customerID,
		TemplateID:     out.TemplateID,
		AnchorType:     out.FinalAnchor,
		RouteResult:    "channel_ai",
		IntentBefore:   si.TVector[0],
		IntentAfter:    channelIntentAfter,
	})

	return Enqueue(ch.TenantID, ch.ID, customerID, conv.ID, reply, "text")
}

// ensureConversation 取该客户在渠道下的活跃会话，无则新建（Channel 标渠道类型，SessionID=externalID）。
func ensureConversation(tenantID, customerID uint, externalID, chType string) (*model.Conversation, error) {
	var conv model.Conversation
	err := db.DB.Where("customer_id = ? AND status = ?", customerID, "active").
		Order("id DESC").First(&conv).Error
	if err == nil {
		return &conv, nil
	}
	nc := model.Conversation{
		TenantID: tenantID, CustomerID: customerID, Status: "active",
		Channel: channelTag(chType), SessionID: externalID, Mode: "ai",
		IsAiReplyEnabled: true,
	}
	if cerr := db.DB.Create(&nc).Error; cerr != nil {
		return nil, cerr
	}
	return &nc, nil
}

// channelTag 为通道日志生成稳定标签。
func channelTag(chType string) string {
	switch chType {
	case model.ChannelTypeWechatMP:
		return "wechat_mp"
	case model.ChannelTypeWecomKf:
		return "wecom_kf"
	default:
		return "wecom"
	}
}

// gateReply 内容安全处置（对齐 C1 平台热开关）：shadow 只计数，enforce MASK 改写 / BLOCK 退场语。
func gateReply(reply string, tenantID, convID uint) string {
	cfg := runtimecfg.DefaultSystemConfigService
	if !cfg.GetBoolForTenant(0, "contentsafety_enabled", true) {
		return reply
	}
	mode := cfg.GetStringForTenant(0, "contentsafety_mode", "shadow")
	res := contentsafety.CheckFull(reply)
	if !res.Hit {
		return reply
	}
	metrics.IncContentSafetyHit()
	log.Printf("[通道][内容安全] 会话%d 命中 level=%s mode=%s", convID, res.Level, mode)
	if mode != "enforce" {
		return reply
	}
	if res.Level == contentsafety.LevelBlock {
		metrics.IncContentSafetyBlock()
		return "这个我帮你确认下，稍等一下我回你"
	}
	return res.Cleaned
}

var _ = context.Background // 预留：后续异步出站/CDP 摄入用 ctx
