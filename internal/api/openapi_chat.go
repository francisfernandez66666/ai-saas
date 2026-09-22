// OpenAPI 对话端点：对外渠道嵌入的 chat/completions 兼容接口。
package api

import "ai-scrm/internal/pii"

// ============================================================
// OpenAPI 对话端点（商业化 M4 渠道嵌入，2026-08-29）
//
// POST /openapi/v1/chat/completions —— 对外输出 AI 销售能力（抖音/TikTok/淘宝等渠道嵌入）
// 复用站内全链路：OrchestrateReply（计费/隔离/埋点/留资一致）；
// 响应兼容 OpenAI chat/completions 结构。
//
// 设计要点：
//   1. 鉴权：Bearer sk_（OpenAPIAuth）→ RequirePerm(chat.write)
//   2. 会话归属：external_user_id + session_id → 服务端全量持久化（Customer + Conversation）
//   3. 计费：与站内同池同链路（CheckTokenAvailability 前置闸 + SinkRecordUsage 三桶扣减在 llm 层统一走）
//   4. 硬边界/留资：与站内一致（无关话题拦截、到店倾向留资合并+分配顾问）
// ============================================================

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"ai-scrm/internal/attribution"
	"ai-scrm/internal/chatflow"
	"ai-scrm/internal/db"
	"ai-scrm/internal/engine/flow"
	"ai-scrm/internal/engine/strategy"
	"ai-scrm/internal/logx"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
	"ai-scrm/internal/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// openAPIChatReq OpenAI 兼容请求（扩展渠道字段）
type openAPIChatReq struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	ExternalUserID string  `json:"external_user_id"` // 渠道侧用户ID（必填，服务端映射为 Customer）
	SessionID      string  `json:"session_id"`       // 渠道侧会话ID（可选，隔离同一用户多轮会话）
	Channel        string  `json:"channel"`          // 渠道标识：douyin/tiktok/taobao...（默认 openapi）
	Temperature    float64 `json:"temperature"`
	Stream         bool    `json:"stream"` // 是否流式返回（SSE，OpenAI 兼容逐帧；默认 false 全量返回）
}

// P1-27 修复(2026-09-09)：手机号提取统一走 chatflow.PhoneRegex（含\b边界，防订单号/长数字串
// 子串误命中误触发留资+顾问分配+企微推送），替换此前缺边界的本地正则。
func detectPhone(input string) string {
	return chatflow.PhoneRegex.FindString(input)
}

// openAPIChatCtx A3 收敛（2026-09-22 批四）：OpenAPI 对话链路的阶段上下文。
//
// 为什么非改不可：本入口此前是全系统唯一"绕过合并队列"的对话写入口——直接
// `Infer` + `OrchestrateReply`，既没有 batchID/epoch（无投递认领、无 SetReply 归还），
// 也没有人工锁定态早退。后果是三件：
//  1. 外部渠道客户连发 3 条 → 3 次 AI 调用、3 条互相矛盾的回答（web/通道链只会 1 条合并回答），配额白烧；
//  2. 会话被顾问锁定后，OpenAPI 仍照常以 AI 身份抢答，人工接管形同虚设；
//  3. 归因样本 reply_attributions 里 openapi 行的口径与 web 行不可比（批五择臂的前提被破坏）。
//
// 现在与两条 C 端链同构：入站落库 → 硬边界 → 人工锁定裁决 → 入队合并 → 持权者生成 →
// SetReply 归还批次 → ClaimReplyDelivery 认领出站。响应契约（OpenAI chat/completions
// 裸 JSON / SSE 逐帧）逐字不变，调用方无感。
type openAPIChatCtx struct {
	c            *gin.Context
	tenantID     uint
	trace        string // E3：入站 trace，透传给合并队列做同 trace 日志贯穿
	req          openAPIChatReq
	userInput    string // 本次请求最后一条 user 消息
	channel      string
	customer     *model.Customer
	conversation *model.Conversation

	mergedContent   string // 入队后本批次的合并内容（持权者据此生成）
	processEpoch    uint64 // 批次代际：SetReply / ClaimReplyDelivery 的 fencing 凭据
	holdsProcessing bool   // 是否持有处理权（true 时任何早退都必须 SetReply 归还）
}

