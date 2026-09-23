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
	"fmt"
	"log"
	"time"

	"ai-scrm/internal/attribution"
	"ai-scrm/internal/chatflow"
	"ai-scrm/internal/contentsafety"
	"ai-scrm/internal/db"
	"ai-scrm/internal/engine/flow"
	"ai-scrm/internal/engine/strategy"
	"ai-scrm/internal/logx"
	"ai-scrm/internal/model"
	"ai-scrm/internal/service"

	"gorm.io/gorm"
)

// ProcessInbound 处理一条已解密的入站消息（适配器解析后调用）。
// ch：来源通道；in：归一化入站。返回错误仅用于回调侧决定是否重试，业务落库失败已尽量降级。
// D4 修复(2026-09-14)：以 (channel_id, msg_id) 幂等抢占——微信/企微超时重推不再产生双份 AI 回复。
// 命名返回值承载 done 回调标记 processed/failed（D2：失败计数支撑游标安全推进）。
//
// D5 重构(2026-09-16，AUDIT_DEFECT_VERIFY §7)：通道入站并入 web 同款合并队列。
// 旧实现每收必生成必回（inbound.go 旧注释自认"通道未接入合并队列"），同一客户在企微/网页双入口
// 连发消息时网页 25s 窗口合并回一条、微信侧逐条回复——合并语义与延迟节奏分裂（客户可感知）。
// 新结构：同步段只做"幂等抢占 + 身份/会话 + 客户消息落库 + 人工接管闸"（微信 5s ack 压力大幅缓解），
// 随后把"入队等待合并 →（处理者）策略/生成/（被合并者）领取批次唯一回复 → 闸门 → 落库 → 出站"
// 交给 headless worker goroutine，与 web 共用同一批次、同一 epoch fencing、同一 CalcHumanlikeDelay 节奏。
// 已知保留差异（残项更新 2026-09-16C，AUDIT_GAP_REALITY_2026-09-16）：留资硬拦截已由
// D3 批（2026-09-16B）在 worker 处理段实装（本文件 DetectLeadCapture 调用点）；仍缺的是
// 到店快速通道与硬边界第一层——这些属 web handler 内联逻辑，抽取属 §7 计划的
// 后续"chat 管线抽函数"手术。
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
	// E3(2026-09-19)：无回调请求上下文的来源（轮询等）自造 trace，保证 worker 全链路可串
	if in.TraceID == "" {
		in.TraceID = logx.NewTraceID()
	}

	// P1-7 修复(2026-09-15)：MsgID 为空时退用信封摘要作去重锚——旧实现直接放行不抢占，
	// 抓包重放同一份有效报文可无限触发入站落库+重复 AI 出站（无重发成本）。
	dedupID := in.MsgID
	if dedupID == "" {
		dedupID = in.EnvelopeID
	}
	done, skip := claimInbound(ch, dedupID)
	handedOff := false
	defer func() {
		if !handedOff { // 未移交 worker：同步段结局直接写台账（processed/failed）
			done(err)
		}
	}()
	if skip {
		err = nil
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

	// 1. 落客户入站消息（与 web "先落库再入队" 同款纪律：worker 挂窗口期间客户刷新历史不丢这条）
	inMsg := model.Message{
		TenantID: ch.TenantID, ConversationID: conv.ID, CustomerID: customerID,
		SenderType: "customer", Content: in.Content, MessageType: "text",
		RouteResult: "channel_inbound",
	}
	db.DB.Create(&inMsg)
	// D9：渠道入站同样回填上一轮 AI 接钩归因。
	_ = attribution.MarkHookedBeforeMessage(ch.TenantID, conv.ID, inMsg.ID)

	db.DB.Model(&model.Conversation{}).Where("id = ?", conv.ID).Update("last_message_at", time.Now())

	// 2. 人工接管态 / AI 关闭：不出声，等顾问手动回复（顾问回复经出站桥回渠道）。
	//    不入队——避免白白占用一个合并窗口把后续真 AI 消息卷进静默批次。
	if conv.IsHumanLocked || !conv.IsAiReplyEnabled {
		log.Printf("[通道] 会话%d 人工锁定/AI关闭，AI 不回复", conv.ID)
		return nil
	}

	// 3. 移交 headless worker（D5）：入队 → 生成/领取批次回复 → 出站。回调段即刻 ack success。
	go runInboundWorker(ch, in, customerID, conv, inMsg.ID, done, time.Now())
	handedOff = true
	return nil
}

