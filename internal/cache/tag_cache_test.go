// 标签缓存 TTL 兜底刷新单测（2026-09-09）：
//   - reloadLocal 成功后记录 lastReloadAt（TTL 判断依据）
//   - ttlStale：刚重载不陈旧，超 TTL 判定陈旧（触发兜底刷新）
package cache

import (
	"testing"
	"time"

	"ai-scrm/internal/testutil"
)

// TestTTLStale 最近重载后不陈旧；超 TTL 判定陈旧
func TestTTLStale(t *testing.T) {
	testutil.SetupTestDB(t)
	m := &TagCacheManager{}
	// 从未重载：lastReloadAt 零值 → 视为超时陈旧
	if !m.ttlStale(60 * time.Second) {
		t.Fatal("未初始化（零值 lastReloadAt）应判定为陈旧")
	}
	// 重载后：fresh，不应陈旧
	m.reloadLocal()
	if m.ttlStale(60 * time.Second) {
		t.Fatal("刚重载不应陈旧")
	}
	if m.lastReloadAt.IsZero() {
		t.Fatal("reloadLocal 应记录 lastReloadAt")
	}
	// 模拟 TTL 过期：把 lastReloadAt 拨到 61s 前
	m.mu.Lock()
	m.lastReloadAt = time.Now().Add(-61 * time.Second)
	m.mu.Unlock()
	if !m.ttlStale(60 * time.Second) {
		t.Fatal("超 TTL 应判定为陈旧")
	}
}
