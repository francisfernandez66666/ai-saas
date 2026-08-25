package model

import (
	"time"
)

// ============================================================
// 邮箱验证码（商业化第二批追加，借鉴翻译助手三期 §3.9）
//
// 用途：注册验证 / 换绑新邮箱；与 password_resets（密码重置）分表，
// purpose 区分场景防止跨场景消费
// 安全：code 只存 SHA-256 哈希；10分钟有效；一次性；错误5次作废；
//       同邮箱60s冷却；单IP每日20封
// ============================================================

// 验证码用途常量
const (
	EmailPurposeRegister = "register"  // 注册验证
	EmailPurposeBind     = "bind_email" // 换绑新邮箱（登录态）
)

// EmailVerify 邮箱验证码表
type EmailVerify struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	Email      string    `gorm:"size:100;index" json:"email"`   // 目标邮箱（小写）
	Purpose    string    `gorm:"size:20;index" json:"purpose"`  // register|bind_email
	CodeHash   string    `gorm:"size:200" json:"-"`             // SHA256(6位码)
	ExpiredAt  time.Time `json:"expired_at"`                    // 签发+10分钟
	Used       bool      `gorm:"default:false" json:"used"`     // 一次性消费标记
	Attempts   int       `json:"attempts"`                      // 错误尝试次数（≥5作废）
	SentIP     string    `gorm:"size:45" json:"-"`              // 发送请求来源IP（日限额锚点）
	LastSentAt time.Time `json:"last_sent_at"`                  // 冷却锚点
	CreatedAt  time.Time `json:"created_at"`
}

// TableName 指定表名
func (EmailVerify) TableName() string {
	return "email_verifies"
}
