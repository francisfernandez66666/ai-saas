// 消息中心台账观测位（G-15③，2026-09-24）
//
// 为什么单开一项：data_hygiene.go 那三项数的是"数据形态"（孤儿行、待归档、存档链路），
// 本项数的是"事件到底有没有被吃掉"。在 G-15 之前，message_event_records 只在**发布阶段**
// 落一行 sent，消费者成功/失败从不回写——于是 /status/detail 关于消息中心的一切永远全绿，
// 因为"没人看"和"没问题"在数据上长得一模一样。
//
// 现在消费终态会回写同一行（mq.markEventOutcome），dead_letter 就是"这条事件重试耗尽、
// 此后再没人处理过"的唯一副本（Kafka offset 已提交、进程内总线无重投）。本项把它数出来。
//
// 判级只到 warn，不判 crit：**单条死信的正确响应是"查台账看原因、必要时人工重放"，
// 不是半夜把人叫起来**。而且 log 模式（MQ_TYPE=log，默认形态）本来就允许瞬时失败，
// crit 会让一次 DB 抖动变成刷群。要收紧走 monitor_dead_letter_warn 热配。
package metrics

import (
	"errors"
	"strconv"
	"sync"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/mq"
)

// 探针实现接缝：单测注入固定计数，不依赖真库（真库形态由迁移 025 与冒烟脚本覆盖）
var (
	deadLetterCountFn = queryDeadLetters
	mqAuditProbeTTL   = 10 * time.Minute
)

// mqAuditCache 死信计数缓存（与 hygieneCache 同一 TTL 纪律：探测被反复调用，绝不逐次现算）
type mqAuditCache struct {
	at         time.Time
	deadLetter int64
	stale      bool
}

var (
	mqAuditMu    sync.Mutex
	mqAuditState mqAuditCache
)

// queryDeadLetters 在册死信条数（走迁移 025 的部分索引 idx_mq_event_records_dead）
func queryDeadLetters() (int64, error) {
	if db.DB == nil {
		// 未接库（单测/启动早期）：报错而非返回 0 —— 返回 0 会被缓存成"一条死信都没有"，
		// 而观测位最坏的失效形态就是把"没数据可读"读成"一切正常"（同 hygieneSnapshot 的 stale 口径）。
		return 0, errors.New("db 未初始化，死信计数不可得")
	}
	var n int64
	err := db.DB.Model(&model.MessageEventRecord{}).
		Where("status = ?", mq.StatusDeadLetter).Count(&n).Error
	return n, err
}

// mqAuditSnapshot 取死信计数（带 TTL 缓存）。返回 (计数, 是否沿用过旧值)。
// 取数失败沿用上一次值并标 stale：观测位不得因一次抖动清零假装健康（同 hygieneSnapshot 口径）。
func mqAuditSnapshot() (int64, bool) {
	mqAuditMu.Lock()
	defer mqAuditMu.Unlock()
	now := hygieneNow()
	if !mqAuditState.at.IsZero() && now.Sub(mqAuditState.at) < mqAuditProbeTTL {
		return mqAuditState.deadLetter, mqAuditState.stale
	}
	next := mqAuditCache{at: now, deadLetter: mqAuditState.deadLetter}
	if n, err := deadLetterCountFn(); err == nil {
		next.deadLetter = n
	} else {
		next.stale = true
	}
	mqAuditState = next
	return next.deadLetter, next.stale
}

// ResetMQAuditCache 清空死信探测缓存（测试与手工重探用；生产按 TTL 自然过期）
func ResetMQAuditCache() {
	mqAuditMu.Lock()
	mqAuditState = mqAuditCache{}
	mqAuditMu.Unlock()
}

// MQDeadLetters 对外暴露当前在册死信条数（带 TTL 缓存，供 /status/detail 直出为字段）。
// 与 DataHygiene() 同口径：冒烟脚本要的是稳定的数字，不是让人去翻 checks 数组。
func MQDeadLetters() int64 {
	n, _ := mqAuditSnapshot()
	return n
}

// mqAuditCheck 组装消息中心台账观测项（由 hygieneChecks 调用）
func mqAuditCheck() HealthCheck {
	n, stale := mqAuditSnapshot()
	warn := monitorCfgInt("monitor_dead_letter_warn", 1)
	ch := HealthCheck{
		Name:   "mq_dead_letters",
		Status: StatusOK,
		Value:  strconv.FormatInt(n, 10),
		WarnAt: strconv.FormatInt(warn, 10),
		CritAt: "-", // 不判 crit：单条死信查台账处理即可，不该刷群（见文件头）
		Desc: "重试耗尽后无人再处理的事件条数（payload 与失败原因都在 message_event_records 里，" +
			"清理器默认不删该状态，mq_dead_letter_retention_days=0 表示永久留档）",
	}
	if n >= warn {
		ch.Status = StatusWarn
	}
	if stale {
		ch.Value += "（沿用 " + mqAuditProbeTTL.String() + " 内的上一次取数，本轮查询失败）"
	}
	return ch
}
