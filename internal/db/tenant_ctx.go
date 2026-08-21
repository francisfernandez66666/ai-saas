package db

import (
	"context"
	"log"

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
