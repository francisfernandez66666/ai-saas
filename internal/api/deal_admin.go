// 商机与报价管理端（商机批 · 批次2，2026-09-23）：管道看板、格子下钻名单、建单/推进/编辑，
// 以及报价单的版本链与状态机动作（发出/接受/拒绝/作废/改草稿）。
//
// 只走 admin 闸（AdminRequired 挂在路由组上），读写一律 db.RQ(c) + 行内 tenant_id 双闸。
//
// 四条口径写在这里，因为它们都直接影响"运营看得见什么、能不能核对"：
//  1. **看板数字与名单同源**：两侧都走 deal.Board / deal.DrillDeals 共用的一次扫描（scanFacts），
//     所以"格子 12 张、点进去 9 行"在这批是结构上不给机会（D4 与活码批同一条教训）。
//  2. **单位纪律**：看板格子单位是"张单子"，因此下钻回的是商机名单而不是客户名单——
//     金额与赢单率同理（问的是单，不是人）。
//  3. **推进/编辑/报价动作都带条件守卫**：0 行受影响判 409 而不是成功
//     （deal.ErrConflict）。两个人同时点"标成交"时，后一个人必须知道自己没改成。
//  4. **金额一律分**（int64），接口不接受浮点元：报价是钱，钱在边界上不容许四舍五入。
//
// 数据范围（db.DataScope）在这里是**可选裁剪**：AdminRequired 已保证是租户管理员/超管，
// 默认看全租户；顾问侧的窄口径入口在 deal_advisor.go（客户台面），不复用本文件的处理器。
package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ai-scrm/internal/db"
	"ai-scrm/internal/deal"
	"ai-scrm/internal/model"
)

