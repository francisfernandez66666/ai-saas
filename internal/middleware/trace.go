// Package middleware Gin 中间件链：TenantResolver(fail-closed)→JWTAuth→TenantConsistency→OrgResolve 等安全闸。
package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"github.com/gin-gonic/gin"
)

// TraceIDKey gin 上下文与 context.Context 共用的 trace 键
const TraceIDKey = "trace_id"

// TraceID 全链路追踪中间件（P1-3，2026-08-30）
// 优先复用客户端下发的 X-Trace-ID（跨服务串联），否则生成 16 位 hex；
// 写入 c.Set(trace_id) 供后续 handler/log/mq 读取，并回写响应头 X-Trace-ID。
func TraceID() gin.HandlerFunc {
	return func(c *gin.Context) {
		tid := c.GetHeader("X-Trace-ID")
		if tid == "" {
			tid = randomHex(8)
		}
		c.Set(TraceIDKey, tid)
		c.Header("X-Trace-ID", tid)
		c.Next()
	}
}

// GetTraceID 从 gin 上下文取 trace（缺省空串）
func GetTraceID(c *gin.Context) string {
	if v, ok := c.Get(TraceIDKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// randomHex 生成 n 字节十六进制串
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
