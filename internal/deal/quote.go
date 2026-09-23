// 报价单的读写层：版本链（改版不覆盖）、状态机落库（草稿→发出→接受/拒绝/作废/过期）、
// 明细的服务端重算与 JSON 落列，以及过期 sweep。
//
// 为什么"改版"是新写一行而不是改原来那行：客户手里那张必须能被复现。
// 覆盖旧版等于把证据抹了——三周后客户说"你们报的是 28 万"，
// 系统里只剩一版被改过的 26 万，这单无论怎么谈都说不清（见 model/deal.go 文件头）。
//
// 为什么过期由 sweep 落库、读侧不改写：读接口做写操作会让并发读取互相踩，
// 还会让"上周看到的数字"随谁先看而漂移。过期是状态变化，状态变化要有时刻，
// 那个时刻由 sweep 写下（QuoteStatusExpired 的语义是"系统判定过期"，不是"有人来看过"）。
package deal

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"ai-scrm/internal/model"

	"gorm.io/gorm"
)

// QuoteDetail 报价单详情（含明细行；列表只给 QuoteSummary）
type QuoteDetail struct {
	QuoteSummary
	DealID    uint              `json:"opportunity_id"`
	Lines     []model.QuoteLine `json:"lines"`
	Note      string            `json:"note"`
	CreatedBy uint              `json:"created_by"`
	UpdatedAt string            `json:"updated_at"`
}

// CreateQuoteInput 新建一版报价
type CreateQuoteInput struct {
	TenantID   uint
	DealID     uint
	CreatedBy  uint
	Lines      []QuoteLineInput
	Note       string
	ValidUntil *time.Time
	// SendNow 建完即发出（"写好直接发客户"是主路径，两步点击是折磨）；
	// 仍走同一个状态机，不绕过 draft 直接当已发出——那样 sent_at 与状态就不是同一次判定的产物了。
	SendNow bool
	// Now 注入点：单测要把 sent_at/version 摆在指定时刻
	Now time.Time
}

// CreateQuote 为一张商机出新版本报价，并把该商机原来的在途报价标为"被新版取代"。
func CreateQuote(gdb *gorm.DB, in CreateQuoteInput) (*QuoteDetail, error) {
	if in.TenantID == 0 {
		return nil, &ErrReject{Reason: ReasonTenantRequired, Msg: "无租户语境"}
	}
	g := isolate(gdb)
	var d model.Opportunity
	if err := g.Where("tenant_id = ? AND id = ?", in.TenantID, in.DealID).First(&d).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if IsTerminal(d.Stage) {
		// 终局单不再报价：成交后重新报价属于下一张单子，流失后翻案同理。
		// 放开的话，管道里会出现"已流失的单子挂着一张今天刚发出的报价"。
		return nil, &ErrReject{Reason: ReasonDealClosed, Msg: "商机已终局，不再出报价"}
	}
	lines, total, r := BuildQuoteLines(in.Lines)
	if r != "" {
		return nil, &ErrReject{Reason: r, Msg: "报价明细不合法：" + r}
	}
	payload, err := json.Marshal(lines)
	if err != nil {
		return nil, err
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	// 版本号取本单历史最大 +1：**不按"活着的报价数"算**，
	// 否则把 v1 作废之后，下一张又回到 v1，两个 v1 并存，"客户手上那版"就彻底说不清了。
	var maxVer *int
	if err := isolate(gdb).Model(&model.Quote{}).
		Where("tenant_id = ? AND opportunity_id = ?", in.TenantID, in.DealID).
		Select("max(version)").Scan(&maxVer).Error; err != nil {
		return nil, err
	}
	ver := 1
	if maxVer != nil && *maxVer > 0 {
		ver = *maxVer + 1
	}
	// 旧在途单先标"被取代"：条件里钉死 status，避免把已接受/已拒绝的历史也顺手改掉
	res := isolate(gdb).Model(&model.Quote{}).
		Where("tenant_id = ? AND opportunity_id = ? AND status IN ?", in.TenantID, in.DealID,
			[]string{model.QuoteStatusDraft, model.QuoteStatusSent}).
		Updates(map[string]any{"status": model.QuoteStatusSuperseded, "updated_at": now})
	if res.Error != nil {
		return nil, res.Error
	}
	row := model.Quote{
		TenantID:      in.TenantID,
		OpportunityID: in.DealID,
		Version:       ver,
		Lines:         string(payload),
		TotalCents:    total,
		Status:        model.QuoteStatusDraft,
		ValidUntil:    in.ValidUntil,
		CreatedBy:     in.CreatedBy,
		Note:          trimRunes(strings.TrimSpace(in.Note), 200),
	}
	if err := isolate(gdb).Create(&row).Error; err != nil {
		// 并发双开两版：部分唯一索引 ux_quote_one_open_per_deal 拦下后插的那张。
		// 这里不静默回读旧版——两版内容多半不同，"你以为存了新的其实没有"比报错更糟。
		if strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key") {
			return nil, errors.New("deal: 该商机已有未发出的报价，请先处理后再建新版本")
		}
		return nil, err
	}
	if in.SendNow {
		sent, err := SendQuote(gdb, ActInput{TenantID: in.TenantID, QuoteID: row.ID, Now: now})
		if err != nil {
			return nil, err
		}
		return sent, nil
	}
	return quoteDetail(&row), nil
}

