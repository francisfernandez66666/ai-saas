// replayeval 离线回放评测的纯逻辑层（PLAN_FIX_2026-09-22 §6-E，批五 E「黄金集换目标」）。
// 职责：四桶分层选样、裁判盲评左右随机序（可复现去偏）、裁判输出健壮解析、
// 分桶胜负聚合与 Markdown/stdout 报告渲染。
// 全部为纯函数（随机序按注入种子派生），不碰 DB 与网络——main.go 只做组装与真实接线，
// 单测用假数据即可全覆盖，真连库部分另测。
package main

import (
	"fmt"
	"hash/fnv"
	"math/rand"
	"sort"
	"strings"
	"time"
)

// 四桶常量：按「终局转化标签」优先级的互斥分桶（批五 A 回填的 arrived/dealt 最高，
// 其次当轮留资 lead_captured，再次当轮接钩 hooked，全无线索 none）。
// 为什么按终局优先：本评测要回答的是「新策略有没有让成交率变高」，
// 越接近终局的样本越贵、越该单独看胜率，不能被海量 none 样本稀释。
const (
	bucketConverted = "converted" // 客户最终到店或成交（arrived_at/dealt_at 任一非空）
	bucketLead      = "lead"      // 未走到终局但当轮留资
	bucketHooked    = "hooked"    // 未留资但当轮接钩
	bucketNone      = "none"      // 无任何正反馈（窗口内也可能只是还没兑现，报告如实标注）
)

// bucketOrder 固定桶序：选样轮转与报告渲染共用，保证同输入同输出（确定性）。
var bucketOrder = []string{bucketConverted, bucketLead, bucketHooked, bucketNone}

// outcomeFlags 一条归因行的正反馈信号（DB 列的布尔化视图，nil 时间视为 false）。
type outcomeFlags struct {
	Hooked       bool
	LeadCaptured bool
	Arrived      bool
	Dealt        bool
}

// classifyOutcome 按优先级 converted > lead > hooked > none 归桶（互斥，一行只进一桶）。
func classifyOutcome(o outcomeFlags) string {
	switch {
	case o.Arrived || o.Dealt:
		return bucketConverted
	case o.LeadCaptured:
		return bucketLead
	case o.Hooked:
		return bucketHooked
	default:
		return bucketNone
	}
}

// replaySample 一条回放样本：归因行 + 关联 AI 消息原文（历史真实回复）+ 终局桶。
type replaySample struct {
	AttributionID  uint
	MessageID      uint
	ConversationID uint
	CustomerID     uint
	CreatedAt      time.Time
	HistoryReply   string // 当年真实发出的 AI 回复（messages.content，sender_type='ai'）
	Bucket         string
}

// stratifySamples 从候选池按四桶分层挑出 limit 条：桶内按时间倒序（新样本的画像/上下文
// 与当前策略更接近），桶间轮转（round-robin）补齐——保证小桶（converted 最稀缺）不被
// 大桶挤光，每桶都拿到近似均匀的代表性。整体确定性：同池同 limit 必得同结果。
func stratifySamples(pool []replaySample, limit int) []replaySample {
	if limit <= 0 || len(pool) == 0 {
		return nil
	}
	groups := make(map[string][]replaySample, len(bucketOrder))
	for _, s := range pool {
		groups[s.Bucket] = append(groups[s.Bucket], s)
	}
	for _, b := range groups {
		sort.SliceStable(b, func(i, j int) bool {
			if !b[i].CreatedAt.Equal(b[j].CreatedAt) {
				return b[i].CreatedAt.After(b[j].CreatedAt)
			}
			return b[i].AttributionID > b[j].AttributionID
		})
	}
	out := make([]replaySample, 0, limit)
	// 轮转指针：每轮从每个非空桶各取 1 条，直到取满 limit 或全部取空
	for idx := 0; len(out) < limit; idx++ {
		progress := false
		for _, b := range bucketOrder {
			if idx >= len(groups[b]) {
				continue
			}
			out = append(out, groups[b][idx])
			progress = true
			if len(out) >= limit {
				break
			}
		}
		if !progress {
			break
		}
	}
	return out
}

// pickReplaySide 盲评左右随机序：以 (seed, 样本ID) 的 FNV 哈希为种子派生一枚公平硬币，
// 返回回放回复应放置的一侧（"A" 或 "B"）。
// 为什么要稳定派生而不是全局 rand：①同 seed 同样本两次跑必得同序——报告可复现、可审计；
// ②样本间哈希近似独立——大数下两侧分布趋近对半，抵消裁判模型的「位置偏好」这一已知偏差。
func pickReplaySide(seed int64, sampleID uint) string {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%d|%d", seed, sampleID)
	rnd := rand.New(rand.NewSource(int64(h.Sum64() >> 1)))
	if rnd.Intn(2) == 0 {
		return "A"
	}
	return "B"
}

