// Package model 业务领域模型与 GORM 表结构定义（SaaS 多租户，tenant_id 隔离，写入经自动盖章回调）。
package model

import "time"

// ============================================================
// CDP 数据底座模型（Phase P4 真实化）
// 画像/标签/事件/ID映射四件套，承载 OneID 真实身份归并与行为摄入。
// 所有表 TenantID 默认 0 并建索引：fail-closed 隔离，跨租户读取由业务层
// 携带生效租户条件过滤；ID映射的 internal_type 键用于 OneID 锚点重指向。
// ============================================================

// CdpProfile CDP客户画像表
// 用于存储客户在CDP系统中的统一画像信息
type CdpProfile struct {
	ID          uint   `gorm:"primaryKey;autoIncrement"`
	TenantID    uint   `gorm:"default:0;index"`
	CustomerID  uint   `gorm:"index"`
	CdpId       string `gorm:"size:32;uniqueIndex:idx_cdp_profile_tenant_cdpid"`
	ProfileName string `gorm:"size:64"`
	Status      int    `gorm:"default:1"`
	ProfileData string `gorm:"type:json"` // JSON格式的画像数据
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// CdpTagDefinition CDP标签定义表
// 定义系统可用的标签类型和元数据
type CdpTagDefinition struct {
	ID            uint    `gorm:"primaryKey;autoIncrement"`
	TenantID      uint    `gorm:"default:0;index"`
	Code          string  `gorm:"size:32;uniqueIndex:idx_cdp_tag_tenant_code"`
	Name          string  `gorm:"size:64"`
	Category      string  `gorm:"size:32"` // 如: intent, behavior, preference
	WeightDefault float64 `gorm:"default:0"`
	IsActive      bool    `gorm:"default:true"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// CdpTagAssignment CDP标签分配表
// 将标签分配给特定客户的画像
type CdpTagAssignment struct {
	ID           uint   `gorm:"primaryKey;autoIncrement"`
	TenantID     uint   `gorm:"default:0;index"`
	CdpProfileID uint   `gorm:"index"`
	DefinitionID uint   `gorm:"index"`
	TagValue     string `gorm:"size:128"` // 标签的具体值
	AssignedAt   time.Time
	ExpiredAt    time.Time // 标签过期时间，可选
}

// EventLog CDP事件日志表
// 记录客户行为事件，用于画像更新和流程追踪
type EventLog struct {
	ID          uint   `gorm:"primaryKey;autoIncrement"`
	TenantID    uint   `gorm:"default:0;index"`
	CustomerID  uint   `gorm:"index"`
	EventType   string `gorm:"size:32"`   // 如: visit, test_drive, message, intent_update
	EventKey    string `gorm:"size:64"`   // 事件关键字（如标签码、车型码）
	EventValue  string `gorm:"type:json"` // 事件具体值（JSON格式）
	Source      string `gorm:"size:32"`   // 数据来源：scrm, api, system, manual
	RelatedID   uint   // 关联ID（如对应的消息ID、订单ID）
	RelatedType string // 关联类型：message, order, test_drive
	CreatedAt   time.Time
}

// IdMapping 内部ID到CDP外部ID的映射表
// 用于维护内部系统ID与CDP系统ID之间的对应关系
type IdMapping struct {
	ID           uint   `gorm:"primaryKey;autoIncrement"`
	TenantID     uint   `gorm:"default:0;index"`
	InternalType string `gorm:"size:32;index" json:"internal_type"` // 如: customer, conversation, message（J13-2026-08-27 补索引）
	InternalID   uint   // 内部系统ID
	CdpEntityId  string `gorm:"size:64"` // CDP系统实体ID
	MappingType  string // 映射类型：one2one, one2many
	Source       string // 数据源
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
