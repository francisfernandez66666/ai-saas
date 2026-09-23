// 管道看板与商机名单：一次扫描出"各阶段几张单、多少钱、几张卡住了"，
// 以及"点这一格看的就是这几张单"。
//
// 为什么计数与名单共用同一次扫描（scanFacts）而不是"计数走 SQL 聚合、名单另写一条查询"：
// 那样就是两边各判一次，改一侧忘另一侧迟早出现"卡片 12、点进去 9 行"
// （D4 与活码批同一条教训，这里从结构上断掉：先有一批人/一批单，再既数它又列它）。
//
// 数据量前提：一次扫描把本租户窗口内的单子（只取判定要的四个字段）读进内存，
// 上限 MaxDealsScanned；超出即如实标 Truncated。销售管道的单量级是"每客户几张"，
// 5000 张远超任何真实租户，撞到上限说明数据本身需要清理或窗口需要收窄——
// 那时看板上的数会比真实值小，而这个事实在响应里就看得见，不会静默。
package deal

import (
	"errors"
	"sort"
	"strings"
	"time"

	"ai-scrm/internal/model"

	"gorm.io/gorm"
)

// Scope 读侧作用域：租户 + （可选）"只看这个客户集合的单子"。
//
// CustomerScope 是一条**只 SELECT customers.id 的子查询句柄**，由 API 层用
// db.RQ(c).Scopes(db.DataScope(c)) 构造——数据范围裁剪的判据只有 db 一处能答
// （部门子树物化路径、角色分支、fail-closed），本包只负责"用它圈客户"。
//
// 为什么不复用 db.DataScope 直接打在 opportunities 上：DataScope 追加的是裸
// `assigned_user_id …` 谓词（见 internal/db/tenant_ctx.go），商机表上**刻意没有这一列**
// （归属跟着客户走，见 model/deal.go 文件头），直接套会得到 42703 而不是"少看了一些单"。
// 传 nil = 不按归属裁剪（管理员/超管视角）。
type Scope struct {
	TenantID      uint
	CustomerScope *gorm.DB
}

// dealFact 一张单子的判定输入（只带判据要用的列，不把宽表读进内存）
type dealFact struct {
	ID         uint
	CustomerID uint
	Stage      string
	// StageAt 必须显式钉列名：GORM 把 StageAt 推断成 stage_at，而列叫 stage_entered_at。
	// 不钉的话字段恒为零值——停滞天数永远算 0，看板一片绿、停滞榜永远空，
	// 而这正是"最该被看见的那几张"消失的方式（首跑实锤）。
	StageAt     time.Time `gorm:"column:stage_entered_at"`
	AmountCents int64
}

// scanFacts 取窗口内本租户（可选客户裁剪）的单子判定输入。
//
// 时间窗打在 created_at 上：**这段时期开出来的单子今天停在哪**，
// 与"今天有多少单子在各个环节"是两个问题，本包选前者（口径随 BoardNote 下发），
// 因为管理者问的是"我上个月开的那些单现在怎么样了"。
func scanFacts(gdb *gorm.DB, sc Scope, days int) ([]dealFact, bool, error) {
	g := isolate(gdb)
	limit := MaxDealsScanned + 1 // 多取一条即知是否触顶，不必再 count(*) 扫一遍
	q := g.Model(&model.Opportunity{}).
		Select("id, customer_id, stage, stage_entered_at, amount_cents").
		Where("tenant_id = ? AND created_at >= ?", sc.TenantID, since(ClampDays(days))).
		Order("id DESC").
		Limit(limit)
	if sc.CustomerScope != nil {
		// 子查询里外层已带 tenant_id，这里再钉一次：跨租户单子一条都不进判定集
		q = q.Where("tenant_id = ? AND customer_id IN (?)", sc.TenantID, sc.CustomerScope)
	}
	var rows []dealFact
	if err := q.Scan(&rows).Error; err != nil {
		return nil, false, err
	}
	truncated := len(rows) > MaxDealsScanned
	if truncated {
		rows = rows[:MaxDealsScanned]
	}
	return rows, truncated, nil
}

func since(days int) time.Time {
	return time.Now().Add(-time.Duration(days) * 24 * time.Hour)
}

// StageCell 一个阶段格子
type StageCell struct {
	Stage       string `json:"stage"`
	StageName   string `json:"stage_name"`
	Count       int64  `json:"count"`
	AmountCents int64  `json:"amount_cents"`
	DrillFilter string `json:"drill_filter"` // 前端拿它直接下钻，不必自己拼字符串
}

