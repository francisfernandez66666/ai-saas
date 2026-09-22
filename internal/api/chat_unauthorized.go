// 对话核心 API（C 端访客入口）：客户端 Client.tsx 实际调用的链路，经
// POST /api/v1/chat/test 暴露（路由名含 test 是历史遗留，**不是测试桩**）。
//
// C1 双链路说明（PLAN_FIX_2026-09-21）：
//
//	本文件 = C 端访客主链路（免登录 + visitor_key 自证 + Turnstile + IP 限流，
//	  已含会话竞态保护/四层分流/延迟清零/留资检测/OneID 合并，能力是完整的）；
//	chat_main.go 的 POST /api/v1/chat = 登录态入口（挂在 v1.Use(JWTAuth) 之后，
//	  **匿名访客会被 401**，前端零消费者）。
//
// 两者功能重叠而鉴权语义不同，收敛需架构决策（见 PLAN_FIX C1）；在此之前不要
// 凭本文件旧注释"测试入口"误判它是可下线/可忽略的旁路。
package api

import "ai-scrm/internal/pii"

// 对话核心API：C端客户与B端销售共用的交互入口，链路为 客户发消息→策略中心7步推理→AI生成回复。
// 含会话竞态保护、四层分流(硬边界/到店快速通道/简单消息/合并队列)、延迟清零、留资检测与OneID合并。

