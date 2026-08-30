// Package strategy：internal/engine/strategy 模块（自动补包注释）。
package strategy

import (
	"ai-scrm/internal/model"
	"ai-scrm/internal/strategytypes"
)

// ============================================================
// 策略中心引擎 - 类型定义
// 所有策略引擎相关的结构体、常量都定义在这里
// ============================================================

// ---- 锚类型常量（P2-B 迁往 internal/strategytypes，此处保留别名零破坏兼容）----
const (
	AnchorNoThrow     = strategytypes.AnchorNoThrow
	AnchorSameKind    = strategytypes.AnchorSameKind
	AnchorScene       = strategytypes.AnchorScene
	AnchorDisassemble = strategytypes.AnchorDisassemble
	AnchorCompare     = strategytypes.AnchorCompare
	AnchorLoss        = strategytypes.AnchorLoss
	AnchorScarcity    = strategytypes.AnchorScarcity
	AnchorSelfPay     = strategytypes.AnchorSelfPay
)

// AnchorCount 锚类型总数（中立包别名）
const AnchorCount = strategytypes.AnchorCount

// AnchorNames 锚类型名称映射（中立包别名）
var AnchorNames = strategytypes.AnchorNames

// AnchorAggressiveness 各锚类型的aggressiveness值（中立包别名）
var AnchorAggressiveness = strategytypes.AnchorAggressiveness

// ---- 路由结果常量 ----
const (
	RouteAI           = "ai"            // AI继续对话
	RoutePendingHuman = "pending_human" // 待人工接管（软接管：AI先回复，通知销售，销售3分钟内不接管则继续AI）
	RouteHuman        = "human"         // 直接转人工（硬切：客户侧有感知）
	RouteFish         = "fish"          // 养鱼（暂缓跟进）
	RoutePrice        = "price"         // 询价路由：客户问价格，引导到店试驾后出报价
)

// ---- 紧迫等级常量 ----
const (
	UrgencyL1 = "L1" // L1：低紧迫（常规跟进）
	UrgencyL2 = "L2" // L2：中紧迫（加快节奏）
	UrgencyL3 = "L3" // L3：高紧迫（强制切人）
)

// ---- 心智阶段常量（0-5共6段）----
const (
	StageAwareness     = 0 // 认知阶段
	StageInterest      = 1 // 兴趣阶段
	StageConsideration = 2 // 考虑阶段
	StageEvaluation    = 3 // 评估阶段
	StageDecision      = 4 // 决策阶段
	StagePurchase      = 5 // 成交阶段
)

// StrategyInput 策略引擎输入
// 输入 = T向量（客户画像） + S状态（会话状态） + 客户当前输入
type StrategyInput struct {
	TVector        [32]float64        // T向量，32维客户画像
	State          model.SessionState // 会话状态S
	CustomerInput  string             // 客户最新输入文本
	CustomerTags   []string           // 客户标签列表
	CustomerID     uint               // 客户ID
	ConversationID uint               // 会话ID
	CanPromote     bool               // 是否允许促单（只有已到店+已报价后才允许，防止体验阶段推优惠）
	JourneyStage   string             // 客户线索阶段（用于意图识别+留资转化策略）
	// TenantID 租户ID（M1租户隔离修复 2026-08-25）：
	// 引擎缓存为全量加载（DefaultEngine 含所有租户私有模板/卖点），
	// Step4 召回时按此字段内存过滤——预置(tenant_id=0)全员可见，私有仅本租户可见；
	// 值为0时 fail-closed 只见预置。所有构造处必须显式传 customer.TenantID。
	TenantID uint
	// DeptIDs 顾问部门继承链（三级包架构 2026-08-26）：自身→父→…→根 的部门ID集合。
	// 部门专属内容（部门包物化）仅当链上包含其 DepartmentID 时可见；
	// 为空=纯租户语境（C端客户对话/未指派顾问），只见行业+企业两层
	DeptIDs []uint
}

// StrategyOutput 策略引擎输出
// 输出 = 选好的锚 + 选好的话术 + 路由决策 + 各种中间结果
// StrategyOutput 策略引擎输出（P2-B 起为 strategytypes.StrategyOutput 的类型别名，
// 全仓既有引用零改动；ai/llm 改引中立包后依赖方向即为合法）
type StrategyOutput = strategytypes.StrategyOutput

// AnchorWeight 锚类型权重
// 用于Step1的加权打分: score_a = w_a · f_a(T, S)
type AnchorWeight struct {
	// json 标签修复（2026-08-26）：后台配置为 snake_case，无标签时反序列化静默得到
	// 全零权重（len 校验通过）——自该功能上线起所有锚打分实际用零权重，意向/信任失效
	IntentScoreWeight float64 `json:"intent_score_weight"`
	TrustWeight       float64 `json:"trust_weight"`
	HookRateWeight    float64 `json:"hook_rate_weight"`
	StageWeight       float64 `json:"stage_weight"`
	PriceSensWeight   float64 `json:"price_sens_weight"`
	BaseBias          float64 `json:"base_bias"`
}

