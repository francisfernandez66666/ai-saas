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

/*
VisibleDeptIDs 返回操作者可见的部门 ID 集合

用途：根据用户角色返回其可见的部门 ID 列表，用于数据范围裁剪。

角色权限说明：
  - tenant_admin / super_admin：返回 (nil, true, nil)，不做部门裁剪（全租户可见）
  - dept_admin：返回本部门及所有子部门 ID（按物化路径前缀匹配）
  - 其余角色或缺失 dept_path：返回空集合（fail-closed 设计，查不到任何数据）

参数：
  - c: Gin 上下文（包含用户角色和部门路径信息）

返回值：
  - ids: 可见部门 ID 列表（nil 表示全租户可见）
  - allScope: 是否全租户可见（true 时调用方无需追加部门条件）
  - err: 错误信息

设计说明：
  - fail-closed 设计：无明确权限时拒绝访问，而非开放全部数据
  - 物化路径前缀匹配实现高效的子树查询
*/
func VisibleDeptIDs(c *gin.Context) (ids []uint, allScope bool, err error) {
	// 获取用户角色
	role, _ := c.Get("role")
	r, _ := role.(string)
	// 全租户角色：返回全租户标识
	switch r {
	case model.RoleSuperAdmin, model.RoleTenantAdmin:
		return nil, true, nil
	}
	// 非部门管理员：返回空集合（fail-closed）
	if r != model.RoleDeptAdmin {
		return []uint{}, false, nil
	}
	// 获取部门路径
	pathV, _ := c.Get("dept_path")
	deptPath, _ := pathV.(string)
	// 部门路径为空：返回空集合（fail-closed）
	if deptPath == "" {
		return []uint{}, false, nil
	}
	// 查询所有子部门（物化路径前缀匹配）
	var depts []model.Department
	if e := db.RQ(c).Select("id").Where("path LIKE ?", deptPath+"%").Find(&depts).Error; e != nil {
		return nil, false, e
	}
	// 提取部门 ID 列表
	out := make([]uint, 0, len(depts))
	for _, d := range depts {
		out = append(out, d.ID)
	}
	if out == nil {
		out = []uint{}
	}
	return out, false, nil
}

/*
VisibleAssignedUserSubquery 返回"归属于可见部门用户"的 ID 子查询

用途：为 customers 表提供部门级数据范围裁剪。

设计说明：
  - customers 表无 department_id 字段
  - 客户通过 assigned_user_id 关联到 tenant_users.department_id
  - 因此部门范围需通过 assigned_user_id IN (可见部门下的用户) 表达

参数：
  - c: Gin 上下文
  - deptIDs: 可见部门 ID 列表
  - allScope: 是否全租户可见

返回值：
  - *gorm.DB: GORM 子查询（allScope=true 时返回 nil）
  - bool: 是否全租户可见
*/
func VisibleAssignedUserSubquery(c *gin.Context, deptIDs []uint, allScope bool) (*gorm.DB, bool) {
	// 全租户可见：返回 nil，调用方无需追加部门条件
	if allScope {
		return nil, true
	}
	// 获取租户 ID
	tid := db.EffectiveTenantIDFromGin(c)
	// 构造子查询：查询可见部门下的用户 ID
	sub := db.DB.Table("tenant_users").
		Select("id").
		Where("tenant_id = ? AND department_id IN ?", tid, deptIDs)
	return sub, false
}
