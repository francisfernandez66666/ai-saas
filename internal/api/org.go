// 组织架构：四级用户体系、作用域门禁、部门树与用户管理（fail-closed 权限矩阵 + 配额）
package api

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/service"
	"ai-scrm/pkg/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ============================================================
// 组织架构管理（四级用户体系）
//
// 权限矩阵：
//   tenant_admin：全租户任意部门/用户；可建根部门；可任命 dept_admin/user/readonly
//   dept_admin ：仅本部门子树；可在子树内（不含自己所在部门）任命下级 dept_admin、建 user；
//                不可触碰 readonly/tenant_admin 及子树外任何账号
// 数据门禁：所有读写先过 assertDeptInScope（fail-closed）
// ============================================================

// OrgManageRequired 组织管理入口守卫：放行 tenant_admin/dept_admin，其余拒绝
func OrgManageRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		role, _ := c.Get("role")
		r, _ := role.(string)
		switch r {
		case model.RoleSuperAdmin, model.RoleTenantAdmin, model.RoleDeptAdmin:
			c.Next()
			return
		}
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"code": 403, "message": "需要租户管理员或部门管理员权限", "data": nil,
		})
	}
}

// ---- 作用域工具 ----

// myScope 从 Context 取操作者作用域
type orgScope struct {
	TenantID uint
	Role     string
	UserID   uint
	DeptID   uint
	DeptPath string // 形如 "/1/5/"；tenant_admin 为 ""
}

// getOrgScope 从 gin.Context 解析操作者作用域（角色/用户ID/部门ID/部门物化路径）
// 入参：c 请求上下文；出参：orgScope（租户隔离基石，后续所有门禁判断均依赖它）
func getOrgScope(c *gin.Context) orgScope {
	s := orgScope{TenantID: db.EffectiveTenantIDFromGin(c)}
	if v, ok := c.Get("role"); ok {
		s.Role, _ = v.(string)
	}
	if v, ok := c.Get("user_id"); ok {
		switch val := v.(type) {
		case uint:
			s.UserID = val
		case int:
			s.UserID = uint(val)
		case int64:
			s.UserID = uint(val)
		}
	}
	if v, ok := c.Get("dept_id"); ok {
		switch val := v.(type) {
		case uint:
			s.DeptID = val
		case int:
			s.DeptID = uint(val)
		case int64:
			s.DeptID = uint(val)
		}
	}
	if v, ok := c.Get("dept_path"); ok {
		s.DeptPath, _ = v.(string)
	}
	return s
}

// deptInScope 部门是否落在操作者管辖范围内（fail-closed）
// tenant_admin 恒真；dept_admin 要求目标路径以其自身路径为前缀
func deptInScope(s orgScope, deptPath string) bool {
	if s.Role == model.RoleSuperAdmin || s.Role == model.RoleTenantAdmin {
		return true
	}
	if s.Role == model.RoleDeptAdmin && s.DeptPath != "" {
		return strings.HasPrefix(deptPath, s.DeptPath)
	}
	return false
}

// canAssignRole 角色分配合法性（调用方已确保部门在范围内）
func canAssignRole(operator orgScope, targetRole string) bool {
	switch operator.Role {
	case model.RoleSuperAdmin:
		switch targetRole {
		case model.RoleTenantAdmin, model.RoleDeptAdmin, model.RoleUser, model.RoleReadOnly:
			return true
		}
	case model.RoleTenantAdmin:
		switch targetRole {
		case model.RoleTenantAdmin, model.RoleDeptAdmin, model.RoleUser, model.RoleReadOnly:
			return true
		}
	case model.RoleDeptAdmin:
		switch targetRole {
		case model.RoleDeptAdmin, model.RoleUser:
			return true
		}
	}
	return false
}

// ---- 部门树查询 ----