// runInboundWorker D5 headless 等待者：与 web chat 共用合并队列的通道处理循环。
// 三种归宿：
//  1. 简单消息 → 不合并，serial 锁 + 人类化延迟 + 罐头回复（与 web 简单消息分支同款语义）；
//  2. 被并入在途批次（shouldProcess=false）→ EnqueueAndWait 已挂到批次收账，reply 即全批唯一回复
//     （处理者可能是 web 请求或另一路通道 worker）；直接出站，不重复落库（DB 双入口共享）；
//  3. 本 worker 持处理权 → 策略推理 + AI 生成 + 闸门 + 落库 + 人类化延迟 + SetReply(epoch) 唤醒
//     web 等待者 + 出站投递。任何提前退出路径必须 release()（SetReply 空串）释放批次，防等待者挂死。
func runInboundWorker(ch *model.Channel, in *InboundMessage, customerID uint, conv *model.Conversation,
	inMsgID uint, done func(error), workerStart time.Time) {

	tid := ch.TenantID
	// E3(2026-09-19)：worker 无 gin 上下文，按入站消息携带的 trace 造 trace-only ctx
	//（不带取消信号——回调 ack 后连接即断，AI 链不能被其拖死），贯穿队列与 AI 出站。
	workerCtx := logx.ContextWithTrace(context.Background(), in.TraceID)
	var workErr error
	// D8 护栏(2026-09-16B)：release 提升到函数作用域，panic 恢复时必须释放批次——
	// 旧实现 recover 后直接收尾，processing 锁挂死到 600s 超时自愈，期间 web/通道
	// 等待者全部悬着（与 chat_main 处理段 panic 无释放同型缺陷）。
	var release func(string)
	released := false
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[通道] 入站 worker panic 已恢复 channel=%d customer=%d: %v", ch.ID, customerID, r)
			workErr = fmt.Errorf("inbound worker panic: %v", r)
			if release != nil {
				release("") // 空回复释放，epoch fencing 防践踏
			}
		}
		done(workErr) // 台账收尾随 worker——回调 ack 期间状态为 processing，重推被 claim 挡
	}()

	mergedContent, shouldProcess, reply, mergeWaitDuration, isSimple, mergeCount, epoch :=
		service.DefaultMessageQueueService.EnqueueAndWait(tid, customerID, in.Content, in.TraceID)

	// 分支 1：简单消息快速通道（不合并；与 web chat_main 简单消息分支同款节奏）
	if isSimple {
		defer service.DefaultMessageQueueService.SimpleMessageDone(tid, customerID)
		if runtimecfg.DefaultSystemConfigService.GetString("reply_delay_mode", "normal") != "instant" {
			chatflow.CancellableSleep(customerID, service.GetSimpleReplyDelay())
		}
		simpleReply := service.GetSimpleReply(in.Content)
		saveAndDeliver(ch, conv, customerID, simpleReply, "channel_simple")
		// D3 修复(2026-09-16B)：与 web 简单消息分支同款"防漏"留资检测（简单消息一般不含
		// 手机号，但 2-gram 误判进快速通道时不能把留资吞掉）
		var simpleCust model.Customer
		if db.DB.First(&simpleCust, customerID).Error == nil &&
			simpleCust.JourneyStage != model.JourneyLeadCaptured && simpleCust.JourneyStage != model.JourneyArrived &&
			simpleCust.JourneyStage != model.JourneyOrdered && simpleCust.JourneyStage != model.JourneyDelivered {
			if leadResult := chatflow.DetectLeadCapture(in.Content, &simpleCust); leadResult != 0 {
				log.Printf("[通道] 客户%d 留资检测(简单消息防漏,OneID目标=%d)", customerID, leadResult)
			}
		}
		return
	}

	// 分支 2：被合并进在途批次——本条内容已含在对方批次的合并文本里，只送达不生成。
	// D5 投递认领：处理者可能已是同通道另一 worker（它投过），也可能是不出站的 web 请求——
	// "每批每通道恰好一次"交给 ClaimReplyDelivery 原子裁决。
	if !shouldProcess {
		if reply == "" {
			// 处理者走人工/静默路由（如转人工无感知）：通道同样不出声，网页侧自有提示
			return
		}
		if service.DefaultMessageQueueService.ClaimReplyDelivery(tid, customerID, epoch, ch.ID) {
			// 只投递不落库：回复文本已由批次处理者落 messages（DB 双入口共享，重复落=历史双气泡）
			deliverText(ch, conv, customerID, reply)
		}
		return
	}

	// 分支 3：本 worker 是处理者。epoch fencing：无论何种退出，批次必须被释放（空回复也要），
	// 否则 web/通道等待者挂到 processing 锁超时（600s）才自愈。
	// D8：released 标志与 release 闭包已提升到函数头（panic 兜底 defer 需要引用）。
	release = func(text string) {
		if !released {
			released = true
			service.DefaultMessageQueueService.SetReply(tid, customerID, epoch, text)
		}
	}

	// D7 相似抑制（跨窗口止痛）：仅当本批只有本一条通道消息时才判（mergeCount>1 时批次里可能
	// 混着 web 消息，抑制会连带吞掉网页等待者的唯一回复——批内重复本就由合并窗口解决）。
	if mergeCount <= 1 && channelSimilarMerged(tid, conv.ID, customerID, inMsgID, mergedContent) {
		log.Printf("[通道] 客户%d 相似消息抑制重复回复", customerID)
		release("")
		return
	}

	var cust model.Customer
	if err := db.DB.First(&cust, customerID).Error; err != nil {
		release("")
		return
	}
	// D3 修复(2026-09-16B，AUDIT_UAT_VERIFY_2026-09-16B)：通道入站补留资硬拦截——旧实现
	// 零引用 DetectLeadCapture，客户在企微/微信客服/公众号里发手机号不落 phone/阶段/顾问分配，
	// 顾问端永远看不到线索。放在策略推理之前，与 web（chat_main 留资硬拦截）同口径。
	if cust.JourneyStage != model.JourneyLeadCaptured && cust.JourneyStage != model.JourneyArrived &&
		cust.JourneyStage != model.JourneyOrdered && cust.JourneyStage != model.JourneyDelivered {
		if leadResult := chatflow.DetectLeadCapture(mergedContent, &cust); leadResult != 0 {
			log.Printf("[通道] 客户%d 留资检测命中(OneID目标=%d)，重载客户并设1轮引导反问", customerID, leadResult)
			db.DB.First(&cust, customerID) // 同步 journey_stage 等内存字段，供后续推理/延迟判定
			db.DB.Model(&model.Conversation{}).Where("id = ?", conv.ID).Updates(map[string]interface{}{
				"guided_remaining_rounds": 1,
				"guided_disabled":         false,
			})
			conv.GuidedRemainingRounds = 1
			conv.GuidedDisabled = false
		}
	}
	si := strategy.StrategyInput{
		TVector:        cust.BuildBaseTVector(),
		State:          conv.GetState(),
		CustomerInput:  mergedContent, // D5：推理输入用合并后的批次全文（与 web 同口径）
		CustomerID:     cust.ID,
		ConversationID: conv.ID,
		CanPromote:     cust.CanPromote(),
		JourneyStage:   cust.JourneyStage,
		TenantID:       cust.TenantID,
		DeptIDs:        service.DeptChainForUser(conv.AssignedUserID),
	}
	out := strategy.DefaultEngine.Infer(si)
	// D5 路由对齐 web（chat_main 751-820）：旧通道实现非 AI 一律静默，price 询价/human 转人工
	// 在微信侧与网页侧行为分裂（网页会引导到店/发退场词，通道无声）。合并后统一按 web 口径。
	routeGo := out.RouteResult == strategy.RouteAI || out.RouteResult == strategy.RoutePrice
	switch out.RouteResult {
	case strategy.RouteHuman:
		// 直接转人工（硬切，有感知）：退场词 + 锁定会话，与 web 同文案同动作
		exitText := "你好，我现在有点忙，你要不留个信息咱们到店谈，我顺便帮你查一下你的问题"
		db.DB.Model(&model.Conversation{}).Where("id = ?", conv.ID).Updates(map[string]interface{}{
			"mode": "human", "is_human_locked": true, "is_ai_reply_enabled": false,
		})
		conv.Mode, conv.IsHumanLocked, conv.IsAiReplyEnabled = "human", true, false
		release(exitText)
		saveAndDeliver(ch, conv, customerID, exitText, "channel_human_exit")
		return
	case strategy.RoutePendingHuman:
		// D7 修复(2026-09-16B，AUDIT_UAT_VERIFY_2026-09-16B)：软接管与 web 同口径——
		// 旧实现在 default 分支静默，客户在微信里干等永不回复。退场词+锁会话+关AI回复，
		// 顾问分配同样推迟到客户回手机号（留资拦截已在本 worker 前段接上）。
		pendingExit := "你好，我现在有点忙，你要不留个信息咱们到店谈，我顺便帮你查一下你的问题"
		db.DB.Model(&model.Conversation{}).Where("id = ?", conv.ID).Updates(map[string]interface{}{
			"mode": "human", "is_human_locked": true, "is_ai_reply_enabled": false,
		})
		conv.Mode, conv.IsHumanLocked, conv.IsAiReplyEnabled = "human", true, false
		release(pendingExit)
		saveAndDeliver(ch, conv, customerID, pendingExit, "channel_pending_human_exit")
		return
	case strategy.RouteFish:
		// D7 修复(2026-09-16B)：养鱼罐头话术对齐 web（旧通道静默）。会话保持 active，
		// 与 web 同：仅内存 Mode=fish 不回写（web 亦未持久化该列）。
		fishText := "好的，你先考虑考虑，有任何问题随时找我~ 我会持续关注你的需求，有好消息也会及时通知你。"
		conv.Mode = "fish"
		release(fishText)
		saveAndDeliver(ch, conv, customerID, fishText, "channel_fish")
		return
	case strategy.RouteAI, strategy.RoutePrice:
		routeGo = true
	default:
		log.Printf("[通道] 会话%d 路由=%s，非AI直出（释放批次不出声）", conv.ID, out.RouteResult)
	}
	if !routeGo {
		release("")
		return
	}

	// 生成 AI 回复（红线：flow→strategy→llm）
	aiReply := flow.DefaultEngine.OrchestrateReply(workerCtx, &cust, conv.ID, mergedContent, &out, si.DeptIDs)
	if aiReply == "" {
		release("")
		return
	}
	aiReply = gateReply(aiReply, tid, conv.ID)

	// 人类化延迟与 web 同款：合并窗口已消耗的时间计入偏移，总时长 2 分钟硬顶截断
	humanlikeDelay := service.CalcHumanlikeDelay(tid, aiReply, mergeWaitDuration, mergeCount,
		service.IsStoreVisitIntentForTenant(tid, mergedContent) && !chatflow.IsLeadCaptured(&cust))
	elapsed := time.Since(workerStart)
	if remaining := 120*time.Second - elapsed; humanlikeDelay > remaining {
		humanlikeDelay = remaining
	}
	if humanlikeDelay < 0 {
		humanlikeDelay = 0
	}
	if runtimecfg.DefaultSystemConfigService.GetString("reply_delay_mode", "normal") != "instant" {
		chatflow.CancellableSleep(customerID, humanlikeDelay)
	}

	// D5 认领先于 release：等待者被唤醒后与处理者抢同一 (epoch,channel) 键，
	// 处理者在批次持有期内先占，等待者（含后续批的）必拿 false——恰好一次投递。
	claim := service.DefaultMessageQueueService.ClaimReplyDelivery(tid, customerID, epoch, ch.ID)
	release(aiReply) // 唤醒 web/其它通道等待者（携带本批代际，旧处理者复活不践踏）
	if claim {
		deliverAI(ch, conv, customerID, aiReply, &out, si.TVector[0], inMsgID)
	}
}