// OpenAPI 出站面常量
const (
	// openAPIDeliveryChannel 投递认领的通道维度。取 0 与真实通道（tenant_channels.id 从 1 起）
	// 天然分键：同一批次里"微信客服 worker 出站到微信"和"OpenAPI 同步回给渠道方"是两条
	// 独立出站路径，彼此不该互斥；0 只用来封堵 OpenAPI 自身在同批内的重复落库/重复回包。
	openAPIDeliveryChannel = 0
	// openAPIDegradedReply 等待者兜底文案：与 MessageQueueService.WriteDegradedNotice 同源，
	// 处理者实例宕机等极端场景下，同步调用方也要拿到一句人话而不是空 content。
	openAPIDegradedReply = "系统繁忙，请稍等片刻再试一次"
)

// OpenAPIChatCompletions POST /openapi/v1/chat/completions —— 通过 API Key 发起对话。
// 各阶段方法返回 true 表示已响应客户端，主流程直接 return。
func OpenAPIChatCompletions(c *gin.Context) {
	extendWriteDeadlineForAI(c) // D4：OpenAPI 对话同步等 AI 链，延长本连接写截止（调用方 http 超时自管）

	s := &openAPIChatCtx{
		c:        c,
		tenantID: middleware.EffectiveTenantID(c),
		trace:    middleware.GetTraceID(c), // P1-3/E3：全链路 trace
	}
	if s.openAPIBind() {
		return
	}
	if s.openAPIResolveParty() {
		return
	}
	if s.openAPIStoreInbound() {
		return
	}
	if s.openAPIHardBoundary() {
		return
	}
	if s.openAPITakeover() {
		return
	}
	if s.openAPIEnqueue() {
		return
	}
	s.openAPIProcessAndReply()
}

// openAPIBind 解析请求体并取出本轮输入。副作用：绑定失败(400)/缺 external_user_id(400)/
// messages 无用户内容(400) 时响应并 return true；否则填充 s.req / s.userInput / s.channel。
func (s *openAPIChatCtx) openAPIBind() bool {
	if err := s.c.ShouldBindJSON(&s.req); err != nil {
		log.Printf("[OpenAPI][trace=%s] 参数绑定失败 tenant=%d: %v", s.trace, s.tenantID, err)
		RespErrBind(s.c, err)
		return true
	}
	if s.req.ExternalUserID == "" {
		RespErr(s.c, http.StatusBadRequest, 400, "external_user_id 必填")
		return true
	}
	// 取最后一条用户消息作为本轮输入（OpenAI 多轮数组里渠道方可能把历史一起发来，只回本轮）
	for i := len(s.req.Messages) - 1; i >= 0; i-- {
		if s.req.Messages[i].Role == "user" && s.req.Messages[i].Content != "" {
			s.userInput = s.req.Messages[i].Content
			break
		}
	}
	if s.userInput == "" {
		RespErr(s.c, http.StatusBadRequest, 400, "messages 中无用户内容")
		return true
	}
	s.channel = s.req.Channel
	if s.channel == "" {
		s.channel = "openapi"
	}
	return false
}

// openAPIResolveParty 解析/创建客户与会话。副作用：任一失败(500) 时响应并 return true。
func (s *openAPIChatCtx) openAPIResolveParty() bool {
	customer, err := resolveOpenAPICustomer(s.c, s.tenantID, s.channel, s.req.ExternalUserID)
	if err != nil {
		RespErr(s.c, http.StatusInternalServerError, 500, "客户解析失败")
		return true
	}
	s.customer = customer

	conversation, err := resolveOpenAPIConversation(s.c, s.tenantID, customer.ID, s.channel, s.req.SessionID)
	if err != nil {
		RespErr(s.c, http.StatusInternalServerError, 500, "会话解析失败")
		return true
	}
	s.conversation = conversation
	return false
}

