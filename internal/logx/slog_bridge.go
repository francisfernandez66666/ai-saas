// 标准库日志 → slog 桥接（2026-09-15 价值增强批）
// 背景：全仓 556 处 log.Printf、0 处结构化——单机 tail 够用，但多实例上云后
// 无法按字段聚合检索（Loki/ELK 只能整行 grep）。
// 方案：不逐点改写 556 处调用（改动面大且易漏），而是把 std log 的输出流整体桥接进
// slog：每条日志成为一行 JSON/text 记录（time + level + msg，msg 保留 "file:line 原文"）。
// 新代码可直接用 slog.With(...)；后续可按模块渐进替换。
// 级别推断：消息前缀含 [PANIC/FATAL → Error，[WARN/⚠/安全警告 → Warn，其余 Info。
package logx

import (
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

// slogBridge 实现 io.Writer：std log 每条记录一次 Write，按前缀启发式分级转投 slog。
type slogBridge struct{}

// Write 实现 io.Writer：把 std log 输出的一行原文转投 slog。
// 分级启发式：行首含 [ERROR]/[WARN]/panic 等标记时按对应级别，其余按 Info——
// 旧代码里大量 log.Printf("[Billing][ERROR] ...") 自带级别前缀，不必逐文件改造。
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
//
// logFile != "" 时启用日志轮转（P2-6）：handler 挂在 RotatingWriter 上而非 os.Stderr。
// 这修掉了"日志经 shell 重定向落盘但永不轮转、单文件可达数百 MB"的缺陷。
// 注意：启用后**不要再用 >> 重定向同一个文件**，否则会出现两个写入方互相抢写同一 inode。
func InitStructuredLogging(format string, logFile string) {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	var sink io.Writer = os.Stderr

	// 轮转参数：默认单文件 100 MB、保留 5 份（= 最多占 600 MB），可用环境变量覆盖。
	const (
		defaultRotateMaxSize    = 100 << 20 // 100 MB
		defaultRotateMaxBackups = 5
	)
	if logFile != "" {
		maxSize := envInt64("LOG_ROTATE_MAX_SIZE_MB", defaultRotateMaxSize>>20) << 20
		maxBackups := int(envInt64("LOG_ROTATE_MAX_BACKUPS", defaultRotateMaxBackups))
		rw, err := NewRotatingWriter(logFile, maxSize, maxBackups)
		if err != nil {
			// 轮转初始化失败不得阻断启动：退回 stderr，保证日志可观测
			log.SetOutput(os.Stderr)
			fmt.Fprintf(os.Stderr, "[logx][WARN] 日志轮转初始化失败(%v)，已退回 stderr 输出\n", err)
		} else {
			sink = rw
		}
	}

	var h slog.Handler
	if strings.EqualFold(format, "json") {
		h = slog.NewJSONHandler(sink, opts)
	} else {
		h = slog.NewTextHandler(sink, opts)
	}
	slog.SetDefault(slog.New(h))
	log.SetFlags(log.Lshortfile)
	log.SetOutput(slogBridge{})
}

// envInt64 读环境变量整数，非法/缺省回落 def
func envInt64(key string, def int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
