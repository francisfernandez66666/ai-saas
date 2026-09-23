// 商机与报价的读写层：建单（含"一客户一在途"的复用与并发回落）、阶段推进、
// 金额与预期成交日修改、管道看板计数与格子的下钻名单、报价单的版本链与状态机。
//
// 句柄约定（触达批实锤过的红线，逐条遵守）：调用方传进来的是 db.RQ(c)/db.PQ(c)，
// 它的 clone=0、条件**就地累加**。本文件每个函数都要跑不止一条查询，
// 所以一律先过 isolate() 换成"继承租户条件但各查询独立"的会话句柄。
//
// 写入约定（C7 事务内写租户表红线）：所有写租户表的入口都显式带 tenantID 落列，
// 不依赖请求 context 里的租户自动盖章——后台钩子链路（AI 自动建单）本就没有 gin ctx。
//
// 更新约定（复核批起反复踩的坑）：改单子一律**字段级 Updates + 条件守卫**
// （WHERE 里带上"我以为它还是的那个状态"），绝不整行 Save。
// 典型现场：顾问 A 打开报价单草稿、顾问 B 在此期间把它发了出去，
// A 保存草稿若整行覆写就会把 sent 改回 draft 且抹掉 sent_at——发出去过的单子又变回没发过。
package deal

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"ai-scrm/internal/model"

	"gorm.io/gorm"
)

// isolate 见文件头：把派生句柄换成"各查询独立但继承租户条件"的会话句柄。
func isolate(gdb *gorm.DB) *gorm.DB {
	if gdb == nil {
		return nil
	}
	return gdb.Session(&gorm.Session{})
}

// fresh 换成"完全不继承已有条件"的新语句（context 仍沿用，租户盖章/RLS 会话变量不丢）。
//
// 只有一条路需要它：**联表查询**。db.RQ(c) 的底条件是裸 `tenant_id = ?`（不带表名），
// 单表查询继承没事，一旦 JOIN 了 customers/tenant_users，PG 就报 42702 ambiguous——
// 报错点还在"建单成功后回显详情"这一步，看起来像查询坏了，其实是条件串了门。
// 用它就必须自己把租户条件钉全（本包各调用点都显式带 `d.tenant_id = ?`）。
func fresh(gdb *gorm.DB) *gorm.DB {
	if gdb == nil {
		return nil
	}
	return gdb.Session(&gorm.Session{NewDB: true})
}

// ErrNotFound 商机/报价单不存在或不在本租户作用域内（对外一律 404，不回显"别家有没有这条"）
var ErrNotFound = errors.New("deal: not found")

// ErrConflict 乐观并发冲突：条件守卫（WHERE 带上"我以为它还是的那个状态"）0 行受影响。
//
// 为什么要单独一个哨兵而不是回 500 或报成功：后者会让操作的人以为自己把单子标成了成交，
// 而实际那张单子还停在谈判中——**推进失败必须让动手的人知道**。
// API 层按 errors.Is 判它并回 409（冒烟同样按状态码分支，不解析中文文案）。
var ErrConflict = errors.New("已被他人改动，请刷新后重试")

// ErrReject 入参拒绝（Reason 是稳定原因码，前端与冒烟按它分支，不解析中文）
type ErrReject struct {
	Reason string
	Msg    string
}

// Error 实现 error 接口：拒绝原因带稳定原因码前缀，供上层按码分支而不是猜文案。
func (e *ErrReject) Error() string { return "deal: " + e.Msg }

// RejectReason 从 error 里取稳定原因码（非 ErrReject 返回 ""）
func RejectReason(err error) string {
	var re *ErrReject
	if errors.As(err, &re) {
		return re.Reason
	}
	return ""
}

// DefaultWindowDays 看板与名单的默认统计窗口
const DefaultWindowDays = 90

// MaxWindowDays 窗口硬上限（一年；再长该走导出而不是线上聚合）
const MaxWindowDays = 365

// MaxPageSize 单页硬顶（口径同 D4/活码：下钻用来"核对这一格是哪几张"，导数走导出接口）
const MaxPageSize = 100