// ActInput 对既有报价单执行动作（发/接受/拒绝/作废）
type ActInput struct {
	TenantID uint
	QuoteID  uint
	Now      time.Time
}

// SendQuote 草稿 → 已发出（此后内容锁定）。
//
// 条件守卫带上 status='draft'：两人同时点"发出"时后一个 0 行受影响并报 conflict——
// 这里绝不能报成功。发出去的东西是要以"我们确实发过"为前提去主张的。
func SendQuote(gdb *gorm.DB, in ActInput) (*QuoteDetail, error) {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	q, err := loadQuote(gdb, in.TenantID, in.QuoteID)
	if err != nil {
		return nil, err
	}
	if r := DecideQuoteAction(q.Status, ActionSend); r != "" {
		return nil, &ErrReject{Reason: r, Msg: "这张报价单当前状态不能发出：" + r}
	}
	upd := isolate(gdb).Model(&model.Quote{}).
		Where("tenant_id = ? AND id = ? AND status = ?", in.TenantID, in.QuoteID, model.QuoteStatusDraft).
		Updates(map[string]any{"status": model.QuoteStatusSent, "sent_at": now, "updated_at": now})
	if upd.Error != nil {
		return nil, upd.Error
	}
	if upd.RowsAffected == 0 {
		return nil, fmt.Errorf("deal: 报价单%w", ErrConflict)
	}
	back, err := loadQuote(gdb, in.TenantID, in.QuoteID)
	if err != nil {
		return nil, err
	}
	return quoteDetail(back), nil
}

// DecideQuote 客户答复回写（accept=true 接受，false 拒绝）。
//
// **不接受完自动把商机标成成交**：成交要带金额校验、是销售管理者的判断，
// 不该由一次"客户说可以"隐式改掉整张管道的位置。两步分开，谁标的、什么时候标的都留得下痕迹。
func DecideQuote(gdb *gorm.DB, in ActInput, accept bool) (*QuoteDetail, error) {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	q, err := loadQuote(gdb, in.TenantID, in.QuoteID)
	if err != nil {
		return nil, err
	}
	action := ActionAccept
	status := model.QuoteStatusAccepted
	if !accept {
		action = ActionDecline
		status = model.QuoteStatusDeclined
	}
	if r := DecideQuoteAction(q.Status, action); r != "" {
		return nil, &ErrReject{Reason: r, Msg: "这张报价单当前状态不能被答复：" + r}
	}
	upd := isolate(gdb).Model(&model.Quote{}).
		Where("tenant_id = ? AND id = ? AND status = ?", in.TenantID, in.QuoteID, model.QuoteStatusSent).
		Updates(map[string]any{"status": status, "decided_at": now, "updated_at": now})
	if upd.Error != nil {
		return nil, upd.Error
	}
	if upd.RowsAffected == 0 {
		return nil, fmt.Errorf("deal: 报价单%w", ErrConflict)
	}
	back, err := loadQuote(gdb, in.TenantID, in.QuoteID)
	if err != nil {
		return nil, err
	}
	return quoteDetail(back), nil
}