// BoardResult 管道看板
type BoardResult struct {
	Days      int         `json:"days"`
	StuckDays int         `json:"stuck_days"`
	Stages    []StageCell `json:"stages"`

	OpenCount        int64 `json:"open_count"`
	OpenAmountCents  int64 `json:"open_amount_cents"`
	StuckCount       int64 `json:"stuck_count"`
	StuckAmountCents int64 `json:"stuck_amount_cents"`
	WonCount         int64 `json:"won_count"`
	WonAmountCents   int64 `json:"won_amount_cents"`
	LostCount        int64 `json:"lost_count"`
	TotalCount       int64 `json:"total_count"`

	// WinRatePct 赢单率 = 成交数 /(成交数 + 流失数)*100。
	// **分母只算已终局的单子**：把 40 张还在谈的算进分母，赢单率就变成"管道深度的倒影"，
	// 谁都能靠多开单把它压低，那它就再也反映不了销售能力。
	WinRatePct float64 `json:"win_rate_pct"`

	Truncated bool   `json:"truncated"`
	Note      string `json:"note"`
}

// Board 管道看板（六阶段格子 + 在途/停滞/赢单率）
func Board(gdb *gorm.DB, sc Scope, days, stuckDays int) (*BoardResult, error) {
	days = ClampDays(days)
	stuckDays = ClampStuckDays(stuckDays)
	facts, truncated, err := scanFacts(gdb, sc, days)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	res := &BoardResult{
		Days: days, StuckDays: stuckDays,
		Stages:    make([]StageCell, 0, len(StageFilterCodes())),
		Truncated: truncated,
		Note:      BoardNote,
	}
	// 阶段格子先按固定顺序铺满：零命中也要出现。
	// 缺格子和"格子里是 0"在管理会上是两种说法——前者会被理解成"这块还没做"。
	// 先铺满再取指针：append 会让切片换底层数组，边 append 边存 &res.Stages[i]
	// 存进去的就是上一块数组的地址，后面的累加全打在孤儿内存上（看着绿、实际永不生效）。
	stages := append(append([]string{}, model.DealStages...), model.DealStageWon, model.DealStageLost)
	res.Stages = make([]StageCell, 0, len(stages))
	for _, s := range stages {
		res.Stages = append(res.Stages, StageCell{
			Stage: s, StageName: model.DealStageNames[s], DrillFilter: FilterStagePrefix + s,
		})
	}
	cells := map[string]*StageCell{}
	for i := range res.Stages {
		cells[res.Stages[i].Stage] = &res.Stages[i]
	}
	for _, f := range facts {
		c, ok := cells[f.Stage]
		if !ok { // 历史脏值（新增阶段码前的旧行）不炸看板，也不编进任何格子——它照样进 TotalCount
			continue
		}
		c.Count++
		c.AmountCents += f.AmountCents
		if MatchFilter(FilterOpen, f.Stage, f.StageAt, now, stuckDays) {
			res.OpenCount++
			res.OpenAmountCents += f.AmountCents
		}
		if MatchFilter(FilterStuck, f.Stage, f.StageAt, now, stuckDays) {
			res.StuckCount++
			res.StuckAmountCents += f.AmountCents
		}
		if MatchFilter(FilterWon, f.Stage, f.StageAt, now, stuckDays) {
			res.WonCount++
			res.WonAmountCents += f.AmountCents
		}
		if MatchFilter(FilterLost, f.Stage, f.StageAt, now, stuckDays) {
			res.LostCount++
		}
	}
	res.TotalCount = int64(len(facts))
	if denom := res.WonCount + res.LostCount; denom > 0 {
		res.WinRatePct = float64(res.WonCount) * 100 / float64(denom)
	}
	return res, nil
}

