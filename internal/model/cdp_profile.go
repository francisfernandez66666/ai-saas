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
	ID          uint      `gorm:"primaryKey;autoIncrement"`                                      // 主键ID
	TenantID    uint      `gorm:"default:0;uniqueIndex:idx_cdp_profile_tenant_cdpid,priority:1"` // 租户ID（复合唯一首列）
	CustomerID  uint      `gorm:"index"`                                                         // 客户ID
	CdpId       string    `gorm:"size:32;uniqueIndex:idx_cdp_profile_tenant_cdpid,priority:2"`   // CDP实体ID（租户级唯一，非全局唯一）
	ProfileName string    `gorm:"size:64"`                                                       // 画像名称
	Status      int       `gorm:"default:1"`                                                     // 状态
	ProfileData string    `gorm:"type:json"`                                                     // JSON格式的画像数据
	CreatedAt   time.Time // 创建时间
	UpdatedAt   time.Time // 更新时间
}

// CdpTagDefinition CDP标签定义表
// 定义系统可用的标签类型和元数据
type CdpTagDefinition struct {
	ID            uint      `gorm:"primaryKey;autoIncrement"`                                 // 主键ID
	TenantID      uint      `gorm:"default:0;uniqueIndex:idx_cdp_tag_tenant_code,priority:1"` // 租户ID（复合唯一首列）
	Code          string    `gorm:"size:32;uniqueIndex:idx_cdp_tag_tenant_code,priority:2"`   // 编码（租户级唯一，非全局唯一）
	Name          string    `gorm:"size:64"`                                                  // 名称
	Category      string    `gorm:"size:32"`                                                  // 如: intent, behavior, preference
	WeightDefault float64   `gorm:"default:0"`                                                // 默认权重
	IsActive      bool      `gorm:"default:true"`                                             // 是否上架
	CreatedAt     time.Time // 创建时间
	UpdatedAt     time.Time // 更新时间
}

// CdpTagAssignment CDP标签分配表
// 将标签分配给特定客户的画像
type CdpTagAssignment struct {
	ID           uint      `gorm:"primaryKey;autoIncrement"` // 主键ID
	TenantID     uint      `gorm:"default:0;index"`          // 租户ID
	CdpProfileID uint      `gorm:"index"`                    // 关联画像ID
	DefinitionID uint      `gorm:"index"`                    // 标签定义ID
	TagValue     string    `gorm:"size:128"`                 // 标签的具体值
	AssignedAt   time.Time // 分配时间
	ExpiredAt    time.Time // 标签过期时间，可选
}

// EventLog CDP事件日志表
// 记录客户行为事件，用于画像更新和流程追踪
type EventLog struct {
	ID          uint      `gorm:"primaryKey;autoIncrement"` // 主键ID
	TenantID    uint      `gorm:"default:0;index"`          // 租户ID
	CustomerID  uint      `gorm:"index"`                    // 客户ID
	EventType   string    `gorm:"size:32"`                  // 如: visit, test_drive, message, intent_update
	EventKey    string    `gorm:"size:64"`                  // 事件关键字（如标签码、车型码）
	EventValue  string    `gorm:"type:json"`                // 事件具体值（JSON格式）
	Source      string    `gorm:"size:32"`                  // 数据来源：scrm, api, system, manual
	RelatedID   uint      // 关联ID（如对应的消息ID、订单ID）
	RelatedType string    // 关联类型：message, order, test_drive
	CreatedAt   time.Time // 创建时间
}

// CdpComputedTagRule CDP 计算标签规则表（配置驱动，业务自助配规则，无需改代码）
// 业务先在 cdp_tag_definitions 登记派生标签，再在此写"何时打该标签"的条件；
// 摄入流水线按规则评估、命中即 ApplyTag，扩展标签能力不进代码。
// 红线（与原子标签同）：规则目标标签若属 attitude 维（态度维），一律跳过——
// 态度严禁由行为/规则派生，只能来自零方渠道或 NLP 语义（见 computed_tag.go）。
type CdpComputedTagRule struct {
	ID            uint      `gorm:"primaryKey;autoIncrement"`                                  // 主键ID
	TenantID      uint      `gorm:"default:0;uniqueIndex:idx_cdp_rule_tenant_name,priority:1"` // 租户ID（0=系统级规则，全租户生效）
	Name          string    `gorm:"size:64;uniqueIndex:idx_cdp_rule_tenant_name,priority:2"`   // 规则名（租户级唯一）
	TargetTagCode string    `gorm:"size:32;index"`                                             // 命中后要打的派生标签 code（须先登记进 cdp_tag_definitions）
	Condition     string    `gorm:"type:json"`                                                 // 条件 DSL（JSON，见 cdp/computed_tag.go evalCondition）
	Priority      int       `gorm:"default:0"`                                                 // 评估顺序，小优先
	Status        int       `gorm:"default:1"`                                                 // 1 启用 / 0 禁用
	CreatedAt     time.Time // 创建时间
	UpdatedAt     time.Time // 更新时间
}

// TableName 指定表名
func (CdpComputedTagRule) TableName() string {
	return "cdp_computed_tag_rules"
}

// IdMapping 内部ID到CDP外部ID的映射表
// 用于维护内部系统ID与CDP系统ID之间的对应关系
type IdMapping struct {
	ID           uint      `gorm:"primaryKey;autoIncrement"`            // 主键ID
	TenantID     uint      `gorm:"default:0;index"`                     // 租户ID
	InternalType string    `gorm:"size:128;index" json:"internal_type"` // 如: customer, conversation, message（P1-34(2026-09-09)：size:32→128，"anchor:email:" 前缀 13 字符 + 长邮箱必溢出导致锚点静默丢失；对齐 customer_identity.go 同语义字段 100）
	InternalID   uint      // 内部系统ID
	CdpEntityId  string    `gorm:"size:64"` // CDP系统实体ID
	MappingType  string    // 映射类型：one2one, one2many
	Source       string    // 数据源
	CreatedAt    time.Time // 创建时间
	UpdatedAt    time.Time // 更新时间
}
