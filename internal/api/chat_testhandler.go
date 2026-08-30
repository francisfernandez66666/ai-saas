package api

// 对话核心API：C端客户与B端销售共用的交互入口，链路为 客户发消息→策略中心7步推理→AI生成回复。
// 含会话竞态保护、三层分流(硬边界/到店快速通道/简单消息)、合并队列、延迟清零、留资检测与OneID合并。

import (
	"ai-scrm/config"
	"ai-scrm/internal/ai"
	"ai-scrm/internal/chatflow"
	"ai-scrm/internal/db"
	"ai-scrm/internal/engine/flow"
	"ai-scrm/internal/engine/strategy"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
	"ai-scrm/internal/mq"
	"ai-scrm/internal/schema"
	"ai-scrm/internal/service"
	"context"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"regexp"
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

// Chat POST /api/v1/chat 正式对话入口（JWT链；硬边界→快速通道→简单消息→合并队列四层分流）
func ChatTest(c *gin.Context) {
	// 修复：记录请求开始时间，用于总延迟2分钟硬顶兜底
	// 总回复时长 = 合并等待 + AI调用 + 模拟延迟，不得超过2分钟
	requestStart := time.Now()

	// 生效租户：测试接口同样走租户隔离的合并队列
	tenantID := middleware.EffectiveTenantID(c)

	var req struct {
		CustomerID uint   `json:"customer_id"`                // 客户ID，默认用1号模拟客户
		Content    string `json:"content" binding:"required"` // 客户说的话
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误: "+err.Error())
		return
	}

	// 默认用1号客户
	customerID := req.CustomerID
	if customerID == 0 {
		customerID = 1
	}

	// 查询客户
	var customer model.Customer
	result := db.RQ(c).First(&customer, customerID)
	if result.Error != nil {
		RespErr(c, http.StatusNotFound, 404, "客户不存在，可用customer_id=1测试")
		return
	}

	// ---- 修复：入队列前三层分流（与Chat正式接口同步） ----
	// 优先级：硬边界拦截(0延迟) > 到店倾向快速通道(10-15秒) > 简单消息(8秒) > 正常合并队列
	// 根因：用户发"高数题"等了9分钟才收到回复；"试驾"意向被合并吞掉
	// 原来所有消息都先进合并队列(25s)，再到GenerateAIReply里才查硬边界和到店倾向——太晚了

	// 第一层：硬边界拦截（0延迟，不走AI，不进队列）
	if service.IsOffTopic(req.Content) {
		reply := service.GetOffTopicReply(req.Content)
		log.Printf("[硬边界-测试接口] 客户%d 拦截无关话题(入队前): %q → %q", customer.ID, service.MaskPhoneInText(req.Content), service.MaskPhoneInText(reply))
		// 查找或创建活跃会话
		var conv model.Conversation
		if err := db.RQ(c).Where("customer_id = ? AND status = ?", customer.ID, "active").
			Order("updated_at DESC").First(&conv).Error; err != nil {
			conv = model.Conversation{
				CustomerID:     customer.ID,
				AssignedUserID: customer.AssignedUserID,
				Status:         "active",
				Mode:           "ai",
				Channel:        "web",
			}
			db.RQ(c).Create(&conv)
		}
		// 保存客户消息+AI拦截回复
		customerMsg := model.Message{
			ConversationID: conv.ID,
			CustomerID:     customer.ID,
			SenderType:     "customer",
			Content:        req.Content,
			MessageType:    "text",
			CreatedAt:      time.Now(),
		}
		db.RQ(c).Create(&customerMsg)
		offTopicMsg := model.Message{
			ConversationID: conv.ID,
			CustomerID:     customer.ID,
			SenderType:     "ai",
			Content:        reply,
			MessageType:    "text",
			RouteResult:    "offtopic_hardbound",
			CreatedAt:      time.Now(),
		}
		db.RQ(c).Create(&offTopicMsg)
		// 修复问题7：硬边界拦截路径也调用AutoTagFromText，确保无关话题场景也打标签
		autoTags, tagErr := service.DefaultTagService.AutoTagFromText(customer.ID, req.Content)
		if tagErr == nil && len(autoTags) > 0 {
			log.Printf("[测试接口-硬边界] 客户%d自动打标: %v", customer.ID, autoTags)
		}
		// 唤醒队列中可能等待的其他请求
		service.DefaultMessageQueueService.SetReply(tenantID, customer.ID, reply)
		RespOK(c, "success", gin.H{
			"conversation_id":    conv.ID,
			"ai_reply":           reply,
			"route_result":       "offtopic_hardbound",
			"customer_msg_id":    customerMsg.ID,               // 修复问题4：返回客户消息DB ID
			"assistant_messages": []model.Message{offTopicMsg}, // 修复：返回带真实DB ID的消息列表，前端用此去重
		})
		return
	}

	// 第二层：到店倾向快速通道（不进合并队列，直接快速回复）
	// 分支逻辑（硬编码，与Chat正式接口统一）：
	// A. 客户已留资 → 跳过，走正常流程
	// B. 当前消息含手机号 → 已留资线索：标记留资+分配顾问（基于手机号校验）+确认回复
	// C. 当前消息无手机号 → 未留资线索：关闭引导+两段式追问，推迟分配顾问到手机号回复时
	if strategy.IsStoreVisitIntent(req.Content) {
		// ---- 前置拦截：已留资客户不再走到店快速通道 ----
		if customer.JourneyStage == model.JourneyLeadCaptured ||
			customer.JourneyStage == model.JourneyArrived ||
			customer.JourneyStage == model.JourneyOrdered ||
			customer.JourneyStage == model.JourneyDelivered {
			log.Printf("[到店倾向-测试接口] 客户%d 已是%s阶段，跳过到店快速通道",
				customer.ID, customer.JourneyStage)
			goto skipStoreVisitFastTest
		}

		// ---- 留资前置检测：当前消息是否包含手机号 ----
		phoneRegexTest := regexp.MustCompile(`1[3-9]\d{9}`)
		phoneMatchTest := phoneRegexTest.FindString(req.Content)

		// 查找或创建活跃会话（各分支共用）
		var conv model.Conversation
		if err := db.RQ(c).Where("customer_id = ? AND status = ?", customer.ID, "active").
			Order("updated_at DESC").First(&conv).Error; err != nil {
			conv = model.Conversation{
				CustomerID:     customer.ID,
				AssignedUserID: customer.AssignedUserID,
				Status:         "active",
				Mode:           "ai",
				Channel:        "web",
			}
			db.RQ(c).Create(&conv)
		}

		// 保存客户消息（各分支共用）
		customerMsg := model.Message{
			ConversationID: conv.ID,
			CustomerID:     customer.ID,
			SenderType:     "customer",
			Content:        req.Content,
			MessageType:    "text",
			CreatedAt:      time.Now(),
		}
		db.RQ(c).Create(&customerMsg)

		// 修复：到店倾向快速通道之前完全没打标，导致客户刚留资试驾这一段
		// 高信号内容白白漏掉。现在和其它分支一样，每条客户消息落库后立刻打标，
		// 不管走哪个路由分支、不管有没有分配顾问，做到"持续打标"。
		if autoTags, tagErr := service.DefaultTagService.AutoTagFromText(customer.ID, req.Content); tagErr == nil && len(autoTags) > 0 {
			log.Printf("[到店倾向-测试接口] 客户%d自动打标: %v", customer.ID, autoTags)
			var updatedCustomerForTag model.Customer
			if err := db.RQ(c).First(&updatedCustomerForTag, customer.ID).Error; err == nil {
				_ = service.DefaultTagService.ApplyTagWeightsToTVector(&updatedCustomerForTag, updatedCustomerForTag.BuildBaseTVector())
				db.RQ(c).Model(&updatedCustomerForTag).Update("t_vector", updatedCustomerForTag.TVectorJSON)
			}
		}

		if phoneMatchTest != "" {
			// ====== 分支B：已留资线索（硬编码） ======
			log.Printf("[到店倾向-已留资线索-测试接口] 客户%d 消息含手机号%s，走已留资硬编码路径",
				customer.ID, phoneMatchTest)

			// 0. OneID合并：手机号匹配到老客户时，迁移所有数据
			mergedTargetIDTest := chatflow.MergeCustomerByPhone(&customer, phoneMatchTest)
			if mergedTargetIDTest > 0 {
				var reloadedCust model.Customer
				db.RQ(c).Where("id = ?", mergedTargetIDTest).First(&reloadedCust)
				customer = reloadedCust
				db.RQ(c).Where("customer_id = ? AND status = ?", customer.ID, "active").
					Order("updated_at DESC").First(&conv)
				log.Printf("[到店倾向-已留资-测试接口-OneID] 合并到老客户%d，前端需切换customer_id", mergedTargetIDTest)
			}

			// 1. 标记留资 + 分配顾问
			leadUpdates := map[string]interface{}{}
			if customer.Phone == "" {
				leadUpdates["phone"] = phoneMatchTest
			}
			leadUpdates["journey_stage"] = model.JourneyLeadCaptured
			leadUpdates["assignment_reason"] = "lead_captured"
			// 修复问题2：ChatTest到店倾向已留资分支也用轮询选顾问
			if customer.AssignedUserID == 0 {
				var salesUsers []model.User
				// 修复Bug1（2026-08-22）：角色改用 model.RoleSales 常量（同主路径）
				db.RQ(c).Where("role = ? AND status = 1", model.RoleSales).Find(&salesUsers)
				if len(salesUsers) > 0 {
					minCount := -1
					var bestUserID uint = salesUsers[0].ID
					for _, u := range salesUsers {
						var count int64
						db.RQ(c).Model(&model.Customer{}).Where("assigned_user_id = ? AND status = 1", u.ID).Count(&count)
						if minCount < 0 || int(count) < minCount {
							minCount = int(count)
							bestUserID = u.ID
						}
					}
					leadUpdates["assigned_user_id"] = bestUserID
				} else {
					// 修复Bug1：兜底值必须显式 uint，防 v.(uint) 断言 panic
					leadUpdates["assigned_user_id"] = uint(2) // 兜底：张伟(ID=2)
				}
			}
			if len(leadUpdates) > 0 {
				db.RQ(c).Model(&customer).Updates(leadUpdates)
				// 同步内存对象（修复Bug1：断言改安全形式）
				if v, ok := leadUpdates["phone"]; ok {
					customer.Phone, _ = v.(string)
				}
				if v, ok := leadUpdates["journey_stage"]; ok {
					customer.JourneyStage, _ = v.(string)
				}
				if v, ok := leadUpdates["assigned_user_id"]; ok {
					if uid, uok := v.(uint); uok {
						customer.AssignedUserID = uid
					} else {
						log.Printf("[留资-告警][测试接口] assigned_user_id 类型异常(%T)，保持原值: %v", v, customer.AssignedUserID)
					}
				}
			}
			log.Printf("[到店倾向-已留资线索-测试接口] 客户%d 留资成功: phone=%s, stage=lead_captured, assigned=%d",
				customer.ID, service.MaskPhone(phoneMatchTest), customer.AssignedUserID)
			// P3：到店分支留资事件上行（ChatTest 路径）
			if err := mq.Publish(context.Background(), mq.TopicUserEvent, tenantID,
				fmt.Sprintf("c:%d", customer.ID), "lead_captured",
				mq.UserEvent{EventType: "behavior", EventName: "lead_captured", AnchorType: "phone",
					Attributes: map[string]any{"customer_id": customer.ID, "path": "store_visit_branch_test"},
					OccurredAt: time.Now()}); err != nil {
				log.Printf("[MQ] lead_captured(到店分支测试) 发布失败: %v", err)
			}

			// 2. 生成线索记录（给顾问看）
			followUp := model.FollowUp{
				CustomerID:     customer.ID,
				ConversationID: conv.ID,
				UserID:         customer.AssignedUserID,
				Type:           "ai_triggered",
				Method:         "store",
				Content:        fmt.Sprintf("客户到店意向+已留资，手机号:%s，原始消息:%s", phoneMatchTest, req.Content),
				Result:         "lead_captured",
			}
			db.RQ(c).Create(&followUp)
			log.Printf("[到店倾向-已留资线索-测试接口] 客户%d 线索已生成(FollowUp ID=%d)，分配顾问%d",
				customer.ID, followUp.ID, customer.AssignedUserID)

			// 3. 通知顾问
			log.Printf("[通知顾问-测试接口] 顾问%d 有新的已留资到店线索：客户%d，手机号%s",
				customer.AssignedUserID, customer.ID, phoneMatchTest)

			// 4. 标记待人工接管，留1轮引导式反问（下一条AI回复时抛）
			// 顺序：先发确认语→客户继续聊→AI再抛反问句
			conv.PendingHandoff = true
			conv.GuidedRemainingRounds = 1
			conv.GuidedDisabled = false
			now := time.Now()
			conv.HandoffNotifiedAt = &now
			conv.LastMessageAt = &now
			db.RQ(c).Save(&conv)

			// 5. 丝滑确认回复（预定义模板随机选，不走AI）
			firstDelay := service.GetStoreVisitFirstDelay()
			chatflow.CancellableSleep(customer.ID, firstDelay)

			leadCapturedReplies := []string{
				"收到！我先帮您约上时间，约好了跟您说~",
				"好嘞，我这就安排，弄好了通知您~",
				"没问题！我这边先帮您约，确认好了跟您说一声~",
				"收到，我先帮您把试驾约上，安排好了告诉您~",
				"好的！我先帮您把时间约好，确认了跟您说~",
			}
			rand.Seed(time.Now().UnixNano())
			leadCapturedReply := leadCapturedReplies[rand.Intn(len(leadCapturedReplies))]

			leadMsg := model.Message{
				ConversationID: conv.ID,
				CustomerID:     customer.ID,
				SenderType:     "ai",
				Content:        leadCapturedReply,
				MessageType:    "text",
				RouteResult:    "lead_captured_confirmed",
				CreatedAt:      time.Now(),
			}
			db.RQ(c).Create(&leadMsg)

			RespOK(c, "success", gin.H{
				"conversation_id":    conv.ID,
				"ai_reply":           leadCapturedReply,
				"route_result":       "lead_captured_confirmed",
				"merged_customer_id": mergedTargetIDTest,       // OneID合并：>0表示前端需切换customer_id
				"customer_msg_id":    customerMsg.ID,           // 修复：返回客户消息DB ID
				"assistant_messages": []model.Message{leadMsg}, // 修复：返回带真实DB ID的消息列表
			})
			return
		}

		// ====== 分支C：未留资线索（关闭引导+两段式AI快速回复，推迟分配顾问） ======
		// 客户表达到店意向但没给手机号 → 关闭引导+两段式AI快速回复，不分配顾问
		// 顾问分配推迟到客户回复手机号时，由 DetectLeadCapture 根据手机号校验决定
		firstReply := service.GetStoreVisitFirstReply(req.Content)
		firstDelay := service.GetStoreVisitFirstDelay()

		log.Printf("[到店倾向-未留资线索-测试接口] 客户%d 关闭引导+两段式回复, 推迟分配顾问", customer.ID)

		// 关闭引导式反问
		conv.GuidedDisabled = true
		db.RQ(c).Model(&conv).Update("guided_disabled", true)

		// 第一段AI快速回复（接住意向）
		chatflow.CancellableSleep(customer.ID, firstDelay)

		storeVisitMsg := model.Message{
			ConversationID: conv.ID,
			CustomerID:     customer.ID,
			SenderType:     "ai",
			Content:        firstReply,
			MessageType:    "text",
			RouteResult:    "store_visit_fast",
			CreatedAt:      time.Now(),
		}
		db.RQ(c).Create(&storeVisitMsg)

		// 第二段追问（异步，25-45秒后发出，收集预约信息）
		// 修复Bug2（2026-08-22）：同主路径——脱离请求生命周期写库，显式租户盖章+错误检查
		secondReply := service.GetStoreVisitSecondReply(req.Content)
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
		}(customer.ID, conv.ID, secondReply, tenantID)

		RespOK(c, "success", gin.H{
			"conversation_id":    conv.ID,
			"ai_reply":           firstReply,
			"route_result":       "store_visit_fast",
			"customer_msg_id":    customerMsg.ID,
			"assistant_messages": []model.Message{storeVisitMsg},
		})
		return
	}
skipStoreVisitFastTest:

	// ---- 修复问题4：先存客户消息到DB，再EnqueueAndWait ----
	// 根因：ChatTest在EnqueueAndWait之后才存客户消息，客户F5刷新时DB里没有这条消息所以丢失
	// 修复：跟Chat接口第263行逻辑一致，先存DB再入队列；合并后如果内容变了再更新DB
	testCustomerMsg := model.Message{
		ConversationID: 0, // 暂填0，后面拿到conversation后更新
		CustomerID:     customer.ID,
		SenderType:     "customer",
		Content:        req.Content,
		MessageType:    "text",
		Emotion:        strategy.DetectEmotion(req.Content),
		CreatedAt:      time.Now(),
	}
	db.RQ(c).Create(&testCustomerMsg)
	testCustomerMsgID := testCustomerMsg.ID // 记录真实DB ID，供后续合并更新和前端替换temp ID

	// 第三层+第四层：简单消息 + 正常合并队列（原有逻辑不变）
	// ---- 消息入队 + 合并窗口等待（和正式接口一致） ----
	// 第一个拿到处理权的请求负责生成回复，后续请求挂起等待
	// cachedReply 不再使用（Bug 1 修复后 merged 请求只返回状态标记）
	mergedContent, shouldProcess, _, mergeWaitDuration, isSimple, mergeCount := service.DefaultMessageQueueService.EnqueueAndWait(tenantID, customer.ID, req.Content)
	if isSimple {
		// H7修复(2026-08-26)：实例内同客户简单消息串行，处理完释放锁
		defer service.DefaultMessageQueueService.SimpleMessageDone(tenantID, customer.ID)
		// 修复问题2：instant模式下简单消息跳过延迟直接回复
		replyDelayMode := service.DefaultSystemConfigService.GetString("reply_delay_mode", "normal")
		if replyDelayMode != "instant" {
			// 修复：简单消息不能秒回，加20-45秒随机延迟
			simpleDelay := service.GetSimpleReplyDelay()
			chatflow.CancellableSleep(customer.ID, simpleDelay)
		}

		simpleReply := service.GetSimpleReply(req.Content)
		// 查找或创建活跃会话
		var conv model.Conversation
		db.RQ(c).Where("customer_id = ? AND status = ?", customer.ID, "active").
			Order("updated_at DESC").Limit(1).Find(&conv)
		if conv.ID == 0 {
			conv = model.Conversation{
				CustomerID:     customer.ID,
				AssignedUserID: customer.AssignedUserID,
				Status:         "active",
				Mode:           "ai",
				Channel:        "web",
			}
			db.RQ(c).Create(&conv)
		}
		// 修复问题4：更新先存的那条客户消息的conversation_id
		db.RQ(c).Model(&model.Message{}).Where("id = ?", testCustomerMsgID).Update("conversation_id", conv.ID)
		simpleMsg := model.Message{
			ConversationID: conv.ID,
			CustomerID:     customer.ID,
			SenderType:     "ai",
			Content:        simpleReply,
			MessageType:    "text",
			RouteResult:    "simple_fast",
			CreatedAt:      time.Now(),
		}
		db.RQ(c).Create(&simpleMsg)

		// 修复问题7：简单消息路径也调用AutoTagFromText，确保简单消息场景也打标签
		autoTags, tagErr := service.DefaultTagService.AutoTagFromText(customer.ID, req.Content)
		if tagErr == nil && len(autoTags) > 0 {
			log.Printf("[测试接口-简单消息] 客户%d自动打标: %v", customer.ID, autoTags)
		}

		RespOK(c, "success", gin.H{
			"conversation_id":    conv.ID,
			"ai_reply":           simpleReply,
			"message":            simpleMsg,
			"customer_msg_id":    testCustomerMsgID,          // 修复问题4：返回客户消息DB ID，供前端替换temp ID
			"assistant_messages": []model.Message{simpleMsg}, // 修复：返回带真实DB ID的消息列表
		})
		return
	}

	if !shouldProcess {
		// Bug 1 修复：合并请求只返回合并状态，不返回完整AI回复
		// 前端收到 merged=true 时，不渲染新消息气泡，等主请求的回复即可
		RespOK(c, "success", gin.H{
			"merged":          true,
			"merged_note":     "本条消息已与先前的消息合并处理，回复将在主请求中返回",
			"customer_input":  req.Content,       // 保留原始输入，方便调试对照
			"customer_msg_id": testCustomerMsgID, // 修复：返回客户消息DB ID，供前端替换temp ID
		})
		return
	}

	// ---- 拿到处理权的请求：用合并后的内容走完整流程 ----

	// Bug 2 修复：会话创建用竞态保护（先查再建），与正式接口统一
	// 原Bug：测试接口完全没有会话管理，冷启动秒回不会出现
	// 修复：用客户级互斥锁串行化"查找或创建"，确保只有一个会话 + 冷启动秒回
	var conversation model.Conversation
	isNewConversation := false
	// 欢迎词已移至 /chat/welcome 接口，此处不再创建

	convMu := chatflow.GetConversationMutex(customer.ID)
	convMu.Lock()

	// 先查该客户是否已有活跃会话（复用，不重复创建）
	result = db.RQ(c).Where("customer_id = ? AND status = ?", customer.ID, "active").
		Order("updated_at DESC").
		First(&conversation)

	if result.Error != nil {
		// 没有活跃会话 → 冷启动，创建新会话 + 秒回消息
		conversation = model.Conversation{
			CustomerID:     customer.ID,
			AssignedUserID: customer.AssignedUserID,
			Status:         "active",
			Mode:           "ai",
			Channel:        "web",
		}
		db.RQ(c).Create(&conversation)
		isNewConversation = true
		log.Printf("[测试接口] 冷启动: 客户%d创建新会话%d", customer.ID, conversation.ID)

		// 欢迎词已移至 /chat/welcome 接口负责秒回，此处不再创建欢迎消息

		// 启动默认流程（新会话的初始流程引擎）
		flowCtx := &flow.FlowContext{
			TenantID:       tenantID,
			CustomerID:     customer.ID,
			ConversationID: conversation.ID,
			RouteResult:    "ai",
		}
		flow.DefaultEngine.StartFlow("default_chat_flow", flowCtx)
	} else {
		// 已有活跃会话，直接复用（不触发冷启动秒回）
		log.Printf("[测试接口] 复用已有会话%d, 客户%d", conversation.ID, customer.ID)
	}

	convMu.Unlock()

	// ---- 人工接管模式：顾问超时未回则AI回复，已回则跳过AI ----
	aiTimeout := service.DefaultSystemConfigService.GetInt("assigned_lead_ai_timeout", 300)
	aiTimeoutDur := time.Duration(aiTimeout) * time.Second
	aiAutoReply := service.DefaultSystemConfigService.GetBool("assigned_lead_ai_auto_reply", true)

	if conversation.Mode == "human" && conversation.IsHumanLocked {
		// IsAiReplyEnabled=false → 单人模式，仅顾问回复
		// 到店场景(PendingHandoff=true)：5分钟超时自动重开AI，恢复人机共存
		// AI接不住场景(PendingHandoff=false)：仅顾问手动点击AI回复按钮恢复
		if !conversation.IsAiReplyEnabled {
			if conversation.PendingHandoff && conversation.LastHumanReplyAt != nil &&
				time.Since(*conversation.LastHumanReplyAt) >= aiTimeoutDur {
				// 超时自动重开AI
				conversation.IsAiReplyEnabled = true
				conversation.IsHumanLocked = false
				conversation.Mode = "ai"
				conversation.PendingHandoff = false
				db.RQ(c).Model(&conversation).Updates(map[string]interface{}{
					"is_ai_reply_enabled": true,
					"is_human_locked":     false,
					"mode":                "ai",
					"pending_handoff":     false,
				})
				log.Printf("[ChatTest] 会话%d 顾问超时%d秒未回复，自动重开AI回复", conversation.ID, aiTimeout)
				// 继续走正常流程（不return）
			} else {
				log.Printf("[ChatTest] 客户%d 会话%d 人工接管且AI回复已关闭(IsAiReplyEnabled=false)，跳过AI",
					customer.ID, conversation.ID)
				// 客户消息中包含手机号 → 在此处提前调用留资检测，确保基于手机号的分配顾问逻辑不被绕过
				if customer.JourneyStage != model.JourneyLeadCaptured && customer.JourneyStage != model.JourneyArrived &&
					customer.JourneyStage != model.JourneyOrdered && customer.JourneyStage != model.JourneyDelivered {
					leadResult := chatflow.DetectLeadCapture(req.Content, &customer)
					if leadResult != 0 {
						log.Printf("[ChatTest-留资检测-human_locked] 客户 %d 已留资+分配顾问", customer.ID)
						if leadResult > 0 {
							log.Printf("[ChatTest-留资检测-OneID] 前端需切换customer_id → %d", leadResult)
						}
					}
				}
				now := time.Now()
				conversation.LastMessageAt = &now
				db.RQ(c).Save(&conversation)
				db.RQ(c).Model(&model.Message{}).Where("id = ?", testCustomerMsgID).Update("conversation_id", conversation.ID)
				c.JSON(http.StatusOK, schema.Response{
					Code:    0,
					Message: "success",
					Data: schema.ChatResponse{
						ConversationID:    conversation.ID,
						AssistantMessages: []model.Message{},
						RouteResult:       "human_locked_no_ai",
						Mode:              "human",
						CustomerMsgID:     testCustomerMsgID,
					},
				})
				return
			}
		}

		if conversation.IsAiReplyEnabled && aiAutoReply && conversation.LastHumanReplyAt != nil {
			sinceLastReply := time.Since(*conversation.LastHumanReplyAt)
			if sinceLastReply < aiTimeoutDur {
				log.Printf("[ChatTest] 客户%d 会话%d 人工接管，顾问%ds内已回复，跳过AI（客户消息已入库顾问秒看）",
					customer.ID, conversation.ID, int(sinceLastReply.Seconds()))
				now := time.Now()
				conversation.LastMessageAt = &now
				db.RQ(c).Save(&conversation)
				db.RQ(c).Model(&model.Message{}).Where("id = ?", testCustomerMsgID).Update("conversation_id", conversation.ID)

				c.JSON(http.StatusOK, schema.Response{
					Code:    0,
					Message: "success",
					Data: schema.ChatResponse{
						ConversationID:    conversation.ID,
						AssistantMessages: []model.Message{},
						RouteResult:       "human_skip_ai",
						Mode:              "human",
						CustomerMsgID:     testCustomerMsgID,
					},
				})
				return
			}
		}
		// 顾问未回复或超时，AI自动回复
		log.Printf("[ChatTest] 客户%d 会话%d 人工接管，AI自动回复(配置=%v timeout=%ds)",
			customer.ID, conversation.ID, aiAutoReply, aiTimeout)
	}

	// ---- 硬拦截：留资检测（手机号校验+分配顾问） ----
	// 放在策略引擎之前，确保手机号分配逻辑不受策略路由影响
	testLeadResult := 0
	if customer.JourneyStage != model.JourneyLeadCaptured && customer.JourneyStage != model.JourneyArrived &&
		customer.JourneyStage != model.JourneyOrdered && customer.JourneyStage != model.JourneyDelivered {
		testLeadResult = chatflow.DetectLeadCapture(mergedContent, &customer)
		if testLeadResult != 0 {
			log.Printf("[留资检测-硬拦截-测试接口] 客户 %d 已留资，自动分配顾问", customer.ID)
			if testLeadResult > 0 {
				log.Printf("[留资检测-OneID-测试接口] 前端需切换customer_id → %d", testLeadResult)
			}
			if err := db.RQ(c).First(&customer, customer.ID).Error; err == nil {
				log.Printf("[留资检测-硬拦截-测试接口] 客户 %d 重新加载成功, journey_stage=%s", customer.ID, customer.JourneyStage)
			}
			// 硬编码：留资后设置1轮引导式反问，允许下一轮AI回复含反问句
			db.RQ(c).Model(&model.Conversation{}).Where("id = ?", conversation.ID).Updates(map[string]interface{}{
				"guided_remaining_rounds": 1,
				"guided_disabled":         false,
			})
			conversation.GuidedRemainingRounds = 1
			conversation.GuidedDisabled = false
		}
	}

	// ---- 调用策略引擎（用合并后的内容推理） ----
	// 注意：与旧版不同，现在使用会话的真实状态而非空状态
	tVector := customer.BuildBaseTVector() // 修复：策略推理输入必须是基准向量，不是可能已叠加过的持久化值
	state := conversation.GetState()
	customerTags := customer.GetTags()

	strategyInput := strategy.StrategyInput{
		TVector:        tVector,
		State:          state,
		CustomerInput:  mergedContent,
		CustomerTags:   customerTags,
		CustomerID:     customer.ID,
		ConversationID: conversation.ID,
		CanPromote:     customer.CanPromote(),
		JourneyStage:   customer.JourneyStage,
		TenantID:       customer.TenantID, // M1租户隔离修复：模板/卖点召回按此过滤
		// 三级包架构：归属顾问的部门继承链（未指派=C端纯租户语境，只见行业+企业层）
		DeptIDs: service.DeptChainForUser(conversation.AssignedUserID),
	}
	strategyOutput := strategy.DefaultEngine.Infer(strategyInput)

	// ---- 生成AI回复 ----
	// 与旧版不同：现在传入真实 conversationID，AI可以获取历史对话上下文
	aiReply := flow.DefaultEngine.OrchestrateReply(&customer, conversation.ID, mergedContent, &strategyOutput, service.DeptChainForUser(conversation.AssignedUserID))

	// 模拟真人回复延迟：打字(40字/分钟) + 线下偏移
	// 到店倾向客户：去掉线下偏移，顾问必须快速响应
	isStoreVisit := strategy.IsStoreVisitIntent(mergedContent) && !chatflow.IsLeadCaptured(&customer) // 到店意图且未留资才去除线下偏移
	log.Printf("[ChatTest] 客户%d 到店倾向检测: %v, 合并内容: %q", customer.ID, isStoreVisit, service.MaskPhoneInText(mergedContent))
	humanlikeDelay := service.CalcHumanlikeDelay(tenantID, aiReply, mergeWaitDuration, mergeCount, isStoreVisit)

	// 胡搅蛮缠：总非车话题>10且最近未恢复→回复速度降到3分钟一次
	hjTotalOffTopic := chatflow.CountTotalOffTopic(customer.ID)
	hjOnTopic := chatflow.CountConsecutiveOnTopic(customer.ID)
	if hjTotalOffTopic > 10 && hjOnTopic < 3 {
		minDelay := 180 * time.Second
		if humanlikeDelay < minDelay {
			log.Printf("[胡搅蛮缠-降速-测试接口] 客户%d 非车%d>10, 延迟从%.1fs提升到%.1fs",
				customer.ID, hjTotalOffTopic, humanlikeDelay.Seconds(), minDelay.Seconds())
			humanlikeDelay = minDelay
		}
	}

	// 修复：总回复时长2分钟硬顶兜底
	// 已用时间 = 合并等待 + AI调用，剩余预算 = 120秒 - 已用时间
	// 如果humanlikeDelay超出剩余预算，截断到剩余预算（最少0秒，直接跳过延迟）
	maxTotalDelay := 120 * time.Second
	elapsed := time.Since(requestStart)
	remainingBudget := maxTotalDelay - elapsed
	if humanlikeDelay > remainingBudget {
		log.Printf("[ChatTest] 客户%d 总延迟硬顶触发: 已用%.1fs + 模拟延迟%.1fs > 2分钟, 截断到%.1fs",
			customer.ID, elapsed.Seconds(), humanlikeDelay.Seconds(), remainingBudget.Seconds())
		humanlikeDelay = remainingBudget
	}
	if humanlikeDelay < 0 {
		humanlikeDelay = 0
	}

	log.Printf("[ChatTest] 客户%d 模拟延迟: %.1fs, 已用: %.1fs, 总计: %.1fs, 开始sleep...", customer.ID, humanlikeDelay.Seconds(), elapsed.Seconds(), (elapsed + humanlikeDelay).Seconds())
	// 修复问题2：instant模式跳过CancellableSleep，秒回无延迟
	replyDelayMode := service.DefaultSystemConfigService.GetString("reply_delay_mode", "normal")
	if replyDelayMode == "instant" {
		log.Printf("[ChatTest] 客户%d instant模式，跳过延迟直接回复", customer.ID)
	} else {
		// 修复问题3：用可取消延迟替代time.Sleep，支持"立即回复"按钮
		chatflow.CancellableSleep(customer.ID, humanlikeDelay)
	}
	log.Printf("[ChatTest] 客户%d 延迟结束，返回回复", customer.ID)

	// 回复写入队列缓存，唤醒所有等待的请求
	service.DefaultMessageQueueService.SetReply(tenantID, customer.ID, aiReply)

	// ---- 修复问题4：更新之前存的客户消息 ----
	// 原来客户消息在EnqueueAndWait之后才存DB，F5刷新会丢失
	// 现在已提前存DB，这里只需更新conversation_id和合并后的内容
	if mergedContent != req.Content {
		// 合并窗口导致内容变化，更新DB中的客户消息content
		db.RQ(c).Model(&model.Message{}).Where("id = ?", testCustomerMsgID).Updates(map[string]interface{}{
			"conversation_id": conversation.ID,
			"content":         mergedContent,
			"emotion":         strategy.DetectEmotion(mergedContent),
		})
	} else {
		// 内容没变，只更新conversation_id（之前暂填0）
		db.RQ(c).Model(&model.Message{}).Where("id = ?", testCustomerMsgID).Update("conversation_id", conversation.ID)
	}

	// ---- 保存AI回复消息 ----
	aiMsg := model.Message{
		ConversationID: conversation.ID,
		CustomerID:     customer.ID,
		SenderType:     "ai",
		Content:        aiReply,
		MessageType:    "text",
		AnchorType:     strategyOutput.FinalAnchor,
		TemplateID:     strategyOutput.TemplateID,
		RouteResult:    strategyOutput.RouteResult,
		IntentScore:    tVector[0],
		CreatedAt:      time.Now(),
	}
	db.RQ(c).Create(&aiMsg)
	publishConversationMsg(tenantID, customer.ID, strategyOutput.RouteResult, state.Emotion)

	// ---- 更新会话状态 ----
	conversation.LastMessageAt = &aiMsg.CreatedAt
	conversation.LastTid = strategyOutput.TemplateID
	conversation.LastAnchorType = strategyOutput.FinalAnchor
	conversation.Emotion = strategy.DetectEmotion(mergedContent)

	updatedState := chatflow.UpdateConversationState(&conversation, &strategyOutput, &customer, mergedContent)
	conversation.SaveState(updatedState)
	db.RQ(c).Save(&conversation)

	// ---- 更新客户画像（意向分反哺） ----
	newIntent := tVector[0] + strategyOutput.IntentDelta
	if newIntent < 0 {
		newIntent = 0
	}
	if newIntent > 1 {
		newIntent = 1
	}
	customer.IntentScore = newIntent
	newTVector := customer.GetTVector()
	newTVector[0] = newIntent
	customer.SaveTVector(newTVector)
	db.RQ(c).Save(&customer)

	// ---- 自动打标（测试接口也集成，方便验证打标效果） ----
	// 修复问题7：不再仅限RouteAI路径，所有路由结果都打标
	newTags := []string{}
	autoTags, tagErr := service.DefaultTagService.AutoTagFromText(customer.ID, mergedContent)
	if tagErr == nil && len(autoTags) > 0 {
		newTags = autoTags
		log.Printf("[测试接口] 客户%d自动打标: %v", customer.ID, autoTags)

		// 打标后应用标签权重到T向量，并保存
		var updatedCustomer model.Customer
		if err := db.RQ(c).First(&updatedCustomer, customer.ID).Error; err == nil {
			_ = service.DefaultTagService.ApplyTagWeightsToTVector(&updatedCustomer, updatedCustomer.BuildBaseTVector())
			db.RQ(c).Model(&updatedCustomer).Update("t_vector", updatedCustomer.TVectorJSON)
		}
	}

	// ---- 返回结果 ----
	// 欢迎词由 /chat/welcome 接口独立返回，此处不再附带 earlier_messages
	RespOK(c, "success", gin.H{
		"conversation_id":     conversation.ID,        // 会话ID（供后续请求复用）
		"customer_input":      req.Content,            // 原始输入（调试对照）
		"merged_content":      mergedContent,          // 合并后的完整内容（调试用）
		"ai_reply":            aiReply,                // AI生成的回复
		"assistant_messages":  []model.Message{aiMsg}, // 修复：返回带真实DB ID的消息列表，前端用此去重
		"new_tags":            newTags,                // 本轮自动打标的标签
		"is_new_conversation": isNewConversation,      // 是否冷启动
		"customer_msg_id":     testCustomerMsgID,      // 修复问题4：客户消息DB ID，供前端替换temp ID
		"anchor": gin.H{
			"type":              strategyOutput.FinalAnchor,
			"name":              strategy.GetAnchorName(strategyOutput.FinalAnchor),
			"confidence":        strategyOutput.AnchorConfidence,
			"original_anchor":   strategyOutput.OriginalAnchor, // softmax原始锚（降级前）
			"soft_downgrade":    strategyOutput.SoftDowngrade,
			"stage_before_lock": strategyOutput.StageBeforeLock, // 阶段锁降级前的锚类型
			"stage_ceiling_agg": strategyOutput.StageCeilingAgg, // 当前阶段允许的agg上限
			"stage_downgraded":  strategyOutput.StageDowngraded, // 阶段锁是否触发了降级
		},
		"template": gin.H{
			"id":   strategyOutput.TemplateID,
			"name": strategyOutput.TemplateName,
		},
		"exchange": gin.H{
			"flag": strategyOutput.ExchangeFlag,
			"type": strategyOutput.ExchangeType,
		},
		"route": gin.H{
			"result": strategyOutput.RouteResult,
			"reason": strategyOutput.RouteReason,
		},
		"urgency_level":      strategyOutput.UrgencyLevel,
		"intent_delta":       strategyOutput.IntentDelta,
		"is_ai_mode":         (config.GlobalConfig.AI.MockMode == false && service.DefaultSystemConfigService.GetBool("mock_mode", false)) && (ai.DefaultClient.APIKey != "" || (ai.SiliconFlowDefaultClient != nil && ai.SiliconFlowDefaultClient.Enabled)),
		"merged_customer_id": testLeadResult, // OneID合并：>0表示前端需切换customer_id
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
