// Package model 业务领域模型与 GORM 表结构定义（SaaS 多租户，tenant_id 隔离，写入经自动盖章回调）。
package model

import (
	"time"
)

// ============================================================
// 行业包数据模型（P1 行业包地基，2026-08-25）
//
// 商业模式定位：行业包免费分发（剃刀），token 消耗收费（刀片）；
// 加密仅为 IP 保护（防竞品抽取沉淀的行业知识资产），不是收费闸门。
//
// IndustryPack        平台侧包注册表（上传→验签入库→启停→分发）
// TenantPackBinding   租户↔包绑定（自助选包；一租户一主包）
//
// 包内容物化语义见 internal/industrypack/apply.go：
// templates/features 以 pk_{code}_ 前缀写入租户私有层（幂等重灌=换版本）；
// 卸载即清除该前缀行，不污染全局预置池(tenant_id=0)。
// ============================================================

// IndustryPack 行业包注册表
type IndustryPack struct {
	ID       uint   `gorm:"primaryKey" json:"id"`      // 注释：主键ID
	Code     string `gorm:"size:32;index" json:"code"` // 包唯一码（manifest.code）
	Name     string `gorm:"size:100" json:"name"`      // 显示名
	Industry string `gorm:"size:50" json:"industry"`   // 行业标识
	Version  string `gorm:"size:20" json:"version"`    // semver
	// ---- 三级树形结构（2026-08-26 定稿）：行业 → 企业(=租户) → 部门 ----
	PackLevel  string `gorm:"size:20;default:enterprise;index" json:"pack_level"` // industry/enterprise/department
	ParentCode string `gorm:"size:32;index" json:"parent_code"`                   // 上级包code：企业包→行业code；部门包→企业包code；行业包为空
	// ShareCrossDept 跨部门共享开关（KB继承链改造 2026-08-26）：1=允许跨部门回退可见（默认）
	// 0=退出共享（仅本部门链可见，即使租户策略开也不外泄）。仅对 department 级包有意义
	ShareCrossDept int       `gorm:"default:1" json:"share_cross_dept"`      // 注释：跨部门共享开关
	FileName       string    `gorm:"size:200" json:"file_name"`              // 原始文件名
	FilePath       string    `gorm:"size:300" json:"-"`                      // 服务端存储路径（不出网）
	FileSize       int64     `json:"file_size"`                              // 字节
	ContentSHA256  string    `gorm:"size:64" json:"content_sha256"`          // 内容摘要（来自 manifest）
	Status         string    `gorm:"size:20;default:disabled" json:"status"` // disabled/active
	UploadedBy     uint      `json:"uploaded_by"`                            // 上传超管
	CreatedAt      time.Time `json:"created_at"`                             // 注释：创建时间
	UpdatedAt      time.Time `json:"updated_at"`                             // 注释：更新时间
}

// TenantPackBinding 租户绑定关系：行业主包 + 可选挂靠企业包（一租户一套）
type TenantPackBinding struct {
	ID             uint   `gorm:"primaryKey" json:"id"`           // 注释：主键ID
	TenantID       uint   `gorm:"uniqueIndex" json:"tenant_id"`   // 唯一：换绑=更新行
	PackID         uint   `gorm:"index" json:"pack_id"`           // 行业包ID
	PackCode       string `gorm:"size:32" json:"pack_code"`       // 行业包 code（冗余便于清理）
	AppliedVersion string `gorm:"size:20" json:"applied_version"` // 注释：已应用版本
	// 企业包层（可选；企业=租户的上级资产来源，物化时叠加在行业包之上）
	EnterprisePackID  *uint     `json:"enterprise_pack_id"`                // 注释：企业包ID
	EnterpriseCode    string    `gorm:"size:32" json:"enterprise_code"`    // 注释：企业包编码
	EnterpriseVersion string    `gorm:"size:20" json:"enterprise_version"` // 注释：企业包版本
	BoundAt           time.Time `json:"bound_at"`                          // 注释：绑定时间
	UpdatedAt         time.Time `json:"updated_at"`                        // 注释：更新时间
}

// DeptPackBinding 部门↔部门包绑定（三级树第三层；一部门一主部门包）
type DeptPackBinding struct {
	ID             uint      `gorm:"primaryKey" json:"id"`             // 注释：主键ID
	TenantID       uint      `gorm:"index" json:"tenant_id"`           // 注释：租户ID
	DepartmentID   uint      `gorm:"uniqueIndex" json:"department_id"` // 一部门一包
	PackID         uint      `gorm:"index" json:"pack_id"`             // 注释：包ID
	PackCode       string    `gorm:"size:32" json:"pack_code"`         // 注释：包编码
	AppliedVersion string    `gorm:"size:20" json:"applied_version"`   // 注释：已应用版本
	BoundAt        time.Time `json:"bound_at"`                         // 注释：绑定时间
	UpdatedAt      time.Time `json:"updated_at"`                       // 注释：更新时间
}

// TableName 指定表名
func (IndustryPack) TableName() string { return "industry_packs" }

// TableName 指定表名
func (TenantPackBinding) TableName() string { return "tenant_pack_bindings" }
