// Package strategy 择臂层（批五 B，2026-09-23）：Step4 召回候选在"取最高"之前按 pack_stats 后验重排。
//
// 设计纪律完全对齐 service/rerank.go 的 E9 fail-open 风格：
// 开关关 / pack_stats 无行 / 样本不足 / 读取报错 / 候选越界 → 一律退回原规则分，
// **只改候选顺序、绝不改候选集合**；template_bandit_enabled 默认 false 时字节级零行为变化。
package strategy

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"log"
	"math"
	"math/rand"
	"sort"
	"time"
)

// ============================================================
// 常量与钳制（候选数上下界，对齐 kbRerankCandidates 的 [2,32] 钳法）
// ============================================================

const (
	// banditMinHeadSize 低于 2 个候选无从"择臂"（单臂重排无意义），直接退规则序
	banditMinHeadSize = 2
	// banditCandidateCap 单次参与择臂的候选上限：防模板池异常膨胀时打分开销失控
	banditCandidateCap = 32
	// banditExploreC UCB 探索系数 c：bonus = c·sqrt(ln N / n)。取 1.0 是伯努利臂经验中值，
	// 调大 → 冷臂过度探索，调小 → 过早收敛。
	banditExploreC = 1.0
	// banditStatWeight 量纲桥：最终分 = MatchScore + banditStatWeight·统计增量。
	// 为什么要权重：规则分量纲 ~[0,2]（tagMatch 0~1 + priority·0.1），统计增量 [0, ~1.5]，
	// 1:1 相加会让高 Priority 永远压过真实转化数据——正是 A5 裁定"Priority 恒赢"的病灶复刻。
	// ×3 使样本达标臂的后验差成为锚内主因，Priority 只剩冷启动先验与兜底话语权。
	banditStatWeight = 3.0
	// banditPriorK 先验伪样本强度：Priority 退化而来的先验相当于 K 条伪观测，
	// 为什么取 10：远小于 min_samples(50)，保证真实数据到位后先验只起"兜底方向"作用，
	// 不会把人工优先级固化成永远打不破的墙（A5 裁定的核心病灶就是 Priority 恒赢）。
	banditPriorK = 10.0
)

// ============================================================
// 奖励红线（reward hacking 封堵）——主代理批五 A 裁定的同款约束：
// intent_before/intent_after 由 Step7_UpdateIntent 规则产出、eval_score 由离线评分函数产出，
// 它们都是**策略自身的输出**。拿它们当奖励 = 让择臂层学会"挑让规则加分的话说"，
// 与真实销售结果（留资/到店/成交）脱钩，自动化越成功越歪。
// 因此 metric 白名单只允许 lead/arrive/deal 三键；任何未知值（含 intent/eval 字面量）
// 一律退默认 lead——绝不新增"策略自产出"指标进白名单。
// ============================================================

// sanitizeBanditMetric 奖励指标白名单收敛：lead|arrive|deal 之外的任何值（含 intent_after/eval_score
// 等策略自产出指标的字面量）一律退默认 lead，从入口上封死 reward hacking。
func sanitizeBanditMetric(metric string) string {
	switch metric {
	case "arrive", "deal":
		return metric
	default:
		return "lead"
	}
}

// banditRewardRate 从 pack_stats 快照行取奖励率列——同样只认三键白名单（未知退 lead）。
func banditRewardRate(row model.PackStatSnapshot, metric string) float64 {
	switch sanitizeBanditMetric(metric) {
	case "arrive":
		return row.ArriveRate
	case "deal":
		return row.DealRate
	default:
		return row.LeadRate
	}
}

// BanditArmStat 一臂（锚内 template_id）的后验素材：来自 pack_stats 物化快照（批五 A 产出）。
type BanditArmStat struct {
	SampleCount int64
	Rate        float64 // 已按 experiment_reward_metric 选列的奖励率（0~1）
}

