// generateAIReplyInner 的阶段拆分（2026-09-28，超长函数拆分批，形态对齐 internal/api chatSessionCtx 先例）。
//
// 背景：generateAIReplyInner 原为 334 行 god function，按既有注释段"剪切-粘贴"为
// aiReplyStage 上的若干阶段方法。**纯结构性重构，行为零变化**——所有条件顺序、SQL、
// 日志文案、返回值语义（提前 return 改为 (fallback, hit) 双返回值由主体判定）逐字保留。
// 跨段共享的局部变量收口为 aiReplyStage 字段，不引入任何包级全局变量。
package llm

import (
	"ai-scrm/config"
	"ai-scrm/internal/ai"
	"ai-scrm/internal/billing"
	"ai-scrm/internal/chatflow"
	"ai-scrm/internal/db"
	"ai-scrm/internal/metrics"
	"ai-scrm/internal/model"
	"ai-scrm/internal/pii"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/service"
	"ai-scrm/internal/strategytypes"
	"context"
	"log"
	"math/rand"
	"strings"
	"time"
)

// aiReplyStage 一次 AI 回复生成在函数生命周期内的共享可变上下文。
// 承载原函数体里跨段使用的局部变量（会话快照/计费闸产物/对话信号计数/prompt 中间产物/出站回复），
// 让各阶段方法签名保持一行，且副作用落点显式可读（写哪个字段=产出什么）。
type aiReplyStage struct {
	// 入参（与原 generateAIReplyInner 签名一一对应）
	ctx            context.Context
	customer       *model.Customer
	conversationID uint
	userInput      string
	strategyOutput *strategytypes.StrategyOutput
	features       []model.Feature

	// 会话加载与计费前置闸产物
	genConv     model.Conversation // 进入函数时的会话快照；递减只发生在 defer（consumeGuidedRound），各段读到的都是未递减值
	canPromote  bool
	tenantID    uint
	gatewayMode bool

	// 对话信号检测产物（阈值 + 实测计数）
	guidedDialogMaxRounds  int
	repeatQuestionMaxTimes int
	offtopicRepeatMaxTimes int
	dialogRoundCount       int
	repeatCount            int
	offtopicRepeatCount    int
	totalOffTopic          int
	consecutiveOnTopic     int

	// Prompt 组装产物
	modelID        uint
	hasArrived     bool
	isStoreVisit   bool
	systemPrompt   string
	strategyPrompt string
	messages       []ai.ChatMessage

	// 出站回复（硬拦截链逐级改写）
	reply string
}

// loadConversation 加载会话，检查引导式反问状态。
// 拆出原因：这是全链路唯一一次读 conversations 整行，且刻意沿用原实现的"忽略错误取零值"
// 语义（查不到时 GuidedRemainingRounds=0 走无引导分支），单独成段防被误改成必败分支。
func (s *aiReplyStage) loadConversation() {
	db.DB.First(&s.genConv, s.conversationID)
}

// consumeGuidedRound 引导式反问递减统一出口（原函数体内联 defer 块，逐字迁移）。
// 拆出原因：它是唯一改 s.genConv 的地方，与主链路的"只读判定"形态相反；
// defer 注册点仍在 generateAIReplyInner，保证所有出口（成功/兜底/降级/硬拦截）都会消耗一轮。
func (s *aiReplyStage) consumeGuidedRound() {
	if s.genConv.GuidedRemainingRounds > 0 {
		s.genConv.GuidedRemainingRounds--
		updates := map[string]interface{}{
			"guided_remaining_rounds": s.genConv.GuidedRemainingRounds,
		}
		if s.genConv.GuidedRemainingRounds == 0 {
			s.genConv.GuidedDisabled = true
			updates["guided_disabled"] = true
			log.Printf("[引导轮数耗尽] 会话%d 引导式反问轮数已用完，关闭引导", s.conversationID)
		}
		db.DB.Model(&s.genConv).Updates(updates)
	}
}

