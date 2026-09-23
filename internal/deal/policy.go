// Package deal 商机与报价领域层：建单与改派阶段的裁决、报价单的版本与状态机、
// 管道看板（各阶段单子数/金额/停滞数）与"点一格看那几张单"的名单判定。
//
// 分包位置（D2b 分包红线）：领域代码不进 internal/service，调用方是 api 层与（可选的）AI 自动建单钩子。
// 依赖方向：deal → model（只用枚举与表结构）。**本包不 import llm/chatflow/channel/billing**：
// 它回答"这张单子走到哪一步、值多少钱"，不碰"AI 怎么回"，也不碰"钱怎么扣"——
// 成交金额是销售口径的**预计值**，与 billing 里真实到账的订单金额是两回事，
// 让 deal 去读订单表就会出现"管道说成交 300 万、账上到账 3 万"的互相打脸。
//
// 所有拒绝都带**稳定原因码**（ErrReject.Reason），前端与冒烟按码分支，不解析中文文案；
// 判定逻辑一律做成纯函数（DecideMove / BuildQuoteLines / DecideQuoteAction / MatchFilter），
// 单测不需要连库就能把每个分支跑一遍。
package deal

import (
	"math"
	"strings"
	"time"

	"ai-scrm/internal/model"
)

// ============================================================
// 阶段机器（纯函数）
// ============================================================

// IsTerminal 该阶段是否为终局（won/lost）。终局行是历史事实：不再改阶段、不再改金额。
// 为什么锁得这么死：管道报表算的是"这个月赢了多少"，
// 一张三个月前成交的单今天被改回"谈判中"，上个月的报表就会跟着变——
// 已经拿去开过的会、发过的数字，不能被一条 UPDATE 悄悄改掉。
func IsTerminal(stage string) bool {
	return stage == model.DealStageWon || stage == model.DealStageLost
}

// StageRank 在途阶段的深浅（0 起）；非在途阶段（终局或未知）返回 -1。
// 推进只允许往大数走，这条判据是"阶段"这个词唯一的意义。
func StageRank(stage string) int {
	for i, s := range model.DealStages {
		if s == stage {
			return i
		}
	}
	return -1
}

// IsValidStage 是否为合法阶段码（含终局两态）。建单入参用它校验。
func IsValidStage(stage string) bool {
	if _, ok := model.DealStageNames[stage]; !ok {
		return false
	}
	return stage == model.DealStageWon || stage == model.DealStageLost || StageRank(stage) >= 0
}

// 拒绝原因码（稳定字面量：响应、日志、冒烟三处按它分支，两侧都不要自造）
const (
	ReasonStageUnknown       = "stage_unknown"         // 阶段码不认识
	ReasonSameStage          = "same_stage"            // 原地移动（会白刷"停滞天数"，故拒）
	ReasonDealClosed         = "deal_closed"           // 已终局，不再改
	ReasonStageBackward      = "stage_backward"        // 逆向推进
	ReasonWonAmountRequired  = "won_amount_required"   // 成交必须有金额
	ReasonLostReasonRequired = "lost_reason_required"  // 流失必须给原因
	ReasonLostReasonUnknown  = "lost_reason_unknown"   // 流失原因不在白名单
	ReasonAmountNegative     = "amount_negative"       // 金额为负
	ReasonAmountTooLarge     = "amount_too_large"      // 金额超出上限
	ReasonTitleRequired      = "title_required"        // 标题为空
	ReasonTitleTooLong       = "title_too_long"        // 标题过长
	ReasonCustomerNotFound   = "customer_not_found"    // 客户不存在或不在本租户
	ReasonDealAlreadyOpen    = "deal_already_open"     // 该客户已有在途商机
	ReasonDealNotFound       = "deal_not_found"        // 商机不存在（对外一律 404）
	ReasonQuoteLocked        = "quote_locked"          // 已发出的报价不可编辑
	ReasonQuoteNotDraft      = "quote_not_draft"       // 只有草稿能发出
	ReasonQuoteNotSent       = "quote_not_sent"        // 只有已发出的能接受/拒绝
	ReasonQuoteFinal         = "quote_final"           // 报价单已是终局态
	ReasonQuoteNotFound      = "quote_not_found"       // 报价单不存在
	ReasonQuoteLinesRequired = "quote_lines_required"  // 明细为空
	ReasonQuoteTooManyLines  = "quote_too_many_lines"  // 明细行数超限
	ReasonQuoteLineName      = "quote_line_name"       // 明细项目名为空/过长
	ReasonQuoteLineQty       = "quote_line_qty"        // 明细数量非法
	ReasonQuoteLineUnit      = "quote_line_unit"       // 明细单价非法
	ReasonQuoteTotalTooLarge = "quote_total_too_large" // 合计超出上限
	ReasonTenantRequired     = "tenant_required"       // 无租户语境
	ReasonSourceUnknown      = "source_unknown"        // 来源码不认识
)