// dealQueryInt 读整型 query，非法/非正一律回默认（窗口与页码没有负数语义）。
func dealQueryInt(c *gin.Context, key string, def int) int {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// dealTimeLayouts 日期入参可接受的形态：前端日期选择器给 "2006-01-02"，
// 接口/Mock 与冒烟脚本会给带时分秒的两种写法，一律收。
var dealTimeLayouts = []string{"2006-01-02", "2006-01-02 15:04:05", time.RFC3339}

// dealParseDate 宽容解析日期入参，返回 (值, 形态是否非法)。
//
// 空串 / "null" / 未传都得到 (nil, false)——"**要不要写这一列**"由请求里的
// set_close_date / set_valid 显式声明（指针字段的零值与"未提供"在 Go 里同形，
// 靠猜的结果就是"清空日期"静默无效，报表继续按一个已经不存在的承诺算超期）。
func dealParseDate(raw string) (*time.Time, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || s == "null" {
		return nil, false
	}
	for _, lay := range dealTimeLayouts {
		if t, err := time.ParseInLocation(lay, s, time.Local); err == nil {
			return &t, false
		}
	}
	return nil, true // 形态不对：调用方据此报 400，不静默当成"没传"
}

// dealWriteErr 把领域层错误翻成 HTTP：
// 稳定原因码 → 400 + reason（前端与冒烟按 reason 分支，文案可改码不可改）；
// ErrNotFound → 404（不存在与跨租户同形，不回显差别）；
// ErrConflict → 409（条件守卫 0 行受影响：有人抢在你之前改了这条）。
func dealWriteErr(c *gin.Context, err error, safeMsg string) {
	switch {
	case err == nil:
		return
	case deal.RejectReason(err) != "":
		c.JSON(http.StatusBadRequest, gin.H{
			"code": 400, "message": err.Error(), "error_code": "deal_rejected", "reason": deal.RejectReason(err),
		})
	case errors.Is(err, deal.ErrConflict):
		RespErr(c, http.StatusConflict, 409, err.Error())
	case errors.Is(err, deal.ErrNotFound):
		RespErr(c, http.StatusNotFound, 404, "记录不存在")
	case errors.Is(err, deal.ErrBadFilter):
		RespErr(c, http.StatusBadRequest, 400, err.Error())
	default:
		RespErrInternal(c, err, safeMsg)
	}
}

// dealBoardConfig 随看板下发全套枚举与口径：前端渲染下拉/表头一律读它，
// 不在 TypeScript 里再写一份中文阶段名（两份枚举迟早对不上，且错位发生在页面上看不见的地方）。
func dealBoardConfig() gin.H {
	stages := append(append([]string{}, model.DealStages...), model.DealStageWon, model.DealStageLost)
	return gin.H{
		"stages":             stages,
		"stage_names":        model.DealStageNames,
		"filters":            deal.FilterCodes,
		"filter_labels":      deal.FilterLabelMap(),
		"lost_reasons":       model.DealLostReasonNames,
		"quote_statuses":     model.QuoteStatusNames,
		"sources":            []string{model.DealSourceManual, model.DealSourceAcquisition, model.DealSourceAI},
		"max_amount_cents":   deal.MaxAmountCents,
		"max_quote_lines":    deal.MaxQuoteLines,
		"default_stuck_days": deal.DefaultStuckDays,
	}
}

// DealBoard GET /admin/deals/board?days=&stuck_days=
// apidump:ts DealBoardResp
// 销售管道看板：六个阶段格子（各几张/各多少钱）+ 在途/停滞/赢单三个汇总位。
func DealBoard(c *gin.Context) {
	tid := db.EffectiveTenantIDFromGin(c)
	days := dealQueryInt(c, "days", deal.DefaultWindowDays)
	stuck := dealQueryInt(c, "stuck_days", deal.DefaultStuckDays)
	res, err := deal.Board(db.RQ(c), deal.Scope{TenantID: tid}, days, stuck)
	if err != nil {
		RespErrInternal(c, err, "商机看板读取失败")
		return
	}
	RespOK(c, "ok", gin.H{
		"days": res.Days, "stuck_days": res.StuckDays,
		"stages":             res.Stages,
		"open_count":         res.OpenCount,
		"open_amount_cents":  res.OpenAmountCents,
		"stuck_count":        res.StuckCount,
		"stuck_amount_cents": res.StuckAmountCents,
		"won_count":          res.WonCount,
		"won_amount_cents":   res.WonAmountCents,
		"lost_count":         res.LostCount,
		"total_count":        res.TotalCount,
		"win_rate_pct":       res.WinRatePct,
		"truncated":          res.Truncated,
		"note":               res.Note,
		"config":             dealBoardConfig(),
	})
}

// DrillDeals GET /admin/deals?filter=&days=&stuck_days=&page=&page_size=
// apidump:ts DealDrillResp
// 看板格子点开就是这几张单：total 恒等于格子数字（两侧同源），list 是当前页。
func DrillDeals(c *gin.Context) {
	tid := db.EffectiveTenantIDFromGin(c)
	filter := strings.TrimSpace(c.Query("filter"))
	if filter == "" {
		// 不默认回某一份名单：默认视图与"我点了哪一格"混在一起就没法核对，缺参数就是缺参数
		RespErr(c, http.StatusBadRequest, 400, "缺少参数 filter")
		return
	}
	days := dealQueryInt(c, "days", deal.DefaultWindowDays)
	stuck := dealQueryInt(c, "stuck_days", deal.DefaultStuckDays)
	page := dealQueryInt(c, "page", 1)
	size := deal.ClampPageSize(dealQueryInt(c, "page_size", 20))
	res, err := deal.DrillDeals(db.RQ(c), deal.Scope{TenantID: tid}, filter, days, stuck, page, size)
	if err != nil {
		dealWriteErr(c, err, "商机名单读取失败")
		return
	}
	RespOK(c, "ok", gin.H{
		"filter": res.Filter, "label": res.Label, "days": res.Days, "stuck_days": res.StuckDays,
		"total": res.Total, "list": res.List, "truncated": res.Truncated, "note": res.Note,
		// 回显钳制后的分页：前端据此算总页数，回原值会让 page_size=1000 的请求
		// 在页面上算出"共 1 页"而实际每页只有 100 行。
		"page": page, "page_size": size,
	})
}

// dealCreateReq 建单请求体。金额用 int64 分；日期用字符串（""=不承诺）。
type dealCreateReq struct {
	CustomerID      uint   `json:"customer_id"`
	Title           string `json:"title"`
	Stage           string `json:"stage"`
	AmountCents     int64  `json:"amount_cents"`
	Source          string `json:"source"`
	ExpectedCloseAt string `json:"expected_close_at"`
}

// CreateDeal POST /admin/deals {customer_id,title,stage,amount_cents,source,expected_close_at}
// apidump:ts DealDetailResp
// 人工建单。同一客户已有在途单时明确拒绝（deal_already_open）而不是复用：
// 点"新建"的人拿到一张别人正在谈的旧单，会在上面改金额。
func CreateDeal(c *gin.Context) {
	tid := db.EffectiveTenantIDFromGin(c)
	var req dealCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErrBind(c, err)
		return
	}
	exp, bad := dealParseDate(req.ExpectedCloseAt)
	if bad {
		RespErr(c, http.StatusBadRequest, 400, "expected_close_at 日期格式不合法")
		return
	}
	res, err := deal.CreateDeal(db.RQ(c), deal.CreateInput{
		TenantID: tid, CustomerID: req.CustomerID, Title: req.Title, Stage: req.Stage,
		AmountCents: req.AmountCents, Source: req.Source, ExpectedCloseAt: exp,
	})
	if err != nil {
		dealWriteErr(c, err, "商机创建失败")
		return
	}
	dealRespDetail(c, tid, res.Deal.ID)
}

