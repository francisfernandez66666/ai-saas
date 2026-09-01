// 邮箱验证码接口：注册发码/换绑发码/换绑完成（防枚举、防薅、Turnstile 前置）
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
	RespOK(c, "", gin.H{
		"email_verify_enabled": service.EmailVerifyEnabled(),
	})
}

// emailCodeReq 注册场景发送验证码请求体（仅需邮箱）
type emailCodeReq struct {
	Email string `json:"email" binding:"required"`
}

// SendRegisterEmailCode POST /api/v1/auth/email-code —— 注册场景发码（免登录）
func SendRegisterEmailCode(c *gin.Context) {
	var req emailCodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "邮箱不能为空")
		return
	}
	if !service.EmailVerifyEnabled() {
		RespErr(c, http.StatusBadRequest, 400, "当前未开启邮箱验证")
		return
	}
	// 防枚举（J6）：已注册邮箱不返回冲突，统一返回“成功”且不发码，
	// 攻击者无法借响应差异判断某邮箱是否已注册，杜绝邮箱枚举
	var dup int64
	db.DB.Model(&model.User{}).Where("email = ?", service.NormalizeEmail(req.Email)).Count(&dup)
	if dup > 0 {
		RespOK(c, "若该邮箱可注册，验证码将发送至该邮箱（10分钟内有效）", nil)
		return
	}
	if err := service.SendEmailCode(req.Email, model.EmailPurposeRegister, c.ClientIP()); err != nil {
		status := http.StatusInternalServerError
		msg := err.Error()
		// 按 service 层错误消息前缀判定限流/格式错误类型（约定式，非结构化错误码）
		if msg[:6] == "发送太频" || msg[:12] == "今日验证码发送" || msg[:6] == "邮箱格式" {
			status = http.StatusTooManyRequests
			if msg[:6] == "邮箱格式" {
				status = http.StatusBadRequest
			}
		}
		RespErr(c, status, status, msg)
		return
	}
	RespOK(c, "验证码已发送至邮箱，10分钟内有效", nil)
}

// bindEmailCodeReq 换绑场景向新邮箱发验证码的请求体（登录态）
type bindEmailCodeReq struct {
	NewEmail string `json:"new_email" binding:"required"`
}

// SendBindEmailCode POST /api/v1/auth/email/code —— 换绑场景向新邮箱发码（登录态）
func SendBindEmailCode(c *gin.Context) {
	var req bindEmailCodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "新邮箱不能为空")
		return
	}
	newEmail := service.NormalizeEmail(req.NewEmail)
	uid, _, _ := middleware.CurrentUser(c)

	// L5修复(2026-08-27)：与注册发码路径对齐，关闭邮箱验证时换绑链路同样禁用
	if !service.EmailVerifyEnabled() {
		RespErr(c, http.StatusBadRequest, 400, "当前未开启邮箱验证")
		return
	}

	// 新邮箱不得与他人/自己现有绑定冲突
	var dup int64
	db.DB.Model(&model.User{}).Where("email = ? AND id <> ?", newEmail, uid).Count(&dup)
	if dup > 0 {
		RespErr(c, http.StatusConflict, 409, "该邮箱已被其他账号使用")
		return
	}
	if err := service.SendEmailCode(newEmail, model.EmailPurposeBind, c.ClientIP()); err != nil {
		RespErr(c, http.StatusTooManyRequests, 429, err.Error())
		return
	}
	RespOK(c, "验证码已发送至新邮箱，10分钟内有效", nil)
}

// changeEmailReq 换绑邮箱确认请求体：新邮箱 + 验证码（证明能收信）
type changeEmailReq struct {
	NewEmail string `json:"new_email" binding:"required"`
	Code     string `json:"code" binding:"required"`
}

// ChangeEmail POST /api/v1/auth/email/change —— 校验验证码完成换绑（登录态）
// 安全：验证新邮箱所有权（证明能收信）即放行；登录态本身已证身份
func ChangeEmail(c *gin.Context) {
	var req changeEmailReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误")
		return
	}
	newEmail := service.NormalizeEmail(req.NewEmail)
	uid, username, _ := middleware.CurrentUser(c)
	tid := tenantIDOf(c)

	// 防薅v2 换绑撞库（2026-08-26）：新邮箱曾参与任何奖励领取 → 拒绝换绑。
	// 奖励双唯一语义（ID主键维度+邮箱外键维度）：放行"被奖励过的邮箱"换入=二次套利入口；
	// 置于验码之前——撞库邮箱不值得消耗一次真实发信。
	var rc int64
	db.RQ(c).Model(&model.RewardClaim{}).Where("email = ?", newEmail).Count(&rc)
	if rc > 0 {
		RespErr(c, http.StatusConflict, 409, "该邮箱涉及历史奖励记录，不可用于换绑")
		return
	}
	if err := service.VerifyEmailCode(newEmail, model.EmailPurposeBind, req.Code); err != nil {
		RespErr(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	// 二次校验唯一性（发码与消费之间可能被他人抢注）
	var dup int64
	db.DB.Model(&model.User{}).Where("email = ? AND id <> ?", newEmail, uid).Count(&dup)
	if dup > 0 {
		RespErr(c, http.StatusConflict, 409, "该邮箱刚被其他账号绑定，请更换")
		return
	}
	if err := db.DB.Model(&model.User{}).Where("id = ?", uid).
		Update("email", newEmail).Error; err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "更新失败")
		return
	}
	writeAuditSimple(c, tid, "email_change", "user:"+username+"→"+service.MaskEmailAddr(newEmail))
	RespOK(c, "邮箱绑定成功："+service.MaskEmailAddr(newEmail), nil)
}