// openAPIStoreInbound 客户消息落库 + 接钩归因回填 + 顾问端推送 + 持续打标。
// 副作用：落库失败(500) 时响应并 return true。
func (s *openAPIChatCtx) openAPIStoreInbound() bool {
	now := time.Now()
	customerMsg := model.Message{
		TenantID:       s.tenantID,
		ConversationID: s.conversation.ID,
		CustomerID:     s.customer.ID,
		SenderType:     "customer",
		Content:        s.userInput,
		MessageType:    "text",
		CreatedAt:      now,
	}
	if err := db.RQ(s.c).Create(&customerMsg).Error; err != nil {
		RespErr(s.c, http.StatusInternalServerError, 500, "消息落库失败")
		return true
	}
	// D9：外部渠道客户回复同样回填上一轮 AI 接钩归因。
	_ = attribution.MarkHookedBeforeMessage(s.tenantID, s.conversation.ID, customerMsg.ID)
	// P1-1 实时推送：外部渠道客户新消息通知本租户顾问端（推送消息内容，前端即时更新）
	notifyWSWithContent(s.tenantID, s.customer.ID, s.conversation.ID, "customer",
		customerMsg.ID, s.userInput, s.customer.Name, now.Format("2006-01-02T15:04:05Z"))

	// 持续打标（与站内一致）
	if autoTags, tagErr := service.DefaultTagService.AutoTagFromText(s.tenantID, s.customer.ID, s.userInput); tagErr == nil && len(autoTags) > 0 {
		var uc model.Customer
		if db.RQ(s.c).First(&uc, s.customer.ID).Error == nil {
			service.DefaultTagService.ApplyTagWeightsToTVector(s.tenantID, &uc, uc.BuildBaseTVector())
			db.RQ(s.c).Model(&uc).Update("t_vector", uc.TVectorJSON)
		}
	}
	return false
}

// openAPIHardBoundary 第一层：无关话题硬边界（0 延迟，不入队、不消耗配额）。
// 副作用：命中时落库 AI 消息并响应 return true。
func (s *openAPIChatCtx) openAPIHardBoundary() bool {
	if !service.IsOffTopicForTenant(s.tenantID, s.userInput) {
		return false
	}
	reply := service.GetOffTopicReplyForTenant(s.tenantID, s.userInput)
	persistOpenAPIAIMessage(s.c, s.conversation, s.customer.ID, s.tenantID, s.channel, reply, 0, "", "offtopic_hardbound")
	openAIRespond(s.c, s.req.Stream, s.req.Model, reply, estimateTokens(s.userInput), estimateTokens(reply))
	return true
}

// openAPITakeover 人工锁定态早退（A1 语义 + A3 补齐）。
//
// 放在入队**之前**：这轮既然不由 AI 应答，就不该为它开一个合并批次（开了就得再归还处理权，
// 且会把后续真正的 AI 轮次并进一个"没人生成回复"的批）。裁决走唯一真相源
// chatflow.HumanTakeoverDecide，与 web/C 端链同判（超时会由该函数自己落库重开 AI）。
//
// 跳过 AI 时不落库 AI 消息（顾问的话才是本轮答案，落一条 AI 冒充回复会污染历史与归因），
// 只刷新会话活跃时间并回一句中性托管话术——OpenAPI 是同步接口，没有"静默不回包"这一选项。
func (s *openAPIChatCtx) openAPITakeover() bool {
	dec := chatflow.HumanTakeoverDecide(s.conversation)
	if dec.Action != chatflow.TakeoverSkipAI {
		if dec.Reopened {
			log.Printf("[OpenAPI] 会话%d 顾问超时未回复，已自动重开AI回复", s.conversation.ID)
		}
		return false
	}
	log.Printf("[OpenAPI] 客户%d 会话%d 人工锁定态跳过AI(route=%s)", s.customer.ID, s.conversation.ID, dec.Route)
	now := time.Now()
	db.RQ(s.c).Model(&model.Conversation{}).Where("id = ? AND tenant_id = ?", s.conversation.ID, s.tenantID).
		Update("last_message_at", now)
	reply := service.GetHumanTakeoverReplyForTenant(s.tenantID, s.userInput)
	openAIRespond(s.c, s.req.Stream, s.req.Model, reply, estimateTokens(s.userInput), estimateTokens(reply))
	return true
}

