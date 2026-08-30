// Package service 提供 SCRM 业务服务层实现（计费/消息/配置/脱敏/监控/向量等）。
package service

// ============================================================
// Prometheus 指标暴露（P2 监控闭环，2026-08-29 / P1-2 扩充 2026-08-30）
//
// 手写 Prometheus 文本格式（零外部依赖），/metrics 端点返回。
// 包含：运行时 gauge + HTTP 请求计数/延迟直方图 + AI 成功率 + DB 连接数
//       + 支付成功率 + 到期租户数 + 磁盘水位（6 关键指标看板数据源）。
// 与 P1-4 ComputeHealth 同源，避免重复探测。
// ============================================================

import (
	"fmt"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
)

// ---- 基础计数 ----
var httpTotal uint64 // HTTP 总请求数（atomic，免锁）

// ---- AI 调用成功率计数 ----
var aiSuccessTotal uint64
var aiFailureTotal uint64

// ---- 支付成功率计数 ----
var paymentPaidTotal uint64
var paymentFailedTotal uint64

// bootTime 进程启动时间（指标用 uptime）
var bootTime = time.Now()

// IncRequest 每次请求 +1（中间件调用）
func IncRequest() {
	atomic.AddUint64(&httpTotal, 1)
}

// IncAISuccess AI 调用成功 +1（llm 层在真模型成功返回后调用）
func IncAISuccess() { atomic.AddUint64(&aiSuccessTotal, 1) }

// IncAIFailure AI 调用失败 +1（全模型失败降级模板时调用）
func IncAIFailure() { atomic.AddUint64(&aiFailureTotal, 1) }

// IncPaymentPaid 支付成功 +1（订单确认到账/发放后调用）
func IncPaymentPaid() { atomic.AddUint64(&paymentPaidTotal, 1) }

// IncPaymentFailed 支付失败/关闭 +1（订单超时关闭/退款时调用）
func IncPaymentFailed() { atomic.AddUint64(&paymentFailedTotal, 1) }

// ---- HTTP 请求延迟直方图（P99 来源）----
// 桶（秒）：指数分布覆盖 5ms~10s
var latencyBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
var latencyBucketCounts []uint64 // 与 latencyBuckets 等长
var latencySumNs uint64          // 总纳秒（atomic）
var latencyCount uint64          // 总样本数（atomic）

func init() {
	latencyBucketCounts = make([]uint64, len(latencyBuckets))
}

// RecordRequestLatency 记录一次请求延迟（中间件在 c.Next() 后调用）
func RecordRequestLatency(d time.Duration) {
	sec := d.Seconds()
	atomic.AddUint64(&latencySumNs, uint64(d.Nanoseconds()))
	atomic.AddUint64(&latencyCount, 1)
	idx := len(latencyBuckets) - 1 // +Inf 桶
	for i, b := range latencyBuckets {
		if sec <= b {
			idx = i
			break
		}
	}
	atomic.AddUint64(&latencyBucketCounts[idx], 1)
}

