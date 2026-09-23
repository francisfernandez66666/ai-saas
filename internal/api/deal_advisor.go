// 顾问端的商机与报价入口（商机批 · 批次2，2026-09-23）：客户台面上"这个人跟过几单、
// 当前谈到哪、报过几版价"，以及开单 / 推进 / 出报价三个动作。
//
// 与 /admin/deals* 的**分工不是权限大小，而是问题不同**：
//   - 管理端问"整条管道健康吗、哪一格卡住了"→ 看板 + 格子下钻，看的是全租户；
//   - 顾问端问"我手上这个客户下一步做什么"→ 按客户取数（DealListItem 上的 OpenQuote
//     直接告诉他"有一版草稿还没发"），看不到别人的客户。
//
// 数据范围是这一组端点的**唯一新风险**：opportunities 表刻意没有归属列
// （单子跟着客户走，客户改派后单子自然换人，见 internal/model/deal.go 文件头），
// 所以裁剪只能落在"客户集合"上——用 db.DataScope 生成一条只 SELECT customers.id 的子查询，
// 传给 deal.Scope.CustomerScope。**不能直接把 DataScope 打在 opportunities 上**：
// 它追加的是裸 `assigned_user_id …` 谓词，商机表没这列，套上去就是 42703 恒 500。
//
// 终局单在这里**照常列出**（与看板的时间窗不同）：顾问问的是"跟过几单"，
// 把三个月前流失的那单藏起来，他就会重复开一张一模一样的单。
package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"ai-scrm/internal/db"
	"ai-scrm/internal/deal"
	"ai-scrm/internal/model"
)

// dealAdvisorScope 构造"登录者可见客户"的商机读作用域。
//
// CustomerScope 是一条子查询句柄（DataScope 的唯一落点在 customers 上，
// 与 D4 把 DataScope 落在 conversations 上再让 messages 收敛同构）。
// admin/super 角色时 DataScope 不加条件 ⇒ 等价于全租户，不需要另开一条代码路径。
func dealAdvisorScope(c *gin.Context) deal.Scope {
	return deal.Scope{
		TenantID: db.EffectiveTenantIDFromGin(c),
		CustomerScope: db.RQ(c).Model(&model.Customer{}).
			Scopes(db.DataScope(c)).Select("id"),
	}
}

// AdvisorCustomerDeals GET /advisor/customer/:id/deals
// apidump:ts AdvisorDealListResp
// 这个客户名下的全部商机（含终局单，最新优先）+ 每单的当前活报价。
//
// 客户不在自己的数据范围内时回**空列表**而不是 403：台面上根本不该出现这个人
// （列表接口已经裁过），这里给空集既不外泄"这个 ID 存在"，也不会让前端页面炸在错误分支上。
func AdvisorCustomerDeals(c *gin.Context) {
	cid, ok := PathUintID(c)
	if !ok {
		return
	}
	sc := dealAdvisorScope(c)
	items, err := deal.DealsByCustomer(db.RQ(c), sc, cid)
	if err != nil {
		RespErrInternal(c, err, "商机列表读取失败")
		return
	}
	RespOK(c, "ok", gin.H{
		"customer_id": cid, "list": items, "total": len(items),
		"config": dealBoardConfig(),
	})
}