// DefaultAnchorWeights 各锚类型的默认权重配置
// 这是策略引擎的核心参数之一，决定了不同锚的偏好
// 为什么每个锚都有不同权重？因为不同锚适用于不同的客户状态
var DefaultAnchorWeights = [AnchorCount]AnchorWeight{
	// 0-不抛：意向很低或信任很低时倾向不抛
	{
		IntentScoreWeight: -2.0, // 意向越低越可能不抛
		TrustWeight:       -1.5, // 信任越低越可能不抛
		HookRateWeight:    -1.0,
		StageWeight:       -1.0,
		PriceSensWeight:   0.0,
		BaseBias:          0.5, // 基础偏置
	},
	// 1-同类/场景锚：入门级，适合初期
	{
		IntentScoreWeight: 0.5,
		TrustWeight:       0.3,
		HookRateWeight:    0.2,
		StageWeight:       0.3,
		PriceSensWeight:   0.0,
		BaseBias:          1.0, // 基础偏置较高，初期常用
	},
	// 2-拆解锚：中等aggressiveness
	{
		IntentScoreWeight: 1.0,
		TrustWeight:       0.5,
		HookRateWeight:    0.3,
		StageWeight:       0.8,
		PriceSensWeight:   0.0,
		BaseBias:          0.5,
	},
	// 3-对比锚：较高aggressiveness，中高意向
	{
		IntentScoreWeight: 1.5,
		TrustWeight:       0.8,
		HookRateWeight:    0.5,
		StageWeight:       1.2,
		PriceSensWeight:   0.5,
		BaseBias:          0.3,
	},
	// 4-损失锚：高aggressiveness
	{
		IntentScoreWeight: 1.8,
		TrustWeight:       1.0,
		HookRateWeight:    0.6,
		StageWeight:       1.5,
		PriceSensWeight:   0.8,
		BaseBias:          0.0,
	},
	// 5-稀缺锚：很高aggressiveness
	{
		IntentScoreWeight: 2.0,
		TrustWeight:       1.2,
		HookRateWeight:    0.8,
		StageWeight:       1.8,
		PriceSensWeight:   0.3,
		BaseBias:          -0.2,
	},
	// 6-代价自担锚：最高aggressiveness
	{
		IntentScoreWeight: 2.2,
		TrustWeight:       1.5,
		HookRateWeight:    1.0,
		StageWeight:       2.0,
		PriceSensWeight:   0.5,
		BaseBias:          -0.5,
	},
}

// ---- 心智阶段锁（Step2.5：阶段天花板）----
// 核心规则：客户在哪个心智阶段，锚的aggressiveness就不能超过该阶段的上限
// 这直接实现了PRD的5级策略链路：认知→懂我→兴趣→促转→沉淀
//
// 设计逻辑：
//
//	认知阶段（stage 0）：客户刚进来，只了解产品 → 只能用温和锚（aggressiveness ≤ 1）
//	兴趣阶段（stage 1）：客户开始感兴趣 → 可以用拆解锚讲产品（aggressiveness ≤ 2）
//	考虑阶段（stage 2）：客户在对比考虑 → 可以用对比锚做说服（aggressiveness ≤ 3）
//	评估阶段（stage 3）：客户快做决定了 → 可以用损失锚促转（aggressiveness ≤ 4）
//	决策/成交（stage 4-5）：客户已经下定决心 → 不限制，所有锚都可以用
//
// 为什么需要这个锁？
//
//	没有锁时，种子客户预置的品牌抗性能把对比锚分数推到最高，
//	导致客户刚说"你好"就触发对比锚+促单——完全违背5级策略链路。
//	阶段锁强制"先认知识别，再逐步升级"，不管T向量值多高，
//	认知阶段就只能用同类锚，不可能"平A开大"。
var StageAnchorCeiling = [6]int{
	1, // Stage 0（认知）：aggressiveness上限=1，只能用不抛/同类/场景锚
	2, // Stage 1（兴趣）：aggressiveness上限=2，可以用拆解锚
	3, // Stage 2（考虑）：aggressiveness上限=3，可以用对比锚
	4, // Stage 3（评估）：aggressiveness上限=4，可以用损失锚
	6, // Stage 4（决策）：不限制，所有锚都可以用
	6, // Stage 5（成交）：不限制
}

// ---- 线索阶段天花板（Step1.6：业务漏斗硬锁）----
// 核心规则：客户在哪个线索阶段，锚的aggressiveness就不能超过该阶段的上限
// 这与心智阶段锁(Step2.5)不同——心智阶段是"对话热度"，线索阶段是"业务漏斗位置"
//
// 设计逻辑：
//
//	ai_connected（AI建联）：客户刚接触 → aggressiveness ≤ 1（只能温和了解，不能推车）
//	human_connected/lead_captured（人工建联/已留资）：客户开始信任 → aggressiveness ≤ 2（可以讲产品，不能促单）
//	arrived（已到店，未报价）：客户在体验 → aggressiveness ≤ 3（可以对比，不能促单）
//	arrived+quoted（已到店，已报价）：客户在决策 → 不限制（允许促单）
//	ordered/delivered：已成交 → 不限制
//	lost：已战败 → aggressiveness = 0（只安抚，不推销）
//
// 为什么需要这个锁？
//
//	心智阶段锁基于对话轮次和意向分，但种子客户预装了高意向标签，
//	导致对话2-3轮心智阶段就推到3-4，阶段锁天花板很高。
//	线索阶段锁基于业务漏斗位置，是"你在哪个环节"的硬约束：
//	你没到店就是没到店，不管对话多热烈，都不能线上促单。
//	到店前推优惠=吓退客户，到店后推优惠=促进成交。
var JourneyStageAggressivenessCeiling = map[string]int{
	"ai_connected":    1, // AI建联：只能不抛/同类锚，温和了解
	"human_connected": 2, // 人工建联：可用拆解锚，讲产品
	"lead_captured":   2, // 已留资：可用拆解锚，讲产品
	"arrived":         3, // 已到店（含未报价子状态）：可用对比锚，但不能促单
	"ordered":         6, // 已下单：不限制
	"delivered":       6, // 已交车：不限制
	"lost":            0, // 已战败：只安抚，不推销
}