// DealListItem 名单/详情共用的一行（前端渲染所需字段齐备，不必二次查客户）
type DealListItem struct {
	ID              uint          `json:"id"`
	CustomerID      uint          `json:"customer_id"`
	CustomerName    string        `json:"customer_name"`
	CustomerPhone   string        `json:"customer_phone"`
	CustomerStage   string        `json:"customer_journey_stage"`
	OwnerUserID     uint          `json:"owner_user_id"` // 客户当前归属（不是快照，改派后立即生效）
	OwnerName       string        `json:"owner_name"`
	Title           string        `json:"title"`
	Stage           string        `json:"stage"`
	StageName       string        `json:"stage_name"`
	StageEnteredAt  string        `json:"stage_entered_at"`
	StalledDays     float64       `json:"stalled_days"` // 在这一步停了几天（停滞榜排序依据）
	AmountCents     int64         `json:"amount_cents"`
	Source          string        `json:"source"`
	SourceCode      string        `json:"source_code"`
	ExpectedCloseAt string        `json:"expected_close_at"` // 空串=未承诺
	WonAt           string        `json:"won_at"`
	LostAt          string        `json:"lost_at"`
	LostReason      string        `json:"lost_reason"`
	LostReasonName  string        `json:"lost_reason_name"`
	QuoteCount      int64         `json:"quote_count"` // 出过几版报价
	OpenQuote       *QuoteSummary `json:"open_quote"`  // 当前那张活着的报价单（无则 null）
	CreatedAt       string        `json:"created_at"`
}

// QuoteSummary 报价单摘要（列表内嵌，明细行只在详情给）
type QuoteSummary struct {
	ID         uint   `json:"id"`
	Version    int    `json:"version"`
	Status     string `json:"status"`
	StatusName string `json:"status_name"`
	TotalCents int64  `json:"total_cents"`
	ValidUntil string `json:"valid_until"`
	SentAt     string `json:"sent_at"`
	DecidedAt  string `json:"decided_at"`
	CreatedAt  string `json:"created_at"`
}

// DrillResult 下钻结果：Total 恒为"这一格命中几张"，与看板同源；List 是当前页。
type DrillResult struct {
	Filter    string         `json:"filter"`
	Label     string         `json:"label"`
	Days      int            `json:"days"`
	StuckDays int            `json:"stuck_days"`
	Total     int64          `json:"total"`
	List      []DealListItem `json:"list"`
	Truncated bool           `json:"truncated"`
	Note      string         `json:"note"`
}

// ErrBadFilter 过滤码不可下钻（不认识的一律拒，不默认回某一份名单）
var ErrBadFilter = errors.New("deal: filter not drillable")

// DrillDeals 按过滤码取商机名单（分页语义同 D4：total 不随分页变；
// 越界页只回空列表但 total 如实；单子行缺失时**不编造行也不改 total**）。
//
// 排序口径刻意随格子变：停滞榜按"停得最久"排（那一格的存在意义就是把最该跟的顶到前面），
// 其余按最新优先（新开的单最需要立刻动作）。
func DrillDeals(gdb *gorm.DB, sc Scope, filter string, days, stuckDays, page, pageSize int) (*DrillResult, error) {
	if !IsValidFilter(filter) {
		return nil, ErrBadFilter
	}
	days = ClampDays(days)
	stuckDays = ClampStuckDays(stuckDays)
	pageSize = ClampPageSize(pageSize)
	if page <= 0 {
		page = 1
	}
	facts, truncated, err := scanFacts(gdb, sc, days)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	hits := make([]dealFact, 0, len(facts))
	for _, f := range facts {
		if MatchFilter(filter, f.Stage, f.StageAt, now, stuckDays) {
			hits = append(hits, f)
		}
	}
	res := &DrillResult{
		Filter: filter, Label: FilterLabel(filter), Days: days, StuckDays: stuckDays,
		Total: int64(len(hits)), List: []DealListItem{}, Truncated: truncated, Note: BoardNote,
	}
	if len(hits) == 0 {
		return res, nil
	}
	if strings.TrimSpace(filter) == FilterStuck {
		sort.SliceStable(hits, func(i, j int) bool {
			return hits[i].StageAt.Before(hits[j].StageAt) // 先进入当前阶段的（停得最久的）在前
		})
	} // 否则保持 scanFacts 的 id DESC（最新优先）
	start := (page - 1) * pageSize
	if start >= len(hits) {
		return res, nil // 越界页：list 空、total 如实
	}
	end := start + pageSize
	if end > len(hits) {
		end = len(hits)
	}
	ids := make([]uint, 0, end-start)
	for _, h := range hits[start:end] {
		ids = append(ids, h.ID)
	}
	rows, err := loadDealRows(isolate(gdb), sc.TenantID, ids)
	if err != nil {
		return nil, err
	}
	res.List = rows
	return res, nil
}

