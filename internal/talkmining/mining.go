// Package talkmining 「金牌顾问」人工回复话术的离线挖掘（批五 D，PLAN_FIX_2026-09-22 §6-D）。
//
// 定位：人工接管段的顾问回复一直躺在 messages 表里（sender_type='human' + assigned_user_id），
// 但全仓没有任何读侧聚合通路——真正带来到店/成交的话术从没被回收，反而以硬编码形式
// 散落在 prompt 里（如 prompt_builder.go 的"到店前推优惠=吓退客户"）。本包把这段经验
// 变成数据驱动的通路：顾问消息按 (顾问 × 锚位/阶段 × 客户旅程阶段) 分组，按**终局转化率**
// 排序出高转回复簇，再由 draft.go 交 LLM 归纳为 status='draft' 的模板草稿（人审后启用）。
//
// 分层：mining.go 是纯函数层（不碰 DB/gin/网络，口径全部可单测钉死，同 internal/analytics
// 的"口径收敛为纯函数"做法）；DB 读取与草稿落库在 draft.go。
//
// 分包红线：新领域代码不进 internal/service / internal/api（D2a/D2b）；LLM 经
// GenerateDraftFunc 函数变量注入（业务层禁止直连 internal/llm），同 service.EvalLLMFunc 形态。
package talkmining

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"unicode"

	"ai-scrm/internal/model"
	"ai-scrm/internal/pii"
)

// Unanchored 锚位哨兵：该顾问消息没有可靠锚位上下文（人工消息的 anchor_type 列默认 0，
// 而 0 同时是合法锚类型"不抛"，无法区分"真不抛"与"没记录"），分组时退化为按阶段归并。
const Unanchored = -1

// 客户终局结果档位（粗粒度漏斗序，不引入阶段序的中间态）：
// 转化 = rank(Outcome) >= rank(TargetOutcome)。
const (
	OutcomeNone    = "none"    // 未达任何终局（在建/战败/留资未到店均算 none）
	OutcomeArrived = "arrived" // 已到店（journey_stage ∈ arrived/ordered/delivered）
	OutcomeDealt   = "dealt"   // 已成交（journey_stage ∈ ordered/delivered）
)

// outcomeRank 终局档位序：none < arrived < dealt。未知档位按 none 处理（fail-safe，不虚增转化）。
var outcomeRank = map[string]int{
	OutcomeNone:    0,
	OutcomeArrived: 1,
	OutcomeDealt:   2,
}

// 挖掘默认参数（Options 零值字段回落到这里，见各常量理由）。
const (
	// DefaultMinSamples 出结论组的最小"去重客户数"门槛。为什么按客户数而非消息数：
	// 同一客户被同一顾问连发 10 条相似话术，证据量只有 1 单，按消息数算会把噪声垫成"高转"。
	// 为什么只有 5：本作业产出的是**候选草稿**（进人审 + E4 A/B），不是择优结论——
	// 择优要 ≈2200/组（见 pack_stats sample_min=50 只够报警的同一算术），出候选 5 单起步即可，
	// 由下游实验层负责"证据不够就不下结论"。
	DefaultMinSamples = 5
	// DefaultMinTextLen 归一化后代表话的最小长度（rune）。"好的""收到""嗯"这类
	// 全行业高频短答复跨顾问完全同质，进簇只会产出无信息量模板，直接当噪声丢弃。
	DefaultMinTextLen = 4
)

// DefaultTargetOutcome 默认转化目标：到店。销售主链的因果锚点是"约到店"（见 AGENTS 产品口径），
// 成交样本更稀疏，离线挖掘按到店收敛更快；需要更严口径时调用方可显式传 dealt。
const DefaultTargetOutcome = OutcomeArrived

// Record 挖掘输入的一条顾问消息（纯数据，由调用方/LoadAdvisorRecords 组装）。
//
// 隐私红线：只允许携带**顾问侧**文本——客户消息即便再像"好话术"也不入库，
// 一是顾问话术才是可复制的销售动作，二是客户原文进候选池等于把 PII 直接送进模板库。
type Record struct {
	AdvisorID  uint   // 顾问用户 ID（会话 assigned_user_id / 消息 sender_id）
	AnchorType int    // 锚位上下文（0-6）；无可靠锚位时传 Unanchored，按阶段归并
	Stage      string // 当时客户旅程阶段码（journey_stage，作分组上下文）
	CustomerID uint   // 客户 ID（按客户去重计样本）
	Content    string // 顾问消息原文（未脱敏；产物输出前由 Mine 统一过 pii 掩码）
	Outcome    string // 该客户终局结果：none/arrived/dealt（见 OutcomeFromStage）
}