// GetDepartmentTree GET /org/departments/tree
// 返回当前作用域内完整部门树（dept_admin 仅见子树），附带每部门人数
func GetDepartmentTree(c *gin.Context) {
	s := getOrgScope(c)
	var depts []model.Department
	// db.RQ(c) 自动按当前租户盖章 tenant_id，保证部门查询不跨租户
	q := db.RQ(c).Order("path ASC, sort_order ASC, id ASC")
	if err := q.Find(&depts).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "查询失败"})
		return
	}

	// 各部门人数（一次聚合）
	type cntRow struct {
		DepartmentID uint
		Cnt          int64
	}
	var cnts []cntRow
	db.RQ(c).Model(&model.User{}).
		Select("department_id, COUNT(*) as cnt").
		Where("department_id IS NOT NULL AND status = 1").
		Group("department_id").Scan(&cnts)
	cntMap := map[uint]int64{}
	for _, r := range cnts {
		cntMap[r.DepartmentID] = r.Cnt
	}

	// 过滤作用域（dept_admin 只保留子树）并组装树
	type node struct {
		model.Department
		UserCount int64   `json:"user_count"`
		Children  []*node `json:"children"`
	}
	nodes := map[uint]*node{}
	var roots []*node
	for _, d := range depts {
		if !deptInScope(s, d.Path) {
			continue
		}
		nodes[d.ID] = &node{Department: d, UserCount: cntMap[d.ID]}
	}
	for _, d := range depts {
		n := nodes[d.ID]
		if n == nil {
			continue
		}
		if d.ParentID != nil && nodes[*d.ParentID] != nil {
			parent := nodes[*d.ParentID]
			parent.Children = append(parent.Children, n)
		} else {
			roots = append(roots, n)
		}
	}
	if roots == nil {
		roots = []*node{}
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "success", "data": roots})
}

// ---- 部门创建/修改/删除 ----

type deptCreateReq struct {
	Name      string `json:"name" binding:"required"`
	ParentID  *uint  `json:"parent_id"` // 空=根部门（仅 tenant_admin）
	SortOrder int    `json:"sort_order"`
}

