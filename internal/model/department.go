// Package model 业务领域模型与 GORM 表结构定义（SaaS 多租户，tenant_id 隔离，写入经自动盖章回调）。
package model

import (
	"time"
)

// ============================================================
// 部门模型（四级用户体系：super_admin → tenant_admin → dept_admin → user）
//
// 树形实现：物化路径 path（如根"/1/"，子"/1/5/"）——
//   - 子树查询：path LIKE '/1/5/%'（命中索引前缀）
//   - 权限继承：dept_admin 管辖范围 = 其部门 path 前缀命中的整棵子树
// 角色常量复用 model/tenant_user.go 已有定义（super_admin/tenant_admin/dept_admin/user/readonly）。
// 完整性约束：
//   - UNIQUE(tenant_id, name, parent_id)：同父下部门名唯一
//   - 防环：只允许挂载到"非自身及后代"的父节点（API 层校验）
//   - 删除保护：仅允许删除空部门（无子部门且无用户）
// ============================================================

// Department 部门表
type Department struct {
	ID        uint      `gorm:"primaryKey" json:"id"` // 注释：主键ID
	TenantID  uint      `gorm:"not null;default:0;uniqueIndex:idx_dept_tenant_name_parent,priority:1" json:"tenant_id"` // 租户ID
	ParentID  *uint     `gorm:"uniqueIndex:idx_dept_tenant_name_parent,priority:3" json:"parent_id"`                    // 父部门ID（NULL=根部门；指针使 NULL 不参与唯一冲突）
	Name      string    `gorm:"size:100;not null;uniqueIndex:idx_dept_tenant_name_parent,priority:2" json:"name"`       // 部门名称
	Path      string    `gorm:"size:500;not null;index" json:"path"`                                                    // 物化路径 "/1/5/12/"（含自身）
	Depth     int       `gorm:"not null;default:1" json:"depth"`                                                        // 层级深度（根=1）
	SortOrder int       `gorm:"not null;default:0" json:"sort_order"`                                                   // 同级排序
	Status    int       `gorm:"not null;default:1" json:"status"`                                                       // 1=启用 0=停用
	CreatedAt time.Time `json:"created_at"` // 注释：创建时间
	UpdatedAt time.Time `json:"updated_at"` // 注释：更新时间
}

// TableName 指定表名
func (Department) TableName() string {
	return "departments"
}