// AdvisorCreateDeal POST /advisor/customer/:id/deals {title,stage,amount_cents,expected_close_at}
// apidump:ts DealDetailResp
// 顾问给客户开单。来源一律按后端判定填（客户身上有活码就是 acquisition，否则 manual），
// 不给前端传 source 的口子——"这单是谁带来的"是归因事实，不是填写人的自由发挥。
func AdvisorCreateDeal(c *gin.Context) {
	cid, ok := PathUintID(c)
	if !ok {
		return
	}
	sc := dealAdvisorScope(c)
	if !dealCustomerVisible(c, sc, cid) {
		RespErr(c, http.StatusNotFound, 404, "客户不存在")
		return
	}
	var req struct {
		Title           string `json:"title"`
		Stage           string `json:"stage"`
		AmountCents     int64  `json:"amount_cents"`
		ExpectedCloseAt string `json:"expected_close_at"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErrBind(c, err)
		return
	}
	exp, bad := dealParseDate(req.ExpectedCloseAt)
	if bad {
		RespErr(c, http.StatusBadRequest, 400, "expected_close_at 日期格式不合法")
		return
	}
	var cust model.Customer
	// 用平台句柄按 ID + 租户取一行（可见性上面已由 dealCustomerVisible 判过）
	if err := db.RQ(c).Where("id = ?", cid).First(&cust).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "客户不存在")
		return
	}
	source := model.DealSourceManual
	if strings.TrimSpace(cust.AcquisitionCode) != "" {
		source = model.DealSourceAcquisition
	}
	res, err := deal.CreateDeal(db.RQ(c), deal.CreateInput{
		TenantID: sc.TenantID, CustomerID: cid, Title: req.Title, Stage: req.Stage,
		AmountCents: req.AmountCents, Source: source, SourceCode: cust.AcquisitionCode,
		ExpectedCloseAt: exp,
	})
	if err != nil {
		dealWriteErr(c, err, "商机创建失败")
		return
	}
	dealRespDetail(c, sc.TenantID, res.Deal.ID)
}

// AdvisorMoveDeal POST /advisor/deals/:id/move {to,amount_cents,change_amount,lost_reason}
// apidump:ts DealDetailResp
// 顾问推进自己名下客户的单（裁决与并发守卫同管理端，见 deal.MoveDeal）。
func AdvisorMoveDeal(c *gin.Context) {
	id, ok := PathUintID(c)
	if !ok {
		return
	}
	sc := dealAdvisorScope(c)
	if !dealVisibleToScope(c, sc, id) {
		return
	}
	var req dealMoveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErrBind(c, err)
		return
	}
	back, err := deal.MoveDeal(db.RQ(c), deal.MoveInput{
		TenantID: sc.TenantID, DealID: id, To: req.To, AmountCents: req.AmountCents,
		ChangeAmount: req.ChangeAmount, LostReason: req.LostReason,
	})
	if err != nil {
		dealWriteErr(c, err, "商机推进失败")
		return
	}
	dealRespDetail(c, sc.TenantID, back.ID)
}

// AdvisorCreateQuote POST /advisor/deals/:id/quotes {lines,note,valid_until,send}
// apidump:ts DealQuoteResp
// 顾问出报价（发不发由 send 决定，仍走同一个状态机）。
func AdvisorCreateQuote(c *gin.Context) {
	id, ok := PathUintID(c)
	if !ok {
		return
	}
	sc := dealAdvisorScope(c)
	if !dealVisibleToScope(c, sc, id) {
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
		TenantID: sc.TenantID, DealID: id, CreatedBy: currentUserID(c),
		Lines: lines, Note: req.Note, ValidUntil: valid, SendNow: req.Send,
	})
	if err != nil {
		dealWriteErr(c, err, "报价单创建失败")
		return
	}
	RespOK(c, "ok", gin.H{"quote": q})
}

// ============================================================
// 可见性判定（三个写入口共用，别各写一遍）
// ============================================================

// dealCustomerVisible 客户是否落在登录者的数据范围内（DataScope 落在 customers 上）。
//
// 这里必须是**一次 count 判定**而不是"把客户读出来再在 Go 里比归属"：
// 部门子树、角色分支、fail-closed 三套判据只有 db 一处答得准，
// 在业务层重抄一遍就等于养出第二套口径（两套迟早分叉，分叉的表现是越权）。
func dealCustomerVisible(c *gin.Context, sc deal.Scope, customerID uint) bool {
	if customerID == 0 || sc.CustomerScope == nil {
		return false
	}
	var n int64
	q := db.RQ(c).Model(&model.Customer{}).
		Where("tenant_id = ? AND id = ?", sc.TenantID, customerID).
		Scopes(db.DataScope(c))
	if err := q.Count(&n).Error; err != nil {
		return false
	}
	return n > 0
}

// dealVisibleToScope 商机对当前登录者是否可写（不可见时**已经把 404 写进响应**，返回 false）。
//
// 判定复用 deal.GetDeal：它内部已经有同一套"CustomerScope 圈客户"的裁剪逻辑，
// 这里再手写一条 SQL 就是第二套判据（改一处漏一处，越权就是这么来的）。
// 不存在 / 跨租户 / 客户不在数据范围内一律 404 同形，不回显差别。
func dealVisibleToScope(c *gin.Context, sc deal.Scope, dealID uint) bool {
	_, _, err := deal.GetDeal(db.RQ(c), sc, dealID)
	if err != nil {
		dealWriteErr(c, err, "商机读取失败")
		return false
	}
	return true
}