// MaxAmountCents 单张商机/报价的金额上限（分）。
// 1e12 分 = 100 亿元：远超任何单笔业务真实值，但它不是"随便写的数"——
// 没有上限，一次前端把"万"当"元"提交就会写出一个把整张管道的合计顶爆的值，
// 而平均值、加权预测这些指标都会跟着变成垃圾，且没人会去查"为什么平均一单 30 亿"。
const MaxAmountCents int64 = 1_000_000_000_00

// MaxTitleRunes 标题长度上限（按字素计，不是字节：中文一字符 3 字节）
const MaxTitleRunes = 60

// ValidateTitle 标题校验（""=通过）
func ValidateTitle(title string) string {
	t := strings.TrimSpace(title)
	if t == "" {
		return ReasonTitleRequired
	}
	if len([]rune(t)) > MaxTitleRunes {
		return ReasonTitleTooLong
	}
	return ""
}

// ValidateAmount 金额校验（分，""=通过）
func ValidateAmount(cents int64) string {
	if cents < 0 {
		return ReasonAmountNegative
	}
	if cents > MaxAmountCents {
		return ReasonAmountTooLarge
	}
	return ""
}

// NormalizeSource 来源码归一：非法值一律回 manual。
// 为什么不在这里报错：来源是**旁路信息**（人工建单/活码带来/AI 识别），
// 为它拒掉一张真实的单子是本末倒置；宁可标成 manual 也不让单子进不来。
func NormalizeSource(raw string) string {
	switch strings.TrimSpace(raw) {
	case model.DealSourceManual, model.DealSourceAcquisition, model.DealSourceAI:
		return strings.TrimSpace(raw)
	default:
		return model.DealSourceManual
	}
}

// IsValidSource 严格校验（接口入参用：显式传了错的就要报错，静默归一会让前端 bug 永久隐身）
func IsValidSource(raw string) bool {
	s := strings.TrimSpace(raw)
	return s == model.DealSourceManual || s == model.DealSourceAcquisition || s == model.DealSourceAI
}

// 报价单动作码（DecideQuoteAction 的第二个参数）
const (
	ActionEdit      = "edit"
	ActionSend      = "send"
	ActionAccept    = "accept"
	ActionDecline   = "decline"
	ActionVoid      = "void"
	ActionSupersede = "supersede"
)

// StageMoveInput 阶段推进裁决输入（纯函数入参，不带 ID——落库侧的 MoveInput 才有租户与单子 ID）
type StageMoveInput struct {
	From        string
	To          string
	AmountCents int64 // 单子当前金额（won 判定要用；本次一并改金额时由调用方传新值）
	LostReason  string
	Now         time.Time // 注入点：单测要把 won_at/lost_at 摆在必定时刻
}

// MoveResult 阶段推进裁决结果。
// 字段是"最终要写进库的样子"而不是"要改哪些列"：
// 流失原因在非 lost 阶段恒为空、成交时间只在 won 有值——这些归零规则集中在这里做，
// 调用方各写一遍迟早出现 lost 单上还挂着 lost_reason=” 而 won 单上有时间这种脏组合。
type MoveResult struct {
	Allow      bool
	Reason     string
	Stage      string
	LostReason string
	StageAt    time.Time
	WonAt      *time.Time
	LostAt     *time.Time
	// Terminal 推进后是否进入终局（调用方据此决定后续是否还允许改金额）
	Terminal bool
}

