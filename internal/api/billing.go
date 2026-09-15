// 收银台API：商业化收银台，含下单/支付回调/退款/发票等接口。
package api

import "ai-scrm/internal/billing"

import "ai-scrm/internal/notify"

import (
	"ai-scrm/internal/runtimecfg"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 收银台 API（商业化第一批 M1，2026-08-23）
//
// 路由（挂 JWTAuth + TenantConsistency + OrgResolve + AdminRequired）：
//   POST   /api/v1/billing/orders            创建订单（static_qr→回收款码；mock→模拟码）
//   GET    /api/v1/billing/orders/:id        轮询订单状态（收银台自动刷新）
//   POST   /api/v1/billing/orders/mock-pay   模拟到账（仅 mock 模式，生产 403）
//   POST   /api/v1/billing/manual-confirm    「我已付费」→ critical 审计 + 催告超管
//   GET    /api/v1/super/orders/pending      超管待人工确认列表
//   POST   /api/v1/super/orders/:id/confirm  确认到账 → 幂等发放
//
// 安全要点：
//   - mock-pay 双保险之一：pay_mode != mock 一律 403（另一保险是前端隐藏入口）
//   - 订单查询限定本租户，跨租户单号一律 404 不泄露存在性
// ============================================================

// createOrderReq 创建订单请求体：仅传 package_id（必填），其余金额/渠道由服务端从商业包表推导
type createOrderReq struct {
	PackageID uint `json:"package_id" binding:"required"`
}

// CreateBillingOrder POST /api/v1/billing/orders
// CreateBillingOrder 为指定商业包创建支付订单。
func CreateBillingOrder(c *gin.Context) {
	var req createOrderReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误：package_id 必填")
		return
	}
	tid := tenantIDOf(c)

	var pkg model.Package
	if err := db.DB.Where("id = ? AND enabled = ?", req.PackageID, true).First(&pkg).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "商业包不存在或已下架")
		return
	}
	if pkg.PType == model.PackageTypeFree {
		RespErr(c, http.StatusBadRequest, 400, "试用包无需购买，注册后自动发放")
		return
	}

	order, err := billing.CreateOrderForPackage(tid, &pkg)
	if err != nil {
		RespErr(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	writeOrderAudit(c, tid, "order_create", order)
	RespOK(c, "", order)
}

// GetBillingOrder GET /api/v1/billing/orders/:id —— 收银台轮询
func GetBillingOrder(c *gin.Context) {
	tid := tenantIDOf(c)
	// 健壮性收口(2026-09-05)：ID 入口校验，非法直接 400，不再以脏值打到 PG(22P02)
	oid, ok := PathUintID(c)
	if !ok {
		return
	}
	var order model.BillingOrder
	if err := db.DB.Where("id = ? AND tenant_id = ?", oid, tid).First(&order).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "订单不存在")
		return
	}
	RespOK(c, "", order)
}

// ListBillingOrders GET /api/v1/billing/orders —— 本租户订单列表（最新在前）
// 修复：此前收银台靠前端 sessionStorage 记订单ID逐个轮询，换设备/清缓存即丢历史；
// 服务端列表以租户为锚，天然完整
func ListBillingOrders(c *gin.Context) {
	tid := tenantIDOf(c)
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if limit < 1 || limit > 200 {
		limit = 50
	}
	orders := []model.BillingOrder{}
	// F1 修复(2026-09-15)：Select 白名单补齐退款/发票/升级抵扣列——旧口径只选 10 列，
	// 退款成功后列表接口 refund_amount_cents 恒为 0、refunded_at 恒为 null（DB 有值），
	// Admin 订单列表与财务对账视图被误导（UAT 字节级复核实测复现）。
	if err := db.DB.Where("tenant_id = ?", tid).
		Select("id, order_no, package_id, amount_cents, original_amount_cents, period, channel, status," +
			"manual_confirm, paid_at, created_at, refunded_at, refund_amount_cents, refund_psp_status," +
			"expire_at, invoice_requested, invoice_status, invoice_no, invoice_title," +
			"upgrade_offset_cents, upgrade_base_order_id").
		Order("id DESC").Limit(limit).Find(&orders).Error; err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "查询失败")
		return
	}
	// 附带包名便于展示（一次 JOIN 查询比 N+1 查 packages 好）
	type row struct {
		model.BillingOrder
		PackageName string `json:"package_name"`
	}
	rows := []row{}
	if len(orders) > 0 {
		nameMap := map[uint]string{}
		pkgIDs := make([]uint, 0, len(orders))
		for _, o := range orders {
			if o.PackageID > 0 {
				pkgIDs = append(pkgIDs, o.PackageID)
			}
		}
		if len(pkgIDs) > 0 {
			var pkgs []model.Package
			db.DB.Select("id, name").Where("id IN ?", pkgIDs).Find(&pkgs)
			for _, p := range pkgs {
				nameMap[p.ID] = p.Name
			}
		}
		for _, o := range orders {
			rows = append(rows, row{BillingOrder: o, PackageName: nameMap[o.PackageID]})
		}
	}
	RespOK(c, "", rows)
}

