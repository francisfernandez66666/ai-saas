// Package db 数据库连接、自动迁移、租户上下文注入与写入自动盖章（RQ/PQ/T/WithPreset/DataScope）。
package db

import (
	"context"
	"log"

	"ai-scrm/internal/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ============================================================
// 租户上下文传递 + 写入自动盖章（Phase P1）
//
// 链路：中间件解析出生效租户 → 写入 request context →
//   ① 读：db.RQ(c) 会话自带 WHERE tenant_id=?（防跨租户读/改/删）
//   ② 写：GORM 全局 Create 回调从 stmt.Context 取租户，
//      模型含 TenantID 字段且为零值时自动填充（防数据落错租户）
//
// 好处：28+ 个 Create 调用点零改动即获得租户归属；新增模型默认生效
// ============================================================

// tenantCtxKey 租户上下文键类型（私有空结构体，避免与其他包的 context key 冲突）
type tenantCtxKey struct{}

// WithTenant 把租户ID写入 context（中间件调用）
func WithTenant(ctx context.Context, tenantID uint) context.Context {
	return context.WithValue(ctx, tenantCtxKey{}, tenantID)
}

// TenantFromContext 从 context 取租户ID（无则 0）
func TenantFromContext(ctx context.Context) uint {
	if ctx == nil {
		return 0
	}
	if v, ok := ctx.Value(tenantCtxKey{}).(uint); ok {
		return v
	}
	return 0
}

// registerTenantStampCallback 注册全局 Create 回调：写入自动盖租户章
// 规则：context 有租户 && 模型有 TenantID 字段 && 字段为零值 → 填充为上下文租户
// （显式指定了非零 TenantID 的写入不受影响，保留人工覆盖能力）
func registerTenantStampCallback(gdb *gorm.DB) {
	err := gdb.Callback().Create().Before("gorm:create").Register("tenant:auto_stamp", func(tx *gorm.DB) {
		tid := TenantFromContext(tx.Statement.Context)
		if tid == 0 || tx.Statement == nil || tx.Statement.Schema == nil {
			return
		}
		f := tx.Statement.Schema.LookUpField("TenantID")
		if f == nil {
			return // 模型无租户字段（如 tenants 表自身）
		}
		cur, isZero := f.ValueOf(tx.Statement.Context, tx.Statement.ReflectValue)
		if isZero || cur == 0 {
			tx.Statement.SetColumn("TenantID", tid)
		}
	})
	if err != nil {
		log.Printf("[tenant] 注册写入盖章回调失败: %v", err)
	} else {
		log.Println("[tenant] 写入自动盖章回调已注册（context 租户 → TenantID 零值填充）")
	}
}

// EffectiveTenantIDFromGin 从 gin.Context 读生效租户（与 middleware 写入的键约定一致）
// 放在 db 包避免 middleware→db 反向依赖
func EffectiveTenantIDFromGin(c *gin.Context) uint {
	if c == nil {
		return 0
	}
	v, exists := c.Get("tenant_id")
	if !exists {
		return 0
	}
	switch val := v.(type) {
	case uint:
		return val
	case int:
		return uint(val)
	case int64:
		return uint(val)
	case float64:
		return uint(val)
	}
	return 0
}

// PQ 预置可见读会话：租户私有(tenant_id=tid) + 系统预置(tenant_id=0) 联合可见
// 仅用于预置语义表的"读"：tags/templates/features/brands/car_models/
// model_specs/competitor_compares/knowledge_fragments/flow_definitions/tag_rules 等
// 这些表的"写"仍用 RQ（只能改自己的，保护系统预置数据不被租户篡改）
func PQ(c *gin.Context) *gorm.DB {
	tid := EffectiveTenantIDFromGin(c)

	var ctx context.Context
	if c.Request != nil && c.Request.Context() != nil {
		ctx = c.Request.Context()
	} else {
		ctx = context.Background()
	}
	if tid > 0 {
		ctx = WithTenant(ctx, tid)
	}

	q := DB.WithContext(ctx)
	if tid > 0 {
		q = q.Where("tenant_id IN ?", []uint{tid, 0})
	}
	return q
}

// RQ 租户会话：替代裸 db.DB 的标准入口
// - SELECT/UPDATE/DELETE 自动带 WHERE tenant_id=?（INSERT 不受 Where 影响）
// - 绑定请求上下文（Create 回调据此盖章）
// 用法：把原 db.DB.Xxx(...) 改为 db.RQ(c).Xxx(...)
// 注意：每次调用返回全新会话，可安全复用于多条独立查询
func RQ(c *gin.Context) *gorm.DB {
	tid := EffectiveTenantIDFromGin(c)

	// 上下文：优先沿用请求自身 context（保留超时/取消语义），注入租户
	var ctx context.Context
	if c.Request != nil && c.Request.Context() != nil {
		ctx = c.Request.Context()
	} else {
		ctx = context.Background()
	}
	if tid > 0 {
		ctx = WithTenant(ctx, tid)
	}

	q := DB.WithContext(ctx)
	if tid > 0 {
		q = q.Where("tenant_id = ?", tid)
	}
	return q
}

// ============================================================
// 数据范围隔离（四级组织体系 · 第二维，叠加在租户过滤之上）
//
// 角色可见范围（业务表：customers/conversations/follow_ups/test_drives）：
//   super_admin / tenant_admin → 本租户全部（RQ 已过滤）
//   dept_admin  → 归属销售位于"本部门子树"的客户（path 前缀子查询）
//   readonly    → 同其挂载部门的范围推导（挂根=全租户只读视角）
//   user        → 仅 assigned_user_id = 本人
// 依赖中间件注入的 Context 键：role / dept_path / user_id（OrgResolve 写入）
// ============================================================

// DataScope 数据范围 scope：与 RQ(c) 叠加使用
// 用法：db.RQ(c).Scopes(db.DataScope(c)).Model(&model.Customer{})
func DataScope(c *gin.Context) func(db *gorm.DB) *gorm.DB {
	return func(tx *gorm.DB) *gorm.DB {
		tid := EffectiveTenantIDFromGin(c)
		if tid == 0 {
			return tx
		}
		var roleStr string
		if v, ok := c.Get("role"); ok {
			roleStr, _ = v.(string)
		}
		switch roleStr {
		case model.RoleSuperAdmin, model.RoleTenantAdmin:
			return tx // 平台超管（已显式指定租户）/ 租户管理员：全租户
		case model.RoleDeptAdmin, model.RoleReadOnly:
			pathVal, _ := c.Get("dept_path")
			path, _ := pathVal.(string)
			if path == "" {
				// 未挂载部门：fail-closed，看不到任何业务数据
				return tx.Where("1 = 0")
			}
			// 子树范围：归属销售 ∈ 子树部门下用户集合（物化路径前缀匹配）
			return tx.Where(
				"assigned_user_id IN (SELECT id FROM tenant_users WHERE tenant_id = ? AND department_id IN (SELECT id FROM departments WHERE tenant_id = ? AND path LIKE ?))",
				tid, tid, path+"%")
		case model.RoleUser:
			var uid uint
			if v, ok := c.Get("user_id"); ok {
				switch val := v.(type) {
				case uint:
					uid = val
				case int:
					uid = uint(val)
				case int64:
					uid = uint(val)
				}
			}
			if uid == 0 {
				return tx.Where("1 = 0")
			}
			return tx.Where("assigned_user_id = ?", uid)
		default:
			// 未知角色 fail-closed
			return tx.Where("1 = 0")
		}
	}
}
