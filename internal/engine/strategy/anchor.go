package strategy

import (
	"ai-scrm/config"
	"ai-scrm/internal/model"
	"ai-scrm/internal/service"
	"ai-scrm/internal/strategytypes"
	"ai-scrm/pkg/utils"
	"log"
	"strings"
)

// ============================================================
// 策略中心 - 锚打分 & softmax & 软降级
// 对应线上推理7步公式的 Step1 / Step2 / Step3
// ============================================================

// Step1_CalcAnchorScores 计算各锚类型的打分
// score_a = w_a · f_a(T, S)
//
// 核心逻辑：
//   - 每个锚类型有一组权重 w_a
//   - f_a(T, S) 是特征函数，从T向量和S状态中提取特征
//   - 加权求和得到每个锚的原始分数
//
// 为什么这么设计？
//   - 线性加权模型简单可解释，方便调参
//   - 权重可以通过AB测试不断优化
//   - 特征函数可以不断扩充
func Step1_CalcAnchorScores(tVector [32]float64, state model.SessionState) [AnchorCount]float64 {
	var scores [AnchorCount]float64

	// 从T向量和S状态中提取特征值
	intentScore := tVector[0]            // T[0] 意向分
	trustLevel := tVector[6]             // T[6] 信任度
	priceSens := tVector[1]              // T[1] 价格敏感度
	stage := float64(state.CurrentStage) // 心智阶段
	hookRate := state.HookRate           // 接钩率

	// 修复：锚权重从硬编码→后台可调
	// 后台改权重→热加载→下次推理立即生效，不需要改代码发版
	// 读取失败时fallback到DefaultAnchorWeights（保持原有行为）
	var anchorWeights [AnchorCount]AnchorWeight
	var weightsFromConfig []AnchorWeight
	if service.DefaultSystemConfigService.GetJSON("anchor_weights", &weightsFromConfig) && len(weightsFromConfig) == AnchorCount {
		for i := 0; i < AnchorCount; i++ {
			anchorWeights[i] = weightsFromConfig[i]
		}
		// 防御（2026-08-26）：全零权重=反序列化失败的产物，回落默认而非退化打分
		allZero := true
		for _, w := range anchorWeights {
			if w.IntentScoreWeight != 0 || w.TrustWeight != 0 || w.HookRateWeight != 0 ||
				w.StageWeight != 0 || w.PriceSensWeight != 0 || w.BaseBias != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			anchorWeights = DefaultAnchorWeights
			log.Printf("[策略引擎] Step1: 后台锚权重为全零(反序列化异常)，回落默认值")
		} else {
			log.Printf("[策略引擎] Step1: 使用后台配置的锚权重")
		}
	} else {
		anchorWeights = DefaultAnchorWeights
		log.Printf("[策略引擎] Step1: 后台锚权重配置缺失或格式错误，使用硬编码默认值")
	}

	// 修复：首轮规则从硬编码→后台可调
	// first_round_nothrow_bonus：首轮不抛锚加分（默认5.0）
	// first_round_compare_penalty：首轮对比锚及以上减分（默认-3.0）
	firstRoundNoThrowBonus := service.DefaultSystemConfigService.GetFloat("first_round_nothrow_bonus", 5.0)
	firstRoundComparePenalty := service.DefaultSystemConfigService.GetFloat("first_round_compare_penalty", -3.0)

	// 对每个锚类型计算加权分数
	for a := 0; a < AnchorCount; a++ {
		w := anchorWeights[a]

		// score = w_intent * intent + w_trust * trust + w_hook * hook_rate + w_stage * stage + w_price * price_sens + bias
		score := w.IntentScoreWeight*intentScore +
			w.TrustWeight*trustLevel +
			w.HookRateWeight*hookRate +
			w.StageWeight*(stage/5.0) + // 阶段归一化到0-1
			w.PriceSensWeight*priceSens +
			w.BaseBias

		// 特殊规则：第一次对话（attempts=0），强力偏向不抛锚
		// 修复历程：
		//   v1：不抛锚-0.5（方向反了，寒暄直接开大）
		//   v2：不抛锚+3.0，同类锚+2.0（同类锚分数还是压过不抛锚）
		//   v3（当前）：不抛锚+5.0，同类/场景锚+0（确保数值上不抛锚压住同类锚）
		// 同时有寒暄检测（IsGreeting）做语义级双重保险
		// 修复：首轮规则值从硬编码→读后台配置，后台调参即时生效
		if state.Attempts == 0 {
			if a == AnchorNoThrow {
				score += firstRoundNoThrowBonus // 不抛锚大幅加成，确保数值上压住同类锚
			}
			if a >= AnchorCompare { // 对比锚及以上（aggressiveness ≥ 3）
				score += firstRoundComparePenalty // 强力减分，防止首轮"平A开大"
			}
		}

		// 特殊规则：情绪负面时，降低高aggressiveness锚的分数
		if state.Emotion == "negative" {
			if a >= AnchorCompare {
				score -= 1.5 // 对比锚及以上降分
			}
			if a == AnchorNoThrow {
				score += 0.8 // 不抛锚（先安抚）
			}
		}

		// 特殊规则：抗性检测（从T[14]读取抗性类型）
		// PRD核心：客户有抗性时，策略中心输出对比/拆解锚 + exchange_flag
		// 抗性类型：0=无 1=价格 2=规格 3=服务 4=品牌
		resistanceType := int(tVector[14])
		if resistanceType > 0 {
			// ---- 共通规则：有抗性时，温和锚和过于激进的锚都靠边 ----
			if a == AnchorSameKind {
				score -= 1.0 // 同类锚减分——客户有抗性了，不能再聊"很多人选"
			}
			if a == AnchorScarcity || a == AnchorSelfPay {
				score -= 1.5 // 稀缺/代价自担减分——抗性时太激进会激化对立
			}

			// ---- 按抗性类型差异化加分 ----
			switch resistanceType {
			case 1: // 价格抗性：首选对比锚（性价比）+ 拆解锚（价值拆解），次选损失锚
				if a == AnchorCompare {
					score += 2.5 // 对比锚——"同价位我们配置最高"
				}
				if a == AnchorDisassemble {
					score += 2.0 // 拆解锚——"贵有贵的道理，拆开给您算"
				}
				if a == AnchorLoss {
					score += 1.0 // 损失锚——"便宜车后期开销更大"
				}
			case 2: // 规格抗性：首选拆解锚（配置拆解）+ 对比锚（竞品对比）
				if a == AnchorDisassemble {
					score += 2.5 // 拆解锚——"这项配置的作用是..."
				}
				if a == AnchorCompare {
					score += 2.0 // 对比锚——"同级别对比我们参数更高"
				}
				if a == AnchorLoss {
					score += 0.8 // 损失锚辅助
				}
			case 3: // 服务抗性：对比锚 + 损失锚
				if a == AnchorCompare {
					score += 2.2 // 对比锚——"对比竞品我们服务多2年质保"
				}
				if a == AnchorLoss {
					score += 1.5 // 损失锚——"服务不好后期花钱更多"
				}
				if a == AnchorDisassemble {
					score += 1.0 // 拆解锚辅助
				}
			case 4: // 品牌抗性：对比锚 + 同类锚（口碑）
				if a == AnchorCompare {
					score += 2.0 // 对比锚——"品牌力看销量/保值率"
				}
				if a == AnchorSameKind {
					score += 1.5 // 同类锚——"很多客户从XX品牌转过来的"（抵消之前的减分后净+0.5）
				}
				if a == AnchorLoss {
					score += 0.8
				}
			}
		}

		// 特殊规则：高意向持续轮数多，推高aggressive锚
		if state.HighIntentRounds >= 2 {
			if a == AnchorScarcity || a == AnchorSelfPay {
				// 每多一轮高意向，稀缺/代价自担锚加0.3分，持续高热度逐步推高促单锚
				score += float64(state.HighIntentRounds) * 0.3
			}
		}

		scores[a] = score
	}

	return scores
}

