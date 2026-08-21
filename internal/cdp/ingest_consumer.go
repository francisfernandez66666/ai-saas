package cdp

import (
	"ai-scrm/internal/model"
	"log"
)

// IngestConsumer 事件消费者
type IngestConsumer interface {
	ConsumeChatMessage(model.Message) error
	ConsumeSystemEvent(model.EventLog) error
	Flush() error
}

// NewIngestConsumer 创建事件消费者
func NewIngestConsumer() IngestConsumer {
	return &ingestConsumer{}
}

type ingestConsumer struct{}

// ConsumeChatMessage 消费聊天消息
func (c *ingestConsumer) ConsumeChatMessage(msg model.Message) error {
	log.Printf("[CDP] Consume message customer=%d", msg.CustomerID)
	return nil
}

// ConsumeSystemEvent 消费系统事件
func (c *ingestConsumer) ConsumeSystemEvent(event model.EventLog) error {
	log.Printf("[CDP] Consume event customer=%d", event.CustomerID)
	return nil
}

// Flush 立即刷新
func (c *ingestConsumer) Flush() error {
	log.Println("[CDP] Flush triggered")
	return nil
}