// deliverText D5 等待者路径专用：批次回复文本已由处理者落 messages（双入口共享历史），本函数只投出站。
func deliverText(ch *model.Channel, conv *model.Conversation, customerID uint, text string) {
	if text == "" {
		return
	}
	if _, err := Enqueue(ch.TenantID, ch.ID, customerID, conv.ID, text, "text"); err != nil {
		log.Printf("[通道] 出站投递失败 channel=%d conv=%d: %v", ch.ID, conv.ID, err) // 出站队列自带死信/重发
	}
}

// saveAndDeliver 通道简单消息快速通道回复：落库（历史双入口共享）+ 出站投递。
func saveAndDeliver(ch *model.Channel, conv *model.Conversation, customerID uint, text, routeResult string) {
	if text == "" {
		return
	}
	aiMsg := model.Message{
		TenantID: ch.TenantID, ConversationID: conv.ID, CustomerID: customerID,
		SenderType: "ai", Content: text, MessageType: "text",
		RouteResult: routeResult,
	}
	if err := db.DB.Create(&aiMsg).Error; err != nil {
		log.Printf("[通道] 回复落库失败 conv=%d: %v", conv.ID, err)
	}
	if _, err := Enqueue(ch.TenantID, ch.ID, customerID, conv.ID, text, "text"); err != nil {
		log.Printf("[通道] 出站投递失败 channel=%d conv=%d: %v", ch.ID, conv.ID, err) // 出站队列自带死信/重发
	}
}

