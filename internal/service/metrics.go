package service

// ============================================================
// Prometheus 指标暴露（P2 监控闭环，2026-08-29）
//
// 手写 Prometheus 文本格式（零外部依赖），/metrics 端点返回。
// 包含：运行时 gauge（goroutine/DB/合并队列/24h严重事件）+ HTTP 请求计数（atomic）。
// 与 P1-4 ComputeHealth 同源，避免重复探测。
// ============================================================

import (
	"fmt"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"
)

// httpTotal HTTP 总请求数（atomic 计数，免锁）
var httpTotal uint64

// bootTime 进程启动时间（指标用 uptime）
var bootTime = time.Now()

// IncRequest 每次请求 +1（中间件调用）
func IncRequest() {
	atomic.AddUint64(&httpTotal, 1)
}

// RenderPrometheus 生成 Prometheus  exposition 格式文本
func RenderPrometheus() string {
	snap := ComputeHealth()
	var crit24h int64
	for _, ch := range snap.Checks {
		if ch.Name == "critical_24h" {
			if v, err := strconv.ParseInt(ch.Value, 10, 64); err == nil {
				crit24h = v
			}
		}
	}
	dbUp := 0
	if snap.DBOK {
		dbUp = 1
	}
	var b []byte
	b = append(b, "# HELP ai_scrm_goroutines runtime goroutine count\n"...)
	b = append(b, "# TYPE ai_scrm_goroutines gauge\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_goroutines %d\n", runtime.NumGoroutine())...)
	b = append(b, "# HELP ai_scrm_db_up postgres reachable (1=up)\n"...)
	b = append(b, "# TYPE ai_scrm_db_up gauge\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_db_up %d\n", dbUp)...)
	b = append(b, "# HELP ai_scrm_merge_queue_active message merge queue active sessions\n"...)
	b = append(b, "# TYPE ai_scrm_merge_queue_active gauge\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_merge_queue_active %d\n", DefaultMessageQueueService.ActiveQueueCount())...)
	b = append(b, "# HELP ai_scrm_critical_24h critical audit events in last 24h\n"...)
	b = append(b, "# TYPE ai_scrm_critical_24h gauge\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_critical_24h %d\n", crit24h)...)
	b = append(b, "# HELP ai_scrm_http_requests_total total http requests since boot\n"...)
	b = append(b, "# TYPE ai_scrm_http_requests_total counter\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_http_requests_total %d\n", atomic.LoadUint64(&httpTotal))...)
	b = append(b, "# HELP ai_scrm_uptime_seconds process uptime\n"...)
	b = append(b, "# TYPE ai_scrm_uptime_seconds gauge\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_uptime_seconds %d\n", int(time.Since(bootTime).Seconds()))...)
	return string(b)
}