// BanditStatsLoader 择臂后验读取函数签名——可注入：默认走 DB，单测换内存实现（不碰库即可复现）。
type BanditStatsLoader func(tenantID uint, templateIDs []string, metric string) (map[string]BanditArmStat, error)

// banditStatsLoader 全局注入点（同 rerank 的 DefaultRerankClient 惯例：nil/失败 → fail-open）。
var banditStatsLoader BanditStatsLoader = loadBanditStatsFromDB

// banditRandSeed thompson 模式随机源注入点：生产取当前纳秒；单测替换为固定种子即可复现采样序。
// 为什么不做全局共享 rng：并发召回下共享 rand.Rand 需要加锁，而 thompson 每次重排本来就是一次性
// 决策，用种子新建即可，锁都省了。
var banditRandSeed = func() int64 { return time.Now().UnixNano() }

// ============================================================
// 热配置读取（nil 安全，全部 runtimecfg Safe*/ForTenant 口径；未播种=关）
// ============================================================

// banditEnabledFor 择臂开关：租户覆盖 → 系统默认 → false。
// 主链零风险约束：未播种/单例未初始化一律视为关，行为与插入前逐字节一致。
func banditEnabledFor(tenantID uint) bool {
	svc := runtimecfg.DefaultSystemConfigService
	if svc == nil {
		return false
	}
	return svc.GetBoolForTenant(tenantID, "template_bandit_enabled", false)
}

// banditModeFor 择臂策略：ucb（默认，确定性可复现）|thompson（Beta 后验采样）。未知值退 ucb。
func banditModeFor(tenantID uint) string {
	svc := runtimecfg.DefaultSystemConfigService
	if svc == nil {
		return "ucb"
	}
	if m := svc.GetStringForTenant(tenantID, "template_bandit_mode", "ucb"); m == "thompson" {
		return "thompson"
	}
	return "ucb"
}

// banditMinSamplesFor 择臂启用的最小样本数：低于此值该臂退规则分（冷启动保护）。钳下限 1。
func banditMinSamplesFor(tenantID uint) int64 {
	svc := runtimecfg.DefaultSystemConfigService
	if svc == nil {
		return 50
	}
	n := svc.GetIntForTenant(tenantID, "template_bandit_min_samples", 50)
	if n < 1 {
		n = 1
	}
	return int64(n)
}

// banditMetricFor 奖励指标（复用实验层 experiment_reward_metric 键）：白名单收敛见 sanitizeBanditMetric。
func banditMetricFor(tenantID uint) string {
	svc := runtimecfg.DefaultSystemConfigService
	if svc == nil {
		return "lead"
	}
	return sanitizeBanditMetric(svc.GetStringForTenant(tenantID, "experiment_reward_metric", "lead"))
}

// ============================================================
// Beta-Bernoulli 后验与打分队列
// ============================================================

// banditPriorFromPriority 人工 Priority 退化为 Beta 先验超参 (α0, β0)。
// 为什么这么映射：priorMean = 0.5 + 0.5·p/(p+K) 把 Priority=0 落在**无信息先验** Beta(6,6)
// （均值 0.5，中性冷启动不预设好坏），Priority 越大越偏 1 但永不到 1（保留翻盘空间）；
// 伪样本强度固定 banditPriorK=10，即先验只值约 10 条观测——
// 真实样本 ≥ min_samples(50) 后数据必然主导后验，Priority 只剩冷启动方向性（"先验与兜底"语义）。
func banditPriorFromPriority(priority int) (float64, float64) {
	p := float64(priority)
	if p < 0 {
		p = 0 // 负 Priority 钳 0，退无信息先验
	}
	priorMean := 0.5 + 0.5*(p/(p+banditPriorK))
	return 1.0 + priorMean*banditPriorK, 1.0 + (1.0-priorMean)*banditPriorK
}

