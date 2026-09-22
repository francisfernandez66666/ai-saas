// Package strategy 判优层（批五 C，2026-09-23）：A/B 双臂判优与模板行动建议的纯函数层。
//
// 本文件**不碰 DB、不碰 gin**（对齐 analytics/contribution.go 的"口径收敛成纯函数"做法）：
// 输入两臂 (successes, trials) → 输出决策枚举 + 置信度；DB 装配与写路径全部留在调用方。
// 功效不足时绝不输出判优结论——这是项目"不假装精确"的既有风格（见 ContributionNotes）。
package strategy

import (
	"ai-scrm/internal/runtimecfg"
	"math"
	"sort"
	"time"
)

// DecisionKind 判优决策枚举（导出为字符串，前端/审计直接消费）。
type DecisionKind string

// 判优决策枚举取值：insufficient_samples=功效不足不下结论；not_significant=样本达标但差异不显著；
// winner_a/winner_b=对应臂以 ≥confidence 的后验概率胜出（a=对照臂，b=实验/被评臂）。
const (
	DecisionInsufficientSamples DecisionKind = "insufficient_samples"
	DecisionNotSignificant      DecisionKind = "not_significant"
	DecisionWinnerA             DecisionKind = "winner_a"
	DecisionWinnerB             DecisionKind = "winner_b"
)

// ExperimentArm 单臂观测：successes=奖励事件次数（如留资条数），trials=样本数（pack_stats.sample_count）。
type ExperimentArm struct {
	Successes int64
	Trials    int64
}

// ExperimentVerdict 判优结论：Decision + 置信度 + 判定口径回显（前端卡片与审计都要求自解释）。
type ExperimentVerdict struct {
	Decision   DecisionKind `json:"decision"`
	Confidence float64      `json:"confidence"`    // 胜出（或占优）一方的后验概率；insufficient 时为 0
	ProbBOverA float64      `json:"prob_b_over_a"` // P(臂B > 臂A)，方向原始值，便于复核
	Metric     string       `json:"metric"`
	MinSamples int64        `json:"min_samples"`
}

// DecisionParams 判优参数（由 DecisionParamsFromConfig 从热配置装配，纯函数只吃参数不读配置）。
type DecisionParams struct {
	Metric         string
	MinSamplesLead int64 // lead/arrive/deal 稀疏指标共用的大门槛（experiment_min_samples_lead）
	MinSamplesHook int64 // hook 稠密指标小门槛（experiment_min_samples_hook）
	Confidence     float64
}

// DecisionParamsFromConfig 读取判优热配置（系统默认层，等价于 DecisionParamsForTenant(0)）。
// 保留给"无租户上下文"的调用方（单测、平台层视图）；手上确有租户的调用方一律用租户版。
// 指标仍过 sanitizeBanditMetric 白名单——判优与择臂共享同一条 reward hacking 红线。
func DecisionParamsFromConfig() DecisionParams {
	return DecisionParamsForTenant(0)
}

// cfgBoolFor/cfgIntFor/cfgFloatFor/cfgStringFor 租户生效值读取的 nil 安全壳：
// 配置单例在部分冷路径/单测里未初始化，直接调方法会 nil panic（同 bandit.go 的守卫写法）。
func cfgBoolFor(tid uint, key string, def bool) bool {
	svc := runtimecfg.DefaultSystemConfigService
	if svc == nil {
		return def
	}
	return svc.GetBoolForTenant(tid, key, def)
}

func cfgIntFor(tid uint, key string, def int) int {
	svc := runtimecfg.DefaultSystemConfigService
	if svc == nil {
		return def
	}
	return svc.GetIntForTenant(tid, key, def)
}

func cfgFloatFor(tid uint, key string, def float64) float64 {
	svc := runtimecfg.DefaultSystemConfigService
	if svc == nil {
		return def
	}
	return svc.GetFloatForTenant(tid, key, def)
}

func cfgStringFor(tid uint, key string, def string) string {
	svc := runtimecfg.DefaultSystemConfigService
	if svc == nil {
		return def
	}
	return svc.GetStringForTenant(tid, key, def)
}

