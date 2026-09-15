// B2 修复(2026-09-14)：WS 顾问推送数据范围注入——realtime.Hub 投递前询问 api 层
// "该顾问连接能否看到该客户的事件"，与 HTTP 侧 customerInDataScope/db.DataScope 同语义。
// realtime 包保持零 db 依赖，判定函数在此实现并经 init 注入。
package api

import (
	"sync"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/realtime"
)

// ownerCache 客户归属缓存（事件风暴下避免每事件查库；10s TTL，改派后短暂窗口可接受——
// 推送本就有轮询兜底，窗口外拉取即对齐）
type ownerEntry struct {
	assignedUserID uint
	cachedAt       time.Time
}

var (
	ownerCacheMu sync.Mutex
	ownerCache   = map[uint]ownerEntry{}
)

// customerOwner 查客户当前归属销售（缓存 10s；查不到返回 0,false=fail-closed）
func customerOwner(customerID uint) (uint, bool) {
	ownerCacheMu.Lock()
	if e, ok := ownerCache[customerID]; ok && time.Since(e.cachedAt) < 10*time.Second {
		ownerCacheMu.Unlock()
		return e.assignedUserID, true
	}
	ownerCacheMu.Unlock()
	var cust model.Customer
	if err := db.DB.Select("id, assigned_user_id").First(&cust, customerID).Error; err != nil {
		return 0, false
	}
	ownerCacheMu.Lock()
	ownerCache[customerID] = ownerEntry{assignedUserID: cust.AssignedUserID, cachedAt: time.Now()}
	ownerCacheMu.Unlock()
	return cust.AssignedUserID, true
}

// deptOwnerInSubtree 判断客户归属人是否落在 deptPath 子树（与 customerInDataScope 同 SQL 口径）
func deptOwnerInSubtree(tenantID uint, owner uint, deptPath string) bool {
	if owner == 0 || deptPath == "" {
		return false
	}
	var cnt int64
	db.DB.Table("tenant_users u").
		Joins("LEFT JOIN departments d ON u.department_id = d.id").
		Where("u.tenant_id = ? AND u.id = ? AND d.path LIKE ?", tenantID, owner, deptPath+"%").
		Count(&cnt)
	return cnt > 0
}

// advisorWSVisible realtime.AdvisorScopeFunc 实现（B2）
func advisorWSVisible(role, deptPath string, userID, tenantID, customerID uint) bool {
	switch role {
	case model.RoleSuperAdmin, model.RoleTenantAdmin:
		return true
	case model.RoleUser:
		owner, ok := customerOwner(customerID)
		return ok && owner != 0 && owner == userID
	case model.RoleDeptAdmin:
		owner, ok := customerOwner(customerID)
		return ok && deptOwnerInSubtree(tenantID, owner, deptPath)
	case model.RoleReadOnly:
		// 只读账号与 sales 同档（仅本人名下），无归属即不可见
		owner, ok := customerOwner(customerID)
		return ok && owner != 0 && owner == userID
	}
	// 未知角色/无角色：fail-closed（系统连接如需全收须显式给 tenant_admin 角色）
	return false
}

func init() {
	realtime.SetAdvisorScope(advisorWSVisible)
}