// bandArmScore 计算单臂统计增量分。返回 0 = 该臂维持原规则分（每臂独立 fail-open）。
//
// 公式：
//   - successes = round(rate·n)，clamp [0,n]（pack_stats 率是浮点聚合值，还原成伯努利计数）
//   - 后验 α = α0 + successes，β = β0 + (n − successes)
//   - ucb：mean + c·sqrt(ln N / n)——确定性、同输入必同输出（可复现模式）
//   - thompson：θ ~ Beta(α,β) 采样——探索更平滑，随机源由 banditRandSeed 注入
//
// 为什么"增量"而非"替换"规则分：最终分 = MatchScore + 统计增量，无后验臂增量恒 0，
// 整层关掉时公式退化即原规则排序——fail-open 不是补丁而是评分函数的自然边界。
func banditArmScore(stat BanditArmStat, hasStat bool, minSamples int64, mode string, priority int, totalSamples int64, rng *rand.Rand) float64 {
	if !hasStat {
		return 0
	}
	n := stat.SampleCount
	if n < minSamples || n <= 0 {
		return 0 // 样本不足：该臂不启用，退规则分
	}
	if stat.Rate < 0 {
		return 0 // 脏数据防御：负率视为无后验
	}
	successes := math.Round(stat.Rate * float64(n))
	if successes > float64(n) {
		successes = float64(n) // 率>1 的脏聚合值钳回，绝不让 NaN/Inf 进排序
	}
	alpha0, beta0 := banditPriorFromPriority(priority)
	alpha := alpha0 + successes
	beta := beta0 + float64(n) - successes
	var value float64
	switch mode {
	case "thompson":
		if rng == nil {
			value = alpha / (alpha + beta) // 随机源缺失 → 退后验均值（确定性兜底，不 panic）
		} else {
			value = betaSample(alpha, beta, rng)
		}
	default: // ucb
		mean := alpha / (alpha + beta)
		N := float64(totalSamples)
		if N < math.E {
			N = math.E // ln N ≥ 1，防总样本过小探索项塌成 0
		}
		value = mean + banditExploreC*math.Sqrt(math.Log(N)/float64(n))
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0 // 计算异常 → 该臂退规则分（整层 fail-open 纪律的臂级版本）
	}
	return value
}

// rankRecallCandidates 择臂重排主入口：输入候选（不改动），输出**同一集合**的新顺序。
//
// 顺序语义：先按 MatchScore 稳定降序得到"规则序"——它与旧实现"取最高（首个最大值）"
// 逐位等价（稳定排序保持并列项的原始相对序），因此开关关/任何 fail-open 路径下
// 取 ranked[0] 与旧代码取 best 的结果完全一致，主链零行为变化。
// 开关开时仅对前 banditCandidateCap 个候选按 规则分+统计增量 稳定重排，尾部原样保留
// （对齐 applyRerank"不动第 N 名之后的尾部"）。
func rankRecallCandidates(candidates []RecalledTemplate, tenantID uint) []RecalledTemplate {
	ordered := make([]RecalledTemplate, len(candidates))
	copy(ordered, candidates)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].MatchScore > ordered[j].MatchScore })

	if len(ordered) < banditMinHeadSize || !banditEnabledFor(tenantID) {
		return ordered
	}
	head := banditCandidateCap
	if head > len(ordered) {
		head = len(ordered)
	}
	if head < banditMinHeadSize {
		return ordered
	}
	ids := make([]string, 0, head)
	for _, c := range ordered[:head] {
		if c.Template != nil {
			ids = append(ids, c.Template.ID)
		}
	}
	metric := banditMetricFor(tenantID)
	stats, err := banditStatsLoader(tenantID, ids, metric)
	if err != nil {
		log.Printf("[模板择臂] 后验读取失败，回退规则序: tenant=%d err=%v", tenantID, err)
		return ordered
	}
	mode := banditModeFor(tenantID)
	minSamples := banditMinSamplesFor(tenantID)
	var rng *rand.Rand
	if mode == "thompson" {
		rng = rand.New(rand.NewSource(banditRandSeed()))
	}
	var total int64
	for _, st := range stats {
		if st.SampleCount > 0 {
			total += st.SampleCount
		}
	}
	// 逐臂打分：finalScore = 规则分 + banditStatWeight·统计增量（无后验/样本不足/异常 → 增量 0 = 原规则分）
	scores := make([]float64, head)
	for i, c := range ordered[:head] {
		priority := 0
		tid := ""
		if c.Template != nil {
			priority = c.Template.Priority
			tid = c.Template.ID
		}
		st, ok := stats[tid]
		scores[i] = c.MatchScore + banditStatWeight*banditArmScore(st, ok, minSamples, mode, priority, total, rng)
	}
	idx := make([]int, head)
	for i := range idx {
		idx[i] = i
	}
	// 稳定排序：并列得分保持规则序 → 整层确定性（UCB 模式完全可复现；thompson 同种子可复现）
	sort.SliceStable(idx, func(a, b int) bool { return scores[a] > scores[b] })
	ranked := make([]RecalledTemplate, 0, len(ordered))
	for _, i := range idx {
		ranked = append(ranked, ordered[i])
	}
	ranked = append(ranked, ordered[head:]...) // 尾部（超出候选钳上限部分）原样保留，集合不变
	return ranked
}