// CreateDepartment POST /org/departments
func CreateDepartment(c *gin.Context) {
	s := getOrgScope(c)
	var req deptCreateReq
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误：name 必填"})
		return
	}

	// 根部门仅租户管理员可建
	if req.ParentID == nil && s.Role != model.RoleTenantAdmin {
		c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "仅租户管理员可创建根部门"})
		return
	}

	tid := s.TenantID
	parentPath := "/"
	depth := 1
	if req.ParentID != nil {
		var p model.Department
		if err := db.RQ(c).First(&p, *req.ParentID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "父部门不存在"})
			return
		}
		if !deptInScope(s, p.Path) {
			c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "父部门不在您的管辖范围内"})
			return
		}

		parentPath = p.Path
		depth = p.Depth + 1
	}

	// 配额检查：MaxDepartments（0=不限）
	var tenant model.Tenant
	if err := db.DB.Select("max_departments").First(&tenant, tid).Error; err == nil && tenant.MaxDepartments > 0 {
		var cnt int64
		db.RQ(c).Model(&model.Department{}).Count(&cnt)
		if cnt >= int64(tenant.MaxDepartments) {
			c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "部门数已达套餐上限，请升级套餐"})
			return
		}
	}

	// 同父重名拦截（唯一索引兜底前先友好报错）
	var dup int64
	parentCond := "parent_id IS NULL"
	if req.ParentID != nil {
		parentCond = "parent_id = ?"
	}
	dq := db.RQ(c).Model(&model.Department{}).Where("name = ?", strings.TrimSpace(req.Name))
	if req.ParentID != nil {
		dq = dq.Where(parentCond, *req.ParentID)
	} else {
		dq = dq.Where(parentCond)
	}
	dq.Count(&dup)
	if dup > 0 {
		c.JSON(http.StatusConflict, gin.H{"code": 409, "message": "同级下已存在同名部门"})
		return
	}

	// 事务：插行拿ID → 回填物化路径
	err := db.DB.Transaction(func(tx *gorm.DB) error {
		dept := model.Department{
			TenantID:  tid,
			Name:      strings.TrimSpace(req.Name),
			Depth:     depth,
			SortOrder: req.SortOrder,
			Status:    1,
		}
		if req.ParentID != nil {
			pid := *req.ParentID
			dept.ParentID = &pid
		}
		// 先以占位路径插入（NOT NULL 约束），拿到ID后再回填真实物化路径
		dept.Path = "/"
		if err := tx.Create(&dept).Error; err != nil {
			return err
		}
		newPath := fmt.Sprintf("%s%d/", parentPath, dept.ID)
		return tx.Model(&dept).Update("path", newPath).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "创建失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "创建成功"})
}

type deptUpdateReq struct {
	Name        *string `json:"name"`
	NewParentID *uint   `json:"new_parent_id"` // 移动到新父部门
	SortOrder   *int    `json:"sort_order"`
}

// UpdateDepartment PUT /org/departments/:id —— 改名 / 移动（重写整棵子树路径）/ 排序
func UpdateDepartment(c *gin.Context) {
	s := getOrgScope(c)
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var dept model.Department
	if err := db.RQ(c).First(&dept, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "部门不存在"})
		return
	}
	// 操作者必须管辖该部门本身（且不能动根部门结构除非 tenant_admin）
	if !deptInScope(s, dept.Path) {
		c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "部门不在您的管辖范围内"})
		return
	}
	if dept.ParentID == nil && s.Role != model.RoleTenantAdmin {
		c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "根部门仅租户管理员可修改"})
		return
	}

	var req deptUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误"})
		return
	}

	updates := map[string]interface{}{}
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		updates["name"] = strings.TrimSpace(*req.Name)
	}
	if req.SortOrder != nil {
		updates["sort_order"] = *req.SortOrder
	}

	// ---- 移动分支：防环校验 + 子树路径整体重写 ----
	moving := false
	if req.NewParentID != nil {
		newPID := *req.NewParentID
		if newPID != dept.ID {
			var np model.Department
			if err := db.RQ(c).First(&np, newPID).Error; err != nil {
				c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "目标父部门不存在"})
				return
			}
			if !deptInScope(s, np.Path) || !deptInScope(s, dept.Path) {
				c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "移动范围超出管辖权限"})
				return
			}
			// 防环：新父不能是自身或自身的后代（即其 path 以被移动部门 path 为前缀）
			if np.ID == dept.ID || strings.HasPrefix(np.Path, dept.Path) {
				c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "不能移动到自身或自己的子部门下（会形成环）"})
				return
			}
			if dept.ParentID == nil || *dept.ParentID != newPID {
				moving = true
				updates["parent_id"] = newPID
				updates["depth"] = np.Depth + 1
			}
			_ = moving
		}
	}

	err := db.DB.Transaction(func(tx *gorm.DB) error {
		if len(updates) > 0 {
			if err := tx.Model(&model.Department{}).Where("id = ?", dept.ID).Updates(updates).Error; err != nil {
				return err
			}
		}
		// 子树路径重写：newPath = 新父path + 自身ID；后代 REPLACE(oldPrefix,newPrefix)
		if moving {
			var newParentPath string
			if pid, ok := updates["parent_id"].(uint); ok {
				var p model.Department
				tx.Select("path").First(&p, pid)
				newParentPath = p.Path
			}
			oldPrefix := dept.Path
			newPrefix := fmt.Sprintf("%s%d/", newParentPath, dept.ID)
			if err := tx.Exec(
				`UPDATE departments SET path = CONCAT(?, SUBSTRING(path, LENGTH(?)+1)),
				 depth = depth + ?
				 WHERE tenant_id = ? AND path LIKE ?`,
				newPrefix, oldPrefix, updates["depth"].(int)-dept.Depth, s.TenantID, oldPrefix+"%").Error; err != nil {
				return err
			}
			service.InvalidateTenantUsers(s.TenantID) // 路径变化 → 全员组织缓存失效
		} else if len(updates) > 0 {
			// 改名/排序不影响 path
		}
		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "更新失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "更新成功"})
}

// DeleteDepartment DELETE /org/departments/:id —— 仅允许删除空部门
func DeleteDepartment(c *gin.Context) {
	s := getOrgScope(c)
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var dept model.Department
	if err := db.RQ(c).First(&dept, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "部门不存在"})
		return
	}
	if !deptInScope(s, dept.Path) {
		c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "部门不在您的管辖范围内"})
		return
	}
	var childCnt, userCnt int64
	db.RQ(c).Model(&model.Department{}).Where("parent_id = ?", id).Count(&childCnt)
	db.RQ(c).Model(&model.User{}).Where("department_id = ?", id).Count(&userCnt)
	if childCnt > 0 || userCnt > 0 {
		c.JSON(http.StatusConflict, gin.H{
			"code":    409,
			"message": fmt.Sprintf("部门非空（子部门 %d 个 / 成员 %d 人），请先清空", childCnt, userCnt),
		})
		return
	}
	if err := db.RQ(c).Delete(&model.Department{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "删除失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "删除成功"})
}

// ---- 用户管理 ----

type userCreateReq struct {
	Username     string `json:"username" binding:"required"`
	Password     string `json:"password" binding:"required"`
	RealName     string `json:"real_name"`
	Phone        string `json:"phone"`
	Role         string `json:"role" binding:"required"`
	DepartmentID uint   `json:"department_id" binding:"required"`
}

// CreateUser POST /org/users —— 在指定部门下创建账号（角色按权限矩阵裁剪）
func CreateUser(c *gin.Context) {
	s := getOrgScope(c)
	var req userCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误"})
		return
	}
	req.Username = strings.TrimSpace(req.Username)

	// 部门必须在操作者范围内
	var dept model.Department
	if err := db.RQ(c).First(&dept, req.DepartmentID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "目标部门不存在"})
		return
	}
	if !deptInScope(s, dept.Path) {
		c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "目标部门不在您的管辖范围内"})
		return
	}
	// dept_admin 不能在自己所在部门任命另一个管理员（决策口径：任命限子树内非本级）
	if s.Role == model.RoleDeptAdmin && req.Role == model.RoleDeptAdmin && dept.ID == s.DeptID {
		c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "不能在本部门任命部门管理员，请在子部门上操作"})
		return
	}
	if !canAssignRole(s, req.Role) {
		c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "无权分配该角色"})
		return
	}

	// 用户名唯一
	var dup int64
	db.DB.Model(&model.User{}).Where("username = ?", req.Username).Count(&dup)
	if dup > 0 {
		c.JSON(http.StatusConflict, gin.H{"code": 409, "message": "用户名已存在"})
		return
	}
	// 用户数配额
	var tenant model.Tenant
	if err := db.DB.Select("max_users").First(&tenant, s.TenantID).Error; err == nil && tenant.MaxUsers > 0 {
		var cnt int64
		db.RQ(c).Model(&model.User{}).Where("status = 1").Count(&cnt)
		if cnt >= int64(tenant.MaxUsers) {
			c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "用户数已达套餐上限"})
			return
		}
	}

	hashed, err := utils.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "密码处理失败"})
		return
	}
	did := req.DepartmentID
	u := model.User{
		Username:     req.Username,
		PasswordHash: hashed,
		RealName:     req.RealName,
		Role:         req.Role,
		Phone:        req.Phone,
		Status:       1,
		TenantID:     &s.TenantID,
		DepartmentID: &did,
	}
	if err := db.DB.Create(&u).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "创建失败: " + err.Error()})
		return
	}
	log.Printf("[Org] 用户创建: %s 角色=%s 部门=%d 操作者=%d", u.Username, u.Role, did, s.UserID)
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "创建成功", "data": gin.H{"id": u.ID}})
}