// GroupKey 分组键：顾问 × (锚位或阶段) × 客户旅程阶段。
//
// 为什么是这个键：话术转化强烈依赖"谁在说（个人风格）+ 在哪个节点说（锚位/阶段）"——
// prompt_builder.go:195 硬编码的那条经验（到店前推优惠吓退、到店后推优惠促进成交）本质就是
// "同一句话术在不同阶段转化率相反"，不拆阶段维度就会把相反结论平均成无害噪声。
type GroupKey struct {
	AdvisorID  uint
	AnchorType int
	Stage      string
}

// Cluster 一个高转回复簇（分组键下的话术归并结果 + 转化统计）。
type Cluster struct {
	Key            GroupKey // 分组键
	Samples        int      // 样本数 = 簇内**去重后**客户数
	Conversions    int      // 其中达到 TargetOutcome 的客户数
	ConvRate       float64  // 转化率 = Conversions / Samples（分母为 0 时恒 0，不产 NaN）
	Insufficient   bool     // true = 样本不足（insufficient_samples），只报统计不出结论
	MessageCount   int      // 归并掉的顾问消息总条数（含近似重复）
	VariantCount   int      // 归一化去重后的话术变体数
	Representative string   // 代表性原文（出现最多的变体，已过 pii 脱敏）
	Signature      string   // 簇稳定签名：fnv32a(归一化代表文本)，跨进程可复现
	TargetOutcome  string   // 本簇转化率对应的目标档位（arrived/dealt）
}

// Options 挖掘参数。零值字段逐项回落到 Default* 常量。
type Options struct {
	MinSamples    int    // 出结论组的最小去重客户数；<=0 用 DefaultMinSamples
	MinTextLen    int    // 归一化后最小话术长度；<=0 用 DefaultMinTextLen
	TargetOutcome string // 转化目标档位（arrived/dealt）；空用 DefaultTargetOutcome
	TopK          int    // 最多返回的簇数（不足样本组排在其后、不计入结论位）；<=0 不截断
}

// OutcomeFromStage 把客户旅程阶段映射为终局档位。lost（战败）显式归 none——
// 战败客户绝不能算转化分子，否则"劝退话术"会被学成"高转话术"。
func OutcomeFromStage(stage string) string {
	switch stage {
	case model.JourneyArrived:
		return OutcomeArrived
	case model.JourneyOrdered, model.JourneyDelivered:
		return OutcomeDealt
	default:
		return OutcomeNone
	}
}