// VoidQuote 人工作废（草稿/已发出皆可；终局态一律拒绝）。
// 作废是"这张单子不算了但也不消失"——没有删除路径，口径同活码只启停。
func VoidQuote(gdb *gorm.DB, in ActInput) (*QuoteDetail, error) {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	q, err := loadQuote(gdb, in.TenantID, in.QuoteID)
	if err != nil {
		return nil, err
	}
	if r := DecideQuoteAction(q.Status, ActionVoid); r != "" {
		return nil, &ErrReject{Reason: r, Msg: "这张报价单不能被作废：" + r}
	}
	upd := isolate(gdb).Model(&model.Quote{}).
		Where("tenant_id = ? AND id = ? AND status IN ?", in.TenantID, in.QuoteID,
			[]string{model.QuoteStatusDraft, model.QuoteStatusSent}).
		Updates(map[string]any{"status": model.QuoteStatusVoid, "updated_at": now})
	if upd.Error != nil {
		return nil, upd.Error
	}
	if upd.RowsAffected == 0 {
		return nil, fmt.Errorf("deal: 报价单%w", ErrConflict)
	}
	back, err := loadQuote(gdb, in.TenantID, in.QuoteID)
	if err != nil {
		return nil, err
	}
	return quoteDetail(back), nil
}

// EditQuoteInput 修改草稿
type EditQuoteInput struct {
	TenantID   uint
	QuoteID    uint
	Lines      []QuoteLineInput
	Note       string
	ValidUntil *time.Time
	SetValid   bool // 显式声明要改有效期（否则"清空"与"没传"分不开）
	Now        time.Time
}

// EditQuote 改草稿内容（已发出的拒：quote_locked / 终局拒：quote_final）。
// 合计一律由服务端按明细重算，前端传来的任何合计都不作数。
func EditQuote(gdb *gorm.DB, in EditQuoteInput) (*QuoteDetail, error) {
	q, err := loadQuote(gdb, in.TenantID, in.QuoteID)
	if err != nil {
		return nil, err
	}
	if r := DecideQuoteAction(q.Status, ActionEdit); r != "" {
		return nil, &ErrReject{Reason: r, Msg: "这张报价单不能编辑：" + r}
	}
	lines, total, r := BuildQuoteLines(in.Lines)
	if r != "" {
		return nil, &ErrReject{Reason: r, Msg: "报价明细不合法：" + r}
	}
	payload, err := json.Marshal(lines)
	if err != nil {
		return nil, err
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	upd := map[string]any{
		"lines":       string(payload),
		"total_cents": total,
		"note":        trimRunes(strings.TrimSpace(in.Note), 200),
		"updated_at":  now,
	}
	if in.SetValid {
		// nil 即清空：不设有效期是一种正常选择。同 EditDeal 一条坑——map 里的 nil 指针
		// 会被 GORM 当成"这列不改"，清空静默无效，过期的报价单会一直挂在"已发出"上。
		if in.ValidUntil == nil {
			upd["valid_until"] = gorm.Expr("NULL")
		} else {
			upd["valid_until"] = in.ValidUntil
		}
	}
	res := isolate(gdb).Model(&model.Quote{}).
		Where("tenant_id = ? AND id = ? AND status = ?", in.TenantID, in.QuoteID, model.QuoteStatusDraft).
		Updates(upd)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, &ErrReject{Reason: ReasonQuoteLocked, Msg: "报价单刚被他人发出，已锁定"}
	}
	back, err := loadQuote(gdb, in.TenantID, in.QuoteID)
	if err != nil {
		return nil, err
	}
	return quoteDetail(back), nil
}

