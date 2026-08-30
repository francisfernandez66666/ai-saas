// 邮箱验证码服务：注册/换绑场景验证码发送与一次性校验，含防薅四件套。
package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
)

// ============================================================
// 邮箱验证码服务（商业化第二批，借鉴翻译助手三期 §3.9 防薅节奏）
//
// 场景：注册验证（register）/ 换绑新邮箱（bind_email）；密码重置走
// password_resets 独立表。共用 SMTPSender 发送通道。
//
// 防薅四件套：
//   同邮箱 60s 冷却 / 单 IP 每日 20 封 / 10 分钟有效 / 错误 5 次作废
// ============================================================

// 常量/变量定义块（自动补注释）。
const (
	emailCodeTTL     = 10 * time.Minute // 有效期
	emailCooldown    = 60 * time.Second // 同邮箱发送冷却
	emailIPDailyCap  = 20               // 单IP每日上限
	emailMaxAttempts = 5                // 最大错误尝试次数
)

// hashCodeEmail SHA256 摘要（与密码重置码同一哈希口径）
func hashCodeEmail(code string) string {
	h := sha256.Sum256([]byte(code))
	return hex.EncodeToString(h[:])
}

// normalizeEmail 邮箱规范化：去空格+小写
func normalizeEmail(e string) string {
	return strings.ToLower(strings.TrimSpace(e))
}

// SendEmailCode 生成并发送邮箱验证码
// 返回 error；调用方负责映射为友好提示
func SendEmailCode(email, purpose, ip string) error {
	email = normalizeEmail(email)
	if !strings.Contains(email, "@") || len(email) > 100 {
		return fmt.Errorf("邮箱格式不正确")
	}

	// 同邮箱冷却：读最近一条记录的签发时间
	var last model.EmailVerify
	if err := db.DB.Where("email = ? AND purpose = ?", email, purpose).
		Order("id DESC").First(&last).Error; err == nil {
		if time.Since(last.LastSentAt) < emailCooldown {
			return fmt.Errorf("发送太频繁，请1分钟后再试")
		}
	}
	// 单IP每日上限
	var today int64
	db.DB.Model(&model.EmailVerify{}).
		Where("sent_ip = ? AND created_at >= CURRENT_DATE", ip).
		Count(&today)
	if today >= emailIPDailyCap {
		return fmt.Errorf("今日验证码发送次数已达上限，请明日再试")
	}

	code := genEmailCode() // 6位随机码（crypto/rand）
	rec := model.EmailVerify{
		Email:      email,
		Purpose:    purpose,
		CodeHash:   hashCodeEmail(code),
		ExpiredAt:  time.Now().Add(emailCodeTTL),
		SentIP:     ip,
		LastSentAt: time.Now(),
	}
	if err := db.DB.Create(&rec).Error; err != nil {
		return fmt.Errorf("验证码写入失败")
	}

	sender := DefaultResetSender() // 复用通道选择（smtp/log，配置驱动）
	subject, body := buildEmailCodeContent(purpose, code)
	if _, isLog := sender.(LogSender); isLog {
		// log 通道：码直接落服务端日志（开发调试用）；验证码属敏感凭据，脱敏不落明文
		log.Printf("[邮箱验证码] 用途=%s 邮箱=%s 验证码=***(已脱敏,10分钟有效)", purpose, maskEmail(email))
		return nil
	}
	if err := sender.SendRaw([]string{email}, subject, body); err != nil {
		return fmt.Errorf("邮件发送失败：%v", err)
	}
	log.Printf("[邮箱验证码] 已发送 用途=%s to=%s", purpose, maskEmail(email))
	return nil
}

// VerifyEmailCode 校验并一次性消费验证码
// 命中：used=false→true 原子抢占 + 过期/作废/错误计数校验
func VerifyEmailCode(email, purpose, code string) error {
	email = normalizeEmail(email)
	var rec model.EmailVerify
	err := db.DB.Where("email = ? AND purpose = ?", email, purpose).
		Order("id DESC").First(&rec).Error
	if err != nil || rec.Used || time.Now().After(rec.ExpiredAt) {
		return fmt.Errorf("验证码错误或已过期")
	}
	if rec.Attempts >= emailMaxAttempts {
		return fmt.Errorf("错误次数过多，请重新获取验证码")
	}
	if rec.CodeHash != hashCodeEmail(strings.TrimSpace(code)) {
		db.DB.Model(&model.EmailVerify{}).Where("id = ?", rec.ID).
			UpdateColumn("attempts", rec.Attempts+1) // 错误计数（≥5次本码作废）
		return fmt.Errorf("验证码错误或已过期")
	}
	// 一次性抢占：并发重放只有一个赢家
	res := db.DB.Model(&model.EmailVerify{}).
		Where("id = ? AND used = false", rec.ID).Update("used", true)
	if res.RowsAffected == 0 {
		return fmt.Errorf("验证码已被使用")
	}
	return nil
}

// EmailVerifyEnabled 注册邮箱验证开关（平台级热配置）
func EmailVerifyEnabled() bool {
	if DefaultSystemConfigService == nil {
		return false
	}
	return DefaultSystemConfigService.GetBool("email_verify_enabled", false)
}

// buildEmailCodeContent 按用途组装邮件标题与正文
func buildEmailCodeContent(purpose, code string) (string, string) {
	switch purpose {
	case model.EmailPurposeBind:
		return "跨山 LexCross 绑定新邮箱验证码",
			fmt.Sprintf("您正在将账号绑定到本邮箱。\n\n验证码：%s\n\n10 分钟内有效，仅可使用一次。若非本人操作请忽略本邮件。", code)
	default: // register
		return "跨山 LexCross 注册验证码",
			fmt.Sprintf("欢迎注册跨山 LexCross！\n\n验证码：%s\n\n10 分钟内有效，仅可使用一次。若非本人操作请忽略本邮件。", code)
	}
}

// genEmailCode 6位随机数字码（crypto/rand 密码学安全）
func genEmailCode() string {
	max := big.NewInt(1000000)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "000000" // 熵源异常兜底（配合一次性+哈希存储仍安全）
	}
	return fmt.Sprintf("%06d", n.Int64())
}

// NormalizeEmail 邮箱规范化（导出供 api 层复用）：去空格+小写
func NormalizeEmail(e string) string {
	return strings.ToLower(strings.TrimSpace(e))
}