// MockPayOrder POST /api/v1/billing/orders/mock-pay {order_id}
// 仅 pay_mode=mock 可用；生产环境 403（双保险之接口侧）
// R3 修复(2026-09-11)：pay_mode 出厂默认 mock（config.GetConfig 兜底值），生产部署若忘配
// pay_mode=sdk，任何租户 admin 调本接口即可 0 元白嫖权益——加进程级发布闸门：
// release 构建（GIN_MODE=release）必须显式 ALLOW_MOCK_PAY=true 才开放，否则一律 403。
// 开发/测试环境不受影响（E2E 冒烟依赖 mock-pay 跑通全链路）。
func MockPayOrder(c *gin.Context) {
	if os.Getenv("GIN_MODE") == "release" && os.Getenv("ALLOW_MOCK_PAY") != "true" {
		RespErr(c, http.StatusForbidden, 403, "生产环境已禁用模拟支付")
		return
	}
	if billing.GetPayMode() != "mock" {
		RespErr(c, http.StatusForbidden, 403, "非模拟模式禁止模拟支付")
		return
	}
	var req struct {
		OrderID uint `json:"order_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误：order_id 必填")
		return
	}
	tid := tenantIDOf(c)
	var order model.BillingOrder
	if err := db.DB.Where("id = ? AND tenant_id = ?", req.OrderID, tid).First(&order).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "订单不存在")
		return
	}

	confirmed, err := confirmAndGrant(c, &order, "mock")
	if err != nil {
		RespErr(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	db.RQ(c).First(&order, order.ID)
	msg := map[bool]string{true: "模拟到账成功，权益已发放", false: "订单已处理过，勿重复操作"}[confirmed]
	RespOK(c, msg, gin.H{"order": order, "granted": confirmed})
}

// ManualConfirmPaid POST /api/v1/billing/manual-confirm {order_id}
// static_qr 模式下租户点「我已付费」：置 manual_confirm 标记 → critical 审计 + 企微催告超管
// 权益不在此刻发放，必须等超管核实到账后 confirm（防白嫖）
func ManualConfirmPaid(c *gin.Context) {
	var req struct {
		OrderID uint `json:"order_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误：order_id 必填")
		return
	}
	tid := tenantIDOf(c)
	var order model.BillingOrder
	if err := db.DB.Where("id = ? AND tenant_id = ?", req.OrderID, tid).First(&order).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "订单不存在")
		return
	}
	if order.Status == "paid" {
		RespOK(c, "订单已确认到账", order)
		return
	}

	// 订单已按 id+tenant_id 前置限定（跨租户在 rls_scope_test 已验证 404 守卫），此更新安全
	res := db.DB.Model(&model.BillingOrder{}).Where("id = ?", order.ID).
		Update("manual_confirm", true)
	if res.Error != nil {
		RespErr(c, http.StatusInternalServerError, 500, "标记失败")
		return
	}

	// critical 审计 + 企微/邮件通道催告超管（通知通道抽象见 service/notifier.go）
	var tName string
	db.DB.Model(&model.Tenant{}).Select("name").Where("id = ?", tid).Scan(&tName)
	writeOrderAudit(c, tid, "order_manual_confirm_critical", &order)
	notify.NotifyManualConfirmPaid(order.OrderNo, tName, order.AmountCents)

	order.ManualConfirm = true
	RespOK(c, "已提交，平台核实收款后将自动开通（通常10分钟内）", order)
}