// dealMoveReq 推进请求体。ChangeAmount 决定 amount_cents 是"改成这个值"还是"没传"。
type dealMoveReq struct {
	To           string `json:"to"`
	AmountCents  int64  `json:"amount_cents"`
	ChangeAmount bool   `json:"change_amount"`
	LostReason   string `json:"lost_reason"`
}

// MoveDeal POST /admin/deals/:id/move {to,amount_cents,change_amount,lost_reason}
// apidump:ts DealDetailResp
// 阶段推进（裁决全在 deal.DecideMove：只准前进、won 要金额、lost 要原因、终局锁死）。
func MoveDeal(c *gin.Context) {
	tid := db.EffectiveTenantIDFromGin(c)
	id, ok := PathUintID(c)
	if !ok {
		return
	}
	var req dealMoveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErrBind(c, err)
		return
	}
	back, err := deal.MoveDeal(db.RQ(c), deal.MoveInput{
		TenantID: tid, DealID: id, To: req.To, AmountCents: req.AmountCents,
		ChangeAmount: req.ChangeAmount, LostReason: req.LostReason,
	})
	if err != nil {
		dealWriteErr(c, err, "商机推进失败")
		return
	}
	dealRespDetail(c, tid, back.ID)
}

// dealEditReq 编辑请求体（不含阶段：改阶段只有 /move 这一条路，别留后门）
type dealEditReq struct {
	Title           string `json:"title"`
	AmountCents     int64  `json:"amount_cents"`
	ChangeAmount    bool   `json:"change_amount"`
	ExpectedCloseAt string `json:"expected_close_at"`
	SetCloseDate    bool   `json:"set_close_date"`
}

// EditDeal PUT /admin/deals/:id {title,amount_cents,change_amount,expected_close_at,set_close_date}
// apidump:ts DealDetailResp
// 改标题/金额/预期成交日。**终局单拒（deal_closed）**：已发生的成交金额是历史事实，
// 事后改动等于重写报表。
func EditDeal(c *gin.Context) {
	tid := db.EffectiveTenantIDFromGin(c)
	id, ok := PathUintID(c)
	if !ok {
		return
	}
	var req dealEditReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErrBind(c, err)
		return
	}
	exp, bad := dealParseDate(req.ExpectedCloseAt)
	if bad {
		RespErr(c, http.StatusBadRequest, 400, "expected_close_at 日期格式不合法")
		return
	}
	var ptr *time.Time
	if req.SetCloseDate {
		ptr = exp
	}
	back, err := deal.EditDeal(db.RQ(c), deal.EditInput{
		TenantID: tid, DealID: id, Title: req.Title, AmountCents: req.AmountCents,
		ChangeAmount: req.ChangeAmount, ExpectedCloseAt: ptr, SetCloseDate: req.SetCloseDate,
	})
	if err != nil {
		dealWriteErr(c, err, "商机编辑失败")
		return
	}
	dealRespDetail(c, tid, back.ID)
}

// GetDealDetail GET /admin/deals/:id
// apidump:ts DealDetailResp
// 单张商机详情 + 其报价版本序列（v1→v3 全在，改版不覆盖旧版）。
func GetDealDetail(c *gin.Context) {
	tid := db.EffectiveTenantIDFromGin(c)
	id, ok := PathUintID(c)
	if !ok {
		return
	}
	item, quotes, err := deal.GetDeal(db.RQ(c), deal.Scope{TenantID: tid}, id)
	if err != nil {
		dealWriteErr(c, err, "商机详情读取失败")
		return
	}
	if item == nil {
		item = &deal.DealListItem{}
	}
	RespOK(c, "ok", gin.H{"deal": item, "quotes": quotes, "config": dealBoardConfig()})
}

