package model

import (
	"time"
)

// ============================================================
// 用户模型 - 系统用户（管理员、销售）
// ============================================================
//
// User 系统用户表
// SaaS 化改造：指向 tenant_users 表，sys_users 已废弃。
type User struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Username     string    `gorm:"size:50;uniqueIndex;not null" json:"username"` // 用户名
	PasswordHash string    `gorm:"size:255;not null" json:"-"`                   // 密码哈希（不返回给前端）
	RealName     string    `gorm:"size:50" json:"real_name"`                     // 真实姓名
	Role         string    `gorm:"size:20;not null;default:sales" json:"role"`   // 角色: super_admin(超级管理员)/tenant_admin(租户管理员)/sales(销售)/readonly(只读)
	Phone        string    `gorm:"size:20" json:"phone"`                         // 手机号
	Email        string    `gorm:"size:100" json:"email"`                        // 邮箱
	Avatar       string    `gorm:"size:255" json:"avatar"`                       // 头像URL
	Status       int       `gorm:"default:1" json:"status"`                      // 状态: 1-正常 0-禁用
	MustChangePassword bool  `gorm:"column:must_change_password;default:false" json:"must_change_password"` // 首登强制改密标记（M3，seed 默认账号置 true）
	Department   string    `gorm:"size:50" json:"department"`                    // 部门
	TenantID     *uint     `gorm:"index" json:"-"`                               // 租户ID，NULL=超级管理员，非NULL=某租户下用户
	DepartmentID *uint     `gorm:"index" json:"department_id"`                   // 所属部门ID（NULL=直属租户层，仅 tenant_admin 允许）
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

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
