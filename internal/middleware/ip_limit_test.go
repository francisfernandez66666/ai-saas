// ip_limit 单测（P1-4 限流 Redis 双轨，2026-09-15）：
// 单元环境 redisclient 未 Init → IsEnabled()=false，中间件自动走内存兜底轨——
// 本文件验证内存轨的固定窗口语义（首放行/超限 429/窗口重置放行）与 429 响应契约；
// Redis 轨共用同一判定表达式（n > int64(limit)），语义一致性由 smoke 多实例场景回归兜底。
package middleware

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"ai-scrm/internal/redisclient"

	"github.com/gin-gonic/gin"
)

// resetIPLimitState 清空限流表（用例间隔离）
func resetIPLimitState(t *testing.T) {
	t.Helper()
	ipLimitMu.Lock()
	ipLimitMap = map[string]*ipWindow{}
	ipLimitDirty = ipLimitDirty[:0]
	lastSweepAt = time.Time{}
	ipLimitMu.Unlock()
}

// newLimitRouter 构建挂载限流中间件的最小路由（固定 ClientIP 便于断言）
func newLimitRouter(bucket string, limit int, window time.Duration) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Request.RemoteAddr = "9.9.9.9:1234"; c.Next() })
	r.GET("/rl", IPRateLimit(bucket, limit, window), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"code": 0})
	})
	return r
}

// TestMemoryRateLimit_WindowSemantics 内存兜底轨固定窗口：limit 内放行、第 limit+1 次 429、窗口重置后放行
func TestMemoryRateLimit_WindowSemantics(t *testing.T) {
	resetIPLimitState(t)
	const limit = 3
	r := newLimitRouter("utwin", limit, 50*time.Millisecond)

	hit := func() int {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/rl", nil)
		r.ServeHTTP(w, req)
		return w.Code
	}

	// limit 次内全部放行
	for i := 1; i <= limit; i++ {
		if got := hit(); got != http.StatusOK {
			t.Fatalf("第 %d 次请求应放行(200)，实际 %d", i, got)
		}
	}
	// 第 limit+1 次超限
	if got := hit(); got != http.StatusTooManyRequests {
		t.Fatalf("超限请求应 429，实际 %d", got)
	}
	// 窗口过期后重新放行（固定窗口重置）
	time.Sleep(60 * time.Millisecond)
	if got := hit(); got != http.StatusOK {
		t.Fatalf("窗口重置后应放行(200)，实际 %d", got)
	}
}

// TestMemoryRateLimit_TenantIsolation 同一 IP 不同租户计数互不影响（key 含 tenantID 维度）
func TestMemoryRateLimit_TenantIsolation(t *testing.T) {
	resetIPLimitState(t)
	const limit = 1
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Request.RemoteAddr = "8.8.8.8:1234"
		// 模拟 TenantResolver 注入：?tid= 区分租户
		tid, _ := strconv.ParseUint(c.Query("tid"), 10, 64)
		c.Set("tenant_id", uint(tid))
		c.Next()
	})
	r.GET("/rl", IPRateLimit("utiso", limit, time.Minute), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"code": 0})
	})

	hit := func(tid string) int {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/rl?tid="+tid, nil)
		r.ServeHTTP(w, req)
		return w.Code
	}

	if got := hit("1"); got != http.StatusOK {
		t.Fatalf("租户1首次应放行，实际 %d", got)
	}
	if got := hit("1"); got != http.StatusTooManyRequests {
		t.Fatalf("租户1第二次应 429，实际 %d", got)
	}
	if got := hit("2"); got != http.StatusOK {
		t.Fatalf("租户2独立计数应放行，实际 %d", got)
	}
}

// TestMemoryRateLimit_ConcurrentSafety 并发计数不超卖：limit=10，20 并发后放行数恰为 10
func TestMemoryRateLimit_ConcurrentSafety(t *testing.T) {
	resetIPLimitState(t)
	const limit, total = 10, 20
	r := newLimitRouter("utconc", limit, time.Minute)

	var mu sync.Mutex
	passed := 0
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/rl", nil)
			r.ServeHTTP(w, req)
			if w.Code == http.StatusOK {
				mu.Lock()
				passed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if passed != limit {
		t.Fatalf("并发下放行数应恰为 %d，实际 %d（超卖/少卖）", limit, passed)
	}
}

// TestMemoryRateLimit_FallbackWhenRedisDisabled Redis 未启用时中间件不可 5xx（降级内存轨无感）
func TestMemoryRateLimit_FallbackWhenRedisDisabled(t *testing.T) {
	resetIPLimitState(t)
	// 单元环境 redisclient 未 Init：IsEnabled() 恒 false，走内存轨
	if redisclient.IsEnabled() {
		t.Skip("Redis 已启用，跳过内存降级断言（CI 用真 Redis 时不适用）")
	}
	r := newLimitRouter("utfb", 2, time.Minute)
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/rl", nil)
		r.ServeHTTP(w, req)
		if w.Code >= 500 {
			t.Fatalf("Redis 关闭时限流不应 5xx，第 %d 次实际 %d", i+1, w.Code)
		}
	}
}
