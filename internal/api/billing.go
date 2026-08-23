package api

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/service"

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

type createOrderReq struct {
	PackageID uint `json:"package_id" binding:"required"`
}

// CreateBillingOrder POST /api/v1/billing/orders
func CreateBillingOrder(c *gin.Context) {
	var req createOrderReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误：package_id 必填"})
		return
	}
	tid := tenantIDOf(c)

	var pkg model.Package
	if err := db.DB.Where("id = ? AND enabled = ?", req.PackageID, true).First(&pkg).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "商业包不存在或已下架"})
		return
	}
	if pkg.PType == model.PackageTypeFree {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "试用包无需购买，注册后自动发放"})
		return
	}

	order, err := service.CreateOrderForPackage(tid, &pkg)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
		return
	}
	writeOrderAudit(c, tid, "order_create", order)
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": order})
}

// GetBillingOrder GET /api/v1/billing/orders/:id —— 收银台轮询
func GetBillingOrder(c *gin.Context) {
	tid := tenantIDOf(c)
	var order model.BillingOrder
	if err := db.DB.Where("id = ? AND tenant_id = ?", c.Param("id"), tid).First(&order).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "订单不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": order})
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
	if err := db.DB.Where("tenant_id = ?", tid).
		Select("id, order_no, package_id, amount_cents, period, channel, status,"+
			"manual_confirm, paid_at, created_at").
		Order("id DESC").Limit(limit).Find(&orders).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "查询失败"})
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
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": rows})
}

// MockPayOrder POST /api/v1/billing/orders/mock-pay {order_id}
// 仅 pay_mode=mock 可用；生产环境 403（双保险之接口侧）
func MockPayOrder(c *gin.Context) {
	if service.GetPayMode() != "mock" {
		c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "非模拟模式禁止模拟支付"})
		return
	}
	var req struct {
		OrderID uint `json:"order_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误：order_id 必填"})
		return
	}
	tid := tenantIDOf(c)
	var order model.BillingOrder
	if err := db.DB.Where("id = ? AND tenant_id = ?", req.OrderID, tid).First(&order).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "订单不存在"})
		return
	}

	confirmed, err := confirmAndGrant(c, &order, "mock")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
		return
	}
	db.DB.First(&order, order.ID)
	msg := map[bool]string{true: "模拟到账成功，权益已发放", false: "订单已处理过，勿重复操作"}[confirmed]
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": msg, "data": gin.H{"order": order, "granted": confirmed}})
}

// ManualConfirmPaid POST /api/v1/billing/manual-confirm {order_id}
// static_qr 模式下租户点「我已付费」：置 manual_confirm 标记 → critical 审计 + 企微催告超管
// 权益不在此刻发放，必须等超管核实到账后 confirm（防白嫖）
func ManualConfirmPaid(c *gin.Context) {
	var req struct {
		OrderID uint `json:"order_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误：order_id 必填"})
		return
	}
	tid := tenantIDOf(c)
	var order model.BillingOrder
	if err := db.DB.Where("id = ? AND tenant_id = ?", req.OrderID, tid).First(&order).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "订单不存在"})
		return
	}
	if order.Status == "paid" {
		c.JSON(http.StatusOK, gin.H{"code": 0, "message": "订单已确认到账", "data": order})
		return
	}

	res := db.DB.Model(&model.BillingOrder{}).Where("id = ?", order.ID).
		Update("manual_confirm", true)
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "标记失败"})
		return
	}

	// critical 审计 + 企微/邮件通道催告超管（通知通道抽象见 service/notifier.go）
	var tName string
	db.DB.Model(&model.Tenant{}).Select("name").Where("id = ?", tid).Scan(&tName)
	writeOrderAudit(c, tid, "order_manual_confirm_critical", &order)
	service.NotifyManualConfirmPaid(order.OrderNo, tName, order.AmountCents)

	order.ManualConfirm = true
	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "已提交，平台核实收款后将自动开通（通常10分钟内）",
		"data":    order,
	})
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
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": rows})
}

// SuperConfirmOrder POST /api/v1/super/orders/:id/confirm —— 超管确认到账
// 幂等：重复 confirm 命中 MarkOrderPaid RowsAffected=0，不二次发放
func SuperConfirmOrder(c *gin.Context) {
	var order model.BillingOrder
	if err := db.DB.First(&order, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "订单不存在"})
		return
	}
	if order.Channel == "" || order.Channel == "mock" {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "模拟渠道订单无需人工确认"})
		return
	}

	confirmed, err := confirmAndGrant(c, &order, "manual")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
		return
	}
	db.DB.First(&order, order.ID)
	msg := map[bool]string{true: "已确认到账，权益发放完成", false: "订单此前已确认过，未重复发放"}[confirmed]
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": msg, "data": order})
}

// confirmAndGrant 确认到账统一落点：幂等改单 → 发放 → 审计 → MQ payment 事件
// 返回 confirmed=false 表示订单早已流转（调用方提示幂等命中即可）
func confirmAndGrant(c *gin.Context, order *model.BillingOrder, channel string) (bool, error) {
	fresh, confirmed, err := service.MarkOrderPaid(order.ID, channel)
	if err != nil {
		return false, err
	}
	if !confirmed {
		return false, nil // 幂等命中：不二次发放、不发重复事件
	}
	if fresh.TenantID == nil {
		return true, fmt.Errorf("订单%d 无租户归属，无法发放", fresh.ID)
	}
	if err := service.GrantOrderEntitlement(nil, fresh); err != nil {
		// 发放失败必须显式暴露：钱已收权益未发，超管需看到错误重试
		log.Printf("[Billing][ERROR] 订单%s 已到账但发放失败: %v", fresh.OrderNo, err)
		return true, fmt.Errorf("权益发放失败请联系平台核查: %w", err)
	}
	action := "order_paid_confirm"
	if channel == "mock" {
		action = "order_paid_mock"
	}
	writeOrderAudit(c, *fresh.TenantID, action, fresh)
	service.PublishPaymentEvent(fresh)
	return true, nil
}

// writeOrderAudit 订单链路审计（异步写，失败不影响主流程）
func writeOrderAudit(c *gin.Context, tenantID uint, action string, order *model.BillingOrder) {
	uidV, _ := c.Get("user_id")
	detail := fmt.Sprintf(`{"order_no":"%s","amount_cents":%d,"channel":"%s","status":"%s","package_id":%d}`,
		order.OrderNo, order.AmountCents, order.Channel, order.Status, order.PackageID)
	go func() {
		defer func() { _ = recover() }()
		db.DB.Create(&model.TenantAuditLog{
			TenantID: tenantID, UserID: toUintSafe(uidV), Action: action,
			Resource: "billing_order:" + order.OrderNo, Detail: detail,
			IP: c.ClientIP(), UserAgent: c.Request.UserAgent(),
		})
	}()
}
