// Package llm LLM 调用唯一入口：构建 Prompt、多模型降级路由（硅基流动主力→DeepSeek备用，
// 智谱已从路由移除——频繁 429，见 AGENTS.md；跨模型自动降级详见 ai/ai_router.go）、计量与兜底
package llm

import (
	"ai-scrm/internal/ai"
	"ai-scrm/internal/cache"
	"ai-scrm/internal/db"
	"ai-scrm/internal/metrics"
	"ai-scrm/internal/model"
	"ai-scrm/internal/strategytypes"
	"context"
	"log"
	"strings"
)

// ============================================================
// LLM 话术服务（Phase C 接缝版，自 chat.go 下沉）
// 完整流程：构建系统Prompt → 构建历史对话 → 构建策略Prompt → 调用GLM → 失败兜底用模板
// ⚠️ 架构真理（SAAS_PLAN §18）：本包是 LLM 的唯一调用入口；
// P2 将重构为只被策略引擎调用，业务层（chat.go）不再直连。
// ============================================================

// GenerateAIReply 生成AI回复（对外唯一入口）。
// Q5 修复(2026-09-12)：统一收口 sanitizeAddress——prompt 铁律与硬编码话术仍可能漏出
// 敬语「您」（去 AI 味铁律：说"你"不说"您"），出站前做一次替换 + 告警计数作纵深防御。
// 原多 return 路径的实现整体改名 generateAIReplyInner，此处只包一层清洗。
func GenerateAIReply(ctx context.Context, customer *model.Customer, conversationID uint, userInput string,
	strategyOutput *strategytypes.StrategyOutput, features []model.Feature) string {
	return sanitizeAddress(generateAIReplyInner(ctx, customer, conversationID, userInput, strategyOutput, features))
}

// generateAIReplyInner 生成AI回复（内部实现，含全部提前 return 分支）
// 完整流程：构建系统Prompt → 构建历史对话 → 构建策略Prompt → 调用GLM → 失败兜底用模板
// 模拟真人延迟已外移到调用方（思考20-40s + 打字40字/分钟）
// GenerateAIReply 生成AI回复（P2-B 起签名带 features：引擎特性集由调用方传入，
// 本包不再反向依赖 engine/strategy，依赖方向收敛为 llm→strategytypes/ai/chatflow）
func generateAIReplyInner(ctx context.Context, customer *model.Customer, conversationID uint, userInput string,
	strategyOutput *strategytypes.StrategyOutput, features []model.Feature) string {

	// 阶段上下文：跨段共享的局部变量全部收口为字段（拆分形态见 chat_reply_stages.go，
	// 纯结构性重构，各阶段执行顺序与短路语义与原函数体逐字一致）
	s := &aiReplyStage{
		ctx:            ctx,
		customer:       customer,
		conversationID: conversationID,
		userInput:      userInput,
		strategyOutput: strategyOutput,
		features:       features,
	}

	// 加载会话，检查引导式反问状态
	s.loadConversation()

	// P2-60 修复(2026-09-09)：引导式反问递减改 defer 统一出口——
	// 原只在"真实AI成功路径"末尾递减，配额耗尽/降级/硬拦截等提前 return 的路径
	// 永不递减，租户永久卡在 GuidedRemainingRounds=1，反问永不关闭。
	// defer 保证所有出口（成功/兜底/降级/硬拦截）都会消耗一轮。
	defer s.consumeGuidedRound()

	// 话题硬边界拦截（命中即终稿，不走AI）
	if reply, blocked := s.offTopicHardBlock(); blocked {
		return reply
	}

	// 询价硬拦截（命中即随机取一条行业话术，不进AI）
	if reply, blocked := s.priceHardBlock(); blocked {
		return reply
	}

	// 计费闸 + 模式闸：三桶余额/模拟模式/无可用模型，任一命中降级为模板兜底
	if fallback, hit := s.billingAndModePrecheck(); hit {
		return fallback
	}

	// 对话信号检测：轮数/重复问题/非车话题计数与三个热配置阈值
	s.detectDialogSignals()

	// 全硬拦截，不依赖prompt指令。
	// 所有规则流程在下面硬编码的interceptor中执行：
	//   - IsLeadCaptured → stripGuidedQuestions
	//   - closeGuided (dialogRound/repeat) → stripGuidedQuestions
	//   - offtopicRepeatCount → 固定回复+降权
	//   - GuidedDisabled → 顶部return确认语

	// 1. 系统Prompt（人设 + 卖点知识 + 价格管控 + 促单锁 + 到店转化策略 + KB 注入）
	s.buildSystemPromptWithKB()

	// 2. 策略指令（锚方向 + 话术参考 + 条件交换 + 知识库素材 + 销售路径决策段）
	s.buildStrategyPrompt()

	// 3+4. 组装消息列表：system → 历史对话 → 当前策略+用户消息
	s.assembleMessages()

	// 5+6. 调用AI（多模型降级路由）+ 落账；失败/空回复降级为模板兜底
	if fallback, hit := s.callAIAndBill(); hit {
		return fallback
	}

	// 7/7b. 硬拦截：留资反问剥离 + 引导关闭条件剥离
	s.stripGuidedQuestionsGuards()

	// 7c. 硬拦截：胡搅蛮缠分支（固定回复替换 + 意向分降权/恢复）
	s.enforceOffTopicGuards()

	// 7d. 硬拦截：引导关闭后全面剥离反问句
	s.stripAllQuestionsWhenGuidedClosed()

	// 8. 知识库盲点兜底检测（命中即整体替换为兜底话术）
	if blindspotReply, hit := s.knowledgeBlindspotFallback(); hit {
		return blindspotReply
	}

	// 注：引导式反问递减已上移到 defer 统一出口（P2-60），所有 return 路径统一消耗

	// 微修复（2026-08-22）：实测AI回复带前导换行（模型输出习惯），统一去除首尾空白
	return strings.TrimSpace(s.reply)
}

