// Package middleware Gin 中间件链：TenantResolver(fail-closed)→JWTAuth→TenantConsistency→OrgResolve 等安全闸。
package middleware

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 轻量内存限流中间件（固定窗口，P0安全止血 2026-08-26）
//
// 背景：/chat/test|guest|welcome 等免登录接口此前无任何频控，
// 可被匿名刷量（烧AI额度/DB写放大）。Turnstile 默认关闭时尤其裸奔。
//
// 设计取舍：
//   - 单实例内存计数（多实例部署下各实例独立计数，阈值按实例数折算即可，
//     全局分布式限流列入后续专项）
//   - 固定窗口实现简单、无依赖；key = bucket:tenant:ip
//   - 命中限流返回 429，不消耗下游资源
// ============================================================

type ipWindow struct {
	count   int
	resetAt time.Time
}

var (
	ipLimitMu   sync.Mutex
	ipLimitMap  = map[string]*ipWindow{} // key -> window
	lastSweepAt time.Time
)

// IPRateLimit 限流中间件工厂：window 窗口内每 IP（按租户隔离计数）最多 limit 次
func IPRateLimit(bucket string, limit int, window time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := bucket + ":" + strconv.FormatUint(uint64(EffectiveTenantID(c)), 10) + ":" + c.ClientIP()
		now := time.Now()

		ipLimitMu.Lock()
		w, ok := ipLimitMap[key]
		if !ok || now.After(w.resetAt) {
			ipLimitMap[key] = &ipWindow{count: 1, resetAt: now.Add(window)}
			ipLimitMu.Unlock()
		} else {
			w.count++
			over := w.count > limit
			ipLimitMu.Unlock()
			if over {
				c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
					"code":    429,
					"message": "请求太频繁，请稍后再试",
				})
				return
			}
		}

		// 惰性清理：每 10 分钟清一遍过期窗口，防止 map 无限增长
		ipLimitMu.Lock()
		if lastSweepAt.IsZero() || now.Sub(lastSweepAt) > 10*time.Minute {
			for k, win := range ipLimitMap {
				if now.After(win.resetAt) {
					delete(ipLimitMap, k)
				}
			}
			lastSweepAt = now
		}
		ipLimitMu.Unlock()

		c.Next()
	}
}

// CheckVisitorKey 访客身份校验（免登录接口的横向越权防线）
// 规则：已登录用户（user_id>0，B端顾问/管理员）直接放行；
// 匿名请求必须携带 ?visitor_key= 且与目标客户的 VisitorKey 完全一致。
// expectedKey 传目标客户已加载的 VisitorKey（空串=该客户无密钥，一律拒绝匿名访问）。
func CheckVisitorKey(c *gin.Context, expectedKey string) bool {
	if uidV, ok := c.Get("user_id"); ok {
		if uid, _ := uidV.(uint); uid > 0 {
			return true
		}
	}
	vk := c.Query("visitor_key")
	return vk != "" && expectedKey != "" && vk == expectedKey
}
