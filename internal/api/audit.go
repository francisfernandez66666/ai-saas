// 审计日志查询API：超管全平台与租户本租户的只读分页查询。
package api

// 审计日志查询API：超管全平台(SuperAuditLogs)与租户本租户(AdminAuditLogs)的只读分页查询。
// 只读不写审计，避免自我激励写入死循环。

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

// auditLogRow 审计日志单条记录（查询结果格式）
type auditLogRow struct {
	ID        uint   `json:"id"`         // 日志ID
	TenantID  uint   `json:"tenant_id"`  // 所属租户ID
	UserID    uint   `json:"user_id"`    // 操作用户ID
	Username  string `json:"username"`   // 操作用户名（JOIN tenant_users 获取）
	Action    string `json:"action"`     // 操作类型（如 account_cancel、super_tenant_status）
	Resource  string `json:"resource"`   // 操作资源（如 tenant:123）
	Detail    string `json:"detail"`     // 操作详情（JSON格式）
	IP        string `json:"ip"`         // 操作者IP地址
	CreatedAt string `json:"created_at"` // 操作时间
}

// auditQueryResult 审计查询分页结果
type auditQueryResult struct {
	Total    int64         `json:"total"`     // 总记录数
	Page     int           `json:"page"`      // 当前页码
	PageSize int           `json:"page_size"` // 每页条数
	List     []auditLogRow `json:"list"`      // 日志列表
}

// queryAuditLogs 审计查询公共实现（分页 + 多条件筛选）
// tenantID > 0 时限定本租户；0 = 超管全平台（可再按 ?tenant_id= 过滤）
// 支持 action、from、to 筛选条件，防自激励死循环（只读不写）
func queryAuditLogs(c *gin.Context, tenantID uint) {
	// 解析分页参数，设置合理默认值
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	// 构建查询：LEFT JOIN tenant_users 获取操作用户名
	q := db.DB.Table("tenant_audit_logs a").
		Select(`a.id, COALESCE(a.tenant_id,0) as tenant_id, COALESCE(a.user_id,0) as user_id,
			COALESCE(u.username,'') as username, a.action, COALESCE(a.resource,'') as resource,
			COALESCE(a.detail,'') as detail, COALESCE(a.ip,'') as ip,
			TO_CHAR(a.created_at,'YYYY-MM-DD HH24:MI:SS') as created_at`).
		Joins("LEFT JOIN tenant_users u ON a.user_id = u.id")

	// 租户隔离：非超管只能查本租户，超管可按 tenant_id 过滤
	if tenantID > 0 {
		q = q.Where("a.tenant_id = ?", tenantID)
	} else if v := c.Query("tenant_id"); v != "" {
		q = q.Where("a.tenant_id = ?", v) // 超管按租户筛选
	}
	// 按操作类型筛选
	if action := c.Query("action"); action != "" {
		q = q.Where("a.action = ?", action)
	}
	// 按时间范围筛选
	if from := c.Query("from"); from != "" {
		q = q.Where("a.created_at >= ?", from+" 00:00:00")
	}
	if to := c.Query("to"); to != "" {
		q = q.Where("a.created_at <= ?", to+" 23:59:59")
	}

	// 统计总数
	var total int64
	q.Count(&total)

	// 分页查询
	rows := []auditLogRow{}
	if err := q.Order("a.id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Scan(&rows).Error; err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "查询失败")
		return
	}
	RespOK(c, "", auditQueryResult{
		Total: total, Page: page, PageSize: pageSize, List: rows,
	})
}

// SuperAuditLogs 超管查询全平台审计日志
// GET /api/v1/super/audit-logs?tenant_id=&action=&from=&to=&page=
// 可按租户ID、操作类型、时间范围筛选，支持分页
func SuperAuditLogs(c *gin.Context) {
	// tenantID=0 表示超管全平台查询
	queryAuditLogs(c, 0)
}

// AdminAuditLogs 租户管理员查询本租户审计日志
// GET /api/v1/admin/audit-logs?action=&from=&to=&page=
// 仅限查询本租户数据，不可查看其他租户
func AdminAuditLogs(c *gin.Context) {
	queryAuditLogs(c, tenantIDOf(c))
}
