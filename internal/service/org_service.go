package service

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/redisclient"

	"gorm.io/gorm"
)

// ============================================================
// 组织上下文服务（四级用户体系）
//
// 职责：按用户ID加载【实时】角色/部门/路径，供中间件与数据范围隔离使用。
// 为什么不走 JWT？角色降级/调部门必须即时生效——DB 为准 + 双层缓存：
//   1) 进程内 30s TTL 缓存（无 Redis 时的兜底时效）
//   2) Redis user_ver:{uid} 版本戳：管理端改角色/调部门即 INCR，
//      各实例下次请求比对版本不一致立即重载（近实时踢生效）
// ============================================================

// OrgContext 用户组织上下文
type OrgContext struct {
	UserID             uint
	TenantID           uint
	Role               string // super_admin/tenant_admin/dept_admin/user/readonly（DB 实时值）
	Status             int    // 1=正常 0=禁用
	DeptID             uint   // 挂载部门（0=未挂载，仅 tenant_admin 允许）
	DeptPath           string // 物化路径 "/1/5/"；直属租户层为空串
	MustChangePassword bool   // 首登强制改密标记（M3，改密成功后清除）
}

type orgCacheEntry struct {
	ver      int64       // 加载时的 user_ver 版本（Redis 版本戳）
	oc       *OrgContext // nil=负缓存（用户不存在）
	expireAt time.Time
}

const (
	orgCacheTTL     = 30 * time.Second
	orgVerKeyPrefix = "userv:"
)

var (
	orgCache sync.Map // userID(uint) → *orgCacheEntry
)

// LoadOrgContext 加载组织上下文（带双层缓存），用户不存在返回 nil
func LoadOrgContext(userID uint) *OrgContext {
	if userID == 0 {
		return nil
	}
	// 读本地缓存
	if v, ok := orgCache.Load(userID); ok {
		e := v.(*orgCacheEntry)
		fresh := time.Now().Before(e.expireAt)
		verOK := true
		if redisclient.IsEnabled() {
			if s, ok := redisclient.Get(orgVerKeyPrefix + strconv.FormatUint(uint64(userID), 10)); ok {
				if n, err := strconv.ParseInt(s, 10, 64); err == nil && n != e.ver {
					verOK = false // 版本被其他实例/本实例管理操作推进
				}
			}
		}
		if fresh && verOK {
			return e.oc
		}
		orgCache.Delete(userID)
	}

	oc := loadOrgFromDB(userID)
	ver := int64(0)
	if redisclient.IsEnabled() {
		if s, ok := redisclient.Get(orgVerKeyPrefix + strconv.FormatUint(uint64(userID), 10)); ok {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				ver = n
			}
		}
	}
	if oc == nil {
		orgCache.Store(userID, &orgCacheEntry{ver: ver, oc: nil, expireAt: time.Now().Add(5 * time.Second)})
		return nil
	}
	orgCache.Store(userID, &orgCacheEntry{ver: ver, oc: oc, expireAt: time.Now().Add(orgCacheTTL)})
	return oc
}

// loadOrgFromDB 查库组装（LEFT JOIN 部门取 path）
func loadOrgFromDB(userID uint) *OrgContext {
	var row struct {
		ID                 uint
		TenantID           *uint
		Role               string
		Status             int
		DeptID             *uint
		Path               *string
		MustChangePassword bool
	}
	err := db.DB.Table("tenant_users u").
		Select("u.id, u.tenant_id, u.role, u.status, u.department_id as dept_id, d.path as path, COALESCE(u.must_change_password,false) as must_change_password").
		Joins("LEFT JOIN departments d ON u.department_id = d.id").
		Where("u.id = ?", userID).
		Take(&row).Error
	if err != nil {
		if err != gorm.ErrRecordNotFound {
			log.Printf("[Org] 加载用户上下文失败 uid=%d: %v", userID, err)
		}
		return nil
	}
	oc := &OrgContext{
		UserID:             row.ID,
		Role:               row.Role,
		Status:             row.Status,
		DeptPath:           "",
		MustChangePassword: row.MustChangePassword,
	}
	if row.TenantID != nil {
		oc.TenantID = *row.TenantID
	}
	if row.DeptID != nil {
		oc.DeptID = *row.DeptID
	}
	if row.Path != nil {
		oc.DeptPath = *row.Path
	}
	return oc
}

// InvalidateOrg 用户组织信息变更后调用：删本地缓存 + 推进 Redis 版本戳
// （多实例下其他实例在下一次请求比对版本即失效）
func InvalidateOrg(userID uint) {
	orgCache.Delete(userID)
	if redisclient.IsEnabled() {
		key := orgVerKeyPrefix + strconv.FormatUint(uint64(userID), 10)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if c := redisclient.Client(); c != nil {
			_ = c.Incr(ctx, key).Err()
		}
	}
}

// InvalidateTenantUsers 批量失效（部门移动重写子树 path 后，子树全部用户路径变化）
func InvalidateTenantUsers(tenantID uint) {
	var ids []uint
	if err := db.DB.Model(&model.User{}).Where("tenant_id = ?", tenantID).Pluck("id", &ids).Error; err != nil {
		return
	}
	for _, id := range ids {
		InvalidateOrg(id)
	}
}
