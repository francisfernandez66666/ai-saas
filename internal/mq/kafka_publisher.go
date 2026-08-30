// Package mq 消息中心：Kafka/Log 双实现、事件信封、Inbox 幂等、审计落库
package mq

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"ai-scrm/config"

	"github.com/segmentio/kafka-go"
)

// ============================================================
// KafkaCenter：segmentio/kafka-go 实现
// 生产者：按主题动态路由（AllowAutoTopicCreation），key=one_id 保证同用户有序
// 消费者组：Subscribe 登记 → StartConsumers 统一启动，消费前 Inbox 幂等抢占
// ============================================================

// KafkaCenter Kafka 生产者/消费者组实现（segmentio/kafka-go）
type KafkaCenter struct {
	cfg config.MQConfig // MQ 配置（brokers/前缀等）
	w   *kafka.Writer
	// 2026-08-22：多消费者 fan-out（同 topic 多个订阅方，与 LogCenter 语义对齐）
	handlers map[string][]EventHandler
}

// newKafkaCenter 构造 Kafka 生产者（无 broker 配置时返回错误，由 Init 降级 log 模式）
func newKafkaCenter(cfg config.MQConfig) (*KafkaCenter, error) {
	if len(cfg.Brokers) == 0 {
		return nil, fmt.Errorf("KAFKA_BROKERS 未配置")
	}
	w := &kafka.Writer{
		Addr:                   kafka.TCP(cfg.Brokers...),
		Balancer:               &kafka.Hash{}, // 按 key(one_id) 哈希分区
		AllowAutoTopicCreation: true,
		RequiredAcks:           kafka.RequireOne,
		BatchTimeout:           50 * time.Millisecond, // 攒批 50ms 降低小消息写放大
	}
	return &KafkaCenter{cfg: cfg, w: w, handlers: map[string][]EventHandler{}}, nil
}

// Publish 生产事件（Header 注入 + 审计落库 + kafka 写入）
func (c *KafkaCenter) Publish(ctx context.Context, topic string, tenantID uint, oneID string, eventType string, payload interface{}) error {
	env := buildEnvelope(ctx, topic, tenantID, oneID, eventType, payload)
	recordAudit(env, "sent")

	fullTopic := c.cfg.TopicPrefix + topic
	err := c.w.WriteMessages(ctx, kafka.Message{
		Topic:   fullTopic,
		Key:     []byte(env.Key),
		Value:   env.Payload,
		Headers: kafkaHeaders(env),
	})
	if err != nil {
		log.Printf("[MQ-Kafka] 发布失败 topic=%s event=%s trace=%s: %v", fullTopic, env.Header.EventID, env.Header.TraceID, err)
		return err
	}
	log.Printf("[MQ-Kafka] 已发布 topic=%s event=%s tenant=%d one=%s type=%s trace=%s",
		fullTopic, env.Header.EventID, tenantID, env.Header.OneID, eventType, env.Header.TraceID)
	return nil
}

// Subscribe 登记消费者回调（追加语义；StartConsumers 时统一建 reader）
func (c *KafkaCenter) Subscribe(topic string, handler EventHandler) {
	c.handlers[topic] = append(c.handlers[topic], handler)
	log.Printf("[MQ-Kafka] 已订阅 topic=%s（消费者数=%d）", c.cfg.TopicPrefix+topic, len(c.handlers[topic]))
}

// StartConsumers 为每个已订阅主题启动一个消费者组循环（阻塞，调用方放 goroutine）
// fan-out：循环内依次调用该 topic 全部 handler
func (c *KafkaCenter) StartConsumers(ctx context.Context) {
	for topic := range c.handlers {
		go c.consumeLoop(ctx, topic)
	}
}