// DecisionParamsForTenant 判优参数的**租户生效版**装配（2026-09-23 批六）。
//
// 为什么必须按租户读而不是读系统默认：这批 experiment_* 键都**不是**平台级键——
// 租户管理员在后台配置中心就能改，写入的是 (tenant_id, key) 覆盖层。而系统默认层读法
// （SafeCfg*）永远看不到租户覆盖，结果是"UI 上改了、判优照旧"的静默失效
// （与 email_verify_enabled 当年"写租户层读系统层永远看不到变更"同一课）。
// tid=0 时 lookupTenant 直接落系统层，与旧全局读逐字节等价，故默认态零行为变化。
func DecisionParamsForTenant(tenantID uint) DecisionParams {
	return DecisionParams{
		Metric: sanitizeBanditMetricWithHook(cfgStringFor(tenantID, "experiment_reward_metric", "lead")),
		// 门槛钳下限 1：配 0/负数会让"样本≥门槛"恒真，等于取消功效检查（同 banditMinSamplesFor 的钳法）
		MinSamplesLead: maxInt64(int64(cfgIntFor(tenantID, "experiment_min_samples_lead", 2200)), 1),
		MinSamplesHook: maxInt64(int64(cfgIntFor(tenantID, "experiment_min_samples_hook", 400)), 1),
		Confidence:     clampConfidence(cfgFloatFor(tenantID, "experiment_confidence", 0.95)),
	}
}

// clampConfidence 置信阈值钳到 (0.5, 1)：≤0.5 等于"抛硬币即判优"，≥1 后验概率数学上取不到
// （判优永远不结论）。两者都按误配置处理，回落默认 0.95。
func clampConfidence(v float64) float64 {
	if v <= 0.5 || v >= 1 {
		return 0.95
	}
	return v
}

// maxInt64 取大值（本包零依赖小工具，避免为一次比较引 math.MaxInt 语义歧义）。
func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// BuildTemplateSuggestionsByTenant 按租户分组跑判优（每组用自己那套 experiment_* 参数）。
// 为什么不能一次全量喂进 BuildTemplateSuggestions：分组键虽含租户，但门槛/奖励指标/置信度
// 是**单份**参数——跨租户混用等于拿 A 租户的开关去判 B 租户的话术，
// 而这三个参数恰恰是租户可自配的（见 DecisionParamsForTenant）。
// 输出按租户升序拼接、组内已由纯函数定序，整体可复现（卡片不抖动）。
func BuildTemplateSuggestionsByTenant(inputs []TemplateStatInput) []TemplateSuggestion {
	byTenant := make(map[uint][]TemplateStatInput, 8)
	for _, in := range inputs {
		byTenant[in.TenantID] = append(byTenant[in.TenantID], in)
	}
	ids := make([]uint, 0, len(byTenant))
	for tid := range byTenant {
		ids = append(ids, tid)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]TemplateSuggestion, 0, len(inputs))
	for _, tid := range ids {
		out = append(out, BuildTemplateSuggestions(byTenant[tid], DecisionParamsForTenant(tid))...)
	}
	return out
}

// sanitizeBanditMetricWithHook 判优层指标白名单：lead/arrive/deal/hook 四键（hook 仅判优用，
// 择臂奖励不含 hook——hooked 是"客户当轮回没回"的近端信号，判 A/B 显著性可以用，当长期奖励嫌短视）。
// 未知值（含 intent/eval 字面量）退 lead，理由同 sanitizeBanditMetric 的红色注释块。
func sanitizeBanditMetricWithHook(metric string) string {
	if metric == "hook" {
		return "hook"
	}
	return sanitizeBanditMetric(metric)
}

// minSamplesForMetric 按指标取门槛：hook 用稠密小门槛，lead/arrive/deal 用大门槛。
// 为什么分开：留资率 5%→7% 的双侧检验需 ≈2200/组，而接钩率 30%→35% 也需 ≈1400/组，
// 50 条只够判"灾难级劣化"（sample_min 告警口径），拿来判优就是统计学越权（D4 裁定）。
func minSamplesForMetric(metric string, lead, hook int64) int64 {
	if metric == "hook" {
		return hook
	}
	return lead
}

// normCdf 标准正态分布 CDF：Φ(x)=0.5·(1+erf(x/√2))，math.Erf 零依赖实现。
func normCdf(x float64) float64 {
	return 0.5 * (1.0 + math.Erf(x/math.Sqrt2))
}