// CalcAnchorScoresPromoteLocked 带促单锁的锚打分
// 在CanPromote()=false时（未到店+已报价），稀缺锚和代价自担锚被强力压制
// 防止AI在客户只是"到店体验"时就推"专属优惠""限时优惠"等促单话术
// 其他锚类型（不抛/同类/拆解/对比/损失）不受影响
func CalcAnchorScoresPromoteLocked(scores [AnchorCount]float64) [AnchorCount]float64 {
	// 稀缺锚(aggressiveness=5)和代价自担锚(aggressiveness=6)大幅减分
	// 同时压制条件交换的触发（条件交换通常与稀缺/代价自担锚绑定）
	scores[AnchorScarcity] -= 5.0 // 稀缺锚：几乎不可能被选中
	scores[AnchorSelfPay] -= 5.0  // 代价自担锚：几乎不可能被选中
	return scores
}

// ============================================================
// 到店/体验意图识别
// 客户说"想去看看""想试驾"→识别为到店意图
// 此时应推留资转化策略（引导留资+召唤人工确认），
// 而不是推车参数/优惠促单
// ============================================================

// IsStoreVisitIntent 判断客户输入是否表达到店/体验/试驾意图
func IsStoreVisitIntent(text string) bool {
	return strategytypes.IsStoreVisitIntent(text)
}