// SuperPendingOrders GET /api/v1/super/orders/pending —— 待人工确认列表
func SuperPendingOrders(c *gin.Context) {
	type row struct {
		ID            uint       `json:"id"`
		OrderNo       string     `json:"order_no"`
		TenantName    string     `json:"tenant_name"`
		TenantID      uint       `json:"tenant_id"`
		PackageName   string     `json:"package_name"`
		AmountCents   int        `json:"amount_cents"`
		Status        string     `json:"status"`
		ManualConfirm bool       `json:"manual_confirm"`
		CreatedAt     time.Time  `json:"created_at"`
		PaidAt        *time.Time `json:"paid_at"`
	}
	rows := []row{}
	err := db.DB.Table("billing_orders o").
		Select(`o.id, o.order_no, COALESCE(t.name,'') as tenant_name,
			COALESCE(o.tenant_id,0) as tenant_id,
			COALESCE(p.name,'') as package_name,
			o.amount_cents, o.status, COALESCE(o.manual_confirm,false) as manual_confirm,
			o.created_at, o.paid_at`).
		Joins("LEFT JOIN tenants t ON o.tenant_id = t.id").
		Joins("LEFT JOIN packages p ON o.package_id = p.id").
		Where("o.status = 'pending' AND COALESCE(o.manual_confirm,false) = true").
		Order("o.created_at ASC").Limit(200).Scan(&rows).Error
	if err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "查询失败")
		return
	}
	RespOK(c, "", rows)
}

// SuperConfirmOrder POST /api/v1/super/orders/:id/confirm —— 超管确认到账
// 幂等：重复 confirm 命中 MarkOrderPaid RowsAffected=0，不二次发放
func SuperConfirmOrder(c *gin.Context) {
	// 健壮性收口(2026-09-05)：ID 入口校验，非法不再触 DB（超管跨租户，无需租户锚）
	oid, ok := PathUintID(c)
	if !ok {
		return
	}
	var order model.BillingOrder
	if err := db.DB.First(&order, oid).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "订单不存在")
		return
	}
	if order.Channel == "" || order.Channel == "mock" {
		RespErr(c, http.StatusBadRequest, 400, "模拟渠道订单无需人工确认")
		return
	}

	confirmed, err := confirmAndGrant(c, &order, "manual")
	if err != nil {
		RespErr(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	db.DB.First(&order, order.ID)
	msg := map[bool]string{true: "已确认到账，权益发放完成", false: "订单此前已确认过，未重复发放"}[confirmed]
	RespOK(c, msg, order)
}

// SuperRefundOrder POST /api/v1/super/billing/orders/:id/refund —— 超管执行退款（B7 双轨资金落点）
// 租户侧 refund 在非 mock 模式仅受理申请；实际 clawback+PSP 出款必须经此端点（平台审批位）。
func SuperRefundOrder(c *gin.Context) {
	oid, ok := PathUintID(c)
	if !ok {
		return
	}
	var order model.BillingOrder
	if err := db.DB.First(&order, oid).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "订单不存在")
		return
	}
	o, flowed, err := billing.MarkOrderRefunded(order.ID)
	if err != nil {
		if errors.Is(err, billing.ErrRefundNoRemaining) {
			RespErr(c, http.StatusConflict, int(CodeBizErr), err.Error())
			return
		}
		RespErr(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	if !flowed {
		RespErr(c, http.StatusConflict, int(CodeBizErr), "订单当前状态不可退款或已退过")
		return
	}
	tid := uint(0)
	if o.TenantID != nil {
		tid = *o.TenantID
	}
	writeOrderAudit(c, tid, "super_order_refund", o)
	RespOK(c, "退款执行完成（权益已回收）", o)
}

// ============================================================
// §W 发票管理（平台侧，2026-09-14）：资质未到位，走"租户申请→超管人工开具→回录发票号"极限闭环。
// ============================================================

// SuperListInvoices GET /api/v1/super/invoices?status=requested
// 跨租户发票申请列表（默认看全部，status=requested 为待开具工作队列）。
func SuperListInvoices(c *gin.Context) {
	q := db.DB.Model(&model.BillingOrder{}).Where("invoice_status IS NOT NULL AND invoice_status <> ''")
	if s := c.Query("status"); s != "" {
		q = q.Where("invoice_status = ?", s)
	}
	var rows []model.BillingOrder
	if err := q.Order("id DESC").Limit(200).Find(&rows).Error; err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "查询失败")
		return
	}
	list := make([]gin.H, 0, len(rows))
	for _, o := range rows {
		list = append(list, gin.H{
			"order_id": o.ID, "tenant_id": o.TenantID, "amount_cents": o.AmountCents,
			"invoice_status": o.InvoiceStatus, "invoice_title": o.InvoiceTitle,
			"invoice_tax_no": o.InvoiceTaxNo, "invoice_email": o.InvoiceEmail,
			"invoice_no": o.InvoiceNo,
		})
	}
	RespOK(c, "ok", gin.H{"list": list, "total": len(list)})
}

