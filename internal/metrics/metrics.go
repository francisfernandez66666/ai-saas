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

// ---- P2-2 回复投递认领降级计数（2026-09-19 审计批三）----
// replyDeliveryDegradeTotal Redis 故障导致 ClaimReplyDelivery 降级单机裁决的次数。
// 降级语义=宁双发不漏发（出站台账可稽核），该计数非零即说明多实例互斥已失守，需运维介入。
var replyDeliveryDegradeTotal uint64

// IncReplyDeliveryDegrade Redis 故障投递认领降级单机裁决 +1（service.ClaimReplyDelivery 调用）
func IncReplyDeliveryDegrade() { atomic.AddUint64(&replyDeliveryDegradeTotal, 1) }

// ---- 主动触达计数（触达最小闭环，2026-09-23 批次2）----
// 四个口径都是"任务裁决结果"计数，由 internal/outreach 调度器埋点：
//
//	queued  = 判定可发且已写入出站队列（真正发出去了）
//	skipped = 被策略拦下（无通道/窗口已过/通道停用），属预期行为不计失败
//	sent    = 出站回执确认送达
//	failed  = 真失败（发送报错、重试耗尽、回执缺失）
//
// 为什么要四个而不是一个总数：queued 与 sent 的差值就是"卡在出站队列"的量，
// 只有分开数才能看出是策略太严（skipped 高）还是投递链路坏了（sent 追不上 queued）。
var (
	outreachQueuedTotal  uint64
	outreachSkippedTotal uint64
	outreachSentTotal    uint64
	outreachFailedTotal  uint64
)

// IncOutreachQueued 触达任务入出站队列 +1
func IncOutreachQueued() { atomic.AddUint64(&outreachQueuedTotal, 1) }

// IncOutreachSkipped 触达任务被策略拦下 +1
func IncOutreachSkipped() { atomic.AddUint64(&outreachSkippedTotal, 1) }

// IncOutreachSent 触达任务确认送达 +1
func IncOutreachSent() { atomic.AddUint64(&outreachSentTotal, 1) }

// IncOutreachFailed 触达任务发送失败 +1
func IncOutreachFailed() { atomic.AddUint64(&outreachFailedTotal, 1) }

// ---- D3 用量预警与催缴计数（2026-09-23）----
// 四个口径分两组，各自回答一个运维问题：
//
//	usage_alert_sent / usage_alert_skipped  = 预警"说出口"的量 vs "想说但没通道"的量
//	dunning_sent / dunning_suspended        = 催缴邮件发出量 / 自动封禁施加量
//
// 为什么 sent 与 skipped 必须分开数：SMTP/群机器人没配时 sweeper 依然会"命中档位"，
// 若只记一个总数，看板看起来一片繁荣，实际一封邮件都没出去（= 上线首日最容易踩的静默失效）。
// skipped 单独一个计数就是那件事的探针：它大于 0 就说明该去配 SMTP 了。
// dunning_suspended 更是资金侧红线探针：它涨而 dunning_sent 不涨 = 只封不催，客户莫名掉线。
var (
	usageAlertSentTotal    uint64
	usageAlertSkippedTotal uint64
	dunningSentTotal       uint64
	dunningSuspendedTotal  uint64
)

// IncUsageAlertSent 用量预警成功经至少一条真实通道发出 +1
func IncUsageAlertSent() { atomic.AddUint64(&usageAlertSentTotal, 1) }

// IncUsageAlertSkipped 用量预警命中但无可用投递通道（SMTP/群未配置或无管理员邮箱）+1
func IncUsageAlertSkipped() { atomic.AddUint64(&usageAlertSkippedTotal, 1) }

// IncDunningSent 催缴档位通知实际发出（邮件或群任一成功）+1
func IncDunningSent() { atomic.AddUint64(&dunningSentTotal, 1) }

// IncDunningSuspended 催缴宽限期末自动封禁施加 +1
func IncDunningSuspended() { atomic.AddUint64(&dunningSuspendedTotal, 1) }

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

// ---- 批二 S2：找回密码"不安全通道"命中计数（2026-09-23）----
// resetCodeInsecureTotal release 模式下重置码落到 log 通道的次数。
// log 通道=验证码明文写进服务端日志，任何有日志读取权的人可为任意绑定邮箱账号改密；
// 生产环境该计数非 0 就说明 SMTP 未配或 reset_code_channel 未切 smtp，用户其实无法自助找回密码。
var resetCodeInsecureTotal uint64

// IncResetCodeInsecure 生产态走了 log 通道发重置码 +1（api.SendResetCode 调用）
func IncResetCodeInsecure() { atomic.AddUint64(&resetCodeInsecureTotal, 1) }