// openAPIRelease 归还处理权（P0-8 纪律）：持权者的每条早退路径都必须 SetReply，
// 否则同批等待者要挂到 processing_lock_timeout（默认 600s）才被自愈释放。
// 有回复正文就带正文（等待者能直接复用），没有则带空串表示"本批不产出"。
func (s *openAPIChatCtx) openAPIRelease(reply string) {
	if s.holdsProcessing {
		service.DefaultMessageQueueService.SetReply(s.tenantID, s.customer.ID, s.processEpoch, reply)
	}
}

// openAPIEnqueue 第三层：入合并队列并等窗口收账。
// 副作用：三分支——简单消息直接快速回、等待者复用处理者回复、拿到处理权则继续走生成；
// 前两支响应后 return true。
//
// 与 web 链的一处刻意的差异：本链不做"打字延迟/模拟真人延迟"（CancellableSleep）。
// OpenAPI 的调用方是渠道服务端而非人类客户端，人为拖延只会撞对方的 http 超时，
// 且外部渠道的话术人设由渠道侧自己渲染。
func (s *openAPIChatCtx) openAPIEnqueue() bool {
	logx.WithTrace(middleware.CtxWithTrace(s.c)).Info("openapi 入站", "tenant_id", s.tenantID, "customer_id", s.customer.ID)
	mergedContent, shouldProcess, waiterReply, _, isSimple, _, epoch :=
		service.DefaultMessageQueueService.EnqueueAndWait(s.tenantID, s.customer.ID, s.userInput, s.trace)
	s.mergedContent = mergedContent
	s.processEpoch = epoch

	// 简单消息（"在吗"类）：不进批次、不与其它请求争锁
	if isSimple {
		defer service.DefaultMessageQueueService.SimpleMessageDone(s.tenantID, s.customer.ID)
		simpleReply := service.GetSimpleReply(s.userInput)
		persistOpenAPIAIMessage(s.c, s.conversation, s.customer.ID, s.tenantID, s.channel,
			simpleReply, 0, "", "simple_fast")
		openAIRespond(s.c, s.req.Stream, s.req.Model, simpleReply,
			estimateTokens(s.userInput), estimateTokens(simpleReply))
		return true
	}

	// 等待者：本条已被并进别人持有的批次，回复由处理者生成并落库。
	// 这里只回响应，绝不重复落库/重复扣费（重复落库会在站内历史里出现两条 AI 消息）。
	if !shouldProcess {
		txt := waiterReply
		if txt == "" {
			txt = openAPIDegradedReply
		}
		openAIRespond(s.c, s.req.Stream, s.req.Model, txt,
			estimateTokens(s.userInput), estimateTokens(txt))
		return true
	}

	s.holdsProcessing = true
	return false
}

