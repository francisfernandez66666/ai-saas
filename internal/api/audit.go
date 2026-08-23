package api

import (
	"net/http"
	"strconv"

	"ai-scrm/internal/db"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 审计日志查询 API（商业化第一批 M5，2026-08-23）
//
// 补齐"只写不读"缺口：
//   GET /api/v1/super/audit-logs?tenant_id=&action=&from=&to=&page=  超管全平台
//   GET /api/v1/admin/audit-logs?action=&from=&to=&page=             本租户
// 只读查询不写审计（防自激励死循环）
// ============================================================

type auditLogRow struct {
	ID        uint   `json:"id"`
	TenantID  uint   `json:"tenant_id"`
	UserID    uint   `json:"user_id"`
	Username  string `json:"username"`
	Action    string `json:"action"`
	Resource  string `json:"resource"`
	Detail    string `json:"detail"`
	IP        string `json:"ip"`
	CreatedAt string `json:"created_at"`
}

type auditQueryResult struct {
	Total    int64          `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
	List     []auditLogRow  `json:"list"`
}

// queryAuditLogs 审计查询公共实现（分页 + action/from/to 筛选）
// tenantID > 0 时限定本租户；0 = 超管全平台（可再按 ?tenant_id= 过滤）
func queryAuditLogs(c *gin.Context, tenantID uint) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	q := db.DB.Table("tenant_audit_logs a").
		Select(`a.id, COALESCE(a.tenant_id,0) as tenant_id, COALESCE(a.user_id,0) as user_id,
			COALESCE(u.username,'') as username, a.action, COALESCE(a.resource,'') as resource,
			COALESCE(a.detail,'') as detail, COALESCE(a.ip,'') as ip,
			TO_CHAR(a.created_at,'YYYY-MM-DD HH24:MI:SS') as created_at`).
		Joins("LEFT JOIN tenant_users u ON a.user_id = u.id")

	if tenantID > 0 {
		q = q.Where("a.tenant_id = ?", tenantID)
	} else if v := c.Query("tenant_id"); v != "" {
		q = q.Where("a.tenant_id = ?", v) // 超管按租户筛选
	}
	if action := c.Query("action"); action != "" {
		q = q.Where("a.action = ?", action)
	}
	if from := c.Query("from"); from != "" {
		q = q.Where("a.created_at >= ?", from+" 00:00:00")
	}
	if to := c.Query("to"); to != "" {
		q = q.Where("a.created_at <= ?", to+" 23:59:59")
	}

	var total int64
	q.Count(&total)

	rows := []auditLogRow{}
	if err := q.Order("a.id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Scan(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": auditQueryResult{
		Total: total, Page: page, PageSize: pageSize, List: rows,
	}})
}

// SuperAuditLogs GET /api/v1/super/audit-logs —— 全平台（可按租户筛选）
func SuperAuditLogs(c *gin.Context) {
	queryAuditLogs(c, 0)
}

// AdminAuditLogs GET /api/v1/admin/audit-logs —— 本租户
func AdminAuditLogs(c *gin.Context) {
	queryAuditLogs(c, tenantIDOf(c))
}