// SuperIssueInvoice POST /api/v1/super/invoices/:order_id/issue {invoice_no}
// 人工开票后回录发票号：requested→issued。
func SuperIssueInvoice(c *gin.Context) {
	oid, ok := PathUintID(c)
	if !ok {
		return
	}
	var body struct {
		InvoiceNo string `json:"invoice_no"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.InvoiceNo) == "" {
		RespErr(c, http.StatusBadRequest, 400, "invoice_no 必填")
		return
	}
	o, err := billing.IssueInvoice(oid, strings.TrimSpace(body.InvoiceNo))
	if err != nil {
		RespErr(c, http.StatusBadRequest, int(CodeBizErr), err.Error())
		return
	}
	writeOrderAudit(c, 0, "super_invoice_issue", o)
	RespOK(c, "发票已开具", o)
}

// SuperVoidInvoice POST /api/v1/super/invoices/:order_id/void
// 作废发票（开错/退票）：→voided，租户可重新申请。
func SuperVoidInvoice(c *gin.Context) {
	oid, ok := PathUintID(c)
	if !ok {
		return
	}
	o, err := billing.VoidInvoice(oid)
	if err != nil {
		RespErr(c, http.StatusBadRequest, int(CodeBizErr), err.Error())
		return
	}
	writeOrderAudit(c, 0, "super_invoice_void", o)
	RespOK(c, "发票已作废", o)
}

// confirmAndGrant 确认到账统一落点：幂等改单 → 发放 → 审计 → MQ payment 事件
// 返回 confirmed=false 表示订单早已流转（调用方提示幂等命中即可）
// R5 修复(2026-09-11)：manual 通道遇超时关单时走"迟到到账复活"——真实银行/渠道到账
// 慢于 24h 关单属正常业务场景，此前该单钱货两失且无人工补救入口（mock 通道在 Reopen 内拒）
func confirmAndGrant(c *gin.Context, order *model.BillingOrder, channel string) (bool, error) {
	fresh, confirmed, err := billing.MarkOrderPaid(order.ID, channel)
	if err != nil {
		return false, err
	}
	if !confirmed && channel == "manual" {
		if reopened, ok, rerr := billing.ReopenClosedOrderPaid(order.ID, channel); rerr == nil && ok {
			fresh, confirmed = reopened, true
		}
	}
	if !confirmed {
		return false, nil // 幂等命中：不二次发放、不发重复事件
	}
	if fresh.TenantID == nil {
		return true, fmt.Errorf("订单%d 无租户归属，无法发放", fresh.ID)
	}
	if err := billing.GrantOrderEntitlement(nil, fresh); err != nil {
		// 发放失败必须显式暴露：钱已收权益未发，超管需看到错误重试
		log.Printf("[Billing][ERROR] 订单%s 已到账但发放失败: %v", fresh.OrderNo, err)
		return true, fmt.Errorf("权益发放失败请联系平台核查: %w", err)
	}
	action := "order_paid_confirm"
	if channel == "mock" {
		action = "order_paid_mock"
	}
	writeOrderAudit(c, *fresh.TenantID, action, fresh)
	billing.PublishPaymentEvent(fresh)
	return true, nil
}

// BillingWebhook POST /api/v1/billing/webhook/:channel —— 支付网关异步回调（无需鉴权）
// 校验签名 → 定位订单 → 幂等到账 → 发放权益。PSP 到账后由服务端到服务端调用，
// 故不可依赖登录态；安全性来自渠道验签。
//
// P0-1b(2026-09-15)：按渠道分发验签协议——
//   wechat：微信支付 V3 回调（JSON body resource 字段 AES-256-GCM 解密，APIv3Key 为解密凭证；
//           Wechatpay-Timestamp ±5min 时间窗 + Wechatpay-Nonce 防重放）。
//   alipay：支付宝异步通知（form 参数 RSA2 平台公钥全参数验签，notify_id 防重放）。
//   其它（mock/gateway/自定义聚合台）：既有 HMAC-SHA256 V2 通用验签（C6 口径不变）。
//
// 各渠道最终都汇入 ConfirmOrderByChannel（渠道互验 L3 + MarkOrderPaid 幂等 + 台账先行发放），
// 协议差异只体现在"如何拿到可信的 orderNo/到账状态"，不触碰资金安全语义。
func BillingWebhook(c *gin.Context) {
	channel := c.Param("channel")
	switch channel {
	case "wechat":
		billingWebhookWechat(c)
	case "alipay":
		billingWebhookAlipay(c)
	default:
		billingWebhookGateway(c, channel)
	}
}

// billingWebhookWechat 微信支付 V3 回调分支。
// 报文：{resource:{ciphertext, nonce, associated_data}}，HTTP 头 Wechatpay-Timestamp/Wechatpay-Nonce。
func billingWebhookWechat(c *gin.Context) {
	// 时间窗：Wechatpay-Timestamp 为 unix 秒，±5min（复用 C6 窗口校验）
	if !billing.WebhookTimestampFresh(c.GetHeader("Wechatpay-Timestamp")) {
		RespErr(c, http.StatusForbidden, 403, "回调时间戳超出有效窗口（±5分钟）")
		return
	}
	nonceHdr := c.GetHeader("Wechatpay-Nonce")
	if nonceHdr == "" || billing.WebhookNonceSeen(nonceHdr) {
		RespErr(c, http.StatusConflict, int(CodeBizErr), "重复回调（nonce 已消费）")
		return
	}
	var body struct {
		Resource struct {
			Ciphertext      string `json:"ciphertext"`
			Nonce           string `json:"nonce"`
			AssociatedData  string `json:"associated_data"`
		} `json:"resource"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Resource.Ciphertext == "" {
		RespErr(c, http.StatusBadRequest, 400, "回调参数错误（缺少 resource.ciphertext）")
		return
	}
	apiv3Key := getPayConf("pay_wechat_apiv3_key", "PAY_WECHAT_APIV3_KEY")
	orderNo, tradeState, err := billing.DecryptWechatResource(apiv3Key, body.Resource.Ciphertext, body.Resource.Nonce, body.Resource.AssociatedData)
	if err != nil {
		RespErr(c, http.StatusForbidden, 403, err.Error())
		return
	}
	if orderNo == "" {
		RespErr(c, http.StatusBadRequest, 400, "回调明文缺少 out_trade_no")
		return
	}
	if tradeState != "SUCCESS" {
		RespOK(c, "非成功状态，忽略", gin.H{"trade_state": tradeState})
		return
	}
	order, flowed, err := billing.ConfirmOrderByChannel(orderNo, "wechat")
	if err != nil {
		RespErr(c, http.StatusNotFound, 404, err.Error())
		return
	}
	// 微信要求成功应答 {code:"SUCCESS"}，非成功应答会被重推；本系统统一信封 + 200 亦满足"2xx 即成功"语义
	RespOK(c, map[bool]string{true: "到账成功，权益已发放", false: "订单此前已处理"}[flowed], gin.H{"order_no": order.OrderNo, "flowed": flowed})
}

// billingWebhookAlipay 支付宝异步通知分支（form 表单）。
// 验签：全参数（排除 sign/sign_type/空值）字典序拼串 → 支付宝平台公钥 RSA2 验签。
func billingWebhookAlipay(c *gin.Context) {
	if err := c.Request.ParseForm(); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "通知 form 解析失败")
		return
	}
	params := map[string]string{}
	for k := range c.Request.PostForm {
		params[k] = c.Request.PostForm.Get(k)
	}
	// 防重放：notify_id 为支付宝通知唯一标识
	if params["notify_id"] != "" && billing.WebhookNonceSeen(params["notify_id"]) {
		RespErr(c, http.StatusConflict, int(CodeBizErr), "重复回调（notify_id 已消费）")
		return
	}
	pubPEM := getPayConf("pay_alipay_public_key", "PAY_ALIPAY_PUBLIC_KEY")
	if pubPEM == "" {
		RespErr(c, http.StatusServiceUnavailable, 503, "支付宝平台公钥未配置（pay_alipay_public_key），无法验签")
		return
	}
	pub, err := billing.ParseRSAPublicKey([]byte(pubPEM))
	if err != nil {
		RespErr(c, http.StatusServiceUnavailable, 503, "支付宝平台公钥解析失败: "+err.Error())
		return
	}
	orderNo, tradeStatus, err := billing.VerifyAlipayNotify(pub, params)
	if err != nil {
		RespErr(c, http.StatusForbidden, 403, err.Error())
		return
	}
	if orderNo == "" {
		RespErr(c, http.StatusBadRequest, 400, "通知缺少 out_trade_no")
		return
	}
	if tradeStatus != "TRADE_SUCCESS" && tradeStatus != "TRADE_FINISHED" {
		RespOK(c, "非成功状态，忽略", gin.H{"trade_status": tradeStatus})
		return
	}
	order, flowed, cerr := billing.ConfirmOrderByChannel(orderNo, "alipay")
	if cerr != nil {
		RespErr(c, http.StatusNotFound, 404, cerr.Error())
		return
	}
	RespOK(c, map[bool]string{true: "到账成功，权益已发放", false: "订单此前已处理"}[flowed], gin.H{"order_no": order.OrderNo, "flowed": flowed})
}

