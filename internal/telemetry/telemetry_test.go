// Package telemetry 测试：P2-7 otel 装配可用性（Init→Tracer→span→shutdown 全链路不 panic）。
package telemetry

import (
	"context"
	"testing"
)

// TestInitAndSpanLifecycle 验证 Init 成功、Tracer 可用、span 可 start/end、shutdown 可 flush。
func TestInitAndSpanLifecycle(t *testing.T) {
	shutdown, err := Init(context.Background(), "telemetry-test")
	if err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	if shutdown == nil {
		t.Fatal("Init 返回的 shutdown 为 nil")
	}
	// Tracer 必须非 nil 且能起 span（stdout exporter 会把 span 写 stderr，不影响断言）
	tr := Tracer()
	if tr == nil {
		t.Fatal("Tracer() 返回 nil")
	}
	_, span := tr.Start(context.Background(), "test-op")
	if span == nil {
		t.Fatal("Start 返回 nil span")
	}
	span.End()
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown flush 失败: %v", err)
	}
}

// TestTracerNoopSafe 验证未显式 Init 的进程里 Tracer() 也返回可用 noop（不 nil 解引用）。
// 注：若本测试在 TestInitAndSpanLifecycle 之后跑，全局 tracer 已被 Init 设为真实 tracer，
// 两种都合法——此处只断言"非 nil 且可起 span"。
func TestTracerNoopSafe(t *testing.T) {
	_, span := Tracer().Start(context.Background(), "noop-op")
	if span == nil {
		t.Fatal("Tracer().Start 返回 nil span")
	}
	span.End()
}
