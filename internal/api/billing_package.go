package api

import (
	"net/http"

	"ai-scrm/internal/db"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
	"ai-scrm/internal/service"

	"github.com/gin-gonic/gin"
)

// ============================================================
// AI 商业包 API（商业化第一批 M2，2026-08-23）
//
//   GET  /api/v1/packages              公开定价页数据（读 packages 表）
//   POST /api/v1/billing/subscribe     订阅：free 直发；paid/increment 走 M1 下单
//   GET  /api/v1/billing/my-package    当前包+剩余额度（前端顶栏展示）
//   CRUD /api/v1/super/packages        超管商业包管理 + 启停
// ============================================================

// tenantIDOf 生效租户读取（中间件链已裁决）
func tenantIDOf(c *gin.Context) uint {
	return middleware.EffectiveTenantID(c)
}

// ListPackages GET /api/v1/packages （免登录公开定价）
func ListPackages(c *gin.Context) {
	var pkgs []model.Package
	if err := db.DB.Where("enabled = ?", true).Order("sort_order ASC, id ASC").Find(&pkgs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": pkgs})
}

// SubscribePackage POST /api/v1/billing/subscribe {package_id}
// free 直发（幂等由发放语义保证：试用包叠加月配额）；paid/increment 创建订单走收银台
func SubscribePackage(c *gin.Context) {
	var req struct {
		PackageID uint `json:"package_id" binding:"required"`
	}
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
		if err := service.GrantPackage(nil, tid, &pkg); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "发放失败：" + err.Error()})
			return
		}
		writeOrderAudit(c, tid, "package_grant_free", &model.BillingOrder{PackageID: pkg.ID})
		c.JSON(http.StatusOK, gin.H{"code": 0, "message": "试用包已到账", "data": gin.H{"granted": true, "package": pkg}})
		return
	}

	// paid/increment：创建订单进收银台流程
	order, err := service.CreateOrderForPackage(tid, &pkg)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
		return
	}
	writeOrderAudit(c, tid, "order_create", order)
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "订单已创建，请完成支付", "data": gin.H{"order": order, "pay_mode": service.GetPayMode()}})
}

// MyPackage GET /api/v1/billing/my-package —— 当前包+剩余额度（顶栏展示）
func MyPackage(c *gin.Context) {
	tid := tenantIDOf(c)

	var t model.Tenant
	if err := db.DB.Select("id, name, status, expired_at, max_ai_calls_monthly, used_ai_calls, ai_call_balance").
		First(&t, tid).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "租户不存在"})
		return
	}

	used, maxMonthly, balance, _ := service.GetTenantQuotaView(tid)
	enforced := true
	if service.DefaultSystemConfigService != nil {
		enforced = service.DefaultSystemConfigService.GetBool("billing_enforced", false)
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{
		"tenant_name":      t.Name,
		"status":           t.Status,
		"expired_at":       t.ExpiredAt,
		"max_ai_calls":     maxMonthly,
		"used_ai_calls":    used,
		"ai_call_balance":  balance, // 增量包买断余额（不随月重置）
		"billing_enforced": enforced,
		"pay_mode":         service.GetPayMode(),
	}})
}

// ---- 超管商业包管理 ----

type pkgUpsertReq struct {
	Code         string `json:"code" binding:"required"`
	Name         string `json:"name" binding:"required"`
	PType        string `json:"p_type" binding:"required"`
	AICalls      int    `json:"ai_calls"`
	PriceCents   int    `json:"price_cents"`
	DurationDays int    `json:"duration_days"`
	Description  string `json:"description"`
	Enabled      *bool  `json:"enabled"`
	SortOrder    int    `json:"sort_order"`
}

var validPkgTypes = map[string]bool{model.PackageTypeFree: true, model.PackageTypePaid: true, model.PackageTypeIncrement: true}

// SuperPackageList GET /api/v1/super/packages —— 全量列表（含下架）
func SuperPackageList(c *gin.Context) {
	var pkgs []model.Package
	db.DB.Order("sort_order ASC, id ASC").Find(&pkgs)
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": pkgs})
}

// SuperPackageCreate POST /api/v1/super/packages
func SuperPackageCreate(c *gin.Context) {
	var req pkgUpsertReq
	if err := c.ShouldBindJSON(&req); err != nil || !validPkgTypes[req.PType] {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误：code/name/p_type 必填且类型合法"})
		return
	}
	pkg := model.Package{
		Code: req.Code, Name: req.Name, PType: req.PType,
		AICalls: req.AICalls, PriceCents: req.PriceCents, DurationDays: req.DurationDays,
		Description: req.Description, Enabled: true, SortOrder: req.SortOrder,
	}
	if req.Enabled != nil {
		pkg.Enabled = *req.Enabled
	}
	if err := db.DB.Create(&pkg).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "创建失败（code 重复?）：" + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": pkg})
}

// SuperPackageUpdate PUT /api/v1/super/packages/:id
// 修复：改用指针可选字段——启停场景只传 {enabled}，原 required 绑定会直接400
func SuperPackageUpdate(c *gin.Context) {
	var pkg model.Package
	if err := db.DB.First(&pkg, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "商业包不存在"})
		return
	}
	var req struct {
		Name         *string `json:"name"`
		PType        *string `json:"p_type"`
		AICalls      *int    `json:"ai_calls"`
		PriceCents   *int    `json:"price_cents"`
		DurationDays *int    `json:"duration_days"`
		Description  *string `json:"description"`
		Enabled      *bool   `json:"enabled"`
		SortOrder    *int    `json:"sort_order"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误"})
		return
	}
	updates := map[string]interface{}{}
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.PType != nil {
		if !validPkgTypes[*req.PType] {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "非法包类型"})
			return
		}
		updates["p_type"] = *req.PType
	}
	if req.AICalls != nil {
		updates["ai_calls"] = *req.AICalls
	}
	if req.PriceCents != nil {
		updates["price_cents"] = *req.PriceCents
	}
	if req.DurationDays != nil {
		updates["duration_days"] = *req.DurationDays
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled // 启停即时生效（定价页/订阅入口同步过滤）
	}
	if req.SortOrder != nil {
		updates["sort_order"] = *req.SortOrder
	}
	if len(updates) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "无可更新字段"})
		return
	}
	if err := db.DB.Model(&pkg).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "更新失败"})
		return
	}
	db.DB.First(&pkg, pkg.ID)
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": pkg})
}

// SuperPackageDelete DELETE /api/v1/super/packages/:id
// 已有订单引用时软处理：仅下架不物理删（订单审计完整性优先）
func SuperPackageDelete(c *gin.Context) {
	var pkg model.Package
	if err := db.DB.First(&pkg, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "商业包不存在"})
		return
	}
	var refCount int64
	db.DB.Model(&model.BillingOrder{}).Where("package_id = ?", pkg.ID).Count(&refCount)
	if refCount > 0 {
		db.DB.Model(&pkg).Update("enabled", false)
		c.JSON(http.StatusOK, gin.H{"code": 0, "message": "该包已有订单引用，已改为下架（保留审计）"})
		return
	}
	db.DB.Delete(&pkg)
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "已删除"})
}
