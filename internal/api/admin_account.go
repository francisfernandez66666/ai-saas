package api

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

// CancelAccount POST /api/v1/admin/account/cancel {password}
func CancelAccount(c *gin.Context) {
	uid, _, _ := middleware.CurrentUser(c)
	ti := middleware.GetTenantInfo(c)
	if ti.ID == 0 {
		c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "无租户语境"})
		return
	}
	var req struct {
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误：password 必填"})
		return
	}

	// 密码二次确认
	var user model.User
	if err := db.DB.Select("id, password_hash").First(&user, uid).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "用户不存在"})
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "密码错误"})
		return
	}

	now := time.Now()
	res := db.DB.Model(&model.Tenant{}).Where("id = ?", ti.ID).
		Update("cancel_at", now)
	if res.Error != nil || res.RowsAffected == 0 {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "注销受理失败"})
		return
	}

	// 同步禁用本租户签发的全部 API Key（权限即时收回）
	db.DB.Model(&model.ApiKey{}).Where("tenant_id = ?", ti.ID).Update("is_active", false)

	db.DB.Create(&model.TenantAuditLog{
		TenantID: ti.ID, UserID: uid, Action: "account_cancel",
		Resource: "tenant:" + ti.Code,
		Detail:   `{"retention":"数据保留","effective":"次日零点禁登录","api_keys":"disabled"}`,
		IP:       c.ClientIP(), UserAgent: c.Request.UserAgent(),
	})

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "注销已受理：今日内仍可登录，明日零点起账号停用（后台数据保留）",
	})
}
