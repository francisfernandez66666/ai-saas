// 超管后台API：平台运营接口，仅 super_admin 可访问，操作全留审计。
package api

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
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

// SuperRequired 平台超管权限守卫中间件
// 区别于 AdminRequired（允许租户管理员通过），此守卫仅允许 super_admin 角色
// 无权限时返回 403 并中断请求
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

// SuperTenantList 获取全平台租户列表（含用量与套餐信息）
// GET /api/v1/super/tenants
// LEFT JOIN subscription_plans 带出套餐名，最多返回500条
// 用于超管运营视图，监控所有租户状态和资源使用情况
func SuperTenantList(c *gin.Context) {
	// row 全平台租户列表聚合行：LEFT JOIN subscription_plans 带出套餐名，便于超管运营视图
	type row struct {
		ID            uint    `json:"id"`             // 租户ID
		Name          string  `json:"name"`           // 租户名称
		Code          string  `json:"code"`           // 租户编码
		Tier          string  `json:"tier"`           // 等级
		Status        string  `json:"status"`         // 状态（active/suspended/trial等）
		PlanName      *string `json:"plan_name"`      // 套餐名称
		UsedCustomers int     `json:"used_customers"` // 已用客户数
		MaxCustomers  int     `json:"max_customers"`  // 客户数上限
		CreatedAt     string  `json:"created_at"`     // 创建时间
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
		RespErr(c, http.StatusInternalServerError, 500, "查询失败")
		return
	}
	RespOK(c, "", rows)
}

// SuperTenantStatus 超管修改租户状态
// PUT /api/v1/super/tenants/:id/status {status}
// 合法流转：active↔suspended；expired/cancelled 仅超管手动干预
// 修改后立即清除租户解析缓存，确保封禁即时生效
func SuperTenantStatus(c *gin.Context) {
	var req struct {
		Status string `json:"status" binding:"required"` // 目标状态
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, 400, 400, "参数错误")
		return
	}
	// 校验目标状态合法性
	valid := map[string]bool{"active": true, "suspended": true, "expired": true, "cancelled": true, "trial": true}
	if !valid[req.Status] {
		RespErr(c, 400, 400, "非法状态")
		return
	}
	// 健壮性收口(2026-09-05)：ID 入口校验，非法直接 400，不再以空串/非数字打到 PG(22P02)
	id, ok := PathUintID(c)
	if !ok {
		return
	}
	res := db.DB.Model(&model.Tenant{}).Where("id = ?", id).Update("status", req.Status)
	if res.Error != nil || res.RowsAffected == 0 {
		RespErr(c, 404, 404, "租户不存在")
		return
	}

	// 写入审计日志
	uidV, _ := c.Get("user_id")
	db.DB.Create(&model.TenantAuditLog{
		TenantID: id, UserID: toUintSafe(uidV), Action: "super_tenant_status",
		Resource: "tenant:" + strconv.FormatUint(uint64(id), 10), Detail: `{"to":"` + req.Status + `"}`,
		IP: c.ClientIP(), UserAgent: c.Request.UserAgent(),
	})
	// 租户解析缓存失效，封禁即时生效
	middleware.InvalidateTenantCache()
	RespOK(c, "状态已更新为 "+req.Status, nil)
}

// SuperGrantTrial 超管发放试用额度（审核模式放行）
// POST /api/v1/super/tenants/:id/grant-trial
// 幂等设计：已存在 trial_granted 审计记录的租户拒绝重复发放
// 流转路径：review/trial/suspended → trial，发放7天试用期
func SuperGrantTrial(c *gin.Context) {
	// 健壮性收口(2026-09-05)：ID 入口校验，非法不再触 DB
	tid, ok := PathUintID(c)
	if !ok {
		return
	}
	var t model.Tenant
	if err := db.DB.First(&t, tid).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "租户不存在")
		return
	}
	// 幂等拦截：检查是否已发放过试用额度
	var granted int64
	db.DB.Model(&model.TenantAuditLog{}).
		Where("tenant_id = ? AND action = ?", t.ID, "trial_granted").
		Count(&granted)
	if granted > 0 {
		RespErr(c, http.StatusConflict, 409, "该租户已发放过试用额度（幂等拦截）")
		return
	}
	// P1.5(2026-08-26)：换用幂等的 GrantTrialBucket（双唯一防撞库；
	// 非审核态重复调用因台账唯一而跳过，不再二次入桶）
	service.GrantTrialBucket(nil, t.ID, t.ContactEmail)
	// 设置试用期：当前时间开始，7天后结束
	now := time.Now()
	end := now.AddDate(0, 0, 7)
	db.DB.Model(&model.Tenant{}).Where("id = ?", t.ID).Updates(map[string]interface{}{
		"status":         "trial",
		"trial_start_at": now,
		"trial_end_at":   end,
	})
	// 写入审计日志
	uidV, _ := c.Get("user_id")
	db.DB.Create(&model.TenantAuditLog{
		TenantID: t.ID, UserID: toUintSafe(uidV), Action: "trial_granted",
		Resource: fmt.Sprintf("tenant:%d", t.ID),
		IP:       c.ClientIP(), UserAgent: c.Request.UserAgent(),
	})
	log.Printf("[防薅] 超管为租户%d(%s)发放试用包，状态 review→trial", t.ID, t.Code)
	RespOK(c, "试用额度已发放，租户已激活", nil)
}

// toUintSafe 轻量类型转换：将 interface{} 安全转换为 uint
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