// offTopicHardBlock 话题硬边界拦截段；hit=true 表示 reply 即最终回复、主函数可直接 return。
// 拆出原因：纯判据分支，命中即短路整条链路（零 AI 调用、零落账），
// 与后续"计费→prompt→调用"的副作用路径完全不同形态。
func (s *aiReplyStage) offTopicHardBlock() (string, bool) {
	// ---- 话题硬边界拦截 ----
	// 修复：只靠提示词软约束不够，AI还是会回答无关话题
	// 硬边界 = 代码层拦截，命中无关话题直接返回引导话术，不走AI
	// 白名单优先：消息含车相关词则放行（"帮我写个试驾报告"→含"试驾"→不拦截）
	if service.IsOffTopicForTenant(s.customer.TenantID, s.userInput) {
		reply := service.GetOffTopicReplyForTenant(s.customer.TenantID, s.userInput)
		log.Printf("[硬边界] 拦截无关话题，引导回车")
		return reply, true
	}
	return "", false
}

// priceHardBlock 询价硬拦截段：客户问价格，一律引导到店/体验后报价，不进AI。
// 拆出原因：话术从行业键随机取一条即终态（同样零 AI 调用），且判据依赖未递减的
// genConv.GuidedRemainingRounds 决定 lead/nolead 分支——取值时点必须与主链路一致，单独成段锁住。
func (s *aiReplyStage) priceHardBlock() (string, bool) {
	// ---- 询价硬拦截：客户问价格，一律引导到店/体验后报价，不进AI ----
	// 泛行业化（P2.4）：关键词从 industry.price_keywords 读取，行业包可配置；空回退汽车默认
	// P1-29(2026-09-09)：话术整体迁入 industry.price_reply_lead/nolead 行业键（行业包可覆盖）；
	// P1-30：已留资分支不再出现"约试驾"（与 prompt 硬规则【已留资禁止促到店】矛盾）
	priceKeywords := service.IndustryPriceKeywordsForTenant(s.customer.TenantID)
	if service.ContainsKeywordForTenant(s.userInput, priceKeywords) {
		log.Printf("[询价硬拦截] 客户%d 触发询价硬拦截, 引导体验后报价", s.customer.ID)
		leadCapturedGuiding := chatflow.IsLeadCaptured(s.customer) && s.genConv.GuidedRemainingRounds == 0
		replies := service.IndustryPriceRepliesForTenant(s.customer.TenantID, leadCapturedGuiding)
		return replies[rand.Intn(len(replies))], true
	}
	return "", false
}

