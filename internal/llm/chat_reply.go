// Package llm LLM 调用唯一入口：构建 Prompt、多模型降级路由（智谱GLM→硅基流动）、计量与兜底
package llm

import (
	"ai-scrm/config"
	"ai-scrm/internal/ai"
	"ai-scrm/internal/cache"
	"ai-scrm/internal/chatflow"
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/service"
	"ai-scrm/internal/strategytypes"
	"log"
	"math/rand"
	"strings"
	"time"
)

// ============================================================
// LLM 话术服务（Phase C 接缝版，自 chat.go 下沉）
// 完整流程：构建系统Prompt → 构建历史对话 → 构建策略Prompt → 调用GLM → 失败兜底用模板
// ⚠️ 架构真理（SAAS_PLAN §18）：本包是 LLM 的唯一调用入口；
// P2 将重构为只被策略引擎调用，业务层（chat.go）不再直连。
// ============================================================

// GenerateAIReply 生成AI回复
// 完整流程：构建系统Prompt → 构建历史对话 → 构建策略Prompt → 调用GLM → 失败兜底用模板
// 模拟真人延迟已外移到调用方（思考20-40s + 打字40字/分钟）
// GenerateAIReply 生成AI回复（P2-B 起签名带 features：引擎特性集由调用方传入，
// 本包不再反向依赖 engine/strategy，依赖方向收敛为 llm→strategytypes/ai/chatflow）
func GenerateAIReply(customer *model.Customer, conversationID uint, userInput string,
	strategyOutput *strategytypes.StrategyOutput, features []model.Feature) string {

	// 加载会话，检查引导式反问状态
	var genConv model.Conversation
	db.DB.First(&genConv, conversationID)

	// ---- 话题硬边界拦截 ----
	// 修复：只靠提示词软约束不够，AI还是会回答无关话题
	// 硬边界 = 代码层拦截，命中无关话题直接返回引导话术，不走AI
	// 白名单优先：消息含车相关词则放行（"帮我写个试驾报告"→含"试驾"→不拦截）
	if service.IsOffTopic(userInput) {
		reply := service.GetOffTopicReply(userInput)
		log.Printf("[硬边界] 拦截无关话题，引导回车")
		return reply
	}

	// ---- 询价硬拦截：客户问价格，一律引导到店试驾后细谈，不走AI ----
	// 硬编码：不依赖prompt，直接在代码层拦截替换
	// 句式参考：「要不帮您约个试驾，体验过后我再根据您的配置需求做个报价，怎么样呀」
	// 可以AI自由发挥，但核心意图不变：引导到店试驾→体验后出报价
	priceKeywords := []string{
		"多少钱", "什么价", "报价", "价格", "价位", "售价", "贵不贵",
		"怎么卖", "怎么算", "落地价", "优惠多少", "便宜多少",
		"车价", "售价多少", "多少钱一辆", "多少钱一台",
	}
	for _, kw := range priceKeywords {
		if strings.Contains(userInput, kw) {
			log.Printf("[询价硬拦截] 客户%d 询价关键词: %s, 引导到店试驾", customer.ID, kw)
			leadCapturedGuiding := chatflow.IsLeadCaptured(customer) && genConv.GuidedRemainingRounds == 0
			if leadCapturedGuiding {
				// 已留资且引导关闭：纯陈述引导，不再反问
				replies := []string{
					"价格得看具体配置和您的需求来定，要不帮您约个试驾，体验过后我再根据您的配置需求做个报价",
					"车价跟配置和选装方案有关，建议您先到店试驾体验一下，试好了我按您的需求出个详细报价",
					"具体价格得看您选什么配置，要不先约个试驾，您亲身感受后再根据您的需求给您报价",
				}
				rand.Seed(time.Now().UnixNano())
				return replies[rand.Intn(len(replies))]
			}
			// 未留资或引导未关闭：可以带一句反问引导
			replies := []string{
				"要不帮您约个试驾，体验过后我再根据您的配置需求做个报价，怎么样呀",
				"价格得看配置来定，要不先帮您约个试驾，您试完车我按您的需求做个详细报价，行不",
				"车价跟具体配置有关，要不我帮您安排个试驾，体验好了我按您的需求出个报价，您看咋样",
			}
			rand.Seed(time.Now().UnixNano())
			return replies[rand.Intn(len(replies))]
		}
	}

	// 促单锁：提前计算canPromote，确保所有路径（含兜底）都受控
	// 修复：原来canPromote声明在MockMode检查之后，前两处BuildFallbackReply调用无法传入
	canPromote := customer.CanPromote()

	// ---- 计量与配额（SaaS 计费）：硬边界拦截不消耗配额，走到这里才算一次 AI 回复 ----
	tenantID := customer.TenantID
	if !service.CheckAIQuota(tenantID) {
		log.Printf("[Usage] 租户%d AI 配额已超限，本次降级为规则话术（不发起模型请求）", tenantID)
		return ai.BuildFallbackReply(strategyOutput, canPromote)
	}
	service.RecordAICall(tenantID)

	// P1.5 Token三桶引擎前置检查（2026-08-26）：总闸/强制未开时恒放行；
	// 三桶均空 → 降级规则话术（扣减优先级 ③免费桶→①订阅额度→②余额 在 DeductTokensActual 落地）
	if !service.CheckTokenAvailability(tenantID) {
		log.Printf("[TokenBilling] 租户%d 三桶余额不足，本次降级规则话术", tenantID)
		return ai.BuildFallbackReply(strategyOutput, canPromote)
	}

	// 模拟模式：直接用策略中心的模板话术兜底
	// 修复：从SystemConfigService读取mock_mode，后台开关即时生效
	if service.DefaultSystemConfigService.GetBool("mock_mode", config.GlobalConfig.AI.MockMode) {
		return ai.BuildFallbackReply(strategyOutput, canPromote)
	}

	// 检查是否有任何可用的AI模型（智谱或硅基流动）
	hasAnyAI := ai.DefaultClient.APIKey != ""
	if ai.SiliconFlowDefaultClient != nil && ai.SiliconFlowDefaultClient.Enabled {
		hasAnyAI = true
	}
	if !hasAnyAI {
		log.Printf("[AI] 无可用AI模型，使用模板兜底")
		return ai.BuildFallbackReply(strategyOutput, canPromote)
	}

	// 确认走多模型降级路由
	log.Printf("[AI] 走多模型降级链路，当前路由模型数: %d", len(ai.Router.GetModels()))

	// 真实AI模式：用高质量Prompt构建器
	// 修复问题4+6：检测对话轮数和重复话题，动态注入策略指令
	// 4. 引导式对话在冷启动用户N次关键需求回答后关闭（默认5轮，后台可调）
	//    达到阈值后：关闭反问，专注解答+适当介绍ROX品牌/车型/能力
	// 4b. 客户重复性问题超过3次，关闭反问引导式语句，直接走解决陈述
	// 6. 非车话题重复3次及以上后，改语气，关闭引导式反问，认真说回聊到车上
	guidedDialogMaxRounds := service.DefaultSystemConfigService.GetInt("guided_dialog_max_rounds", 5)
	repeatQuestionMaxTimes := service.DefaultSystemConfigService.GetInt("repeat_question_max_times", 3)
	offtopicRepeatMaxTimes := service.DefaultSystemConfigService.GetInt("offtopic_repeat_max_times", 3)

	// 检测对话轮数（客户发了多少条消息）
	dialogRoundCount := 0
	var customerMsgCount []model.Message
	db.DB.Where("customer_id = ? AND sender_type = ?", customer.ID, "customer").
		Find(&customerMsgCount)
	dialogRoundCount = len(customerMsgCount)

	// 检测重复问题（客户最近的消息和之前的消息相似度）
	repeatCount := chatflow.CountSimilarQuestions(customer.ID, userInput)

	// 检测非车话题重复次数
	offtopicRepeatCount := chatflow.CountOffTopicRepeats(customer.ID)
	// 胡搅蛮缠分支：总非车话题数和连续在话题数
	totalOffTopic := chatflow.CountTotalOffTopic(customer.ID)
	consecutiveOnTopic := chatflow.CountConsecutiveOnTopic(customer.ID)

	// 全硬拦截，不依赖prompt指令。
	// 所有规则流程在下面硬编码的interceptor中执行：
	//   - IsLeadCaptured → stripGuidedQuestions
	//   - closeGuided (dialogRound/repeat) → stripGuidedQuestions
	//   - offtopicRepeatCount → 固定回复+降权
	//   - GuidedDisabled → 顶部return确认语

	// 1. 系统Prompt（人设 + 卖点知识 + 价格管控 + 促单锁 + 到店转化策略）
	hasArrived := customer.HasArrived() // 判断客户是否已到店，用于价格管控
	// canPromote已在函数顶部声明，此处不再重复
	isStoreVisit := strategytypes.IsStoreVisitIntent(userInput) && !chatflow.IsLeadCaptured(customer) // 到店意图且未留资才注入到店策略
	modelID := getCustomerModelID(customer)
	systemPrompt := ai.BuildSystemPrompt(customer.TenantID, features, modelID, hasArrived, strategyOutput.FinalAnchor, canPromote, isStoreVisit, chatflow.IsLeadCaptured(customer))

	// P2 双层KB：租户自有资料融合检索注入（二元组打分 top3；无命中不加段）
	if kbHits := service.SearchTenantKnowledge(customer.TenantID, userInput, 3); len(kbHits) > 0 {
		var kbs strings.Builder
		kbs.WriteString("\n【企业知识库参考】以下为该企业自有资料片段，仅供参考；与客户问题相关就自然融入，无关或不确定就别硬套：\n")
		for _, f := range kbHits {
			content := []rune(f.Content)
			if len(content) > 300 {
				content = content[:300]
			}
			kbs.WriteString("- [" + f.Title + "] " + string(content) + "\n")
		}
		systemPrompt += kbs.String()
		log.Printf("[KB] 租户%d 命中 %d 条自有知识片段注入 prompt", customer.TenantID, len(kbHits))
	}

	// 2. 策略指令（锚方向 + 话术参考 + 条件交换 + 知识库素材）
	strategyPrompt := ai.BuildStrategyPrompt(strategyOutput, customer.GetTags(), modelID, hasArrived, canPromote, chatflow.IsLeadCaptured(customer))

	// 3. 构建对话上下文
	// 修复问题7：关闭模型聊天记忆（默认0轮），改用核心内容摘要注入system prompt
	// 核心摘要提取：用户需求、看过哪些车、在开什么车、关注点等
	// 不依赖模型记忆来回复（会偏移），只用核心信息让AI知道客户背景
	chatHistoryRounds := service.DefaultSystemConfigService.GetInt("chat_history_rounds", 0)
	var historyMessages []ai.ChatMessage
	if chatHistoryRounds > 0 {
		// 如果配置了>0轮，仍用传统对话历史注入
		historyMessages = getConversationHistory(conversationID, chatHistoryRounds)
	}

	// 4. 组装消息列表：system → 历史对话 → 当前策略+用户消息
	messages := make([]ai.ChatMessage, 0, len(historyMessages)+2)

	// 修复问题7：当关闭对话历史注入(chat_history_rounds=0)时，注入客户核心信息摘要
	// 摘要来源：客户画像字段 + 最近消息中的关键信息
	// 不注入完整历史，只提取核心需求/兴趣/关注点，避免模型记忆偏移
	customerContextSummary := ""
	if chatHistoryRounds == 0 {
		customerContextSummary = chatflow.BuildCustomerContextSummary(customer, conversationID)
	}

	// system prompt：如果有核心摘要+策略调整，追加到system prompt末尾
	systemPromptFinal := systemPrompt
	if customerContextSummary != "" {
		systemPromptFinal = systemPrompt + "\n\n【客户核心信息摘要】\n" + customerContextSummary
	}

	messages = append(messages, ai.ChatMessage{
		Role:    "system",
		Content: systemPromptFinal,
	})
	// 历史对话
	messages = append(messages, historyMessages...)
	// 当前轮（策略prompt + 用户输入）
	messages = append(messages, ai.ChatMessage{
		Role: "user",
		Content: strategyPrompt + "\n\n" +
			"客户最新说的话：\n" + userInput + "\n\n" +
			"请直接回复客户：",
	})

	// 5. 调用AI（走多模型路由，自动降级；stage_models 可为 reply 阶段覆盖专属模型）
	// 修复：从SystemConfigService读取temperature，后台调参即时生效
	aiTemp := service.DefaultSystemConfigService.GetFloat("ai_temperature", ai.DefaultClient.Temperature)
	callStart := time.Now()
	reply, provider, modelName, usage, err := ai.Router.GenerateTextForStage("reply", messages, aiTemp)
	if err != nil {
		log.Printf("[AI] 所有模型均调用失败: %v, 降级使用模板回复", err)
		return ai.BuildFallbackReply(strategyOutput, canPromote)
	}
	// M3 计量落账（异步best-effort）：请求级 token/成本/延迟 → usage_ledger
	service.RecordUsage(tenantID, customer.ID, 0, "reply", provider, modelName,
		usage.PromptTokens, usage.CompletionTokens, time.Since(callStart).Milliseconds())
	// P1.5 按实际用量三桶顺序扣减（③→①→②；总闸/灰度未开时 no-op）
	go service.DeductTokensActual(tenantID, int64(usage.TotalTokens))
	if modelName != "" {
		log.Printf("[AI] 实际使用模型: %s (tokens=%d)", modelName, usage.TotalTokens)
	}

	// 6. 空回复兜底
	if reply == "" {
		return ai.BuildFallbackReply(strategyOutput, canPromote)
	}

	// 7. 硬拦截：已留资客户AI回复中不能含反问句
	// 硬编码：GuidedRemainingRounds>0时表示这是留资后第一条回复，允许反问句通过
	// 留资后第一条反问句由分支B直接回复固定句或AI注入，后续轮次全部剥离
	if chatflow.IsLeadCaptured(customer) && genConv.GuidedRemainingRounds == 0 {
		stripped := chatflow.StripGuidedQuestions(reply)
		if stripped != reply {
			log.Printf("[留资硬拦截] 客户%d 已留资，AI回复含反问句，已剥离: %q → %q", customer.ID, reply, stripped)
			reply = stripped
		}
	}

	// 7b. 硬拦截：引导式反问关闭条件触发时剥离反问句
	if !chatflow.IsLeadCaptured(customer) {
		closeGuided := (dialogRoundCount >= guidedDialogMaxRounds && strategyOutput.FinalAnchor == strategytypes.AnchorNoThrow) ||
			(repeatCount >= repeatQuestionMaxTimes)
		if closeGuided {
			stripped := chatflow.StripGuidedQuestions(reply)
			if stripped != reply {
				log.Printf("[引导关闭硬拦截] 客户%d 触发反问关闭条件，已剥离: %q → %q", customer.ID, reply, stripped)
				reply = stripped
			}
		}
	}

	// 7c. 硬拦截：胡搅蛮缠分支
	// 3轮非车话题后关闭引导+固定回复
	if offtopicRepeatCount >= offtopicRepeatMaxTimes {
		reply = "我这边只能回答您车和品牌相关的问题哈。咱们要不还是回到车和品牌上来？车相关的咱们还是专业对口的哈，您放心"
		log.Printf("[胡搅蛮缠-硬拦截] 客户%d 非车话题%d次 >= 阈值%d，已替换回复",
			customer.ID, offtopicRepeatCount, offtopicRepeatMaxTimes)
	}
	// 10轮以上总非车话题：降低意向分+放慢回复
	if totalOffTopic > 10 && consecutiveOnTopic < 3 {
		customer.IntentScore = customer.IntentScore * 0.5
		if customer.IntentScore < 0.05 {
			customer.IntentScore = 0.05
		}
		db.DB.Model(customer).Update("intent_score", customer.IntentScore)
		log.Printf("[胡搅蛮缠-降权] 客户%d 非车话题总数%d>10, 意向分降至%.2f",
			customer.ID, totalOffTopic, customer.IntentScore)
	}
	// 恢复：连续3轮以上回到车话题
	if consecutiveOnTopic >= 3 {
		restoredScore := customer.IntentScore * 2.0
		if restoredScore > 1.0 {
			restoredScore = 1.0
		}
		if restoredScore > customer.IntentScore {
			customer.IntentScore = restoredScore
			db.DB.Model(customer).Update("intent_score", customer.IntentScore)
			log.Printf("[胡搅蛮缠-恢复] 客户%d 连续%d轮在话题, 意向分恢复至%.2f",
				customer.ID, consecutiveOnTopic, customer.IntentScore)
		}
	}

	// 7d. 硬拦截：引导关闭后，全面剥离所有反问句（无论是否已留资）
	// 覆盖更多中文反问模式：什么、怎么、哪、有没有、是不是等
	// 引导关闭后AI只做陈述句介绍产品/了解需求，不再反问
	if genConv.GuidedDisabled && genConv.GuidedRemainingRounds == 0 {
		oldReply := reply
		reply = chatflow.StripAllQuestions(reply)
		if reply != oldReply {
			log.Printf("[引导关闭-全面剥离] 客户%d 引导已关闭，全面剥离反问句: %q → %q", customer.ID, oldReply, reply)
		}
	}

	// 8. 知识库盲点兜底检测
	// 修复问题5：触及知识库盲点后，模型一直在瞎回复兜圈子
	// 检测AI回复中是否包含不确定/兜圈子的信号词，触发后用盲点兜底话术替换
	// 兜底话术：关闭引导式提问，直接回"好的，稍等，这个问题我查一下"
	// 如果客户继续提问相关问题："不好意思我现在忙，要不您到店来体验下？"
	if service.DefaultSystemConfigService.GetBool("knowledge_blindspot_fallback_enabled", true) {
		blindspotReply := chatflow.DetectKnowledgeBlindspot(reply, userInput)
		if blindspotReply != "" {
			log.Printf("[知识库盲点兜底] 客户提问触及盲点，原始AI回复含不确定信号，替换为兜底话术")
			return blindspotReply
		}
	}

	// 9. 递减引导式反问剩余轮数
	// 留资后AI有5轮引导式反问，每轮AI回复后递减，到0后关闭引导
	if genConv.GuidedRemainingRounds > 0 {
		genConv.GuidedRemainingRounds--
		updates := map[string]interface{}{
			"guided_remaining_rounds": genConv.GuidedRemainingRounds,
		}
		if genConv.GuidedRemainingRounds == 0 {
			genConv.GuidedDisabled = true
			updates["guided_disabled"] = true
			log.Printf("[引导轮数耗尽] 会话%d 引导式反问5轮已用完，关闭引导", conversationID)
		}
		db.DB.Model(&genConv).Updates(updates)
	}

	// 微修复（2026-08-22）：实测AI回复带前导换行（模型输出习惯），统一去除首尾空白
	return strings.TrimSpace(reply)
}