// openAPIProcessAndReply 持权者的正常流程：留资捕获 → 策略推理 → 人工分支早退 →
// 编排层生成 → 内容安全闸门 → 投递认领 → 落库/归因 → SetReply 唤醒等待者 → 响应。
// 输入一律用合并后的 s.mergedContent（不再用单条 s.userInput），与站内口径一致。
func (s *openAPIChatCtx) openAPIProcessAndReply() {
	tenantID, customer, conversation := s.tenantID, s.customer, s.conversation

	// 到店倾向/留资（外部渠道同样捕获线索 → OneID 合并+留资+分配顾问）
	if service.IsStoreVisitIntentForTenant(tenantID, s.mergedContent) && !isCapturedStage(customer.JourneyStage) {
		if phone := detectPhone(s.mergedContent); phone != "" {
			if mergedID := chatflow.MergeCustomerByPhone(customer, phone); mergedID > 0 {
				var reloaded model.Customer
				if db.RQ(s.c).First(&reloaded, mergedID).Error == nil {
					customer = &reloaded
					s.customer = &reloaded
					// 重新定位活跃会话（合并后可能切换）
					var conv model.Conversation
					if db.RQ(s.c).Where("customer_id = ? AND status = ?", customer.ID, "active").
						Order("updated_at DESC").First(&conv).Error == nil {
						conversation = &conv
						s.conversation = &conv
					}
				}
			}
			applyOpenAPILeadCapture(s.c, customer, phone)
		}
	}

	// 构建策略输入 → 推理（与顾问触发 AI 同链路）
	tVector := customer.BuildBaseTVector()
	strategyInput := strategy.StrategyInput{
		TVector:        tVector,
		State:          conversation.GetState(),
		CustomerInput:  s.mergedContent,
		CustomerTags:   customer.GetTags(),
		CustomerID:     customer.ID,
		ConversationID: conversation.ID,
		CanPromote:     customer.CanPromote(),
		JourneyStage:   customer.JourneyStage,
		TenantID:       tenantID,
		DeptIDs:        nil, // 外部渠道无顾问部门链，纯租户语境（仅见行业+企业两层包）
	}
	strategyOutput := strategy.DefaultEngine.Infer(strategyInput)

	// P2-21 修复(2026-09-09)：路由结果为 human/pending_human 时不再发 AI 话术——
	// 原实现忽略 RouteResult 直接 OrchestrateReply，已留资线索被 AI 接管（与站内两分支语义矛盾）。
	// A3 补充：本分支已持处理权，早退前必须 SetReply 归还，否则等待者挂死。
	if strategyOutput.RouteResult == "human" || strategyOutput.RouteResult == "pending_human" {
		reply := service.GetHumanTakeoverReplyForTenant(tenantID, s.mergedContent)
		aiMsgID := persistOpenAPIAIMessage(s.c, conversation, customer.ID, tenantID, s.channel, reply, 0, "", "human_takeover")
		_ = attribution.MarkPendingHuman(tenantID, conversation.ID, customer.ID)
		notifyWSWithContent(tenantID, customer.ID, conversation.ID, "ai",
			aiMsgID, reply, "AI顾问", time.Now().Format("2006-01-02T15:04:05Z"))
		s.openAPIRelease(reply)
		openAIRespond(s.c, s.req.Stream, s.req.Model, reply,
			estimateTokens(s.mergedContent), estimateTokens(reply))
		return
	}

	aiReply := flow.DefaultEngine.OrchestrateReply(middleware.CtxWithTrace(s.c), customer, conversation.ID, s.mergedContent, &strategyOutput, nil)

	// 内容安全闸门（C1）：外部渠道出站同样过滤；BLOCK 用退场语替换，绝不下发违规原文
	if action, out := ContentsafetyGate(aiReply, conversation.ID); aiReply != "" && action != GatePass {
		if action == GateRewrite {
			aiReply = out
		} else {
			aiReply = SafetyHandoffReply()
			log.Printf("[OpenAPI] 会话%d 内容安全拦截，已用退场语替换", conversation.ID)
		}
	}

	// A3 投递认领：同批次同出站面只允许一次"落库 + 推送"。认领失败说明同批已有 OpenAPI
	// 侧完成投递（极端：处理权被超时自愈转交后又复活），此时把已生成的文本回给调用方，
	// 但不再重复写消息行——宁可外部渠道看到一条重复话术，也不在站内历史里留双行。
	if !service.DefaultMessageQueueService.ClaimReplyDelivery(tenantID, customer.ID, s.processEpoch, openAPIDeliveryChannel) {
		log.Printf("[OpenAPI] 客户%d 批次%d 投递权已被同批认领，本次不落库不推送", customer.ID, s.processEpoch)
		s.openAPIRelease(aiReply)
		openAIRespond(s.c, s.req.Stream, s.req.Model, aiReply,
			estimateTokens(s.mergedContent), estimateTokens(aiReply))
		return
	}

	// 持久化 AI 消息
	aiMsgID := persistOpenAPIAIMessage(s.c, conversation, customer.ID, tenantID, s.channel, aiReply,
		strategyOutput.FinalAnchor, strategyOutput.TemplateID, "ai_triggered_by_openapi")
	// P1-1 实时推送：外部渠道 AI 回复通知客户端与顾问端（推送消息内容，前端即时更新）
	notifyWSWithContent(tenantID, customer.ID, conversation.ID, "ai",
		aiMsgID, aiReply, "AI顾问", time.Now().Format("2006-01-02T15:04:05Z"))

	// D9：OpenAPI 外部渠道 AI 回复同样落包/模板归因。
	openIntentAfter := tVector[0] + strategyOutput.IntentDelta
	if openIntentAfter < 0 {
		openIntentAfter = 0
	}
	if openIntentAfter > 1 {
		openIntentAfter = 1
	}
	_ = attribution.RecordReply(attribution.RecordReplyInput{
		TenantID:       tenantID,
		MessageID:      aiMsgID,
		ConversationID: conversation.ID,
		CustomerID:     customer.ID,
		TemplateID:     strategyOutput.TemplateID,
		AnchorType:     strategyOutput.FinalAnchor,
		RouteResult:    "ai_triggered_by_openapi",
		IntentBefore:   tVector[0],
		IntentAfter:    openIntentAfter,
	})

	// 唤醒等待者（含其它实例转交来的消息）后再返回
	s.openAPIRelease(aiReply)

	// 返回 OpenAI 兼容结构（stream=true 走 SSE 逐帧，false 全量 JSON）
	openAIRespond(s.c, s.req.Stream, s.req.Model, aiReply,
		estimateTokens(s.mergedContent), estimateTokens(aiReply))
}

