// Package middleware 租户解析中间件单元测试（P2-1 自动化测试，2026-08-30）
package middleware

import (
	"ai-scrm/internal/model"
	"testing"
	"time"
)

// ============================================================
// 租户缓存单元测试
//
// 测试覆盖：
//   1. 缓存读写
//   2. 缓存过期
//   3. 缓存失效
//   4. 负缓存
// ============================================================

// TestSetCachedTenant 测试缓存写入
func TestSetCachedTenant(t *testing.T) {
	InvalidateTenantCache()

	// 写入 nil（负缓存）
	setCachedTenant("test:nil", nil)

	// 写入非 nil
	setCachedTenant("test:exists", &model.Tenant{ID: 1})

	// 验证缓存存在
	if v := getCachedTenant("test:nil"); v != nil {
		t.Fatal("负缓存应返回 nil")
	}
	if v := getCachedTenant("test:exists"); v == nil {
		t.Fatal("缓存应存在")
	}
}

// TestCacheExpiration 测试缓存过期
func TestCacheExpiration(t *testing.T) {
	InvalidateTenantCache()

	setCachedTenant("test:expire", &model.Tenant{ID: 1})

	// 立即读取应命中
	if v := getCachedTenant("test:expire"); v == nil {
		t.Fatal("缓存应命中")
	}

	// 手动设置过期时间（模拟过期）
	key := "test:expire"
	if v, ok := resolveCache.Load(key); ok {
		entry := v.(*tenantCacheEntry)
		entry.expireAt = time.Now().Add(-1 * time.Second)
		resolveCache.Store(key, entry)
	}

	// 再次读取应返回 nil（过期）
	if v := getCachedTenant("test:expire"); v != nil {
		t.Fatal("过期缓存应返回 nil")
	}
}

// TestInvalidateTenantCache 测试缓存失效
func TestInvalidateTenantCache(t *testing.T) {
	InvalidateTenantCache()

	setCachedTenant("test:invalidate", &model.Tenant{ID: 1})

	if v := getCachedTenant("test:invalidate"); v == nil {
		t.Fatal("缓存应存在")
	}

	InvalidateTenantCache()

	if v := getCachedTenant("test:invalidate"); v != nil {
		t.Fatal("缓存应已清除")
	}
}

// TestNegativeCache 测试负缓存
func TestNegativeCache(t *testing.T) {
	InvalidateTenantCache()

	setCachedTenant("test:negative", nil)

	if v := getCachedTenant("test:negative"); v != nil {
		t.Fatal("负缓存应返回 nil")
	}

	// 验证过期时间约为 10 秒
	key := "test:negative"
	if v, ok := resolveCache.Load(key); ok {
		entry := v.(*tenantCacheEntry)
		remaining := time.Until(entry.expireAt)
		if remaining < 9*time.Second || remaining > 11*time.Second {
			t.Fatalf("负缓存过期时间应约为 10s，实际 %v", remaining)
		}
	}
}

// TestTenantCacheTTL 测试缓存 TTL 常量
func TestTenantCacheTTL(t *testing.T) {
	if tenantCacheTTL != 30*time.Second {
		t.Fatalf("正缓存 TTL 应为 30s，实际 %v", tenantCacheTTL)
	}
	if tenantNegCacheTTL != 10*time.Second {
		t.Fatalf("负缓存 TTL 应为 10s，实际 %v", tenantNegCacheTTL)
	}
}
