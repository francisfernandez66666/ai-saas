// 账号安全三件套：改密、首登强改密、重置密码去演示化（随机码+SHA256哈希+一次性+限频）
package api

import "ai-scrm/internal/notify"

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"ai-scrm/config"
	"ai-scrm/internal/db"
	"ai-scrm/internal/metrics"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// ============================================================
// 账号安全三件套（商业化第一批 M3，2026-08-23）
//
//   1. POST /api/v1/auth/change-password      改密（旧密码校验 + 强度策略）
//   2. 首登强制改密：must_change_password 标记 + MustChangePasswordGuard 中间件
//   3. 重置密码去演示化：固定码123456 删除 → 随机6位码
//      （password_resets 表哈希存储 / 10分钟有效 / 一次性 / 同账号60s限发）
//
// 发送通道：Sender 抽象（service/notifier.go）。log 模式=知道用户名即可看到码，
// 故额外要求提供注册时的手机号/邮箱匹配才发码；切 smtp 后该要求自动放宽为可选
// ============================================================

// resetCodeTTL 重置码有效期；resetCodeResend 同账号限发间隔；resetMaxAttempts 单码最大尝试次数（防爆破）
const (
	resetCodeTTL     = 10 * time.Minute // 验证码有效期
	resetCodeResend  = 60 * time.Second // 同账号限发间隔
	resetMaxAttempts = 5                // 单码最大尝试次数（防爆破）
)

// changePasswordReq 登录态修改密码请求体（旧密码校验 + 新密码强度校验）
type changePasswordReq struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required"`
}

// validatePasswordStrength 密码强度：≥8位且同时含字母和数字
// UAT定稿(2026-08-26)：注册与改密共用同一强度基线；弱密码一律拒绝
// 实测批残项收口(2026-09-19)：长度改按 rune 计数——原 len() 按字节判定，
// 中文/emoji 密码 3 个字符合 9 字节却只有 3 位，误放行弱密码。
func validatePasswordStrength(pwd string) error {
	if utf8.RuneCountInString(pwd) < 8 {
		return fmt.Errorf("密码至少8位且同时包含字母和数字")
	}
	var hasLetter, hasDigit bool
	for _, r := range pwd {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			hasLetter = true
		case r >= '0' && r <= '9':
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		return fmt.Errorf("密码至少8位且同时包含字母和数字")
	}
	return nil
}

// ChangePassword POST /api/v1/auth/change-password {old_password,new_password}
// 校验链：旧密码 bcrypt 比对 → 新密码强度 → 更新 hash → 清除强制改密标记
func ChangePassword(c *gin.Context) {
	var req changePasswordReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误")
		return
	}
	userID, _, _ := middleware.CurrentUser(c)

	var user model.User
	if err := db.DB.First(&user, userID).Error; err != nil {
		RespErr(c, http.StatusUnauthorized, 401, "登录态异常，请重新登录")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.OldPassword)); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "旧密码错误")
		return
	}
	if err := validatePasswordStrength(req.NewPassword); err != nil {
		RespErr(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), 12)
	if err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "密码处理失败")
		return
	}
	// 改密成功同时清除首登强改密标记（中间件据此放行全量接口）
	// B4 修复(2026-09-14)：递增 token_version 吊销改密前签发的所有旧 JWT（踢掉窃持旧 token 的会话）
	if err := db.DB.Model(&model.User{}).Where("id = ?", userID).Updates(map[string]interface{}{
		"password_hash":        string(hashed),
		"must_change_password": false,
		"token_version":        gorm.Expr("COALESCE(token_version, 0) + 1"),
	}).Error; err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "更新失败")
		return
	}
	RespOK(c, "密码修改成功", nil)
}

// resetCodeReq 申请重置密码请求体：用户名必填，联系信息在 log 通道下必填（防码泄露）
type resetCodeReq struct {
	Username string `json:"username" binding:"required"`
	Contact  string `json:"contact"` // 注册时的手机号/邮箱（log 通道必填校验项）
}

// genResetCode 6位随机数字码（crypto/rand，非伪随机）
func genResetCode() string {
	max := big.NewInt(1000000)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "000000" // 熵源异常兜底（实际不可达；配合一次性+哈希存储仍安全）
	}
	return fmt.Sprintf("%06d", n.Int64())
}

// hashCode 验证码 SHA256 摘要（库内只存哈希，库泄露不暴露明文码）
func hashCode(code string) string {
	h := sha256.Sum256([]byte(code))
	return hex.EncodeToString(h[:])
}

