// Package model 业务领域模型与 GORM 表结构定义（SaaS 多租户，tenant_id 隔离，写入经自动盖章回调）。
package model

import (
	"time"
)

// ============================================================
// 用户模型 - 系统用户（管理员、销售）
// ============================================================
// User 系统用户表
// SaaS 化改造：指向 tenant_users 表，sys_users 已废弃。
// P1-32(2026-09-09)：租户用户唯一模型源收敛——原 model/tenant_user.go 与本品双结构同映射
// tenant_users 表，但 database.go 仅迁移 User（TenantUser 从未被迁移，其 idx_tu_tenant_username
// 复合唯一索引从未建出）。现定论：用户名全局唯一（Username uniqueIndex 既成事实），
// 删除 TenantUser 死结构，角色常量与别名下沉本文件。
type User struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`                                                  // 主键ID
	Username           string    `gorm:"size:50;uniqueIndex;not null" json:"username"`                          // 用户名（全局唯一）
	PasswordHash       string    `gorm:"size:255;not null" json:"-"`                                            // 密码哈希（不返回给前端）
	RealName           string    `gorm:"size:50" json:"real_name"`                                              // 真实姓名
	Role               string    `gorm:"size:20;not null;default:sales" json:"role"`                            // 角色: super_admin(超级管理员)/tenant_admin(租户管理员)/sales(销售)/readonly(只读)
	Phone              string    `gorm:"size:20" json:"phone"`                                                  // 手机号
	Email              string    `gorm:"size:100" json:"email"`                                                 // 邮箱
	Avatar             string    `gorm:"size:255" json:"avatar"`                                                // 头像URL
	Status             int       `gorm:"default:1" json:"status"`                                               // 状态: 1-正常 0-禁用
	MustChangePassword bool      `gorm:"column:must_change_password;default:false" json:"must_change_password"` // 首登强制改密标记（M3，seed 默认账号置 true）
	Department         string    `gorm:"size:50" json:"department"`                                             // 部门
	TenantID           *uint     `gorm:"index" json:"-"`                                                        // 租户ID，NULL=超级管理员，非NULL=某租户下用户
	DepartmentID       *uint     `gorm:"index" json:"department_id"`                                            // 所属部门ID（NULL=直属租户层，仅 tenant_admin 允许）
	CreatedAt          time.Time `json:"created_at"`                                                            // 创建时间
	UpdatedAt          time.Time `json:"updated_at"`                                                            // 更新时间
}

// Role constants for type-safe checks（P1-32 自 tenant_user.go 收敛于此）
const (
	RoleSuperAdmin  = "super_admin"
	RoleTenantAdmin = "tenant_admin"
	RoleDeptAdmin   = "dept_admin" // 部门管理员（四级体系，管辖本部门子树）
	RoleUser        = "user"       // 普通用户（仅本人数据）
	RoleReadOnly    = "readonly"

	// 旧角色名兼容别名（存量数据迁移由 db.MigrateOrgData 处理）
	RoleSales = RoleUser
)

// TableName 指定表名
// SaaS 化改造：指向 tenant_users 表，sys_users 已废弃
func (User) TableName() string {
	return "tenant_users"
}

// IsSuperAdmin 是否超级管理员
func (u *User) IsSuperAdmin() bool {
	return u.Role == "super_admin"
}

// IsTenantAdmin 是否租户管理员
func (u *User) IsTenantAdmin() bool {
	return u.Role == "tenant_admin"
}

// IsAdmin 是否管理员（兼容旧检查：超级管理员或租户管理员）
func (u *User) IsAdmin() bool {
	return u.Role == "admin" || u.Role == "super_admin" || u.Role == "tenant_admin"
}

// IsSales 是否销售
func (u *User) IsSales() bool {
	return u.Role == "sales"
}

// IsReadOnly 是否只读
func (u *User) IsReadOnly() bool {
	return u.Role == "readonly"
}