// Step2_SoftmaxAnchor 锚打分 softmax 归一化
// p(a|T,S) = softmax(score_a / τ)，取概率最大的锚 a_hat
//
// 为什么用 softmax？
//   - 将原始分数转化为概率分布，可解释性强
//   - 温度τ控制分布的"锐利度"
//   - τ越小，最高分的概率越接近1（更确定）
//   - τ越大，分布越均匀（更探索）
func Step2_SoftmaxAnchor(scores [AnchorCount]float64) (probs [AnchorCount]float64, bestAnchor int, confidence float64) {
	// 将数组转为切片
	scoreSlice := make([]float64, AnchorCount)
	for i, s := range scores {
		scoreSlice[i] = s
	}

	// 调用softmax函数
	// 修复：从SystemConfigService读取tau，后台调参即时生效
	tau := service.DefaultSystemConfigService.GetFloat("tau", config.GlobalConfig.Strategy.Tau)
	probSlice := utils.Softmax(scoreSlice, tau)

	// 转回数组
	for i, p := range probSlice {
		probs[i] = p
	}

	// 找最大概率的锚类型
	bestAnchor = utils.ArgMax(probSlice)
	confidence = probs[bestAnchor]

	return probs, bestAnchor, confidence
}

// Step3_SoftDowngrade 软降级修锚
// 当 hook_rate 低 或 silent_duration 长时，降低aggressiveness一级
//
// 软降级的设计思想：
//   - 如果客户不怎么接钩（hook_rate低），说明当前策略太激进
//   - 如果客户沉默很久，说明需要降温和缓
//   - 降低一级aggressiveness，用更温和的方式沟通
//
// 注意：这是"软"降级，不是直接换锚，而是在选定锚的基础上微调
func Step3_SoftDowngrade(selectedAnchor int, state model.SessionState) (finalAnchor int, isDowngraded bool) {
	finalAnchor = selectedAnchor
	isDowngraded = false

	// 获取当前锚的aggressiveness等级
	currentAgg := AnchorAggressiveness[selectedAnchor]

	// 检查是否需要降级
	needDowngrade := false

	// 判据1：接钩率低（低于阈值θ_hookrate_low）
	// 修复：从SystemConfigService读取，后台调参即时生效
	hookRateLow := state.Attempts >= 2 && state.HookRate < service.DefaultSystemConfigService.GetFloat("theta_hookrate_low", config.GlobalConfig.Strategy.ThetaHookRateLow)
	if hookRateLow {
		needDowngrade = true
	}

	// 判据2：沉默时长超过阈值
	silentLong := state.SilentDuration > service.DefaultSystemConfigService.GetInt("theta_silent", config.GlobalConfig.Strategy.ThetaSilent)
	if silentLong {
		needDowngrade = true
	}

	// 判据3：情绪负面
	if state.Emotion == "negative" {
		needDowngrade = true
	}

	// 执行降级
	if needDowngrade && currentAgg > 0 {
		// 降低一级aggressiveness
		newAgg := currentAgg - 1

		// 找到aggressiveness等于newAgg的锚类型
		for a := 0; a < AnchorCount; a++ {
			if AnchorAggressiveness[a] == newAgg {
				finalAnchor = a
				isDowngraded = true
				break
			}
		}
	}

	// 边界处理：不抛锚不能再降了
	// 疑点：finalAnchor 由上方 for 循环取 AnchorAggressiveness==newAgg 的锚类型赋值，
	// 其取值范围恒 ≥ 0，finalAnchor<0 分支实际不可达（死代码），可删。
	if finalAnchor < 0 {
		finalAnchor = AnchorNoThrow
	}

	return finalAnchor, isDowngraded
}

// GetAnchorName 获取锚类型名称
func GetAnchorName(anchorType int) string {
	return strategytypes.GetAnchorName(anchorType)
}