// hashEqual 常量时间哈希比较（P2-20）：长度不等直接 false（提前返回不泄漏长度信息）
func hashEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// SendResetCode POST /api/v1/auth/reset-password {username,contact}
// 重构原因：原实现固定验证码123456且直接回传前端=任何人可重置任意账号（演示遗留漏洞）
func SendResetCode(c *gin.Context) {
	var req resetCodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误")
		return
	}

	var user model.User
	exists := true
	if err := db.DB.Select("id, username, phone, email").Where("username = ?", req.Username).First(&user).Error; err != nil {
		exists = false
	}
	// 防枚举（J6）：账号不存在或未绑定邮箱时，统一返回“成功”且不发码，
	// 攻击者无法借响应差异判断某用户名是否注册，从而杜绝账号枚举
	if !exists || strings.TrimSpace(user.Email) == "" {
		RespOK(c, "若该账号存在且已绑定邮箱，验证码将发送至其绑定邮箱（10分钟内有效）", nil)
		return
	}

	// S2 修复（2026-09-23 批二）：通道判定从"读配置"改为"读实际生效通道"。
	// 原写法在 reset_code_channel=smtp 但 SMTP 环境变量缺失时仍按 smtp 分支走——
	// 实际发送方 DefaultResetSender 已降级 log，于是既跳过了 contact 自证、
	// 又对用户回"验证码已发送至绑定邮箱"，而码其实只在服务端日志里（用户永远收不到=生产不可自助）。
	channel := notify.ResetSenderKind()
	if channel == "log" && !config.IsDevModeConfirmed() {
		// 生产态落到 log 通道=明文重置码进日志：任何有日志读取权的人可改任意绑定邮箱账号的密码。
		// 不阻断（阻断会让本就无邮件通道的部署彻底无法重置），但要计数 + 打点，
		// 并在 /status readiness 的 reset_code_channel_secure 观测位亮灯（见 metrics/readiness.go）。
		metrics.IncResetCodeInsecure()
		log.Printf("[重置码][SEC-WARN] 生产态使用 log 通道发送验证码 username=%s：请配 SMTP_HOST/SMTP_USER 并设 reset_code_channel=smtp", req.Username)
	}

	// log 通道防薅：必须匹配注册手机号/邮箱才发码（smtp 通道下选填，不阻断）
	if channel == "log" {
		if req.Contact == "" || (req.Contact != user.Phone && req.Contact != user.Email) {
			RespErr(c, http.StatusBadRequest, 400, "请提供注册时的手机号或邮箱以完成身份校验")
			return
		}
	}

	// 同账号60s限发：读最近一条未过期记录的签发时间
	var last model.PasswordReset
	if err := db.DB.Where("username = ?", req.Username).Order("id DESC").First(&last).Error; err == nil {
		if time.Since(last.LastSentAt) < resetCodeResend {
			RespErr(c, http.StatusTooManyRequests, 429, "发送太频繁，请1分钟后再试")
			return
		}
	}

	code := genResetCode()
	rec := model.PasswordReset{
		Username:   req.Username,
		CodeHash:   hashCode(code), // 库内只存哈希，库泄露不暴露验证码
		ExpiredAt:  time.Now().Add(resetCodeTTL),
		LastSentAt: time.Now(),
	}
	if err := db.DB.Create(&rec).Error; err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "验证码生成失败")
		return
	}

	// Sender 抽象分发：smtp=发到账号绑定邮箱；log=打日志（开发调试，仍要求contact校验）
	if err := notify.DefaultResetSender().SendResetCode(user.Email, code); err != nil {
		log.Printf("[重置码] 发送失败 username=%s: %v", req.Username, err)
		RespErr(c, http.StatusBadGateway, 502, "邮件发送失败，请稍后再试或联系管理员")
		return
	}

	msg := "验证码已发送至绑定邮箱 " + notify.MaskEmailAddr(user.Email) + "，10分钟内有效"
	if channel == "log" {
		// S2 修复（2026-09-23）：旧文案"请查看服务端日志"把内部实现抛给最终用户——
		// 真实用户没有日志读取权，等于告知"这条路走不通"却不给出口。
		// 现按运维口径改：验证码仍照发（管理员/自助渠道可取），用户侧引导找管理员。
		msg = "重置验证码已通过管理员通道下发，请联系客服或管理员协助完成改密（10分钟内有效）"
	}
	RespOK(c, msg, nil)
}