// billingAndModePrecheck 计费闸 + 模式闸：促单锁、网关判定、三桶余额、模拟模式、可用模型检查。
// 任一命中降级即返回 (兜底话术, true)，主函数直接 return；全部通过则把 canPromote/tenantID/gatewayMode
// 挂在 s 上供后续落账段消费。
// 拆出原因：这四条路共用同一形态——"不烧 token、直接落 BuildFallbackReply"，
// 且 gatewayMode 的"调用前预判"语义（C3 修复）必须与实际服务 provider 分离比对，集中一段防误改。
func (s *aiReplyStage) billingAndModePrecheck() (string, bool) {
	// 促单锁：提前计算canPromote，确保所有路径（含兜底）都受控
	// 修复：原来canPromote声明在MockMode检查之后，前两处BuildFallbackReply调用无法传入
	s.canPromote = s.customer.CanPromote()

	// 计费统一（2026-09-03）：ConsumeAIQuota 已降级为统计旁路（恒 true，仅累计 used_ai_calls），
	// 真正的计费闸是下方 CheckTokenAvailability（前置拦截） + SinkRecordUsage（批量扣减）
	s.tenantID = s.customer.TenantID

	// 网关模式：计费权已上收 AI 网关（网关侧做 fail-closed 计量），本地跳过自身计量避免重复扣减
	s.gatewayMode = ai.DefaultGatewayClient != nil && s.tenantID != 0
	if !s.gatewayMode {
		billing.ConsumeAIQuota(s.tenantID) // 统计旁路：恒放行，仅累计计数

		// P1.5 Token三桶引擎前置检查（2026-08-26）：总闸/强制未开时恒放行；
		// 三桶均空 → 降级规则话术（扣减优先级 ③免费桶→①订阅额度→②余额 在 DeductTokensActual 落地）
		if !billing.CheckTokenAvailability(s.tenantID) {
			log.Printf("[TokenBilling] 租户%d 三桶余额不足，本次降级规则话术", s.tenantID)
			// G-5(2026-09-24)：降级同步进 /metrics 计数器，回归断言不再 grep 日志文件
			metrics.IncAIFallback("quota_exhausted")
			return ai.BuildFallbackReply(s.strategyOutput, s.canPromote), true
		}
	}

	// 模拟模式：直接用策略中心的模板话术兜底
	// 修复：从SystemConfigService读取mock_mode，后台开关即时生效
	// 修复：AI_MOCK_MODE 环境变量应作为模拟模式的权威信号。
	// 原有 GetBool("mock_mode", env) 会被种子写死的系统配置 false 覆盖，导致 env 失效、
	// 开发环境实际走真实 LLM 调用（慢且可能无 key 报错）。改为 env 或 系统配置任一为真即模拟。
	if config.GlobalConfig.AI.MockMode || runtimecfg.SafeCfgBool("mock_mode", false) {
		// FIX-M(2026-09-28)：模拟模式按虚拟用量走**同一条落账腿**，默认 0＝与改造前逐字节一致。
		s.billMockUsage()
		return ai.BuildFallbackReply(s.strategyOutput, s.canPromote), true
	}

	// 检查是否有任何可用的AI模型（降级链中任一 provider 存活即可）
	hasAnyAI := ai.DefaultClient.APIKey != ""
	if ai.SiliconFlowDefaultClient != nil && ai.SiliconFlowDefaultClient.Enabled {
		hasAnyAI = true
	}
	if !hasAnyAI {
		log.Printf("[AI] 无可用AI模型，使用模板兜底")
		metrics.IncAIFallback("no_ai_model") // G-5(2026-09-24)：同上，降级原因走指标不走日志
		return ai.BuildFallbackReply(s.strategyOutput, s.canPromote), true
	}

	// 确认走多模型降级路由
	log.Printf("[AI] 走多模型降级链路，当前路由模型数: %d", len(ai.Router.GetModels()))
	return "", false
}

// detectDialogSignals 对话信号检测：读三个热配置阈值 + 统计轮数/重复问题/非车话题计数。
// 拆出原因：这一段全是只读探测（DB 计数 + chatflow 统计），产出五个计数供
// stripGuidedQuestionsGuards/enforceOffTopicGuards 两个硬拦截段消费，与 prompt 组装无耦合。
func (s *aiReplyStage) detectDialogSignals() {
	// 真实AI模式：用高质量Prompt构建器
	// 修复问题4+6：检测对话轮数和重复话题，动态注入策略指令
	// 4. 引导式对话在冷启动用户N次关键需求回答后关闭（默认5轮，后台可调）
	//    达到阈值后：关闭反问，专注解答+适当介绍ROX品牌/车型/能力
	// 4b. 客户重复性问题超过3次，关闭反问引导式语句，直接走解决陈述
	// 6. 非车话题重复3次及以上后，改语气，关闭引导式反问，认真说回聊到车上
	s.guidedDialogMaxRounds = runtimecfg.SafeCfgInt("guided_dialog_max_rounds", 5)
	s.repeatQuestionMaxTimes = runtimecfg.SafeCfgInt("repeat_question_max_times", 3)
	s.offtopicRepeatMaxTimes = runtimecfg.SafeCfgInt("offtopic_repeat_max_times", 3)

	// 检测对话轮数（客户发了多少条消息）
	// P2-61 修复：原全历史 Find 进内存；dialogRoundCount 仅用于引导式对话阈值判断，
	// 收敛到最近 50 条足够（阈值 5~10 轮）
	dialogRoundCount := 0
	var customerMsgCount []model.Message
	db.DB.Where("customer_id = ? AND sender_type = ?", s.customer.ID, "customer").
		Order("id DESC").Limit(50).Find(&customerMsgCount)
	dialogRoundCount = len(customerMsgCount)
	s.dialogRoundCount = dialogRoundCount

	// 检测重复问题（客户最近的消息和之前的消息相似度）
	s.repeatCount = chatflow.CountSimilarQuestions(s.customer.ID, s.userInput)

	// 检测非车话题重复次数
	s.offtopicRepeatCount = chatflow.CountOffTopicRepeats(s.customer.ID)
	// 胡搅蛮缠分支：总非车话题数和连续在话题数
	s.totalOffTopic = chatflow.CountTotalOffTopic(s.customer.ID)
	s.consecutiveOnTopic = chatflow.CountConsecutiveOnTopic(s.customer.ID)
}

