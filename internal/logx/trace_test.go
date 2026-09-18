// E3 trace 底座单测（2026-09-19）：ctx 存取 + WithTrace 结构化字段。
package logx

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestContextTraceRoundTrip(t *testing.T) {
	if got := TraceFrom(context.Background()); got != "" {
		t.Fatalf("无 trace ctx 应返回空串，得到 %q", got)
	}
	if got := TraceFrom(nil); got != "" {
		t.Fatalf("nil ctx 应安全返回空串，得到 %q", got)
	}
	base := context.Background()
	if ctx2 := ContextWithTrace(base, ""); ctx2 != base {
		t.Fatal("空 trace 不应包装 ctx（零开销约定）")
	}
	ctx := ContextWithTrace(base, "abc1234567890def")
	if TraceFrom(ctx) != "abc1234567890def" {
		t.Fatal("trace 存取不一致")
	}
	// 与 middleware.TraceIDKey 的键一致性：两侧都是普通 string 键 "trace_id"
	if ctx.Value("trace_id") != "abc1234567890def" {
		t.Fatal("键必须为字面量 trace_id（与 middleware.CtxWithTrace 互读）")
	}
}

func TestWithTraceEmitsField(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	defer slog.SetDefault(old)
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))

	WithTrace(ContextWithTrace(context.Background(), "t-1234567890")).Info("入站", "tenant_id", 7)
	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatalf("非 JSON 行: %v", err)
	}
	if rec["trace_id"] != "t-1234567890" || rec["tenant_id"] == nil {
		t.Fatalf("trace_id/附加字段缺失: %v", rec)
	}

	buf.Reset()
	WithTrace(context.Background()).Info("无 trace")
	if strings.Contains(buf.String(), "trace_id") {
		t.Fatalf("无 trace 不应带字段: %s", buf.String())
	}
}

func TestNewTraceID(t *testing.T) {
	a, b := NewTraceID(), NewTraceID()
	if len(a) != 16 || a == b {
		t.Fatalf("NewTraceID 应为 16 位 hex 且近似唯一: %s %s", a, b)
	}
}
