// Package strategytypes 策略中立类型包（P2-B 架构解耦产物）
// 只放纯数据类型与纯函数：锚常量、锚名、StrategyOutput、到店意图/接钩/情绪文本匹配。
// 任何层（ai/llm/strategy）都可安全引用，不构成环，是断 ai→strategy 反向依赖的关键。
package strategytypes

import (
	"strings"
)

// ============================================================
// 策略中立类型包（P2-B 架构解耦产物）
//
// 为什么存在？原 StrategyOutput/锚常量定义在 internal/engine/strategy，
// 导致 ai→strategy 反向依赖（prompt_builder 需要策略输出类型），
// 进而阻塞"策略引擎单线调用 LLM"的依赖方向（strategy→llm→ai→strategy 成环）。
//
// 本包只放【纯数据类型 + 纯函数】：锚常量、锚名、StrategyOutput、
// 到店意图文本匹配。任何层都可安全引用，不构成环。
// 依赖方向验收：ai→strategytypes ✓ llm→strategytypes ✓ strategy→strategytypes ✓
// ============================================================

// ---- 锚类型常量（对应8类锚空间A）----
// aggressiveness序列：不抛 < 同类≈场景 < 拆解 < 对比(含service) < 损失 < 稀缺 < 代价自担
const (
	AnchorNoThrow     = 0 // 不抛锚 (aggressiveness=0)
	AnchorSameKind    = 1 // 同类锚 (aggressiveness=1)
	AnchorScene       = 1 // 场景锚 (aggressiveness=1，和同类锚同级)
	AnchorDisassemble = 2 // 拆解锚 (aggressiveness=2)
	AnchorCompare     = 3 // 对比锚 (aggressiveness=3)
	AnchorLoss        = 4 // 损失锚 (aggressiveness=4)
	AnchorScarcity    = 5 // 稀缺锚 (aggressiveness=5)
	AnchorSelfPay     = 6 // 代价自担锚 (aggressiveness=6)
)

// AnchorCount 锚类型总数（不抛、同类、拆解、对比、损失、稀缺、代价自担 = 7种aggressiveness级别）
// 注意：场景锚和同类锚aggressiveness相同，算一个档位
const AnchorCount = 7

// AnchorNames 锚类型名称映射
var AnchorNames = [AnchorCount]string{
	"不抛",     // 0
	"同类/场景锚", // 1
	"拆解锚",    // 2
	"对比锚",    // 3
	"损失锚",    // 4
	"稀缺锚",    // 5
	"代价自担锚",  // 6
}

// AnchorAggressiveness 各锚类型的aggressiveness值
// 数组索引 = 锚类型，值 = aggressiveness等级
var AnchorAggressiveness = [AnchorCount]int{
	0, // 不抛
	1, // 同类/场景
	2, // 拆解
	3, // 对比
	4, // 损失
	5, // 稀缺
	6, // 代价自担
}

// GetAnchorName 获取锚类型名称
func GetAnchorName(anchorType int) string {
	if anchorType >= 0 && anchorType < AnchorCount {
		return AnchorNames[anchorType]
	}
	return "未知"
}

