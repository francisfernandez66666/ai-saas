package strategy

import (
	"ai-scrm/config"
	"ai-scrm/internal/model"
	"ai-scrm/internal/service"
	"ai-scrm/internal/strategytypes"
	"strings"
)

// ============================================================
// 策略中心 - 紧迫等级判定 & 路由决策
// 对应线上推理7步公式的 Step5 / Step6
// ============================================================

// Step5_CalcUrgency 计算紧迫等级（L1/L2/L3）
//
// 紧迫等级的作用：
//   - 指导话术的紧迫程度
//   - L3是强制切人的触发条件之一
//
// 判定逻辑：
//   - L1：意向分 < θ_urgency_L1（常规跟进）
//   - L2：θ_urgency_L1 ≤ 意向分 < θ_urgency_L2（加快节奏）
//   - L3：意向分 ≥ θ_urgency_L2（高意向，需重点关注）
func Step5_CalcUrgency(intentScore float64, highIntentRounds int) string {
	// 修复：从SystemConfigService读取阈值，后台调参即时生效
	L1 := service.DefaultSystemConfigService.GetFloat("theta_urgency_l1", config.GlobalConfig.Strategy.ThetaUrgencyL1)
	L2 := service.DefaultSystemConfigService.GetFloat("theta_urgency_l2", config.GlobalConfig.Strategy.ThetaUrgencyL2)

	// L3：高意向持续多轮
	if intentScore >= L2 && highIntentRounds >= service.DefaultSystemConfigService.GetInt("theta_l3_rounds", config.GlobalConfig.Strategy.ThetaL3Rounds) {
		return UrgencyL3
	}

	// L2：中高意向
	if intentScore >= L1 {
		return UrgencyL2
	}

	// L1：低意向
	return UrgencyL1
}

// IsPriceInquiry 检查客户消息是否为询价意图
// 硬编码：多个询价关键词匹配，命中即返回true
func IsPriceInquiry(text string) bool {
	priceKeywords := []string{
		"多少钱", "什么价", "报价", "价格", "价位", "售价", "贵不贵",
		"怎么卖", "怎么算", "落地价", "优惠多少", "便宜多少",
		"车价", "售价多少", "多少钱一辆", "多少钱一台",
		"价格多少", "价格怎么", "什么价格", "如何收费",
	}
	for _, kw := range priceKeywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

// Step6_RouteDecision 路由决策（AI / 转人工 / 养鱼 / 询价引导到店）
//
// 三判据转人工：
//  1. 信任度 < θ_trust
//  2. attempts ≥ θ_rounds 且 hook_rate < θ_hookrate_crit
//  3. 情绪负面
//
// L3强制切人：
//   - 意向分 ≥ θ_L3_intent 且 高意向持续 ≥ θ_L3_rounds
//
// 养鱼：
//   - 意向分很低且多轮没起色
//
// 其余情况继续AI对话
//
// 询价硬路由（最高优先级）：客户问价格 → 引导到店试驾后出报价
// 话术策略可走AI prompt，流程硬编码不走错
func Step6_RouteDecision(
	tVector [32]float64,
	state model.SessionState,
	urgencyLevel string,
	customerInput string,
) (routeResult string, reason string) {

	// ============================================================
	// 第0步：询价硬路由（最高优先级）
	// 客户问价格 → RoutePrice → 到店试驾引导流程
	// 已留资客户不重复问留资信息，直接引导到店试驾体验后出报价
	// ============================================================
	if IsPriceInquiry(customerInput) {
		return RoutePrice, "客户询价，引导到店试驾后出报价"
	}

	intentScore := tVector[0]
	trustLevel := tVector[6]
	// 修复：从SystemConfigService读取路由阈值，后台调参即时生效
	thetaTrust := service.DefaultSystemConfigService.GetFloat("theta_trust", config.GlobalConfig.Strategy.ThetaTrust)
	thetaRounds := service.DefaultSystemConfigService.GetInt("theta_rounds", config.GlobalConfig.Strategy.ThetaRounds)
	thetaHookRateCrit := service.DefaultSystemConfigService.GetFloat("theta_hook_rate_crit", config.GlobalConfig.Strategy.ThetaHookRateCrit)
	thetaL3Intent := service.DefaultSystemConfigService.GetFloat("theta_l3_intent", config.GlobalConfig.Strategy.ThetaL3Intent)
	thetaL3Rounds := service.DefaultSystemConfigService.GetInt("theta_l3_rounds", config.GlobalConfig.Strategy.ThetaL3Rounds)

	// ============================================================
	// 先判断是否需要转人工（优先级最高）
	// ============================================================

	// 判据1：信任度低于阈值（对话至少2轮后才判断
	if trustLevel < thetaTrust && state.Attempts >= 2 {
		return RoutePendingHuman, "信任度过低，待人工接管建立信任"
	}

	// 判据2：多轮后接钩率危急
	if state.Attempts >= thetaRounds && state.HookRate < thetaHookRateCrit {
		return RoutePendingHuman, "多轮对话接钩率过低，待人工介入"
	}

	// 判据3：情绪负面（且持续2轮以上）
	if state.Emotion == "negative" && state.Attempts >= 2 {
		return RoutePendingHuman, "客户情绪负面，待人工安抚"
	}

	// L3强制切人：高意向且持续多轮
	if intentScore >= thetaL3Intent && state.HighIntentRounds >= thetaL3Rounds {
		return RoutePendingHuman, "L3高意向客户，待人工跟进"
	}

	// ============================================================
	// 判断是否需要养鱼（低意向且无起色）
	// ============================================================

	// 养鱼硬阈值：意向分<0.2 且 对话≥3轮 且 接钩率<0.2，三项同时满足才判定无药可救
	if intentScore < 0.2 && state.Attempts >= 3 && state.HookRate < 0.2 {
		return RouteFish, "客户意向低且响应差，进入养鱼模式"
	}

	// ============================================================
	// 其余情况继续AI对话
	// ============================================================

	return RouteAI, "正常AI对话"
}

// Step7_UpdateIntent 意向分反哺（Step7）
//
// 根据本轮对话的情况，更新客户的意向分
// 这是一个简单的增量更新模型
//
// 更新规则：
//   - 客户主动提问/深入了解：+0.05 ~ +0.1
//   - 客户接钩成功：+0.03
//   - 客户回复简短/敷衍：-0.02
//   - 客户表达不满：-0.1
func Step7_UpdateIntent(
	currentIntent float64,
	customerInput string,
	hooked bool,
	emotion string,
) float64 {
	delta := 0.0

	// 接钩成功，意向上升
	if hooked {
		delta += 0.03
	}

	// 情绪正面，意向上升
	if emotion == "positive" {
		delta += 0.05
	}

	// 情绪负面，意向下降
	if emotion == "negative" {
		delta -= 0.1
	}

	// 消息长度也可以作为参考（长消息说明更投入）
	if len([]rune(customerInput)) > 20 {
		delta += 0.02
	}

	// 计算新的意向分（限制在0-1之间）
	newIntent := currentIntent + delta
	if newIntent < 0 {
		newIntent = 0
	}
	if newIntent > 1 {
		newIntent = 1
	}

	return newIntent
}

// CheckHooked 接钩判定（P2-B 委托 strategytypes）
func CheckHooked(customerInput string) bool {
	return strategytypes.CheckHooked(customerInput)
}

// DetectEmotion 情绪判定（P2-B 委托 strategytypes）
func DetectEmotion(text string) string {
	return strategytypes.DetectEmotion(text)
}