// MaxDealsScanned 一次看板/下钻最多扫描的单子数。
// 超过则**如实标 truncated** 而不是悄悄少算：管道单量本该远低于此，
// 真到了这个量级说明这家租户要么在批量导入历史数据、要么窗口开得太大，
// 两种情况下"数字比名单长"都比"两个都少一个"更难查。
const MaxDealsScanned = 5000

// ClampDays 外部 days 钳进 [1, MaxWindowDays]，非法/缺省回默认
func ClampDays(days int) int {
	if days <= 0 {
		return DefaultWindowDays
	}
	if days > MaxWindowDays {
		return MaxWindowDays
	}
	return days
}

// ClampPageSize 外部 page_size 钳进 [1, MaxPageSize]
func ClampPageSize(size int) int {
	if size <= 0 {
		return 20
	}
	if size > MaxPageSize {
		return MaxPageSize
	}
	return size
}

// BoardNote 口径说明（随响应下发，前端与冒烟都读它，不各自写第二套话）
const BoardNote = "时间窗打在商机创建时间上：统计这段时间开出来的单子此刻各停在哪个阶段；金额单位为分，成交口径为已填的预计成交金额，与计费订单实收金额无关。"

// ============================================================
// 建单
// ============================================================

// CreateInput 建单入参
type CreateInput struct {
	TenantID    uint
	CustomerID  uint
	Title       string
	Stage       string // 缺省 qualified（"需求确认"）：一上来就填 quoted 是自欺
	AmountCents int64
	Source      string // manual|acquisition|ai，非法归 manual
	SourceCode  string // 活码短码快照（仅 source=acquisition 时有意义）
	// ExpectedCloseAt 预期成交日，可空（空=不承诺，报表不把它算进超期）
	ExpectedCloseAt *time.Time
	// Now 注入点：单测要把 stage_entered_at 摆在任意时刻（不注入就只能等真时间流逝）
	Now time.Time
}

// CreateResult 建单结果。Existing=true 表示该客户已有在途单、本次没新建。
type CreateResult struct {
	Deal     *model.Opportunity
	Existing bool // 命中已有在途单（人工建单会被拒，幂等入口会把它当成功）
}

// CreateDeal 人工建单：客户已有在途单时**明确拒绝**（ReasonDealAlreadyOpen）。
//
// 为什么这里不静默复用：管理者点"新建商机"时如果拿到一张别人正在谈的旧单，
// 他会以为那是他的新单并在上面改金额——拒一次的成本远小于错账的成本。
// 自动链路（AI 识别到报价信号）要的是幂等，走 EnsureOpenDeal。
func CreateDeal(gdb *gorm.DB, in CreateInput) (*CreateResult, error) {
	res, err := createDeal(gdb, in, false)
	if err != nil {
		return nil, err
	}
	if res.Existing {
		return nil, &ErrReject{Reason: ReasonDealAlreadyOpen, Msg: "该客户已有在途商机，请在原单上推进"}
	}
	return res, nil
}

// EnsureOpenDeal 幂等建单：已有在途单就直接返回它（不报错）。
// 给 AI 自动建单钩子与批量导入用——它们要的是"有一张在途单"，不是"我建了这张单"。
func EnsureOpenDeal(gdb *gorm.DB, in CreateInput) (*CreateResult, error) {
	return createDeal(gdb, in, true)
}

