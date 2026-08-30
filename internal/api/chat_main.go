// 对话核心API（主链路）：消息接收、三层分流、合并队列与AI回复生成。
package api

// 对话核心API：C端客户与B端销售共用的交互入口，链路为 客户发消息→策略中心7步推理→AI生成回复。
// 含会话竞态保护、三层分流(硬边界/到店快速通道/简单消息)、合并队列、延迟清零、留资检测与OneID合并。

import (
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
func Chat(c *gin.Context) {
	// 修复：记录请求开始时间，用于总延迟2分钟硬顶兜底
	requestStart := time.Now()

	// 生效租户（中间件链保证存在）：消息合并队列按 tenantID:customerID 隔离
	tenantID := middleware.EffectiveTenantID(c)

	trace := middleware.GetTraceID(c) // P1-3：全链路 trace

	var req schema.ChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("[对话][trace=%s] 参数绑定失败 tenant=%d: %v", trace, tenantID, err)
		c.JSON(http.StatusBadRequest, schema.Response{
			Code:    400,
			Message: "参数错误: " + err.Error(),
			Data:    nil,
		})
		return
	}

	// 1. 查询客户（加入租户隔离）
	var customer model.Customer
	result := db.RQ(c).Scopes(db.T(c)).First(&customer, req.CustomerID)
	if result.Error != nil {
		c.JSON(http.StatusNotFound, schema.Response{
			Code:    404,
			Message: "客户不存在",
			Data:    nil,
		})
		return
	}

	// C3：懒下发访客密钥（历史客户可能没有），供客户端持久化并在 history/welcome 携带
	if customer.VisitorKey == "" {
		customer.VisitorKey = model.GenerateVisitorKey()
		db.RQ(c).Model(&customer).Update("visitor_key", customer.VisitorKey)
	}

	// 2. 获取或创建会话（竞态保护：先查再建，防止并发请求重复创建）
	//
	// 原Bug：客户连发多条消息时，多个并发请求都看到 conversation_id=0，
	// 各自创建新会话，导致同一客户出现多个活跃会话。
	// 修复：用客户级互斥锁串行化"查找或创建"，先查已有活跃会话复用，
	// 没有才创建新会话+冷启动秒回。只有持锁的请求执行创建，
	// 后续请求直接复用已创建的会话。
	var conversation model.Conversation

	if req.ConversationID > 0 {
		// 前端传了会话ID，直接查找已有会话（无需竞态保护）
		result := db.RQ(c).First(&conversation, req.ConversationID)
		if result.Error != nil {
			c.JSON(http.StatusNotFound, schema.Response{
				Code:    404,
				Message: "会话不存在",
				Data:    nil,
			})
			return
		}
	} else {
		// 前端没传会话ID → 需要查找或创建，加锁防止并发竞态
		convMu := chatflow.GetConversationMutex(customer.ID)
		convMu.Lock()

		// 先查该客户是否已有活跃会话（复用，不重复创建，加入租户隔离）
		result := db.RQ(c).Scopes(db.T(c)).Where("customer_id = ? AND status = ?", customer.ID, "active").
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
			log.Printf("[对话] 冷启动: 创建新会话 %d, 客户 %d", conversation.ID, customer.ID)

			// 欢迎词已移至独立的 /chat/welcome 接口负责秒回
			// 此处不再创建欢迎消息，避免欢迎词跟AI回复绑在一起等延迟

			// 首次对话自动推进线索状态：→ AI建联
			// 新客户首次对话，无论之前是什么状态，都标记为AI建联
			if customer.JourneyStage == "" || customer.JourneyStage == model.JourneyAIConnected {
				customer.JourneyStage = model.JourneyAIConnected
				db.RQ(c).Model(&customer).Update("journey_stage", model.JourneyAIConnected)
				log.Printf("[对话] 客户%d自动推进到AI建联状态", customer.ID)
			}

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
			log.Printf("[对话] 复用已有会话 %d, 客户 %d", conversation.ID, customer.ID)
		}

		convMu.Unlock()
	}

	// 3. 检查是否人工超时接管
	humanTimeout := chatflow.CheckHumanTimeout(&conversation)
	if humanTimeout {
		log.Printf("[对话] 人工超时，AI接管会话: %d", conversation.ID)
		conversation.Mode = "ai"
		conversation.IsHumanLocked = false
	}

	// ---- 修复问题1：相似消息合并为一次回答 ----
	// 根因：客户发"我要试驾"，再发"怎么试驾"，语义相近但后端每条都走AI生成回复，
	// 导致顾问端出现多条重复/近似回复气泡
	// 方案：入队前查客户最近2条消息，用2-gram关键词重叠度>50%判断是否"相似问题"
	// 相似则返回merged状态，前端不渲染新气泡，等主请求回复即可
	var recentCustomerMsgs []model.Message
	if err := db.RQ(c).Where("customer_id = ? AND sender_type = ?", customer.ID, "customer").
		Order("id DESC").Limit(2).Find(&recentCustomerMsgs).Error; err == nil {
		currentKeywords := chatflow.ExtractKeywords(req.Content)
		if len(currentKeywords) > 0 {
			for _, pastMsg := range recentCustomerMsgs {
				pastKeywords := chatflow.ExtractKeywords(pastMsg.Content)
				if len(pastKeywords) == 0 {
					continue
				}
				// 计算关键词重叠度
				overlapCount := 0
				for _, w := range currentKeywords {
					for _, pw := range pastKeywords {
						if w == pw {
							overlapCount++
							break
						}
					}
				}
				overlapRate := float64(overlapCount) / float64(len(currentKeywords))
				if overlapRate > 0.5 {
					log.Printf("[相似消息合并] 客户%d 当前:%q 与历史:%q 重叠度%.0f%%, 合并为一次回答",
						customer.ID, req.Content, pastMsg.Content, overlapRate*100)

					// 修复：之前这里直接return，客户这条消息完全没落库，
					// 顾问端翻聊天记录会发现客户明明说了话但历史里没有，容易误判。
					// 现在先把这条消息存下来（标记为merged_suppressed，不再触发新的AI回复），
					// 再返回merged状态，保证"客户发的每条消息都能在历史里查到"。
					suppressedMsg := model.Message{
						ConversationID: conversation.ID,
						CustomerID:     customer.ID,
						SenderType:     "customer",
						Content:        req.Content,
						MessageType:    "text",
						RouteResult:    "merged_suppressed",
						Emotion:        strategy.DetectEmotion(req.Content),
						CreatedAt:      time.Now(),
					}
					db.RQ(c).Create(&suppressedMsg)

					c.JSON(http.StatusOK, schema.Response{
						Code:    0,
						Message: "success",
						Data: schema.ChatResponse{
							ConversationID: conversation.ID,
							Merged:         true,
							MergedNote:     "相似消息已合并处理",
							CustomerMsgID:  suppressedMsg.ID,
						},
					})
					return
				}
			}
		}
	}

	// 4. 保存客户消息
	now := time.Now()
	customerMsg := model.Message{
		ConversationID: conversation.ID,
		CustomerID:     customer.ID,
		SenderType:     "customer",
		Content:        req.Content,
		MessageType:    "text",
		Emotion:        strategy.DetectEmotion(req.Content),
		CreatedAt:      now,
	}
	db.RQ(c).Create(&customerMsg)
	// P1-2 实时推送：客户新消息通知本租户顾问端（前端即时拉取，轮询兜底）
	notifyWS(tenantID, customer.ID, conversation.ID, "customer")

	// 更新会话最后消息时间
	conversation.LastMessageAt = &now

	// 修复：打标是持续行为，客户冷启动第一条消息落库后就要立刻打标，
	// 不能等到策略推理跑完、且只在routeResult==AI时才打。之前的问题：
	// 1) 待人工接管/已转人工/养鱼分支完全不打标；
	// 2) 客户被锁定给顾问接手后（下面的human分支直接return），更是彻底停止打标。
	// 现在挪到这里、放在human锁定判断之前，保证"只要客户说话就持续打标"，
	// 覆盖冷启动、AI模式、待接管、已转人工、顾问接手后全部场景。
	newTags := []string{}
	if autoTags, tagErr := service.DefaultTagService.AutoTagFromText(customer.ID, req.Content); tagErr == nil && len(autoTags) > 0 {
		newTags = autoTags
		log.Printf("[对话] 客户%d自动打标(持续): %v", customer.ID, autoTags)
		var updatedCustomerForTag model.Customer
		if err := db.RQ(c).First(&updatedCustomerForTag, customer.ID).Error; err == nil {
			_ = service.DefaultTagService.ApplyTagWeightsToTVector(&updatedCustomerForTag, updatedCustomerForTag.BuildBaseTVector())
			db.RQ(c).Model(&updatedCustomerForTag).Update("t_vector", updatedCustomerForTag.TVectorJSON)
			customer = updatedCustomerForTag
		}
	}

	// 5. 如果是人工模式，直接返回（等人工回复）
	if conversation.Mode == "human" && conversation.IsHumanLocked {
		c.JSON(http.StatusOK, schema.Response{
			Code:    0,
			Message: "success",
			Data: schema.ChatResponse{
				ConversationID: conversation.ID,
				Message: gin.H{
					"sender_type": "system",
					"content":     "已收到您的消息，销售顾问正在赶来的路上，请稍候~",
				},
				RouteResult: "human",
				Mode:        "human",
			},
		})
		return
	}

	// ---- 修复：入队列前三层分流 ----
	// 优先级：硬边界拦截(0延迟) > 到店倾向快速通道(10-15秒) > 简单消息(8秒) > 正常合并队列
	// 根因：用户发"高数题"等了9分钟才收到回复；"试驾"意向被合并吞掉
	// 原来所有消息都先进合并队列(25s)，再到GenerateAIReply里才查硬边界和到店倾向——太晚了

	// 第一层：硬边界拦截（0延迟，不走AI，不进队列）
	// 在入口用原始消息检测，不依赖合并后的内容
	if service.IsOffTopic(req.Content) {
		reply := service.GetOffTopicReply(req.Content)
		log.Printf("[硬边界][trace=%s] 客户%d 拦截无关话题(入队前): %q → %q", trace, customer.ID, service.MaskPhoneInText(req.Content), service.MaskPhoneInText(reply))
		// 保存AI拦截回复消息到DB
		offTopicMsg := model.Message{
			ConversationID: conversation.ID,
			CustomerID:     customer.ID,
			SenderType:     "ai",
			Content:        reply,
			MessageType:    "text",
			RouteResult:    "offtopic_hardbound",
			CreatedAt:      time.Now(),
		}
		db.RQ(c).Create(&offTopicMsg)
		// 唤醒队列中可能等待的其他请求
		service.DefaultMessageQueueService.SetReply(tenantID, customer.ID, reply)
		RespOK(c, "success", gin.H{
			"conversation_id": conversation.ID,
			"ai_reply":        reply,
			"route_result":    "offtopic_hardbound",
		})
		return
	}

	// 第二层：到店倾向快速通道（不进合并队列，直接快速回复）
	// 修复：客户说"试驾""到店"等高意向信号，必须快速接住，不能等25秒合并窗口
	//
	// 分支逻辑（硬编码，不依赖AI prompt）：
	// A. 客户已留资（journey_stage >= lead_captured）→ 跳过本层，走正常流程（AI或人工接管）
	// B. 当前消息包含手机号 → 已留资线索：标记留资+分配顾问（基于手机号校验）+确认回复
	// C. 当前消息无手机号 → 未留资线索：关闭引导+两段式追问，推迟分配顾问到手机号回复时
	if strategy.IsStoreVisitIntent(req.Content) {
		// ---- 前置拦截：已留资客户不再走到店快速通道 ----
		// 修复根因：客户之前已留资（如第二轮回复给了手机号），再次表达到店意向时
		// 不应再问"留个手机号"，应直接走正常AI对话或人工接管
		if customer.JourneyStage == model.JourneyLeadCaptured ||
			customer.JourneyStage == model.JourneyArrived ||
			customer.JourneyStage == model.JourneyOrdered ||
			customer.JourneyStage == model.JourneyDelivered {
			log.Printf("[到店倾向] 客户%d 已是%s阶段，跳过到店快速通道，走正常流程",
				customer.ID, customer.JourneyStage)
			// 跳出本层，继续走下面的合并队列正常流程
			goto skipStoreVisitFast
		}

		// ---- 留资前置检测：当前消息是否包含手机号 ----
		// 修复根因：客户一轮回复"小张，13333333333，2个人，周六下午两点，体验越野"
		// 既有到店关键词又有手机号，之前走两段式还问"留个手机号"，完全不合理
		phoneRegex := regexp.MustCompile(`1[3-9]\d{9}`)
		phoneMatch := phoneRegex.FindString(req.Content)

		if phoneMatch != "" {
			// ====== 分支B：已留资线索（硬编码） ======
			// 客户消息含手机号 → 已留资 → 人工接管路径
			// 1. 标记留资 + 分配顾问（+ OneID合并）
			// 2. 生成线索记录（给顾问看）
			// 3. 通知顾问
			// 4. 硬编码确认回复（不走AI）
			log.Printf("[到店倾向-已留资线索] 客户%d 消息含手机号%s，走已留资硬编码路径",
				customer.ID, phoneMatch)

			// 0. OneID合并：手机号匹配到老客户时，迁移所有数据
			mergedTargetID := chatflow.MergeCustomerByPhone(&customer, phoneMatch)
			if mergedTargetID > 0 {
				// 合并完成，重新加载老客户数据，会话也跟着迁过去了
				db.RQ(c).First(&customer, mergedTargetID)
				// 重新加载会话（已被迁移到老客户下）
				db.RQ(c).Where("customer_id = ? AND status = ?", customer.ID, "active").
					Order("updated_at DESC").First(&conversation)
				log.Printf("[到店倾向-已留资-OneID] 合并到老客户%d，前端需切换customer_id", mergedTargetID)
			}

			// 1. 标记留资 + 分配顾问
			leadUpdates := map[string]interface{}{}
			if customer.Phone == "" {
				leadUpdates["phone"] = phoneMatch
			}
			leadUpdates["journey_stage"] = model.JourneyLeadCaptured
			leadUpdates["assignment_reason"] = "lead_captured"
			// 修复问题2：到店倾向已留资分支也要用轮询选顾问，不能硬编码1
			// 根因：硬编码assigned_user_id=1是admin，张伟是ID=2，所以销售端看不到客户
			if customer.AssignedUserID == 0 {
				var salesUsers []model.User
				// 修复Bug1（2026-08-22）：角色改用 model.RoleSales 常量。
				// 根因：组织迁移把 sales 改名为 user 后，硬编码"sales"永远查不到 → 永远走兜底分支
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
					// 修复Bug1：兜底值必须显式 uint——int 字面量入 map 后被下方 v.(uint) 断言 panic
					leadUpdates["assigned_user_id"] = uint(2) // 兜底：无销售用户时默认分配2号（张伟）
				}
			}
			if len(leadUpdates) > 0 {
				db.RQ(c).Model(&customer).Updates(leadUpdates)
				// 同步内存对象（修复Bug1：断言全部改安全形式，杜绝留资链路 panic 中断）
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
						log.Printf("[留资-告警] assigned_user_id 类型异常(%T)，保持原值: %v", v, customer.AssignedUserID)
					}
				}
			}
			log.Printf("[到店倾向-已留资线索][留资检测] 客户%d 留资成功: phone=%s, stage=lead_captured, assigned=%d",
				customer.ID, service.MaskPhone(phoneMatch), customer.AssignedUserID)

			// P3：到店分支留资事件上行（与 DetectLeadCapture 主路径埋点对齐）
			if err := mq.Publish(context.Background(), mq.TopicUserEvent, tenantID,
				fmt.Sprintf("c:%d", customer.ID), "lead_captured",
				mq.UserEvent{EventType: "behavior", EventName: "lead_captured", AnchorType: "phone",
					Attributes: map[string]any{"customer_id": customer.ID, "path": "store_visit_branch"},
					OccurredAt: time.Now()}); err != nil {
				log.Printf("[MQ] lead_captured(到店分支) 发布失败: %v", err)
			}

			// 2. 生成线索记录（FollowUp，顾问端可见）
			followUp := model.FollowUp{
				CustomerID:     customer.ID,
				ConversationID: conversation.ID,
				UserID:         customer.AssignedUserID, // 归属顾问
				Type:           "ai_triggered",          // AI触发生成
				Method:         "store",                 // 到店渠道
				Content:        fmt.Sprintf("客户到店意向+已留资，手机号:%s，原始消息:%s", phoneMatch, req.Content),
				Result:         "lead_captured", // 已留资线索
			}
			db.RQ(c).Create(&followUp)
			log.Printf("[到店倾向-已留资线索] 客户%d 线索已生成(FollowUp ID=%d)，分配顾问%d",
				customer.ID, followUp.ID, customer.AssignedUserID)

			// 3. 通知顾问（当前简化为日志，后续可接WebSocket/邮件/飞书）
			log.Printf("[通知顾问] 顾问%d 有新的已留资到店线索：客户%d，手机号%s",
				customer.AssignedUserID, customer.ID, phoneMatch)

			// 4. 标记待人工接管，但AI先发一条引导式反问
			// 硬编码：留资后第一条回复是固定引导句，不走AI
			// 话术核心字段：用车需求、关注点、旧车置换、用车时间
			conversation.PendingHandoff = true
			conversation.GuidedRemainingRounds = 0
			conversation.GuidedDisabled = false
			now := time.Now()
			conversation.HandoffNotifiedAt = &now
			db.RQ(c).Save(&conversation)

			// 5. 固定引导式反问（硬编码，不走AI避免延迟）
			// 已留资客户第一条回复浓缩成固定引导句，不再发"我先帮您约上时间"式确认语
			firstDelay := service.GetStoreVisitFirstDelay()
			chatflow.CancellableSleep(customer.ID, firstDelay)

			leadCapturedReplies := []string{
				"好呀，要不您再详细跟我说说您的用车需求，关注哪些方面，有没有老车要置换，大概什么时候想用车吧",
				"好嘞，您方便详细聊聊您的用车需求吗？关注什么方面比较多？有没有旧车考虑置换，大概啥时候想用车呢",
				"好的，您要不跟我说说您的用车场景和需求？关注哪些地方比较多，有没有老车要换，大概打算啥时候用车",
			}
			leadCapturedReply := leadCapturedReplies[rand.Intn(len(leadCapturedReplies))]

			leadMsg := model.Message{
				ConversationID: conversation.ID,
				CustomerID:     customer.ID,
				SenderType:     "ai",
				Content:        leadCapturedReply,
				MessageType:    "text",
				RouteResult:    "lead_captured_confirmed", // 已留资确认路由标记
				CreatedAt:      time.Now(),
			}
			db.RQ(c).Create(&leadMsg)

			RespOK(c, "success", gin.H{
				"conversation_id":    conversation.ID,
				"ai_reply":           leadCapturedReply,
				"route_result":       "lead_captured_confirmed",
				"merged_customer_id": mergedTargetID, // OneID合并：>0表示前端需切换customer_id
			})
			return
		}

		// ====== 分支C：未留资线索（关闭引导+两段式AI快速回复，推迟分配顾问） ======
		// 客户表达到店意向但没给手机号 → 关闭引导+两段式AI快速回复，不分配顾问
		// 顾问分配推迟到客户回复手机号时，由 DetectLeadCapture 根据手机号校验决定：
		//   - 手机号已存在 → 合并到原客户+原顾问
		//   - 新手机号 → 轮询分配
		firstReply := service.GetStoreVisitFirstReply(req.Content)
		firstDelay := service.GetStoreVisitFirstDelay() // 10-15秒

		log.Printf("[到店倾向-未留资线索] 客户%d 关闭引导+两段式回复, 推迟分配顾问", customer.ID)

		// 关闭引导式反问
		conversation.GuidedDisabled = true
		db.RQ(c).Model(&conversation).Update("guided_disabled", true)

		// 第一段AI快速回复（接住意向）
		chatflow.CancellableSleep(customer.ID, firstDelay)

		storeVisitMsg := model.Message{
			ConversationID: conversation.ID,
			CustomerID:     customer.ID,
			SenderType:     "ai",
			Content:        firstReply,
			MessageType:    "text",
			RouteResult:    "store_visit_fast",
			CreatedAt:      time.Now(),
		}
		db.RQ(c).Create(&storeVisitMsg)

		// 第二段追问（异步，25-45秒后发出，收集预约信息）
		// 修复Bug2（2026-08-22）：goroutine 内禁用 db.RQ(c)——handler 返回后 request ctx
		// 被 net/http 取消，GORM 写入静默失败，第二段追问永远不落库。
		// 改为：入 goroutine 前捕获租户ID，内部用脱离请求生命周期的 context 构建会话
		secondReply := service.GetStoreVisitSecondReply(req.Content)
		goroutineTenantID := tenantID
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
				log.Printf("[到店倾向-告警] 客户%d 第二段追问落库失败(已重试放弃): %v", cid, err)
				return
			}
			log.Printf("[到店倾向-未留资线索] 客户%d 第二段追问已发送", cid)
		}(customer.ID, conversation.ID, secondReply, goroutineTenantID)

		RespOK(c, "success", gin.H{
			"conversation_id": conversation.ID,
			"ai_reply":        firstReply,
			"route_result":    "store_visit_fast",
		})
		return
	}