// dealRespDetail 写完立刻回读详情：让前端一次请求就拿到"落库后的真实样子"
// （谁并发改过、金额被谁改、阶段变没变，都不用再补一次 GET，也不给"报了成功其实是旧值"的机会）。
func dealRespDetail(c *gin.Context, tid, id uint) {
	item, quotes, err := deal.GetDeal(db.RQ(c), deal.Scope{TenantID: tid}, id)
	if err != nil {
		dealWriteErr(c, err, "商机详情读取失败")
		return
	}
	if item == nil {
		item = &deal.DealListItem{}
	}
	RespOK(c, "ok", gin.H{"deal": item, "quotes": quotes, "config": dealBoardConfig()})
}

// ============================================================
// 报价单
// ============================================================

// quoteLineReq 明细行入参（只有名称/数量/单价三项，小计与合计由服务端算）
type quoteLineReq struct {
	Name      string `json:"name"`
	Qty       int    `json:"qty"`
	UnitCents int64  `json:"unit_cents"`
}

// dealQuoteCreateReq 建版请求体。Send 在建完后走同一个状态机发出（不是绕过 draft 直接 sent）。
// SetValid 声明"这次要改有效期"——不声明时"清空"与"没传"在 JSON 里同形（见 deal.EditQuoteInput 注释）。
type dealQuoteCreateReq struct {
	Lines      []quoteLineReq `json:"lines"`
	Note       string         `json:"note"`
	Send       bool           `json:"send"`
	ValidUntil string         `json:"valid_until"`
	SetValid   bool           `json:"set_valid"`
}

// CreateQuote POST /admin/deals/:id/quotes {lines,note,valid_until,send}
// apidump:ts DealQuoteResp
// 出一版新报价：自动把该商机原来的在途报价标为"被新版取代"，version 取历史最大 +1。
func CreateQuote(c *gin.Context) {
	tid := db.EffectiveTenantIDFromGin(c)
	id, ok := PathUintID(c)
	if !ok {
		return
	}
	var req dealQuoteCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErrBind(c, err)
		return
	}
	valid, bad := dealParseDate(req.ValidUntil)
	if bad {
		RespErr(c, http.StatusBadRequest, 400, "valid_until 日期格式不合法")
		return
	}
	lines := make([]deal.QuoteLineInput, 0, len(req.Lines))
	for _, l := range req.Lines {
		lines = append(lines, deal.QuoteLineInput{Name: l.Name, Qty: l.Qty, UnitCents: l.UnitCents})
	}
	q, err := deal.CreateQuote(db.RQ(c), deal.CreateQuoteInput{
		TenantID: tid, DealID: id, CreatedBy: currentUserID(c),
		Lines: lines, Note: req.Note, ValidUntil: valid, SendNow: req.Send,
	})
	if err != nil {
		dealWriteErr(c, err, "报价单创建失败")
		return
	}
	RespOK(c, "ok", gin.H{"quote": q})
}

// ListDealQuotes GET /admin/deals/:id/quotes
// apidump:ts DealQuoteListResp
// 这张商机的报价版本序列（version 倒序，含历史与作废）。
func ListDealQuotes(c *gin.Context) {
	tid := db.EffectiveTenantIDFromGin(c)
	id, ok := PathUintID(c)
	if !ok {
		return
	}
	// 先过一遍详情闸：跨租户/不存在的商机在这里同样 404，不回显差别
	if _, _, err := deal.GetDeal(db.RQ(c), deal.Scope{TenantID: tid}, id); err != nil {
		dealWriteErr(c, err, "报价单列表读取失败")
		return
	}
	list, err := deal.ListQuotes(db.RQ(c), tid, id)
	if err != nil {
		RespErrInternal(c, err, "报价单列表读取失败")
		return
	}
	RespOK(c, "ok", gin.H{"list": list, "total": len(list)})
}