func (c *KafkaCenter) consumeLoop(ctx context.Context, topic string) {
	fullTopic := c.cfg.TopicPrefix + topic
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:     c.cfg.Brokers,
		GroupID:     c.cfg.TopicPrefix + "group", // 同组多实例自动分摊分区
		GroupTopics: []string{fullTopic},
		MinBytes:    1,
		MaxBytes:    10e6,
	})
	defer func() {
		_ = reader.Close()
	}()
	log.Printf("[MQ-Kafka] 消费循环启动 topic=%s group=%s", fullTopic, c.cfg.TopicPrefix+"group")

	for {
		msg, err := reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return // 正常关闭
			}
			log.Printf("[MQ-Kafka] 拉取失败 topic=%s: %v", fullTopic, err)
			continue
		}

		env, herr := fromKafkaMessage(topic, msg)
		if herr != nil {
			log.Printf("[MQ-Kafka] 消息解析失败 topic=%s: %v", fullTopic, herr)
			_ = reader.CommitMessages(ctx, msg)
			continue
		}

		// Inbox 幂等抢占：event_id 已处理(done)则直接提交跳过；pending(首次)则执行
		processed, err := EnsureProcessed(env.Header.EventID, env.Header.TenantID, env.Header.OneID, topic)
		if err != nil {
			log.Printf("[MQ-Kafka] Inbox 抢占失败 event=%s: %v", env.Header.EventID, err)
			// 不提交，等待重投
			continue
		}
		if processed {
			_ = reader.CommitMessages(ctx, msg)
			continue
		}
		// H2修复(2026-08-27)：处理失败走进程内重试队列（指数退避，最多5次），不进DLQ
		if c.processWithRetry(ctx, env, topic) {
			_ = MarkInboxDone(env.Header.EventID)
		} else {
			MarkInboxFailed(env.Header.EventID) // 超限放弃，仅记日志，无死信队列
		}
		_ = reader.CommitMessages(ctx, msg)
	}
}

// processWithRetry 消费失败重试（H2，2026-08-27）：指数退避最多5次，全部失败返回 false。
// 各 handler 自身应幂等（Inbox 已防重复事件），此处重试仅应对瞬时失败。
func (c *KafkaCenter) processWithRetry(ctx context.Context, env Envelope, topic string) bool {
	const maxRetry = 5
	delay := time.Second
	for attempt := 0; attempt <= maxRetry; attempt++ {
		if attempt > 0 {
			time.Sleep(delay)
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
			log.Printf("[MQ-Kafka] 事件%s 第%d次重试", env.Header.EventID, attempt)
		}
		failed := false
		for i, h := range c.handlers[topic] {
			if err := h(ctx, env); err != nil {
				log.Printf("[MQ-Kafka] 处理失败 event=%s consumer#%d: %v", env.Header.EventID, i, err)
				failed = true
			}
		}
		if !failed {
			return true
		}
	}
	log.Printf("[MQ-Kafka] 事件%s 重试%d次仍失败，放弃(无DLQ)", env.Header.EventID, maxRetry)
	return false
}

// fromKafkaMessage kafka 消息 → 信封（Header 校验：铁律字段缺失即拒收）
func fromKafkaMessage(defaultTopic string, msg kafka.Message) (Envelope, error) {
	env := Envelope{
		Topic:   strings.TrimPrefix(msg.Topic, ""),
		Key:     string(msg.Key),
		Payload: msg.Value,
	}
	for _, h := range msg.Headers {
		switch h.Key {
		case "tenant_id":
			n, _ := strconv.ParseUint(string(h.Value), 10, 64)
			env.Header.TenantID = uint(n)
		case "one_id":
			env.Header.OneID = string(h.Value)
		case "event_id":
			env.Header.EventID = string(h.Value)
		case "event_time":
			if t, err := time.Parse(time.RFC3339Nano, string(h.Value)); err == nil {
				env.Header.EventTime = t
			}
		case "trace_id":
			env.Header.TraceID = string(h.Value)
		}
	}
	if env.Header.EventID == "" || env.Header.TenantID == 0 {
		return env, fmt.Errorf("Header 缺失铁律字段(event_id/tenant_id)")
	}
	_ = defaultTopic
	return env, nil
}

// Close 关闭生产者
func (c *KafkaCenter) Close() error {
	return c.w.Close()
}

var _ = json.Marshal // 保持导入对齐
