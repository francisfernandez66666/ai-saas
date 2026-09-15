// Package middleware Gin 中间件链：TenantResolver(fail-closed)→JWTAuth→TenantConsistency→OrgResolve 等安全闸。
package middleware

import (
	"crypto/subtle"
	"net/http"
	"strconv"
	"sync"
	"time"

	"ai-scrm/internal/redisclient"

	"github.com/gin-gonic/gin"
)

// ============================================================
// IP 限流中间件（固定窗口，P0安全止血 2026-08-26）
//
// 背景：/chat/test|guest|welcome 等免登录接口此前无任何频控，
// 可被匿名刷量（烧AI额度/DB写放大）。Turnstile 默认关闭时尤其裸奔。
//
// P1-4 升级(2026-09-15，AUDIT_GAP_VERIFICATION §3.2)：Redis 双轨——
//   - Redis 启用：固定窗口计数落 Redis（IncrWithTTL，与注册防薅同款原语），
//     多副本共享同一计数，"实际限流阈值 = 配置值 × 副本数"的漂移根除；
//   - Redis 未启用/操作异常：自动降级进程内存计数（单实例语义不变），
//     与 billing.WebhookNonceSeen 同款"Redis 优先、内存兜底"双轨范式。
//
// 设计取舍：
//   - 固定窗口实现简单；key = bucket:tenant:ip（Redis 侧加 rl: 前缀隔离键空间）
//   - 命中限流返回 429，不消耗下游资源
// ============================================================

// ipWindow 单 IP 限流窗口：累计次数 + 窗口重置时刻
type ipWindow struct {
	count   int
	resetAt time.Time
}

// ipLimitMap 固定窗口限流表（bucket:tenant:ip → 窗口），ipLimitMu 保护并发访问，lastSweepAt 记录上次清扫时刻
var (
	ipLimitMu   sync.Mutex
	ipLimitMap  = map[string]*ipWindow{} // key -> window
	lastSweepAt time.Time
	// ipLimitDirty P2-2 修复：新增 key 的增量队列——清扫只遍历增量队列，
	// 不再对全表 10min 一次扫描持锁（高流量下清一遍海量 key 期间限流全阻塞）。
	ipLimitDirty []string
)

// IPRateLimit 限流中间件工厂：window 窗口内每 IP（按租户隔离计数）最多 limit 次
// 多实例部署须开启 REDIS_ENABLED，否则各实例独立计数（阈值按副本数折算）。
func IPRateLimit(bucket string, limit int, window time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := bucket + ":" + strconv.FormatUint(uint64(EffectiveTenantID(c)), 10) + ":" + c.ClientIP()

		// ---- Redis 全局轨：多副本共享计数 ----
		// IncrWithTTL 在未启用时返回 0（不会进此分支）、INCR 异常时也返回 0 → 降级内存，限流不裸奔。
		if redisclient.IsEnabled() {
			if n := redisclient.IncrWithTTL("rl:"+key, window); n > 0 {
				if n > int64(limit) {
					c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
						"code":    429,
						"message": "请求太频繁，请稍后再试",
					})
					return
				}
				c.Next()
				return
			}
		}

		// ---- 内存兜底轨：Redis 未启用/异常时的单实例语义 ----
		if memoryIncrLimit(key, limit, window) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"code":    429,
				"message": "请求太频繁，请稍后再试",
			})
			return
		}
		c.Next()
	}
}

// memoryIncrLimit 进程内固定窗口计数：返回 true=已超限。
// 保持原实现语义（两段锁：计数与惰性清扫分离，清扫只遍历增量队列不阻塞计数）。
// 抽函数便于单测直测窗口重置/超限语义。
func memoryIncrLimit(key string, limit int, window time.Duration) bool {
	now := time.Now()

	ipLimitMu.Lock()
	w, ok := ipLimitMap[key]
	if !ok || now.After(w.resetAt) {
		ipLimitMap[key] = &ipWindow{count: 1, resetAt: now.Add(window)}
		if !ok {
			ipLimitDirty = append(ipLimitDirty, key) // 新 key 记入增量队列
		}
		ipLimitMu.Unlock()
	} else {
		w.count++
		over := w.count > limit
		ipLimitMu.Unlock()
		if over {
			return true
		}
	}

	// 惰性清理：每 10 分钟扫增量队列清过期窗口（P2-2：不再全表扫描，防止 map 无限增长且不阻塞限流）
	ipLimitMu.Lock()
	if lastSweepAt.IsZero() || now.Sub(lastSweepAt) > 10*time.Minute {
		for _, k := range ipLimitDirty {
			if win, ok2 := ipLimitMap[k]; ok2 && now.After(win.resetAt) {
				delete(ipLimitMap, k)
			}
		}
		ipLimitDirty = ipLimitDirty[:0]
		lastSweepAt = now
	}
	ipLimitMu.Unlock()
	return false
}

// CheckVisitorKey 访客身份校验（免登录接口的横向越权防线）
// 规则：已登录用户（user_id>0，B端顾问/管理员）直接放行；
// 匿名请求必须携带 ?visitor_key= 且与目标客户的 VisitorKey 完全一致。
// expectedKey 传目标客户已加载的 VisitorKey（空串=该客户无密钥，一律拒绝匿名访问）。
// P2-16 修复：visitor_key 用 subtle.ConstantTimeCompare（常量时间比较，防 timing attack 逐字节爆破）。
func CheckVisitorKey(c *gin.Context, expectedKey string) bool {
	if uidV, ok := c.Get("user_id"); ok {
		if uid, _ := uidV.(uint); uid > 0 {
			return true
		}
	}
	vk := c.Query("visitor_key")
	if vk == "" || expectedKey == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(vk), []byte(expectedKey)) == 1
}
