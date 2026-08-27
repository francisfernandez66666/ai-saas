// 数据范围分级：部门树向下继承裁剪（tenant_admin/super_admin 全租户，dept_admin 仅子树，fail-closed）
package api

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ============================================================
// 数据范围分级（批次三 J2）
//
// 权限沿部门树向下继承：dept_admin 仅可见本部门及下属子部门的数据；
// tenant_admin / super_admin 看全租户。tenant_id 隔离由 db.RQ(c) 自动盖章，
// 本文件只负责"部门级"裁剪。
// ============================================================

// VisibleDeptIDs 返回操作者可见的部门ID集合。
//   - tenant_admin / super_admin：返回 (nil, true, nil)，调用方不做部门裁剪（全租户）。
//   - dept_admin：返回其本部门及所有子部门ID（按物化路径前缀匹配，含自身）。
//   - 其余角色或缺失 dept_path：返回空集合（fail-closed，查不到任何数据）。
//
// 调用方据此构造 WHERE department_id IN (?) 或对应的用户子查询。
func VisibleDeptIDs(c *gin.Context) (ids []uint, allScope bool, err error) {
	role, _ := c.Get("role")
	r, _ := role.(string)
	switch r {
	case model.RoleSuperAdmin, model.RoleTenantAdmin:
		return nil, true, nil
	}
	if r != model.RoleDeptAdmin {
		return []uint{}, false, nil
	}
	pathV, _ := c.Get("dept_path")
	deptPath, _ := pathV.(string)
	if deptPath == "" {
		return []uint{}, false, nil
	}
	var depts []model.Department
	if e := db.RQ(c).Select("id").Where("path LIKE ?", deptPath+"%").Find(&depts).Error; e != nil {
		return nil, false, e
	}
	out := make([]uint, 0, len(depts))
	for _, d := range depts {
		out = append(out, d.ID)
	}
	if out == nil {
		out = []uint{}
	}
	return out, false, nil
}

// VisibleAssignedUserSubquery 返回"归属于可见部门用户"的 ID 子查询。
// customers 表无 department_id，客户经 assigned_user_id 关联到 tenant_users.department_id，
// 故部门范围需通过 assigned_user_id IN (可见部门下的用户) 表达。
// allScope=true 时返回 nil（调用方无需追加部门条件）。
func VisibleAssignedUserSubquery(c *gin.Context, deptIDs []uint, allScope bool) (*gorm.DB, bool) {
	if allScope {
		return nil, true
	}
	tid := db.EffectiveTenantIDFromGin(c)
	sub := db.DB.Table("tenant_users").
		Select("id").
		Where("tenant_id = ? AND department_id IN ?", tid, deptIDs)
	return sub, false
}