// DecideExperiment 双臂判优纯函数：输入 (successes, trials)，输出决策 + 置信度。
//
// 判定方法：两比例正态近似（Wald）——z=(pB−pA)/sqrt(pA(1−pA)/nA + pB(1−pB)/nB)，
// P(B>A) ≈ Φ(z)，与阈值 confidence 比较。
// 为什么选正态近似而不是蒙特卡洛 Beta 后验采样：
//  1. 确定性——无随机源，单测可精确钉住边界（蒙特卡洛要么固定种子要么结果抖动）；
//  2. 精度代价可控——Wald 近似在 p 极端贴近 0/1 或小样本时覆盖率劣化（教科书已知缺陷），
//     但本函数只在双端 trials ≥ min_samples（≥400/≥2200）时才产出结论，该样本量下与
//     精确 Beta 后验的偏差 <1%；deal 率极稀疏（如 0.5%）时 z 天然保守 → 多判 not_significant
//     → 输出"继续观察"， errs 偏向"不假装精确"一侧，方向正确。
//
// 功效不足（任一端 trials < 门槛）→ 恒返回 insufficient_samples 且 Confidence=0，
// 绝不输出判优结论。
func DecideExperiment(a, b ExperimentArm, metric string, minSamplesLead, minSamplesHook int64, confidence float64) ExperimentVerdict {
	m := sanitizeBanditMetricWithHook(metric)
	minN := minSamplesForMetric(m, minSamplesLead, minSamplesHook)
	v := ExperimentVerdict{Metric: m, MinSamples: minN}
	if confidence <= 0 || confidence >= 1 {
		confidence = 0.95 // 非法阈值退默认，防配置手滑把判优变成掷硬币
	}
	if a.Trials < minN || b.Trials < minN || a.Trials <= 0 || b.Trials <= 0 {
		v.Decision = DecisionInsufficientSamples
		return v
	}
	// 计数防御性钳制：脏数据（successes>trials）按上限截断，不产生 >1 的率
	sa, sb := clampSuccesses(a), clampSuccesses(b)
	pA := float64(sa) / float64(a.Trials)
	pB := float64(sb) / float64(b.Trials)
	se2 := pA*(1-pA)/float64(a.Trials) + pB*(1-pB)/float64(b.Trials)
	if se2 <= 0 {
		// 双端率同为 0 或同为 1（方差塌缩）：无差异可判
		v.Decision = DecisionNotSignificant
		v.Confidence = 0.5
		v.ProbBOverA = 0.5
		return v
	}
	probB := normCdf((pB - pA) / math.Sqrt(se2))
	v.ProbBOverA = probB
	switch {
	case probB >= confidence:
		v.Decision = DecisionWinnerB
		v.Confidence = probB
	case 1.0-probB >= confidence:
		v.Decision = DecisionWinnerA
		v.Confidence = 1.0 - probB
	default:
		v.Decision = DecisionNotSignificant
		v.Confidence = math.Max(probB, 1.0-probB)
	}
	return v
}

// clampSuccesses 把 successes 钳进 [0, trials]，防脏聚合值穿透出 NaN/率>1。
func clampSuccesses(arm ExperimentArm) int64 {
	if arm.Successes < 0 {
		return 0
	}
	if arm.Successes > arm.Trials {
		return arm.Trials
	}
	return arm.Successes
}

// ============================================================
// L1 行动建议（模板下线/改稿卡片）——仍是纯函数，DB 装配在 api 层
// ============================================================

// TemplateStatInput 判优层的模板统计输入行（api 层从 pack_stats 快照 + templates 锚映射装配）。
type TemplateStatInput struct {
	TenantID    uint    `json:"tenant_id"`
	PackCode    string  `json:"pack_code"`
	PackVersion string  `json:"pack_version"`
	TemplateID  string  `json:"template_id"`
	AnchorType  int     `json:"anchor_type"`
	SampleCount int64   `json:"sample_count"`
	HookRate    float64 `json:"hook_rate"`
	LeadRate    float64 `json:"lead_rate"`
	ArriveRate  float64 `json:"arrive_rate"`
	DealRate    float64 `json:"deal_rate"`
}

// RewardRate 按判优指标取率列（白名单四键，未知退 lead）——红线同上：绝不吃 intent/eval。
func (s TemplateStatInput) RewardRate(metric string) float64 {
	switch sanitizeBanditMetricWithHook(metric) {
	case "hook":
		return s.HookRate
	case "arrive":
		return s.ArriveRate
	case "deal":
		return s.DealRate
	default:
		return s.LeadRate
	}
}

// 建议卡片状态枚举：suggest_review=显著低于同锚对照→建议下线/改稿；leading=显著占优；
// keep_watching=达标但不显著；insufficient_samples=功效不足（明确不下结论）；
// no_peer=同锚仅此一模板，无对照可判。
const (
	SuggestionStatusReview       = "suggest_review"
	SuggestionStatusLeading      = "leading"
	SuggestionStatusWatching     = "keep_watching"
	SuggestionStatusInsufficient = "insufficient_samples"
	SuggestionStatusNoPeer       = "no_peer"
)

