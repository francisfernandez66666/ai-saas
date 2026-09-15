// B2 修复(2026-09-14)：WS 顾问推送数据范围注入——realtime.Hub 投递前询问 api 层
// "该顾问连接能否看到该客户的事件"，与 HTTP 侧 customerInDataScope/db.DataScope 同语义。
// realtime 包保持零 db 依赖，判定函数在此实现并经 init 注入。
package api

import (
	"strconv"
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
	// refreshing 异步刷新去重标记（P2-7）
	refreshing bool
}

var (
	ownerCacheMu sync.Mutex
	ownerCache   = map[uint]ownerEntry{}
)

// customerOwner 查客户当前归属销售（缓存 10s；查不到返回 0,false=fail-closed）
// P2-7 修复(2026-09-15)：本函数被 realtime.Hub 持 RLock 的投递路径调用——同步 DB
// 抖动会卡住该租户全部 WS 投递。改为"过期先回旧值 + 后台异步刷新"，
// 锁内只可能命中一次主键查询（仅首见客户），其余路径纯内存。
func customerOwner(customerID uint) (uint, bool) {
	ownerCacheMu.Lock()
	if e, ok := ownerCache[customerID]; ok {
		if time.Since(e.cachedAt) < 10*time.Second {
			ownerCacheMu.Unlock()
			return e.assignedUserID, true
		}
		if !e.refreshing {
			e.refreshing = true
			ownerCache[customerID] = e
			go refreshCustomerOwner(customerID)
		}
		ownerCacheMu.Unlock()
		return e.assignedUserID, true // stale 兜底，10s 窗口语义本就可接受
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

// refreshCustomerOwner 后台刷新单个客户归属（失败保留旧值等下轮，不注入 0 误伤可见性）
func refreshCustomerOwner(customerID uint) {
	var cust model.Customer
	err := db.DB.Select("id, assigned_user_id").First(&cust, customerID).Error
	ownerCacheMu.Lock()
	defer ownerCacheMu.Unlock()
	if e, ok := ownerCache[customerID]; ok {
		e.refreshing = false
		if err == nil {
			e.assignedUserID = cust.AssignedUserID
			e.cachedAt = time.Now()
		}
		ownerCache[customerID] = e
	}
}

// deptOwnerInSubtree 判断客户归属人是否落在 deptPath 子树（与 customerInDataScope 同 SQL 口径）
func deptOwnerInSubtree(tenantID uint, owner uint, deptPath string) bool {
	if owner == 0 || deptPath == "" {
		return false
	}
	// P2-7 修复(2026-09-15)：同 customerOwner——JOIN 查询在 Hub RLock 投递路径执行，
	// 按 (tenant,owner,deptPath) 缓存 30s，过期回旧值异步刷新（改组/改派分钟级生效可接受，轮询兜底）。
	key := strconv.FormatUint(uint64(tenantID), 10) + "|" + strconv.FormatUint(uint64(owner), 10) + "|" + deptPath
	deptScopeMu.Lock()
	if e, ok := deptScopeCache[key]; ok {
		if time.Since(e.at) < 30*time.Second {
			deptScopeMu.Unlock()
			return e.visible
		}
		if !e.refreshing {
			e.refreshing = true
			deptScopeCache[key] = e
			go refreshDeptScope(key, tenantID, owner, deptPath)
		}
		deptScopeMu.Unlock()
		return e.visible
	}
	deptScopeMu.Unlock()
	visible := queryDeptInSubtree(tenantID, owner, deptPath)
	deptScopeMu.Lock()
	deptScopeCache[key] = deptEntry{visible: visible, at: time.Now()}
	deptScopeMu.Unlock()
	return visible
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

// ---- P2-7 附属实现（2026-09-15）----

type deptEntry struct {
	visible    bool
	at         time.Time
	refreshing bool
}

var (
	deptScopeMu    sync.Mutex
	deptScopeCache = map[string]deptEntry{}
)

// queryDeptInSubtree 实际查库：客户归属人是否落在部门子树（JOIN 不能走 RQ，防列名歧义）
func queryDeptInSubtree(tenantID, owner uint, deptPath string) bool {
	var cnt int64
	db.DB.Table("tenant_users u").
		Joins("LEFT JOIN departments d ON u.department_id = d.id").
		Where("u.tenant_id = ? AND u.id = ? AND d.path LIKE ?", tenantID, owner, deptPath+"%").
		Count(&cnt)
	return cnt > 0
}

// refreshDeptScope 后台刷新部门子树可见性判定
func refreshDeptScope(key string, tenantID, owner uint, deptPath string) {
	visible := queryDeptInSubtree(tenantID, owner, deptPath)
	deptScopeMu.Lock()
	defer deptScopeMu.Unlock()
	if e, ok := deptScopeCache[key]; ok {
		e.visible = visible
		e.at = time.Now()
		e.refreshing = false
		deptScopeCache[key] = e
	}
}
