// slog 桥接单测：std log 各前缀正确映射级别、JSON 可解析、空行吞掉。
package logx

import (
	"bytes"
	"encoding/json"
	"log"
	"log/slog"
	"strings"
	"testing"
)

// TestSlogBridgeLevels 验证 slog 桥接把 std log 文本（INFO/WARN）解析为对应级别的结构化日志。
func TestSlogBridgeLevels(t *testing.T) {
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	b := slogBridge{}
	if _, err := b.Write([]byte("chat.go:10 hello\n")); err != nil {
		t.Fatalf("Write 报错: %v", err)
	}
	if _, err := b.Write([]byte("main.go:20 [WARN] 限流回退")); err != nil {
		t.Fatalf("Write 报错: %v", err)
	}
	if _, err := b.Write([]byte("main.go:30 [PANIC-GUARD] 崩溃")); err != nil {
		t.Fatalf("Write 报错: %v", err)
	}
	if _, err := b.Write([]byte("\n")); err != nil {
		t.Fatalf("空行不应报错: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("应产出 3 条记录（空行吞掉），got %d:\n%s", len(lines), buf.String())
	}
	wantLevels := []string{"INFO", "WARN", "ERROR"}
	for i, ln := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(ln), &rec); err != nil {
			t.Fatalf("第 %d 行非合法 JSON: %v", i, err)
		}
		if rec["level"] != wantLevels[i] {
			t.Fatalf("第 %d 条级别应为 %s，got %v（%v）", i, wantLevels[i], rec["level"], rec["msg"])
		}
		if _, ok := rec["time"]; !ok {
			t.Fatalf("第 %d 条缺 time 字段", i)
		}
	}
}

// TestInitStructuredLoggingSetsFlags 验证桥接后 std log 仅保留 Lshortfile（时间戳统一由 slog 提供，避免重复）。
func TestInitStructuredLoggingSetsFlags(t *testing.T) {
	out := log.Writer()
	flags := log.Flags()
	defer log.SetOutput(out)
	defer log.SetFlags(flags)

	InitStructuredLogging("text")
	if log.Flags() != log.Lshortfile {
		t.Fatalf("桥接后 std log 仅应保留 Lshortfile（时间戳由 slog 提供），got %d", log.Flags())
	}
	log.Printf("[WARN] 测试行 %s", "x")
	if _, ok := log.Writer().(slogBridge); !ok {
		t.Fatalf("log 输出未桥接到 slogBridge")
	}
}
