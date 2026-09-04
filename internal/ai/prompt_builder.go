// Package ai LLM 调用统一入口：Prompt 构建、智谱/硅基流动双客户端、多模型降级路由与 Token 计量。
package ai

import (
	"ai-scrm/internal/cache"
	"ai-scrm/internal/model"
	"ai-scrm/internal/service"
	"ai-scrm/internal/strategytypes"
	"fmt"
	"strings"
)

// ============================================================
// Prompt构建器
// 功能：把策略引擎的输出 + 客户画像 + 卖点知识库 → 组装成高质量的系统Prompt
// 设计原则：控制长度，保证速度；策略引擎定方向，大模型定表达
//
// 价格管控：
//   - 未到店客户（journey_stage < arrived）：Prompt中明确写死"不得提及具体价格数字"
//   - 知识库中的价格字段不注入
//   - 价格追问引导话术：引导到店/留资
// ============================================================

// BuildSystemPrompt 构建系统提示词（人设 + 规则 + 核心卖点 + 车型知识库）
// 刻意精简：控制长度，避免prompt太长拖慢响应
// modelID: 当前车型ID，用于注入品牌和核心参数
// hasArrived: 客户是否已到店，用于价格管控
// finalAnchor: 策略锚类型，不抛锚时跳过卖点和参数注入，避免AI主动推车
// canPromote: 是否允许促单，false时禁止"专属优惠""限时优惠"等促单话术
// isStoreVisit: 客户是否表达到店/体验意图，true时推留资转化策略
// leadCaptured: 客户是否已留资（journey_stage>=lead_captured），硬规则：
//
//	已留资及以后，禁止再输出"促到店/约试驾/问姓名电话"类话术，只管好好介绍产品
func BuildSystemPrompt(tenantID uint, features []model.Feature, modelID uint, hasArrived bool, finalAnchor int, canPromote bool, isStoreVisit bool, leadCaptured bool) string {
	var sb strings.Builder

	// 修复：语气风格从后台配置读取(tone_style)，无需改代码发版
	// neutral=冷静专业 / warm=略带热情 / enthusiastic=热情主动
	toneStyle := service.DefaultSystemConfigService.GetString("tone_style", "warm")

	// 人设——优先行业包配置，缺省按tone_style动态调整
	// 泛行业化（P2）：industry.salesperson 由行业包注入，空回退内置汽车人设
	if persona := service.IndustrySalespersonForTenant(tenantID); persona != "" {
		sb.WriteString(persona)
	} else {
		sb.WriteString(getTonePersona(toneStyle))
	}
	sb.WriteString("\n")

	// P2 双层KB（2026-08-26）：行业/企业包定制指令注入（prompts.json → pack_prompts_{code} 键）
	// 顺序：行业在前、企业在后（企业更贴近该租户，放后面权重感更强）
	for _, instruction := range service.GetBoundPackPrompts(tenantID) {
		sb.WriteString("【定制指令】\n")
		sb.WriteString(instruction)
		sb.WriteString("\n\n")
	}

	// P2 行业包参数约束 + 话术心态（params.json / mindset.json → pack_params_/pack_mindset_{code} 键）
	if params := service.GetBoundPackParams(tenantID); len(params) > 0 {
		sb.WriteString("【行业包参数约束】\n")
		for _, p := range params {
			sb.WriteString("· " + p + "\n")
		}
		sb.WriteString("\n")
	}
	if mindsets := service.GetBoundPackMindset(tenantID); len(mindsets) > 0 {
		sb.WriteString("【行业包话术心态】\n")
		for _, m := range mindsets {
			sb.WriteString("· " + m + "\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString("【语气铁律】\n")
	sb.WriteString("1. 用口语，不用书面语。说「你」不说「您」，说「咱」不说「我们」\n")
	sb.WriteString("2. 短句为主，一句不超过15个字，不用逗号连长句\n")
	sb.WriteString("3. 禁止「非常感谢」「感谢您的关注」「为您推荐」「亲」「哈」「呢」「呀」「哦」「啦」「吖」等客服腔和卖萌词\n")
	sb.WriteString("4. 禁止用排比、对仗、形容词堆砌、emoji表情\n")
	// 修复：语气铁律第5条根据tone_style动态调整口语词/情绪词
	sb.WriteString(getToneRule5(toneStyle))
	sb.WriteString("\n")
	sb.WriteString("6. 不说「有什么可以帮您」，说「你想了解啥」\n")
	sb.WriteString("7. 绝对不能让客户觉得在跟AI聊天\n")
	sb.WriteString("8. 【称呼铁律】客户留了姓名时，用「姓+先生/女士」称呼，如客户说「小张」→叫「张先生」，客户说「李美」→叫「李女士」。绝不直接叫昵称（如「小张」「美美」），显得不专业。不确定性别时用「张先生/女士」\n")
	// 修复：领域约束句行业化（P2）——industry.domain_constraint 覆盖，缺省回退汽车版
	// 旧硬编码：你只聊车、品牌、用车生活相关的话题…（保留为回退文案）
	sb.WriteString("9. 【领域约束】" + domainConstraintText(tenantID) + "\n")
	sb.WriteString("10. 【称呼铁律-硬编码】不知道客户真实姓名时，用「您好」开头。绝对禁止以「访客xxx」「访客_xxxx」等临时ID称呼客户，这会让客户觉得在被AI敷衍\n\n")

	// 核心规则——根据锚类型和留资状态区分
	if finalAnchor == strategytypes.AnchorNoThrow && !leadCaptured {
		// 不抛锚且未留资：倾听探索模式，以了解客户需求为主
		// 除非客户表现出强烈的讨论车的欲望，否则不主动推车细节
		sb.WriteString("【规则-倾听探索模式】\n")
		// 修复：倾听探索模式字数限制根据tone_style动态调整
		sb.WriteString(getToneListenRule1(toneStyle))
		sb.WriteString("2. 以倾听为主，用开放式问题了解客户：购车用途、预算范围、关注点、用车时间、家庭成员等\n")
		sb.WriteString("3. 不主动介绍车型卖点、配置、参数，除非客户主动问\n")
		sb.WriteString("4. 不提及具体价格数字\n")
		sb.WriteString("5. 结尾抛一个问题引导客户多说，而不是引导了解产品\n")
		sb.WriteString("6. 客户连发多条消息时，如果是同一句话拆开的，当成一个问题理解；如果是不同问题，逐个自然回答\n")
		sb.WriteString("7. 回复中自然嵌入客户说过的关键词，让客户感觉你在认真听他说话\n")
	} else {
		// 抛锚：正常策略引导模式
		sb.WriteString("【规则】\n")
		sb.WriteString("1. 2-3句话，60字以内，别啰嗦\n")
		// 2. 不同锚类型在BuildStrategyPrompt中分别给出方向，此处不统一加钩子\n\t\t// 了解阶段（AnchorNoThrow）用倾听模式中的开放问题\n\t\t// 懂我/兴趣爆发/促到店阶段由策略引擎的锚点和话术模板提供方向
		sb.WriteString("3. 不贬低竞品，不说绝对化的话\n")
		sb.WriteString("4. 严格按给定策略锚点走，别自己换话题\n")
		sb.WriteString("5. 模板是参考，用口语说出来，别照念\n")
		sb.WriteString("6. 客户连发多条消息时，如果是同一句话拆开的，当成一个问题理解；如果是不同问题，逐个自然回答\n")
		sb.WriteString("7. 回复中自然嵌入客户说过的关键词，让客户感觉你在认真听他说话\n")

		// 价格管控：未到店客户禁止提及具体价格
		if !hasArrived {
			sb.WriteString("6. 【重要】不得提及任何具体价格数字、优惠金额、金融方案具体数字\n")
			// 询价话术：已留资客户→体验后报价（不再约试驾，已经约上了）\n\t\t\t// 未留资客户→引导到店试驾后报价（话术可AI自由发挥）\n\t\t\tif leadCaptured {\n\t\t\t\tsb.WriteString(\"7. 客户问价格时，说价格得看配置和需求来定。话术参考：「等您试驾体验过后，我再根据您的配置需求做个报价」。不要反问「您什么时候试驾」「您对配置有什么要求」——客户已经留过资了，试驾已经在安排中，不需要再约\\n\")\n\t\t\t} else {\n\t\t\t\tsb.WriteString(\"7. 客户问价格时，引导到店试驾后出报价，话术参考：「要不帮您约个试驾，体验过后我再根据您的配置需求做个报价，怎么样呀」。禁止直接报价、禁止说具体数字\\n\")\n\t\t\t}
		}
	}

	sb.WriteString("\n")

	// ============================================================
	// 硬规则：已留资及以后，禁止任何促到店/约试驾/问联系方式的话术
	// journey_stage ∈ {lead_captured, arrived, ordered, delivered}
	// 这是状态机层面的硬拦截，不依赖模型自觉遵守某段话术描述
	// ============================================================
	if leadCaptured {
		sb.WriteString("【硬规则-已留资，禁止促到店】\n")
		sb.WriteString("客户已经留过资料/约过到店试驾，这件事已经办完了：\n")
		sb.WriteString("1. 绝对不要再邀约到店、约试驾、问姓名电话——已经问过、已经留过，别再问第二次\n")
		sb.WriteString("2. 不要说「方便留个联系方式吗」「啥时候方便来店里」这类话\n")
		sb.WriteString("3. 就专心当好一个产品顾问：回答配置、性能、用车场景、售后政策等问题，把车介绍好\n")
		sb.WriteString("4. 如果客户主动问到店细节（比如几点、怎么走），可以回答，但别反过来主动催促\n\n")
	}

	// ============================================================
	// 到店/体验意图 → 留资转化策略
	// 客户想去店里看→AI不应该推车参数，应该引导留资+召唤人工确认
	// 留资完成后通知人工销售确认到店时间和需求
	// ============================================================
	if isStoreVisit && !leadCaptured {
		sb.WriteString("【到店转化策略】\n")
		sb.WriteString("客户表达到店/体验/试驾意图，执行留资转化：\n")
		sb.WriteString("1. 一次性问齐留资信息，别一条一条问！一条消息里问完：姓名、电话、方便到店的日期时间段、几个人来、有啥特殊需求（比如想试驾/看某配置）\n")
		sb.WriteString("2. 参考话术：「方便的话留个信息呗，姓名+电话，还有你大概啥时候方便过来？几个人？我帮你安排」\n")
		sb.WriteString("3. 客户给了部分信息后，只问还没给的，已经给过的别重复问\n")
		sb.WriteString("4. 留资完成后，说「收到，我帮你安排下，稍后给你确认时间」\n")
		sb.WriteString("5. 禁止推车参数、配置细节、优惠信息——到店体验场景核心是线下转化\n")
		sb.WriteString("6. 禁止「专属优惠」「限时优惠」「特价」等促单话术\n")
		sb.WriteString("7. 语气像朋友帮忙预约，别像销售推销\n\n")
	}

	// ============================================================
	// 促单锁（CanPromote=false时）
	// 业务规则：只有已到店+已报价之后才允许促单
	// 到店之前必须渐进式引导：了解→懂我→兴趣信任→促到店
	// 跳步促单会吓退客户
	// ============================================================
	if !canPromote {
		sb.WriteString("【到店前渐进式引导-禁止促单】\n")
		sb.WriteString("客户尚未到店报价，必须按渐进式链路引导，禁止跳步促单：\n")
		sb.WriteString("· 了解阶段：倾听客户需求，不推销，用开放式问题了解购车用途、预算、关注点\n")
		sb.WriteString("· 懂我阶段：复述客户需求让他感觉被理解，针对性推荐但不逼单\n")
		sb.WriteString("· 兴趣信任阶段：分享真实车主故事、产品亮点，建立信任，引导到店体验\n")
		if !leadCaptured {
			sb.WriteString("· 促到店：引导客户来店看实车、试驾，而不是线上促单\n")
		}
		sb.WriteString("· 禁止\"专属优惠\"\"限时优惠\"\"特价\"\"折扣\"\"降价\"\"今天定有优惠\"等促单话术\n")
		sb.WriteString("· 禁止条件交换、制造紧迫感——这些只在已报价后才用\n")
		sb.WriteString("· 记住：到店前推优惠=吓退客户，到店后推优惠=促进成交\n\n")
	}

	// ============================================================
	// 品牌车型全览：不依赖抛锚条件，只要客户绑定了品牌就注入
	// 修复问题1：客户问"有几款车"时策略引擎不抛锚，但AI需要知道品牌下所有车型
	// 原因：车型全览在if finalAnchor != strategytypes.AnchorNoThrow块内，
	// 不抛锚时整个车型全览段被跳过，AI无法回答"有几款车"的问题
	// 修复：把车型全览移到抛锚判断之前，车型核心参数和卖点仍在抛锚后注入
	// ============================================================
	if modelID > 0 {
		carModel := cache.DefaultKnowledgeCache.GetModelByID(modelID)
		if carModel != nil {
			brand := cache.DefaultKnowledgeCache.GetBrandByID(carModel.BrandID)
			brandName := ""
			if brand != nil {
				brandName = brand.Name
			}
			if carModel.BrandID > 0 {
				brandModels := cache.DefaultKnowledgeCache.GetModelsByBrandID(carModel.BrandID)
				if len(brandModels) > 1 {
					sb.WriteString(fmt.Sprintf("\n[%s 车型全览]\n", brandName))
					for _, bm := range brandModels {
						bmSpecs := cache.DefaultKnowledgeCache.GetTopSpecsByModelID(bm.ID, 8)
						priceRange := ""
						seats := ""
						evRange := ""
						combinedRange := ""
						for _, sp := range bmSpecs {
							pn := sp.ParamName
							pv := sp.ParamValue
							switch {
							case sp.IsPrice:
								if priceRange == "" {
									priceRange = pv + sp.ParamUnit
								}
							case pn == "座位数" || pn == "座椅布局" || pn == "座位":
								seats = pv + sp.ParamUnit
							case pn == "纯电续航" || pn == "纯电续航里程" || pn == "CLTC纯电续航":
								evRange = pv + sp.ParamUnit
							case pn == "综合续航" || pn == "CLTC综合续航" || pn == "综合续航里程":
								combinedRange = pv + sp.ParamUnit
							}
						}
						info := fmt.Sprintf("- %s", bm.Name)
						if priceRange != "" {
							info += fmt.Sprintf(": %s起", priceRange)
						}
						if seats != "" {
							info += fmt.Sprintf(", %s座", seats)
						}
						if evRange != "" {
							info += fmt.Sprintf(", 纯电%s", evRange)
						}
						if combinedRange != "" {
							info += fmt.Sprintf(", 综合续航%s", combinedRange)
						}
						sb.WriteString(info + "\n")
					}
					sb.WriteString("客户问有几款车时，按上面列出的如实回答。\n")
				}
			}
		}
	}

	// 不抛锚时跳过产品卖点和车型参数注入
	// 原因：不抛锚=倾听探索阶段，注入卖点会让AI主动推车
	// 只有客户表现出兴趣（抛锚时）才注入产品信息
	// 例外：已留资客户不受此限制——已表明购车意向，AI应能用知识库回答
	// 注意：车型全览已在上方注入，不受抛锚条件限制
	if finalAnchor != strategytypes.AnchorNoThrow || leadCaptured {
		// 核心卖点（只列名称，控制长度）
		sb.WriteString("【产品卖点】\n")
		count := 0
		for _, f := range features {
			if f.Status != 1 {
				continue
			}
			sb.WriteString(fmt.Sprintf("· %s\n", f.FeatureName))
			count++
			if count >= 5 {
				break
			}
		}
		if count == 0 {
			sb.WriteString("· 专业越野性能、安全配置齐全\n")
		}

		// 车型知识库注入（核心参数，给AI提供真实数据支撑）
		// 所有抛锚场景都注入，不只是对比锚——确保AI说的参数都是真实的
		if modelID > 0 {
			carModel := cache.DefaultKnowledgeCache.GetModelByID(modelID)
			if carModel != nil {
				brand := cache.DefaultKnowledgeCache.GetBrandByID(carModel.BrandID)
				brandName := ""
				if brand != nil {
					brandName = brand.Name
				}
				specs := cache.DefaultKnowledgeCache.GetTopSpecsByModelID(modelID, 8)
				if len(specs) > 0 {
					sb.WriteString(fmt.Sprintf("\n[%s %s - 核心参数]\n", brandName, carModel.Name))
					for _, spec := range specs {
						// 价格管控：未到店客户跳过价格类参数
						if !hasArrived && spec.IsPrice {
							continue
						}
						unit := spec.ParamUnit
						sb.WriteString(fmt.Sprintf("- %s: %s%s\n", spec.ParamName, spec.ParamValue, unit))
					}
				}

				// 品牌车型全览已移到if块外（上方），不依赖抛锚条件
				// 修复问题1：客户问"有几款车"时不抛锚，但AI需知道所有车型
			}
		}
	}

	return sb.String()
}

// BuildStrategyPrompt 构建本轮策略指令
// 告诉AI：这一轮要用什么锚、什么话术方向、有没有条件交换
// 如果是对比锚，自动从知识库注入对比素材
// hasArrived: 客户是否已到店，用于价格管控（未到店过滤价格信息）
// canPromote: 是否允许促单，false时禁止条件交换话术
// leadCaptured: 是否已留资，已留资客户不注入倾听探索指令（直接回答，不反问）
func BuildStrategyPrompt(
	strategyOutput *strategytypes.StrategyOutput,
	customerTags []string,
	modelID uint,
	hasArrived bool,
	canPromote bool,
	leadCaptured bool,
) string {
	var sb strings.Builder

	// 锚方向
	sb.WriteString(fmt.Sprintf("【策略锚点】%s\n",
		strategytypes.GetAnchorName(strategyOutput.FinalAnchor)))

	// 不抛锚时注入倾听探索指令，覆盖模板话术
	// 不抛锚=客户还在初期，AI应该先了解需求，而不是引导了解产品
	// 已留资客户跳过倾听探索——客户已表明意向，直接回答不开反问
	if strategyOutput.FinalAnchor == strategytypes.AnchorNoThrow && !leadCaptured {
		sb.WriteString("【倾听探索指令】\n")
		sb.WriteString("你现在处于倾听探索模式：\n")
		sb.WriteString("· 先了解客户：购车用途（家用/商用/越野）、预算范围、关注点（安全/空间/动力/智能）、用车时间、家庭成员\n")
		sb.WriteString("· 用开放式问题引导，例如「您主要是想买来日常通勤还是周末出去玩？」\n")
		sb.WriteString("· 除非客户主动问车，否则不介绍任何车型配置和卖点\n")
		sb.WriteString("· 客户提到具体需求时，简短认可后再追问细节\n\n")
	}

	// 锚类型专用指令——硬编码：不同锚=不同对话阶段
	// AnchorDisassemble(拆解锚=2)=兴趣爆发阶段：客户已透露需求，AI介绍匹配产品
	// AnchorCompare(对比锚=3)=促到店阶段：客户在对比，AI引导到店体验
	// 其他非NoThrow锚：按模板走，均不反问客户
	switch strategyOutput.FinalAnchor {
	case strategytypes.AnchorDisassemble:
		sb.WriteString("【兴趣爆发-拆解指令】\n客户已经透露了需求，这一轮你要：\n· 用已知的客户需求信息，介绍匹配的车型配置和卖点\n· 不要说「您觉得怎么样」「您感兴趣吗」等反问句\n· 用陈述句直接告诉客户这款车能怎么满足他的需求\n\n")
	case strategytypes.AnchorCompare:
		sb.WriteString("【促到店-对比指令】\n客户在对比不同车型，这一轮你要：\n· 用知识库素材客观介绍差异，突出本车优势\n· 话术自然引导到店看实车体验，但不要反问「您什么时候来」\n· 用陈述句表达「您可以到店来看看，实车感受更直观」\n\n")
	default:
		if strategyOutput.FinalAnchor != strategytypes.AnchorNoThrow {
			sb.WriteString("【销售推进指令】\n这一轮按策略锚点和话术模板走，用陈述句推进，不反问客户\n\n")
		}
	}

	// 话术参考
	if strategyOutput.PromptText != "" {
		sb.WriteString("【话术参考】\n")
		sb.WriteString(strategyOutput.PromptText)
		sb.WriteString("\n\n")
	}

	// 钩子方向
	if strategyOutput.HookText != "" {
		sb.WriteString("【钩子】\n")
		sb.WriteString(strategyOutput.HookText)
		sb.WriteString("\n\n")
	}

	// 条件交换（触发时才加）
	// 促单锁：CanPromote=false时，禁止条件交换话术
	if strategyOutput.ExchangeFlag && strategyOutput.ExchangeType != "" && canPromote {
		sb.WriteString("【条件交换】\n")
		sb.WriteString(fmt.Sprintf("交换类型：%s。", exchangeTypeText(strategyOutput.ExchangeType)))
		sb.WriteString("在回复末尾自然带出交换条件，比如「您要是今天能定，我帮您申请个金融优惠」。注意：有前提，不能无条件给。\n\n")
	}

	// 客户标签
	if len(customerTags) > 0 {
		sb.WriteString(fmt.Sprintf("【客户标签】%s\n", strings.Join(customerTags, "、")))
	}

	sb.WriteString("\n直接回复客户：")

	return sb.String()
}

// BuildConversationHistory 把历史消息转换成AI对话格式
// 只保留最近N轮，避免token爆炸
func BuildConversationHistory(messages []model.Message, maxRounds int) []ChatMessage {
	if len(messages) == 0 {
		return nil
	}

	takeCount := maxRounds * 2
	if takeCount > len(messages) {
		takeCount = len(messages)
	}

	startIdx := len(messages) - takeCount
	recentMsgs := messages[startIdx:]

	result := make([]ChatMessage, 0, len(recentMsgs))
	for _, msg := range recentMsgs {
		role := "user"
		if msg.SenderType == "ai" || msg.SenderType == "human" {
			role = "assistant"
		}
		result = append(result, ChatMessage{
			Role:    role,
			Content: msg.Content,
		})
	}

	return result
}

// exchangeTypeText 交换类型的中文描述
func exchangeTypeText(exchangeType string) string {
	switch exchangeType {
	case "price":
		return "价格优惠"
	case "payment":
		return "金融方案/付款方式"
	case "spec":
		return "配置/规格升级"
	case "service":
		return "售后/保养服务"
	case "delivery":
		return "优先提车"
	default:
		return exchangeType
	}
}

// ============================================================
// 知识库Prompt注入
// 为什么需要？策略引擎选好方向后，还需要具体的产品知识支撑话术
// 让AI说的话有根有据，而不是凭空编造
// ============================================================

// BuildKnowledgePrompt 根据车型+竞品品牌动态注入知识库内容
// 控制长度，只注入最相关的Top N条
// 用在对比锚场景下，给AI提供具体的对比素材
// hasArrived: 客户是否已到店，未到店则过滤价格类信息
func BuildKnowledgePrompt(modelID uint, competitorBrand string, hasArrived bool) string {
	var sb strings.Builder

	hasContent := false

	// 如果传了modelID，注入该车型的核心规格参数（Top10）
	// 价格管控：未到店客户过滤掉价格类参数（is_price=true的）
	if modelID > 0 {
		specs := cache.DefaultKnowledgeCache.GetTopSpecsByModelID(modelID, 10)
		if len(specs) > 0 {
			// 获取车型名称
			carModel := cache.DefaultKnowledgeCache.GetModelByID(modelID)
			modelName := ""
			if carModel != nil {
				modelName = carModel.Name
			}

			sb.WriteString(fmt.Sprintf("【产品参数 - %s】\n", modelName))
			count := 0
			for _, spec := range specs {
				// 价格管控：未到店客户跳过价格类参数
				if !hasArrived && spec.IsPrice {
					continue
				}
				unit := spec.ParamUnit
				if unit == "" {
					unit = ""
				}
				sb.WriteString(fmt.Sprintf("· %s: %s%s\n", spec.ParamName, spec.ParamValue, unit))
				count++
			}
			if count > 0 {
				sb.WriteString("\n")
				hasContent = true
			}
		}
	}

	// 如果传了competitorBrand，注入竞品对比知识
	// 价格管控：未到店客户过滤含价格的对比
	if competitorBrand != "" && modelID > 0 {
		compares := cache.DefaultKnowledgeCache.GetComparesByModelAndBrand(modelID, competitorBrand)
		if len(compares) > 0 {
			hasCompareContent := false
			sb.WriteString("【竞品对比素材】\n")
			for _, cmp := range compares {
				// 价格管控：未到店客户跳过含价格的对比
				if !hasArrived && cmp.ContainsPrice {
					continue
				}
				items := cmp.GetCompareItems()
				// 只取前5条对比项，控制长度
				showCount := 5
				if len(items) < showCount {
					showCount = len(items)
				}
				sb.WriteString(fmt.Sprintf("vs %s %s (%s):\n",
					cmp.CompetitorBrand, cmp.CompetitorModel, cmp.CompareType))
				for i := 0; i < showCount; i++ {
					item := items[i]
					sb.WriteString(fmt.Sprintf("  - %s: 我们%s vs 对方%s → %s\n",
						item.Aspect, item.OurValue, item.TheirValue, item.Conclusion))
				}
				hasCompareContent = true
			}
			if hasCompareContent {
				sb.WriteString("\n")
				hasContent = true
			}
		}
	}

	// 如果有知识库内容，加一条使用规则
	if hasContent {
		sb.WriteString("【知识使用规则】用上面的参数和对比素材支撑你的话术，不要凭空编造参数。\n\n")
	}

	return sb.String()
}

// BuildFallbackReply 构建兜底回复
// AI调用失败时，直接用策略模板话术兜底
// canPromote: 是否允许促单，false时禁止"专属优惠"等促单话术（与策略引擎硬锁一致）
func BuildFallbackReply(strategyOutput *strategytypes.StrategyOutput, canPromote bool) string {
	reply := strategyOutput.PromptText

	if strategyOutput.HookText != "" {
		reply += "\n" + strategyOutput.HookText
	}

	// 条件交换兜底
	// 促单锁：CanPromote=false时，兜底回复也不能推"专属优惠"等促单话术
	// 修复：原来BuildFallbackReply无条件推优惠，到店前兜底也会说"专属优惠"
	if strategyOutput.ExchangeFlag && strategyOutput.ExchangeType != "" && canPromote {
		switch strategyOutput.ExchangeType {
		case "price", "payment":
			reply += "\n对了，你要是近期打算定，我帮你问个优惠。"
		case "spec":
			reply += "\n你订得早的话，我帮你争取早点提车。"
		case "service":
			reply += "\n你确定要的话，我送你3年保养。"
		}
	}

	if reply == "" {
		reply = "行，了解了。你还想聊啥？"
	}

	return reply
}

// ============================================================
// 语气风格辅助函数
// 修复：语气风格从后台配置(tone_style)读取，动态调整prompt
// neutral=冷静专业 / warm=略带热情 / enthusiastic=热情主动
// ============================================================

// domainConstraintText 领域约束句子（泛行业化 P2）
// 行业包可通过 industry.domain_constraint 配置「只聊什么」，缺省回退汽车版文案
func domainConstraintText(tenantID uint) string {
	if s := service.IndustryDomainConstraintForTenant(tenantID); s != "" {
		return s
	}
	return "你只聊车、品牌、用车生活相关的话题。客户问算法题、火箭发射、股票量化、写代码等无关话题时，不正面回答，自然引导回车：「这个我还真不太懂，不过你说的这个让我想到，你是不是对智能化挺感兴趣的？咱车的智能座舱你可能会有兴趣」或「哈哈这块我不太行，咱们还是聊聊你用车的事吧」。绝不装全能、绝不硬答无关领域"
}

// getTonePersona 根据语气风格返回人设描述
func getTonePersona(tone string) string {
	switch tone {
	case "enthusiastic":
		return "你是越野SUV品牌的销售顾问，微信聊天风格，热情实在，有感染力。"
	case "warm":
		return "你是越野SUV品牌的销售顾问，微信聊天风格，真诚接地气，有点热乎劲。"
	default: // neutral
		return "你是越野SUV品牌的销售顾问，微信聊天风格，真诚接地气。"
	}
}

// getToneRule5 根据语气风格返回语气铁律第5条（口语词/情绪词）
func getToneRule5(tone string) string {
	switch tone {
	case "enthusiastic":
		return "5. 像老朋友发微信：直接、热情、有感染力，多用情绪词和肯定词（「对」「确实」「太好了」「给力」「靠谱」「可以啊」「牛」「实在」「绝了」「漂亮」「必须的」「放心」）"
	case "warm":
		return "5. 像朋友发微信：直接、自然、带点热情，用口语词和情绪词（「对」「确实」「还行」「能打」「挺好」「不错」「给力」「靠谱」「可以啊」「牛」「实在」）"
	default: // neutral
		return "5. 像朋友发微信：直接、自然，偶尔用口语词（「对」「确实」「还行」「能打」）"
	}
}

// getToneListenRule1 根据语气风格返回倾听探索模式第1条（字数限制）
func getToneListenRule1(tone string) string {
	switch tone {
	case "enthusiastic":
		return "1. 简短回应，1-3句话，50字以内，先给情绪反馈再抛问题\n"
	case "warm":
		return "1. 简短回应，1-3句话，45字以内，可以带一句情绪反馈再抛问题\n"
	default: // neutral
		return "1. 简短回应，1-2句话，30字以内\n"
	}
}