import (
	"ai-scrm/config"
	"ai-scrm/internal/ai"
	"ai-scrm/internal/attribution"
	"ai-scrm/internal/chatflow"
	"ai-scrm/internal/db"
	"ai-scrm/internal/engine/flow"
	"ai-scrm/internal/engine/strategy"
	"ai-scrm/internal/logx"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
	"ai-scrm/internal/mq"
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
//  3. Chat() 和 ChatUnauthorized() 统一使用此机制，行为一致
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

// chatUnauthorizedCtx ChatUnauthorized 请求生命周期内的共享可变上下文。
// 与 chatSessionCtx 同理：把 ChatUnauthorized() 这个 god function 按阶段"剪切-粘贴"成方法，
// 仅显式收口闭包变量为字段，不改变任何条件顺序 / SQL / Redis 键 / trace 语义 / 文案。
type chatUnauthorizedCtx struct {
	c            *gin.Context
	tenantID     uint
	requestStart time.Time
	req          struct {
		CustomerID uint   `json:"customer_id"`
		Content    string `json:"content" binding:"required,max=4000"`
	}
	customer          model.Customer
	conversation      model.Conversation
	testCustomerMsgID uint
	isNewConversation bool
	mergedContent     string
	mergeWaitDuration time.Duration
	mergeCount        int
	processEpoch      uint64
	aiReply           string
	strategyOutput    strategy.StrategyOutput
	tVector           [32]float64
	newTags           []string
	testLeadResult    int
}

// ChatUnauthorized POST /api/v1/chat/test 免登录对话入口（C 端访客主链路）。
//
// 行为零变化说明：仅把原函数体按既有注释段"剪切-粘贴"为 chatUnauthorizedCtx 方法，
// 各方法返回 (done bool) 表示已响应客户端。处理权段的 SetReply 早退语义与原实现逐字一致。
func ChatUnauthorized(c *gin.Context) {
	extendWriteDeadlineForAI(c) // D4：同步处理者分支最坏 25+15+110s，延长本连接写截止（write_deadline.go）
	requestStart := time.Now()

	tenantID := middleware.EffectiveTenantID(c)

	s := &chatUnauthorizedCtx{
		c:            c,
		tenantID:     tenantID,
		requestStart: requestStart,
	}

	if s.chatUnauthorizedResolve() {
		return
	}
	if s.chatUnauthorizedHardBoundary() {
		return
	}
	if s.chatUnauthorizedStoreVisit() {
		return
	}
	if s.chatUnauthorizedSimilarSuppress() {
		return
	}
	if s.chatUnauthorizedEnqueue() {
		return
	}
	s.chatUnauthorizedEnsureProcessing()
	if s.chatUnauthorizedHumanTakeover() {
		return
	}
	s.chatUnauthorizedLeadIntercept()
	s.chatUnauthorizedGenerateReply()
	s.chatUnauthorizedSaveAndReturn()
}

// chatUnauthorizedResolve 身份校验：绑定入参、解析客户ID(必填)、查客户、访客密钥防线(无条件)。
// 副作用：绑定失败/缺 customer_id(400)/客户不存在(404)/访客密钥不匹配(403)时响应并 return true；否则填充 s.customer / s.req。
func (s *chatUnauthorizedCtx) chatUnauthorizedResolve() bool {
	var req struct {
		CustomerID uint   `json:"customer_id"`
		Content    string `json:"content" binding:"required,max=4000"`
	}
	if err := s.c.ShouldBindJSON(&req); err != nil {
		RespErrBind(s.c, err)
		return true
	}
	s.req = req

	// S1 收口（2026-09-22 批二，Q2 决策=保留别名但强制自证身份）：
	// 原"缺省即 1 号客户"是 README 时代的测试便利残留——匿名请求不带 customer_id
	// 就会把消息写进 1 号客户会话。现要求显式传 customer_id（两步：先 /chat/guest 领身份）。
	customerID := req.CustomerID
	if customerID == 0 {
		RespErr(s.c, http.StatusBadRequest, 400, "缺少 customer_id：请先调 /chat/guest 建立访客身份，取回 customer_id 与 visitor_key 后再发消息")
		return true
	}

	// 查询客户
	var customer model.Customer
	result := db.RQ(s.c).First(&customer, customerID)
	if result.Error != nil {
		RespErr(s.c, http.StatusNotFound, 404, "客户不存在")
		return true
	}

	// P0-7 修复(2026-09-09)：C 端正式对话写入口的身份防线。
	// S1 收口（2026-09-22 批二）：删掉旧写的 `customer.VisitorKey != ""` 前置短路——
	// 该短路令"空密钥客户"直接放行任意匿名写入，而库里 303 个客户中 302 个 visitor_key 为空，
	// 实测仅带 X-Tenant-ID 即可向他人会话写消息并触发出站 AI（读侧 /chat/history 同客户却 403，
	// 即"写读不对称"）。CheckVisitorKey 语义（middleware/ip_limit.go:153-163）已够用：
	// 登录态 user_id>0 短路放行；匿名必须带 visitor_key 且与目标逐字节一致；expected 空串一律拒。
	// 存量空密钥客户由迁移 017_visitor_key_backfill 补签，故收紧顺序为"先迁移、后收口"。
	if !middleware.CheckVisitorKey(s.c, customer.VisitorKey) {
		RespErr(s.c, http.StatusForbidden, 403, "访客身份校验失败，请重新进入对话")
		return true
	}

	s.customer = customer
	return false
}

// chatUnauthorizedHardBoundary 第一层：硬边界拦截（0延迟，不走AI，不进队列）。
// 副作用：命中时 EnsureActiveConversation + 落库客户/AI消息 + 打标并响应 return true；否则 return false。
func (s *chatUnauthorizedCtx) chatUnauthorizedHardBoundary() bool {
	if service.IsOffTopicForTenant(s.tenantID, s.req.Content) {
		reply := service.GetOffTopicReplyForTenant(s.tenantID, s.req.Content)
		log.Printf("[硬边界-测试接口] 客户%d 拦截无关话题(入队前): %q → %q", s.customer.ID, pii.MaskPhoneInText(s.req.Content), pii.MaskPhoneInText(reply))
		// 查找或创建活跃会话（G1 收口 2026-09-16C：统一 EnsureActiveConversation）
		conv, _, convErr := chatflow.EnsureActiveConversation(
			s.tenantID, s.customer.ID, s.customer.AssignedUserID,
			func(cv *model.Conversation) { cv.Channel = "web" },
		)
		if convErr != nil {
			log.Printf("[测试接口-告警] 客户%d 会话保障失败: %v", s.customer.ID, convErr)
			RespErr(s.c, http.StatusInternalServerError, 500, "会话初始化失败，请重试")
			return true
		}
		s.conversation = conv
		// 保存客户消息+AI拦截回复
		customerMsg := model.Message{
			ConversationID: conv.ID,
			CustomerID:     s.customer.ID,
			SenderType:     "customer",
			Content:        s.req.Content,
			MessageType:    "text",
			CreatedAt:      time.Now(),
		}
		db.RQ(s.c).Create(&customerMsg)
		offTopicMsg := model.Message{
			ConversationID: conv.ID,
			CustomerID:     s.customer.ID,
			SenderType:     "ai",
			Content:        reply,
			MessageType:    "text",
			RouteResult:    "offtopic_hardbound",
			CreatedAt:      time.Now(),
		}
		db.RQ(s.c).Create(&offTopicMsg)
		// P1-1 实时推送：客户消息 + AI拦截回复
		notifyWSWithContent(s.tenantID, s.customer.ID, conv.ID, "customer", customerMsg.ID, s.req.Content, s.customer.Name, customerMsg.CreatedAt.Format("2006-01-02T15:04:05Z"))
		notifyWSWithContent(s.tenantID, s.customer.ID, conv.ID, "ai", offTopicMsg.ID, reply, "AI顾问", offTopicMsg.CreatedAt.Format("2006-01-02T15:04:05Z"))
		// 硬边界拦截路径也调用AutoTagFromText
		autoTags, tagErr := service.DefaultTagService.AutoTagFromText(s.customer.TenantID, s.customer.ID, s.req.Content)
		if tagErr == nil && len(autoTags) > 0 {
			log.Printf("[测试接口-硬边界] 客户%d自动打标: %v", s.customer.ID, autoTags)
		}
		// 旁路直答，不污染在途批次
		RespOK(s.c, "success", gin.H{
			"conversation_id":    conv.ID,
			"ai_reply":           reply,
			"route_result":       "offtopic_hardbound",
			"customer_msg_id":    customerMsg.ID,
			"assistant_messages": []model.Message{offTopicMsg},
		})
		return true
	}
	return false
}

// chatUnauthorizedStoreVisit 第二层：到店倾向快速通道。
// 已留资客户返回 false 跳过本层；含手机号走分支B(留资)返回 true；未留资走分支C(两段式)返回 true。
func (s *chatUnauthorizedCtx) chatUnauthorizedStoreVisit() bool {
	if service.IsStoreVisitIntentForTenant(s.tenantID, s.req.Content) {
		// 前置拦截：已留资客户不再走到店快速通道
		if s.customer.JourneyStage == model.JourneyLeadCaptured ||
			s.customer.JourneyStage == model.JourneyArrived ||
			s.customer.JourneyStage == model.JourneyOrdered ||
			s.customer.JourneyStage == model.JourneyDelivered {
			log.Printf("[到店倾向-测试接口] 客户%d 已是%s阶段，跳过到店快速通道",
				s.customer.ID, s.customer.JourneyStage)
			return false
		}

		// 留资前置检测：当前消息是否包含手机号
		phoneMatchTest := chatflow.PhoneRegex.FindString(s.req.Content)

		// 查找或创建活跃会话（各分支共用；G1 收口 2026-09-16C）
		conv, _, convErr := chatflow.EnsureActiveConversation(
			s.tenantID, s.customer.ID, s.customer.AssignedUserID,
			func(cv *model.Conversation) { cv.Channel = "web" },
		)
		if convErr != nil {
			log.Printf("[测试接口-告警] 客户%d 会话保障失败: %v", s.customer.ID, convErr)
			RespErr(s.c, http.StatusInternalServerError, 500, "会话初始化失败，请重试")
			return true
		}
		s.conversation = conv

		// 保存客户消息（各分支共用）
		customerMsg := model.Message{
			ConversationID: conv.ID,
			CustomerID:     s.customer.ID,
			SenderType:     "customer",
			Content:        s.req.Content,
			MessageType:    "text",
			CreatedAt:      time.Now(),
		}
		db.RQ(s.c).Create(&customerMsg)

		// 到店倾向快速通道之前完全没打标 → 每条客户消息落库后立刻打标
		if autoTags, tagErr := service.DefaultTagService.AutoTagFromText(s.customer.TenantID, s.customer.ID, s.req.Content); tagErr == nil && len(autoTags) > 0 {
			log.Printf("[到店倾向-测试接口] 客户%d自动打标: %v", s.customer.ID, autoTags)
			var updatedCustomerForTag model.Customer
			if err := db.RQ(s.c).First(&updatedCustomerForTag, s.customer.ID).Error; err == nil {
				_ = service.DefaultTagService.ApplyTagWeightsToTVector(s.customer.TenantID, &updatedCustomerForTag, updatedCustomerForTag.BuildBaseTVector())
				db.RQ(s.c).Model(&updatedCustomerForTag).Update("t_vector", updatedCustomerForTag.TVectorJSON)
			}
		}

		if phoneMatchTest != "" {
			// ====== 分支B：已留资线索（硬编码） ======
			return s.chatUnauthorizedLeadCapture(phoneMatchTest, customerMsg.ID)
		}

		// ====== 分支C：未留资线索（关闭引导+两段式AI快速回复，推迟分配顾问） ======
		firstReply := service.GetStoreVisitFirstReply(s.tenantID, s.req.Content)
		firstDelay := service.GetStoreVisitFirstDelay()

		log.Printf("[到店倾向-未留资线索-测试接口] 客户%d 关闭引导+两段式回复, 推迟分配顾问", s.customer.ID)

		// 关闭引导式反问
		conv.GuidedDisabled = true
		db.RQ(s.c).Model(&conv).Update("guided_disabled", true)

		// 第一段AI快速回复（接住意向）
		chatflow.CancellableSleep(s.customer.ID, firstDelay)

		storeVisitMsg := model.Message{
			ConversationID: conv.ID,
			CustomerID:     s.customer.ID,
			SenderType:     "ai",
			Content:        firstReply,
			MessageType:    "text",
			RouteResult:    "store_visit_fast",
			CreatedAt:      time.Now(),
		}
		db.RQ(s.c).Create(&storeVisitMsg)
		// P1-1 实时推送：客户消息 + 第一段快速回复
		notifyWSWithContent(s.tenantID, s.customer.ID, conv.ID, "customer", customerMsg.ID, s.req.Content, s.customer.Name, customerMsg.CreatedAt.Format("2006-01-02T15:04:05Z"))
		notifyWSWithContent(s.tenantID, s.customer.ID, conv.ID, "ai", storeVisitMsg.ID, firstReply, "AI顾问", storeVisitMsg.CreatedAt.Format("2006-01-02T15:04:05Z"))

		// 第二段追问（异步，25-45秒后发出，收集预约信息）
		secondReply := service.GetStoreVisitSecondReply(s.tenantID, s.req.Content)
		go func(cid, convID uint, content string, tid uint) {
			sd := service.GetStoreVisitSecondDelay()
			chatflow.CancellableSleep(cid, sd)
			secondMsg := model.Message{
				TenantID:       tid,
				ConversationID: convID,
				CustomerID:     cid,
				SenderType:     "ai",
				Content:        content,
				MessageType:    "text",
				RouteResult:    "store_visit_fast",
				CreatedAt:      time.Now(),
			}
			if err := db.DB.WithContext(db.WithTenant(context.Background(), tid)).Create(&secondMsg).Error; err != nil {
				log.Printf("[到店倾向-告警][测试接口] 客户%d 第二段追问落库失败: %v", cid, err)
				return
			}
			log.Printf("[到店倾向-未留资线索-测试接口] 客户%d 第二段追问已发送", cid)
		}(s.customer.ID, conv.ID, secondReply, s.tenantID)

		RespOK(s.c, "success", gin.H{
			"conversation_id":    conv.ID,
			"ai_reply":           firstReply,
			"route_result":       "store_visit_fast",
			"customer_msg_id":    customerMsg.ID,
			"assistant_messages": []model.Message{storeVisitMsg},
			"follow_up":          gin.H{"delay_seconds": int(service.GetStoreVisitSecondDelay().Seconds())},
		})
		return true
	}
	return false
}

// chatUnauthorizedLeadCapture 到店倾向快速通道「分支B：已留资线索」。
// OneID合并 + 标记留资 + 分配顾问 + pending_handoff + 硬编码确认回复。返回 true 表示已响应。
func (s *chatUnauthorizedCtx) chatUnauthorizedLeadCapture(phoneMatchTest string, customerMsgID uint) bool {
	log.Printf("[到店倾向-已留资线索-测试接口] 客户%d 消息含手机号%s，走已留资硬编码路径",
		s.customer.ID, pii.MaskPhone(phoneMatchTest))

	// 0. OneID合并：手机号匹配到老客户时，迁移所有数据
	mergedTargetIDTest := chatflow.MergeCustomerByPhone(&s.customer, phoneMatchTest)
	if mergedTargetIDTest > 0 {
		var reloadedCust model.Customer
		db.RQ(s.c).Where("id = ?", mergedTargetIDTest).First(&reloadedCust)
		s.customer = reloadedCust
		db.RQ(s.c).Where("customer_id = ? AND status = ?", s.customer.ID, "active").
			Order("updated_at DESC").First(&s.conversation)
		log.Printf("[到店倾向-已留资-测试接口-OneID] 合并到老客户%d，前端需切换customer_id", mergedTargetIDTest)
	}

	// 1. 标记留资 + 分配顾问
	leadUpdates := map[string]interface{}{}
	if s.customer.Phone == "" {
		leadUpdates["phone"] = phoneMatchTest
	}
	leadUpdates["journey_stage"] = model.JourneyLeadCaptured
	leadUpdates["assignment_reason"] = "lead_captured"
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
			leadUpdates["assigned_user_id"] = uint(0)
		}
	}
	if len(leadUpdates) > 0 {
		if uerr := db.RQ(s.c).Model(&s.customer).Updates(leadUpdates).Error; uerr != nil {
			log.Printf("[留资-告警][测试接口] 客户%d 留资字段落库失败(phone/stage未持久化): %v", s.customer.ID, uerr)
		}
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
				log.Printf("[留资-告警][测试接口] assigned_user_id 类型异常(%T)，保持原值: %v", v, s.customer.AssignedUserID)
			}
		}
	}
	log.Printf("[到店倾向-已留资线索-测试接口] 客户%d 留资成功: phone=%s, stage=lead_captured, assigned=%d",
		s.customer.ID, pii.MaskPhone(phoneMatchTest), s.customer.AssignedUserID)
	// P3：到店分支留资事件上行（ChatUnauthorized 路径）
	if err := mq.Publish(middleware.CtxWithTrace(s.c), mq.TopicUserEvent, s.tenantID,
		fmt.Sprintf("c:%d", s.customer.ID), "lead_captured",
		mq.UserEvent{EventType: "behavior", EventName: "lead_captured", AnchorType: "phone",
			Attributes: map[string]any{"customer_id": s.customer.ID, "path": "store_visit_branch_test"},
			OccurredAt: time.Now()}); err != nil {
		log.Printf("[MQ] lead_captured(到店分支测试) 发布失败: %v", err)
	}

	// 2. 生成线索记录（给顾问看）
	followUp := model.FollowUp{
		CustomerID:     s.customer.ID,
		ConversationID: s.conversation.ID,
		UserID:         s.customer.AssignedUserID,
		Type:           "ai_triggered",
		Method:         "store",
		Content:        fmt.Sprintf("客户到店意向+已留资，手机号:%s，原始消息:%s", pii.MaskPhone(phoneMatchTest), pii.MaskPhoneInText(s.req.Content)),
		Result:         "lead_captured",
	}
	db.RQ(s.c).Create(&followUp)
	log.Printf("[到店倾向-已留资线索-测试接口] 客户%d 线索已生成(FollowUp ID=%d)，分配顾问%d",
		s.customer.ID, followUp.ID, s.customer.AssignedUserID)

	// 3. 通知顾问
	log.Printf("[通知顾问-测试接口] 顾问%d 有新的已留资到店线索：客户%d，手机号%s",
		s.customer.AssignedUserID, s.customer.ID, pii.MaskPhone(phoneMatchTest))

	// 4. 标记待人工接管，留1轮引导式反问
	conv := &s.conversation
	conv.PendingHandoff = true
	conv.GuidedRemainingRounds = 1
	conv.GuidedDisabled = false
	now := time.Now()
	conv.HandoffNotifiedAt = &now
	conv.LastMessageAt = &now
	db.RQ(s.c).Model(&conv).Updates(map[string]interface{}{
		"pending_handoff":         true,
		"guided_remaining_rounds": 1,
		"guided_disabled":         false,
		"handoff_notified_at":     &now,
		"last_message_at":         &now,
	})

	// 5. 丝滑确认回复（预定义模板随机选，不走AI）
	firstDelay := service.GetStoreVisitFirstDelay()
	chatflow.CancellableSleep(s.customer.ID, firstDelay)

	leadCapturedReplies := []string{
		"收到！我先帮您约上时间，约好了跟您说~",
		"好嘞，我这就安排，弄好了通知您~",
		"没问题！我这边先帮您约，确认好了跟您说一声~",
		"收到，我先帮您把试驾约上，安排好了告诉您~",
		"好的！我先帮您把时间约好，确认了跟您说~",
	}
	leadCapturedReply := leadCapturedReplies[rand.Intn(len(leadCapturedReplies))]

	leadMsg := model.Message{
		ConversationID: conv.ID,
		CustomerID:     s.customer.ID,
		SenderType:     "ai",
		Content:        leadCapturedReply,
		MessageType:    "text",
		RouteResult:    "lead_captured_confirmed",
		CreatedAt:      time.Now(),
	}
	db.RQ(s.c).Create(&leadMsg)
	// P1-1 实时推送：客户消息 + 留资确认回复
	notifyWSWithContent(s.tenantID, s.customer.ID, conv.ID, "customer", customerMsgID, s.req.Content, s.customer.Name, time.Now().Format("2006-01-02T15:04:05Z"))
	notifyWSWithContent(s.tenantID, s.customer.ID, conv.ID, "ai", leadMsg.ID, leadCapturedReply, "AI顾问", leadMsg.CreatedAt.Format("2006-01-02T15:04:05Z"))

	RespOK(s.c, "success", gin.H{
		"conversation_id":    conv.ID,
		"ai_reply":           leadCapturedReply,
		"route_result":       "lead_captured_confirmed",
		"merged_customer_id": mergedTargetIDTest,
		"customer_msg_id":    customerMsgID,
		"assistant_messages": []model.Message{leadMsg},
	})
	return true
}