// dealJoinRow 联表读出来的扁平行（客户名/归属名一次 join 到位，不在 Go 里逐单查客户——那才是真 N+1）
type dealJoinRow struct {
	ID              uint
	CustomerID      uint
	CustomerName    string
	CustomerPhone   string
	CustomerStage   string
	OwnerUserID     uint
	OwnerName       string
	Title           string
	Stage           string
	StageAt         time.Time `gorm:"column:stage_entered_at"` // 同 dealFact：列名与字段名不同名，必须钉
	AmountCents     int64
	Source          string
	SourceCode      string
	ExpectedCloseAt *time.Time
	WonAt           *time.Time
	LostAt          *time.Time
	LostReason      string
	CreatedAt       time.Time
}

// loadDealRows 按 ID 取完整行，顺序与传入 ids 一致（下钻已排好序，这里不重新排）。
//
// 行缺失时**不编造行也不改 total**：客户被删过、或并发下刚被改派到别人名下时，
// "命中数"与"此刻还能看到的明细"本来就可以不等——把 total 改成 len(list) 才是撒谎，
// 那会让看板与名单在同一个请求里自相矛盾。
func loadDealRows(gdb *gorm.DB, tenantID uint, ids []uint) ([]DealListItem, error) {
	if len(ids) == 0 {
		return []DealListItem{}, nil
	}
	// 传入句柄必须换成**不继承条件**的新语句（见 fresh）：db.RQ 的底条件是裸
	// `tenant_id = ?`，一旦下面 JOIN 了 customers/tenant_users 就是 42702 ambiguous。
	// 租户条件由下面的 d.tenant_id 自己钉，join 也按同一租户核对。
	g := fresh(gdb)
	var rows []dealJoinRow
	err := g.Table("opportunities AS d").
		Select(`d.id, d.customer_id, c.name AS customer_name, c.phone AS customer_phone,
			c.journey_stage AS customer_stage, c.assigned_user_id AS owner_user_id,
			-- 归属人显示名：真实名为空时退回用户名（tenant_users 没有 name 列，写错即整条查询 42703）
			COALESCE(NULLIF(u.real_name, ''), u.username, '') AS owner_name,
			d.title, d.stage, d.stage_entered_at, d.amount_cents, d.source, d.source_code,
			d.expected_close_at, d.won_at, d.lost_at, d.lost_reason, d.created_at`).
		Joins("JOIN customers c ON c.id = d.customer_id AND c.tenant_id = d.tenant_id").
		Joins("LEFT JOIN tenant_users u ON u.id = c.assigned_user_id").
		Where("d.tenant_id = ? AND d.id IN ?", tenantID, ids).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	byID := map[uint]dealJoinRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	ordered := make([]DealListItem, 0, len(ids))
	for _, id := range ids {
		r, ok := byID[id]
		if !ok {
			continue // 行已不可见：如实少一行，不拿空壳占位
		}
		now := time.Now()
		item := DealListItem{
			ID: r.ID, CustomerID: r.CustomerID,
			CustomerName: r.CustomerName, CustomerPhone: r.CustomerPhone,
			CustomerStage: r.CustomerStage, OwnerUserID: r.OwnerUserID, OwnerName: r.OwnerName,
			Title: r.Title, Stage: r.Stage, StageName: model.DealStageNames[r.Stage],
			StageEnteredAt: fmtTime(r.StageAt), StalledDays: StalledDays(r.StageAt, now),
			AmountCents: r.AmountCents, Source: r.Source, SourceCode: r.SourceCode,
			ExpectedCloseAt: fmtTimePtr(r.ExpectedCloseAt),
			WonAt:           fmtTimePtr(r.WonAt),
			LostAt:          fmtTimePtr(r.LostAt),
			LostReason:      r.LostReason, LostReasonName: model.DealLostReasonNames[r.LostReason],
			CreatedAt: fmtTime(r.CreatedAt),
		}
		ordered = append(ordered, item)
	}
	if err := attachQuotes(gdb, tenantID, ordered); err != nil {
		return nil, err
	}
	return ordered, nil
}

