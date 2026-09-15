// 标准库日志 → slog 桥接（2026-09-15 价值增强批）
// 背景：全仓 556 处 log.Printf、0 处结构化——单机 tail 够用，但多实例上云后
// 无法按字段聚合检索（Loki/ELK 只能整行 grep）。
// 方案：不逐点改写 556 处调用（改动面大且易漏），而是把 std log 的输出流整体桥接进
// slog：每条日志成为一行 JSON/text 记录（time + level + msg，msg 保留 "file:line 原文"）。
// 新代码可直接用 slog.With(...)；后续可按模块渐进替换。
// 级别推断：消息前缀含 [PANIC/FATAL → Error，[WARN/⚠/安全警告 → Warn，其余 Info。
package logx

import (
	"log"
	"log/slog"
	"os"
	"strings"
)

// slogBridge 实现 io.Writer：std log 每条记录一次 Write，按前缀启发式分级转投 slog。
type slogBridge struct{}

func (slogBridge) Write(p []byte) (int, error) {
	msg := strings.TrimRight(string(p), "\n")
	if strings.TrimSpace(msg) == "" {
		return len(p), nil
	}
	switch {
	case strings.Contains(msg, "[PANIC"), strings.Contains(msg, "FATAL"), strings.Contains(msg, "严重"):
		slog.Error(msg)
	case strings.Contains(msg, "[WARN"), strings.Contains(msg, "⚠"), strings.Contains(msg, "安全警告"):
		slog.Warn(msg)
	default:
		slog.Info(msg)
	}
	return len(p), nil
}

// InitStructuredLogging 初始化 slog 默认 logger 并接管 std log 输出。
// format=json（推荐，release 默认）单行可解析；format=text（debug 默认）人类可读。
// 时间戳由 slog 统一提供，故 std log 侧关掉日期标志、仅保留 Lshortfile（file:line 进 msg）。
func InitStructuredLogging(format string) {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	var h slog.Handler
	if strings.EqualFold(format, "json") {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(h))
	log.SetFlags(log.Lshortfile)
	log.SetOutput(slogBridge{})
}