// chatUnauthorizedSimilarSuppress A2 补齐(2026-09-22 批四)：免登录 C 端链的相似消息抑制。
//
// 此前这段"客户连发近似句只答一次"的判定只写在正式链（chat_main），本链完全没有：
// 同一客户在 2min 内问两句近似的话，正式链合并、本链各答一遍，既吵又让归因样本不可比。
// 判定本体复用 chatflow.FindSimilarInflightMessage（与正式链同一函数），并沿用 P2-1 纪律：
// **只在确有在途批次会回答时**才抑制，绝不再出现历史相似句静默丢答。
//
// 顺序说明：本链是"先入队后建会话"（与正式链相反），所以这里只做**只读**定位活跃会话，
// 不调 EnsureActiveConversation——否则新会话被本分支抢先建出，后面 EnsureProcessing 的
// isNewConversation 变 false，默认流程 StartFlow 就再也不会执行（行为回归）。
// 定位不到活跃会话即客户首条消息，本就不可能存在相似历史，直接放行。
func (s *chatUnauthorizedCtx) chatUnauthorizedSimilarSuppress() bool {
	if service.DefaultMessageQueueService == nil ||
		!service.DefaultMessageQueueService.HasInflightBatch(s.tenantID, s.customer.ID) {
		return false
	}
	hit, ok := chatflow.FindSimilarInflightMessage(db.RQ(s.c), s.tenantID, s.customer.ID, s.req.Content, true, 0)
	if !ok {
		return false
	}
	// 留痕消息要挂会话：只读定位该客户当前活跃会话（上面已确认有在途批次，正常都存在）
	var conv model.Conversation
	if ferr := db.RQ(s.c).Where("tenant_id = ? AND customer_id = ? AND status = ?",
		s.tenantID, s.customer.ID, "active").Order("id DESC").First(&conv).Error; ferr != nil {
		// D5(2026-09-23 批六)：定位不到活跃会话时**不抑制也不写留痕**。
		// 旧写法把 First 的 error 丢掉，conv 是零值 → 消息以 conversation_id=0 落库，
		// 既污染"客户消息数"口径（D2 贡献度看板虚增的那类孤儿行），又留下一条永不关联会话的记录。
		// 本链"先入队后建会话"，此处刻意不调 EnsureActiveConversation（会抢建会话致默认流程不再启动），
		// 故唯一正确动作是放行：让消息走正常链路，由后面的会话保障落正确的 conversation_id。
		log.Printf("[相似消息合并] 客户%d 活跃会话定位失败（%v），本次不抑制：留痕消息无处可挂，写孤儿行比多答一句更糟",
			s.customer.ID, ferr)
		return false
	}

	log.Printf("[相似消息合并] 客户%d 当前:%q 与历史:%q 重叠度%.0f%%, 合并为一次回答",
		s.customer.ID, logx.Safe(s.req.Content, 40), logx.Safe(hit.PastContent, 40), hit.OverlapRate*100)

	suppressedMsg := model.Message{
		ConversationID: conv.ID,
		CustomerID:     s.customer.ID,
		SenderType:     "customer",
		Content:        s.req.Content,
		MessageType:    "text",
		RouteResult:    "merged_suppressed",
		Emotion:        strategy.DetectEmotion(s.req.Content),
		CreatedAt:      time.Now(),
	}
	db.RQ(s.c).Create(&suppressedMsg)
	s.c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data: schema.ChatResponse{
			ConversationID:    conv.ID,
			Merged:            true,
			MergedNote:        "相似消息已合并处理",
			CustomerMsgID:     suppressedMsg.ID,
			AssistantMessages: []model.Message{},
		},
	})
	return true
}