// buildSystemPromptWithKB 构建系统 Prompt（人设+卖点+价格管控+促单锁+到店策略）并注入租户 KB 命中段。
// 拆出原因：这是"进模型的静态指令"唯一产地，KB 注入是纯追加（无命中不加段），
// 与策略指令（每轮变化的部分）分属两个构建器，拆开防两段互相覆写。
func (s *aiReplyStage) buildSystemPromptWithKB() {
	// 1. 系统Prompt（人设 + 卖点知识 + 价格管控 + 促单锁 + 到店转化策略）
	s.hasArrived = s.customer.HasArrived() // 判断客户是否已到店，用于价格管控
	// canPromote已在函数顶部声明，此处不再重复
	s.isStoreVisit = service.IsStoreVisitIntentForTenant(s.customer.TenantID, s.userInput) && !chatflow.IsLeadCaptured(s.customer) // 到店意图且未留资才注入到店策略
	s.modelID = getCustomerModelID(s.customer)
	s.systemPrompt = ai.BuildSystemPrompt(s.customer.TenantID, s.features, s.modelID, s.hasArrived, s.strategyOutput.FinalAnchor, s.canPromote, s.isStoreVisit, chatflow.IsLeadCaptured(s.customer))

	// P2 双层KB：租户自有资料融合检索注入（二元组打分 top3；无命中不加段）
	if kbHits := service.SearchTenantKnowledge(s.customer.TenantID, s.userInput, 3); len(kbHits) > 0 {
		var kbs strings.Builder
		kbs.WriteString("\n【企业知识库参考】以下为该企业自有资料片段，仅供参考；与客户问题相关就自然融入，无关或不确定就别硬套：\n")
		for _, f := range kbHits {
			content := []rune(f.Content)
			if len(content) > 300 {
				content = content[:300]
			}
			kbs.WriteString("- [" + f.Title + "] " + string(content) + "\n")
		}
		s.systemPrompt += kbs.String()
		log.Printf("[KB] 租户%d 命中 %d 条自有知识片段注入 prompt", s.customer.TenantID, len(kbHits))
	}
}

// buildStrategyPrompt 构建本轮策略指令（锚方向+话术参考+条件交换+知识库素材），并按需追加销售路径决策段。
// 拆出原因：销售路径注入依赖调用方经 ctx 下传的决策（A4 实装），判据与策略引擎输出正交，
// 开关关闭时本段整体跳过、prompt 逐字节等价——单独成段方便对照该契约。
func (s *aiReplyStage) buildStrategyPrompt() {
	// 2. 策略指令（锚方向 + 话术参考 + 条件交换 + 知识库素材）
	s.strategyPrompt = ai.BuildStrategyPrompt(s.strategyOutput, s.customer.GetTags(), s.modelID, s.hasArrived, s.canPromote, chatflow.IsLeadCaptured(s.customer))

	// A4 实装(2026-09-23)：销售路径机（engine/flow）输出的每轮消费点——
	// 决策由 OrchestrateReply 评估后经 ctx 下传（strategytypes 中立承载，避免 llm→flow 反向依赖）。
	// 热开关 sales_path_enabled 默认关，关闭时 ctx 恒无值、本段整体跳过，prompt 逐字节等价现状；
	// 开启时仅向策略指令追加「当前阶段→推进目标→下一步动作」结构化约束，不改既有指令分节。
	if sp := strategytypes.SalesPathFromContext(s.ctx); sp != nil && sp.PromptDirective != "" {
		s.strategyPrompt += "\n\n" + sp.PromptDirective
		log.Printf("[销售路径] customer=%d 注入路径决策 %s→%s(信号=%s)",
			s.customer.ID, sp.CurrentStage, sp.TargetStage, sp.ConversionSignal)
	}
}

