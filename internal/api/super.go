// 超管后台API：平台运营接口，仅 super_admin 可访问，操作全留审计。
package api

import "ai-scrm/internal/billing"

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"

	"ai-scrm/internal/schema"
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
// GET /api/v1/super/tenants?page=&page_size=&q=
// LEFT JOIN subscription_plans 带出套餐名；page_size 上限 100，q 为关键字检索
// （纯数字优先按租户 ID 精确命中，另按名称/编码模糊）。
// 用于超管运营视图与「代管租户」选择器，监控所有租户状态和资源使用情况
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
		MaxUsers      int     `json:"max_users"`      // 席位上限（换套餐弹窗展示，2026-09-16 商业缺口批）
		UsedUsers     int     `json:"used_users"`     // 已用席位（启用成员数）
		PlanID        uint    `json:"plan_id"`        // 当前套餐ID
		CreatedAt     string  `json:"created_at"`     // 创建时间
	}
	// P2-29 修复(2026-09-09)：原 Limit(500) 拖全表分页缺失；改分页+总数
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize := schema.NormalizePageSize(atoiDefault(c.DefaultQuery("page_size", "20")))
	if page <= 0 {
		page = 1
	}
	var total int64
	// q 关键字检索(2026-09-24 残项收口)：超管「代管租户」下拉此前只吃列表首页，
	// 清库后承载历史数据的种子租户排在最新 100 家之外，在 UI 上再也代管不到。
	// 纯数字按 ID 直达（"我知道租户号"），其余按名称/编码模糊。
	// 条件分别挂到两条独立链上，**不复用同一个句柄**——db.DB.Table() 起的是 clone=0
	// 句柄，共享会把 Select/Order 一起带进 Count 查询（见 GORM 句柄复用红线）。
	kw := strings.TrimSpace(c.Query("q"))
	var conds []string
	var args []any
	if kw != "" {
		if id, err := strconv.ParseUint(kw, 10, 64); err == nil {
			conds = append(conds, "t.id = ?")
			args = append(args, id)
		}
		// 关键字里的 % 和 _ 是 LIKE 的通配符，必须按字面量处理——"搜一个名字带下划线的
		// 租户"不该变成"搜出全表"。反斜杠先转，否则把后面两个转义再破坏一遍。
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(kw) + "%"
		conds = append(conds, "t.name ILIKE ?", "t.code ILIKE ?")
		args = append(args, like, like)
	}
	where := strings.Join(conds, " OR ")

	countQ := db.DB.Table("tenants t")
	if where != "" {
		countQ = countQ.Where(where, args...)
	}
	countQ.Count(&total)
	rows := []row{}
	listQ := db.DB.Table("tenants t").
		Select(`t.id, t.name, t.code, t.tier, t.status,
			COALESCE(p.name,'') as plan_name,
			t.used_customers, t.max_customers, t.plan_id, t.max_users,
			(SELECT count(*) FROM tenant_users u WHERE u.tenant_id = t.id AND u.status = 1) as used_users,
			TO_CHAR(t.created_at,'YYYY-MM-DD') as created_at`).
		Joins("LEFT JOIN subscription_plans p ON t.plan_id = p.id")
	if where != "" {
		listQ = listQ.Where(where, args...)
	}
	err := listQ.
		// 2026-09-11：改 id DESC——前端无翻页 UI（一次拉 page_size 上限），
		// ASC 会让超管只看到最旧 N 家、新注册租户被挤出视野；DESC 优先展示最新
		Order("t.id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Scan(&rows).Error
	if err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "查询失败")
		return
	}
	RespOK(c, "", gin.H{"list": rows, "total": total, "page": page, "page_size": pageSize})
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
	middleware.InvalidateTenantCacheCluster()
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
	// R9 修复(2026-09-11)：邮箱锚改用租户管理员真实邮箱——t.ContactEmail 从未被 signup 写入
	// （admin_email 存在 tenant_users.email），恒空导致 GrantTrialBucket 的邮箱维度防撞失效
	trialEmail := t.ContactEmail
	if trialEmail == "" {
		var adminMail string
		db.DB.Model(&model.User{}).Where("tenant_id = ? AND email <> ''", t.ID).
			Select("email").Order("id ASC").Limit(1).Scan(&adminMail)
		trialEmail = adminMail
	}
	billing.GrantTrialBucket(nil, t.ID, trialEmail)
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