// attachQuotes 一次性补齐"出过几版报价 + 当前那张活着的"（分组查询，不做 N+1）。
//
// 两条查询各取一个独立会话：第二条若复用第一条的句柄，就会带着上一轮的
// `Select(...)`/`Group(...)` 去 `Find(&model.Quote{})`，出来的不是报价单行而是聚合列。
func attachQuotes(gdb *gorm.DB, tenantID uint, items []DealListItem) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	var counts []struct {
		DealID uint
		N      int64
	}
	if err := isolate(gdb).Model(&model.Quote{}).
		Select("opportunity_id AS deal_id, count(*) AS n").
		Where("tenant_id = ? AND opportunity_id IN ?", tenantID, ids).
		Group("opportunity_id").Scan(&counts).Error; err != nil {
		return err
	}
	countMap := map[uint]int64{}
	for _, c := range counts {
		countMap[c.DealID] = c.N
	}
	var openQuotes []model.Quote
	if err := isolate(gdb).Where("tenant_id = ? AND opportunity_id IN ? AND status IN ?", tenantID, ids,
		[]string{model.QuoteStatusDraft, model.QuoteStatusSent}).
		Order("version DESC").Find(&openQuotes).Error; err != nil {
		return err
	}
	openMap := map[uint]*model.Quote{}
	for i := range openQuotes { // version DESC ⇒ 第一版即当前那张（同单至多一张活口，见 023 部分唯一索引）
		if _, seen := openMap[openQuotes[i].OpportunityID]; !seen {
			openMap[openQuotes[i].OpportunityID] = &openQuotes[i]
		}
	}
	for i := range items {
		items[i].QuoteCount = countMap[items[i].ID]
		if q := openMap[items[i].ID]; q != nil {
			sum := QuoteSummary{
				ID: q.ID, Version: q.Version, Status: q.Status,
				StatusName: model.QuoteStatusNames[q.Status],
				TotalCents: q.TotalCents, ValidUntil: fmtTimePtr(q.ValidUntil),
				SentAt: fmtTimePtr(q.SentAt), DecidedAt: fmtTimePtr(q.DecidedAt),
				CreatedAt: fmtTime(q.CreatedAt),
			}
			items[i].OpenQuote = &sum
		}
	}
	return nil
}

// GetDeal 单张商机详情（含其报价版本序列）。跨租户/不存在一律 ErrNotFound（对外 404，不回显差别）。
func GetDeal(gdb *gorm.DB, sc Scope, id uint) (*DealListItem, []QuoteSummary, error) {
	var d model.Opportunity
	// 每条查询各取一个新会话（勿复用同一个句柄）：First 会把 Model/ORDER BY/LIMIT 与
	// 这条条件留在句柄上，后面 loadDealRows 的联表查询继承了它就跑不通
	// （而建单后回显详情走的正是这条链）。
	if err := isolate(gdb).Where("tenant_id = ? AND id = ?", sc.TenantID, id).First(&d).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	// 归属裁剪在详情上同样生效：顾问打不开不属于自己客户的单子，
	// 否则"列表看不见的单子换个 URL 就能看"会让整条数据范围白做。
	if sc.CustomerScope != nil {
		var n int64
		if err := isolate(gdb).Model(&model.Customer{}).
			Where("tenant_id = ? AND id IN (?)", sc.TenantID, sc.CustomerScope).
			Where("id = ?", d.CustomerID).Count(&n).Error; err != nil {
			return nil, nil, err
		}
		if n == 0 {
			return nil, nil, ErrNotFound
		}
	}
	items, err := loadDealRows(isolate(gdb), sc.TenantID, []uint{id})
	if err != nil {
		return nil, nil, err
	}
	if len(items) == 0 {
		return nil, nil, ErrNotFound
	}
	quotes, err := ListQuotes(isolate(gdb), sc.TenantID, id)
	if err != nil {
		return nil, nil, err
	}
	return &items[0], quotes, nil
}

// DealsByCustomer 这个客户名下的全部商机（含历史终局单），最新优先。
//
// 与看板/下钻不同，这里**没有时间窗**：顾问在台面上问的是"这个人跟过几单、上次谈到哪"，
// 把三个月前流失的那单窗口掉，他就会重复开一张一模一样的单。
// 终局单在这里是资产不是噪音，正因如此它不参与 ux_deal_one_open_per_customer 的占用。
func DealsByCustomer(gdb *gorm.DB, sc Scope, customerID uint) ([]DealListItem, error) {
	if customerID == 0 {
		return []DealListItem{}, nil
	}
	g := isolate(gdb)
	var ids []uint
	q := g.Model(&model.Opportunity{}).
		Where("tenant_id = ? AND customer_id = ?", sc.TenantID, customerID).
		Order("id DESC")
	if sc.CustomerScope != nil {
		q = q.Where("customer_id IN (?)", sc.CustomerScope)
	}
	if err := q.Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return loadDealRows(isolate(gdb), sc.TenantID, ids)
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}

func fmtTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return fmtTime(*t)
}