// chatUnauthorizedEnqueue 第三层+第四层：先存客户消息到DB，再 EnqueueAndWait；
// 简单消息/合并等待命中时直接响应 return true；否则（拿到处理权）填充合并产物后 return false。
func (s *chatUnauthorizedCtx) chatUnauthorizedEnqueue() bool {
	// 先存客户消息到DB，再EnqueueAndWait（与原逻辑一致）
	testCustomerMsg := model.Message{
		ConversationID: 0, // 暂填0，后面拿到conversation后更新
		CustomerID:     s.customer.ID,
		SenderType:     "customer",
		Content:        s.req.Content,
		MessageType:    "text",
		Emotion:        strategy.DetectEmotion(s.req.Content),
		CreatedAt:      time.Now(),
	}
	db.RQ(s.c).Create(&testCustomerMsg)
	s.testCustomerMsgID = testCustomerMsg.ID

	logx.WithTrace(middleware.CtxWithTrace(s.c)).Info("chat_test 入站", "tenant_id", s.tenantID, "customer_id", s.customer.ID)
	mergedContent, shouldProcess, _, mergeWaitDuration, isSimple, mergeCount, processEpoch := service.DefaultMessageQueueService.EnqueueAndWait(s.tenantID, s.customer.ID, s.req.Content, middleware.GetTraceID(s.c))
	s.mergedContent = mergedContent
	s.mergeWaitDuration = mergeWaitDuration
	s.mergeCount = mergeCount
	s.processEpoch = processEpoch

	if isSimple {
		defer service.DefaultMessageQueueService.SimpleMessageDone(s.tenantID, s.customer.ID)
		replyDelayMode := runtimecfg.DefaultSystemConfigService.GetString("reply_delay_mode", "normal")
		if replyDelayMode != "instant" {
			simpleDelay := service.GetSimpleReplyDelay()
			chatflow.CancellableSleep(s.customer.ID, simpleDelay)
		}

		simpleReply := service.GetSimpleReply(s.req.Content)
		// 查找或创建活跃会话（G1 收口 2026-09-16C）
		conv, _, convErr := chatflow.EnsureActiveConversation(
			s.tenantID, s.customer.ID, s.customer.AssignedUserID,
			func(cv *model.Conversation) { cv.Channel = "web" },
		)
		if convErr != nil {
			log.Printf("[测试接口-告警] 客户%d 会话保障失败: %v", s.customer.ID, convErr)
			RespErr(s.c, http.StatusInternalServerError, 500, "会话初始化失败，请重试")
			return true
		}
		s.conversation = conv
		// 更新先存的那条客户消息的conversation_id
		db.RQ(s.c).Model(&model.Message{}).Where("id = ?", s.testCustomerMsgID).Update("conversation_id", conv.ID)
		_ = attribution.MarkHookedBeforeMessage(s.tenantID, conv.ID, s.testCustomerMsgID)
		simpleMsg := model.Message{
			ConversationID: conv.ID,
			CustomerID:     s.customer.ID,
			SenderType:     "ai",
			Content:        simpleReply,
			MessageType:    "text",
			RouteResult:    "simple_fast",
			CreatedAt:      time.Now(),
		}
		db.RQ(s.c).Create(&simpleMsg)
		// P1-1 实时推送：客户消息 + 简单回复
		var simpleCustMsg model.Message
		db.RQ(s.c).First(&simpleCustMsg, s.testCustomerMsgID)
		if simpleCustMsg.ID > 0 {
			notifyWSWithContent(s.tenantID, s.customer.ID, conv.ID, "customer", simpleCustMsg.ID, simpleCustMsg.Content, s.customer.Name, simpleCustMsg.CreatedAt.Format("2006-01-02T15:04:05Z"))
		}
		notifyWSWithContent(s.tenantID, s.customer.ID, conv.ID, "ai", simpleMsg.ID, simpleReply, "AI顾问", simpleMsg.CreatedAt.Format("2006-01-02T15:04:05Z"))

		// 简单消息路径也调用AutoTagFromText
		autoTags, tagErr := service.DefaultTagService.AutoTagFromText(s.customer.TenantID, s.customer.ID, s.req.Content)
		if tagErr == nil && len(autoTags) > 0 {
			log.Printf("[测试接口-简单消息] 客户%d自动打标: %v", s.customer.ID, autoTags)
		}

		RespOK(s.c, "success", gin.H{
			"conversation_id":    conv.ID,
			"ai_reply":           simpleReply,
			"message":            simpleMsg,
			"customer_msg_id":    s.testCustomerMsgID,
			"assistant_messages": []model.Message{simpleMsg},
		})
		return true
	}

	if !shouldProcess {
		// 合并消息也必须立即回填会话并推送顾问端。
		var activeConv model.Conversation
		db.RQ(s.c).Where("customer_id = ? AND status = ?", s.customer.ID, "active").
			Order("updated_at DESC").Limit(1).Find(&activeConv)
		if activeConv.ID > 0 {
			db.RQ(s.c).Model(&model.Message{}).Where("id = ?", s.testCustomerMsgID).Update("conversation_id", activeConv.ID)
			notifyWSWithContent(s.tenantID, s.customer.ID, activeConv.ID, "customer", s.testCustomerMsgID, s.req.Content, s.customer.Name, time.Now().Format("2006-01-02T15:04:05Z"))
		}
		RespOK(s.c, "success", gin.H{
			"merged":          true,
			"merged_note":     "本条消息已与先前的消息合并处理，回复将在主请求中返回",
			"customer_input":  s.req.Content,
			"customer_msg_id": s.testCustomerMsgID,
		})
		return true
	}

	return false
}