// assembleMessages 组装消息列表：system → 历史对话 → 当前策略+用户消息。
// 拆出原因：唯一同时消费 prompt 两个产物与 chat_history_rounds 热开关的段——
// 轮数=0 时改注入客户核心信息摘要（避免模型记忆偏移），两条注入路径互斥，集中一处才看得清。
func (s *aiReplyStage) assembleMessages() {
	// 3. 构建对话上下文
	// 对话历史轮数由 system_configs 的 chat_history_rounds 控制（DB 默认 3 轮）
	// =0 时改用核心内容摘要注入 system prompt，避免模型记忆偏移
	// 核心摘要提取：用户需求、看过哪些车、在开什么车、关注点等
	chatHistoryRounds := runtimecfg.SafeCfgInt("chat_history_rounds", 3)
	var historyMessages []ai.ChatMessage
	if chatHistoryRounds > 0 {
		// 如果配置了>0轮，仍用传统对话历史注入
		historyMessages = getConversationHistory(s.customer.TenantID, s.conversationID, chatHistoryRounds)
	}

	// 4. 组装消息列表：system → 历史对话 → 当前策略+用户消息
	messages := make([]ai.ChatMessage, 0, len(historyMessages)+2)

	// 修复问题7：当关闭对话历史注入(chat_history_rounds=0)时，注入客户核心信息摘要
	// 摘要来源：客户画像字段 + 最近消息中的关键信息
	// 不注入完整历史，只提取核心需求/兴趣/关注点，避免模型记忆偏移
	customerContextSummary := ""
	if chatHistoryRounds == 0 {
		customerContextSummary = chatflow.BuildCustomerContextSummary(s.customer, s.conversationID)
	}

	// system prompt：如果有核心摘要+策略调整，追加到system prompt末尾
	systemPromptFinal := s.systemPrompt
	if customerContextSummary != "" {
		systemPromptFinal = s.systemPrompt + "\n\n【客户核心信息摘要】\n" + customerContextSummary
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
		Content: s.strategyPrompt + "\n\n" +
			"客户最新说的话：\n" + s.userInput + "\n\n" +
			"请直接回复客户：",
	})
	s.messages = messages
}

// billTokens 把一次回复的 token 消耗送进「台账 + 三桶扣减」这条唯一落账腿。
//
// 拆出原因（FIX-M, 2026-09-28）：这条腿原先只内联在 callAIAndBill 里，于是模拟模式
// （在真正调模型**之前**就短路返回模板话术）零落账、零扣减——「③免费桶→①订阅额度→②余额」
// 这条商业红线在没有任何模型凭证的环境里**根本不可判**，uat §八 的中间三格等于空转，
// 只有接了真 Key 才验得到。抽成单点后两条路共用同一形态，副作用顺序
// （RecordUsage 台账 → SinkRecordUsage 扣减）与原实现逐字一致，不改任何计量语义。
//
// 扣减额取 usage.TotalTokens（服务端回报的总量），与改造前同一读法——台账内部的
// prompt+completion 只是它的拆分展示，两者可能不等（推理模型的思考 token 只进总量）。
//
// C3 修复(2026-09-14) 保留：gatewayMode 是调用前的预判，免计量必须按**实际服务的 provider**
// 判——降级链若落到本地直连 key 仍跳过计量＝真实消耗零落账（漏钱洞）；
// 反之网关真的服务了这一次调用时本地不得再计一遍（双扣）。
func (s *aiReplyStage) billTokens(provider, modelName string, usage ai.Usage, latencyMs int64) {
	if s.gatewayMode && provider == string(ai.ProviderGateway) {
		return
	}
	billing.RecordUsage(s.tenantID, s.customer.ID, 0, "reply", provider, modelName,
		usage.PromptTokens, usage.CompletionTokens, latencyMs)
	// P1.5 按实际用量三桶顺序扣减（③→①→②；总闸/灰度未开时 no-op）
	// 2026-09-03 计费统一：由异步 `go DeductTokensActual` 改为投递 UsageSink 批量落库，
	// 消除并发下扣减顺序不保证的竞态（每租户每 flush 周期单事务扣减）
	billing.SinkRecordUsage(s.tenantID, int64(usage.TotalTokens))
}