// getPayConf 平台配置读取（系统层优先，env 兜底）——webhook 侧与 billing 包 getPlatformConf 同款语义。
func getPayConf(sysKey, envKey string) string {
	if runtimecfg.DefaultSystemConfigService != nil {
		if v := runtimecfg.DefaultSystemConfigService.GetString(sysKey, ""); v != "" {
			return v
		}
	}
	return os.Getenv(envKey)
}

// billingWebhookGateway 既有通用 HMAC V2 验签分支（mock/gateway/自定义聚合台，C6 口径不变）。
func billingWebhookGateway(c *gin.Context, channel string) {
	var cb struct {
		OutTradeNo  string `json:"out_trade_no"`
		OrderNo     string `json:"order_no"`
		TradeStatus string `json:"trade_status"` // TRADE_SUCCESS / SUCCESS
		Timestamp   string `json:"timestamp"`    // C6：unix 秒（参与签名，±5min 窗口）
		Nonce       string `json:"nonce"`        // C6：随机串（防重放，Redis/内存去重）
		Sign        string `json:"sign"`
	}
	if err := c.ShouldBindJSON(&cb); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "回调参数错误")
		return
	}
	orderNo := cb.OrderNo
	if orderNo == "" {
		orderNo = cb.OutTradeNo
	}
	if orderNo == "" {
		RespErr(c, http.StatusBadRequest, 400, "缺少订单号")
		return
	}
	// 网关签名密钥（与 CreatePayment 对称）
	key := getPayConf("pay_gateway_key", "PAY_GATEWAY_KEY")
	// §W 极限收口(2026-09-14)：未配置 pay_gateway_key 时，仅"mock 渠道 + 非 release 模式"
	// 允许固定开发密钥（本地/E2E 模拟 PSP 到账）；release 一律 503——不给任何渠道开假到账后门。
	if key == "" {
		if channel == "mock" && gin.Mode() != gin.ReleaseMode {
			key = "mock-webhook-dev-key"
		} else {
			RespErr(c, http.StatusServiceUnavailable, 503, "支付网关密钥未配置")
			return
		}
	}
	// C6 升级(2026-09-14)：验签改 V2 口径（timestamp+nonce 参与签名）+ 时间窗 + nonce 去重。
	// 旧口径 HMAC(order_no|status) 已删除，杜绝无限重放。
	if !billing.WebhookTimestampFresh(cb.Timestamp) {
		RespErr(c, http.StatusForbidden, 403, "回调时间戳超出有效窗口（±5分钟）")
		return
	}
	if !billing.VerifyGatewaySignV2(key, orderNo, cb.TradeStatus, cb.Timestamp, cb.Nonce, cb.Sign) {
		RespErr(c, http.StatusForbidden, 403, "签名校验失败")
		return
	}
	if billing.WebhookNonceSeen(cb.Nonce) {
		RespErr(c, http.StatusConflict, int(CodeBizErr), "重复回调（nonce 已消费）")
		return
	}
	if cb.TradeStatus != "TRADE_SUCCESS" && cb.TradeStatus != "SUCCESS" {
		RespOK(c, "非成功状态，忽略", nil)
		return
	}
	order, flowed, err := billing.ConfirmOrderByChannel(orderNo, channel)
	if err != nil {
		RespErr(c, http.StatusNotFound, 404, err.Error())
		return
	}
	RespOK(c, map[bool]string{true: "到账成功，权益已发放", false: "订单此前已处理"}[flowed], gin.H{"order_no": order.OrderNo, "flowed": flowed})
}