// Step2_5_StageCeiling 心智阶段锁修锚（Step2.5）
// 核心规则：锚的aggressiveness不能超过客户当前心智阶段的上限
//
// 为什么需要这一步？
//
//	没有阶段锁时，softmax完全凭分数选锚，种子客户的品牌抗性能把对比锚
//	分数推到最高，导致客户刚说"你好"就触发对比锚+条件交换+促单——
//	完全违背PRD的5级策略链路（认知→懂我→兴趣→促转→沉淀）。
//
//	阶段锁强制执行：你在认知阶段，只能用温和锚（aggressiveness ≤ 1）；
//	你在兴趣阶段，可以用拆解锚（≤ 2）；你在考虑阶段，可以用对比锚（≤ 3）；
//	依此类推。不管T向量值多高、不管抗性加分多少，阶段锁是天花板。
//
// 降级逻辑：
//
//	如果softmax选出的锚的aggressiveness超过了当前阶段上限，
//	则降级到"当前阶段上限对应的锚类型"。
//	降级方式：找到aggressiveness恰好等于上限的最常见锚类型。
//	例如：stage=0，ceiling=1，softmax选了对比锚(aggressiveness=3) → 降级到同类锚(aggressiveness=1)
//
// 与Step3（软降级）的关系：
//
//	Step2.5在Step3之前执行，两者都是降级但触发条件不同：
//	- Step2.5：阶段锁——强制性的，基于客户心智阶段
//	- Step3：软降级——基于接钩率/沉默时长/情绪等动态信号
//	如果两步都触发，最终锚会更温和（双重降级）
func Step2_5_StageCeiling(selectedAnchor int, currentStage int) (finalAnchor int, isDowngraded bool) {
	finalAnchor = selectedAnchor
	isDowngraded = false

	// 获取当前阶段允许的aggressiveness上限
	// 修复：从SystemConfigService读取阶段锁天花板，后台调参即时生效
	stageCeilingSlice := service.DefaultSystemConfigService.GetIntSlice("stage_anchor_ceiling", StageAnchorCeiling[:])
	// 边界保护：stage超出范围时，用最宽松的上限（不限制）
	if currentStage < 0 || currentStage >= len(stageCeilingSlice) {
		return finalAnchor, false // 不降级
	}
	ceilingAgg := stageCeilingSlice[currentStage]

	// 检查选中锚的aggressiveness是否超过阶段上限
	selectedAgg := AnchorAggressiveness[selectedAnchor]

	if selectedAgg > ceilingAgg {
		// 超过上限 → 降级到aggressiveness恰好等于上限的锚类型
		// 从低到高扫描，找到第一个aggressiveness == ceilingAgg的锚
		// 这保证了降级后的锚是"当前阶段允许的最aggressive锚"
		for a := 0; a < AnchorCount; a++ {
			if AnchorAggressiveness[a] == ceilingAgg {
				finalAnchor = a
				isDowngraded = true
				break
			}
		}

		// 兜底：如果没找到恰好等于上限的锚，降级到不抛锚（aggressiveness=0）
		if !isDowngraded {
			finalAnchor = AnchorNoThrow
			isDowngraded = true
		}
	}

	return finalAnchor, isDowngraded
}

// ============================================================
// 寒暄检测（Step0：语义级前置拦截）
//
// 核心思想：不管客户画像多高（高意向/已到店/越野爱好者），
// 纯寒暄就是不应该抛锚。画像决定"聊什么"，对话阶段决定"能不能推"。
//
// 为什么需要这一步？
//   种子客户预装了标签和画像（高意向+越野爱好者+已到店），
//   导致引擎觉得"这个人意向很高，可以直接推车"。
//   但对话层面客户只是说了声"你好"，画像热度≠对话热度。
//   寒暄检测在语义层面切断这个混淆，纯寒暄强制不抛锚。
//
// 判定条件（全部满足才触发）：
//   1. 输入文本很短（≤10字）
//   2. 命中寒暄词库
//   3. 不包含任何购车相关关键词（防止"你好，请问XX车型多少钱"被误判）
// ============================================================

// IsGreeting 判断客户输入是否为纯寒暄
// 纯寒暄 = 短文本 + 命中寒暄词 + 无购车意图
func IsGreeting(text string) bool {
	text = strings.TrimSpace(text)
	runes := []rune(text)

	// 条件1：太长不是寒暄（>10字可能包含具体问题）
	if len(runes) > 10 {
		return false
	}

	// 条件2：命中寒暄词库
	greetingWords := []string{
		"你好", "您好", "在吗", "在不在", "哈喽", "嗨", "hi", "hello",
		"嘿", "喂", "嗨嗨", "早上好", "下午好", "晚上好",
		"嗯", "哦", "好的", "ok", "OK", "嗯嗯", "哈哈",
	}
	matched := false
	textLower := strings.ToLower(text)
	for _, w := range greetingWords {
		if strings.Contains(textLower, w) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}

	// 条件3：排除包含购车意图关键词的情况
	// "你好，请问极石ADAMAS多少钱" 不算纯寒暄
	intentKeywords := []string{
		"车", "价", "买", "试驾", "贷款", "优惠", "配置", "参数",
		"SUV", "suv", "越野", "极石", "对比", "推荐", "选",
		"多少", "便宜", "贵", "多少钱", "落地",
	}
	for _, w := range intentKeywords {
		if strings.Contains(textLower, w) {
			return false // 有购车意图，不是纯寒暄
		}
	}

	return true
}