// loadBanditStatsFromDB 默认后验读取器：pack_stats 物化快照（批五 A 产出，本层只读不写）。
// 同一 template_id 跨多包版本并存时取 computed_at 最新一条快照。
// tenantID=0（系统预置语境/无租户后台任务）不发查询——择臂只对有归因数据的租户生效。
func loadBanditStatsFromDB(tenantID uint, templateIDs []string, metric string) (map[string]BanditArmStat, error) {
	out := make(map[string]BanditArmStat, len(templateIDs))
	if tenantID == 0 || len(templateIDs) == 0 {
		return out, nil
	}
	var rows []model.PackStatSnapshot
	if err := db.DB.Where("tenant_id = ? AND template_id IN ?", tenantID, templateIDs).
		Order("computed_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		if _, seen := out[r.TemplateID]; seen {
			continue // computed_at DESC 已保证首见即最新快照
		}
		out[r.TemplateID] = BanditArmStat{SampleCount: r.SampleCount, Rate: banditRewardRate(r, metric)}
	}
	return out, nil
}

// betaSample Beta(α,β) 采样：X~Gamma(α,1), Y~Gamma(β,1)，返回 X/(X+Y)。
// 为什么自己实现：不引第三方依赖（项目铁律），math/rand 已够用且配合注入种子完全可复现。
func betaSample(alpha, beta float64, rng *rand.Rand) float64 {
	if alpha <= 0 || beta <= 0 || rng == nil {
		if alpha+beta == 0 {
			return 0.5
		}
		return alpha / (alpha + beta) // 退化参数 → 直接回后验均值，不 panic
	}
	x := gammaSample(alpha, rng)
	y := gammaSample(beta, rng)
	if x+y == 0 {
		return 0.5
	}
	return x / (x + y)
}

// gammaSample shape 尺度 Gamma(shape,1) 采样，Marsaglia-Tsang 转导法；shape<1 用 a+1 提升技巧。
func gammaSample(shape float64, rng *rand.Rand) float64 {
	if shape <= 0 {
		return 0
	}
	if shape < 1 {
		// Gamma(a) = Gamma(a+1)·U^(1/a)：把 boost 后的结果乘回来
		return gammaSample(shape+1, rng) * math.Pow(rng.Float64(), 1.0/shape)
	}
	d := shape - 1.0/3.0
	c := 1.0 / math.Sqrt(9.0*d)
	for {
		x := rng.NormFloat64()
		v := 1.0 + c*x
		if v <= 0 {
			continue // 拒绝非正支，保持接受域正确性
		}
		v3 := v * v * v
		u := rng.Float64()
		if u < 1.0-0.0331*x*x*x*x {
			return d * v3 // 快速接受分支
		}
		if math.Log(u) < 0.5*x*x+d*(1.0-v3+math.Log(v3)) {
			return d * v3
		}
	}
}
