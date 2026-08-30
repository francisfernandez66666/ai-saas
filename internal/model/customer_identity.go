// Package model 业务领域模型与 GORM 表结构定义（SaaS 多租户，tenant_id 隔离，写入经自动盖章回调）。
package model

import (
	"time"
)

// ============================================================
// 客户身份标识表（L3：OneID 合并存储拆表）
// 背景：客户通过不同渠道（手机号/微信号/openid）进来，身份字段原本
// 与画像字段混在 customers 表。OneID 合并时仅迁移 customers 字段无法
// 表达"同一自然人多个身份锚点"关系，故拆分身份维度单独落库。
// 语义：一个 customer_id 可对应多条身份记录；同一 (tenant_id, identity_type,
// identity_value) 唯一（一个身份锚点只能归属于一个自然人）。
// ============================================================

// CustomerIdentity 客户身份标识
type CustomerIdentity struct {
	ID            uint      `gorm:"primaryKey" json:"id"`                                                // 主键ID
	TenantID      uint      `gorm:"default:0;index;uniqueIndex:uniq_tenant_identity" json:"tenant_id"`   // 租户ID（fail-closed 隔离）
	CustomerID    uint      `gorm:"index" json:"customer_id"`                                            // 指向 customers.id（最终保留的自然人）
	IdentityType  string    `gorm:"size:20;index;uniqueIndex:uniq_tenant_identity" json:"identity_type"` // 身份类型：phone/wechat/openid
	IdentityValue string    `gorm:"size:100;uniqueIndex:uniq_tenant_identity" json:"identity_value"`     // 身份值（原样小写/规范化由写入方保证）
	Verified      bool      `gorm:"default:false" json:"verified"`                                       // 是否已验证（验证码/微信授权等）
	CreatedAt     time.Time `json:"created_at"`                                                          // 创建时间
}

// TableName 指定表名
func (CustomerIdentity) TableName() string {
	return "customer_identities"
}

// 身份类型常量
const (
	IdentityTypePhone  = "phone"
	IdentityTypeWechat = "wechat"
	IdentityTypeOpenid = "openid"
)