// chatUnauthorizedEnsureProcessing 拿到处理权后确保会话（查/建），并立即回填客户消息的 conversation_id 与合并内容、推送顾问端。
func (s *chatUnauthorizedCtx) chatUnauthorizedEnsureProcessing() {
	// Bug 2 修复：会话"先查再建"防重；统一走 EnsureActiveConversation（G1 收口）
	var conversation model.Conversation
	isNewConversation := false

	convEnsured, created, convErr := chatflow.EnsureActiveConversation(
		s.tenantID, s.customer.ID, s.customer.AssignedUserID,
		func(cv *model.Conversation) { cv.Channel = "web" },
	)
	if convErr != nil {
		// 持处理权后早退必须归还批次（P0-8 纪律），空回复释放
		log.Printf("[测试接口-告警] 客户%d 会话保障失败: %v", s.customer.ID, convErr)
		service.DefaultMessageQueueService.SetReply(s.tenantID, s.customer.ID, s.processEpoch, "")
		RespErr(s.c, http.StatusInternalServerError, 500, "会话初始化失败，请重试")
		return
	}
	conversation, isNewConversation = convEnsured, created
	s.conversation = conversation
	s.isNewConversation = isNewConversation

	if isNewConversation {
		log.Printf("[测试接口] 冷启动: 客户%d创建新会话%d", s.customer.ID, conversation.ID)
		// 启动默认流程（新会话的初始流程引擎）
		flowCtx := &flow.FlowContext{
			TenantID:       s.tenantID,
			CustomerID:     s.customer.ID,
			ConversationID: conversation.ID,
			RouteResult:    "ai",
		}
		flow.DefaultEngine.StartFlow("default_chat_flow", flowCtx)
	} else {
		log.Printf("[测试接口] 复用已有会话%d, 客户%d", conversation.ID, s.customer.ID)
	}

	// 会话已确定，立即回填该请求客户消息的 conversation_id 与合并后内容，并推送顾问端。
	if s.mergedContent != s.req.Content {
		db.RQ(s.c).Model(&model.Message{}).Where("id = ?", s.testCustomerMsgID).Updates(map[string]interface{}{
			"conversation_id": conversation.ID,
			"content":         s.mergedContent,
			"emotion":         strategy.DetectEmotion(s.mergedContent),
		})
	} else {
		db.RQ(s.c).Model(&model.Message{}).Where("id = ?", s.testCustomerMsgID).Update("conversation_id", conversation.ID)
	}
	var backfilledCustMsg model.Message
	db.RQ(s.c).First(&backfilledCustMsg, s.testCustomerMsgID)
	if backfilledCustMsg.ID > 0 {
		notifyWSWithContent(s.tenantID, s.customer.ID, conversation.ID, "customer", backfilledCustMsg.ID, backfilledCustMsg.Content, s.customer.Name, backfilledCustMsg.CreatedAt.Format("2006-01-02T15:04:05Z"))
	}
	_ = attribution.MarkHookedBeforeMessage(s.tenantID, conversation.ID, s.testCustomerMsgID)
}