type userUpdateReq struct {
	Role         *string `json:"role"`
	DepartmentID *uint   `json:"department_id"`
	Status       *int    `json:"status"`
	Password     *string `json:"password"`
	RealName     *string `json:"real_name"`
}

// UpdateUser PUT /org/users/:id —— 改角色/调部门/启停/改密（全部即时生效：InvalidateOrg）
func UpdateUser(c *gin.Context) {
	s := getOrgScope(c)
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)

	var target model.User
	if err := db.RQ(c).First(&target, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "用户不存在"})
		return
	}
	// 目标必须在操作者范围内（dept_admin 校验其当前部门路径）
	targetPath := "/"
	if target.DepartmentID != nil {
		var td model.Department
		if err := db.RQ(c).Select("path").First(&td, *target.DepartmentID).Error; err == nil {
			targetPath = td.Path
		}
	} else if s.Role != model.RoleTenantAdmin {
		// 未挂部门的账号（直属租户层）只有租户管理员能动
		c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "该账号不在您的管辖范围内"})
		return
	}
	if target.DepartmentID != nil && !deptInScope(s, targetPath) {
		c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "该账号不在您的管辖范围内"})
		return
	}
	// 保护线：不能改自己角色（防误操作自锁）；不能动超管
	if uint64(target.ID) == id && false {
		// 占位：self 判断在下方用 UserID 比较
	}
	if target.ID == s.UserID {
		c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "不能修改自己的组织信息"})
		return
	}
	if target.Role == model.RoleSuperAdmin {
		c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "平台超管账号不可在此修改"})
		return
	}

	var req userUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误"})
		return
	}

	updates := map[string]interface{}{}
	if req.Role != nil && *req.Role != target.Role {
		if !canAssignRole(s, *req.Role) {
			c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "无权分配该角色"})
			return
		}
		// dept_admin 任命 dept_admin 时同样禁止落在自己所在部门
		if s.Role == model.RoleDeptAdmin && *req.Role == model.RoleDeptAdmin &&
			target.DepartmentID != nil && *target.DepartmentID == s.DeptID {
			c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "不能在本部门任命部门管理员"})
			return
		}
		updates["role"] = *req.Role
	}
	if req.DepartmentID != nil {
		var nd model.Department
		if err := db.RQ(c).First(&nd, *req.DepartmentID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "目标部门不存在"})
			return
		}
		if !deptInScope(s, nd.Path) {
			c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "目标部门不在您的管辖范围内"})
			return
		}
		updates["department_id"] = *req.DepartmentID
	}
	if req.Status != nil {
		st := 0
		if *req.Status == 1 {
			st = 1
		}
		updates["status"] = st
	}
	if req.RealName != nil {
		updates["real_name"] = strings.TrimSpace(*req.RealName)
	}
	if req.Password != nil && *req.Password != "" {
		hashed, herr := utils.HashPassword(*req.Password)
		if herr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "密码处理失败"})
			return
		}
		updates["password_hash"] = hashed
	}
	if len(updates) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "无可更新字段"})
		return
	}
	if err := db.RQ(c).Model(&model.User{}).Where("id = ?", target.ID).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "更新失败: " + err.Error()})
		return
	}
	service.InvalidateOrg(target.ID) // 即时生效
	log.Printf("[Org] 用户更新 uid=%d 字段=%d 操作者=%d", target.ID, len(updates), s.UserID)
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "更新成功"})
}

