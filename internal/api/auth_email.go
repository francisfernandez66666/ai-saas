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
// 邮箱验证码 API（2026-08-24，阿里云SMTP接入）
//
// 公开：
//   GET  /api/v1/auth/register-config   注册页配置下发（前端据此显隐验证码输入框）
//   POST /api/v1/auth/email-code        发送注册验证码 {email}（TurnstileGuard 前置）
// 登录态：
//   POST /api/v1/auth/email/code        发送换绑验证码到新邮箱 {new_email}
//   POST /api/v1/auth/email/change      校验并完成换绑 {new_email, code}
//
// 防薅：同邮箱60s冷却 / 单IP每日20封 / 10分钟有效 / 错5次作废（service层统一实现）
// ============================================================

// RegisterConfig GET /api/v1/auth/register-config （免登录公开）
func RegisterConfig(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{
		"email_verify_enabled": service.EmailVerifyEnabled(),
	}})
}

type emailCodeReq struct {
	Email string `json:"email" binding:"required"`
}

// SendRegisterEmailCode POST /api/v1/auth/email-code —— 注册场景发码（免登录）
func SendRegisterEmailCode(c *gin.Context) {
	var req emailCodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "邮箱不能为空"})
		return
	}
	if !service.EmailVerifyEnabled() {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "当前未开启邮箱验证"})
		return
	}
	// 已被占用的邮箱拒绝再发（防枚举已有账号+防抢占）
	var dup int64
	db.DB.Model(&model.User{}).Where("email = ?", service.NormalizeEmail(req.Email)).Count(&dup)
	if dup > 0 {
		c.JSON(http.StatusConflict, gin.H{"code": 409, "message": "该邮箱已被绑定，请更换或直接登录"})
		return
	}
	if err := service.SendEmailCode(req.Email, model.EmailPurposeRegister, c.ClientIP()); err != nil {
		status := http.StatusInternalServerError
		msg := err.Error()
		if msg[:6] == "发送太频" || msg[:12] == "今日验证码发送" || msg[:6] == "邮箱格式" {
			status = http.StatusTooManyRequests
			if msg[:6] == "邮箱格式" {
				status = http.StatusBadRequest
			}
		}
		c.JSON(status, gin.H{"code": status, "message": msg})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "验证码已发送至邮箱，10分钟内有效"})
}

type bindEmailCodeReq struct {
	NewEmail string `json:"new_email" binding:"required"`
}

// SendBindEmailCode POST /api/v1/auth/email/code —— 换绑场景向新邮箱发码（登录态）
func SendBindEmailCode(c *gin.Context) {
	var req bindEmailCodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "新邮箱不能为空"})
		return
	}
	newEmail := service.NormalizeEmail(req.NewEmail)
	uid, _, _ := middleware.CurrentUser(c)

	// 新邮箱不得与他人/自己现有绑定冲突
	var dup int64
	db.DB.Model(&model.User{}).Where("email = ? AND id <> ?", newEmail, uid).Count(&dup)
	if dup > 0 {
		c.JSON(http.StatusConflict, gin.H{"code": 409, "message": "该邮箱已被其他账号使用"})
		return
	}
	if err := service.SendEmailCode(newEmail, model.EmailPurposeBind, c.ClientIP()); err != nil {
		c.JSON(http.StatusTooManyRequests, gin.H{"code": 429, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "验证码已发送至新邮箱，10分钟内有效"})
}

type changeEmailReq struct {
	NewEmail string `json:"new_email" binding:"required"`
	Code     string `json:"code" binding:"required"`
}

// ChangeEmail POST /api/v1/auth/email/change —— 校验验证码完成换绑（登录态）
// 安全：验证新邮箱所有权（证明能收信）即放行；登录态本身已证身份
func ChangeEmail(c *gin.Context) {
	var req changeEmailReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误"})
		return
	}
	newEmail := service.NormalizeEmail(req.NewEmail)
	uid, username, _ := middleware.CurrentUser(c)
	tid := tenantIDOf(c)

	if err := service.VerifyEmailCode(newEmail, model.EmailPurposeBind, req.Code); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
		return
	}
	// 二次校验唯一性（发码与消费之间可能被他人抢注）
	var dup int64
	db.DB.Model(&model.User{}).Where("email = ? AND id <> ?", newEmail, uid).Count(&dup)
	if dup > 0 {
		c.JSON(http.StatusConflict, gin.H{"code": 409, "message": "该邮箱刚被其他账号绑定，请更换"})
		return
	}
	if err := db.DB.Model(&model.User{}).Where("id = ?", uid).
		Update("email", newEmail).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "更新失败"})
		return
	}
	writeAuditSimple(c, tid, "email_change", "user:"+username+"→"+service.MaskEmailAddr(newEmail))
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "邮箱绑定成功：" + service.MaskEmailAddr(newEmail)})
}
