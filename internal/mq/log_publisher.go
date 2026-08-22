package mq

import (
	"context"
	"encoding/json"
	"log"

	"ai-scrm/config"
)

// ============================================================
// LogCenter：MQ_TYPE=log 降级实现（默认）
// Publish 打结构化 JSON 日志 + 审计落库；Subscribe 仅登记
// 业务代码零改动，切 MQ_TYPE=kafka 即上真实总线
// ============================================================

type LogCenter struct {
	cfg      config.MQConfig
	handlers map[string]EventHandler
}

func newLogCenter(cfg config.MQConfig) *LogCenter {
	return &LogCenter{cfg: cfg, handlers: map[string]EventHandler{}}
}

// Publish 结构化日志发布 + 本地分发
// P4 起 LogCenter 兼作"进程内事件总线"：无 Kafka 时订阅者（如 CDP IngestConsumer）
// 仍可闭环消费，切换 MQ_TYPE=kafka 后无缝升级为真实总线，业务与消费者代码零改动
func (c *LogCenter) Publish(ctx context.Context, topic string, tenantID uint, oneID string, eventType string, payload interface{}) error {
	env := buildEnvelope(topic, tenantID, oneID, eventType, payload)
	recordAudit(env, "sent")

	line, _ := json.Marshal(map[string]interface{}{
		"topic":      c.cfg.TopicPrefix + topic,
		"event_id":   env.Header.EventID,
		"tenant_id":  env.Header.TenantID,
		"one_id":     env.Header.OneID,
		"event_time": env.Header.EventTime,
		"trace_id":   env.Header.TraceID,
		"payload":    json.RawMessage(env.Payload),
	})
	log.Printf("[MQ-LOG] %s", string(line))

	// 本地异步分发（panic 隔离，不阻断发布方）
	if h, ok := c.handlers[topic]; ok {
		envCopy := env
		go func() {
			defer func() { _ = recover() }()
			_ = h(ctx, envCopy)
		}()
	}
	return nil
}

// Subscribe 登记（log 模式无投递通道，仅保留接口一致性）
func (c *LogCenter) Subscribe(topic string, handler EventHandler) {
	c.handlers[topic] = handler
	log.Printf("[MQ-LOG] 已登记订阅 topic=%s（log 模式不投递）", c.cfg.TopicPrefix+topic)
}

// StartConsumers 空操作
func (c *LogCenter) StartConsumers(ctx context.Context) {}

// Close 空操作
func (c *LogCenter) Close() error { return nil }