// NormalizeForDedup 近似重复归并用的轻度归一化：去所有空白/标点/符号/表情
// （只保留字母、数字、汉字符号外的 Unicode Letter/Digit）、统一小写。
// 为什么自己做而不引依赖：顾问连发同句式（"加个标点""带个 emoji""全角半角"混用）
// 是最高频的伪重复，字母数字 + CJK 的白名单一个 pass 就能收拢，无需分词/embedding。
func NormalizeForDedup(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// variantAcc 簇内一个归一化话术变体的累加器。
type variantAcc struct {
	display string // 首见原文（输出前脱敏）
	count   int    // 该变体的消息条数
	order   int    // 首见序号（并列时按更早出现取胜，保证排序确定性）
}

// groupAcc 单个分组键的累加器。
type groupAcc struct {
	variants     map[string]*variantAcc // 归一化文本 → 变体
	custOutcome  map[uint]int           // 客户 → 其在本簇的最优终局档位（按客户去重计样本）
	messageCount int
	nextOrder    int
}

// Mine 核心纯函数：把顾问消息批按分组键聚类为回复簇，并按转化率排序输出。
//
// 排序规则（结论位在前）：非不足组按 (转化率 desc, 样本数 desc, 分组键 asc)；
// 不足组一律排最后且只带 Insufficient 标记——为什么样本不足"不得输出结论"：
// 5 单以下的高转化率基本是运气（一个铁杆客户什么都买），把它喂给 LLM 出稿
// 等于让随机噪声进入人审队列，还披着"数据驱动"的外衣，比没有数据更糟。
func Mine(records []Record, opts Options) []Cluster {
	minSamples := opts.MinSamples
	if minSamples <= 0 {
		minSamples = DefaultMinSamples
	}
	minTextLen := opts.MinTextLen
	if minTextLen <= 0 {
		minTextLen = DefaultMinTextLen
	}
	target := opts.TargetOutcome
	if target == "" {
		target = DefaultTargetOutcome
	}
	if _, ok := outcomeRank[target]; !ok {
		target = DefaultTargetOutcome
	}
	targetRank := outcomeRank[target]

	groups := map[GroupKey]*groupAcc{}
	for _, rec := range records {
		// 无归属记录直接丢弃：advisor=0 无法回答"这是谁的打法"，customer=0 是
		// 已知存在的孤儿消息（conversation_id/customer_id=0 残留），会把样本数虚增。
		if rec.AdvisorID == 0 || rec.CustomerID == 0 {
			continue
		}
		norm := NormalizeForDedup(rec.Content)
		if len([]rune(norm)) < minTextLen {
			continue
		}
		key := GroupKey{AdvisorID: rec.AdvisorID, AnchorType: rec.AnchorType, Stage: rec.Stage}
		g := groups[key]
		if g == nil {
			g = &groupAcc{variants: map[string]*variantAcc{}, custOutcome: map[uint]int{}}
			groups[key] = g
		}
		v := g.variants[norm]
		if v == nil {
			v = &variantAcc{display: rec.Content, order: g.nextOrder}
			g.variants[norm] = v
			g.nextOrder++
		}
		v.count++
		g.messageCount++
		rank := outcomeRank[rec.Outcome] // 未知档位查表得 0（none），不虚增
		// 未转化客户（rank=0）也必须入图：样本分母 = 全部去重客户，
		// 若只在"更优档位出现时"写入，分母只剩转化者，转化率恒 100%，挖掘彻底失真。
		prev, seen := g.custOutcome[rec.CustomerID]
		if !seen || rank > prev {
			g.custOutcome[rec.CustomerID] = rank
		}
	}

	clusters := make([]Cluster, 0, len(groups))
	for key, g := range groups {
		c := Cluster{Key: key, TargetOutcome: target}
		c.Samples = len(g.custOutcome)
		for _, rank := range g.custOutcome {
			if rank >= targetRank {
				c.Conversions++
			}
		}
		if c.Samples > 0 {
			c.ConvRate = float64(c.Conversions) / float64(c.Samples)
		}
		c.Insufficient = c.Samples < minSamples
		c.MessageCount = g.messageCount
		c.VariantCount = len(g.variants)
		rep, repNorm := pickRepresentative(g)
		c.Representative = pii.MaskPhoneInText(rep) // 隐私红线：代表原文入产物前必须过手机号掩码
		c.Signature = fnv32Hex(repNorm)
		clusters = append(clusters, c)
	}

	sortClusters(clusters)
	if opts.TopK > 0 && len(clusters) > opts.TopK {
		// TopK 只截结论位（非不足组）；不足组一律保留在尾部，便于日志观察"差多少样本"，
		// 但下游（DraftTopClusters）会拒绝它们，永远不会因截断把不足组挤进结论位。
		keep := make([]Cluster, 0, len(clusters))
		quotas := opts.TopK
		var tail []Cluster
		for _, cl := range clusters {
			if !cl.Insufficient && quotas > 0 {
				keep = append(keep, cl)
				quotas--
			} else {
				tail = append(tail, cl)
			}
		}
		keep = append(keep, tail...)
		clusters = keep
	}
	return clusters
}

// pickRepresentative 选出簇内代表变体：消息数最多者胜，并列取更早出现的（确定性）。
func pickRepresentative(g *groupAcc) (string, string) {
	bestNorm := ""
	var best *variantAcc
	for norm, v := range g.variants {
		if best == nil || v.count > best.count || (v.count == best.count && v.order < best.order) {
			best, bestNorm = v, norm
		}
	}
	return best.display, bestNorm
}

// sortClusters 排序：结论位在前（转化率 desc → 样本数 desc → 分组键字典序保证确定性），
// 样本不足组一律垫底。为什么强调"按转化率而非样本量"：金牌顾问每天发几百条消息，
// 最大簇永远是他的日常寒暄；不先按转化率排，挖出来的只会是"说得最多的"而不是"卖得动的"。
func sortClusters(clusters []Cluster) {
	sort.SliceStable(clusters, func(i, j int) bool {
		a, b := clusters[i], clusters[j]
		if a.Insufficient != b.Insufficient {
			return !a.Insufficient // 不足组排后
		}
		if a.ConvRate != b.ConvRate {
			return a.ConvRate > b.ConvRate
		}
		if a.Samples != b.Samples {
			return a.Samples > b.Samples
		}
		if a.Key.AdvisorID != b.Key.AdvisorID {
			return a.Key.AdvisorID < b.Key.AdvisorID
		}
		if a.Key.AnchorType != b.Key.AnchorType {
			return a.Key.AnchorType < b.Key.AnchorType
		}
		return a.Key.Stage < b.Key.Stage
	})
}

// fnv32Hex fnv32a 稳定哈希（8 位十六进制）。跨进程/跨版本确定（同 strategy.abHashBucket
// 的选型理由：带 seed 的哈希不可复现，幂等判重必须用无 seed 的 fnv32a）。
func fnv32Hex(s string) string {
	h := fnv.New32a()
	// fnv 的 Write 对内存 buffer 永不返回 error，忽略 errcheck 误报
	_, _ = fmt.Fprintf(h, "%s", s)
	return fmt.Sprintf("%08x", h.Sum32())
}
