// 账号注销API：管理员密码二次确认后受理注销，数据保留并禁用本租户全部API Key。
package api

// 账号注销API（P4）：租户管理员密码二次确认后受理注销，当日可登录、次日禁登录，数据保留并禁用本租户全部API Key。

// ============================================================
// 账号注销（P4，2026-08-26）
//
// POST /api/v1/admin/account/cancel {password}
//
// 语义（定稿）：
//   - 注销当日仍可登录，次日零点起登录与全部租户访问被拒
//   - 后台数据完整保留（不删除任何业务数据）
//   - 同步禁用该租户名下签发的全部 API Key（权限即时收回）
//   - 需密码二次确认；写审计 account_cancel
// ============================================================

import (
	"net/http"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// CancelAccount 租户管理员账号注销
// POST /api/v1/admin/account/cancel {password}
// 需要密码二次确认，注销后当日仍可登录，次日零点起禁用
// 数据完整保留，API Key 同步禁用
func CancelAccount(c *gin.Context) {
	uid, _, _ := middleware.CurrentUser(c)
	ti := middleware.GetTenantInfo(c)
	if ti.ID == 0 {
		RespErr(c, http.StatusForbidden, 403, "无租户语境")
		return
	}
	// 绑定请求体，仅需密码字段
	var req struct {
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误：password 必填")
		return
	}

	// 密码二次确认：防止误操作，必须验证当前登录用户密码
	var user model.User
	if err := db.DB.Select("id, password_hash").First(&user, uid).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "用户不存在")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)) != nil {
		RespErr(c, http.StatusBadRequest, 400, "密码错误")
		return
	}

	// 设置注销时间（次日零点生效）
	now := time.Now()
	res := db.DB.Model(&model.Tenant{}).Where("id = ?", ti.ID).
		Update("cancel_at", now)
	if res.Error != nil || res.RowsAffected == 0 {
		RespErr(c, http.StatusInternalServerError, 500, "注销受理失败")
		return
	}

	// 同步禁用本租户签发的全部 API Key（权限即时收回）
	db.DB.Model(&model.ApiKey{}).Where("tenant_id = ?", ti.ID).Update("is_active", false)

	// 写入审计日志，记录注销操作
	db.DB.Create(&model.TenantAuditLog{
		TenantID: ti.ID, UserID: uid, Action: "account_cancel",
		Resource: "tenant:" + ti.Code,
		Detail:   `{"retention":"数据保留","effective":"次日零点禁登录","api_keys":"disabled"}`,
		IP:       c.ClientIP(), UserAgent: c.Request.UserAgent(),
	})

	RespOK(c, "注销已受理：今日内仍可登录，明日零点起账号停用（后台数据保留）", nil)
}
