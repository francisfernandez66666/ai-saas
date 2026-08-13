package db

import (
	"log"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// T 从 gin.Context 自动取出 tenant_id 并封装为 GORM query wrapper
// 用法：db.DB.Where("status = ?", 1).Scopes(db.T(c))
// 效果：自动在 SQL 中添加 "WHERE tenant_id = ?"
// 如果 Context 中没有 tenant_id，则不添加条件（透传模式，用于免登录免租户接口）
// 如果 tenant_id=0（超级管理员），则不过滤，透传所有数据
func T(c *gin.Context) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		tenantID, exists := c.Get("tenant_id")
		if !exists {
			log.Printf("[db.T] Context 中未找到 tenant_id，将跳过 tenant 过滤")
			return db
		}

		id, ok := tenantID.(uint)
		if !ok {
			log.Printf("[db.T] tenant_id 类型断言失败: %v", tenantID)
			return db
		}

		// 超级管理员（tenant_id=0）直接透传，不添加租户过滤
		if id == 0 {
			log.Printf("[db.T] 超级管理员透传，不添加 tenant_id 过滤")
			return db
		}

		log.Printf("[db.T] 注入 tenant_id=%d 到 SQL 查询", id)
		return db.Scopes(func(tx *gorm.DB) *gorm.DB {
			return tx.Where("tenant_id = ?", id)
		})
	}
}

// TenantFilter 根据 tenantID 创建 GORM query wrapper，用于 Engine 等非 per-request 场景
// 当 tenantID=0 时，返回原 db（无过滤，查所有租户）
// 当 tenantID>0 时，添加 WHERE tenant_id = ? 过滤
func TenantFilter(tenantID uint) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if tenantID == 0 {
			return db
		}
		return db.Where("tenant_id = ?", tenantID)
	}
}