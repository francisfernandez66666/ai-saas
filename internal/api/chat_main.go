// 对话核心API（主链路）：消息接收、四层分流、合并队列与AI回复生成。
package api

import "ai-scrm/internal/metrics"

import "ai-scrm/internal/pii"

// 对话核心API：C端客户与B端销售共用的交互入口，链路为 客户发消息→策略中心7步推理→AI生成回复。
// 含会话竞态保护、三层分流(硬边界/到店快速通道/简单消息)、合并队列、延迟清零、留资检测与OneID合并。

import (
	"ai-scrm/internal/attribution"
	"ai-scrm/internal/chatflow"
	"ai-scrm/internal/db"
	"ai-scrm/internal/engine/flow"
	"ai-scrm/internal/engine/strategy"
	"ai-scrm/internal/logx"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
	"ai-scrm/internal/mq"
	"ai-scrm/internal/redisclient"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/schema"
	"ai-scrm/internal/service"
	"context"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 会话创建互斥锁（解决并发请求重复创建会话的竞态问题）
//
// 场景：客户连发多条消息时，多个并发请求同时到达，
// 如果都看到 conversation_id=0，会各自创建新会话，
// 导致同一客户出现多个活跃会话。用客户级互斥锁确保：
//  1. 同一客户同一时刻只有一个请求执行"查找或创建会话"
//  2. 先查已有活跃会话，有则复用；没有才创建新会话+冷启动秒回
//  3. Chat() 和 ChatTest() 统一使用此机制，行为一致
//
// ============================================================

// ============================================================
// 延迟清零（立即回复）机制
// 修复问题3：顾问/管理员点击"立即回复"按钮时，跳过当前客户的模拟真人延迟
// 设计：用sync.Map存储每个客户的延迟取消通道，当ClearDelay被调用时发送信号
// time.Sleep替换为select-based的可取消延迟，收到取消信号后立即继续
// ============================================================

// ============================================================
// 对话相关API
// 这是系统的核心交互接口，C端客户和B端销售共用
// 核心流程：客户发消息 → 策略中心决策 → AI生成回复 → 返回结果
// ============================================================

// Chat 对话接口（客户发消息，获取AI回复）
//
// 完整执行流程：
// 1. 验证客户身份
// 2. 获取或创建会话
// 3. 保存客户消息
// 4. 调用策略中心引擎（7步推理）
// 5. 根据路由结果决定：AI回复 / 转人工 / 养鱼
// 6. 调用AI生成最终回复（模拟模式用规则）
// 7. 更新客户画像（意向分等）
// 8. 更新会话状态
// 9. 返回结果

// chatSessionCtx 主对话链路在请求生命周期内的共享可变上下文。
// 用于把 Chat() 这个 god function 按既有注释段机械拆成可独立阅读的方法，
// 所有方法都挂在本结构体上，闭包引用的外部变量显式收口为字段，避免超长参数表。
// 仅做"剪切-粘贴成方法"，不改变任何条件顺序 / SQL / Redis 键 / trace 语义 / 文案。
type chatSessionCtx struct {
	c            *gin.Context
	tenantID     uint
	trace        string
	requestStart time.Time
	req          schema.ChatRequest
	customer     model.Customer
	conversation model.Conversation

	// 合并队列产出（chatInferRoute 内 EnqueueAndWait 填充，供处理权阶段消费）
	mergedContent     string
	mergeWaitDuration time.Duration
	mergeCount        int
	processEpoch      uint64

	// 处理阶段产出
	aiReply        string
	routeResult    string
	strategyOutput strategy.StrategyOutput
	tVector        [32]float64
	strategyInfo   schema.StrategyInfo

	// 跨阶段共享的副作用产物
	newTags              []string
	leadCapturedResult   int
	customerMsgID        uint
	customerMsgCreatedAt time.Time

	// takeover 人工锁定态裁决（A1，chatEnsureConversation 判定 / chatSaveInbound 消费）
	takeover chatflow.TakeoverDecision
}

// Chat POST /api/v1/chat 正式对话入口（JWT链；硬边界→快速通道→简单消息→合并队列四层分流）
//
// 行为零变化说明：本函数仅把原 Chat() 体按既有注释段"剪切-粘贴"为 chatSessionCtx 上的若干方法，
// 每个方法返回 (done bool) 表示"已响应客户端、主函数可直接 return"。所有条件顺序、SQL、Redis 键、
// trace 语义、文案字符串均与原实现逐字一致。
func Chat(c *gin.Context) {
	extendWriteDeadlineForAI(c) // D4：同步链路最坏 25+15+110s，延长本连接写截止（write_deadline.go）
	requestStart := time.Now()

	tenantID := middleware.EffectiveTenantID(c)
	trace := middleware.GetTraceID(c) // P1-3：全链路 trace

	s := &chatSessionCtx{
		c:            c,
		tenantID:     tenantID,
		trace:        trace,
		requestStart: requestStart,
	}

	if s.chatResolveCustomer() {
		return
	}
	if s.chatEnsureConversation() {
		return
	}
	if s.chatMergeSuppress() {
		return
	}
	if s.chatSaveInbound() {
		return
	}
	if s.chatInferRoute() {
		return
	}
}

// chatResolveCustomer 步骤1：身份校验 + 懒下发 visitor_key。
// 副作用：ShouldBindJSON 失败 / 客户不存在时直接响应并 return true；否则填充 s.customer。
func (s *chatSessionCtx) chatResolveCustomer() bool {
	var req schema.ChatRequest
	if err := s.c.ShouldBindJSON(&req); err != nil {
		log.Printf("[对话][trace=%s] 参数绑定失败 tenant=%d: %v", s.trace, s.tenantID, err)
		RespErrBind(s.c, err)
		return true
	}
	s.req = req

	// 1. 查询客户（加入租户隔离）
	var customer model.Customer
	result := db.RQ(s.c).Scopes(db.T(s.c)).First(&customer, req.CustomerID)
	if result.Error != nil {
		RespErr(s.c, http.StatusNotFound, 404, "客户不存在")
		return true
	}

	// C3：懒下发访客密钥（历史客户可能没有），供客户端持久化并在 history/welcome 携带
	if customer.VisitorKey == "" {
		customer.VisitorKey = model.GenerateVisitorKey()
		db.RQ(s.c).Model(&customer).Update("visitor_key", customer.VisitorKey)
	}

	s.customer = customer
	return false
}

// chatEnsureConversation 步骤2：竞态保护下的查找或创建会话。
// 副作用：会话不存在(404) / 冷启动创建失败(500)时响应并 return true；
// 否则填充 s.conversation，并产出人工锁定态裁决 s.takeover（A1：判定即落库，见 chatflow.HumanTakeoverDecide）。
func (s *chatSessionCtx) chatEnsureConversation() bool {
	var conversation model.Conversation

	if s.req.ConversationID > 0 {
		// 前端传了会话ID，直接查找已有会话（无需竞态保护）
		result := db.RQ(s.c).First(&conversation, s.req.ConversationID)
		if result.Error != nil {
			RespErr(s.c, http.StatusNotFound, 404, "会话不存在")
			return true
		}
		// P1-6 修复(2026-09-09)：会话归属校验（防会话级 IDOR）。
		if conversation.CustomerID != s.customer.ID {
			var convCustomer model.Customer
			if err := db.RQ(s.c).First(&convCustomer, conversation.CustomerID).Error; err != nil {
				RespErr(s.c, http.StatusNotFound, 404, "会话不存在")
				return true
			}
			if !customerInDataScope(s.c, convCustomer.AssignedUserID) {
				RespErr(s.c, http.StatusNotFound, 404, "会话不存在")
				return true
			}
		}
	} else {
		// 前端没传会话ID → 需要查找或创建，加锁防止并发竞态
		convMu := chatflow.GetConversationMutex(s.customer.ID)
		convMu.Lock()

		// 先查该客户是否已有活跃会话（复用，不重复创建，加入租户隔离）
		result := db.RQ(s.c).Scopes(db.T(s.c)).Where("customer_id = ? AND status = ?", s.customer.ID, "active").
			Order("updated_at DESC").
			First(&conversation)

		// D8 修复(2026-09-14)：进程内 convMu 跨实例无效——多实例并发首条消息会各建一个
		// active 会话。Redis 短锁裁决：持锁者复查后创建；拿不到锁=他实例在途，等 500ms 复查；
		// 仍未命中才兜底自建（Redis 关闭时维持旧单机语义）。
		if result.Error != nil {
			h := redisclient.TryLock(fmt.Sprintf("conv:create:%d", s.customer.ID), 8*time.Second)
			if h != nil {
				defer h.Unlock()
				result = db.RQ(s.c).Scopes(db.T(s.c)).Where("customer_id = ? AND status = ?", s.customer.ID, "active").
					Order("updated_at DESC").First(&conversation) // 持锁复查，防双查皆空
			} else if redisclient.IsEnabled() {
				time.Sleep(500 * time.Millisecond)
				result = db.RQ(s.c).Scopes(db.T(s.c)).Where("customer_id = ? AND status = ?", s.customer.ID, "active").
					Order("updated_at DESC").First(&conversation)
			}
		}

		if result.Error != nil {
			// 没有活跃会话 → 冷启动，创建新会话 + 秒回消息
			conversation = model.Conversation{
				CustomerID:     s.customer.ID,
				AssignedUserID: s.customer.AssignedUserID,
				Status:         "active",
				Mode:           "ai",
				Channel:        "web",
			}
			// G1 收口(2026-09-16C)：013 唯一索引兜底后，Redis 关闭/他路直插撞车时创建会报约束冲突——复查复用对方那条，
			if cerr := db.RQ(s.c).Create(&conversation).Error; cerr != nil {
				if rerr := db.RQ(s.c).Scopes(db.T(s.c)).Where("customer_id = ? AND status = ?", s.customer.ID, "active").
					Order("updated_at DESC").First(&conversation).Error; rerr != nil {
					log.Printf("[对话-告警] 冷启动创建失败且复查无果: 客户%d err=%v recheck=%v", s.customer.ID, cerr, rerr)
					RespErr(s.c, http.StatusInternalServerError, 500, "会话初始化失败，请重试")
					return true
				}
				log.Printf("[对话] 冷启动撞唯一约束，复用并发方会话%d（客户%d）", conversation.ID, s.customer.ID)
			} else {
				log.Printf("[对话] 冷启动: 创建新会话 %d, 客户 %d", conversation.ID, s.customer.ID)
			}

			// 首次对话自动推进线索状态：→ AI建联
			if s.customer.JourneyStage == "" || s.customer.JourneyStage == model.JourneyAIConnected {
				s.customer.JourneyStage = model.JourneyAIConnected
				db.RQ(s.c).Model(&s.customer).Update("journey_stage", model.JourneyAIConnected)
				log.Printf("[对话] 客户%d自动推进到AI建联状态", s.customer.ID)
			}

			// 启动默认流程（新会话的初始流程引擎）
			flowCtx := &flow.FlowContext{
				TenantID:       s.tenantID,
				CustomerID:     s.customer.ID,
				ConversationID: conversation.ID,
				RouteResult:    "ai",
			}
			flow.DefaultEngine.StartFlow("default_chat_flow", flowCtx)
		} else {
			// 已有活跃会话，直接复用（不触发冷启动秒回）
			log.Printf("[对话] 复用已有会话 %d, 客户 %d", conversation.ID, s.customer.ID)
		}

		convMu.Unlock()
	}

	s.conversation = conversation

	// 3. 检查是否人工超时接管
	// A1 修复(2026-09-22 批四)：旧实现在这里只写内存
	// （s.conversation.Mode="ai" / IsHumanLocked=false）却不落库——请求结束即丢，下一轮从 DB
	// 重载又是锁定态，正式链的客户被永久卡在"顾问正在赶来"；且与免登录链（真落库重开 AI）
	// 给出不同动作，同样本不可比。现统一走 chatflow.HumanTakeoverDecide（判定即落库）。
	// CheckHumanTimeout 仍保留：它管的是软接管 pending_handoff 的超时回退，语义与本裁决正交。
	if chatflow.CheckHumanTimeout(&s.conversation) {
		log.Printf("[对话] 人工超时，AI接管会话: %d", s.conversation.ID)
	}
	s.takeover = chatflow.HumanTakeoverDecide(&s.conversation)
	return false
}

// chatMergeSuppress 步骤3前：相似消息合并为一次回答（合并时间窗 + 在途批次护栏）。
// 副作用：命中合并时落库 suppressed 消息并响应 return true；否则 return false 继续。
// A2 重构(2026-09-22 批四)：窗口计算与重叠度判定下沉到 chatflow.FindSimilarInflightMessage，
// 与免登录 C 端链共用同一实现（此前那段逻辑只存在于本文件，C 端链没有）。
// 本链在消息落库**之前**判定，故 excludeMsgID 传 0。
func (s *chatSessionCtx) chatMergeSuppress() bool {
	inflightBatch := service.DefaultMessageQueueService != nil &&
		service.DefaultMessageQueueService.HasInflightBatch(s.tenantID, s.customer.ID)
	hit, ok := chatflow.FindSimilarInflightMessage(db.RQ(s.c), s.tenantID, s.customer.ID, s.req.Content, inflightBatch, 0)
	if !ok {
		return false
	}
	log.Printf("[相似消息合并] 客户%d 当前:%q 与历史:%q 重叠度%.0f%%, 合并为一次回答",
		s.customer.ID, logx.Safe(s.req.Content, 40), logx.Safe(hit.PastContent, 40), hit.OverlapRate*100)

	suppressedMsg := model.Message{
		ConversationID: s.conversation.ID,
		CustomerID:     s.customer.ID,
		SenderType:     "customer",
		Content:        s.req.Content,
		MessageType:    "text",
		RouteResult:    "merged_suppressed",
		Emotion:        strategy.DetectEmotion(s.req.Content),
		CreatedAt:      time.Now(),
	}
	db.RQ(s.c).Create(&suppressedMsg)

	RespOK(s.c, "success", schema.ChatResponse{
		ConversationID: s.conversation.ID,
		Merged:         true,
		MergedNote:     "相似消息已合并处理",
		CustomerMsgID:  suppressedMsg.ID,
	})
	return true
}

// chatSaveInbound 步骤3-4：落库客户消息 + 回填接钩归因 + WS 推送 + 持续打标。
// 副作用：人工锁定态直接响应"已收到"并 return true；否则填充 s.newTags / 客户消息ID，return false。
func (s *chatSessionCtx) chatSaveInbound() bool {
	now := time.Now()
	customerMsg := model.Message{
		ConversationID: s.conversation.ID,
		CustomerID:     s.customer.ID,
		SenderType:     "customer",
		Content:        s.req.Content,
		MessageType:    "text",
		Emotion:        strategy.DetectEmotion(s.req.Content),
		CreatedAt:      now,
	}
	db.RQ(s.c).Create(&customerMsg)
	// D9：客户消息到达即回填上一轮 AI 回复的接钩归因。
	_ = attribution.MarkHookedBeforeMessage(s.tenantID, s.conversation.ID, customerMsg.ID)
	// P1-1 实时推送：客户新消息通知本租户顾问端（推送消息内容，前端即时更新）
	notifyWSWithContent(s.tenantID, s.customer.ID, s.conversation.ID, "customer",
		customerMsg.ID, s.req.Content, s.customer.Name, now.Format("2006-01-02T15:04:05Z"))

	// 更新会话最后消息时间
	s.conversation.LastMessageAt = &now

	newTags := []string{}
	if autoTags, tagErr := service.DefaultTagService.AutoTagFromText(s.customer.TenantID, s.customer.ID, s.req.Content); tagErr == nil && len(autoTags) > 0 {
		newTags = autoTags
		log.Printf("[对话] 客户%d自动打标(持续): %v", s.customer.ID, autoTags)
		var updatedCustomerForTag model.Customer
		if err := db.RQ(s.c).First(&updatedCustomerForTag, s.customer.ID).Error; err == nil {
			_ = service.DefaultTagService.ApplyTagWeightsToTVector(s.customer.TenantID, &updatedCustomerForTag, updatedCustomerForTag.BuildBaseTVector())
			db.RQ(s.c).Model(&updatedCustomerForTag).Update("t_vector", updatedCustomerForTag.TVectorJSON)
			s.customer = updatedCustomerForTag
		}
	}
	s.newTags = newTags
	s.customerMsgID = customerMsg.ID
	s.customerMsgCreatedAt = customerMsg.CreatedAt

	// 5. 人工锁定态：按 A1 统一裁决决定是否让 AI 代答
	// A1 修复(2026-09-22 批四)：旧条件 `Mode=="human" && IsHumanLocked` 一旦成立就无条件
	// 回"正在赶来"——既不看顾问是否已超时未回，也不做留资检测，客户留了手机号也石沉大海。
	// 现由 chatflow.HumanTakeoverDecide 统一裁决：能代答的已经在上一步落库解锁并放行，
	// 走到这里说明确实该等人；路由标记与免登录链取同一字面量（归因口径对齐），
	// 文案沿用正式链既有话术（客户无感，不需改口径）。
	if s.takeover.Action == chatflow.TakeoverSkipAI {
		db.RQ(s.c).Model(&model.Conversation{}).Where("id = ?", s.conversation.ID).
			Update("last_message_at", now)
		// 与免登录链同款：锁定期客户甩手机号也要即时留资，不能因为"等人"而漏接
		if s.customer.JourneyStage != model.JourneyLeadCaptured && s.customer.JourneyStage != model.JourneyArrived &&
			s.customer.JourneyStage != model.JourneyOrdered && s.customer.JourneyStage != model.JourneyDelivered {
			if leadResult := chatflow.DetectLeadCapture(s.req.Content, &s.customer); leadResult != 0 {
				log.Printf("[对话-留资检测-human_locked] 客户 %d 已留资+分配顾问", s.customer.ID)
			}
		}
		log.Printf("[对话] 会话%d 人工锁定，本轮跳过AI(route=%s)", s.conversation.ID, s.takeover.Route)
		RespOK(s.c, "success", schema.ChatResponse{
			ConversationID: s.conversation.ID,
			Message: gin.H{
				"sender_type": "system",
				"content":     "已收到你的消息，销售顾问正在赶来的路上，请稍候~",
			},
			RouteResult: s.takeover.Route,
			Mode:        "human",
		})
		return true
	}
	return false
}

// chatInferRoute 入队前三层分流（硬边界 / 到店快速通道 / 合并队列）。
// 命中硬边界/分支B/分支C/简单消息/合并等待时直接响应并 return true；否则进入处理权流程。
func (s *chatSessionCtx) chatInferRoute() bool {
	// 第一层：硬边界拦截（0延迟，不走AI，不进队列）
	if service.IsOffTopicForTenant(s.tenantID, s.req.Content) {
		reply := service.GetOffTopicReplyForTenant(s.tenantID, s.req.Content)
		log.Printf("[硬边界][trace=%s] 客户%d 拦截无关话题(入队前): %q → %q", s.trace, s.customer.ID, pii.MaskPhoneInText(s.req.Content), pii.MaskPhoneInText(reply))
		// 保存AI拦截回复消息到DB
		offTopicMsg := model.Message{
			ConversationID: s.conversation.ID,
			CustomerID:     s.customer.ID,
			SenderType:     "ai",
			Content:        reply,
			MessageType:    "text",
			RouteResult:    "offtopic_hardbound",
			CreatedAt:      time.Now(),
		}
		db.RQ(s.c).Create(&offTopicMsg)
		// 旁路直答，不污染在途批次（见原注释 D5/P0-9）。
		RespOK(s.c, "success", gin.H{
			"conversation_id": s.conversation.ID,
			"ai_reply":        reply,
			"route_result":    "offtopic_hardbound",
		})
		return true
	}

	// 第二层：到店倾向快速通道（不进合并队列，直接快速回复）
	if service.IsStoreVisitIntentForTenant(s.tenantID, s.req.Content) {
		if done := s.chatStoreVisitFast(); done {
			return true
		}
		// 已留资客户跳过本层，继续走下面的合并队列正常流程（等价原 goto skipStoreVisitFast）
	}

	// 6. 消息入队 + 合并窗口等待
	logx.WithTrace(middleware.CtxWithTrace(s.c)).Info("chat 入站", "tenant_id", s.tenantID, "customer_id", s.customer.ID)
	mergedContent, shouldProcess, _, mergeWaitDuration, isSimple, mergeCount, processEpoch := service.DefaultMessageQueueService.EnqueueAndWait(s.tenantID, s.customer.ID, s.req.Content, middleware.GetTraceID(s.c))
	s.mergedContent = mergedContent
	s.mergeWaitDuration = mergeWaitDuration
	s.mergeCount = mergeCount
	s.processEpoch = processEpoch

	// 简单消息（"在吗"/"那我撤了"等）直接走快速回复通道
	if isSimple {
		defer service.DefaultMessageQueueService.SimpleMessageDone(s.tenantID, s.customer.ID)
		replyDelayModeSimple := runtimecfg.DefaultSystemConfigService.GetString("reply_delay_mode", "normal")
		if replyDelayModeSimple != "instant" {
			simpleDelay := service.GetSimpleReplyDelay()
			chatflow.CancellableSleep(s.customer.ID, simpleDelay)
		}

		simpleReply := service.GetSimpleReply(s.req.Content)

		simpleMsg := model.Message{
			ConversationID: s.conversation.ID,
			CustomerID:     s.customer.ID,
			SenderType:     "ai",
			Content:        simpleReply,
			MessageType:    "text",
			RouteResult:    "simple_fast",
			CreatedAt:      time.Now(),
		}
		db.RQ(s.c).Create(&simpleMsg)

		// 简单消息路径也做留资检测（防漏）
		if s.customer.JourneyStage != model.JourneyLeadCaptured && s.customer.JourneyStage != model.JourneyArrived &&
			s.customer.JourneyStage != model.JourneyOrdered && s.customer.JourneyStage != model.JourneyDelivered {
			leadResult := chatflow.DetectLeadCapture(s.req.Content, &s.customer)
			if leadResult != 0 {
				log.Printf("[留资检测-简单消息] 客户 %d 已留资", s.customer.ID)
				if leadResult > 0 {
					log.Printf("[留资检测-简单消息-OneID] 前端需切换customer_id: %d → %d", s.req.CustomerID, leadResult)
				}
			}
		}

		RespOK(s.c, "success", gin.H{
			"conversation_id": s.conversation.ID,
			"ai_reply":        simpleReply,
			"message":         simpleMsg,
		})
		return true
	}

	if !shouldProcess {
		// 合并请求只返回合并状态，不返回完整AI回复
		RespOK(s.c, "success", schema.ChatResponse{
			ConversationID: s.conversation.ID,
			Merged:         true,
			MergedNote:     "本条消息已与先前的消息合并处理，回复将在主请求中返回",
		})
		return true
	}

	// 拿到处理权的请求，用合并后的内容走完整流程
	return s.chatProcessAndReply()
}

// chatStoreVisitFast 到店倾向快速通道（第二层）。
// 已留资客户返回 false 跳过本层；含手机号走分支B(留资)返回 true；未留资走分支C(两段式)返回 true。
func (s *chatSessionCtx) chatStoreVisitFast() bool {
	// 前置拦截：已留资客户不再走到店快速通道
	if s.customer.JourneyStage == model.JourneyLeadCaptured ||
		s.customer.JourneyStage == model.JourneyArrived ||
		s.customer.JourneyStage == model.JourneyOrdered ||
		s.customer.JourneyStage == model.JourneyDelivered {
		log.Printf("[到店倾向] 客户%d 已是%s阶段，跳过到店快速通道，走正常流程",
			s.customer.ID, s.customer.JourneyStage)
		return false
	}

	// 留资前置检测：当前消息是否包含手机号
	phoneMatch := chatflow.PhoneRegex.FindString(s.req.Content)

	if phoneMatch != "" {
		// ====== 分支B：已留资线索（硬编码） ======
		return s.chatLeadCapture(phoneMatch)
	}

	// ====== 分支C：未留资线索（关闭引导+两段式AI快速回复，推迟分配顾问） ======
	firstReply := service.GetStoreVisitFirstReply(s.tenantID, s.req.Content)
	firstDelay := service.GetStoreVisitFirstDelay() // 10-15秒

	log.Printf("[到店倾向-未留资线索] 客户%d 关闭引导+两段式回复, 推迟分配顾问", s.customer.ID)

	// 关闭引导式反问
	s.conversation.GuidedDisabled = true
	db.RQ(s.c).Model(&s.conversation).Update("guided_disabled", true)

	// 第一段AI快速回复（接住意向）
	chatflow.CancellableSleep(s.customer.ID, firstDelay)

	storeVisitMsg := model.Message{
		ConversationID: s.conversation.ID,
		CustomerID:     s.customer.ID,
		SenderType:     "ai",
		Content:        firstReply,
		MessageType:    "text",
		RouteResult:    "store_visit_fast",
		CreatedAt:      time.Now(),
	}
	db.RQ(s.c).Create(&storeVisitMsg)

	// P1-1 实时推送：客户消息 + 第一段快速回复
	notifyWSWithContent(s.tenantID, s.customer.ID, s.conversation.ID, "customer", s.customerMsgID, s.req.Content, s.customer.Name, s.customerMsgCreatedAt.Format("2006-01-02T15:04:05Z"))
	notifyWSWithContent(s.tenantID, s.customer.ID, s.conversation.ID, "ai", storeVisitMsg.ID, firstReply, "AI顾问", storeVisitMsg.CreatedAt.Format("2006-01-02T15:04:05Z"))

	// 第二段追问（异步，25-45秒后发出，收集预约信息）
	secondReply := service.GetStoreVisitSecondReply(s.tenantID, s.req.Content)
	goroutineTenantID := s.tenantID
	go func(cid, convID uint, content string, tid uint) {
		sd := service.GetStoreVisitSecondDelay()
		chatflow.CancellableSleep(cid, sd)
		secondMsg := model.Message{
			TenantID:       tid, // 显式盖章，不依赖请求上下文
			ConversationID: convID,
			CustomerID:     cid,
			SenderType:     "ai",
			Content:        content,
			MessageType:    "text",
			RouteResult:    "store_visit_fast",
			CreatedAt:      time.Now(),
		}
		if err := db.DB.WithContext(db.WithTenant(context.Background(), tid)).Create(&secondMsg).Error; err != nil {
			log.Printf("[到店倾向-告警] 客户%d 第二段追问落库失败(首次)，重试一次: %v", cid, err)
			if err2 := db.DB.WithContext(db.WithTenant(context.Background(), tid)).Create(&secondMsg).Error; err2 != nil {
				log.Printf("[到店倾向-告警] 客户%d 第二段追问落库失败(重试后放弃): %v", cid, err2)
				metrics.IncStoreVisitSecondFail()
				return
			}
		}
		log.Printf("[到店倾向-未留资线索] 客户%d 第二段追问已发送", cid)
	}(s.customer.ID, s.conversation.ID, secondReply, goroutineTenantID)

	RespOK(s.c, "success", gin.H{
		"conversation_id": s.conversation.ID,
		"ai_reply":        firstReply,
		"route_result":    "store_visit_fast",
	})
	return true
}

// chatLeadCapture 到店倾向快速通道「分支B：已留资线索」。手机号命中即标记留资+分配顾问(+OneID合并)
// + pending_handoff，并发硬编码确认回复，不走AI。等价原 Chat() 分支B。返回 true 表示已响应客户端。
func (s *chatSessionCtx) chatLeadCapture(phoneMatch string) bool {
	log.Printf("[到店倾向-已留资线索] 客户%d 消息含手机号%s，走已留资硬编码路径",
		s.customer.ID, pii.MaskPhone(phoneMatch))

	// 0. OneID合并：手机号匹配到老客户时，迁移所有数据
	mergedTargetID := chatflow.MergeCustomerByPhone(&s.customer, phoneMatch)
	if mergedTargetID > 0 {
		// 合并完成，重新加载老客户数据，会话也跟着迁过去了
		db.RQ(s.c).First(&s.customer, mergedTargetID)
		db.RQ(s.c).Where("customer_id = ? AND status = ?", s.customer.ID, "active").
			Order("updated_at DESC").First(&s.conversation)
		log.Printf("[到店倾向-已留资-OneID] 合并到老客户%d，前端需切换customer_id", mergedTargetID)
	}

	// 1. 标记留资 + 分配顾问
	leadUpdates := map[string]interface{}{}
	if s.customer.Phone == "" {
		leadUpdates["phone"] = phoneMatch
	}
	leadUpdates["journey_stage"] = model.JourneyLeadCaptured
	leadUpdates["assignment_reason"] = "lead_captured"
	// 到店倾向已留资分支也要用轮询选顾问，不能硬编码1
	if s.customer.AssignedUserID == 0 {
		var salesUsers []model.User
		db.RQ(s.c).Where("role = ? AND status = 1", model.RoleSales).Find(&salesUsers)
		if len(salesUsers) > 0 {
			minCount := -1
			var bestUserID uint = salesUsers[0].ID
			for _, u := range salesUsers {
				var count int64
				db.RQ(s.c).Model(&model.Customer{}).Where("assigned_user_id = ? AND status = 1", u.ID).Count(&count)
				if minCount < 0 || int(count) < minCount {
					minCount = int(count)
					bestUserID = u.ID
				}
			}
			leadUpdates["assigned_user_id"] = bestUserID
		} else {
			// 无销售用户时不硬编码 uint(2)（跨租户脏分配），对齐 chat_lead 语义：进人工池
			leadUpdates["assigned_user_id"] = uint(0)
		}
	}
	if len(leadUpdates) > 0 {
		if uerr := db.RQ(s.c).Model(&s.customer).Updates(leadUpdates).Error; uerr != nil {
			log.Printf("[留资-告警][到店倾向] 客户%d 留资字段落库失败(phone/stage未持久化): %v", s.customer.ID, uerr)
		}
		// 同步内存对象（修复Bug1：断言全部改安全形式，杜绝留资链路 panic 中断）
		if v, ok := leadUpdates["phone"]; ok {
			s.customer.Phone, _ = v.(string)
		}
		if v, ok := leadUpdates["journey_stage"]; ok {
			s.customer.JourneyStage, _ = v.(string)
		}
		if v, ok := leadUpdates["assigned_user_id"]; ok {
			if uid, uok := v.(uint); uok {
				s.customer.AssignedUserID = uid
			} else {
				log.Printf("[留资-告警] assigned_user_id 类型异常(%T)，保持原值: %v", v, s.customer.AssignedUserID)
			}
		}
	}
	log.Printf("[到店倾向-已留资线索][留资检测] 客户%d 留资成功: phone=%s, stage=lead_captured, assigned=%d",
		s.customer.ID, pii.MaskPhone(phoneMatch), s.customer.AssignedUserID)

	// P3：到店分支留资事件上行（与 DetectLeadCapture 主路径埋点对齐）
	leadAttrs := map[string]any{"customer_id": s.customer.ID, "path": "store_visit_branch"}
	if s.req.Channel != "" {
		leadAttrs["channel"] = s.req.Channel
	}
	if s.req.Device != "" {
		leadAttrs["device"] = s.req.Device
	}
	if err := mq.Publish(middleware.CtxWithTrace(s.c), mq.TopicUserEvent, s.tenantID,
		fmt.Sprintf("c:%d", s.customer.ID), "lead_captured",
		mq.UserEvent{EventType: "behavior", EventName: "lead_captured", AnchorType: "phone",
			Attributes: leadAttrs,
			OccurredAt: time.Now()}); err != nil {
		log.Printf("[MQ] lead_captured(到店分支) 发布失败: %v", err)
	}

	// 2. 生成线索记录（FollowUp，顾问端可见）
	followUp := model.FollowUp{
		CustomerID:     s.customer.ID,
		ConversationID: s.conversation.ID,
		UserID:         s.customer.AssignedUserID, // 归属顾问
		Type:           "ai_triggered",            // AI触发生成
		Method:         "store",                   // 到店渠道
		Content:        fmt.Sprintf("客户到店意向+已留资，手机号:%s，原始消息:%s", pii.MaskPhone(phoneMatch), pii.MaskPhoneInText(s.req.Content)),
		Result:         "lead_captured", // 已留资线索
	}
	db.RQ(s.c).Create(&followUp)
	log.Printf("[到店倾向-已留资线索] 客户%d 线索已生成(FollowUp ID=%d)，分配顾问%d",
		s.customer.ID, followUp.ID, s.customer.AssignedUserID)

	// 3. 通知顾问（当前简化为日志，后续可接WebSocket/邮件/飞书）
	log.Printf("[通知顾问] 顾问%d 有新的已留资到店线索：客户%d，手机号%s",
		s.customer.AssignedUserID, s.customer.ID, pii.MaskPhone(phoneMatch))

	// 4. 标记待人工接管，但AI先发一条引导式反问
	conversation := &s.conversation
	conversation.PendingHandoff = true
	conversation.GuidedRemainingRounds = 0
	conversation.GuidedDisabled = false
	now := time.Now()
	conversation.HandoffNotifiedAt = &now
	// 到店快速通道同样只写本分支推进的 4 列，勿整行 Save
	db.RQ(s.c).Model(&model.Conversation{}).Where("id = ?", conversation.ID).Updates(map[string]interface{}{
		"pending_handoff":         true,
		"guided_remaining_rounds": 0,
		"guided_disabled":         false,
		"handoff_notified_at":     now,
	})

	// 5. 固定引导式反问（硬编码，不走AI避免延迟）
	firstDelay := service.GetStoreVisitFirstDelay()
	chatflow.CancellableSleep(s.customer.ID, firstDelay)

	leadCapturedReplies := []string{
		"好呀，要不你再详细跟我说说你的用车需求，关注哪些方面，有没有老车要置换，大概什么时候想用车吧",
		"好嘞，你方便详细聊聊你的用车需求吗？关注什么方面比较多？有没有老车考虑置换，大概啥时候想用车呢",
		"好的，你要不跟我说说你的用车场景和需求？关注哪些地方比较多，有没有老车要换，大概打算啥时候用车",
	}
	leadCapturedReply := leadCapturedReplies[rand.Intn(len(leadCapturedReplies))]

	leadMsg := model.Message{
		ConversationID: s.conversation.ID,
		CustomerID:     s.customer.ID,
		SenderType:     "ai",
		Content:        leadCapturedReply,
		MessageType:    "text",
		RouteResult:    "lead_captured_confirmed", // 已留资确认路由标记
		CreatedAt:      time.Now(),
	}
	db.RQ(s.c).Create(&leadMsg)

	RespOK(s.c, "success", gin.H{
		"conversation_id":    s.conversation.ID,
		"ai_reply":           leadCapturedReply,
		"route_result":       "lead_captured_confirmed",
		"merged_customer_id": mergedTargetID, // OneID合并：>0表示前端需切换customer_id
	})
	return true
}

// chatProcessAndReply 拿到合并队列处理权后走完整流程：释放批次闭包兜底 + 硬拦截留资 + 7步推理路由 + 落库返回。
// 等价原 Chat() 处理权段（含 releaseBatch defer）。返回 true 表示已响应客户端。
func (s *chatSessionCtx) chatProcessAndReply() bool {
	batchReleased := false
	releaseBatch := func(text string) {
		if !batchReleased {
			batchReleased = true
			service.DefaultMessageQueueService.SetReply(s.tenantID, s.customer.ID, s.processEpoch, text)
		}
	}
	defer func() {
		if !batchReleased {
			log.Printf("[Chat-告警] 客户%d 处理阶段异常退出，空回复释放批次 epoch=%d", s.customer.ID, s.processEpoch)
			releaseBatch("")
		}
	}()

	// ---- 硬拦截：留资检测（手机号校验+分配顾问） ----
	s.chatLeadCaptureHardIntercept()
	// ---- 调用策略引擎（用合并后的内容推理） ----
	s.chatRunStrategyAndRoute()

	aiReply, aiMsg := s.chatSaveReplyAndUpdate()

	// 12. 唤醒消息队列中等待的其他请求（携带本请求持有的处理代际）
	releaseBatch(aiReply)

	// 13. 返回结果
	RespOK(s.c, "success", schema.ChatResponse{
		ConversationID: s.conversation.ID,
		Message:        aiMsg,
		StrategyInfo:   s.strategyInfo,
		RouteResult:    s.routeResult,
		Mode:           s.conversation.Mode,
		NewTags:        s.newTags,
		PendingHandoff: s.conversation.PendingHandoff,
		// G5：仅 >0（真发生 OneID 合并）才回传目标 ID，其余一律 0
		MergedCustomerID: mergedCustomerIDFor(s.leadCapturedResult),
		VisitorKey:       s.customer.VisitorKey,
	})
	return true
}

// chatLeadCaptureHardIntercept 处理权段的硬拦截留资检测（手机号校验+分配顾问）。
// 副作用：命中时重载客户、置 guided 轮数，并填充 s.leadCapturedResult 供返回的 MergedCustomerID 使用。
func (s *chatSessionCtx) chatLeadCaptureHardIntercept() {
	leadCapturedResult := 0
	if s.customer.JourneyStage != model.JourneyLeadCaptured && s.customer.JourneyStage != model.JourneyArrived &&
		s.customer.JourneyStage != model.JourneyOrdered && s.customer.JourneyStage != model.JourneyDelivered {
		leadCapturedResult = chatflow.DetectLeadCapture(s.mergedContent, &s.customer)
		if leadCapturedResult != 0 {
			log.Printf("[留资检测-硬拦截] 客户 %d 已留资，自动分配顾问", s.customer.ID)
			if leadCapturedResult > 0 {
				log.Printf("[留资检测-OneID] 前端需切换customer_id → %d", leadCapturedResult)
			}
			// 重新加载客户，确保journey_stage等字段同步到内存
			if err := db.RQ(s.c).First(&s.customer, s.customer.ID).Error; err == nil {
				log.Printf("[留资检测-硬拦截] 客户 %d 重新加载成功, journey_stage=%s", s.customer.ID, s.customer.JourneyStage)
			}
			// 硬编码：留资后设置1轮引导式反问
			db.RQ(s.c).Model(&model.Conversation{}).Where("id = ?", s.conversation.ID).Updates(map[string]interface{}{
				"guided_remaining_rounds": 1,
				"guided_disabled":         false,
			})
			s.conversation.GuidedRemainingRounds = 1
			s.conversation.GuidedDisabled = false
		}
	}
	s.leadCapturedResult = leadCapturedResult
}

// chatRunStrategyAndRoute 7步推理 + 路由分流（硬边界已在 chatInferRoute 处理）+ 内容安全闸门。
// 副作用：落库前计算策略输出、按路由结果改写会话 mode、生成 AI 回复（或养鱼/转人工硬编码话术），
// 并经内容安全闸门改写/拦截出站回复。调用后 s.aiReply / s.routeResult / s.strategyOutput / s.tVector 就绪。
func (s *chatSessionCtx) chatRunStrategyAndRoute() {
	s.tVector = s.customer.BuildBaseTVector() // 策略推理输入必须是基准向量，不是可能已叠加过的持久化值
	state := s.conversation.GetState()
	customerTags := s.customer.GetTags()

	strategyInput := strategy.StrategyInput{
		TVector:        s.tVector,
		State:          state,
		CustomerInput:  s.mergedContent,
		CustomerTags:   customerTags,
		CustomerID:     s.customer.ID,
		ConversationID: s.conversation.ID,
		CanPromote:     s.customer.CanPromote(),
		JourneyStage:   s.customer.JourneyStage,
		TenantID:       s.customer.TenantID, // M1租户隔离修复：模板/卖点召回按此过滤
		// 三级包架构：归属顾问的部门继承链（未指派=C端纯租户语境，只见行业+企业层）
		DeptIDs: service.DeptChainForUser(s.conversation.AssignedUserID),
	}

	s.strategyOutput = strategy.DefaultEngine.Infer(strategyInput)

	// 7. 根据路由结果处理
	aiReply := ""
	routeResult := s.strategyOutput.RouteResult

	switch routeResult {
	case strategy.RouteAI:
		if !s.conversation.IsAiReplyEnabled {
			// 5分钟无人回复自动重开AI（仅到店场景生效）
			aiTimedOut := s.conversation.PendingHandoff &&
				s.conversation.LastHumanReplyAt != nil &&
				time.Since(*s.conversation.LastHumanReplyAt) >=
					time.Duration(runtimecfg.DefaultSystemConfigService.GetInt("assigned_lead_ai_timeout", 300))*time.Second
			if aiTimedOut {
				s.conversation.IsAiReplyEnabled = true
				s.conversation.IsHumanLocked = false
				s.conversation.Mode = "ai"
				s.conversation.PendingHandoff = false
				db.RQ(s.c).Model(&s.conversation).Updates(map[string]interface{}{
					"is_ai_reply_enabled": true,
					"is_human_locked":     false,
					"mode":                "ai",
					"pending_handoff":     false,
				})
				log.Printf("[Chat] 会话%d 顾问超时，自动重开AI回复", s.conversation.ID)
				aiReply = flow.DefaultEngine.OrchestrateReply(middleware.CtxWithTrace(s.c), &s.customer, s.conversation.ID, s.mergedContent, &s.strategyOutput, service.DeptChainForUser(s.conversation.AssignedUserID))
			} else {
				aiReply = ""
				routeResult = "human_locked_no_ai"
				log.Printf("[Chat] 会话%d AI回复已关闭(IsAiReplyEnabled=false)，跳过AI", s.conversation.ID)
			}
		} else {
			s.conversation.Mode = "ai"
			aiReply = flow.DefaultEngine.OrchestrateReply(middleware.CtxWithTrace(s.c), &s.customer, s.conversation.ID, s.mergedContent, &s.strategyOutput, service.DeptChainForUser(s.conversation.AssignedUserID))
		}

	case strategy.RoutePendingHuman:
		// AI接不住：软接管（用户侧无感知的硬切）
		aiReply = "你好，我现在有点忙，你要不留个信息咱们到店谈，我顺便帮你查一下你的问题"
		s.conversation.Mode = "human"
		s.conversation.IsHumanLocked = true
		s.conversation.IsAiReplyEnabled = false
		db.RQ(s.c).Model(&s.conversation).Updates(map[string]interface{}{
			"mode":                "human",
			"is_human_locked":     true,
			"is_ai_reply_enabled": false,
		})
		log.Printf("[对话] 会话 %d AI接不住，已关闭AI回复等待顾问，推迟分配顾问，原因: %s", s.conversation.ID, s.strategyOutput.RouteReason)

	case strategy.RoutePrice:
		// 询价路由：客户问价格 → 引导到店试驾后出报价
		s.conversation.Mode = "ai"
		log.Printf("[对话] 会话%d 询价路由触发，引导到店试驾后出报价", s.conversation.ID)
		aiReply = flow.DefaultEngine.OrchestrateReply(middleware.CtxWithTrace(s.c), &s.customer, s.conversation.ID, s.mergedContent, &s.strategyOutput, service.DeptChainForUser(s.conversation.AssignedUserID))

	case strategy.RouteHuman:
		// 直接转人工（硬切，用户有感知）
		aiReply = "你好，我现在有点忙，你要不留个信息咱们到店谈，我顺便帮你查一下你的问题"
		s.conversation.Mode = "human"
		s.conversation.IsHumanLocked = true
		s.conversation.IsAiReplyEnabled = false
		db.RQ(s.c).Model(&s.conversation).Updates(map[string]interface{}{
			"mode":                "human",
			"is_human_locked":     true,
			"is_ai_reply_enabled": false,
		})
		log.Printf("[对话] 会话 %d AI接不住硬切人工，已关闭AI回复等待顾问，推迟分配顾问，原因: %s", s.conversation.ID, s.strategyOutput.RouteReason)

	case strategy.RouteFish:
		// 养鱼模式
		s.conversation.Mode = "fish"
		aiReply = "好的，你先考虑考虑，有任何问题随时找我~ 我会持续关注你的需求，有好消息也会及时通知你。"
		s.conversation.Status = "active"
	}

	// 7.5 内容安全闸门（C1，2026-09-12）：AI 出站回复统一过滤
	//   MASK→改写；BLOCK(enforce)→丢弃AI话、发无感知退场语、关AI回复等顾问（复用 RouteHuman 语义）
	//   shadow 模式仅计数不改写，供上线首周演练误杀率
	if aiReply != "" {
		if action, out := ContentsafetyGate(aiReply, s.conversation.ID); action != GatePass {
			aiReply = s.applyContentsafetyGate(aiReply, action, out)
		}
	}

	s.aiReply = aiReply
	s.routeResult = routeResult
}

// applyContentsafetyGate 内容安全闸门对出站回复的最终处置：MASK→改写；BLOCK→退场转人工。
// 纯函数式（仅改写 s.aiReply 与会话 mode），便于单测直接驱动 GateBlock/GateRewrite 分支。
func (s *chatSessionCtx) applyContentsafetyGate(aiReply string, action GateAction, out string) string {
	switch action {
	case GateRewrite:
		return out
	case GateBlock:
		s.conversation.Mode = "human"
		s.conversation.IsHumanLocked = true
		s.conversation.IsAiReplyEnabled = false
		db.RQ(s.c).Model(&s.conversation).Updates(map[string]interface{}{
			"mode":                "human",
			"is_human_locked":     true,
			"is_ai_reply_enabled": false,
		})
		log.Printf("[对话] 会话%d 内容安全拦截，已转人工等待顾问", s.conversation.ID)
		return SafetyHandoffReply()
	}
	return aiReply
}

// chatSaveReplyAndUpdate 落库 AI 回复、更新会话状态(字段级)与客户画像(意向分反哺)、写归因、
// 构造策略信息并施加"模拟真人延迟"后返回最终 aiReply 与落库消息。等价原 Chat() 步骤8-13。
func (s *chatSessionCtx) chatSaveReplyAndUpdate() (string, model.Message) {
	state := s.conversation.GetState()

	// 8. 保存AI回复消息
	var aiMsg model.Message
	if s.aiReply != "" {
		aiMsg = model.Message{
			ConversationID: s.conversation.ID,
			CustomerID:     s.customer.ID,
			SenderType:     "ai",
			Content:        s.aiReply,
			MessageType:    "text",
			AnchorType:     s.strategyOutput.FinalAnchor,
			TemplateID:     s.strategyOutput.TemplateID,
			RouteResult:    s.routeResult,
			IntentScore:    s.tVector[0],
			Emotion:        state.Emotion,
			CreatedAt:      time.Now(),
		}
		db.RQ(s.c).Create(&aiMsg)
		publishConversationMsg(s.tenantID, s.customer.ID, s.routeResult, state.Emotion)
		// P1-1 实时推送：AI 新回复通知客户端与顾问端
		notifyWSWithContent(s.tenantID, s.customer.ID, s.conversation.ID, "ai",
			aiMsg.ID, s.aiReply, "AI顾问", time.Now().Format("2006-01-02T15:04:05Z"))
	}

	// 9. 更新会话状态
	updatedState := chatflow.UpdateConversationState(&s.conversation, &s.strategyOutput, &s.customer, s.mergedContent)
	s.conversation.SaveState(updatedState)
	s.conversation.LastTid = s.strategyOutput.TemplateID
	s.conversation.LastAnchorType = s.strategyOutput.FinalAnchor
	s.conversation.Emotion = strategy.DetectEmotion(s.mergedContent)

	// 只写本请求负责推进的列（不含 mode/customer_id/guided_*）
	db.RQ(s.c).Model(&model.Conversation{}).Where("id = ?", s.conversation.ID).Updates(map[string]interface{}{
		"attempts":           s.conversation.Attempts,
		"hook_count":         s.conversation.HookCount,
		"last_tid":           s.conversation.LastTid,
		"last_anchor_type":   s.conversation.LastAnchorType,
		"emotion":            s.conversation.Emotion,
		"high_intent_rounds": s.conversation.HighIntentRounds,
		"silent_duration":    s.conversation.SilentDuration,
		"current_stage":      s.conversation.CurrentStage,
		"state_json":         s.conversation.StateJSON,
		"last_message_at":    s.conversation.LastMessageAt,
	})

	// 10. 更新客户画像（意向分反哺）
	newIntent := s.tVector[0] + s.strategyOutput.IntentDelta
	if newIntent < 0 {
		newIntent = 0
	}
	if newIntent > 1 {
		newIntent = 1
	}
	s.customer.IntentScore = newIntent

	// 更新客户T向量
	newTVector := s.customer.GetTVector()
	newTVector[0] = newIntent // 更新意向分
	s.customer.SaveTVector(newTVector)
	db.RQ(s.c).Model(&model.Customer{}).Where("id = ?", s.customer.ID).Updates(map[string]interface{}{
		"intent_score": s.customer.IntentScore,
		"t_vector":     s.customer.TVectorJSON,
	})

	// D9：AI 回复落包/模板/意向变化快照，供包效果归因。
	if aiMsg.ID > 0 {
		_ = attribution.RecordReply(attribution.RecordReplyInput{
			TenantID:       s.tenantID,
			MessageID:      aiMsg.ID,
			ConversationID: s.conversation.ID,
			CustomerID:     s.customer.ID,
			TemplateID:     s.strategyOutput.TemplateID,
			AnchorType:     s.strategyOutput.FinalAnchor,
			RouteResult:    s.routeResult,
			IntentBefore:   s.tVector[0],
			IntentAfter:    newIntent,
		})
		if s.routeResult == strategy.RoutePendingHuman {
			_ = attribution.MarkPendingHuman(s.tenantID, s.conversation.ID, s.customer.ID)
		}
	}

	// 11. 构造策略信息（供B端查看）
	strategyInfo := schema.StrategyInfo{
		AnchorType:       s.strategyOutput.FinalAnchor,
		AnchorTypeName:   strategy.GetAnchorName(s.strategyOutput.FinalAnchor),
		TemplateID:       s.strategyOutput.TemplateID,
		RouteResult:      s.routeResult,
		UrgencyLevel:     s.strategyOutput.UrgencyLevel,
		ExchangeFlag:     s.strategyOutput.ExchangeFlag,
		IntentScore:      newIntent,
		TrustLevel:       s.tVector[6],
		HookRate:         updatedState.HookRate,
		CurrentStage:     updatedState.CurrentStage,
		HighIntentRounds: updatedState.HighIntentRounds,
		Emotion:          s.conversation.Emotion,
		SoftDowngrade:    s.strategyOutput.SoftDowngrade,
		OriginalAnchor:   s.strategyOutput.OriginalAnchor,
	}
	s.strategyInfo = strategyInfo

	// 11.5 模拟真人回复延迟
	isStoreVisit := service.IsStoreVisitIntentForTenant(s.tenantID, s.mergedContent) && !chatflow.IsLeadCaptured(&s.customer)
	log.Printf("[Chat] 客户%d 到店倾向检测: %v, 合并内容: %q", s.customer.ID, isStoreVisit, pii.MaskPhoneInText(s.mergedContent))
	humanlikeDelay := service.CalcHumanlikeDelay(s.tenantID, s.aiReply, s.mergeWaitDuration, s.mergeCount, isStoreVisit)

	// 胡搅蛮缠：总非车话题>10且最近未恢复→回复速度降到3分钟一次
	hjTotalOffTopic := chatflow.CountTotalOffTopic(s.customer.ID)
	hjOnTopic := chatflow.CountConsecutiveOnTopic(s.customer.ID)
	if hjTotalOffTopic > 10 && hjOnTopic < 3 {
		minDelay := 180 * time.Second
		if humanlikeDelay < minDelay {
			log.Printf("[胡搅蛮缠-降速] 客户%d 非车%d>10, 延迟从%.1fs提升到%.1fs",
				s.customer.ID, hjTotalOffTopic, humanlikeDelay.Seconds(), minDelay.Seconds())
			humanlikeDelay = minDelay
		}
	}

	// 总回复时长2分钟硬顶兜底
	maxTotalDelay := 120 * time.Second
	elapsed := time.Since(s.requestStart)
	remainingBudget := maxTotalDelay - elapsed
	if humanlikeDelay > remainingBudget {
		log.Printf("[Chat] 客户%d 总延迟硬顶触发: 已用%.1fs + 模拟延迟%.1fs > 2分钟, 截断到%.1fs",
			s.customer.ID, elapsed.Seconds(), humanlikeDelay.Seconds(), remainingBudget.Seconds())
		humanlikeDelay = remainingBudget
	}
	if humanlikeDelay < 0 {
		humanlikeDelay = 0
	}

	log.Printf("[Chat] 客户%d 模拟延迟: %.1fs, 已用: %.1fs, 总计: %.1fs, 开始sleep...", s.customer.ID, humanlikeDelay.Seconds(), elapsed.Seconds(), (elapsed + humanlikeDelay).Seconds())
	// instant模式跳过CancellableSleep，秒回无延迟
	replyDelayMode := runtimecfg.DefaultSystemConfigService.GetString("reply_delay_mode", "normal")
	if replyDelayMode == "instant" {
		log.Printf("[Chat] 客户%d instant模式，跳过延迟直接回复", s.customer.ID)
	} else {
		chatflow.CancellableSleep(s.customer.ID, humanlikeDelay)
	}
	log.Printf("[Chat] 客户%d 延迟结束，返回回复", s.customer.ID)

	return s.aiReply, aiMsg
}

// mergedCustomerIDFor G5：把 DetectLeadCapture 的 int 结果安全转成回传前端的合并目标 ID——
// 仅正数（真发生 OneID 合并）才回传，0/负值（含 -1 "已留资无需合并"）一律 0，杜绝 uint 负数溢出。
func mergedCustomerIDFor(v int) uint {
	if v > 0 {
		return uint(v)
	}
	return 0
}

// HumanReply 人工回复接口
// 销售在B端发送消息
