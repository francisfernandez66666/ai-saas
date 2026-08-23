package model

import (
	"time"
)

// ============================================================
// 商业化第一批（2026-08-23）新增模型
//
// Package        AI 商业包（试用/包月/增量三类，M2）
// PasswordReset  重置密码验证码（M3 去演示化：随机码+哈希存储+一次性）
// 设计参照：翻译助手 packages 表（生产验证版式），适配 ai-scrm 配额体系
// ============================================================

// 商业包类型常量
const (
	PackageTypeFree      = "free"      // 注册试用包：发放 trial_ai_calls 次 AI 调用
	PackageTypePaid      = "paid"      // 包月包：顺延 expired_at + 提升月配额
	PackageTypeIncrement = "increment" // AI增量包：ai_call_balance += 包含量（买断资产）
)

// Package AI 商业包表
type Package struct {
	ID           uint      `gorm:"primaryKey" json:"id"`              // 主键
	Code         string    `gorm:"size:50;uniqueIndex" json:"code"`   // 标识：trial_500/starter_1000/std_5000/booster_1000
	Name         string    `gorm:"size:100;not null" json:"name"`     // 显示名：试用包/入门包月/标准包月/AI加油包
	PType        string    `gorm:"size:20;not null" json:"p_type"`    // 类型：free/paid/increment
	AICalls      int       `json:"ai_calls"`                          // 包内含 AI 调用次数（对外话术统一叫"次"）
	PriceCents   int       `json:"price_cents"`                       // 售价（分）
	DurationDays int       `json:"duration_days"`                     // 有效期天（paid=30；increment=0 买断；free=0 随租户试用）
	Description  string    `gorm:"type:text" json:"description"`      // 包描述（定价页展示）
	Enabled      bool      `gorm:"default:true" json:"enabled"`       // 是否上架
	SortOrder    int       `json:"sort_order"`                        // 排序
	CreatedAt    time.Time `json:"created_at"`                        // 创建时间
	UpdatedAt    time.Time `json:"updated_at"`                        // 更新时间
}

// TableName 指定表名
func (Package) TableName() string {
	return "packages"
}

// PasswordReset 密码重置验证码表（M3）
// 安全要点：code 存 SHA-256 哈希（库泄露不暴露验证码）；10 分钟有效；used 一次性；
// 同账号 60s 限发由服务层校验 last_sent_at 实现
type PasswordReset struct {
	ID          uint       `gorm:"primaryKey" json:"id"`                    // 主键
	Username    string     `gorm:"size:50;index" json:"username"`           // 目标账号
	CodeHash    string     `gorm:"size:200;index" json:"-"`                 // SHA256(6位随机码)
	ExpiredAt   time.Time  `json:"expired_at"`                              // 过期时间（签发+10分钟）
	Used        bool       `gorm:"default:false" json:"used"`               // 一次性标记（验证成功即置 true）
	LastSentAt  time.Time  `json:"last_sent_at"`                            // 最近发送时间（限频锚点）
	CreatedAt   time.Time  `json:"created_at"`                              // 创建时间
	ConsumedAt  *time.Time `json:"consumed_at"`                             // 消费时间（审计）
}

// TableName 指定表名
func (PasswordReset) TableName() string {
	return "password_resets"
}