// ListQuotes 一张商机的报价版本序列（version 倒序，最新在前）
func ListQuotes(gdb *gorm.DB, tenantID, dealID uint) ([]QuoteSummary, error) {
	var rows []model.Quote
	if err := isolate(gdb).Where("tenant_id = ? AND opportunity_id = ?", tenantID, dealID).
		Order("version DESC").Limit(50).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]QuoteSummary, 0, len(rows))
	for i := range rows {
		out = append(out, *summaryOf(&rows[i]))
	}
	return out, nil
}

// GetQuote 报价单详情（含明细行）。跨租户/不存在一律 ErrNotFound。
func GetQuote(gdb *gorm.DB, tenantID, quoteID uint) (*QuoteDetail, error) {
	q, err := loadQuote(gdb, tenantID, quoteID)
	if err != nil {
		return nil, err
	}
	return quoteDetail(q), nil
}

// SweepExpiredQuotes 把"已发出且已过有效期"的报价单落库为 expired，返回处理行数。
//
// 必须用平台句柄（db.DB）调用：本函数无请求 ctx、无租户语境，
// 传 db.RQ(c) 只会得到一条 `tenant_id = 0` 的条件从而永久空转。
// 它只改 status/时间戳，不新建任何带租户归属的行，所以盖章回调在此不参与。
func SweepExpiredQuotes(gdb *gorm.DB, now time.Time) (int64, error) {
	if now.IsZero() {
		now = time.Now()
	}
	res := isolate(gdb).Model(&model.Quote{}).
		Where("status = ? AND valid_until IS NOT NULL AND valid_until < ?", model.QuoteStatusSent, now).
		Updates(map[string]any{"status": model.QuoteStatusExpired, "updated_at": now})
	return res.RowsAffected, res.Error
}

// ============================================================
// 内部：读取与形态转换
// ============================================================

func loadQuote(gdb *gorm.DB, tenantID, quoteID uint) (*model.Quote, error) {
	var q model.Quote
	err := isolate(gdb).Where("tenant_id = ? AND id = ?", tenantID, quoteID).First(&q).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &q, nil
}

func summaryOf(q *model.Quote) *QuoteSummary {
	return &QuoteSummary{
		ID: q.ID, Version: q.Version, Status: q.Status,
		StatusName: model.QuoteStatusNames[q.Status],
		TotalCents: q.TotalCents, ValidUntil: fmtTimePtr(q.ValidUntil),
		SentAt: fmtTimePtr(q.SentAt), DecidedAt: fmtTimePtr(q.DecidedAt),
		CreatedAt: fmtTime(q.CreatedAt),
	}
}

// quoteDetail 摘要 + 明细。明细 JSON 解析失败时回**空数组**而不是报错：
// 一行坏数据不该让整张报价单打不开（读侧只读，坏数据由写侧的校验保证，见 BuildQuoteLines）。
func quoteDetail(q *model.Quote) *QuoteDetail {
	d := &QuoteDetail{
		QuoteSummary: *summaryOf(q),
		DealID:       q.OpportunityID,
		Lines:        []model.QuoteLine{},
		Note:         q.Note,
		CreatedBy:    q.CreatedBy,
		UpdatedAt:    fmtTime(q.UpdatedAt),
	}
	var lines []model.QuoteLine
	if err := json.Unmarshal([]byte(q.Lines), &lines); err == nil && lines != nil {
		d.Lines = lines
	}
	return d
}

// trimRunes 按字素截断（备注/说明类文本；列宽是字节，中文 3 字节/字，
// 用 rune 数上限换一次安全的字节数保证，比按字节切出半个字符安全得多）
func trimRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