// openAIRespond 按 stream 决定返回形态：
//   - false：直接返回全量 OpenAI chat/completions JSON（与原行为一致）
//   - true：SSE 逐帧流式返回（text/event-stream），兼容 OpenAI streaming 客户端；
//     业务后置处理（引导轮数递减/盲点兜底/反问剥离/意向分）均在生成全量回复后完成，
//     故以分块推送已定稿回复，保证所有业务语义与站内一致（零核心路径改动）。
func openAIRespond(c *gin.Context, stream bool, modelName, content string, promptTokens, completionTokens int) {
	if !stream {
		c.JSON(http.StatusOK, openAICompletion(modelName, content, promptTokens, completionTokens))
		return
	}
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		// 不支持流式刷新则降级全量 JSON
		c.JSON(http.StatusOK, openAICompletion(modelName, content, promptTokens, completionTokens))
		return
	}
	// 按字数切分逐帧推送（每帧一个 token delta），末尾发 [DONE]
	runes := []rune(content)
	step := 4
	for i := 0; i < len(runes); i += step {
		end := i + step
		if end > len(runes) {
			end = len(runes)
		}
		delta := string(runes[i:end])
		frame := gin.H{
			"id":      "chatcmpl-" + time.Now().Format("20060102150405.000000000"),
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   firstNonEmpty(modelName, "ai-scrm"),
			"choices": []gin.H{{
				"index":         0,
				"delta":         gin.H{"role": "assistant", "content": delta},
				"finish_reason": nil,
			}},
		}
		if b, err := json.Marshal(frame); err == nil {
			c.Writer.WriteString("data: " + string(b) + "\n\n")
			flusher.Flush()
		}
	}
	// 结束帧
	done := gin.H{
		"id":      "chatcmpl-" + time.Now().Format("20060102150405.000000000"),
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   firstNonEmpty(modelName, "ai-scrm"),
		"choices": []gin.H{{"index": 0, "delta": gin.H{}, "finish_reason": "stop"}},
	}
	if b, err := json.Marshal(done); err == nil {
		c.Writer.WriteString("data: " + string(b) + "\n\n")
	}
	c.Writer.WriteString("data: [DONE]\n\n")
	flusher.Flush()
}

