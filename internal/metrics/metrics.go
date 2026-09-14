// Package metrics 承载零依赖 Prometheus 文本指标采集与渲染。
package metrics

// ============================================================
// Prometheus 指标暴露（P2 监控闭环，2026-08-29 / P1-2 扩充 2026-08-30）
//
// 手写 Prometheus 文本格式（零外部依赖），/metrics 端点返回。
// 包含：运行时 gauge + HTTP 请求计数/延迟直方图 + AI 成功率 + DB 连接数
//       + 支付成功率 + 到期租户数 + 磁盘水位（6 关键指标看板数据源）。
// 与 P1-4 ComputeHealth 同源，避免重复探测。
// ============================================================

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// queueDepthFunc 合并队列活跃数提供者（由 service 注册，避免 metrics -> service 反向依赖）。
var queueDepthFunc func() int

// SetQueueDepthProvider 注册消息队列活跃深度回调（启动时由 service 调用一次）。
func SetQueueDepthProvider(fn func() int) { queueDepthFunc = fn }

// ---- 基础计数 ----
// httpTotal HTTP 总请求数（atomic，免锁），由中间件IncRequest递增
var httpTotal uint64

// ---- AI 调用成功率计数 ----
// aiSuccessTotal AI调用成功次数，llm层在真模型成功返回后调用IncAISuccess递增
var aiSuccessTotal uint64

// aiFailureTotal AI调用失败次数，全模型失败降级模板时调用IncAIFailure递增
var aiFailureTotal uint64

// ---- 支付成功率计数 ----
// paymentPaidTotal 支付成功次数，订单确认到账/发放后调用IncPaymentPaid递增
var paymentPaidTotal uint64

// paymentFailedTotal 支付失败/关闭次数，订单超时关闭/退款时调用IncPaymentFailed递增
var paymentFailedTotal uint64

// bootTime 进程启动时间（用于计算uptime指标）
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

// ---- 到店第二段追问失败计数（P2-66）----
// storeVisitSecondFailTotal 第二段追问落库重试仍失败的次数
var storeVisitSecondFailTotal uint64

// IncStoreVisitSecondFail 第二段追问落库失败（重试后放弃）+1
func IncStoreVisitSecondFail() { atomic.AddUint64(&storeVisitSecondFailTotal, 1) }

// ---- G-15 Kafka 消息队列指标（2026-09-11）----
// 说明：Kafka 是生产环境的消息总线，负责异步事件发布/消费
// 这三个指标用于监控 Kafka 的健康状态和吞吐量
var kafkaPublishTotal uint64     // Kafka 发布消息总数（每次成功发布+1）
var kafkaConsumeTotal uint64     // Kafka 消费消息总数（每次成功消费+1）
var kafkaConsumeFailTotal uint64 // Kafka 消费失败总数（消费异常时+1）

// IncKafkaPublish Kafka 消息发布成功 +1（mq.KafkaCenter.Publish 成功后调用）
func IncKafkaPublish() { atomic.AddUint64(&kafkaPublishTotal, 1) }

// IncKafkaConsume Kafka 消息消费成功 +1（消费者回调成功返回后调用）
func IncKafkaConsume() { atomic.AddUint64(&kafkaConsumeTotal, 1) }

// IncKafkaConsumeFail Kafka 消费失败 +1（消费者回调返回错误时调用）
// 注意：失败后消息会进入重试队列，超过最大重试次数后进入死信队列
func IncKafkaConsumeFail() { atomic.AddUint64(&kafkaConsumeFailTotal, 1) }

// ---- G-15 投诉事件计数（2026-09-11）----
// 说明：投诉事件来源于满意度评分模块（feedback.go），低评分+有内容视为投诉
// 该指标用于 Prometheus 监控，可配置告警规则（如投诉量突增告警）
var complaintTotal uint64

// IncComplaint 投诉事件 +1（api/feedback.go 低评分投诉触发）
// 调用时机：评分≤2 且评论非空时，发布 complaint 事件到 CDP 后调用
func IncComplaint() { atomic.AddUint64(&complaintTotal, 1) }

// ---- Q5 去 AI 味：敬语「您」出站兜底替换计数 ----
// addressPoliteTotal AI 出站回复命中「您」被兜底替换的次数。
// 铁律要求说"你"不说"您"，正常应为 0；非 0 说明 prompt/硬编码话术仍有漏网点，可配告警。
var addressPoliteTotal uint64

// IncAddressPolite AI 回复含「您」已兜底替换 +1（llm.sanitizeAddress 调用）
func IncAddressPolite() { atomic.AddUint64(&addressPoliteTotal, 1) }

// ---- C1 内容安全闸门计数 ----
// contentSafetyHitTotal 命中词库/机审次数（含 shadow 观察）
var contentSafetyHitTotal uint64