// chatUnauthorizedHumanTakeover 人工接管模式：裁决统一走 chatflow.HumanTakeoverDecide（A1，
// 与正式链共用唯一真相实现）；跳过 AI 时补齐留资检测/落库/推送并归还处理权，
// 命中早退路径 return true，否则 return false 继续走 AI 生成。
func (s *chatUnauthorizedCtx) chatUnauthorizedHumanTakeover() bool {
	// 人工接管模式：顾问超时未回则AI回复，已回则跳过AI
	aiTimeout := runtimecfg.DefaultSystemConfigService.GetInt("assigned_lead_ai_timeout", 300)
	// A1(2026-09-22)：本函数原有的三段判定（单人模式超时重开 / 单人模式静默 / 顾问窗内已回静默）
	// 已抽到 chatflow.HumanTakeoverDecide，此处只保留"跳过 AI 时怎么应答"的链路副作用。
	dec := chatflow.HumanTakeoverDecide(&s.conversation)
	if dec.Action == chatflow.TakeoverAReply {
		if dec.Reopened {
			log.Printf("[ChatUnauthorized] 会话%d 顾问超时%d秒未回复，自动重开AI回复", s.conversation.ID, aiTimeout)
		} else if s.conversation.Mode == "human" && s.conversation.IsHumanLocked {
			// 顾问未回复或超时，AI自动回复
			log.Printf("[ChatUnauthorized] 客户%d 会话%d 人工接管，AI自动回复(timeout=%ds)",
				s.customer.ID, s.conversation.ID, aiTimeout)
		}
		return false
	}

	if dec.Route == chatflow.RouteHumanLockedNoAI {
		log.Printf("[ChatUnauthorized] 客户%d 会话%d 人工接管且AI回复已关闭(IsAiReplyEnabled=false)，跳过AI",
			s.customer.ID, s.conversation.ID)
		// 客户消息中包含手机号 → 在此处提前调用留资检测
		if s.customer.JourneyStage != model.JourneyLeadCaptured && s.customer.JourneyStage != model.JourneyArrived &&
			s.customer.JourneyStage != model.JourneyOrdered && s.customer.JourneyStage != model.JourneyDelivered {
			leadResult := chatflow.DetectLeadCapture(s.req.Content, &s.customer)
			if leadResult != 0 {
				log.Printf("[ChatUnauthorized-留资检测-human_locked] 客户 %d 已留资+分配顾问", s.customer.ID)
				if leadResult > 0 {
					log.Printf("[ChatUnauthorized-留资检测-OneID] 前端需切换customer_id → %d", leadResult)
				}
			}
		}
		now := time.Now()
		s.conversation.LastMessageAt = &now
		db.RQ(s.c).Model(&model.Conversation{}).Where("id = ?", s.conversation.ID).
			Update("last_message_at", now)
		db.RQ(s.c).Model(&model.Message{}).Where("id = ?", s.testCustomerMsgID).Update("conversation_id", s.conversation.ID)
		var lockedCustMsg model.Message
		db.RQ(s.c).First(&lockedCustMsg, s.testCustomerMsgID)
		if lockedCustMsg.ID > 0 {
			notifyWSWithContent(s.tenantID, s.customer.ID, s.conversation.ID, "customer", lockedCustMsg.ID, lockedCustMsg.Content, s.customer.Name, lockedCustMsg.CreatedAt.Format("2006-01-02T15:04:05Z"))
		}
		s.c.JSON(http.StatusOK, schema.Response{
			Code:    0,
			Message: "success",
			Data: schema.ChatResponse{
				ConversationID:    s.conversation.ID,
				AssistantMessages: []model.Message{},
				RouteResult:       chatflow.RouteHumanLockedNoAI,
				Mode:              "human",
				CustomerMsgID:     s.testCustomerMsgID,
			},
		})
		// 处理权早退必须归还队列锁
		service.DefaultMessageQueueService.SetReply(s.tenantID, s.customer.ID, s.processEpoch, "")
		return true
	}

	// RouteHumanSkipAI：顾问在超时窗内已回复，AI 不抢答
	log.Printf("[ChatUnauthorized] 客户%d 会话%d 人工接管，顾问%ds内已回复，跳过AI（客户消息已入库顾问秒看）",
		s.customer.ID, s.conversation.ID, aiTimeout)
	now := time.Now()
	s.conversation.LastMessageAt = &now
	db.RQ(s.c).Model(&model.Conversation{}).Where("id = ?", s.conversation.ID).
		Update("last_message_at", now)
	db.RQ(s.c).Model(&model.Message{}).Where("id = ?", s.testCustomerMsgID).Update("conversation_id", s.conversation.ID)
	var skipCustMsg model.Message
	db.RQ(s.c).First(&skipCustMsg, s.testCustomerMsgID)
	if skipCustMsg.ID > 0 {
		notifyWSWithContent(s.tenantID, s.customer.ID, s.conversation.ID, "customer", skipCustMsg.ID, skipCustMsg.Content, s.customer.Name, skipCustMsg.CreatedAt.Format("2006-01-02T15:04:05Z"))
	}

	s.c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data: schema.ChatResponse{
			ConversationID:    s.conversation.ID,
			AssistantMessages: []model.Message{},
			RouteResult:       chatflow.RouteHumanSkipAI,
			Mode:              "human",
			CustomerMsgID:     s.testCustomerMsgID,
		},
	})
	// 处理权早退必须归还队列锁
	service.DefaultMessageQueueService.SetReply(s.tenantID, s.customer.ID, s.processEpoch, "")
	return true
}

