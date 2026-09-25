// Package mq 消息中心：Kafka/Log 双实现、事件信封、Inbox 幂等、审计落库
package mq

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"ai-scrm/config"
	"ai-scrm/internal/db"
	"ai-scrm/internal/logx"
	"ai-scrm/internal/model"

	"github.com/segmentio/kafka-go"
)

// eventSeq 进程内事件序号（newEventID 改用随机分量后已不再使用；P2-79 清理）
var eventSeq atomic.Uint64

// DefaultCenter 全局消息中心实例
var DefaultCenter MessageCenter

// Init 初始化消息中心（main 启动时调用）
// MQ_TYPE=log → LogCenter（默认，无需 Kafka）
// MQ_TYPE=kafka → KafkaCenter（真实生产者+消费者组）
func Init(cfg config.MQConfig) {
	switch cfg.Type {
	case "kafka":
		c, err := newKafkaCenter(cfg)
		if err != nil {
			log.Printf("[MQ] Kafka 初始化失败，降级 log 模式: %v", err)
			DefaultCenter = newLogCenter(cfg)
		} else {
			DefaultCenter = c
			log.Printf("[MQ] Kafka 消息中心就绪: brokers=%v prefix=%s", cfg.Brokers, cfg.TopicPrefix)
		}
	default:
		DefaultCenter = newLogCenter(cfg)
		log.Printf("[MQ] 消息中心 log 模式（MQ_TYPE=%s）", cfg.Type)
	}
}

// Publish 发布事件（业务侧唯一入口；Header 内部注入防伪造）
func Publish(ctx context.Context, topic string, tenantID uint, oneID string, eventType string, payload interface{}) error {
	if DefaultCenter == nil {
		return fmt.Errorf("消息中心未初始化")
	}
	err := DefaultCenter.Publish(ctx, topic, tenantID, oneID, eventType, payload)
	if err == nil && onPublishSuccess != nil {
		onPublishSuccess() // G-15：Kafka 发布计数回调
	}
	return err
}

// OnPublishSuccess 是 Kafka 消息发布成功的回调函数指针
// G-15 机制说明：
// 1. mq 包不能直接调用 metrics.IncKafkaPublish()（会循环依赖：mq→service→mq）
// 2. 解决方案：mq 包暴露注册接口 SetOnPublishSuccess()，由 service 包在 Init() 时注入回调
// 3. 每次 Kafka 消息发布成功后，mq.Publish() 自动调用此回调，触发 Prometheus 计数+1
// 回调函数指针，由 service 包注册（避免 mq→service 循环依赖）
var onPublishSuccess func()

// SetOnPublishSuccess 注册 Kafka 发布成功回调（service.InitMQ() 调用时注入）
// 回调函数由 metrics.IncKafkaPublish 实现，用于 Prometheus 指标计数
func SetOnPublishSuccess(fn func()) { onPublishSuccess = fn }

// Subscribe 注册消费者回调
func Subscribe(topic string, handler EventHandler) {
	if DefaultCenter != nil {
		DefaultCenter.Subscribe(topic, handler)
	}
}

// StartConsumers 启动消费循环
func StartConsumers(ctx context.Context) {
	if DefaultCenter != nil {
		DefaultCenter.StartConsumers(ctx)
	}
}

// Close 关闭
func Close() {
	if DefaultCenter != nil {
		_ = DefaultCenter.Close()
	}
}

// ============================================================
// 公共：审计落库 + Header 构建
// ============================================================

// buildEnvelope 构建信封（Header 注入唯一入口）
func buildEnvelope(ctx context.Context, topic string, tenantID uint, oneID string, eventType string, payload interface{}) Envelope {
	if oneID == "" {
		oneID = fmt.Sprintf("sys:t%d", tenantID)
	}
	// P1-3：trace 优先沿用请求链路（gin ctx 经 context.WithValue 透传），否则自造。
	// 键与生成器统一走 logx（G-15④）——此前这里手写 ctx.Value("trace_id") 字面量键，
	// 与 middleware.TraceIDKey / logx.TraceKey 是三份并列常量，任一处改名即静默断链。
	traceID := logx.NewTraceID()
	if t := logx.TraceFrom(ctx); t != "" {
		traceID = t
	}
	return Envelope{
		Header: EventHeader{
			TenantID:  tenantID,
			OneID:     oneID,
			EventID:   newEventID(),
			EventTime: time.Now(),
			TraceID:   traceID,
		},
		Topic:   topic,
		Key:     oneID,
		Payload: marshalPayload(map[string]interface{}{"event_type": eventType, "data": payload}),
	}
}

