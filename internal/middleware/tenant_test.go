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

// TestIsPlatformSuperPath A1 修复回归(2026-09-11)：super_admin 无 X-Tenant-ID 时的
// 平台级路径白名单判定——P2-15"无头一律400"未区分平台/租户语义，曾致超管台与四套 E2E 全红。
// 口径：/super、/auth 全放行；/admin/config 放行但 rollback（租户覆盖层操作）除外；
// 租户作用域路径（/org、/admin/apikeys 等）不在白名单，仍强制显式 X-Tenant-ID。
func TestIsPlatformSuperPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/api/v1/super/tenants", true},
		{"/api/v1/super/orders/pending", true},
		{"/api/v1/auth/me", true},
		{"/api/v1/auth/change-password", true},
		{"/api/v1/admin/config", true},
		{"/api/v1/admin/config?category=billing", true},
		{"/api/v1/admin/config/rollback", false}, // 回滚本租户覆盖层，需显式租户语境
		{"/api/v1/org/departments/tree", false},  // 租户作用域：必须带 X-Tenant-ID
		{"/api/v1/admin/apikeys", false},
		{"/api/v1/customers", false},
		{"/api/v1/super", true},
	}
	for _, c := range cases {
		if got := isPlatformSuperPath(c.path); got != c.want {
			t.Errorf("isPlatformSuperPath(%q)=%v，期望 %v", c.path, got, c.want)
		}
	}
}
