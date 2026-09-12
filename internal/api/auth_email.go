// 邮箱验证码接口：注册发码/换绑发码/换绑完成（防枚举、防薅、Turnstile 前置）
package api

import (
	"net/http"
	"strings"

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
	// 2026-09-08：追加下发可选行业列表（注册漏斗「所属行业」下拉数据源 → signup.industry 落包）。
	// 语义：general 恒为默认兜底项（resolveIndustry 未命中回落），其后为已上架行业级包。
	// 前端提交的 industry 值 = 行业包 code（BindTenantToIndustryPack/openActivePackByCode 均按 code 匹配）。
	industries := []gin.H{{"code": "general", "name": "通用行业"}}
	var packs []model.IndustryPack
	db.DB.Model(&model.IndustryPack{}).
		Where("pack_level = ? AND status = ?", "industry", "active").
		Order("id ASC").
		Find(&packs)
	for _, p := range packs {
		industries = append(industries, gin.H{"code": p.Code, "name": p.Name})
	}
	RespOK(c, "", gin.H{
		"email_verify_enabled": service.EmailVerifyEnabled(),
		"industries":           industries,
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
		// P1-22 修复(2026-09-09)：原 `msg[:6]`/`msg[:12]` 对短错误串直接 slice 越界 panic
		// （recovery 兜 500）；改用 strings.HasPrefix 分类，短错误串安全归为 500。
		status := http.StatusInternalServerError
		msg := err.Error()
		switch {
		case strings.HasPrefix(msg, "发送太频"), strings.HasPrefix(msg, "今日验证码发送"), strings.HasPrefix(msg, "邮箱格式"):
			status = http.StatusTooManyRequests
			if strings.HasPrefix(msg, "邮箱格式") {
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
		// P1-22 修复：换绑发码不再把一切错误归 429 误导用户——仅限流/冷却类提示，其余按 500
		msg := err.Error()
		if strings.HasPrefix(msg, "发送太频") || strings.HasPrefix(msg, "今日验证码发送") {
			RespErr(c, http.StatusTooManyRequests, 429, msg)
		} else {
			RespErr(c, http.StatusInternalServerError, 500, "验证码发送失败，请稍后重试")
		}
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
	// R14 修复(2026-09-11)：必须全局查（db.DB）而非租户作用域（db.RQ）——"邮箱在别的租户
	// 领过注册礼，换绑进来再领一份"正是本检查要堵的跨租户套利，租户作用域查询恒为 0 形同虚设。
	var rc int64
	db.DB.Model(&model.RewardClaim{}).Where("email = ?", newEmail).Count(&rc)
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