skipStoreVisitFast:

	// 6. 消息入队 + 合并窗口等待
	// 第一个拿到处理权的请求负责生成回复
	// 后续请求挂起等待，回复生成后一起返回
	// 修复：新增isSimple返回值，简单消息直接走快速回复通道
	mergedContent, shouldProcess, _, mergeWaitDuration, isSimple, mergeCount := service.DefaultMessageQueueService.EnqueueAndWait(tenantID, customer.ID, req.Content)

	// 修复：简单消息（"在吗"/"那我撤了"等）直接走快速回复，20-45秒随机延迟
	if isSimple {
		// H7修复(2026-08-26)：实例内同客户简单消息串行，处理完释放锁
		defer service.DefaultMessageQueueService.SimpleMessageDone(tenantID, customer.ID)
		// 修复问题2：instant模式下简单消息跳过延迟直接回复
		replyDelayModeSimple := service.DefaultSystemConfigService.GetString("reply_delay_mode", "normal")
		if replyDelayModeSimple != "instant" {
			// 修复：简单消息不能秒回，加20-45秒随机延迟，模拟真人看到消息后思考再回复\n			simpleDelay := service.GetSimpleReplyDelay()\n			chatflow.CancellableSleep(customer.ID, simpleDelay)
		}

		simpleReply := service.GetSimpleReply(req.Content)

		// 保存简单回复消息到DB
		simpleMsg := model.Message{
			ConversationID: conversation.ID,
			CustomerID:     customer.ID,
			SenderType:     "ai",
			Content:        simpleReply,
			MessageType:    "text",
			RouteResult:    "simple_fast",
			CreatedAt:      time.Now(),
		}
		db.RQ(c).Create(&simpleMsg)

		// 修复：简单消息路径也需要做留资检测（虽然简单消息一般不会包含手机号，但防漏）
		if customer.JourneyStage != model.JourneyLeadCaptured && customer.JourneyStage != model.JourneyArrived &&
			customer.JourneyStage != model.JourneyOrdered && customer.JourneyStage != model.JourneyDelivered {
			leadResult := chatflow.DetectLeadCapture(req.Content, &customer)
			if leadResult != 0 {
				log.Printf("[留资检测-简单消息] 客户 %d 已留资", customer.ID)
				// OneID合并：前端需要切换到老客户ID
				if leadResult > 0 {
					log.Printf("[留资检测-简单消息-OneID] 前端需切换customer_id: %d → %d", req.CustomerID, leadResult)
				}
			}
		}

		RespOK(c, "success", gin.H{
			"conversation_id": conversation.ID,
			"ai_reply":        simpleReply,
			"message":         simpleMsg,
		})
		return
	}

	if !shouldProcess {
		// Bug 1 修复：合并请求只返回合并状态，不返回完整AI回复
		//
		// 原Bug：三条并发请求都拿到了同一段 ai_reply，前端按每条响应渲染
		// 就会出现三条一模一样的消息气泡。
		// 修复：merged 请求不再返回 ai_reply 和策略详情，只返回合并标记。
		// 前端收到 merged=true 时，不渲染新消息气泡，等主请求的回复即可。
		// conversation_id 仍然返回，供前端后续请求使用。
		c.JSON(http.StatusOK, schema.Response{
			Code:    0,
			Message: "success",
			Data: schema.ChatResponse{
				ConversationID: conversation.ID,
				Merged:         true,
				MergedNote:     "本条消息已与先前的消息合并处理，回复将在主请求中返回",
			},
		})
		return
	}

	// ---- 以下是拿到处理权的请求，用合并后的内容走完整流程
	// 注意：策略引擎基于合并后的内容推理

	// ---- 硬拦截：留资检测（手机号校验+分配顾问） ----
	// 放在策略引擎之前，确保手机号分配逻辑不受策略路由影响
	// 规则：同一手机号→合并原客户+原顾问；新手机号→轮询分配
	leadCapturedResult := 0
	if customer.JourneyStage != model.JourneyLeadCaptured && customer.JourneyStage != model.JourneyArrived &&
		customer.JourneyStage != model.JourneyOrdered && customer.JourneyStage != model.JourneyDelivered {
		leadCapturedResult = chatflow.DetectLeadCapture(mergedContent, &customer)
		if leadCapturedResult != 0 {
			log.Printf("[留资检测-硬拦截] 客户 %d 已留资，自动分配顾问", customer.ID)
			if leadCapturedResult > 0 {
				log.Printf("[留资检测-OneID] 前端需切换customer_id → %d", leadCapturedResult)
			}
			// 重新加载客户，确保journey_stage等字段同步到内存
			if err := db.RQ(c).First(&customer, customer.ID).Error; err == nil {
				log.Printf("[留资检测-硬拦截] 客户 %d 重新加载成功, journey_stage=%s", customer.ID, customer.JourneyStage)
			}
			// 硬编码：留资后设置1轮引导式反问（GuidedRemainingRounds=1），下一轮AI回复允许反问句通过
			// 第7步剥离逻辑根据GuidedRemainingRounds==0才剥离，>0时反问句保留
			// 这轮反问由AI按prompt指令生成，核心字段：用车需求、关注点、旧车置换、用车时间
			db.RQ(c).Model(&model.Conversation{}).Where("id = ?", conversation.ID).Updates(map[string]interface{}{
				"guided_remaining_rounds": 1,
				"guided_disabled":         false,
			})
			conversation.GuidedRemainingRounds = 1
			conversation.GuidedDisabled = false
		}
	}

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

	// 7. 根据路由结果处理
	aiReply := ""
	routeResult := strategyOutput.RouteResult
	// 注：newTags 已经在函数前面"客户消息落库后立刻打标"那一步统一处理并赋值，
	// 这里不再重复调用 AutoTagFromText，避免同一条消息被计两次权重。

	switch routeResult {
	case strategy.RouteAI:
		if !conversation.IsAiReplyEnabled {
			// 5分钟无人回复自动重开AI（仅到店场景生效）
			aiTimedOut := conversation.PendingHandoff &&
				conversation.LastHumanReplyAt != nil &&
				time.Since(*conversation.LastHumanReplyAt) >=
					time.Duration(service.DefaultSystemConfigService.GetInt("assigned_lead_ai_timeout", 300))*time.Second
			if aiTimedOut {
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
				log.Printf("[Chat] 会话%d 顾问超时，自动重开AI回复", conversation.ID)
				aiReply = flow.DefaultEngine.OrchestrateReply(&customer, conversation.ID, mergedContent, &strategyOutput, service.DeptChainForUser(conversation.AssignedUserID))
			} else {
				aiReply = ""
				routeResult = "human_locked_no_ai"
				log.Printf("[Chat] 会话%d AI回复已关闭(IsAiReplyEnabled=false)，跳过AI", conversation.ID)
			}
		} else {
			conversation.Mode = "ai"
			aiReply = flow.DefaultEngine.OrchestrateReply(&customer, conversation.ID, mergedContent, &strategyOutput, service.DeptChainForUser(conversation.AssignedUserID))
		}

	case strategy.RoutePendingHuman:
		// AI接不住：软接管（用户侧无感知的硬切）
		// AI发退场词后关闭AI回复，不分配顾问
		// 顾问分配推迟到客户回复手机号时，由 DetectLeadCapture 根据手机号校验决定
		aiReply = "您好，我现在有点忙，您要不留个信息咱们到店谈，我顺便帮您查一下您的问题"
		conversation.Mode = "human"
		conversation.IsHumanLocked = true
		conversation.IsAiReplyEnabled = false
		db.RQ(c).Model(&conversation).Updates(map[string]interface{}{
			"mode":                "human",
			"is_human_locked":     true,
			"is_ai_reply_enabled": false,
		})
		log.Printf("[对话] 会话 %d AI接不住，已关闭AI回复等待顾问，推迟分配顾问，原因: %s", conversation.ID, strategyOutput.RouteReason)

	case strategy.RoutePrice:
		// 询价路由：客户问价格 → 引导到店试驾后出报价
		// 流程硬编码：不走AI兜底或空回复，直接走AI生成（prompt中已含询价引导规则）
		// 已留资客户：不重复问留资信息，直接引导到店试驾
		conversation.Mode = "ai"
		log.Printf("[对话] 会话%d 询价路由触发，引导到店试驾后出报价", conversation.ID)
		aiReply = flow.DefaultEngine.OrchestrateReply(&customer, conversation.ID, mergedContent, &strategyOutput, service.DeptChainForUser(conversation.AssignedUserID))

	case strategy.RouteHuman:
		// 直接转人工（硬切，用户有感知）
		// AI发退场词后关闭AI回复，不分配顾问
		// 顾问分配推迟到客户回复手机号时，由 DetectLeadCapture 根据手机号校验决定
		aiReply = "您好，我现在有点忙，您要不留个信息咱们到店谈，我顺便帮您查一下您的问题"
		conversation.Mode = "human"
		conversation.IsHumanLocked = true
		conversation.IsAiReplyEnabled = false
		db.RQ(c).Model(&conversation).Updates(map[string]interface{}{
			"mode":                "human",
			"is_human_locked":     true,
			"is_ai_reply_enabled": false,
		})
		log.Printf("[对话] 会话 %d AI接不住硬切人工，已关闭AI回复等待顾问，推迟分配顾问，原因: %s", conversation.ID, strategyOutput.RouteReason)

	case strategy.RouteFish:
		// 养鱼模式
		conversation.Mode = "fish"
		aiReply = "好的，您先考虑考虑，有任何问题随时找我~ 我会持续关注您的需求，有好消息也会及时通知您的。"
		conversation.Status = "active"
	}

	// 8. 保存AI回复消息
	var aiMsg model.Message
	if aiReply != "" {
		aiMsg = model.Message{
			ConversationID: conversation.ID,
			CustomerID:     customer.ID,
			SenderType:     "ai",
			Content:        aiReply,
			MessageType:    "text",
			AnchorType:     strategyOutput.FinalAnchor,
			TemplateID:     strategyOutput.TemplateID,
			RouteResult:    routeResult,
			IntentScore:    tVector[0],
			Emotion:        state.Emotion,
			CreatedAt:      time.Now(),
		}
		db.RQ(c).Create(&aiMsg)
		publishConversationMsg(tenantID, customer.ID, routeResult, state.Emotion)
		// P1-2 实时推送：AI 新回复通知客户端与顾问端（前端即时拉取，轮询兜底）
		notifyWS(tenantID, customer.ID, conversation.ID, "ai")
	}

	// 9. 更新会话状态
	// 注意：Attempts在chatflow.UpdateConversationState()里已经累加过了，这里不要再重复加
	updatedState := chatflow.UpdateConversationState(&conversation, &strategyOutput, &customer, mergedContent)
	conversation.SaveState(updatedState)
	conversation.LastTid = strategyOutput.TemplateID
	conversation.LastAnchorType = strategyOutput.FinalAnchor
	conversation.Emotion = strategy.DetectEmotion(mergedContent)

	db.RQ(c).Save(&conversation)

	// 10. 更新客户画像（意向分反哺）
	newIntent := tVector[0] + strategyOutput.IntentDelta
	if newIntent < 0 {
		newIntent = 0
	}
	if newIntent > 1 {
		newIntent = 1
	}
	customer.IntentScore = newIntent

	// 更新客户T向量
	newTVector := customer.GetTVector()
	newTVector[0] = newIntent // 更新意向分
	customer.SaveTVector(newTVector)
	db.RQ(c).Save(&customer)

	// 11. 构造策略信息（供B端查看）
	strategyInfo := schema.StrategyInfo{
		AnchorType:       strategyOutput.FinalAnchor,
		AnchorTypeName:   strategy.GetAnchorName(strategyOutput.FinalAnchor),
		TemplateID:       strategyOutput.TemplateID,
		RouteResult:      routeResult,
		UrgencyLevel:     strategyOutput.UrgencyLevel,
		ExchangeFlag:     strategyOutput.ExchangeFlag,
		IntentScore:      newIntent,
		TrustLevel:       tVector[6],
		HookRate:         updatedState.HookRate,
		CurrentStage:     updatedState.CurrentStage,
		HighIntentRounds: updatedState.HighIntentRounds,
		Emotion:          conversation.Emotion,
		SoftDowngrade:    strategyOutput.SoftDowngrade,
		OriginalAnchor:   strategyOutput.OriginalAnchor,
	}

	// 11.5 模拟真人回复延迟
	// 修复：AI回复不能秒到，必须模拟真人：打字(40字/分钟) + 线下偏移
	// 到店倾向客户：去掉线下偏移，顾问必须快速响应
	// 放在SetReply之前，确保所有请求（主请求+合并等待请求）都经过延迟后再返回
	isStoreVisit := strategy.IsStoreVisitIntent(mergedContent) && !chatflow.IsLeadCaptured(&customer) // 到店意图且未留资才去除线下偏移
	log.Printf("[Chat] 客户%d 到店倾向检测: %v, 合并内容: %q", customer.ID, isStoreVisit, service.MaskPhoneInText(mergedContent))
	humanlikeDelay := service.CalcHumanlikeDelay(tenantID, aiReply, mergeWaitDuration, mergeCount, isStoreVisit)

	// 胡搅蛮缠：总非车话题>10且最近未恢复→回复速度降到3分钟一次
	hjTotalOffTopic := chatflow.CountTotalOffTopic(customer.ID)
	hjOnTopic := chatflow.CountConsecutiveOnTopic(customer.ID)
	if hjTotalOffTopic > 10 && hjOnTopic < 3 {
		minDelay := 180 * time.Second
		if humanlikeDelay < minDelay {
			log.Printf("[胡搅蛮缠-降速] 客户%d 非车%d>10, 延迟从%.1fs提升到%.1fs",
				customer.ID, hjTotalOffTopic, humanlikeDelay.Seconds(), minDelay.Seconds())
			humanlikeDelay = minDelay
		}
	}

	// 修复：总回复时长2分钟硬顶兜底（与ChatTest同步）
	maxTotalDelay := 120 * time.Second
	elapsed := time.Since(requestStart)
	remainingBudget := maxTotalDelay - elapsed
	if humanlikeDelay > remainingBudget {
		log.Printf("[Chat] 客户%d 总延迟硬顶触发: 已用%.1fs + 模拟延迟%.1fs > 2分钟, 截断到%.1fs",
			customer.ID, elapsed.Seconds(), humanlikeDelay.Seconds(), remainingBudget.Seconds())
		humanlikeDelay = remainingBudget
	}
	if humanlikeDelay < 0 {
		humanlikeDelay = 0
	}

	log.Printf("[Chat] 客户%d 模拟延迟: %.1fs, 已用: %.1fs, 总计: %.1fs, 开始sleep...", customer.ID, humanlikeDelay.Seconds(), elapsed.Seconds(), (elapsed + humanlikeDelay).Seconds())
	// 修复问题2：instant模式跳过CancellableSleep，秒回无延迟
	replyDelayMode := service.DefaultSystemConfigService.GetString("reply_delay_mode", "normal")
	if replyDelayMode == "instant" {
		log.Printf("[Chat] 客户%d instant模式，跳过延迟直接回复", customer.ID)
	} else {
		// 修复问题3：用可取消延迟替代time.Sleep，支持"立即回复"按钮
		chatflow.CancellableSleep(customer.ID, humanlikeDelay)
	}
	log.Printf("[Chat] 客户%d 延迟结束，返回回复", customer.ID)

	// 12. 唤醒消息队列中等待的其他请求
	service.DefaultMessageQueueService.SetReply(tenantID, customer.ID, aiReply)

	// 13. 返回结果
	// 欢迎词由 /chat/welcome 接口独立返回，此处不再附带 earlier_messages
	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data: schema.ChatResponse{
			ConversationID:   conversation.ID,
			Message:          aiMsg,
			StrategyInfo:     strategyInfo,
			RouteResult:      routeResult,
			Mode:             conversation.Mode,
			NewTags:          newTags,                     // 本轮新打上的标签
			PendingHandoff:   conversation.PendingHandoff, // 软接管状态
			MergedCustomerID: uint(leadCapturedResult),    // OneID合并：>0表示前端需切换customer_id
			VisitorKey:       customer.VisitorKey,         // C3：返回访客密钥
		},
	})
}

// HumanReply 人工回复接口
// 销售在B端发送消息