// StrategyOutput 策略引擎输出（7步推理结果的结构化承载）
// 原定义于 engine/strategy/types.go，P2-B 迁入本中立包供 ai/llm 引用
type StrategyOutput struct {
	// Step1-2 结果
	AnchorScores     [AnchorCount]float64 // 各锚类型的打分
	AnchorProbs      [AnchorCount]float64 // softmax后的概率分布
	SelectedAnchor   int                  // 选中的锚类型a_hat
	AnchorConfidence float64              // 选中锚的置信度（概率）

	// Step2.5 阶段锁降级结果
	StageDowngraded bool // 是否因心智阶段锁而被降级
	StageBeforeLock int  // 阶段锁降级前的锚类型（即softmax选出的原始锚）
	StageCeilingAgg int  // 当前阶段允许的aggressiveness上限

	// Step3 软降级结果
	SoftDowngrade  bool // 是否触发了软降级（接钩率低/沉默长/情绪负面）
	OriginalAnchor int  // 第一次降级前的锚类型（softmax原始锚）
	FinalAnchor    int  // 最终锚类型（所有降级之后）

	// Step4 话术召回结果
	TemplateID   string // 选中的话术模板ID
	TemplateName string // 模板名称
	PromptText   string // 渲染后的抛话术（已填充卖点）
	HookText     string // 渲染后的钩话术

	// 条件交换机制
	ExchangeFlag bool   // 是否触发条件交换
	ExchangeType string // 交换类型: spec/payment/service

	// Step5 紧迫等级
	UrgencyLevel string // L1/L2/L3

	// Step6 路由决策
	RouteResult string // ai/human/fish
	RouteReason string // 路由决策原因（便于调试）

	// Step7 意向分变化
	IntentDelta float64 // 意向分增量（反哺）
}

// IsStoreVisitIntent 到店意图判定（纯文本关键词匹配，无状态）
// 原定义于 engine/strategy/anchor.go，P2-B 迁入本中立包供 llm 直接调用
func IsStoreVisitIntent(text string) bool {
	text = strings.ToLower(text)

	visitKeywords := []string{
		"到店", "去店里", "来看看", "看实车", "体验", "试驾",
		"去试驾", "去你们店", "到你们那", "上门", "预约看车",
		"预约试驾", "想看看", "想体验", "线下", "现场",
		"过来一趟", "过去看看", "过去试", "到展厅",
	}
	for _, kw := range visitKeywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

// CheckHooked 接钩判定（纯文本规则，原 engine/strategy/route.go）
// CheckHooked 检查客户是否接钩
// 简易规则：客户回复且回复有实际内容就算接钩
func CheckHooked(customerInput string) bool {
	// 空消息不算接钩
	if len(strings.TrimSpace(customerInput)) == 0 {
		return false
	}

	// 纯语气词不算
	shortNoHook := []string{"哦", "嗯", "啊", "好", "知道了", "呵呵", "哈哈", "嗯呢", "好哒"}
	for _, s := range shortNoHook {
		if customerInput == s {
			return false
		}
	}

	// 有实际内容就算接钩
	return len([]rune(customerInput)) >= 3
}

// DetectEmotion 情绪判定（负面>正面=negative 等，原 engine/strategy/route.go）
// DetectEmotion 简易情绪识别
// 基于关键词的规则识别，后续可以替换为AI识别
func DetectEmotion(text string) string {
	// 负面关键词
	negativeKeywords := []string{
		"不喜欢", "不满意", "太贵了", "太贵", "不值", "不好", "不行",
		"差评", "投诉", "垃圾", "骗", "坑", "套路", "忽悠",
		"别了", "算了", "不用了", "不考虑",
		"生气", "气死", "烦", "讨厌", "失望",
	}

	// 正面关键词
	positiveKeywords := []string{
		"喜欢", "满意", "不错", "好的", "棒", "赞",
		"谢谢", "感谢", "厉害", "专业", "挺好",
		"考虑一下", "有兴趣", "想了解", "怎么买", "多少钱",
		"试驾", "到店", "预约", "下单", "定了",
	}

	negativeCount := countKeywords(text, negativeKeywords)
	positiveCount := countKeywords(text, positiveKeywords)

	if negativeCount > positiveCount && negativeCount > 0 {
		return "negative"
	}
	if positiveCount > negativeCount && positiveCount > 0 {
		return "positive"
	}
	return "neutral"
}

// countKeywords 统计关键词出现次数
// countKeywords 统计关键词出现次数
func countKeywords(text string, keywords []string) int {
	count := 0
	for _, kw := range keywords {
		if len(kw) > 0 {
			occurrences := 0
			for i := 0; i <= len(text)-len(kw); i++ {
				if text[i:i+len(kw)] == kw {
					occurrences++
				}
			}
			count += occurrences
		}
	}
	return count
}
