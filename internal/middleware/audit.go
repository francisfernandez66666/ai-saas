// Package middleware Gin 中间件链。
// 本文件：P1-2 请求级审计中间件（2026-10-13）。
package middleware

import (
	"log"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"

	"github.com/gin-gonic/gin"
)

// requestAuditSkipPrefixes 这些路径不产生请求审计：健康检查/指标/WebSocket/静态资源。
// 高频且非业务语义，落审计只会造成噪声与写入放大，且 WS 不走 JWT 鉴权链本就不带租户上下文。
var requestAuditSkipPrefixes = []string{
	"/health", "/metrics", "/ping", "/ws/", "/favicon",
}

// AuditMiddleware 请求级审计中间件（P1-2，2026-10-13）。
//
// 注册位置：routes.go 的 v1.Use(...) 鉴权链之后——此时 tenant_id / user_id 已由
// JWTAuth / TenantConsistency 写入 Context，本中间件只读取、不阻断。
//
// 解决的问题：原全仓审计靠 20 处手工埋点，普通租户常规请求（查询/导出/改配置/改配额）
// 不落审计，新增 handler 漏埋零惩罚。本中间件在传输层强制记录每个经鉴权链请求。
//
// 失败容忍：异步写库（goroutine），写入失败仅记日志，绝不阻塞或中断主请求链——
// 与 auditSuperAdminAccess（tenant.go:683）同一口径。
//
// 性能纪律（字节级）：
//   - 跳过 noise 前缀，避免心跳/WS/静态资源放大写入；
//   - 写库在独立 goroutine，不占用请求处理线程；
//   - 默认开启，可用 AUDIT_REQUESTS_ENABLED=false 关闭（应急降级，不推荐长期关）。
//
// Context 复用安全：gin 在 handler 返回、整个请求 goroutine 结束后才回收复用 Context。
// c.Next() 返回后仍在当前请求 goroutine 内，读取 c.Writer.Status() / c.ClientIP() 等安全；
// 但异步写库 goroutine 内禁止再读 c（复用后字段串号 + 数据竞争，见 auditSuperAdminAccess
// 注释），故所有字段在 goroutine 外提取为局部变量再传入。
func AuditMiddleware() gin.HandlerFunc {
	enabled := os.Getenv("AUDIT_REQUESTS_ENABLED") != "false" // 默认开启
	return func(c *gin.Context) {
		if !enabled {
			c.Next()
			return
		}
		// noise 路径直接放行，不审计
		p := c.Request.URL.Path
		for _, s := range requestAuditSkipPrefixes {
			if strings.HasPrefix(p, s) {
				c.Next()
				return
			}
		}

		start := time.Now()
		c.Next()

		// —— 以下在 c.Next() 返回后、同一请求 goroutine 内提取，安全 ——
		method := c.Request.Method
		path := c.Request.URL.Path
		status := c.Writer.Status()
		latency := int(time.Since(start).Milliseconds())
		ip := c.ClientIP()
		ua := c.Request.UserAgent()
		tenantID, _ := c.Get("tenant_id")
		userID, _ := c.Get("user_id")

		// 复制值入局部变量 + 按列宽截断，goroutine 内不触碰 c。
		// 截断是必须的：path/user_agent 列是 varchar(512)，超长值会让 PG 报
		// "value too long" / 切断 UTF-8 报 "invalid byte sequence"，该行审计写入失败
		// 被 recover 静默吞掉——与 migrations/032 "user_agent 截断到 512" 注释对齐（修注释与实现不符）。
		tid := toUint(tenantID)
		uid := toUint(userID)
		path = truncateForAudit(path, 512)
		ua = truncateForAudit(ua, 512)
		go func() {
			defer func() { _ = recover() }()
			rec := model.RequestAuditLog{
				TenantID:  tid,
				UserID:    uid,
				Method:    method,
				Path:      path,
				Status:    status,
				LatencyMs: latency,
				IP:        ip,
				UserAgent: ua,
			}
			if err := db.DB.Create(&rec).Error; err != nil {
				log.Printf("[request-audit] 写入失败（不影响请求）: %v", err)
			}
		}()
	}
}

// truncateForAudit 把字符串截到 max 字节以内，且不切断 UTF-8 多字节序列
// （否则残留半字符会让 PG 报 invalid byte sequence，同样丢该行审计）。
func truncateForAudit(s string, max int) string {
	if len(s) <= max {
		return s
	}
	b := s[:max]
	for len(b) > 0 && !utf8.ValidString(b) {
		b = b[:len(b)-1]
	}
	return b
}