// getConversationHistory 获取会话的历史对话消息
// maxRounds: 最多返回几轮对话（一轮=用户+AI各一条）
// 返回按时间正序排列的消息列表
func getConversationHistory(tenantID, conversationID uint, maxRounds int) []ai.ChatMessage {
	if conversationID == 0 || maxRounds <= 0 {
		return nil
	}

	var messages []model.Message
	// 查最近 maxRounds*2 条消息（按ID倒序取，再翻回来）
	// 过滤掉system类型，只看customer和ai/human
	// P2-54 修复：查询带租户 scope（原裸 db.DB 靠 PK 全局唯一兜底，显式过滤更稳）
	limit := maxRounds * 2
	err := db.DB.Scopes(db.TenantFilter(tenantID)).
		Where("conversation_id = ? AND sender_type IN ?",
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
// 如果客户没有明确兴趣产品，返回默认产品（行业包第一个），保证知识库始终注入
func getCustomerModelID(customer *model.Customer) uint {
	tid := customer.TenantID
	// 有兴趣产品时，按名称匹配（租户隔离：仅系统预置+本租户产品，防跨租户命中）
	if customer.InterestProduct != "" {
		carModel := cache.DefaultKnowledgeCache.GetModelByName(tid, customer.InterestProduct)
		if carModel != nil {
			return carModel.ID
		}
	}
	// 没有兴趣车型或匹配失败，返回默认车型（第一个品牌的第一款车）
	defaultModel := cache.DefaultKnowledgeCache.GetDefaultModel(tid)
	if defaultModel != nil {
		return defaultModel.ID
	}
	return 0
}

// ============================================================
// Q5 修复(2026-09-12)：去 AI 味铁律出站兜底——说"你"不说"您"
// 根因：prompt_builder 历史指令自带"用「您好」开头"「等您试驾」话术，与"不说您"铁律
// 自相矛盾，模型随机命中；硬编码兜底话术亦有多处「您」。指令清洗之外，此处在
// AI 回复唯一出口做一次替换（纵深防御），命中即告警计数供回归观察。
// 仅处理生成侧出站文本；chatflow 敏感词/反问检测清单不经过本函数，不受影响。
// ============================================================

// sanitizeAddress 出站敬语清洗："您"→"你"（含"您"字符即替换，边界中文无需分词）。
// 命中时打 WARN 日志并计数（ai_scrm_address_polite_total），供发现漏网的 prompt/话术。
func sanitizeAddress(reply string) string {
	if reply == "" || !strings.Contains(reply, "您") {
		return reply
	}
	clean := strings.ReplaceAll(reply, "您", "你")
	r := []rune(clean)
	if len(r) > 60 {
		r = append(r[:60], '…')
	}
	log.Printf("[人设][WARN] AI回复含「您」已兜底替换: %q", string(r))
	metrics.IncAddressPolite()
	return clean
}
