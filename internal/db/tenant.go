// Package db 数据库连接、自动迁移、租户上下文注入与写入自动盖章（RQ/PQ/T/WithPreset/DataScope）。
package db

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ============================================================
// 租户查询注入工具（SaaS 安全红线版）
//
// 语义说明（配合 middleware.TenantResolver + TenantConsistency 使用）：
// - 所有业务路由经中间件链后，Context 中 tenant_id 必为 >0 的生效租户
//   （匿名/C端=Host解析租户；登录态=一致性校验后的租户）
// - T(c) 仅做机械注入，不再打印日志（高频接口日志洪水），不再有超管全透传
//   （超管跨租户由 TenantConsistency 显式裁决并审计，Context 中永远是具体租户）
// - Context 无 tenant_id（白名单路由/系统内部调用）→ 不过滤，交由调用方自证
// ============================================================

// T 从 gin.Context 自动取出生效租户并封装为 GORM scope
// 用法：db.DB.Where("status = ?", 1).Scopes(db.T(c))
// 效果：自动在 SQL 中添加 "WHERE tenant_id = ?"
func T(c *gin.Context) func(db *gorm.DB) *gorm.DB {
	return func(tx *gorm.DB) *gorm.DB {
		v, exists := c.Get("tenant_id")
		if !exists {
			// 白名单路由/系统内部调用：无租户上下文，不过滤
			return tx
		}
		id, ok := v.(uint)
		if !ok || id == 0 {
			// H2：正常业务路由不会出现 0（中间件链保证）。出现且非平台超管，
			// 视为漏挂 TenantResolver 的异常调用方，fail-closed 返回空结果集。
			if failClosedOnMissingTenant(c) {
				return tx.Where("1 = 0")
			}
			return tx
		}
		return tx.Where("tenant_id = ?", id)
	}
}

// WithPreset 租户私有数据 + 系统预置数据(tenant_id=0)联合可见
// 仅用于预置语义表：tags / templates / features / brands / car_models /
// flow_definitions 等系统预置(0)+租户私有(>0)共存的表
// 用法：db.DB.Scopes(db.WithPreset(tid)).Find(&tags)
func WithPreset(tenantID uint) func(db *gorm.DB) *gorm.DB {
	return func(tx *gorm.DB) *gorm.DB {
		if tenantID == 0 {
			return tx
		}
		return tx.Where("tenant_id IN ?", []uint{tenantID, 0})
	}
}

// TenantFilter 按 tenantID 创建 GORM scope，用于 Engine 等非 per-request 场景
// tenantID=0 时返回原 db（无过滤，查所有租户）——引擎内部语义，与 T(c) 不同
func TenantFilter(tenantID uint) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if tenantID == 0 {
			return db
		}
		return db.Where("tenant_id = ?", tenantID)
	}
}