func createDeal(gdb *gorm.DB, in CreateInput, reuseExisting bool) (*CreateResult, error) {
	if in.TenantID == 0 {
		return nil, &ErrReject{Reason: ReasonTenantRequired, Msg: "无租户语境"}
	}
	if in.CustomerID == 0 {
		return nil, &ErrReject{Reason: ReasonCustomerNotFound, Msg: "缺少客户"}
	}
	if r := ValidateTitle(in.Title); r != "" {
		return nil, &ErrReject{Reason: r, Msg: "标题不合法：" + r}
	}
	if r := ValidateAmount(in.AmountCents); r != "" {
		return nil, &ErrReject{Reason: r, Msg: "金额不合法：" + r}
	}
	stage := strings.TrimSpace(in.Stage)
	if stage == "" {
		stage = model.DealStageQualified
	}
	if !IsValidStage(stage) {
		return nil, &ErrReject{Reason: ReasonStageUnknown, Msg: "阶段码不合法"}
	}
	if IsTerminal(stage) {
		// 建单即终局没有意义：一张没有过程单子进不了管道，
		// 而 won 需要金额、lost 需要原因，这些都在推进时校验更合理。
		return nil, &ErrReject{Reason: ReasonStageUnknown, Msg: "新建商机不能直接是终局阶段"}
	}
	g := isolate(gdb)

	// 客户必须在本租户下存在：商机挂在不存在的客户身上，就是给管道埋一条永远点不开的 dead end
	var custCnt int64
	if err := g.Model(&model.Customer{}).Where("tenant_id = ? AND id = ?", in.TenantID, in.CustomerID).Count(&custCnt).Error; err != nil {
		return nil, err
	}
	if custCnt == 0 {
		return nil, &ErrReject{Reason: ReasonCustomerNotFound, Msg: "客户不存在或不属于本租户"}
	}

	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	// 先查在途：绝大多数重复点击在这一步就被挡掉（DB 部分唯一索引仍是最终兜底）
	var open model.Opportunity
	if err := g.Where("tenant_id = ? AND customer_id = ? AND stage NOT IN ?", in.TenantID, in.CustomerID,
		[]string{model.DealStageWon, model.DealStageLost}).Order("id DESC").First(&open).Error; err == nil {
		if reuseExisting {
			return &CreateResult{Deal: &open, Existing: true}, nil
		}
		return &CreateResult{Existing: true}, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	row := model.Opportunity{
		TenantID:        in.TenantID,
		CustomerID:      in.CustomerID,
		Title:           strings.TrimSpace(in.Title),
		Stage:           stage,
		StageAt:         now,
		AmountCents:     in.AmountCents,
		Source:          NormalizeSource(in.Source),
		SourceCode:      strings.TrimSpace(in.SourceCode),
		ExpectedCloseAt: in.ExpectedCloseAt,
	}
	if err := isolate(gdb).Create(&row).Error; err != nil {
		// 并发双发：两个请求同时过了上面的复查，部分唯一索引 ux_deal_one_open_per_customer 拦住多出来的那条。
		// 撞了就回读既有的那张，绝不让用户看到一条"数据库约束错误"（口径同 G1 EnsureActiveConversation）。
		if strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key") {
			var back model.Opportunity
			if e2 := isolate(gdb).Where("tenant_id = ? AND customer_id = ? AND stage NOT IN ?",
				in.TenantID, in.CustomerID, []string{model.DealStageWon, model.DealStageLost}).
				Order("id DESC").First(&back).Error; e2 == nil {
				if reuseExisting {
					return &CreateResult{Deal: &back, Existing: true}, nil
				}
				return &CreateResult{Existing: true}, nil
			}
		}
		return nil, err
	}
	return &CreateResult{Deal: &row}, nil
}

// ============================================================
// 推进与编辑
// ============================================================

// MoveInput 阶段推进入参
type MoveInput struct {
	TenantID     uint
	DealID       uint
	To           string
	AmountCents  int64 // 本次一并提交的金额（0 分与"不改"由 ChangeAmount 区分；won 判定用的是**改完之后**的金额）
	ChangeAmount bool  // 显式声明要改金额：不声明时 0 分与"不改"分不开
	LostReason   string
	Now          time.Time
}