// billMockUsage 模拟模式的虚拟计量（FIX-M, 2026-09-28）。
//
// 键 ai_mock_usage_tokens 出厂 0 ⇒ 本函数直接返回，与改造前行为逐字节一致；
// 只有在模拟模式为真时才被读（真实模式即使误设该键也不生效，防"设了忘关"把真扣减放大）。
// provider/model 记成 mock，usage_ledger 里一眼与人账分开。
//
// 虚拟量落在 **PromptTokens** 而不是只填 TotalTokens：`billing.RecordUsage` 的免计量早退
// 条件是 `prompt<=0 && completion<=0`（本意是"mock/规则兜底路径无用量"），
// 只填 TotalTokens 会让台账整行不写、只剩扣减——账与扣对不上，比不扣更糟。
// 落在 prompt 侧是因为 `RecordUsage` 内部按 prompt+completion 汇总总量，
// completion 留 0（没有任何真实生成内容，编一个输出数只会让台账看着像真账）。
func (s *aiReplyStage) billMockUsage() {
	tokens := runtimecfg.SafeCfgIntForTenant(s.tenantID, "ai_mock_usage_tokens", 0)
	if tokens <= 0 {
		return
	}
	s.billTokens("mock", "mock", ai.Usage{PromptTokens: tokens, TotalTokens: tokens}, 0)
}

// callAIAndBill 调用多模型降级路由并落账/回流素材；失败或空回复时返回 (兜底话术, true)。
// 拆出原因：全链路唯一真金白银的一段——成功路径的副作用序列（IncAISuccess → RecordUsage →
// SinkRecordUsage → Collect → 空回复兜底）顺序即语义（C3 修复以实际服务 provider 判免计量），
// 单独成段防后续重构插队在落账之前的语句把计量链路截断。
func (s *aiReplyStage) callAIAndBill() (string, bool) {
	// 5. 调用AI（走多模型路由，自动降级；stage_models 可为 reply 阶段覆盖专属模型）
	// 修复：从SystemConfigService读取temperature，后台调参即时生效
	aiTemp := runtimecfg.SafeCfgFloat("ai_temperature", ai.DefaultClient.Temperature)
	callStart := time.Now()
	reply, provider, modelName, usage, err := ai.Router.GenerateTextForStage(s.ctx, "reply", s.tenantID, s.messages, aiTemp)
	if err != nil {
		metrics.IncAIFailure() // P1-2：全模型失败计为 AI 失败（成功率分母）
		log.Printf("[AI] 所有模型均调用失败: %v, 降级使用模板回复", err)
		return ai.BuildFallbackReply(s.strategyOutput, s.canPromote), true
	}
	metrics.IncAISuccess() // P1-2：真模型成功返回计为 AI 成功
	// M3 计量落账（异步best-effort）：请求级 token/成本/延迟 → usage_ledger
	// C3 修复(2026-09-14)：gatewayMode 是调用前的预判；降级链若实际落到本地直连 key
	// （provider != gateway），旧逻辑仍跳过计量 → 真实 token 消耗零落账零扣减（漏钱洞）。
	// 以"实际服务的 provider"为准：仅网关侧服务时才免本地计量。（判据本体见 billTokens）
	s.billTokens(provider, modelName, usage, time.Since(callStart).Milliseconds())

	// 数据飞轮：脱敏对话素材回流（P3，供行业包自动迭代）；未配置 Collector.URL 自动丢弃
	service.Collect("material", s.tenantID, map[string]any{
		"stage":        "reply",
		"user_message": s.userInput,
		"reply":        reply,
		"model":        modelName,
	})
	if modelName != "" {
		log.Printf("[AI] 实际使用模型: %s (tokens=%d)", modelName, usage.TotalTokens)
	}

	// 6. 空回复兜底
	if reply == "" {
		return ai.BuildFallbackReply(s.strategyOutput, s.canPromote), true
	}
	s.reply = reply
	return "", false
}