// recordAudit 事件审计落库（best-effort 异步，不阻断业务）
func recordAudit(env Envelope, status string) {
	go func() {
		defer func() { _ = recover() }()
		// 审计落库 3s 超时，best-effort 不阻塞业务主流程
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		rec := model.MessageEventRecord{
			TenantID:  env.Header.TenantID,
			OneID:     env.Header.OneID,
			EventID:   env.Header.EventID,
			EventType: extractEventType(env.Payload),
			Topic:     env.Topic,
			Key:       env.Key,
			Payload:   string(env.Payload),
			Status:    status,
			TraceID:   env.Header.TraceID, // G-15④：链路 ID 落库，发布/消费两段同 trace
		}
		if err := db.DB.WithContext(ctx).Model(&model.MessageEventRecord{}).Create(&rec).Error; err != nil {
			log.Printf("[MQ] 审计落库失败 event=%s: %v", env.Header.EventID, err)
		}
	}()
}

// 事件消费终态（G-15① 两阶段台账的第二阶段）
const (
	// StatusConsumed 消费者处理成功（含"幂等判定为已处理而跳过"）
	StatusConsumed = "consumed"
	// StatusDeadLetter 重试耗尽后放弃。行内保留 payload+err_msg，是这条事件的**唯一副本**
	// （offset 已提交 / 进程内总线无重投），清理器必须放行此状态，见 service.CleanupMQTables
	StatusDeadLetter = "dead_letter"
)

// markEventOutcome 回写消费终态到发布阶段那条台账。
//
// 为什么同步写而不是像 recordAudit 那样异步：调用点在消费循环里，异步写会在进程崩溃/
// 退出时丢掉终态——而"崩溃前有没有消费完"恰是死信台账要回答的问题；一次按唯一键的
// UPDATE 成本可忽略，不值得为它冒"账没落地"的风险。
//
// 优先级规则：**dead_letter 一旦落下就不被 consumed 改写**。同一事件可扇出给多个消费者
// （log 模式每 handler 一个 goroutine），甲成功、乙失败时，台账必须留住"有人没吃到"这条
// 更差的事实——反向覆盖会把丢事件洗成全绿，那正是本项要消灭的形态。
//
// 行不存在时补建（RowsAffected=0）：发布阶段落库失败、或事件由外部生产者投递
// （Header 齐全即可消费）时，终态仍要留痕，否则"消费到了但查无此事件"更难排。
// 补建行必须显式带 TenantID——本函数在消费协程里调用，没有请求 ctx 可供盖章回调（C7 红线）。
func markEventOutcome(env Envelope, status, errText string) error {
	if db.DB == nil {
		return nil // 未接库（单测/启动早期）：终态留痕降级为无操作，绝不影响消费本身
	}
	errText = truncateRunes(errText, 500) // err_msg 列宽 512；按字符裁，按字节裁会把中文切成半个 UTF-8 序列，PG 直接拒写
	// 每条语句独立从 db.DB 起句（勿复用同一句柄跑两条链——GORM clone=0 会就地累加条件）
	q := db.DB.Model(&model.MessageEventRecord{}).Where("event_id = ?", env.Header.EventID)
	if status == StatusConsumed {
		q = q.Where("status <> ?", StatusDeadLetter)
	}
	res := q.Updates(map[string]interface{}{"status": status, "err_msg": errText, "trace_id": env.Header.TraceID})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		return nil
	}
	if status == StatusConsumed {
		// 更新没命中：可能是"行已被 dead_letter 占了"（按优先级放弃），也可能是行根本不存在。
		// 前者不许补建——补建会撞 event_id 唯一索引，且把已认账的死信重复记一条。
		var n int64
		if err := db.DB.Model(&model.MessageEventRecord{}). // g12:platform 消息中心台账按 event_id 定位，无请求租户作用域
									Where("event_id = ?", env.Header.EventID).Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
	}
	rec := model.MessageEventRecord{
		TenantID:  env.Header.TenantID,
		OneID:     env.Header.OneID,
		EventID:   env.Header.EventID,
		EventType: extractEventType(env.Payload),
		Topic:     env.Topic,
		Key:       env.Key,
		Payload:   string(env.Payload),
		Status:    status,
		TraceID:   env.Header.TraceID,
		ErrMsg:    errText,
	}
	// 消费腿台账：跑在消费协程里、无请求 ctx，租户由发布端在 rec.TenantID 上显式盖章，
	// 故语句直接点名平台级台账表（G-12 棘轮按语句里的模型名放行，不靠注释豁免）
	err := db.DB.Model(&model.MessageEventRecord{}).Create(&rec).Error
	if err == nil {
		return nil
	}
	// 发布阶段的审计是异步落库（recordAudit 开 goroutine，不阻断业务），消费可能跑得比那条
	// INSERT 快：撞 event_id 唯一索引时不报错、改走一次 UPDATE。不重试的话终态就永久丢了，
	// 而"这条事件到底有没有被吃掉"正是本函数存在的唯一理由。
	if !isUniqueViolation(err) {
		return err
	}
	retry := db.DB.Model(&model.MessageEventRecord{}).Where("event_id = ?", env.Header.EventID)
	if status == StatusConsumed {
		retry = retry.Where("status <> ?", StatusDeadLetter)
	}
	return retry.Updates(map[string]interface{}{"status": status, "err_msg": errText, "trace_id": env.Header.TraceID}).Error
}

