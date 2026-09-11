// Package middleware Gin 中间件链：TenantResolver(fail-closed)→JWTAuth→TenantConsistency→OrgResolve 等安全闸。
package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"github.com/gin-gonic/gin"
)

// TraceIDKey gin 上下文与 context.Context 共用的 trace 键
const TraceIDKey = "trace_id"

// TraceID 全链路追踪中间件（P1-3，2026-08-30）
// 优先复用客户端下发的 X-Trace-ID（跨服务串联），否则生成 16 位 hex；
// 写入 c.Set(trace_id) 供后续 handler/log/mq 读取，并回写响应头 X-Trace-ID。
// P2-3 修复(2026-09-09)：客户端可注入任意长度/字符 trace 进日志（日志注入）。
// 下发 trace 非法（非 ^[a-zA-Z0-9-]{8,64}$）一律丢弃重新生成。
func TraceID() gin.HandlerFunc {
	return func(c *gin.Context) {
		tid := c.GetHeader("X-Trace-ID")
		if !validTraceID(tid) {
			tid = ""
		}
		if tid == "" {
			tid = randomHex(8)
		}
		c.Set(TraceIDKey, tid)
		c.Header("X-Trace-ID", tid)
		c.Next()
	}
}

// validTraceID 校验 trace 格式（P2-3）：^[a-zA-Z0-9-]{8,64}$，防日志注入
func validTraceID(s string) bool {
	if len(s) < 8 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
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

// CtxWithTrace 将 gin 请求的 trace 注入 context（P2-4 修复）
// buildEnvelope(ctx) 从 ctx.Value("trace_id") 读 trace，但 gin c.Set 不进 request.Context()——
// 此前 MQ 消息的 trace 恒为空串（客户端 X-Trace-ID 链路断）。API 层发布事件改用它串联。
func CtxWithTrace(c *gin.Context) context.Context {
	ctx := context.Background()
	if c == nil {
		return ctx
	}
	if tid := GetTraceID(c); tid != "" {
		ctx = context.WithValue(ctx, TraceIDKey, tid)
	}
	return ctx
}

// randomHex 生成 n 字节十六进制串
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