// SuperMockWebhook POST /api/v1/super/billing/orders/:id/mock-webhook —— §W 测试资产：
// 超管代发一次"网关到账回调"（内部直调 ConfirmOrderByChannel，幂等语义与真回调一致）。
// 双重闸门：非 release 模式 + 订单渠道必须为 mock——生产环境任何情况不可用。
func SuperMockWebhook(c *gin.Context) {
	if gin.Mode() == gin.ReleaseMode {
		RespErr(c, http.StatusForbidden, 403, "生产环境禁用模拟回调")
		return
	}
	oid, ok := PathUintID(c)
	if !ok {
		return
	}
	var order model.BillingOrder
	if err := db.DB.First(&order, oid).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "订单不存在")
		return
	}
	if order.Channel != "mock" {
		RespErr(c, http.StatusConflict, int(CodeBizErr), "仅 mock 渠道订单可模拟回调")
		return
	}
	updated, flowed, err := billing.ConfirmOrderByChannel(order.OrderNo, "mock")
	if err != nil {
		RespErr(c, http.StatusNotFound, 404, err.Error())
		return
	}
	RespOK(c, map[bool]string{true: "模拟到账成功", false: "订单此前已处理"}[flowed], gin.H{"order_no": updated.OrderNo, "flowed": flowed})
}