// chatUnauthorizedLeadIntercept 处理权段的硬拦截留资检测（手机号校验+分配顾问）。
// 副作用：命中时重载客户、置 guided 轮数，并填充 s.testLeadResult 供返回 MergedCustomerID 使用。
func (s *chatUnauthorizedCtx) chatUnauthorizedLeadIntercept() {
	testLeadResult := 0
	if s.customer.JourneyStage != model.JourneyLeadCaptured && s.customer.JourneyStage != model.JourneyArrived &&
		s.customer.JourneyStage != model.JourneyOrdered && s.customer.JourneyStage != model.JourneyDelivered {
		testLeadResult = chatflow.DetectLeadCapture(s.mergedContent, &s.customer)
		if testLeadResult != 0 {
			log.Printf("[留资检测-硬拦截-测试接口] 客户 %d 已留资，自动分配顾问", s.customer.ID)
			if testLeadResult > 0 {
				log.Printf("[留资检测-OneID-测试接口] 前端需切换customer_id → %d", testLeadResult)
			}
			if err := db.RQ(s.c).First(&s.customer, s.customer.ID).Error; err == nil {
				log.Printf("[留资检测-硬拦截-测试接口] 客户 %d 重新加载成功, journey_stage=%s", s.customer.ID, s.customer.JourneyStage)
			}
			db.RQ(s.c).Model(&model.Conversation{}).Where("id = ?", s.conversation.ID).Updates(map[string]interface{}{
				"guided_remaining_rounds": 1,
				"guided_disabled":         false,
			})
			s.conversation.GuidedRemainingRounds = 1
			s.conversation.GuidedDisabled = false
		}
	}
	s.testLeadResult = testLeadResult
}

// chatUnauthorizedGenerateReply 调用策略引擎（合并后内容推理）+ 生成AI回复 + 内容安全闸门。
// 副作用：填充 s.aiReply / s.strategyOutput / s.tVector。
func (s *chatUnauthorizedCtx) chatUnauthorizedGenerateReply() {
	s.tVector = s.customer.BuildBaseTVector()
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
		TenantID:       s.customer.TenantID,
		DeptIDs:        service.DeptChainForUser(s.conversation.AssignedUserID),
	}
	s.strategyOutput = strategy.DefaultEngine.Infer(strategyInput)

	// 生成AI回复
	aiReply := flow.DefaultEngine.OrchestrateReply(middleware.CtxWithTrace(s.c), &s.customer, s.conversation.ID, s.mergedContent, &s.strategyOutput, service.DeptChainForUser(s.conversation.AssignedUserID))

	// 7.5 内容安全闸门（G8 修复，2026-09-14）：与 chat_main 复用同一 ContentsafetyGate
	if action, out := ContentsafetyGate(aiReply, s.conversation.ID); aiReply != "" && action != GatePass {
		switch action {
		case GateRewrite:
			aiReply = out
		case GateBlock:
			aiReply = SafetyHandoffReply()
			s.conversation.Mode = "human"
			s.conversation.IsHumanLocked = true
			s.conversation.IsAiReplyEnabled = false
			db.RQ(s.c).Model(&s.conversation).Updates(map[string]interface{}{
				"mode":                "human",
				"is_human_locked":     true,
				"is_ai_reply_enabled": false,
			})
			log.Printf("[测试接口] 会话%d 内容安全拦截，已转人工等待顾问", s.conversation.ID)
		}
	}

	s.aiReply = aiReply
}

