// E3 全链路 trace 底座（2026-09-19 增强批）：context 携带 trace_id 的读写辅助。
// 背景：middleware.TraceID 已把请求级 trace 放进 gin 上下文，但 std log 桥接的 556 处
// log.Printf 拿不到 ctx（Write 无 context 参数），无法逐点改写。方案：
//   - 显式传参：合并队列等异步链路把 trace 作为函数参数带到底层日志行（trace=xxx 片段）；
//   - slog 新代码：logx.WithTrace(ctx) 返回带 trace_id 字段的 logger（JSON 输出可直接按字段检索）；
//   - 出站 HTTP：gateway_client 从 ctx 取 trace 写 X-Trace-ID/traceparent 头，跨服务串联。
//
// context key 与 middleware.TraceIDKey 同为字面量 "trace_id"（两侧常量值必须一致）。
package logx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
)

// TraceKey context 中 trace 的键——与 middleware.TraceIDKey 保持一致
const TraceKey = "trace_id"

// NewTraceID 生成 16 位 hex trace（无请求上下文的后台/回调链路自造 trace 用）
func NewTraceID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ContextWithTrace 把 trace 放入 ctx；trace 为空时原样返回（零开销）
func ContextWithTrace(ctx context.Context, trace string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if trace == "" {
		return ctx
	}
	return context.WithValue(ctx, TraceKey, trace)
}

// TraceFrom 从 ctx 取 trace（缺省空串）
func TraceFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if s, ok := ctx.Value(TraceKey).(string); ok {
		return s
	}
	return ""
}

// WithTrace 返回带 trace_id 字段的 slog logger；ctx 无 trace 时退回默认 logger
func WithTrace(ctx context.Context) *slog.Logger {
	l := slog.Default()
	if t := TraceFrom(ctx); t != "" {
		l = l.With("trace_id", t)
	}
	return l
}