// GetResetChannel GET /api/v1/auth/reset-channel
// 返回**实际生效**的找回密码通道（smtp|log），供前端决定文案（S2，2026-09-23 批二）。
// 为什么需要这个端点：登录页原先把"验证码将输出到服务端日志（开发模式）"硬编码给最终用户，
// 既泄露实现细节，又在邮件其实已配好的生产环境说假话。前端改为按本端点渲染后，
// 文案与后端真实通道永不漂移。
// 安全边界：只回通道种类，绝不回 SMTP 主机/账号/密钥等任何配置值。
func GetResetChannel(c *gin.Context) {
	RespOK(c, "", gin.H{"channel": notify.ResetSenderKind()})
}

// resetConfirmReq 重置密码确认请求体：用户名 + 验证码 + 新密码
type resetConfirmReq struct {
	Username    string `json:"username" binding:"required"`
	Code        string `json:"code" binding:"required"`
	NewPassword string `json:"new_password" binding:"required"`
}

// VerifyResetCode POST /api/v1/auth/verify-reset-code {username,code,new_password}
// 验证码一次性消费（used=true 原子抢占），第二次提交同一码必失效
func VerifyResetCode(c *gin.Context) {
	var req resetConfirmReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误")
		return
	}
	if err := validatePasswordStrength(req.NewPassword); err != nil {
		RespErr(c, http.StatusBadRequest, 400, err.Error())
		return
	}

	var rec model.PasswordReset
	err := db.DB.Where("username = ? AND used = ? AND expired_at > ?",
		req.Username, false, time.Now()).Order("id DESC").First(&rec).Error
	if err != nil {
		RespErr(c, http.StatusBadRequest, 400, "验证码错误或已过期")
		return
	}
	// P2-20 修复：单码尝试次数上限——暴力枚举 6 位码（1e6 组合）若无防爆破可无限尝试。
	// B6 修复(2026-09-14)：check+increment 原子化——旧实现先读内存副本判 <5 再另发 UPDATE 自增，
	// 并发窗口内 N 个请求都基于同一旧值通过检查（注释宣称"防并发击穿"实只增量子原子）。
	// 现用条件 UPDATE：仅当 attempts 仍在上限内才消耗一次机会，0 行=超限/已消费，随即作废该码。
	incRes := db.DB.Model(&model.PasswordReset{}).
		Where("id = ? AND used = ? AND attempts < ?", rec.ID, false, resetMaxAttempts).
		Update("attempts", gorm.Expr("attempts + 1"))
	if incRes.Error != nil {
		RespErr(c, http.StatusInternalServerError, 500, "校验失败")
		return
	}
	if incRes.RowsAffected == 0 {
		db.DB.Model(&model.PasswordReset{}).Where("id = ?", rec.ID).
			Updates(map[string]interface{}{"used": true, "consumed_at": time.Now()})
		RespErr(c, http.StatusBadRequest, 400, "验证码错误次数过多，请重新获取")
		return
	}
	// P2-20 修复：哈希比较改常量时间（subtle.ConstantTimeCompare）——防时序侧信道
	// 逐位爆破哈希前缀；rec.CodeHash 与计算值等长时泄漏面收敛。
	if !hashEqual(rec.CodeHash, hashCode(req.Code)) {
		RespErr(c, http.StatusBadRequest, 400, "验证码错误或已过期")
		return
	}

	// 一次性抢占：UPDATE ... WHERE used=false 条件更新，并发重放只有一个赢家
	now := time.Now()
	res := db.DB.Model(&model.PasswordReset{}).
		Where("id = ? AND used = ?", rec.ID, false).
		Updates(map[string]interface{}{"used": true, "consumed_at": now})
	if res.RowsAffected == 0 {
		RespErr(c, http.StatusBadRequest, 400, "验证码已被使用")
		return
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), 12)
	if err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "密码处理失败")
		return
	}
	// 重置成功同样清除强改密标记（用户已证明账号所有权）
	// B4：重置密码必须吊销旧 token——否则攻击者持窃得的旧 JWT 可继续操作，
	// 即便用户已改密。递增 token_version 使重置前签发的 token 全部失效。
	result := db.DB.Model(&model.User{}).Where("username = ?", req.Username).Updates(map[string]interface{}{
		"password_hash":        string(hashed),
		"must_change_password": false,
		"token_version":        gorm.Expr("COALESCE(token_version, 0) + 1"),
	})
	if result.Error != nil || result.RowsAffected == 0 {
		RespErr(c, http.StatusInternalServerError, 500, "重置失败")
		return
	}
	RespOK(c, "密码重置成功，请使用新密码登录", nil)
}