// getConversationHistory 获取会话的历史对话消息
// maxRounds: 最多返回几轮对话（一轮=用户+AI各一条）
// 返回按时间正序排列的消息列表
func getConversationHistory(conversationID uint, maxRounds int) []ai.ChatMessage {
	if conversationID == 0 || maxRounds <= 0 {
		return nil
	}

	var messages []model.Message
	// 查最近 maxRounds*2 条消息（按ID倒序取，再翻回来）
	// 过滤掉system类型，只看customer和ai/human
	limit := maxRounds * 2
	err := db.DB.Where("conversation_id = ? AND sender_type IN ?",
		conversationID, []string{"customer", "ai", "human"}).
		Order("id DESC").
		Limit(limit).
		Find(&messages).Error

	if err != nil || len(messages) == 0 {
		return nil
	}

	// 翻转为正序
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	// 转换成 AI ChatMessage 格式
	return ai.BuildConversationHistory(messages, maxRounds)
}

// getCustomerModelID 获取客户兴趣车型对应的知识库车型ID
// 用于对比锚时注入知识库素材
// 如果客户没有明确兴趣车型，返回默认车型（极石第一款），保证知识库始终注入
func getCustomerModelID(customer *model.Customer) uint {
	// 有兴趣车型时，按名称匹配
	if customer.InterestModel != "" {
		carModel := cache.DefaultKnowledgeCache.GetModelByName(customer.InterestModel)
		if carModel != nil {
			return carModel.ID
		}
	}
	// 没有兴趣车型或匹配失败，返回默认车型（第一个品牌的第一款车）
	defaultModel := cache.DefaultKnowledgeCache.GetDefaultModel()
	if defaultModel != nil {
		return defaultModel.ID
	}
	return 0
}