// contentSafetyBlockTotal enforce 模式实际拦截（转人工/丢弃）次数
var contentSafetyBlockTotal uint64

// IncContentSafetyHit 内容安全命中 +1
func IncContentSafetyHit() { atomic.AddUint64(&contentSafetyHitTotal, 1) }

// IncContentSafetyBlock 内容安全 enforce 拦截 +1
func IncContentSafetyBlock() { atomic.AddUint64(&contentSafetyBlockTotal, 1) }

// ---- D9 行业包质量指标（带 pack/template 标签）----
type packMetricKey struct {
	Pack     string
	Template string
}

var (
	packReplyTotal        sync.Map
	packLeadCapturedTotal sync.Map
	packAlertTotal        sync.Map
)

// incPackCounter 原子累加包维度 Prometheus 计数器。
func incPackCounter(m *sync.Map, pack, template string) {
	key := packMetricKey{Pack: pack, Template: template}
	if p, ok := m.Load(key); ok {
		atomic.AddUint64(p.(*uint64), 1)
		return
	}
	p := new(uint64)
	actual, _ := m.LoadOrStore(key, p)
	atomic.AddUint64(actual.(*uint64), 1)
}

// IncPackReply 包/模板维度 AI 回复归因计数 +1
func IncPackReply(pack, template string) { incPackCounter(&packReplyTotal, pack, template) }

// IncPackLeadCaptured 包维度留资归因计数 +1
func IncPackLeadCaptured(pack string) { incPackCounter(&packLeadCapturedTotal, pack, "") }

// IncPackAlert 包质量告警计数 +1
func IncPackAlert(pack string) { incPackCounter(&packAlertTotal, pack, "") }

// promEscape 转义 Prometheus label 中的特殊字符。
func promEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

// renderPackCounter 将包维度计数器渲染为 Prometheus 文本格式。
func renderPackCounter(b *[]byte, name, help string, m *sync.Map, withTemplate bool) {
	type entry struct {
		key   packMetricKey
		value uint64
	}
	var entries []entry
	m.Range(func(k, v interface{}) bool {
		key, ok := k.(packMetricKey)
		if !ok {
			return true
		}
		p, ok := v.(*uint64)
		if !ok {
			return true
		}
		entries = append(entries, entry{key: key, value: atomic.LoadUint64(p)})
		return true
	})
	if len(entries) == 0 {
		return
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].key.Pack != entries[j].key.Pack {
			return entries[i].key.Pack < entries[j].key.Pack
		}
		return entries[i].key.Template < entries[j].key.Template
	})
	*b = append(*b, fmt.Sprintf("# HELP %s %s\n", name, help)...)
	*b = append(*b, fmt.Sprintf("# TYPE %s counter\n", name)...)
	for _, e := range entries {
		labels := fmt.Sprintf(`pack="%s"`, promEscape(e.key.Pack))
		if withTemplate {
			labels = fmt.Sprintf(`%s,template="%s"`, labels, promEscape(e.key.Template))
		}
		*b = append(*b, fmt.Sprintf("%s{%s} %d\n", name, labels, e.value)...)
	}
}

// ---- HTTP 请求延迟直方图（P99 来源）----
// 桶（秒）：指数分布覆盖 5ms~10s，用于Prometheus histogram计算
var latencyBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
var latencyBucketCounts []uint64 // 与 latencyBuckets 等长，各桶的样本计数
var latencySumNs uint64          // 总纳秒（atomic），用于计算平均延迟
var latencyCount uint64          // 总样本数（atomic），用于计算平均延迟

// init 初始化延迟直方图桶计数数组
func init() {
	latencyBucketCounts = make([]uint64, len(latencyBuckets))
}

// RecordRequestLatency 记录一次请求延迟（中间件在 c.Next() 后调用）
// P1-18 修复(2026-09-09)：原实现只对命中桶 +1，非累计——histogram_quantile() 求出的 P99
// 数学上错误。现改为从命中桶到最大上界桶全部 +1（Prometheus 桶语义：le 为"上界"，须单调），
// +Inf 桶由 latencyCount 兜底。
func RecordRequestLatency(d time.Duration) {
	sec := d.Seconds()
	atomic.AddUint64(&latencySumNs, uint64(d.Nanoseconds()))
	atomic.AddUint64(&latencyCount, 1)
	idx := len(latencyBuckets) - 1 // 默认最大桶
	for i, b := range latencyBuckets {
		if sec <= b {
			idx = i
			break
		}
	}
	for i := idx; i < len(latencyBuckets); i++ {
		atomic.AddUint64(&latencyBucketCounts[i], 1) // 累计：le 桶单调不减
	}
}