// DecideMove 阶段推进裁决（纯函数，不碰库）。
//
// 三条判据各有业务理由：
//  1. **只准往深走**：倒退意味着前面那几步是白填的。真发生了（客户需求变了），
//     正确做法是把这张单作废、重开一张——历史留在原处，新单重新计天数。
//     允许原地倒退的话，"停留天数"这个指标就没有意义了（谁都可以回到起点刷新表）。
//  2. **成交要有钱**：一张金额为 0 的"成交"进不了任何金额口径的报表，
//     却会把成交单数抬高——数字好看、钱没有，是最典型的自欺。
//  3. **流失要有原因**：没有原因的流失等于"不知道丢了为什么"，
//     而赢单率、流失原因分布恰恰是销售管理者唯一能干预的东西。
func DecideMove(in StageMoveInput) MoveResult {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	from, to := strings.TrimSpace(in.From), strings.TrimSpace(in.To)
	if !IsValidStage(from) || !IsValidStage(to) {
		return MoveResult{Reason: ReasonStageUnknown}
	}
	if IsTerminal(from) {
		return MoveResult{Reason: ReasonDealClosed}
	}
	if from == to {
		return MoveResult{Reason: ReasonSameStage}
	}
	if !IsTerminal(to) && StageRank(to) <= StageRank(from) {
		return MoveResult{Reason: ReasonStageBackward}
	}
	res := MoveResult{Allow: true, Stage: to, StageAt: now, Terminal: IsTerminal(to)}
	switch to {
	case model.DealStageWon:
		if in.AmountCents <= 0 {
			return MoveResult{Reason: ReasonWonAmountRequired}
		}
		stamp := now
		res.WonAt = &stamp
	case model.DealStageLost:
		r := strings.TrimSpace(in.LostReason)
		if r == "" {
			return MoveResult{Reason: ReasonLostReasonRequired}
		}
		if _, ok := model.DealLostReasonNames[r]; !ok {
			return MoveResult{Reason: ReasonLostReasonUnknown}
		}
		stamp := now
		res.LostAt = &stamp
		res.LostReason = r
	}
	return res
}

// IsLostReason 流失原因码是否在白名单（接口层校验入参用）
func IsLostReason(r string) bool {
	_, ok := model.DealLostReasonNames[strings.TrimSpace(r)]
	return ok
}

// ============================================================
// 报价单：明细与状态机（纯函数）
// ============================================================

// MaxQuoteLines 一张报价单最多几行明细。
// 上限不是为了"防攻击"，是因为超过这个行数的东西已经不是报价单而是合同附件，
// 而它要在列表页里被展开——第 40 行没人会看，只会让每次刷新都变慢。
const MaxQuoteLines = 40

// MaxQtyPerLine 单行数量上限（够任何真实订单，且挡住"手滑多打四个 0"）
const MaxQtyPerLine = 100_000

// QuoteLineInput 明细入参（前端提交形态）
type QuoteLineInput struct {
	Name      string
	Qty       int
	UnitCents int64
}

// BuildQuoteLines 校验明细并**在服务端重算**小计与合计。
//
// 为什么前端传了合计还要后端算：改一行忘改合计是常态，
// 而报价单是要发给客户看的钱数——错一位就是商务事故，宁可每次多算一遍。
// 返回的 Lines 里 TotalCents 已回填，落库与读侧都以它为准。
func BuildQuoteLines(raw []QuoteLineInput) ([]model.QuoteLine, int64, string) {
	if len(raw) == 0 {
		return nil, 0, ReasonQuoteLinesRequired
	}
	if len(raw) > MaxQuoteLines {
		return nil, 0, ReasonQuoteTooManyLines
	}
	lines := make([]model.QuoteLine, 0, len(raw))
	var total int64
	for _, r := range raw {
		name := strings.TrimSpace(r.Name)
		if name == "" || len([]rune(name)) > MaxTitleRunes {
			return nil, 0, ReasonQuoteLineName
		}
		if r.Qty <= 0 || r.Qty > MaxQtyPerLine {
			return nil, 0, ReasonQuoteLineQty
		}
		if r.UnitCents < 0 || r.UnitCents > MaxAmountCents {
			return nil, 0, ReasonQuoteLineUnit
		}
		lineTotal := int64(r.Qty) * r.UnitCents
		// 溢出判据：乘法一旦溢出变成负数，后面的合计就会静默变小而不报错。
		// 单价为 0 时除法无意义，跳过（此时行小计恒为 0）。
		if r.UnitCents > 0 && lineTotal/r.UnitCents != int64(r.Qty) {
			return nil, 0, ReasonQuoteTotalTooLarge
		}
		if lineTotal > MaxAmountCents {
			return nil, 0, ReasonQuoteTotalTooLarge
		}
		if total > math.MaxInt64-lineTotal {
			return nil, 0, ReasonQuoteTotalTooLarge
		}
		total += lineTotal
		lines = append(lines, model.QuoteLine{Name: name, Qty: r.Qty, UnitCents: r.UnitCents, TotalCents: lineTotal})
	}
	if total > MaxAmountCents {
		return nil, 0, ReasonQuoteTotalTooLarge
	}
	return lines, total, ""
}

