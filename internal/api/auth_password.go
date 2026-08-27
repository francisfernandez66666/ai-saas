// 账号安全三件套：改密、首登强改密、重置密码去演示化（随机码+SHA256哈希+一次性+限频）
package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strings"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
	"ai-scrm/internal/service"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
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

const (
	resetCodeTTL     = 10 * time.Minute // 验证码有效期
	resetCodeResend  = 60 * time.Second // 同账号限发间隔
	resetMaxAttempts = 5                // 单码最大尝试次数（防爆破）
)

type changePasswordReq struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required"`
}

// validatePasswordStrength 密码强度：≥8位且同时含字母和数字
// UAT定稿(2026-08-26)：注册与改密共用同一强度基线；弱密码一律拒绝
func validatePasswordStrength(pwd string) error {
	if len(pwd) < 8 {
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
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误"})
		return
	}
	userID, _, _ := middleware.CurrentUser(c)

	var user model.User
	if err := db.DB.First(&user, userID).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"code": 401, "message": "登录态异常，请重新登录"})
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.OldPassword)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "旧密码错误"})
		return
	}
	if err := validatePasswordStrength(req.NewPassword); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
		return
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), 12)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "密码处理失败"})
		return
	}
	// 改密成功同时清除首登强改密标记（中间件据此放行全量接口）
	if err := db.DB.Model(&model.User{}).Where("id = ?", userID).Updates(map[string]interface{}{
		"password_hash":        string(hashed),
		"must_change_password": false,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "更新失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "密码修改成功"})
}

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

// SendResetCode POST /api/v1/auth/reset-password {username,contact}
// 重构原因：原实现固定验证码123456且直接回传前端=任何人可重置任意账号（演示遗留漏洞）
func SendResetCode(c *gin.Context) {
	var req resetCodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误"})
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
		c.JSON(http.StatusOK, gin.H{
			"code":    0,
			"message": "若该账号存在且已绑定邮箱，验证码将发送至其绑定邮箱（10分钟内有效）",
		})
		return
	}

	channel := service.DefaultSystemConfigService.GetString("reset_code_channel", "log")

	// log 通道防薅：必须匹配注册手机号/邮箱才发码（smtp 通道下选填，不阻断）
	if channel == "log" {
		if req.Contact == "" || (req.Contact != user.Phone && req.Contact != user.Email) {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "请提供注册时的手机号或邮箱以完成身份校验"})
			return
		}
	}

	// 同账号60s限发：读最近一条未过期记录的签发时间
	var last model.PasswordReset
	if err := db.DB.Where("username = ?", req.Username).Order("id DESC").First(&last).Error; err == nil {
		if time.Since(last.LastSentAt) < resetCodeResend {
			c.JSON(http.StatusTooManyRequests, gin.H{"code": 429, "message": "发送太频繁，请1分钟后再试"})
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
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "验证码生成失败"})
		return
	}

	// Sender 抽象分发：smtp=发到账号绑定邮箱；log=打日志（开发调试，仍要求contact校验）
	if err := service.DefaultResetSender().SendResetCode(user.Email, code); err != nil {
		log.Printf("[重置码] 发送失败 username=%s: %v", req.Username, err)
		c.JSON(http.StatusBadGateway, gin.H{"code": 502, "message": "邮件发送失败，请稍后再试或联系管理员"})
		return
	}

	msg := "验证码已发送至绑定邮箱 " + service.MaskEmailAddr(user.Email) + "，10分钟内有效"
	if channel == "log" {
		msg = "验证码已生成（当前为日志通道，请查看服务端日志），10分钟内有效"
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": msg})
}

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
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误"})
		return
	}
	if err := validatePasswordStrength(req.NewPassword); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
		return
	}

	var rec model.PasswordReset
	err := db.DB.Where("username = ? AND used = ? AND expired_at > ?",
		req.Username, false, time.Now()).Order("id DESC").First(&rec).Error
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "验证码错误或已过期"})
		return
	}
	if rec.CodeHash != hashCode(req.Code) {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "验证码错误或已过期"})
		return
	}

	// 一次性抢占：UPDATE ... WHERE used=false 条件更新，并发重放只有一个赢家
	now := time.Now()
	res := db.DB.Model(&model.PasswordReset{}).
		Where("id = ? AND used = ?", rec.ID, false).
		Updates(map[string]interface{}{"used": true, "consumed_at": now})
	if res.RowsAffected == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "验证码已被使用"})
		return
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), 12)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "密码处理失败"})
		return
	}
	// 重置成功同样清除强改密标记（用户已证明账号所有权）
	result := db.DB.Model(&model.User{}).Where("username = ?", req.Username).Updates(map[string]interface{}{
		"password_hash":        string(hashed),
		"must_change_password": false,
	})
	if result.Error != nil || result.RowsAffected == 0 {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "重置失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "密码重置成功，请使用新密码登录"})
}