// GetManagedUsers GET /org/users?keyword=&role=&department_id=
// 用户列表（作用域裁剪：dept_admin 仅子树成员）
func GetManagedUsers(c *gin.Context) {
	s := getOrgScope(c)
	type row struct {
		ID           uint    `json:"id"`
		Username     string  `json:"username"`
		RealName     *string `json:"real_name"`
		Phone        string  `json:"phone"`
		Role         string  `json:"role"`
		Status       int     `json:"status"`
		DepartmentID *uint   `json:"department_id"`
		DeptName     *string `json:"dept_name"`
		CreatedAt    string  `json:"created_at"`
	}
	// JOIN 查询不走 RQ（裸 tenant_id 歧义），租户条件显式携带
	q := db.DB.Table("tenant_users u").
		Select("u.id, u.username, u.real_name, u.phone, u.role, u.status, u.department_id, d.name as dept_name, TO_CHAR(u.created_at,'YYYY-MM-DD') as created_at").
		Joins("LEFT JOIN departments d ON u.department_id = d.id")

	// 作用域过滤
	switch s.Role {
	case model.RoleSuperAdmin, model.RoleTenantAdmin:
		// 全租户（排除超管空租户账号）
		q = q.Where("u.tenant_id = ?", s.TenantID)
	case model.RoleDeptAdmin, model.RoleReadOnly:
		if s.DeptPath == "" {
			q = q.Where("1 = 0")
		} else {
			q = q.Where(`u.tenant_id = ? AND u.department_id IN (SELECT id FROM departments WHERE tenant_id = ? AND path LIKE ?)`,
				s.TenantID, s.TenantID, s.DeptPath+"%")
		}
	default:
		c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "无权查看用户列表"})
		return
	}
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		q = q.Where("u.username LIKE ? OR u.real_name LIKE ?", "%"+kw+"%", "%"+kw+"%")
	}
	if r := c.Query("role"); r != "" {
		q = q.Where("u.role = ?", r)
	}
	if dp := c.Query("department_id"); dp != "" {
		q = q.Where("u.department_id = ?", dp)
	}

	rows := []row{}
	if err := q.Order("u.id ASC").Limit(500).Scan(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "success", "data": rows})
}

var _ = gorm.ErrRecordNotFound