// DecideQuoteAction 报价单状态机裁决（""=允许）。
//
// 状态机的形状是业务事实，不是设计口味：
//   - 草稿可改可发；**发出去就锁**（客户手里那张必须能被复现，改完再发是另一版）；
//   - 只有已发出的能被接受/拒绝（客户没见过的单子谈不上"接受"）；
//   - 作废/被新版取代/过期都通向终局，终局不再动任何内容。
func DecideQuoteAction(status, action string) string {
	switch action {
	case ActionEdit:
		if status == model.QuoteStatusDraft {
			return ""
		}
		if IsQuoteFinal(status) {
			return ReasonQuoteFinal
		}
		return ReasonQuoteLocked
	case ActionSend:
		if status == model.QuoteStatusDraft {
			return ""
		}
		if IsQuoteFinal(status) {
			return ReasonQuoteFinal
		}
		return ReasonQuoteNotDraft
	case ActionAccept, ActionDecline:
		if status == model.QuoteStatusSent {
			return ""
		}
		if IsQuoteFinal(status) {
			return ReasonQuoteFinal
		}
		return ReasonQuoteNotSent
	case ActionVoid:
		if status == model.QuoteStatusDraft || status == model.QuoteStatusSent {
			return ""
		}
		return ReasonQuoteFinal
	case ActionSupersede:
		// 内部动作：出新版本时把上一张在途单标记为"被取代"，draft/sent 皆可
		if status == model.QuoteStatusDraft || status == model.QuoteStatusSent {
			return ""
		}
		return ReasonQuoteFinal
	}
	return ReasonQuoteFinal
}

// IsQuoteFinal 报价单是否已终局（不再接受任何动作）
func IsQuoteFinal(status string) bool {
	switch status {
	case model.QuoteStatusAccepted, model.QuoteStatusDeclined, model.QuoteStatusSuperseded, model.QuoteStatusVoid, model.QuoteStatusExpired:
		return true
	}
	return false
}

// ============================================================
// 管道看板：过滤码（看板计数与名单下钻**共用**的唯一谓词）
// ============================================================

// 看板格子过滤码。**单位一律"张单子"**，所以下钻出来的是商机名单而不是客户名单——
// 一个客户可以有两张历史单（一张流失一张在途），点"成交 12"如果回的是 12 个客户，
// 就会在某一格里出现 11 个人：这是 D4 立下的单位纪律在本批的落点。
const (
	FilterOpen  = "open"  // 在途（四个在途阶段之和）
	FilterStuck = "stuck" // 停滞超阈（在途且久未推进）
	FilterWon   = "won"   // 已成交
	FilterLost  = "lost"  // 已流失
	// FilterStagePrefix 按具体阶段看：stage:quoted / stage:negotiating …
	FilterStagePrefix = "stage:"
)

// DefaultStuckDays "停滞"的默认阈值（天）。
// 为什么是 7：销售管道的常识是"一周没动就是凉了"，
// 而它必须是可配的硬阈值而不是"感觉"——不然停滞榜每次都能被争论掉。
const DefaultStuckDays = 7

// MinStuckDays / MaxStuckDays 阈值钳位区间（1 天=噪音，365 天=没有单子会卡这么久）
const (
	MinStuckDays = 1
	MaxStuckDays = 90
)