// deliverAI 通道处理者分支的 AI 回复落库 + D9 包归因（锚/模板/意图前后值与旧逐条实现同口径）+ 出站。
func deliverAI(ch *model.Channel, conv *model.Conversation, customerID uint, text string,
	out *strategy.StrategyOutput, intentBefore float64, inMsgID uint) {
	intentAfter := intentBefore + out.IntentDelta
	if intentAfter < 0 {
		intentAfter = 0
	}
	if intentAfter > 1 {
		intentAfter = 1
	}
	aiMsg := model.Message{
		TenantID: ch.TenantID, ConversationID: conv.ID, CustomerID: customerID,
		SenderType: "ai", Content: text, MessageType: "text",
		AnchorType: out.FinalAnchor, TemplateID: out.TemplateID, RouteResult: "channel_ai",
	}
	if err := db.DB.Create(&aiMsg).Error; err != nil {
		log.Printf("[通道] AI 回复落库失败 conv=%d: %v", conv.ID, err)
	}
	_ = attribution.RecordReply(attribution.RecordReplyInput{
		TenantID:       ch.TenantID,
		MessageID:      aiMsg.ID,
		ConversationID: conv.ID,
		CustomerID:     customerID,
		TemplateID:     out.TemplateID,
		AnchorType:     out.FinalAnchor,
		RouteResult:    "channel_ai",
		IntentBefore:   intentBefore,
		IntentAfter:    intentAfter,
	})
	if _, err := Enqueue(ch.TenantID, ch.ID, customerID, conv.ID, text, "text"); err != nil {
		log.Printf("[通道] 出站投递失败 channel=%d conv=%d: %v", ch.ID, conv.ID, err)
	}
}

// ensureConversation 取该客户当前活跃会话，无则新建（Channel 标渠道类型，SessionID=externalID）。
// G1 收口(2026-09-16C，AUDIT_GAP_REALITY_2026-09-16)：旧实现裸"先查后插"无锁——多实例
// 并发入站、或与 web 冷启动赛跑会各建一条 active（013 唯一索引上线后将直接撞约束）。
// 现统一走 chatflow.EnsureActiveConversation（Redis 短锁 + 持锁复查 + 撞索引回落复用）。
// AssignedUserID 传 0：归属由留资/分配链路维护，保障会话不越权改写。
func ensureConversation(tenantID, customerID uint, externalID, chType string) (*model.Conversation, error) {
	conv, _, err := chatflow.EnsureActiveConversation(tenantID, customerID, 0, func(cv *model.Conversation) {
		cv.Channel = channelTag(chType)
		cv.SessionID = externalID
		cv.IsAiReplyEnabled = true
	})
	if err != nil {
		return nil, err
	}
	return &conv, nil
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
