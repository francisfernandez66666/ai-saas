package mq

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"sync/atomic"
	"time"

	"ai-scrm/config"
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"

	"github.com/segmentio/kafka-go"
)

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
	return DefaultCenter.Publish(ctx, topic, tenantID, oneID, eventType, payload)
}

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
func buildEnvelope(topic string, tenantID uint, oneID string, eventType string, payload interface{}) Envelope {
	if oneID == "" {
		oneID = fmt.Sprintf("sys:t%d", tenantID)
	}
	traceID := randomHex(8)
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
		}
		if err := db.DB.WithContext(ctx).Create(&rec).Error; err != nil {
			log.Printf("[MQ] 审计落库失败 event=%s: %v", env.Header.EventID, err)
		}
	}()
}

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