// ---- 批三 M1：Token 欠账计数（2026-09-23）----
// tokenDebtTokensTotal 因三桶余额皆空而**未能扣减**、仍挂在欠账表里的 token 累计量。
// 落账失败=公司侧漏收（客户白用 AI），此前静默 return nil 无人知晓，故必须显式暴露成指标。
var tokenDebtTokensTotal uint64

// AddTokenDebtTokens 记入未扣减成功的欠账 token 量（billing 扣减三桶皆空时调用）
func AddTokenDebtTokens(n uint64) { atomic.AddUint64(&tokenDebtTokensTotal, n) }

// ---- C1 内容安全闸门计数 ----// contentSafetyHitTotal 命中词库/机审次数（含 shadow 观察）
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

// ============================================================
// F6 收口(2026-09-14)：通道出站死信指标
// 出站队列此前只有 /admin 通道页可见死信列表，Prometheus 侧零指标——
// 死信堆积（如通道凭据失效、微信端点持续 5xx）无人感知，属运维盲区。
// 补两枚：① 计数 ai_scrm_channel_dead_letter_total{reason} 追踪进入死信的速率；
//        ② gauge  ai_scrm_channel_dead_letter_pending 反映当前积压水位（供告警阈值）。
// ============================================================

var (
	channelDeadLetterTotal   sync.Map // reason(fatal|exhausted) -> *uint64
	channelDeadLetterPending int64    // 当前 failed 出站数（每轮扫描回填）
)

// IncChannelDeadLetter 出站死信计数 +1（reason 维度）。
func IncChannelDeadLetter(reason string) {
	if p, ok := channelDeadLetterTotal.Load(reason); ok {
		atomic.AddUint64(p.(*uint64), 1)
		return
	}
	p := new(uint64)
	actual, _ := channelDeadLetterTotal.LoadOrStore(reason, p)
	atomic.AddUint64(actual.(*uint64), 1)
}

// SetChannelDeadLetterPending 回填当前出站死信积压数（gauge）。
func SetChannelDeadLetterPending(n int64) { atomic.StoreInt64(&channelDeadLetterPending, n) }

// GetChannelDeadLetterPending 读取当前出站死信积压数（供 /status 健康检查）。
func GetChannelDeadLetterPending() int64 { return atomic.LoadInt64(&channelDeadLetterPending) }

// ============================================================
// G-5 观测收口(2026-09-24)：AI 降级原因计数
// 降级分支此前只写日志，回归断言只能 grep 日志文件（uat.sh 第七节旧写法）——
// 日志路径/格式一变就假红，线上也没有可告警的面。改成 /metrics 计数器后，
// 断言走 HTTP 接口，与其余观测位同一口径。
// reason 只有有限几类（quota_exhausted / no_ai_model），**不带租户标签**：
// 租户数会直接变成 series 基数，这类"按原因看速率"的指标不需要归因到租户。
// ============================================================

// aiFallbackTotal reason -> 走规则话术（没调模型）的次数
var aiFallbackTotal sync.Map

// IncAIFallback 记一次"本轮 AI 回复没走模型、由规则兜底"（reason 维度）。
func IncAIFallback(reason string) {
	if p, ok := aiFallbackTotal.Load(reason); ok {
		atomic.AddUint64(p.(*uint64), 1)
		return
	}
	p := new(uint64)
	actual, _ := aiFallbackTotal.LoadOrStore(reason, p)
	atomic.AddUint64(actual.(*uint64), 1)
}