// RefundOrder POST /api/v1/billing/orders/:id/refund —— 退款（幂等）
// B7 双轨(2026-09-14)：租户管理员自助退本租户订单=无平台审批的资金治理缺口。
//   - pay_mode=mock（开发/UAT）：直接执行退款（保留既有灰度语义，与 mock-pay 放行一致）
//   - pay_mode=static_qr/sdk（生产，真钱场景）：降级为"退款申请"（refund_requested=true + 审计），
//     实际 clawback+出款由超管 POST /super/billing/orders/:id/refund 执行
func RefundOrder(c *gin.Context) {
	tid := tenantIDOf(c)
	// 健壮性收口(2026-09-05)：ID 入口校验，非法不再触 DB
	oid, ok := PathUintID(c)
	if !ok {
		return
	}
	var order model.BillingOrder
	if err := db.DB.Where("id = ? AND tenant_id = ?", oid, tid).First(&order).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "订单不存在")
		return
	}
	// B7：非 mock 模式 → 只受理申请，不动资金
	if billing.GetPayMode() != "mock" {
		if order.Status != "paid" {
			RespErr(c, http.StatusConflict, int(CodeBizErr), "订单当前状态不可申请退款")
			return
		}
		if order.RefundRequested {
			RespOK(c, "退款申请已提交，请等待平台处理", order)
			return
		}
		if err := db.DB.Model(&model.BillingOrder{}).Where("id = ?", order.ID).
			Update("refund_requested", true).Error; err != nil {
			RespErr(c, http.StatusInternalServerError, 500, "提交失败")
			return
		}
		order.RefundRequested = true
		writeOrderAudit(c, tid, "order_refund_request", &order)
		RespOK(c, "退款申请已提交，平台核实后处理（通常1个工作日）", order)
		return
	}
	o, flowed, err := billing.MarkOrderRefunded(order.ID)
	if err != nil {
		// 已全部消耗无剩余可退：409 明确拒绝而非 500（退款语义对齐 2026-09-08）
		if errors.Is(err, billing.ErrRefundNoRemaining) {
			RespErr(c, http.StatusConflict, int(CodeBizErr), err.Error())
			return
		}
		RespErr(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	writeOrderAudit(c, tid, "order_refund", o)
	// 毛边3修复(2026-09-05)：此前 flowed=false 也返回 code=0（成功码配失败语义），
	// 对接方易误判为退款成功；改为 409 + 业务错误码，明确"状态机拒绝"
	if !flowed {
		RespErr(c, http.StatusConflict, int(CodeBizErr), "订单当前状态不可退款或已退过")
		return
	}
	RespOK(c, "退款成功（已支付→已退款）", o)
}

// RequestInvoice POST /api/v1/billing/orders/:id/invoice —— 申请发票
func RequestInvoice(c *gin.Context) {
	tid := tenantIDOf(c)
	// 健壮性收口(2026-09-05)：ID 入口校验，非法不再触 DB
	oid, ok := PathUintID(c)
	if !ok {
		return
	}
	var order model.BillingOrder
	if err := db.DB.Where("id = ? AND tenant_id = ?", oid, tid).First(&order).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "订单不存在")
		return
	}
	// §W 发票极限：抬头/税号/邮箱由租户随申请提交（旧实现前端硬编码、无税号位）
	var body struct {
		Title string `json:"title"`
		TaxNo string `json:"tax_no"`
		Email string `json:"email"`
	}
	_ = c.ShouldBindJSON(&body)
	o, err := billing.RequestInvoice(order.ID, strings.TrimSpace(body.Title), strings.TrimSpace(body.TaxNo), strings.TrimSpace(body.Email))
	if err != nil {
		RespErr(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	writeOrderAudit(c, tid, "order_invoice_request", o)
	RespOK(c, "发票申请已提交", o)
}

// writeOrderAudit 订单链路审计（异步写，失败不影响主流程）
// 修复：goroutine 内不读 gin context（handler 返回后 context 可能被回收），
// 改为在调用时提取所有值，goroutine 内仅用捕获的变量。
func writeOrderAudit(c *gin.Context, tenantID uint, action string, order *model.BillingOrder) {
	uidV, _ := c.Get("user_id")
	clientIP := c.ClientIP()
	userAgent := c.Request.UserAgent()
	detail := fmt.Sprintf(`{"order_no":"%s","amount_cents":%d,"channel":"%s","status":"%s","package_id":%d}`,
		order.OrderNo, order.AmountCents, order.Channel, order.Status, order.PackageID)
	go func() {
		defer func() { _ = recover() }()
		db.DB.Create(&model.TenantAuditLog{
			TenantID: tenantID, UserID: toUintSafe(uidV), Action: action,
			Resource: "billing_order:" + order.OrderNo, Detail: detail,
			IP: clientIP, UserAgent: userAgent,
		})
	}()
}