// TemplateSuggestion 单模板行动建议卡片（L1：只出建议，人点确认才写库，零生成链路改动）。
type TemplateSuggestion struct {
	TenantID         uint    `json:"tenant_id"`
	PackCode         string  `json:"pack_code"`
	PackVersion      string  `json:"pack_version"`
	TemplateID       string  `json:"template_id"`
	AnchorType       int     `json:"anchor_type"`
	Status           string  `json:"status"`
	SampleCount      int64   `json:"sample_count"`
	RewardRate       float64 `json:"reward_rate"`
	AnchorMedianRate float64 `json:"anchor_median_rate"` // 同锚其余模板的中位率
	Metric           string  `json:"metric"`
	MinSamples       int64   `json:"min_samples"`
	Confidence       float64 `json:"confidence"`
	Reason           string  `json:"reason"`
}

// BuildTemplateSuggestions 判优层 L1 纯函数：按 (租户,包,版本,锚) 分组，对每个模板做
// "自身 vs 同锚其余模板聚合臂"的双臂判优，产出建议卡片列表。
//
// 判定口径：
//   - 样本 < 该指标门槛 → 恒输出 insufficient_samples（不给结论，"不假装精确"）；
//   - 同锚无其余模板 → no_peer（无对照不判）；
//   - 对照臂显著胜出（且自身率低于同锚中位数）→ suggest_review（建议下线/改稿）；
//   - 自身显著胜出 → leading；其余 → keep_watching。
//
// 为什么对照臂用"其余模板聚合"而非逐个两两检验：同锚内多 variant 逐个比会做多重比较
// （假阳性膨胀），聚合成单一对照臂只比一次，口径保守且卡片可读。
func BuildTemplateSuggestions(stats []TemplateStatInput, p DecisionParams) []TemplateSuggestion {
	type groupKey struct {
		tenantID    uint
		packCode    string
		packVersion string
		anchorType  int
	}
	groups := map[groupKey][]TemplateStatInput{}
	for _, s := range stats {
		k := groupKey{s.TenantID, s.PackCode, s.PackVersion, s.AnchorType}
		groups[k] = append(groups[k], s)
	}
	minN := minSamplesForMetric(p.Metric, p.MinSamplesLead, p.MinSamplesHook)
	out := make([]TemplateSuggestion, 0, len(stats))
	for k, members := range groups {
		// 中位数：同锚全部成员（含自身）的率中位，作为"低于同伴"的直观口径
		median := medianRates(members, p.Metric)
		for _, s := range members {
			rate := clampRate(s.RewardRate(p.Metric))
			item := TemplateSuggestion{
				TenantID: k.tenantID, PackCode: k.packCode, PackVersion: k.packVersion,
				TemplateID: s.TemplateID, AnchorType: k.anchorType,
				SampleCount: s.SampleCount, RewardRate: rate,
				AnchorMedianRate: median, Metric: p.Metric, MinSamples: minN,
			}
			switch {
			case s.SampleCount < minN:
				// 功效不足：明确不下结论（验收口径：接口必须回显 insufficient_samples）
				item.Status = SuggestionStatusInsufficient
				item.Reason = "样本不足判定门槛，继续积累数据，暂不出判优结论"
			default:
				var selfArm, peerArm ExperimentArm
				selfArm = ExperimentArm{Successes: int64(math.Round(rate * float64(s.SampleCount))), Trials: s.SampleCount}
				peerN, peerSucc, peerCnt := int64(0), int64(0), 0
				for _, o := range members {
					if o.TemplateID == s.TemplateID {
						continue
					}
					or := clampRate(o.RewardRate(p.Metric))
					peerN += o.SampleCount
					peerSucc += int64(math.Round(or * float64(o.SampleCount)))
					peerCnt++
				}
				peerArm = ExperimentArm{Successes: peerSucc, Trials: peerN}
				if peerCnt == 0 || peerArm.Trials <= 0 {
					item.Status = SuggestionStatusNoPeer
					item.Reason = "同锚无其余模板，缺少对照臂不做判优"
				} else {
					// a=同锚对照臂，b=被评模板：winner_a ⇒ 对照显著更好 ⇒ 被评模板建议下线/改稿
					verdict := DecideExperiment(peerArm, selfArm, p.Metric, p.MinSamplesLead, p.MinSamplesHook, p.Confidence)
					item.Confidence = verdict.Confidence
					switch verdict.Decision {
					case DecisionWinnerA:
						if rate < median {
							item.Status = SuggestionStatusReview
							item.Reason = "奖励率显著低于同锚其余模板且低于中位数，建议人工复核话术（下线或改稿）"
						} else {
							// 中位数被个别强臂拉高、整体检验仍输——按观察处理，防误杀
							item.Status = SuggestionStatusWatching
							item.Reason = "整体对照显著占优但率不低于同锚中位数，保持观察"
						}
					case DecisionWinnerB:
						item.Status = SuggestionStatusLeading
						item.Reason = "显著优于同锚对照，可作为该锚主力话术"
					default:
						item.Status = SuggestionStatusWatching
						item.Reason = "样本达标但差异未过置信阈值，保持观察"
					}
				}
			}
			out = append(out, item)
		}
	}
	// 输出定序：租户→包→版本→锚→模板ID，防 map 遍历序造成卡片抖动（快照可复现）
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.TenantID != b.TenantID {
			return a.TenantID < b.TenantID
		}
		if a.PackCode != b.PackCode {
			return a.PackCode < b.PackCode
		}
		if a.PackVersion != b.PackVersion {
			return a.PackVersion < b.PackVersion
		}
		if a.AnchorType != b.AnchorType {
			return a.AnchorType < b.AnchorType
		}
		return a.TemplateID < b.TemplateID
	})
	return out
}