// parseJudgeVerdict 健壮解析裁判模型输出，返回 (verdict, parsed)：
// verdict ∈ {"win","tie","loss"}，视角恒为「回放回复相对历史真实回复」。
// 约定格式是单词 A / B / 平局，但裁判模型经常多话，故按序解析：
//  1. 含平局语义（平局/持平/相当/tie/draw）→ tie（有效票）；
//  2. 否则取文本中第一个「独立」出现的 A/B 字母为胜方——独立的判据是左右邻均非
//     ASCII 字母/数字，因此中文紧邻（"回复A更好"）可判，而 "AB"、"A1" 这类粘连词不算票；
//  3. 解析不出 → 记平局并返回 parsed=false（废票单独计数，绝不 panic、绝不猜票）。
func parseJudgeVerdict(raw, replaySide string) (string, bool) {
	s := strings.TrimSpace(raw)
	lower := strings.ToLower(s)
	for _, kw := range []string{"平局", "持平", "相当", "tie", "draw"} {
		if strings.Contains(lower, kw) {
			return "tie", true
		}
	}
	side, ok := firstStandaloneAB(s)
	if !ok {
		return "tie", false
	}
	winner := "A"
	if side == 'B' {
		winner = "B"
	}
	if winner == replaySide {
		return "win", true
	}
	return "loss", true
}

// firstStandaloneAB 返回文本中第一个独立的 A/B 字母（大写化后扫描）；
// 独立=左右字节都不是 ASCII 字母/数字。找不到返回 (0,false)。
func firstStandaloneAB(s string) (byte, bool) {
	up := strings.ToUpper(s)
	for i := 0; i < len(up); i++ {
		c := up[i]
		if c != 'A' && c != 'B' {
			continue
		}
		if i > 0 && isASCIIAlnum(up[i-1]) {
			continue
		}
		if i+1 < len(up) && isASCIIAlnum(up[i+1]) {
			continue
		}
		return c, true
	}
	return 0, false
}