// firstNonEmpty 返回首个非空字符串
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// resolveOpenAPICustomer external_user_id 租户内解析/创建客户
func resolveOpenAPICustomer(c *gin.Context, tenantID uint, channel, externalUserID string) (*model.Customer, error) {
	var cust model.Customer
	err := db.RQ(c).Where("external_user_id = ?", externalUserID).First(&cust).Error
	if err == nil {
		return &cust, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	name := channel + ":" + externalUserID
	if len(name) > 50 {
		name = name[:50]
	}
	cust = model.Customer{
		TenantID:       tenantID,
		Name:           name,
		Source:         channel,
		ExternalUserID: externalUserID,
		JourneyStage:   model.JourneyAIConnected,
		Status:         1,
		VisitorKey:     model.GenerateVisitorKey(),
	}
	if err := db.RQ(c).Create(&cust).Error; err != nil {
		return nil, err
	}
	return &cust, nil
}

// resolveOpenAPIConversation external_user_id + session_id 解析/创建会话。
// G1 收口(2026-09-16C，AUDIT_GAP_REALITY_2026-09-16)：旧查询不带 status='active' 过滤，
// 可命中已关账会话继续写入；且"查无即插"无锁，与 web/通道并发会各建一条 active（013
// 唯一索引上线后撞约束变 500）。现补 active 过滤，并统一到 EnsureActiveConversation：
// 精确(session)查不到时，兜底复用该客户任意活跃会话（每客户至多一条 active 的全局不变式），
// 无活跃会话才新建。
func resolveOpenAPIConversation(c *gin.Context, tenantID, customerID uint, channel, sessionID string) (*model.Conversation, error) {
	var conv model.Conversation
	q := db.RQ(c).Where("customer_id = ? AND status = ?", customerID, "active")
	if sessionID != "" {
		q = q.Where("session_id = ?", sessionID)
	} else {
		q = q.Where("session_id = '' OR session_id IS NULL")
	}
	err := q.Order("updated_at DESC").First(&conv).Error
	if err == nil {
		return &conv, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	// 本 session 无活跃会话 → 保障客户唯一活跃会话（可能归属其他 session：跨渠道共存时
	// 以既有会话延续上下文，符合 OneID 单活跃不变式；归属/接管状态一律不改写）
	ensured, _, eerr := chatflow.EnsureActiveConversation(tenantID, customerID, 0, func(cv *model.Conversation) {
		cv.Channel = channel
		cv.SessionID = sessionID
		cv.GuidedDisabled = true // 外部渠道默认关闭引导式反问（渠道侧自管节奏）
	})
	if eerr != nil {
		return nil, eerr
	}
	return &ensured, nil
}

// applyOpenAPILeadCapture 留资标记 + 轮询分配顾问（与站内一致）
func applyOpenAPILeadCapture(c *gin.Context, customer *model.Customer, phone string) {
	leadUpdates := map[string]interface{}{
		"phone":             phone,
		"journey_stage":     model.JourneyLeadCaptured,
		"assignment_reason": "lead_captured",
	}
	if customer.AssignedUserID == 0 {
		var salesUsers []model.User
		db.RQ(c).Where("role = ? AND status = 1", model.RoleSales).Find(&salesUsers)
		if len(salesUsers) > 0 {
			minCount := -1
			best := salesUsers[0].ID
			for _, u := range salesUsers {
				var count int64
				db.RQ(c).Model(&model.Customer{}).Where("assigned_user_id = ? AND status = 1", u.ID).Count(&count)
				if minCount < 0 || int(count) < minCount {
					minCount = int(count)
					best = u.ID
				}
			}
			leadUpdates["assigned_user_id"] = best
		} else {
			// P1-12 修复(2026-09-09)：无销售用户时不再硬编码 uint(2)（跨租户脏分配），
			// 对齐 chat_lead 语义：assigned_user_id=0 进人工池由 PendingHandoff 认领。
			// D1 修复(2026-09-16B)：删除误写入 customers 的 pending_handoff 键——该列属
			// conversations，SQLSTATE 42703 会中止整行 UPDATE，无销售租户留资静默丢失
			//（见 AUDIT_UAT_VERIFY_2026-09-16B D1）。OpenAPI 留资本就不改会话接管态（既有口径）。
			leadUpdates["assigned_user_id"] = uint(0)
		}
	}
	if err := db.RQ(c).Model(customer).Updates(leadUpdates).Error; err != nil {
		log.Printf("[OpenAPI] 留资更新失败 customer=%d: %v", customer.ID, err)
		return
	}
	if v, ok := leadUpdates["phone"].(string); ok {
		customer.Phone = v
	}
	if v, ok := leadUpdates["journey_stage"].(string); ok {
		customer.JourneyStage = v
	}
	if v, ok := leadUpdates["assigned_user_id"].(uint); ok {
		customer.AssignedUserID = v
	}
	log.Printf("[OpenAPI] 客户%d 留资成功 channel 线索: phone=%s stage=lead_captured assigned=%d",
		customer.ID, pii.MaskPhone(phone), customer.AssignedUserID)
}

// persistOpenAPIAIMessage 持久化 AI 回复并刷新会话
// persistOpenAPIAIMessage 持久化 OpenAPI AI 消息并返回消息ID（P1-1）
func persistOpenAPIAIMessage(c *gin.Context, conv *model.Conversation, customerID, tenantID uint, channel, content string, anchor int, tid, route string) uint {
	msg := model.Message{
		TenantID:       tenantID,
		ConversationID: conv.ID,
		CustomerID:     customerID,
		SenderType:     "ai",
		Content:        content,
		MessageType:    "text",
		AnchorType:     anchor,
		TemplateID:     tid,
		RouteResult:    route,
		CreatedAt:      time.Now(),
	}
	if err := db.RQ(c).Create(&msg).Error; err != nil {
		log.Printf("[OpenAPI] AI消息落库失败 conv=%d: %v", conv.ID, err)
		return 0
	}
	// P1-2 修复(2026-09-18)：字段级 Updates，仅刷新最后消息三列，不整行覆写
	now := time.Now()
	conv.LastMessageAt = &now
	conv.LastTid = tid
	conv.LastAnchorType = anchor
	db.RQ(c).Model(conv).Updates(map[string]interface{}{
		"last_message_at":  &now,
		"last_tid":         tid,
		"last_anchor_type": anchor,
	})
	return msg.ID
}

// isCapturedStage 是否已过留资阶段（无需重复捕获）
func isCapturedStage(stage string) bool {
	switch stage {
	case model.JourneyLeadCaptured, model.JourneyArrived, model.JourneyOrdered, model.JourneyDelivered:
		return true
	}
	return false
}

// estimateTokens 粗略 token 估算（中文按字计，仅用于 OpenAI 兼容 usage 字段）
func estimateTokens(s string) int {
	return len([]rune(s))
}

// openAICompletion 组装 OpenAI chat/completions 兼容响应
func openAICompletion(modelName, content string, promptTokens, completionTokens int) gin.H {
	if modelName == "" {
		modelName = "ai-scrm"
	}
	return gin.H{
		"id":      "chatcmpl-" + time.Now().Format("20060102150405.000000000"),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   modelName,
		"choices": []gin.H{{
			"index": 0,
			"message": gin.H{
				"role":    "assistant",
				"content": content,
			},
			"finish_reason": "stop",
		}},
		"usage": gin.H{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		},
	}
}
