package model

import (
	"time"
)

// ============================================================
// 租户用户模型 - 替代 sys_users
// ============================================================

// TenantUser 租户用户表
// SaaS 化改造：直接替代 sys_users，原 sys_users 表废弃。
// Role 定义：super_admin(超级管理员)/tenant_admin(租户管理员)/sales(销售)/readonly(只读)
// tenant_id 为 NULL 时表示超级管理员，非 NULL 时表示某租户下用户
type TenantUser struct {
	ID            uint           `gorm:"primaryKey" json:"id"`                                         // 主键
	Username      string         `gorm:"size:50;uniqueIndex;not null" json:"username"`                 // 登录名
	PasswordHash  string         `gorm:"size:255;not null" json:"-"`                                   // 密码哈希（不返回给前端）
	RealName      string         `gorm:"size:50" json:"real_name"`                                     // 真实姓名
	TenantID      *uint          `gorm:"index" json:"-"`                                                 // 租户ID，NULL=超级管理员，非NULL=某租户下用户
	Role          string         `gorm:"size:20;not null;default:sales" json:"role"`                    // 角色：super_admin(超级管理员)/tenant_admin(租户管理员)/sales(销售)/readonly(只读)
	Phone         string         `gorm:"size:20" json:"phone"`                                         // 手机号
	Email         string         `gorm:"size:100" json:"email"`                                        // 邮箱
	Avatar        string         `gorm:"size:255" json:"avatar"`                                       // 头像URL
	Department    string         `gorm:"size:50" json:"department"`                                    // 部门
	Status        int            `gorm:"default:1" json:"status"`                                      // 状态：1=正常 0=禁用
	LastLoginAt   *time.Time     `json:"last_login_at"`                                                  // 最后登录时间
	LastLoginIP   string         `gorm:"size:45" json:"last_login_ip"`                                 // 最后登录IP
	CreatedAt     time.Time      `json:"created_at"`                                                    // 创建时间
	UpdatedAt     time.Time      `json:"updated_at"`                                                    // 更新时间
}

// TableName 指定表名
func (TenantUser) TableName() string {
	return "tenant_users"
}

// Role constants for type-safe checks
const (
	RoleSuperAdmin  = "super_admin"
	RoleTenantAdmin = "tenant_admin"
	RoleSales       = "sales"
	RoleReadOnly    = "readonly"
)

// IsSuperAdmin 是否超级管理员
func (u *TenantUser) IsSuperAdmin() bool {
	return u.Role == RoleSuperAdmin
}

// IsTenantAdmin 是否租户管理员
func (u *TenantUser) IsTenantAdmin() bool {
	return u.Role == RoleTenantAdmin
}

// IsSales 是否销售
func (u *TenantUser) IsSales() bool {
	return u.Role == RoleSales
}

// IsReadOnly 是否只读
func (u *TenantUser) IsReadOnly() bool {
	return u.Role == RoleReadOnly
}

// IsAdmin 兼容旧检查：超级管理员或租户管理员
func (u *TenantUser) IsAdmin() bool {
	return u.IsSuperAdmin() || u.IsTenantAdmin()
}