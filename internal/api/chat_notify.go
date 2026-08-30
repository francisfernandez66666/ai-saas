// 对话通知助手：与对话处理解耦的独立推送/通知函数集合。
package api

// ============================================================
// chat.go 模块拆分（P2 巨石拆分，2026-08-29 第一步）
//
// 将 chat.go 内与对话处理解耦的独立通知助手迁移至此，降低 chat.go 单文件体量。
// 纯同包搬迁，不改行为；调用方（chat.go 主/测试handler）引用不变。
// ============================================================

import (
	"context"
	"fmt"
	"log"
	"time"

	"ai-scrm/internal/mq"
	"ai-scrm/internal/service"
)

/*
publishConversationMsg 发布对话消息事件

用途：将对话事件发布到消息队列和数据飞轮，用于 CDP 数据摄入。

参数：
  - tenantID: 租户 ID
  - customerID: 客户 ID
  - route: 对话路由（如客服/销售）
  - emotion: 情绪识别结果（NLP 语义分析，合规零方来源）

设计说明：
  - emotion 由 strategy 情绪识别提供，用于 CDP 态度维打标
  - 红线禁止由行为硬推态度，必须基于 NLP 语义分析
  - 对话事件同时进入数据飞轮（脱敏在 Collect 内完成）
*/
func publishConversationMsg(tenantID uint, customerID uint, route, emotion string) {
	// 发布到消息队列，供下游消费者处理
	if err := mq.Publish(context.Background(), mq.TopicUserEvent, tenantID,
		fmt.Sprintf("c:%d", customerID), "conversation_msg",
		mq.UserEvent{EventType: "behavior", EventName: "conversation_msg",
			Attributes: map[string]any{"customer_id": customerID, "route": route, "emotion": emotion},
			OccurredAt: time.Now()}); err != nil {
		log.Printf("[MQ] conversation_msg 事件发布失败: %v", err)
	}
	// P2 collector：对话事件进数据飞轮（脱敏在 Collect 内完成；URL 空则丢弃）
	service.Collect("conversation", tenantID, map[string]any{
		"customer_id": customerID, "route": route, "emotion": emotion,
	})
}