// truncateRunes 按字符（非字节）截断，防止中/英混排的错误串被切成非法 UTF-8 序列。
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// isUniqueViolation 判唯一索引冲突（SQLSTATE 23505）。
// 不用 gorm.ErrDuplicatedKey：它要求 DB 开 TranslateError，本工程未开，照它判会恒 false。
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "23505")
}

// outcomeErrText 汇总一个事件本轮消费的全部失败原因（多 handler 各自可能失败）。
// 空切片返回空串（consumed 终态不该留脏 err_msg）。
func outcomeErrText(errs []string) string {
	return strings.Join(errs, "; ")
}

// recordConsumeOutcome 落消费终态并在死信时刷 WARN（两种 Center 共用，口径不许分叉）。
// 落库失败只 WARN：终态留痕是观测面，绝不能把一次 DB 抖动变成消费者报错、
// 进而让事件被无限重投。
func recordConsumeOutcome(env Envelope, status string, errs []string) {
	errText := outcomeErrText(errs)
	if err := markEventOutcome(env, status, errText); err != nil {
		log.Printf("[MQ] 消费终态落库失败 event=%s status=%s: %v", env.Header.EventID, status, err)
		return
	}
	if status == StatusDeadLetter {
		log.Printf("[MQ][WARN] 事件进入死信台账 event=%s topic=%s tenant=%d trace=%s 原因=%s",
			env.Header.EventID, env.Topic, env.Header.TenantID, env.Header.TraceID, errText)
	}
}

// extractEventType 从消息负载中提取 event_type 字段（用于审计落库事件名定位），缺失时返回 "unknown"
func extractEventType(payload []byte) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(payload, &m) == nil {
		if v, ok := m["event_type"]; ok {
			var s string
			if json.Unmarshal(v, &s) == nil {
				return s
			}
		}
	}
	return "unknown"
}

// randomHex 用 crypto/rand 生成 n 字节随机数的十六进制串（事件ID/锁值等）
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// kafkaHeaders 信封 → Kafka Headers（铁律：tenant_id/one_id/event_id/event_time 必带）
func kafkaHeaders(env Envelope) []kafka.Header {
	return []kafka.Header{
		{Key: "tenant_id", Value: []byte(strconv.FormatUint(uint64(env.Header.TenantID), 10))},
		{Key: "one_id", Value: []byte(env.Header.OneID)},
		{Key: "event_id", Value: []byte(env.Header.EventID)},
		{Key: "event_time", Value: []byte(env.Header.EventTime.Format(time.RFC3339Nano))},
		{Key: "trace_id", Value: []byte(env.Header.TraceID)},
	}
}