// RenderPrometheus 生成 Prometheus exposition 格式文本
// 手写Prometheus文本格式（零外部依赖），/metrics 端点返回
// 包含：运行时gauge + HTTP请求计数/延迟直方图 + AI成功率 + DB连接数 + 支付成功率 + 到期租户数 + 磁盘水位
// queueDepth 安全读取合并队列活跃会话数（未注册时返回 0，不阻断指标输出）。
func queueDepth() int {
	if queueDepthFunc == nil {
		return 0
	}
	return queueDepthFunc()
}

// RenderPrometheus 汇总运行时、DB、队列与业务指标并生成 /metrics 文本。
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
	b = append(b, fmt.Sprintf("ai_scrm_merge_queue_active %d\n", queueDepth())...)

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
	// P1-18 修复：_sum 用 float 秒（原整数除法丢亚秒精度，histogram_quantile 无法用）
	b = append(b, fmt.Sprintf("ai_scrm_http_request_duration_seconds_sum %.6f\n", float64(atomic.LoadUint64(&latencySumNs))/1e9)...)
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

	// ---- P2-66 指标：到店第二段追问失败 ----
	svsFail := atomic.LoadUint64(&storeVisitSecondFailTotal)
	b = append(b, "# HELP ai_scrm_store_visit_second_fail_total second follow-up persist fail count (after retry)\n"...)
	b = append(b, "# TYPE ai_scrm_store_visit_second_fail_total counter\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_store_visit_second_fail_total %d\n", svsFail)...)

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
	// 磁盘使用率超过阈值可能影响日志写入和数据库操作
	diskRatio, diskOK := diskUsedRatio()
	if diskOK {
		b = append(b, "# HELP ai_scrm_disk_used_ratio disk used ratio (0..1)\n"...)
		b = append(b, "# TYPE ai_scrm_disk_used_ratio gauge\n"...)
		b = append(b, fmt.Sprintf("ai_scrm_disk_used_ratio %.4f\n", diskRatio)...)
	}

	// ---- G-15 指标：Kafka 发布/消费 ----
	b = append(b, "# HELP ai_scrm_kafka_publish_total kafka messages published\n"...)
	b = append(b, "# TYPE ai_scrm_kafka_publish_total counter\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_kafka_publish_total %d\n", atomic.LoadUint64(&kafkaPublishTotal))...)
	b = append(b, "# HELP ai_scrm_kafka_consume_total kafka messages consumed\n"...)
	b = append(b, "# TYPE ai_scrm_kafka_consume_total counter\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_kafka_consume_total %d\n", atomic.LoadUint64(&kafkaConsumeTotal))...)
	b = append(b, "# HELP ai_scrm_kafka_consume_fail_total kafka consumption failures\n"...)
	b = append(b, "# TYPE ai_scrm_kafka_consume_fail_total counter\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_kafka_consume_fail_total %d\n", atomic.LoadUint64(&kafkaConsumeFailTotal))...)

	// ---- G-15 指标：投诉事件 ----
	b = append(b, "# HELP ai_scrm_complaint_total complaint events published\n"...)
	b = append(b, "# TYPE ai_scrm_complaint_total counter\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_complaint_total %d\n", atomic.LoadUint64(&complaintTotal))...)

	// ---- Q5 指标：敬语「您」兜底替换 ----
	b = append(b, "# HELP ai_scrm_address_polite_total AI replies containing 您 sanitized on exit\n"...)
	b = append(b, "# TYPE ai_scrm_address_polite_total counter\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_address_polite_total %d\n", atomic.LoadUint64(&addressPoliteTotal))...)

	// ---- C1 指标：内容安全 ----
	b = append(b, "# HELP ai_scrm_contentsafety_hit_total AI replies hitting content-safety filter\n"...)
	b = append(b, "# TYPE ai_scrm_contentsafety_hit_total counter\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_contentsafety_hit_total %d\n", atomic.LoadUint64(&contentSafetyHitTotal))...)
	b = append(b, "# HELP ai_scrm_contentsafety_block_total AI replies blocked in enforce mode\n"...)
	b = append(b, "# TYPE ai_scrm_contentsafety_block_total counter\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_contentsafety_block_total %d\n", atomic.LoadUint64(&contentSafetyBlockTotal))...)

	// ---- D9 指标：行业包质量归因 ----
	renderPackCounter(&b, "ai_scrm_pack_reply_total", "AI reply attribution count by pack/template", &packReplyTotal, true)
	renderPackCounter(&b, "ai_scrm_pack_lead_captured_total", "Lead captured count attributed by pack", &packLeadCapturedTotal, false)
	renderPackCounter(&b, "ai_scrm_pack_alert_total", "Pack quality alert count by pack", &packAlertTotal, false)

	return string(b)
}

// countExpiringTenants 统计 N 天内即将到期的 active/trial 租户数（P1-2 指标5）
// 用于Prometheus指标，监控即将到期的租户数量
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