// ClampStuckDays 把外部阈值钳进合法区间：**只有 0（没传）才回默认值**。
// 负数走"钳到下限"而不是"回默认"——否则 MinStuckDays 永不可达（<=0 先被默认吃掉），
// 一个永远走不到的下限分支比没有下限更骗人。
func ClampStuckDays(days int) int {
	if days == 0 {
		return DefaultStuckDays
	}
	if days < MinStuckDays {
		return MinStuckDays
	}
	if days > MaxStuckDays {
		return MaxStuckDays
	}
	return days
}

// FilterCodes 看板可点击的过滤码（顺序即展示顺序）
var FilterCodes = []string{FilterOpen, FilterStuck, FilterWon, FilterLost}

// StageFilterCodes 按阶段下钻的过滤码（含终局，六个阶段各自一格）
func StageFilterCodes() []string {
	out := make([]string, 0, len(model.DealStages)+2)
	for _, s := range model.DealStages {
		out = append(out, FilterStagePrefix+s)
	}
	out = append(out, FilterStagePrefix+model.DealStageWon, FilterStagePrefix+model.DealStageLost)
	return out
}

// IsValidFilter 过滤码是否合法（open/stuck/won/lost/stage:<阶段>）
func IsValidFilter(f string) bool {
	f = strings.TrimSpace(f)
	for _, ok := range FilterCodes {
		if f == ok {
			return true
		}
	}
	if s, ok := strings.CutPrefix(f, FilterStagePrefix); ok {
		return IsValidStage(s)
	}
	return false
}

// AllFilterCodes 全部可下钻的过滤码（四个分组 + 六个阶段格子），顺序即展示顺序。
// 看板一次下发这一份，前端就不需要自己拼 "stage:"+s 的字符串——拼错一个字母就是一格点不开的静默缺陷。
func AllFilterCodes() []string {
	out := make([]string, 0, len(FilterCodes)+len(model.DealStages)+2)
	out = append(out, FilterCodes...)
	return append(out, StageFilterCodes()...)
}

// FilterLabelMap 过滤码 → 中文名（随看板 config 下发；前端渲染标题时不再写第二套文案）
func FilterLabelMap() map[string]string {
	out := map[string]string{}
	for _, f := range AllFilterCodes() {
		out[f] = FilterLabel(f)
	}
	return out
}

// FilterLabel 过滤码 → 中文口径名（随响应下发，前端不复写第二套文案）
func FilterLabel(f string) string {
	f = strings.TrimSpace(f)
	switch f {
	case FilterOpen:
		return "在途商机"
	case FilterStuck:
		return "停滞商机"
	case FilterWon:
		return "已成交"
	case FilterLost:
		return "已流失"
	}
	if s, ok := strings.CutPrefix(f, FilterStagePrefix); ok {
		if n, has := model.DealStageNames[s]; has {
			return n
		}
	}
	return f
}

// MatchFilter 唯一谓词：这张单子是否属于这一格。
//
// **看板计数与下钻名单必须调同一个函数**——D4 的教训原样适用：
// 两侧各写一遍 if，改一侧忘另一侧，就会出现"卡片 12、点进去 9 行"，
// 而销售管理者第一次发现不一致时，整块看板就再也无人采信。
func MatchFilter(filter, stage string, stageEnteredAt, now time.Time, stuckDays int) bool {
	switch strings.TrimSpace(filter) {
	case FilterOpen:
		return !IsTerminal(stage)
	case FilterWon:
		return stage == model.DealStageWon
	case FilterLost:
		return stage == model.DealStageLost
	case FilterStuck:
		if IsTerminal(stage) {
			return false
		}
		return StalledDays(stageEnteredAt, now) >= float64(ClampStuckDays(stuckDays))
	}
	if s, ok := strings.CutPrefix(filter, FilterStagePrefix); ok {
		return stage == s
	}
	return false
}

// StalledDays 在当前阶段已停留的天数（不足一天按小数计，负值归零）。
// stageEnteredAt 为零值时返回 0：宁可不进停滞榜，也不把"字段没写"当成"卡了 2 万天"。
func StalledDays(stageEnteredAt, now time.Time) float64 {
	if stageEnteredAt.IsZero() {
		return 0
	}
	d := now.Sub(stageEnteredAt).Hours() / 24
	if d < 0 {
		return 0
	}
	return d
}