// stripGuidedQuestionsGuards 硬拦截 7/7b：已留资客户与引导关闭条件触发时剥离反问句（改写 s.reply）。
// 拆出原因：两条拦截共享同一动作（StripGuidedQuestions + 变更才打日志），判据互补
// （一个看留资态、一个看未留资的关闭条件），成对维护防其中一条被改动时漏看另一条。
func (s *aiReplyStage) stripGuidedQuestionsGuards() {
	// 7. 硬拦截：已留资客户AI回复中不能含反问句
	// 硬编码：GuidedRemainingRounds>0时表示这是留资后第一条回复，允许反问句通过
	// 留资后第一条反问句由分支B直接回复固定句或AI注入，后续轮次全部剥离
	if chatflow.IsLeadCaptured(s.customer) && s.genConv.GuidedRemainingRounds == 0 {
		stripped := chatflow.StripGuidedQuestions(s.reply)
		if stripped != s.reply {
			log.Printf("[留资硬拦截] 客户%d 已留资，AI回复含反问句，已剥离: %q → %q", s.customer.ID, pii.MaskPhoneInText(s.reply), pii.MaskPhoneInText(stripped))
			s.reply = stripped
		}
	}

	// 7b. 硬拦截：引导式反问关闭条件触发时剥离反问句
	if !chatflow.IsLeadCaptured(s.customer) {
		closeGuided := (s.dialogRoundCount >= s.guidedDialogMaxRounds && s.strategyOutput.FinalAnchor == strategytypes.AnchorNoThrow) ||
			(s.repeatCount >= s.repeatQuestionMaxTimes)
		if closeGuided {
			stripped := chatflow.StripGuidedQuestions(s.reply)
			if stripped != s.reply {
				log.Printf("[引导关闭硬拦截] 客户%d 触发反问关闭条件，已剥离: %q → %q", s.customer.ID, pii.MaskPhoneInText(s.reply), pii.MaskPhoneInText(stripped))
				s.reply = stripped
			}
		}
	}
}

