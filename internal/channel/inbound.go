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
	"ai-scrm/internal/chatflow"
	"ai-scrm/internal/contentsafety"
	"ai-scrm/internal/db"
	"ai-scrm/internal/engine/flow"
	"ai-scrm/internal/engine/strategy"
	"ai-scrm/internal/model"
	"ai-scrm/internal/service"

	"gorm.io/gorm"
)

// ProcessInbound 处理一条已解密的入站消息（适配器解析后调用）。
// ch：来源通道；in：归一化入站。返回错误仅用于回调侧决定是否重试，业务落库失败已尽量降级。
// D4 修复(2026-09-14)：以 (channel_id, msg_id) 幂等抢占——微信/企微超时重推不再产生双份 AI 回复。
// 命名返回值承载 done 回调标记 processed/failed（D2：失败计数支撑游标安全推进）。
func ProcessInbound(ch *model.Channel, in *InboundMessage) (err error) {
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

	done, skip := claimInbound(ch, in.MsgID)
	defer func() { done(err) }()
	if skip {
		return nil // 重放/在途/死信：直接 ack，不再二次处理
	}

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

	// D7 修复(2026-09-14)：通道入站与 web 链路对齐"相似消息合并"（同 2-gram 重叠>50% + 同时间窗）——
	// 旧实现客户连发近似问题在微信侧得到多条重复 AI 回复，web 侧却只回一条，行为分叉。
	// 通道未接入合并队列（无等待者语义），故只做"抑制重复回复"：消息照常落库（历史可查），跳过本轮生成。
	if channelSimilarMerged(ch.TenantID, conv.ID, customerID, inMsg.ID, in.Content) {
		log.Printf("[通道] 客户%d 相似消息抑制重复回复", customerID)
		return nil
	}

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

// ============================================================
// D4/D2 入站幂等（2026-09-14）
// ============================================================

// claimInbound 以 (channel_id, msg_id) 抢占一条入站消息。
// 返回 done（写回 processed/failed）与 skip（true=重放/他实例在途/死信超限，调用方直接 ack）。
func claimInbound(ch *model.Channel, msgID string) (done func(error), skip bool) {
	if msgID == "" {
		return func(error) {}, false // 无幂等锚（老协议 msgid 可空）：照常处理
	}
	rec := model.ChannelInboundMsg{TenantID: ch.TenantID, ChannelID: ch.ID, MsgID: msgID, Status: "processing", Attempts: 1}
	if err := db.DB.Create(&rec).Error; err == nil {
		return func(e error) { finishInbound(&rec, e) }, false
	}
	var cur model.ChannelInboundMsg
	if db.DB.Where("channel_id = ? AND msg_id = ?", ch.ID, msgID).First(&cur).Error != nil {
		// 台账读不到（DB 异常）：保守跳过本轮——微信侧自有重推机制，等价一次重试
		return func(error) {}, true
	}
	switch {
	case cur.Status == "processed":
		return func(error) {}, true
	case cur.Status == "processing" && time.Since(cur.UpdatedAt) > 5*time.Minute:
		// 处理中但超 5min：实例崩溃残留，接管重试
		db.DB.Model(&cur).Updates(map[string]any{"status": "processing", "attempts": gorm.Expr("attempts + 1")})
		cur.Attempts++
		return func(e error) { finishInbound(&cur, e) }, false
	case cur.Status == "processing":
		return func(error) {}, true // 他实例在途，防双处理
	case cur.Status == "failed" && cur.Attempts < 5:
		db.DB.Model(&cur).Updates(map[string]any{"status": "processing", "attempts": gorm.Expr("attempts + 1")})
		cur.Attempts++
		return func(e error) { finishInbound(&cur, e) }, false
	default: // failed 超限：死信，不再阻塞游标/重推
		log.Printf("[通道][入站死信] channel=%d msg=%s 重试%d次仍失败，不再处理", ch.ID, msgID, cur.Attempts)
		return func(error) {}, true
	}
}

// finishInbound 回写入站处理结果
func finishInbound(rec *model.ChannelInboundMsg, e error) {
	st := "processed"
	if e != nil {
		st = "failed"
	}
	db.DB.Model(rec).UpdateColumn("status", st)
}

// channelSimilarMerged D7：本条入站文本若与最近窗口内(≤2分钟)该客户【此前】消息
// 2-gram 关键词重叠 >50%，视为近似重复提问，抑制本轮生成（消息已落库，历史可查）。
// excludeID：本条刚落库的入站消息 ID，必须排除——否则与自身比对恒 100% 重叠误抑制。
// 与 web 链路 chat_main 的相似合并同语义。返回 true=应抑制。
func channelSimilarMerged(tenantID, convID, customerID, excludeID uint, content string) bool {
	kw := chatflow.ExtractKeywords(content)
	if len(kw) == 0 {
		return false
	}
	var recent []model.Message
	if err := db.DB.Where("customer_id = ? AND conversation_id = ? AND sender_type = ? AND id <> ? AND created_at > ?",
		customerID, convID, "customer", excludeID, time.Now().Add(-2*time.Minute)).
		Order("id DESC").Limit(3).Find(&recent).Error; err != nil {
		return false
	}
	for _, past := range recent {
		pkw := chatflow.ExtractKeywords(past.Content)
		if len(pkw) == 0 {
			continue
		}
		overlap := 0
		for _, w := range kw {
			for _, p := range pkw {
				if w == p {
					overlap++
					break
				}
			}
		}
		if float64(overlap)/float64(len(kw)) > 0.5 {
			return true
		}
	}
	return false
}