// renderLabeledCounter 渲染单标签(reason)计数器（供死信指标复用）。
func renderLabeledCounter(b *[]byte, name, help, label string, m *sync.Map) {
	type entry struct {
		key   string
		value uint64
	}
	var entries []entry
	m.Range(func(k, v interface{}) bool {
		key, ok := k.(string)
		p, ok2 := v.(*uint64)
		if !ok || !ok2 {
			return true
		}
		entries = append(entries, entry{key: key, value: atomic.LoadUint64(p)})
		return true
	})
	if len(entries) == 0 {
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
	*b = append(*b, fmt.Sprintf("# HELP %s %s\n", name, help)...)
	*b = append(*b, fmt.Sprintf("# TYPE %s counter\n", name)...)
	for _, e := range entries {
		*b = append(*b, fmt.Sprintf("%s{%s=%q} %d\n", name, label, promEscape(e.key), e.value)...)
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

	// ---- P2-2 指标：Redis 故障回复投递认领降级（>0 即多实例互斥失守）----
	b = append(b, "# HELP ai_scrm_reply_delivery_degrade_total reply delivery claim degraded to local arbitration due to Redis error\n"...)
	b = append(b, "# TYPE ai_scrm_reply_delivery_degrade_total counter\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_reply_delivery_degrade_total %d\n", atomic.LoadUint64(&replyDeliveryDegradeTotal))...)

	// ---- 主动触达四计数（触达最小闭环 2026-09-23）----
	// queued 与 sent 的差值 = 卡在出站队列里的触达任务数，运维看这两个数的剪刀差即可判断
	// "投递链路坏了"还是"策略拦得多"（skipped 大但 failed=0 属正常态）。
	b = append(b, "# HELP ai_scrm_outreach_queued_total outreach tasks handed to outbound queue\n"...)
	b = append(b, "# TYPE ai_scrm_outreach_queued_total counter\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_outreach_queued_total %d\n", atomic.LoadUint64(&outreachQueuedTotal))...)
	b = append(b, "# HELP ai_scrm_outreach_skipped_total outreach tasks skipped by policy (no channel/out-of-window/inactive)\n"...)
	b = append(b, "# TYPE ai_scrm_outreach_skipped_total counter\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_outreach_skipped_total %d\n", atomic.LoadUint64(&outreachSkippedTotal))...)
	b = append(b, "# HELP ai_scrm_outreach_sent_total outreach tasks confirmed delivered by outbound receipt\n"...)
	b = append(b, "# TYPE ai_scrm_outreach_sent_total counter\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_outreach_sent_total %d\n", atomic.LoadUint64(&outreachSentTotal))...)
	b = append(b, "# HELP ai_scrm_outreach_failed_total outreach tasks failed (send error/exhausted retries/missing receipt)\n"...)
	b = append(b, "# TYPE ai_scrm_outreach_failed_total counter\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_outreach_failed_total %d\n", atomic.LoadUint64(&outreachFailedTotal))...)

	// ---- D3 用量预警与催缴四计数（2026-09-23）----
	// 判读口径：usage_alert_skipped_total > 0 而 usage_alert_sent_total = 0，
	// 说明预警"命中了但一封都没发出去"——十有八九是 SMTP/群机器人未配置，属上线首日静默失效。
	b = append(b, "# HELP ai_scrm_usage_alert_sent_total usage alerts delivered via at least one real channel\n"...)
	b = append(b, "# TYPE ai_scrm_usage_alert_sent_total counter\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_usage_alert_sent_total %d\n", atomic.LoadUint64(&usageAlertSentTotal))...)
	b = append(b, "# HELP ai_scrm_usage_alert_skipped_total usage alerts hit but no delivery channel available (SMTP/group unset)\n"...)
	b = append(b, "# TYPE ai_scrm_usage_alert_skipped_total counter\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_usage_alert_skipped_total %d\n", atomic.LoadUint64(&usageAlertSkippedTotal))...)
	b = append(b, "# HELP ai_scrm_dunning_sent_total dunning stage notifications delivered\n"...)
	b = append(b, "# TYPE ai_scrm_dunning_sent_total counter\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_dunning_sent_total %d\n", atomic.LoadUint64(&dunningSentTotal))...)
	b = append(b, "# HELP ai_scrm_dunning_suspended_total tenants auto-suspended by dunning grace period (data retained)\n"...)
	b = append(b, "# TYPE ai_scrm_dunning_suspended_total counter\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_dunning_suspended_total %d\n", atomic.LoadUint64(&dunningSuspendedTotal))...)

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

	// ---- 批二 S2 指标：重置码不安全通道（release 下命中 log）----
	b = append(b, "# HELP ai_scrm_reset_code_insecure_total password reset codes delivered via insecure log channel in release mode\n"...)
	b = append(b, "# TYPE ai_scrm_reset_code_insecure_total counter\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_reset_code_insecure_total %d\n", atomic.LoadUint64(&resetCodeInsecureTotal))...)

	// ---- 批三 M1 指标：Token 欠账（三桶皆空未扣减量，公司侧漏收）----
	b = append(b, "# HELP ai_scrm_token_debt_tokens_total tokens accrued but not deducted because all balance buckets were empty\n"...)
	b = append(b, "# TYPE ai_scrm_token_debt_tokens_total counter\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_token_debt_tokens_total %d\n", atomic.LoadUint64(&tokenDebtTokensTotal))...)

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

	// ---- F6 指标：通道死信速率 + 积压水位 ----
	renderLabeledCounter(&b, "ai_scrm_channel_dead_letter_total", "Outbound/inbound messages moved to dead-letter by reason", "reason", &channelDeadLetterTotal)
	renderLabeledCounter(&b, "ai_scrm_ai_fallback_total", "AI replies served by rule fallback (no model call) by reason", "reason", &aiFallbackTotal)
	b = append(b, "# HELP ai_scrm_channel_dead_letter_pending Current failed outbound messages (dead-letter backlog)\n"...)
	b = append(b, "# TYPE ai_scrm_channel_dead_letter_pending gauge\n"...)
	b = append(b, fmt.Sprintf("ai_scrm_channel_dead_letter_pending %d\n", atomic.LoadInt64(&channelDeadLetterPending))...)

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