// enforceOffTopicGuards 硬拦截 7c：胡搅蛮缠分支——固定回复替换 + 意向分降权/恢复（改写 s.reply 与 s.customer）。
// 拆出原因：本段是全链路唯一会**写 customers 表**（intent_score）的回复后处理，
// 降权与恢复两条腿共享 consecutiveOnTopic/totalOffTopic 两个计数，副作用与判定纠缠在此收口。
func (s *aiReplyStage) enforceOffTopicGuards() {
	// 7c. 硬拦截：胡搅蛮缠分支
	// 3轮非车话题后关闭引导+固定回复
	// 批四 P2 修正(DEFECT_VERIFY_2026-09-20)：固定文案曾硬编码"只能回答你车和品牌"，
	// 任何行业租户都返回汽车版且绕开行业分流——改走 GetOffTopicReplyForTenant，
	// 汽车族仍是行业口径兜底、非 auto 包/无绑定回中立句；租户配置 industry.offtopic_replies 优先级最高。
	if s.offtopicRepeatCount >= s.offtopicRepeatMaxTimes {
		s.reply = service.GetOffTopicReplyForTenant(s.customer.TenantID, s.userInput)
		log.Printf("[胡搅蛮缠-硬拦截] 客户%d 非车话题%d次 >= 阈值%d，已替换回复",
			s.customer.ID, s.offtopicRepeatCount, s.offtopicRepeatMaxTimes)
	}
	// 10轮以上总非车话题：降低意向分+放慢回复
	if s.totalOffTopic > 10 && s.consecutiveOnTopic < 3 {
		s.customer.IntentScore = s.customer.IntentScore * 0.5
		if s.customer.IntentScore < 0.05 {
			s.customer.IntentScore = 0.05
		}
		db.DB.Model(s.customer).Update("intent_score", s.customer.IntentScore)
		log.Printf("[胡搅蛮缠-降权] 客户%d 非车话题总数%d>10, 意向分降至%.2f",
			s.customer.ID, s.totalOffTopic, s.customer.IntentScore)
	}
	// 恢复：连续3轮以上回到车话题
	if s.consecutiveOnTopic >= 3 {
		restoredScore := s.customer.IntentScore * 2.0
		if restoredScore > 1.0 {
			restoredScore = 1.0
		}
		if restoredScore > s.customer.IntentScore {
			s.customer.IntentScore = restoredScore
			db.DB.Model(s.customer).Update("intent_score", s.customer.IntentScore)
			log.Printf("[胡搅蛮缠-恢复] 客户%d 连续%d轮在话题, 意向分恢复至%.2f",
				s.customer.ID, s.consecutiveOnTopic, s.customer.IntentScore)
		}
	}
}

// stripAllQuestionsWhenGuidedClosed 硬拦截 7d：引导关闭后全面剥离所有反问句（改写 s.reply）。
// 拆出原因：判据读的是会话上的引导终态（GuidedDisabled && 剩余轮数=0），剥离动作
// 比 7/7b 的 StripGuidedQuestions 更狠（StripAllQuestions），必须保持在全部改写之后执行。
func (s *aiReplyStage) stripAllQuestionsWhenGuidedClosed() {
	// 7d. 硬拦截：引导关闭后，全面剥离所有反问句（无论是否已留资）
	// 覆盖更多中文反问模式：什么、怎么、哪、有没有、是不是等
	// 引导关闭后AI只做陈述句介绍产品/了解需求，不再反问
	if s.genConv.GuidedDisabled && s.genConv.GuidedRemainingRounds == 0 {
		oldReply := s.reply
		s.reply = chatflow.StripAllQuestions(s.reply)
		if s.reply != oldReply {
			log.Printf("[引导关闭-全面剥离] 客户%d 引导已关闭，全面剥离反问句: %q → %q", s.customer.ID, pii.MaskPhoneInText(oldReply), pii.MaskPhoneInText(s.reply))
		}
	}
}

// knowledgeBlindspotFallback 知识库盲点兜底检测（步骤8）；hit=true 时返回话术即终稿。
// 拆出原因：热开关可整体关停、且命中即**替换**（非改写）当前回复——它是硬拦截链之后
// 最后一道"整体作废 AI 回复"的短路，放在 defer 递减之前必须保持 return 形态。
func (s *aiReplyStage) knowledgeBlindspotFallback() (string, bool) {
	// 8. 知识库盲点兜底检测
	// 修复问题5：触及知识库盲点后，模型一直在瞎回复兜圈子
	// 检测AI回复中是否包含不确定/兜圈子的信号词，触发后用盲点兜底话术替换
	// 兜底话术：关闭引导式提问，直接回"好的，稍等，这个问题我查一下"
	// 如果客户继续提问相关问题："不好意思我现在忙，要不您到店来体验下？"
	if runtimecfg.SafeCfgBool("knowledge_blindspot_fallback_enabled", true) {
		blindspotReply := chatflow.DetectKnowledgeBlindspot(s.reply, s.userInput)
		if blindspotReply != "" {
			log.Printf("[知识库盲点兜底] 客户提问触及盲点，原始AI回复含不确定信号，替换为兜底话术")
			return blindspotReply, true
		}
	}
	return "", false
}