// GetQuoteDetail GET /admin/quotes/:id
// apidump:ts DealQuoteResp
// 报价单详情（含明细行）。客户手里那一版必须随时可复现，故历史版本同样可读、不可改。
func GetQuoteDetail(c *gin.Context) {
	tid := db.EffectiveTenantIDFromGin(c)
	id, ok := PathUintID(c)
	if !ok {
		return
	}
	q, err := deal.GetQuote(db.RQ(c), tid, id)
	if err != nil {
		dealWriteErr(c, err, "报价单读取失败")
		return
	}
	RespOK(c, "ok", gin.H{"quote": q})
}

// EditQuoteDraft PUT /admin/quotes/:id {lines,note,valid_until,set_valid}
// apidump:ts DealQuoteResp
// 改草稿（已发出拒 quote_locked、终局态拒 quote_final）。合计一律服务端按明细重算。
func EditQuoteDraft(c *gin.Context) {
	tid := db.EffectiveTenantIDFromGin(c)
	id, ok := PathUintID(c)
	if !ok {
		return
	}
	var req dealQuoteCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErrBind(c, err)
		return
	}
	valid, bad := dealParseDate(req.ValidUntil)
	if bad {
		RespErr(c, http.StatusBadRequest, 400, "valid_until 日期格式不合法")
		return
	}
	lines := make([]deal.QuoteLineInput, 0, len(req.Lines))
	for _, l := range req.Lines {
		lines = append(lines, deal.QuoteLineInput{Name: l.Name, Qty: l.Qty, UnitCents: l.UnitCents})
	}
	q, err := deal.EditQuote(db.RQ(c), deal.EditQuoteInput{
		TenantID: tid, QuoteID: id, Lines: lines, Note: req.Note,
		ValidUntil: valid, SetValid: req.SetValid,
	})
	if err != nil {
		dealWriteErr(c, err, "报价单编辑失败")
		return
	}
	RespOK(c, "ok", gin.H{"quote": q})
}

// SendQuoteDraft POST /admin/quotes/:id/send
// apidump:ts DealQuoteResp
// 草稿 → 已发出（此后内容锁定）。条件守卫带 status='draft'，两人同时点发出时后一个 409。
func SendQuoteDraft(c *gin.Context) {
	dealQuoteAct(c, func(tid, id uint) (*deal.QuoteDetail, error) {
		return deal.SendQuote(db.RQ(c), deal.ActInput{TenantID: tid, QuoteID: id})
	})
}

// AcceptQuote POST /admin/quotes/:id/accept
// apidump:ts DealQuoteResp
// 客户接受回写。**不会顺手把商机推成成交**：成交要金额校验、是人的判断，
// 不该由一次"客户说可以"隐式改掉整张管道的位置。
func AcceptQuote(c *gin.Context) {
	dealQuoteAct(c, func(tid, id uint) (*deal.QuoteDetail, error) {
		return deal.DecideQuote(db.RQ(c), deal.ActInput{TenantID: tid, QuoteID: id}, true)
	})
}

// DeclineQuote POST /admin/quotes/:id/decline
// apidump:ts DealQuoteResp
// 客户拒绝回写（历史行保留，供"我们报过、他拒了"这件事日后仍可查）。
func DeclineQuote(c *gin.Context) {
	dealQuoteAct(c, func(tid, id uint) (*deal.QuoteDetail, error) {
		return deal.DecideQuote(db.RQ(c), deal.ActInput{TenantID: tid, QuoteID: id}, false)
	})
}

// VoidQuote POST /admin/quotes/:id/void
// apidump:ts DealQuoteResp
// 人工作废（草稿/已发出皆可，终局态拒）。**没有删除**：作废是"这张不算了但不消失"。
func VoidQuote(c *gin.Context) {
	dealQuoteAct(c, func(tid, id uint) (*deal.QuoteDetail, error) {
		return deal.VoidQuote(db.RQ(c), deal.ActInput{TenantID: tid, QuoteID: id})
	})
}

// dealQuoteAct 报价动作的统一外壳：解析 ID → 跑动作 → 回详情。
// 四个动作的 HTTP 形态完全一致，写四份就是把同一个错误映射抄四遍（漏抄一处就是行为不一致）。
func dealQuoteAct(c *gin.Context, act func(tid, id uint) (*deal.QuoteDetail, error)) {
	tid := db.EffectiveTenantIDFromGin(c)
	id, ok := PathUintID(c)
	if !ok {
		return
	}
	q, err := act(tid, id)
	if err != nil {
		dealWriteErr(c, err, "报价单操作失败")
		return
	}
	RespOK(c, "ok", gin.H{"quote": q})
}
