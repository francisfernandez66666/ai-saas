package api

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"

	"ai-scrm/internal/service"
	"github.com/gin-gonic/gin"
)

// ============================================================
// 超管后台 API（平台运营）
// 仅 super_admin 可访问；操作全部写 tenant_audit_logs 审计
// ============================================================

// SuperRequired 平台超管守卫（区别于 AdminRequired 的租户管理员放行）
func SuperRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		if role, ok := c.Get("role"); ok {
			if r, _ := role.(string); r == model.RoleSuperAdmin {
				c.Next()
				return
			}
		}
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"code": 403, "message": "需要平台超管权限", "data": nil,
		})
	}
}

// SuperTenantList GET /api/v1/super/tenants —— 全平台租户列表（含用量与套餐）
func SuperTenantList(c *gin.Context) {
	// row 全平台租户列表聚合行：LEFT JOIN subscription_plans 带出套餐名，便于超管运营视图
	type row struct {
		ID            uint    `json:"id"`
		Name          string  `json:"name"`
		Code          string  `json:"code"`
		Tier          string  `json:"tier"`
		Status        string  `json:"status"`
		PlanName      *string `json:"plan_name"`
		UsedCustomers int     `json:"used_customers"`
		MaxCustomers  int     `json:"max_customers"`
		CreatedAt     string  `json:"created_at"`
	}
	rows := []row{}
	err := db.DB.Table("tenants t").
		Select(`t.id, t.name, t.code, t.tier, t.status,
			COALESCE(p.name,'') as plan_name,
			t.used_customers, t.max_customers,
			TO_CHAR(t.created_at,'YYYY-MM-DD') as created_at`).
		Joins("LEFT JOIN subscription_plans p ON t.plan_id = p.id").
		Order("t.id ASC").Limit(500).Scan(&rows).Error
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": rows})
}

// SuperTenantStatus PUT /api/v1/super/tenants/:id/status {status}
// 合法流转：active↔suspended；expired/cancelled 仅超管手动干预
func SuperTenantStatus(c *gin.Context) {
	var req struct {
		Status string `json:"status" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"code": 400, "message": "参数错误"})
		return
	}
	valid := map[string]bool{"active": true, "suspended": true, "expired": true, "cancelled": true, "trial": true}
	if !valid[req.Status] {
		c.JSON(400, gin.H{"code": 400, "message": "非法状态"})
		return
	}
	id := c.Param("id")
	res := db.DB.Model(&model.Tenant{}).Where("id = ?", id).Update("status", req.Status)
	if res.Error != nil || res.RowsAffected == 0 {
		c.JSON(404, gin.H{"code": 404, "message": "租户不存在"})
		return
	}

	uidV, _ := c.Get("user_id")
	db.DB.Create(&model.TenantAuditLog{
		TenantID: 1, UserID: toUintSafe(uidV), Action: "super_tenant_status",
		Resource: "tenant:" + id, Detail: `{"to":"` + req.Status + `"}`,
		IP: c.ClientIP(), UserAgent: c.Request.UserAgent(),
	})
	// 租户解析缓存失效，封禁即时生效
	middleware.InvalidateTenantCache()
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "状态已更新为 " + req.Status})
}

// SuperGrantTrial POST /api/v1/super/tenants/:id/grant-trial —— 审核模式放行（M1）
// 幂等：已存在 trial_granted 审计记录的租户拒绝重复发放；review/trial/suspended → trial
func SuperGrantTrial(c *gin.Context) {
	var t model.Tenant
	if err := db.DB.First(&t, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "租户不存在"})
		return
	}
	var granted int64
	db.DB.Model(&model.TenantAuditLog{}).
		Where("tenant_id = ? AND action = ?", t.ID, "trial_granted").
		Count(&granted)
	if granted > 0 {
		c.JSON(http.StatusConflict, gin.H{"code": 409, "message": "该租户已发放过试用额度（幂等拦截）"})
		return
	}
	// P1.5(2026-08-26)：换用幂等的 GrantTrialBucket（双唯一防撞库；
	// 非审核态重复调用因台账唯一而跳过，不再二次入桶）
	service.GrantTrialBucket(nil, t.ID, t.ContactEmail)
	now := time.Now()
	end := now.AddDate(0, 0, 7)
	db.DB.Model(&model.Tenant{}).Where("id = ?", t.ID).Updates(map[string]interface{}{
		"status":         "trial",
		"trial_start_at": now,
		"trial_end_at":   end,
	})
	uidV, _ := c.Get("user_id")
	db.DB.Create(&model.TenantAuditLog{
		TenantID: t.ID, UserID: toUintSafe(uidV), Action: "trial_granted",
		Resource: fmt.Sprintf("tenant:%d", t.ID),
		IP:       c.ClientIP(), UserAgent: c.Request.UserAgent(),
	})
	log.Printf("[防薅] 超管为租户%d(%s)发放试用包，状态 review→trial", t.ID, t.Code)
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "试用额度已发放，租户已激活"})
}

// toUintSafe 轻量转换
func toUintSafe(v interface{}) uint {
	switch val := v.(type) {
	case uint:
		return val
	case int:
		return uint(val)
	case int64:
		return uint(val)
	}
	return 0
}