// MoveDeal 推进阶段（裁决全在 policy.DecideMove，本函数只负责"按裁决落库"）。
//
// 条件守卫带上了"我以为它现在的阶段"：两个人同时点推进时后一个会 0 行受影响，
// 这里把它报成 conflict 而不是成功——**推进失败必须让操作的人知道**，
// 否则他会以为自己把单子标成了成交，而实际那张单子还停在谈判中。
func MoveDeal(gdb *gorm.DB, in MoveInput) (*model.Opportunity, error) {
	g := isolate(gdb)
	var d model.Opportunity
	if err := g.Where("tenant_id = ? AND id = ?", in.TenantID, in.DealID).First(&d).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	amount := d.AmountCents
	if in.ChangeAmount {
		amount = in.AmountCents
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	dec := DecideMove(StageMoveInput{From: d.Stage, To: in.To, AmountCents: amount, LostReason: in.LostReason, Now: now})
	if !dec.Allow {
		return nil, &ErrReject{Reason: dec.Reason, Msg: "阶段不能这样推进：" + dec.Reason}
	}
	// 终局单不允许再被推进（上面已判），而非终局单允许顺手改金额
	upd := map[string]any{
		"stage":            dec.Stage,
		"stage_entered_at": dec.StageAt,
		"lost_reason":      dec.LostReason, // 归零规则集中在裁决里，这里不判
		"updated_at":       now,
	}
	if in.ChangeAmount {
		upd["amount_cents"] = amount
	}
	if dec.WonAt != nil {
		upd["won_at"] = *dec.WonAt
	}
	if dec.LostAt != nil {
		upd["lost_at"] = *dec.LostAt
	}
	res := isolate(gdb).Model(&model.Opportunity{}).
		Where("tenant_id = ? AND id = ? AND stage = ?", in.TenantID, in.DealID, d.Stage).
		Updates(upd)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, fmt.Errorf("deal: 商机状态%w", ErrConflict)
	}
	var back model.Opportunity
	if err := isolate(gdb).Where("tenant_id = ? AND id = ?", in.TenantID, in.DealID).First(&back).Error; err != nil {
		return nil, err
	}
	return &back, nil
}

// EditInput 非阶段字段编辑入参（标题/金额/预期成交日）
type EditInput struct {
	TenantID        uint
	DealID          uint
	Title           string
	AmountCents     int64
	ChangeAmount    bool
	ExpectedCloseAt *time.Time
	SetCloseDate    bool // 显式声明要改日期（否则"清空"与"没传"分不开）
	Now             time.Time
}

// EditDeal 编辑标题/金额/预期成交日。**终局单一律拒绝**（deal_closed）：
// 已发生的成交金额是历史事实，事后改动等于重写报表（理由见 policy.IsTerminal 注释）。
func EditDeal(gdb *gorm.DB, in EditInput) (*model.Opportunity, error) {
	g := isolate(gdb)
	var d model.Opportunity
	if err := g.Where("tenant_id = ? AND id = ?", in.TenantID, in.DealID).First(&d).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if IsTerminal(d.Stage) {
		return nil, &ErrReject{Reason: ReasonDealClosed, Msg: "已终局的商机不可编辑"}
	}
	upd := map[string]any{}
	if t := strings.TrimSpace(in.Title); t != "" {
		if r := ValidateTitle(t); r != "" {
			return nil, &ErrReject{Reason: r, Msg: "标题不合法：" + r}
		}
		upd["title"] = t
	}
	if in.ChangeAmount {
		if r := ValidateAmount(in.AmountCents); r != "" {
			return nil, &ErrReject{Reason: r, Msg: "金额不合法：" + r}
		}
		upd["amount_cents"] = in.AmountCents
	}
	if in.SetCloseDate {
		// 传 nil 即清空：不承诺比乱承诺诚实。
		// 必须是 Expr("NULL")——map 里的 nil 指针会被 GORM 当成"这列不改"，
		// 于是"清空日期"静默无效，报表继续按一个已经不存在的承诺算超期（首跑实锤）。
		if in.ExpectedCloseAt == nil {
			upd["expected_close_at"] = gorm.Expr("NULL")
		} else {
			upd["expected_close_at"] = in.ExpectedCloseAt
		}
	}
	if len(upd) == 0 {
		return &d, nil // 什么都没要改：原样返回，不写库也不报错
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	upd["updated_at"] = now
	res := isolate(gdb).Model(&model.Opportunity{}).
		Where("tenant_id = ? AND id = ? AND stage NOT IN ?", in.TenantID, in.DealID,
			[]string{model.DealStageWon, model.DealStageLost}).
		Updates(upd)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		// 读到非终局、写时已是终局：有人在这之间标了成交/流失
		return nil, &ErrReject{Reason: ReasonDealClosed, Msg: "商机刚被推进到终局，请刷新"}
	}
	var back model.Opportunity
	if err := isolate(gdb).Where("tenant_id = ? AND id = ?", in.TenantID, in.DealID).First(&back).Error; err != nil {
		return nil, err
	}
	return &back, nil
}
