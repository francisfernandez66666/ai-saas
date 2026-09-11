// Package mq 消息中心：Kafka/Log 双实现、事件信封、Inbox 幂等、审计落库
package mq

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"ai-scrm/config"
)

// ============================================================
// LogCenter：MQ_TYPE=log 降级实现（默认）
// Publish 打结构化 JSON 日志 + 审计落库；Subscribe 仅登记
// 业务代码零改动，切 MQ_TYPE=kafka 即上真实总线
// ============================================================

// LogCenter 进程内事件总线（MQ_TYPE=log 默认降级实现）
// P2-78 修复：handlers map 原无锁——Subscribe(启动期) 与 Publish(运行时读) 并发读写 map
// 触发 data race（靠启动顺序掩盖）。加 RWMutex 保护。
type LogCenter struct {
	cfg config.MQConfig // MQ 配置（仅用前缀打日志）
	// 2026-08-22：改多消费者 fan-out——同 topic 可多个订阅方（CDP 与流程引擎并行消费 user_event，各消费各的流）
	handlers map[string][]EventHandler
	mu       sync.RWMutex
	// P2-78：handler 分发信号量——log 模式每事件每 handler 一个 goroutine，
	// 高峰期无界暴涨；限到 32 并发，超限串行等待（事件本身幂等，慢一点可接受）
	sem chan struct{}
}

// newLogCenter 构造进程内事件总线（默认降级实现，不依赖 Kafka，订阅方异步 fan-out）
func newLogCenter(cfg config.MQConfig) *LogCenter {
	return &LogCenter{
		cfg:      cfg,
		handlers: map[string][]EventHandler{},
		sem:      make(chan struct{}, 32),
	}
}

// Publish 结构化日志发布 + 本地分发
// P4 起 LogCenter 兼作"进程内事件总线"：无 Kafka 时订阅者（如 CDP IngestConsumer）
// 仍可闭环消费，切换 MQ_TYPE=kafka 后无缝升级为真实总线，业务与消费者代码零改动
func (c *LogCenter) Publish(ctx context.Context, topic string, tenantID uint, oneID string, eventType string, payload interface{}) error {
	env := buildEnvelope(ctx, topic, tenantID, oneID, eventType, payload)
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

	// 本地异步分发（fan-out 到该 topic 全部订阅方；panic 隔离，不阻断发布方）
	// P1-37(2026-09-09)：原 `_ = h(ctx, envCopy)` 错误被吞、无重试——log 模式（默认）反而是
	// 可靠性最弱路径。现补同步重试 3 次指数退避，与 Kafka 侧重试口径对齐；仍失败记日志。
	if hs := c.snapshotHandlers(topic); len(hs) > 0 {
		for i := range hs {
			h := hs[i]
			envCopy := env
			go func() {
				// P2-78：信号量限并发，防止事件风暴撑爆 goroutine
				c.sem <- struct{}{}
				defer func() { <-c.sem; _ = recover() }()
				const maxRetry = 3
				delay := time.Millisecond * 200
				for attempt := 0; attempt <= maxRetry; attempt++ {
					if attempt > 0 {
						time.Sleep(delay)
						delay *= 2
					}
					if err := h(ctx, envCopy); err == nil {
						return
					} else {
						log.Printf("[MQ-LOG] 处理失败 event=%s consumer#%d 第%d次: %v", envCopy.Header.EventID, i, attempt, err)
					}
				}
				log.Printf("[MQ-LOG] 事件%s 重试3次仍失败，放弃", envCopy.Header.EventID)
			}()
		}
	}
	return nil
}

// snapshotHandlers 读锁下拷贝该 topic 的 handler 切片（P2-78：避免持锁执行 handler）
func (c *LogCenter) snapshotHandlers(topic string) []EventHandler {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]EventHandler(nil), c.handlers[topic]...)
}

// Subscribe 登记消费者（追加语义，同 topic 多消费者并存）
// P2-78：写锁保护 map；消费端用读锁快照，杜绝并发读写 race
func (c *LogCenter) Subscribe(topic string, handler EventHandler) {
	c.mu.Lock()
	c.handlers[topic] = append(c.handlers[topic], handler)
	n := len(c.handlers[topic])
	c.mu.Unlock()
	log.Printf("[MQ-LOG] 已登记订阅 topic=%s（进程内总线模式，消费者数=%d）",
		c.cfg.TopicPrefix+topic, n)
}

// StartConsumers 空操作
func (c *LogCenter) StartConsumers(ctx context.Context) {}

// Close 空操作
func (c *LogCenter) Close() error { return nil }
