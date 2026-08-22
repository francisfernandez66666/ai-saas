package api

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
	"ai-scrm/pkg/utils"
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

func Chat(c *gin.Context) {
	// 修复：记录请求开始时间，用于总延迟2分钟硬顶兜底
	requestStart := time.Now()

	// 生效租户（中间件链保证存在）：消息合并队列按 tenantID:customerID 隔离
	tenantID := middleware.EffectiveTenantID(c)

	var req schema.ChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
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
		log.Printf("[硬边界] 客户%d 拦截无关话题(入队前): %q → %q", customer.ID, req.Content, reply)
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
		c.JSON(http.StatusOK, gin.H{
			"code":    0,
			"message": "success",
			"data": gin.H{
				"conversation_id": conversation.ID,
				"ai_reply":        reply,
				"route_result":    "offtopic_hardbound",
			},
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
			log.Printf("[到店倾向-已留资线索] 客户%d 留资成功: phone=%s, stage=lead_captured, assigned=%d",
				customer.ID, phoneMatch, customer.AssignedUserID)

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
			rand.Seed(time.Now().UnixNano())
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

			c.JSON(http.StatusOK, gin.H{
				"code":    0,
				"message": "success",
				"data": gin.H{
					"conversation_id":    conversation.ID,
					"ai_reply":           leadCapturedReply,
					"route_result":       "lead_captured_confirmed",
					"merged_customer_id": mergedTargetID, // OneID合并：>0表示前端需切换customer_id
				},
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

		c.JSON(http.StatusOK, gin.H{
			"code":    0,
			"message": "success",
			"data": gin.H{
				"conversation_id": conversation.ID,
				"ai_reply":        firstReply,
				"route_result":    "store_visit_fast",
			},
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

		c.JSON(http.StatusOK, gin.H{
			"code":    0,
			"message": "success",
			"data": gin.H{
				"conversation_id": conversation.ID,
				"ai_reply":        simpleReply,
				"message":         simpleMsg,
			},
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
				aiReply = flow.DefaultEngine.OrchestrateReply(&customer, conversation.ID, mergedContent, &strategyOutput)
			} else {
				aiReply = ""
				routeResult = "human_locked_no_ai"
				log.Printf("[Chat] 会话%d AI回复已关闭(IsAiReplyEnabled=false)，跳过AI", conversation.ID)
			}
		} else {
			conversation.Mode = "ai"
			aiReply = flow.DefaultEngine.OrchestrateReply(&customer, conversation.ID, mergedContent, &strategyOutput)
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
		aiReply = flow.DefaultEngine.OrchestrateReply(&customer, conversation.ID, mergedContent, &strategyOutput)

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
		publishConversationMsg(tenantID, customer.ID, routeResult)
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
	log.Printf("[Chat] 客户%d 到店倾向检测: %v, 合并内容: %q", customer.ID, isStoreVisit, mergedContent)
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
		},
	})
}

// HumanReply 人工回复接口
// 销售在B端发送消息
func HumanReply(c *gin.Context) {
	var req struct {
		ConversationID uint   `json:"conversation_id" binding:"required"`
		Content        string `json:"content" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误", Data: nil})
		return
	}

	// 获取会话
	var conversation model.Conversation
	result := db.RQ(c).First(&conversation, req.ConversationID)
	if result.Error != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "会话不存在", Data: nil})
		return
	}

	// 切换到人工模式并锁定
	conversation.Mode = "human"
	conversation.IsHumanLocked = true
	conversation.PendingHandoff = false // 人工回复后，清除待接管状态
	conversation.HandoffNotifiedAt = nil
	now := time.Now()
	conversation.LastHumanReplyAt = &now
	conversation.LastMessageAt = &now
	db.RQ(c).Save(&conversation)

	// 保存人工消息
	humanMsg := model.Message{
		ConversationID: conversation.ID,
		CustomerID:     conversation.CustomerID,
		SenderType:     "human",
		Content:        req.Content,
		MessageType:    "text",
		CreatedAt:      now,
	}
	db.RQ(c).Create(&humanMsg)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "发送成功",
		Data:    humanMsg,
	})
}

// GetMessages 获取会话消息列表
func GetMessages(c *gin.Context) {
	conversationID := c.Param("id")

	var messages []model.Message
	db.RQ(c).Where("conversation_id = ?", conversationID).
		Order("created_at ASC").
		Limit(200).
		Find(&messages)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data:    messages,
	})
}

// GetConversationList 获取会话列表
func GetConversationList(c *gin.Context) {
	var req schema.ConversationListRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误", Data: nil})
		return
	}

	query := db.RQ(c).Model(&model.Conversation{})

	if req.CustomerID > 0 {
		query = query.Where("customer_id = ?", req.CustomerID)
	}
	if req.Status != "" {
		query = query.Where("status = ?", req.Status)
	}
	if req.Mode != "" {
		query = query.Where("mode = ?", req.Mode)
	}

	var total int64
	query.Count(&total)

	var conversations []model.Conversation
	query.Order("updated_at DESC").
		Offset(req.GetOffset()).
		Limit(req.PageSize).
		Find(&conversations)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data: schema.PageResponse{
			Total:    total,
			Page:     req.Page,
			PageSize: req.PageSize,
			List:     conversations,
		},
	})
}

// TransferToHuman 手动转人工
func TransferToHuman(c *gin.Context) {
	var req struct {
		ConversationID uint `json:"conversation_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误", Data: nil})
		return
	}

	var conversation model.Conversation
	result := db.RQ(c).First(&conversation, req.ConversationID)
	if result.Error != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "会话不存在", Data: nil})
		return
	}

	conversation.Mode = "human"
	conversation.IsHumanLocked = true
	db.RQ(c).Save(&conversation)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "已转人工",
		Data:    conversation,
	})
}