// chatUnauthorizedSaveAndReturn 落库 AI 回复、施加模拟延迟、归还批次、更新会话/客户、写归因与打标，并返回响应。
func (s *chatUnauthorizedCtx) chatUnauthorizedSaveAndReturn() {
	// 保存AI回复消息
	aiMsg := model.Message{
		ConversationID: s.conversation.ID,
		CustomerID:     s.customer.ID,
		SenderType:     "ai",
		Content:        s.aiReply,
		MessageType:    "text",
		AnchorType:     s.strategyOutput.FinalAnchor,
		TemplateID:     s.strategyOutput.TemplateID,
		RouteResult:    s.strategyOutput.RouteResult,
		IntentScore:    s.tVector[0],
		CreatedAt:      time.Now(),
	}
	db.RQ(s.c).Create(&aiMsg)
	publishConversationMsg(s.tenantID, s.customer.ID, s.strategyOutput.RouteResult, s.conversation.GetState().Emotion)
	// P1-1 实时推送：AI回复即时可见
	notifyWSWithContent(s.tenantID, s.customer.ID, s.conversation.ID, "ai", aiMsg.ID, s.aiReply, "AI顾问", aiMsg.CreatedAt.Format("2006-01-02T15:04:05Z"))

	// 模拟真人回复延迟
	isStoreVisit := service.IsStoreVisitIntentForTenant(s.tenantID, s.mergedContent) && !chatflow.IsLeadCaptured(&s.customer)
	log.Printf("[ChatUnauthorized] 客户%d 到店倾向检测: %v, 合并内容: %q", s.customer.ID, isStoreVisit, pii.MaskPhoneInText(s.mergedContent))
	humanlikeDelay := service.CalcHumanlikeDelay(s.tenantID, s.aiReply, s.mergeWaitDuration, s.mergeCount, isStoreVisit)

	// 胡搅蛮缠：总非车话题>10且最近未恢复→回复速度降到3分钟一次
	hjTotalOffTopic := chatflow.CountTotalOffTopic(s.customer.ID)
	hjOnTopic := chatflow.CountConsecutiveOnTopic(s.customer.ID)
	if hjTotalOffTopic > 10 && hjOnTopic < 3 {
		minDelay := 180 * time.Second
		if humanlikeDelay < minDelay {
			log.Printf("[胡搅蛮缠-降速-测试接口] 客户%d 非车%d>10, 延迟从%.1fs提升到%.1fs",
				s.customer.ID, hjTotalOffTopic, humanlikeDelay.Seconds(), minDelay.Seconds())
			humanlikeDelay = minDelay
		}
	}

	// 总回复时长2分钟硬顶兜底
	maxTotalDelay := 120 * time.Second
	elapsed := time.Since(s.requestStart)
	remainingBudget := maxTotalDelay - elapsed
	if humanlikeDelay > remainingBudget {
		log.Printf("[ChatUnauthorized] 客户%d 总延迟硬顶触发: 已用%.1fs + 模拟延迟%.1fs > 2分钟, 截断到%.1fs",
			s.customer.ID, elapsed.Seconds(), humanlikeDelay.Seconds(), remainingBudget.Seconds())
		humanlikeDelay = remainingBudget
	}
	if humanlikeDelay < 0 {
		humanlikeDelay = 0
	}

	log.Printf("[ChatUnauthorized] 客户%d 模拟延迟: %.1fs, 已用: %.1fs, 总计: %.1fs, 开始sleep...", s.customer.ID, humanlikeDelay.Seconds(), elapsed.Seconds(), (elapsed + humanlikeDelay).Seconds())
	replyDelayMode := runtimecfg.DefaultSystemConfigService.GetString("reply_delay_mode", "normal")
	if replyDelayMode == "instant" {
		log.Printf("[ChatUnauthorized] 客户%d instant模式，跳过延迟直接回复", s.customer.ID)
	} else {
		chatflow.CancellableSleep(s.customer.ID, humanlikeDelay)
	}
	log.Printf("[ChatUnauthorized] 客户%d 延迟结束，返回回复", s.customer.ID)

	// 回复写入队列缓存，唤醒所有等待的请求
	service.DefaultMessageQueueService.SetReply(s.tenantID, s.customer.ID, s.processEpoch, s.aiReply)

	// 更新会话状态
	s.conversation.LastMessageAt = &aiMsg.CreatedAt
	s.conversation.LastTid = s.strategyOutput.TemplateID
	s.conversation.LastAnchorType = s.strategyOutput.FinalAnchor
	s.conversation.Emotion = strategy.DetectEmotion(s.mergedContent)

	updatedState := chatflow.UpdateConversationState(&s.conversation, &s.strategyOutput, &s.customer, s.mergedContent)
	s.conversation.SaveState(updatedState)
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

	// 更新客户画像（意向分反哺）
	newIntent := s.tVector[0] + s.strategyOutput.IntentDelta
	if newIntent < 0 {
		newIntent = 0
	}
	if newIntent > 1 {
		newIntent = 1
	}
	s.customer.IntentScore = newIntent
	newTVector := s.customer.GetTVector()
	newTVector[0] = newIntent
	s.customer.SaveTVector(newTVector)
	db.RQ(s.c).Model(&model.Customer{}).Where("id = ?", s.customer.ID).Updates(map[string]interface{}{
		"intent_score": s.customer.IntentScore,
		"t_vector":     s.customer.TVectorJSON,
	})

	// D9：测试链路也写入包/模板/意向变化归因快照。
	_ = attribution.RecordReply(attribution.RecordReplyInput{
		TenantID:       s.tenantID,
		MessageID:      aiMsg.ID,
		ConversationID: s.conversation.ID,
		CustomerID:     s.customer.ID,
		TemplateID:     s.strategyOutput.TemplateID,
		AnchorType:     s.strategyOutput.FinalAnchor,
		RouteResult:    s.strategyOutput.RouteResult,
		IntentBefore:   s.tVector[0],
		IntentAfter:    newIntent,
	})
	if s.strategyOutput.RouteResult == strategy.RoutePendingHuman {
		_ = attribution.MarkPendingHuman(s.tenantID, s.conversation.ID, s.customer.ID)
	}

	// 自动打标（测试接口也集成，方便验证打标效果）
	newTags := []string{}
	autoTags, tagErr := service.DefaultTagService.AutoTagFromText(s.customer.TenantID, s.customer.ID, s.mergedContent)
	if tagErr == nil && len(autoTags) > 0 {
		newTags = autoTags
		log.Printf("[测试接口] 客户%d自动打标: %v", s.customer.ID, autoTags)

		var updatedCustomer model.Customer
		if err := db.RQ(s.c).First(&updatedCustomer, s.customer.ID).Error; err == nil {
			_ = service.DefaultTagService.ApplyTagWeightsToTVector(s.customer.TenantID, &updatedCustomer, updatedCustomer.BuildBaseTVector())
			db.RQ(s.c).Model(&updatedCustomer).Update("t_vector", updatedCustomer.TVectorJSON)
		}
	}
	s.newTags = newTags

	// 返回结果
	RespOK(s.c, "success", gin.H{
		"conversation_id":     s.conversation.ID,
		"customer_input":      s.req.Content,
		"merged_content":      s.mergedContent,
		"ai_reply":            s.aiReply,
		"assistant_messages":  []model.Message{aiMsg},
		"new_tags":            newTags,
		"is_new_conversation": s.isNewConversation,
		"customer_msg_id":     s.testCustomerMsgID,
		"anchor": gin.H{
			"type":              s.strategyOutput.FinalAnchor,
			"name":              strategy.GetAnchorName(s.strategyOutput.FinalAnchor),
			"confidence":        s.strategyOutput.AnchorConfidence,
			"original_anchor":   s.strategyOutput.OriginalAnchor,
			"soft_downgrade":    s.strategyOutput.SoftDowngrade,
			"stage_before_lock": s.strategyOutput.StageBeforeLock,
			"stage_ceiling_agg": s.strategyOutput.StageCeilingAgg,
			"stage_downgraded":  s.strategyOutput.StageDowngraded,
		},
		"template": gin.H{
			"id":   s.strategyOutput.TemplateID,
			"name": s.strategyOutput.TemplateName,
		},
		"exchange": gin.H{
			"flag": s.strategyOutput.ExchangeFlag,
			"type": s.strategyOutput.ExchangeType,
		},
		"route": gin.H{
			"result": s.strategyOutput.RouteResult,
			"reason": s.strategyOutput.RouteReason,
		},
		"urgency_level":      s.strategyOutput.UrgencyLevel,
		"intent_delta":       s.strategyOutput.IntentDelta,
		"is_ai_mode":         !config.GlobalConfig.AI.MockMode && !runtimecfg.DefaultSystemConfigService.GetBool("mock_mode", false) && (ai.DefaultClient.APIKey != "" || (ai.SiliconFlowDefaultClient != nil && ai.SiliconFlowDefaultClient.Enabled)),
		"merged_customer_id": s.testLeadResult,
	})
}

// ============================================================
// 会话欢迎接口（独立秒回，无AI处理）
//
// 与 /chat/test 和 /chat 不同，此接口不做任何AI处理，只做：
//   1. 查找或创建该客户的活跃会话（竞态保护）
//   2. 保存一条系统欢迎消息到DB
//   3. 立刻返回欢迎消息 + 会话ID
//
// 设计原因：
//   原方案把欢迎消息跟AI回复绑在一起返回（同步HTTP只能返回一次），
//   导致欢迎消息要等策略引擎+AI生成完了才到，不是真正的"秒回"。
//   新方案把欢迎消息拆成独立接口，前端在用户"打开页面"时先调此接口，
//   欢迎消息毫秒级到达，用户发消息时再调 /chat/test 获取AI回复。
//
// 使用场景（前端控制调用时机）：
//   - 用户打开页面 → 调 /chat/welcome → 欢迎消息秒回显示
//   - 用户发送消息 → 调 /chat/test → AI回复正常返回
//   - 用户关闭页面重新打开 → 再调 /chat/welcome → 新的欢迎消息
//   - 只要用户不关闭页面，不重复调用此接口
//
// 注意：每次调用都会创建一条新的欢迎消息记录，
// 前端负责控制调用时机（仅在页面打开时调用一次）。
// ============================================================

// Welcome 会话欢迎接口（秒回，无AI处理）
// 前端在用户打开页面时调用，立刻返回欢迎消息和会话ID