// isASCIIAlnum 判断是否 ASCII 字母/数字（汉字等多字节 UTF-8 序列均不在其列，
// 所以中文紧邻的 A/B 视为独立票，符合中文裁判输出的常见形态）。
func isASCIIAlnum(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

// bucketTally 单桶（含"合计"行复用此结构）胜负聚合。
type bucketTally struct {
	Bucket      string
	Selected    int // 入样条数（含后续被跳过的）
	Win         int // 有效票：回放胜
	Tie         int // 有效票：平（含解析失败按平局计的废票）
	Loss        int // 有效票：回放负
	Unparsed    int // 裁判有回复但解析失败 → 记平局并在此计数
	JudgeErrors int // 裁判调用失败（该样本不产票）
}

// votes 有效票数（胜+平+负）。
func (t bucketTally) votes() int { return t.Win + t.Tie + t.Loss }

// winRate 回放胜率（分母=有效票；无票返回 0，报告按 n=0 呈现，不装出结论）。
func (t bucketTally) winRate() float64 {
	if v := t.votes(); v > 0 {
		return float64(t.Win) / float64(v)
	}
	return 0
}

// tieRate 平局率（含废票），报告要求必须给出。
func (t bucketTally) tieRate() float64 {
	if v := t.votes(); v > 0 {
		return float64(t.Tie) / float64(v)
	}
	return 0
}

// evalReport 一次回放评测的全量状态：main 与单测共用的渲染数据源。
type evalReport struct {
	TenantID       uint
	Days           int
	Limit          int
	Seed           int64
	DryRun         bool
	PoolSize       int            // 候选池条数（分层前）
	PoolDist       map[string]int // 候选池四桶分布（必须随报告打印）
	Tallies        map[string]*bucketTally
	JudgeExecuted  bool     // 是否至少产出过一张有效票（全失败=整批 SKIP）
	JudgeErrors    int      // 裁判调用失败总数
	GenerateFailed int      // 回放生成/取题链路报错总数（DB 故障、模型异常等）
	SkippedNoInput int      // 样本实体缺失（无触发消息/客户/会话已删）而跳过的样本数
	Notes          []string // 如实披露项（口径/副作用/降级说明）
}

// newEvalReport 构造空报告，四桶聚合位预置（渲染顺序恒按 bucketOrder）。
func newEvalReport(o options, poolSize int, poolDist map[string]int) *evalReport {
	tallies := make(map[string]*bucketTally, len(bucketOrder))
	for _, b := range bucketOrder {
		tallies[b] = &bucketTally{Bucket: b}
	}
	return &evalReport{
		TenantID: o.TenantID, Days: o.Days, Limit: o.Limit, Seed: o.Seed, DryRun: o.DryRun,
		PoolSize: poolSize, PoolDist: poolDist, Tallies: tallies,
	}
}

// recordSelected 记一条入样（先计数，之后可能因缺触发消息/生成失败而不产票）。
func (r *evalReport) recordSelected(bucket string) {
	if t, ok := r.Tallies[bucket]; ok {
		t.Selected++
	}
}

// recordVerdict 记一张裁判票；parsed=false 的废票按平局计入并单独计数。
func (r *evalReport) recordVerdict(bucket, verdict string, parsed bool) {
	t, ok := r.Tallies[bucket]
	if !ok {
		return
	}
	if !parsed {
		t.Unparsed++
	}
	switch verdict {
	case "win":
		t.Win++
	case "loss":
		t.Loss++
	default:
		t.Tie++
	}
	if parsed {
		r.JudgeExecuted = true
	}
}

// recordJudgeError 记一次裁判调用失败（不进胜负，只进 SKIP 判定）。
func (r *evalReport) recordJudgeError(bucket string) {
	r.JudgeErrors++
	if t, ok := r.Tallies[bucket]; ok {
		t.JudgeErrors++
	}
}

// total 合计聚合行（跨桶加和）。
func (r *evalReport) total() bucketTally {
	sum := bucketTally{Bucket: "合计"}
	for _, b := range bucketOrder {
		t := r.Tallies[b]
		sum.Selected += t.Selected
		sum.Win += t.Win
		sum.Tie += t.Tie
		sum.Loss += t.Loss
		sum.Unparsed += t.Unparsed
		sum.JudgeErrors += t.JudgeErrors
	}
	return sum
}

// conclusion 一句话结论（stdout 与报告共用）。
func (r *evalReport) conclusion() string {
	if r.DryRun {
		return "dry-run 选样报告：零 AI 调用，不含质量结论"
	}
	tot := r.total()
	if !r.JudgeExecuted {
		return "裁判模型不可用或未产出任何有效票，本轮评测整批 SKIP，不产出质量结论"
	}
	switch {
	case tot.Win > tot.Loss:
		return fmt.Sprintf("当前策略的回放回复整体优于历史真实回复（胜 %d / 负 %d），支持继续按现策略迭代", tot.Win, tot.Loss)
	case tot.Win < tot.Loss:
		return fmt.Sprintf("回归警告：当前策略的回放回复劣于历史真实回复（胜 %d / 负 %d），发布前应人工复盘劣化桶", tot.Win, tot.Loss)
	default:
		return fmt.Sprintf("回放与历史版本无显著差异（胜 %d = 负 %d），单凭本轮样本不足以判断策略变化", tot.Win, tot.Loss)
	}
}

// summaryLines stdout 摘要：每桶 胜/平/负/样本数 + 胜率/平局率 + 一句话结论。
// 规范要求「必须打印样本数与桶内分布」——池分布与逐桶 n 都在此。
func (r *evalReport) summaryLines() []string {
	lines := []string{
		fmt.Sprintf("回放评测 tenant=%d days=%d limit=%d seed=%d dry-run=%v",
			r.TenantID, r.Days, r.Limit, r.Seed, r.DryRun),
		fmt.Sprintf("候选池 %d 条，桶分布: converted=%d lead=%d hooked=%d none=%d",
			r.PoolSize, r.PoolDist[bucketConverted], r.PoolDist[bucketLead], r.PoolDist[bucketHooked], r.PoolDist[bucketNone]),
	}
	for _, b := range bucketOrder {
		t := r.Tallies[b]
		lines = append(lines, fmt.Sprintf("  %-9s 胜%d 平%d 负%d 样本%d（胜率 %.1f%% 平局率 %.1f%%，废票%d 裁判失败%d）",
			b, t.Win, t.Tie, t.Loss, t.Selected, t.winRate()*100, t.tieRate()*100, t.Unparsed, t.JudgeErrors))
	}
	tot := r.total()
	lines = append(lines, fmt.Sprintf("  %-9s 胜%d 平%d 负%d 样本%d", "合计", tot.Win, tot.Tie, tot.Loss, tot.Selected))
	if r.GenerateFailed > 0 || r.SkippedNoInput > 0 {
		lines = append(lines, fmt.Sprintf("  链路报错（生成/取题失败）%d 条，样本实体缺失（无触发消息/客户/会话）跳过 %d 条", r.GenerateFailed, r.SkippedNoInput))
	}
	lines = append(lines, "结论: "+r.conclusion())
	return lines
}

// markdown 渲染本地报告（REPLAY_EVAL.md）。所有口径与局限如实入文，不粉饰。
func (r *evalReport) markdown() string {
	var b strings.Builder
	b.WriteString("# AI 销售回放评测报告（replayeval）\n\n")
	fmt.Fprintf(&b, "- 生成时间: %s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&b, "- 租户: %d | 样本窗口: 最近 %d 天 | 回放上限: %d | 盲评种子: %d | dry-run: %v\n",
		r.TenantID, r.Days, r.Limit, r.Seed, r.DryRun)
	b.WriteString("- 评测目标: 用**当前策略**重放历史 AI 回复位，经 LLM 裁判盲评比「回放 vs 历史真实回复」的优劣，" +
		"按**终局转化标签**分桶看胜率——刻意不使用 `eval_score`/`intent_after`（它们是策略自己的输出，拿来当目标是 reward hacking）。\n\n")

	b.WriteString("## 选样\n\n")
	fmt.Fprintf(&b, "候选池 %d 条（reply_attributions ⨝ messages，仅 sender_type='ai' 且关联客户有效），分层后入样：\n\n", r.PoolSize)
	b.WriteString("| 桶 | 判定口径 | 池内条数 | 入样条数 |\n|---|---|---:|---:|\n")
	descs := map[string]string{
		bucketConverted: "arrived_at/dealt_at 任一非空（终局：到店或成交）",
		bucketLead:      "当轮留资 lead_captured",
		bucketHooked:    "当轮接钩 hooked",
		bucketNone:      "无任何正反馈（窗口内可能尚未兑现，谨慎解读）",
	}
	for _, bk := range bucketOrder {
		fmt.Fprintf(&b, "| %s | %s | %d | %d |\n", bk, descs[bk], r.PoolDist[bk], r.Tallies[bk].Selected)
	}

	b.WriteString("\n## 盲评结果\n\n")
	if r.DryRun {
		b.WriteString("dry-run 模式未调用任何 AI，无胜负数据。显式 `-dry-run=false` 才会真回放+真裁判（消耗真实 token）。\n")
	} else {
		b.WriteString("| 桶 | 样本数 | 胜 | 平 | 负 | 胜率 | 平局率 | 废票(解析失败按平局) | 裁判调用失败 |\n|---|---:|---:|---:|---:|---:|---:|---:|---:|\n")
		for _, bk := range bucketOrder {
			t := r.Tallies[bk]
			fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %.1f%% | %.1f%% | %d | %d |\n",
				bk, t.Selected, t.Win, t.Tie, t.Loss, t.winRate()*100, t.tieRate()*100, t.Unparsed, t.JudgeErrors)
		}
		tot := r.total()
		fmt.Fprintf(&b, "| **合计** | %d | %d | %d | %d | %.1f%% | %.1f%% | %d | %d |\n",
			tot.Selected, tot.Win, tot.Tie, tot.Loss, tot.winRate()*100, tot.tieRate()*100, tot.Unparsed, tot.JudgeErrors)
		b.WriteString(fmt.Sprintf("\n去偏方法：回放回复左右位置由 (种子 %d, 样本ID) 的稳定哈希随机决定，同种子可完整复现；\n", r.Seed))
	}

	b.WriteString("\n## 口径与局限（如实披露）\n\n")
	for _, n := range notesFor(r) {
		fmt.Fprintf(&b, "- %s\n", n)
	}

	fmt.Fprintf(&b, "\n## 结论\n\n%s\n", r.conclusion())
	return b.String()
}

// notesFor 汇总固定口径披露 + 本次运行追加的 notes。
func notesFor(r *evalReport) []string {
	base := []string{
		"回放生成走 strategy.GenerateReply（架构红线唯一 LLM 出口）。已知副作用：该链路会递减会话引导轮数并落 usage_ledger 计量——属生成链路固有行为，建议低峰期跑或在测试租户验证后再上生产租户。",
		"历史回复是当年策略+当年画像下产生的，回放用「当前画像」推理（客户状态已随时间演化），两者并非严格同分布；本评测回答的是「现策略在此刻值不值得发」，不是严格的反事实推断。",
		"LLM 裁判存在噪声：小样本桶（尤其 converted）胜率仅供方向参考，不足以支撑自动晋升决策。",
	}
	if r.JudgeErrors > 0 && !r.JudgeExecuted {
		base = append(base, fmt.Sprintf("裁判模型全程不可用（%d 次调用失败，疑似 evals 阶段模型未配置），整批按 SKIP 处理，退出码 0。", r.JudgeErrors))
	}
	return append(base, r.Notes...)
}
