// Package model 业务领域模型与 GORM 表结构定义（SaaS 多租户，tenant_id 隔离，写入经自动盖章回调）。
package model

import "time"

// ============================================================
// 协议签署台账模型
// 用户注册即视为同意《用户协议》《隐私政策》，落两条记录（user/privacy），
// 记录版本、状态与签署时间，供平台超管"协议签署"tab 审计。
// TenantID 随注册租户归属，fail-closed 隔离。
// ============================================================

// AgreementSignature 协议签署台账
// 用户注册即视为同意《用户协议》《隐私政策》，落两条记录（user/privacy），
// 记录版本、状态与签署时间，供平台超管"协议签署"tab 审计。
type AgreementSignature struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	TenantID      uint      `gorm:"index" json:"tenant_id"`
	UserID        uint      `gorm:"index" json:"user_id"`
	AgreementType string    `gorm:"size:20;index" json:"agreement_type"` // user=用户协议 privacy=隐私政策
	Version       string    `gorm:"size:20" json:"version"`
	Status        string    `gorm:"size:20" json:"status"` // signed
	SignedAt      time.Time `json:"signed_at"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (AgreementSignature) TableName() string { return "agreement_signatures" }