// TransferToAI 切回AI
func TransferToAI(c *gin.Context) {
	var req struct {
		ConversationID uint `json:"conversation_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误", Data: nil})
		return
	}

	var conversation model.Conversation
	result := db.RQ(c).First(&conversation, req.ConversationID)
	if result.Error != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "会话不存在", Data: nil})
		return
	}

	conversation.Mode = "ai"
	conversation.IsHumanLocked = false
	db.RQ(c).Save(&conversation)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "已切回AI",
		Data:    conversation,
	})
}

// ============================================================
// AI测试接口（免登录，方便调试）
// 与正式 Chat() 接口统一使用会话竞态保护 + 冷启动秒回 + 合并状态返回
// ============================================================

// ChatTest AI对话测试接口
// 不需要登录token，直接模拟一轮对话
// 用途：快速验证AI接入、冷启动秒回、消息合并等是否正常
//
// 修复内容：
//  1. Bug 2 修复：加入会话竞态保护 + 冷启动秒回（与正式接口统一）
//     原Bug：测试接口完全没有会话管理，冷启动秒回不会出现
//     修复：用客户级互斥锁先查再建，新客户触发秒回，已有客户复用会话
//  2. Bug 1 修复：合并请求只返回合并状态，不返回完整回复
//     原Bug：三条并发请求都返回同一段 ai_reply，看起来像三条重复消息
//     修复：merged 请求只返回 merged=true 标记，主请求返回完整结果
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
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    400,
			"message": "参数错误: " + err.Error(),
		})
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
		c.JSON(http.StatusNotFound, gin.H{
			"code":    404,
			"message": "客户不存在，可用customer_id=1测试",
		})
		return
	}

	// ---- 修复：入队列前三层分流（与Chat正式接口同步） ----
	// 优先级：硬边界拦截(0延迟) > 到店倾向快速通道(10-15秒) > 简单消息(8秒) > 正常合并队列
	// 根因：用户发"高数题"等了9分钟才收到回复；"试驾"意向被合并吞掉
	// 原来所有消息都先进合并队列(25s)，再到GenerateAIReply里才查硬边界和到店倾向——太晚了

	// 第一层：硬边界拦截（0延迟，不走AI，不进队列）
	if service.IsOffTopic(req.Content) {
		reply := service.GetOffTopicReply(req.Content)
		log.Printf("[硬边界-测试接口] 客户%d 拦截无关话题(入队前): %q → %q", customer.ID, req.Content, reply)
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
		c.JSON(http.StatusOK, gin.H{
			"code":    0,
			"message": "success",
			"data": gin.H{
				"conversation_id":    conv.ID,
				"ai_reply":           reply,
				"route_result":       "offtopic_hardbound",
				"customer_msg_id":    customerMsg.ID,               // 修复问题4：返回客户消息DB ID
				"assistant_messages": []model.Message{offTopicMsg}, // 修复：返回带真实DB ID的消息列表，前端用此去重
			},
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
				customer.ID, phoneMatchTest, customer.AssignedUserID)
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

			c.JSON(http.StatusOK, gin.H{
				"code":    0,
				"message": "success",
				"data": gin.H{
					"conversation_id":    conv.ID,
					"ai_reply":           leadCapturedReply,
					"route_result":       "lead_captured_confirmed",
					"merged_customer_id": mergedTargetIDTest,       // OneID合并：>0表示前端需切换customer_id
					"customer_msg_id":    customerMsg.ID,           // 修复：返回客户消息DB ID
					"assistant_messages": []model.Message{leadMsg}, // 修复：返回带真实DB ID的消息列表
				},
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

		c.JSON(http.StatusOK, gin.H{
			"code":    0,
			"message": "success",
			"data": gin.H{
				"conversation_id":    conv.ID,
				"ai_reply":           firstReply,
				"route_result":       "store_visit_fast",
				"customer_msg_id":    customerMsg.ID,
				"assistant_messages": []model.Message{storeVisitMsg},
			},
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

		c.JSON(http.StatusOK, gin.H{
			"code":    0,
			"message": "success",
			"data": gin.H{
				"conversation_id":    conv.ID,
				"ai_reply":           simpleReply,
				"message":            simpleMsg,
				"customer_msg_id":    testCustomerMsgID,          // 修复问题4：返回客户消息DB ID，供前端替换temp ID
				"assistant_messages": []model.Message{simpleMsg}, // 修复：返回带真实DB ID的消息列表
			},
		})
		return
	}

	if !shouldProcess {
		// Bug 1 修复：合并请求只返回合并状态，不返回完整AI回复
		// 前端收到 merged=true 时，不渲染新消息气泡，等主请求的回复即可
		c.JSON(http.StatusOK, gin.H{
			"code":    0,
			"message": "success",
			"data": gin.H{
				"merged":          true,
				"merged_note":     "本条消息已与先前的消息合并处理，回复将在主请求中返回",
				"customer_input":  req.Content,       // 保留原始输入，方便调试对照
				"customer_msg_id": testCustomerMsgID, // 修复：返回客户消息DB ID，供前端替换temp ID
			},
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
	}
	strategyOutput := strategy.DefaultEngine.Infer(strategyInput)

	// ---- 生成AI回复 ----
	// 与旧版不同：现在传入真实 conversationID，AI可以获取历史对话上下文
	aiReply := flow.DefaultEngine.OrchestrateReply(&customer, conversation.ID, mergedContent, &strategyOutput)

	// 模拟真人回复延迟：打字(40字/分钟) + 线下偏移
	// 到店倾向客户：去掉线下偏移，顾问必须快速响应
	isStoreVisit := strategy.IsStoreVisitIntent(mergedContent) && !chatflow.IsLeadCaptured(&customer) // 到店意图且未留资才去除线下偏移
	log.Printf("[ChatTest] 客户%d 到店倾向检测: %v, 合并内容: %q", customer.ID, isStoreVisit, mergedContent)
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
	publishConversationMsg(tenantID, customer.ID, strategyOutput.RouteResult)

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
	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "success",
		"data": gin.H{
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
			"is_ai_mode":         service.DefaultSystemConfigService.GetBool("mock_mode", config.GlobalConfig.AI.MockMode) == false && (ai.DefaultClient.APIKey != "" || (ai.SiliconFlowDefaultClient != nil && ai.SiliconFlowDefaultClient.Enabled)),
			"merged_customer_id": testLeadResult, // OneID合并：>0表示前端需切换customer_id
		},
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
func Welcome(c *gin.Context) {
	var req struct {
		CustomerID uint `json:"customer_id"` // 客户ID，默认1号
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    400,
			"message": "参数错误: " + err.Error(),
		})
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
		c.JSON(http.StatusNotFound, gin.H{
			"code":    404,
			"message": "客户不存在，可用customer_id=1测试",
		})
		return
	}

	// 查找或创建会话（竞态保护，与Chat/ChatTest统一机制）
	var conversation model.Conversation
	isNewConversation := false // 标记是否新创建的会话（返回给前端，供UI区分）

	convMu := chatflow.GetConversationMutex(customer.ID)
	convMu.Lock()

	// 先查该客户是否已有活跃会话
	db.RQ(c).Where("customer_id = ? AND status = ?", customer.ID, "active").
		Order("updated_at DESC").
		Limit(1).Find(&conversation)

	if conversation.ID == 0 {
		// 没有活跃会话 → 创建新会话
		conversation = model.Conversation{
			CustomerID:     customer.ID,
			AssignedUserID: customer.AssignedUserID,
			Status:         "active",
			Mode:           "ai",
			Channel:        "web",
		}
		db.RQ(c).Create(&conversation)
		isNewConversation = true
		log.Printf("[欢迎] 创建新会话 %d, 客户 %d", conversation.ID, customer.ID)

		// 启动默认流程（新会话的初始流程引擎）
		flowCtx := &flow.FlowContext{
			TenantID:       db.EffectiveTenantIDFromGin(c),
			CustomerID:     customer.ID,
			ConversationID: conversation.ID,
			RouteResult:    "ai",
		}
		flow.DefaultEngine.StartFlow("default_chat_flow", flowCtx)
	} else {
		// 已有活跃会话，直接复用
		log.Printf("[欢迎] 复用已有会话 %d, 客户 %d", conversation.ID, customer.ID)
	}

	convMu.Unlock()

	// 创建欢迎消息（每次调用都创建一条新的）
	// 前端控制调用时机：仅在页面打开时调一次，不关闭页面不重复调
	// 非工作时间追加提示，让客户有心理预期
	welcomeText := "你好，请稍等，顾问正在接通中，你可以先说说你的问题。"
	if !utils.IsWorkTime() {
		welcomeText += "由于现在是非工作时间，回复可能较慢，请见谅。"
	}
	welcomeMsg := model.Message{
		ConversationID: conversation.ID,
		CustomerID:     customer.ID,
		SenderType:     "system",
		Content:        welcomeText,
		MessageType:    "text",
		CreatedAt:      time.Now(),
	}
	db.RQ(c).Create(&welcomeMsg)
	log.Printf("[欢迎] 秒回消息已插入, 会话 %d", conversation.ID)

	// 立刻返回（无AI处理，毫秒级响应）
	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "success",
		"data": gin.H{
			"conversation_id":     conversation.ID,   // 会话ID（供后续 /chat/test 调用时传入）
			"welcome_message":     welcomeMsg,        // 欢迎消息（前端直接渲染到聊天窗口）
			"is_new_conversation": isNewConversation, // 是否新创建的会话（前端可据此做UI区分）
		},
	})
}

// ============================================================
// 访客注册接口 - 每次打开client页面创建新访客
// 业务逻辑：外部体验者打开页面 → 自动创建临时访客客户 → 拿到customer_id开始聊天
// 留资时如果手机号匹配到老客户 → OneID合并（聊天记录+标签+线索全部迁移到老客户）
// ============================================================

// CreateGuest 创建访客客户（免登录）
// POST /api/v1/chat/guest
// publishConversationMsg 发布会话消息事件（缺口4修复，2026-08-22）
// 激活 CDP beh_msg_active 标签；注意：聊天内容不送 CDP（SAAS_PLAN §15.4 边界），只传事件事实
func publishConversationMsg(tenantID uint, customerID uint, route string) {
	if err := mq.Publish(context.Background(), mq.TopicUserEvent, tenantID,
		fmt.Sprintf("c:%d", customerID), "conversation_msg",
		mq.UserEvent{EventType: "behavior", EventName: "conversation_msg",
			Attributes: map[string]any{"customer_id": customerID, "route": route},
			OccurredAt: time.Now()}); err != nil {
		log.Printf("[MQ] conversation_msg 事件发布失败: %v", err)
	}
}

func CreateGuest(c *gin.Context) {
	// 生成唯一访客名：访客_后4位随机数
	rand.Seed(time.Now().UnixNano())
	suffix := rand.Intn(9000) + 1000 // 1000-9999
	guestName := fmt.Sprintf("访客_%d", suffix)

	// 修复：未留资访客不分配顾问（铁律：未留资→AI接管不给顾问）
	// 只有留资后才分配顾问，未留资访客AssignedUserID=0
	// 顾问端只看到assigned_user_id > 0的客户，未留资访客不可见
	var assignedUserID uint = 0 // 未留资访客不分配顾问

	customer := model.Customer{
		Name:             guestName,
		CustomerType:     "potential",
		JourneyStage:     model.JourneyAIConnected, // AI建联（起点）
		Source:           "外部体验",
		TrustLevel:       0.3,
		IntentScore:      0.2,
		PriceSensitivity: 0.5,
		BrandAwareness:   0.3,
		ResistanceType:   "none",
		AssignedUserID:   assignedUserID, // 0=未分配顾问，留资后才分配
		Status:           1,
	}

	if err := db.RQ(c).Create(&customer).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": "创建访客失败: " + err.Error(),
		})
		return
	}

	log.Printf("[访客注册] 新访客创建成功: ID=%d, Name=%s, 分配顾问=%d", customer.ID, customer.Name, customer.AssignedUserID)

	// 缺口4修复（2026-08-22）：guest_created 事件上行（激活 CDP idm_guest 标签）
	// 此前 IngestConsumer 支持该事件但全仓无发布点，访客身份标签是死代码
	if err := mq.Publish(context.Background(), mq.TopicUserEvent, middleware.EffectiveTenantID(c),
		fmt.Sprintf("c:%d", customer.ID), "guest_created",
		mq.UserEvent{EventType: "identity", EventName: "guest_created", AnchorType: "device",
			Attributes: map[string]any{"customer_id": customer.ID, "source": customer.Source},
			OccurredAt: time.Now()}); err != nil {
		log.Printf("[MQ] guest_created 事件发布失败: %v", err)
	}

	c.JSON(http.StatusOK, gin.H{
		"code":        0,
		"customer_id": customer.ID,
		"name":        customer.Name,
		"message":     "访客创建成功",
	})
}

// ============================================================
// OneID合并 - 留资时手机号匹配到老客户，合并所有数据
// 业务逻辑：
// 1. 访客A留资提供手机号 → 查到老客户B同手机号
// 2. 把访客A的会话、消息、线索(FollowUp)全部迁移到老客户B
// 3. 合并标签（去重）
// 4. 取最高旅程阶段
// 5. 删除访客A（标记无效）
// 6. 返回老客户B的ID，前端切换

// ============================================================
// ClearDelay 清除客户所有待发延迟消息，实现立即回复
// 修复问题3：顾问/管理员点击"立即回复"按钮时调用
// 设计：通过channel机制通知正在sleep的goroutine立即结束延迟
// 不同于DB-based的message_queue方案，本系统延迟在goroutine内实现
// 所以用channel取消机制替代DB scheduled_at更新
// ============================================================
func ClearDelay(c *gin.Context) {
	var req struct {
		CustomerID uint `json:"customer_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误", Data: nil})
		return
	}

	// 取消该客户的当前延迟（如果有）
	chatflow.CancelDelay(req.CustomerID)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "已清除延迟，消息将立即发出",
		Data:    nil,
	})
}