// RenderPrometheus 生成 Prometheus exposition 格式文本
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

	// ---- P1-2 指标1：HTTP 请求延迟直方图（P99 由 Grafana 用 histogram_quantile 计算）----
	b = append(b, "# HELP ai_scrm_http_request_duration_seconds http request latency\n"...)
	b = append(b, "# TYPE ai_scrm_http_request_duration_seconds histogram\n"...)
	le := "+Inf"
	for i, bound := range latencyBuckets {
		le = strconv.FormatFloat(bound, 'f', -1, 64)
		b = append(b, fmt.Sprintf("ai_scrm_http_request_duration_seconds_bucket{le=\"%s\"} %d\n", le, atomic.LoadUint64(&latencyBucketCounts[i]))...)
	}
	b = append(b, fmt.Sprintf("ai_scrm_http_request_duration_seconds_bucket{le=\"+Inf\"} %d\n", atomic.LoadUint64(&latencyCount))...)
	b = append(b, fmt.Sprintf("ai_scrm_http_request_duration_seconds_sum %d\n", atomic.LoadUint64(&latencySumNs)/1e9)...)
	b = append(b, fmt.Sprintf("ai_scrm_http_request_duration_seconds_count %d\n", atomic.LoadUint64(&latencyCount))...)

	// ---- P1-2 指标2：AI 成功率 ----
	succ := atomic.LoadUint64(&aiSuccessTotal)
	fail := atomic.LoadUint64(&aiFailureTotal)
	rate := 0.0
	if succ+fail > 0 {
		rate = float64(succ) / float64(succ+fail) * 100
	}
	b = append(b, "# HELP ai_scrm_ai_success_total AI generation success count\n"...)
	b = append(b, "# TYPE ai_scrm_ai_success_total counter\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_ai_success_total %d\n", succ)...)
	b = append(b, "# HELP ai_scrm_ai_failure_total AI generation failure count\n"...)
	b = append(b, "# TYPE ai_scrm_ai_failure_total counter\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_ai_failure_total %d\n", fail)...)
	b = append(b, "# HELP ai_scrm_ai_success_rate AI success rate (percent)\n"...)
	b = append(b, "# TYPE ai_scrm_ai_success_rate gauge\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_ai_success_rate %.2f\n", rate)...)

	// ---- P1-2 指标3：DB 连接数 ----
	if db.DB != nil {
		if sqlDB, err := db.DB.DB(); err == nil {
			st := sqlDB.Stats()
			b = append(b, "# HELP ai_scrm_db_open_connections db open connections\n"...)
			b = append(b, "# TYPE ai_scrm_db_open_connections gauge\n"...)
			b = append(b, fmt.Sprintf("ai_scrm_db_open_connections %d\n", st.OpenConnections)...)
			b = append(b, "# HELP ai_scrm_db_idle_connections db idle connections\n"...)
			b = append(b, "# TYPE ai_scrm_db_idle_connections gauge\n"...)
			b = append(b, fmt.Sprintf("ai_scrm_db_idle_connections %d\n", st.Idle)...)
			b = append(b, "# HELP ai_scrm_db_in_use_connections db in-use connections\n"...)
			b = append(b, "# TYPE ai_scrm_db_in_use_connections gauge\n"...)
			b = append(b, fmt.Sprintf("ai_scrm_db_in_use_connections %d\n", st.InUse)...)
		}
	}

	// ---- P1-2 指标4：支付成功率 ----
	pp := atomic.LoadUint64(&paymentPaidTotal)
	pf := atomic.LoadUint64(&paymentFailedTotal)
	prate := 0.0
	if pp+pf > 0 {
		prate = float64(pp) / float64(pp+pf) * 100
	}
	b = append(b, "# HELP ai_scrm_payment_paid_total paid orders count\n"...)
	b = append(b, "# TYPE ai_scrm_payment_paid_total counter\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_payment_paid_total %d\n", pp)...)
	b = append(b, "# HELP ai_scrm_payment_failed_total failed/closed/refunded orders count\n"...)
	b = append(b, "# TYPE ai_scrm_payment_failed_total counter\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_payment_failed_total %d\n", pf)...)
	b = append(b, "# HELP ai_scrm_payment_success_rate payment success rate (percent)\n"...)
	b = append(b, "# TYPE ai_scrm_payment_success_rate gauge\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_payment_success_rate %.2f\n", prate)...)

	// ---- P1-2 指标5：到期租户数（7 天内即将到期 active/trial 租户）----
	expiring := countExpiringTenants(7)
	b = append(b, "# HELP ai_scrm_tenants_expiring_7d tenants expiring within 7d\n"...)
	b = append(b, "# TYPE ai_scrm_tenants_expiring_7d gauge\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_tenants_expiring_7d %d\n", expiring)...)

	// ---- P1-2 指标6：磁盘水位（0~1，1=满）----
	diskRatio, diskOK := diskUsedRatio()
	if diskOK {
		b = append(b, "# HELP ai_scrm_disk_used_ratio disk used ratio (0..1)\n"...)
		b = append(b, "# TYPE ai_scrm_disk_used_ratio gauge\n"...)
		b = append(b, fmt.Sprintf("ai_scrm_disk_used_ratio %.4f\n", diskRatio)...)
	}

	return string(b)
}

// countExpiringTenants 统计 N 天内即将到期的 active/trial 租户数（P1-2 指标5）
func countExpiringTenants(withinDays int) int64 {
	if db.DB == nil {
		return 0
	}
	now := time.Now()
	horizon := now.Add(time.Duration(withinDays) * 24 * time.Hour)
	var cnt int64
	db.DB.Model(&model.Tenant{}).
		Where("status IN ?", []string{"active", "trial"}).
		Where("expired_at IS NOT NULL AND expired_at >= ? AND expired_at <= ?", now, horizon).
		Count(&cnt)
	return cnt
}