// medianRates 同组模板奖励率中位数（空组回 0；两元素取均值）。
func medianRates(rows []TemplateStatInput, metric string) float64 {
	if len(rows) == 0 {
		return 0
	}
	rates := make([]float64, 0, len(rows))
	for _, r := range rows {
		rates = append(rates, clampRate(r.RewardRate(metric)))
	}
	sort.Float64s(rates)
	n := len(rates)
	if n%2 == 1 {
		return rates[n/2]
	}
	return (rates[n/2-1] + rates[n/2]) / 2
}

// clampRate 率值钳进 [0,1]，防脏快照值污染卡片展示与判优计数。
func clampRate(r float64) float64 {
	if r < 0 || math.IsNaN(r) {
		return 0
	}
	if r > 1 {
		return 1
	}
	return r
}

// ============================================================
// L2 晋升计划（本批只算不执行——experiment_auto_promote 默认 false，无自动写库路径被触发）
// ============================================================

// PromotePlan 晋升计算结果：ShouldApply=true 才允许写库；false 时 BlockedReason 说明拦在哪一关。
// 量纲（2026-09-23 拍板）：全部按 templates.ab_weight 的既有语义——**0~100 整数百分点**，
// 与人工改权重的入参校验（api/strategy.go 0~100）和分桶择一（template.go 权重和）同源，
// 不再另立"倍数权重"第二套口径。
type PromotePlan struct {
	ShouldApply    bool      `json:"should_apply"`
	TemplateID     string    `json:"template_id"`
	PreviousWeight int       `json:"previous_weight"`
	NewWeight      int       `json:"new_weight"` // 晋升后的整数百分点权重（恒钳在 [0,100]）
	BlockedReason  string    `json:"blocked_reason"`
	ComputedAt     time.Time `json:"computed_at"`
}

// PromoteDecision 胜出臂 ab_weight 上调计划计算（纯函数，L2 骨架）。
//
// 规则：仅 winner_b（实验臂显著胜出）可晋升；冷却未过拒绝；新权重 = min(prev+step, max)；
// step/maxWeight 均为**百分点整数**（与 ab_weight 列同量纲，见 PromotePlan 注释）。
// 硬钳 [0, abWeightCeil]：配置被写成 500 也只会顶到 100——自动晋升绝不能产出
// 人工入口拒收的权重值（api/strategy.go 校验 0~100），否则下一步人工编辑就会撞校验。
func PromoteDecision(templateID string, prevWeight int, verdict ExperimentVerdict, step, maxWeight int, cooldownRemaining time.Duration, now time.Time) PromotePlan {
	plan := PromotePlan{TemplateID: templateID, PreviousWeight: prevWeight, NewWeight: prevWeight, ComputedAt: now}
	if verdict.Decision != DecisionWinnerB {
		plan.BlockedReason = "no_winner_or_insufficient"
		return plan
	}
	if cooldownRemaining > 0 {
		plan.BlockedReason = "cooldown"
		return plan
	}
	if step <= 0 {
		plan.BlockedReason = "invalid_step"
		return plan
	}
	next := prevWeight + step
	if ceil := maxWeight; ceil > 0 && next > ceil {
		next = ceil
	}
	if next > abWeightCeil {
		next = abWeightCeil
	}
	if next <= prevWeight {
		plan.BlockedReason = "weight_at_max" // 已到/超过上限：无上调空间
		return plan
	}
	plan.ShouldApply = true
	plan.NewWeight = next
	return plan
}

// abWeightCeil templates.ab_weight 的语义上限（0~100 百分点分流权重）
const abWeightCeil = 100
