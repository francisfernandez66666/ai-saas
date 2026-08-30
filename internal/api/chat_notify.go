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

// publishConversationMsg 发布 conversation_msg 用户事件（CDP 摄入入口）。
// P1-3（2026-08-29）：emotion 由 strategy 情绪识别提供（NLP 语义，合规零方来源），
// 用于 CDP 态度维打标；红线禁止由行为硬推态度
func publishConversationMsg(tenantID uint, customerID uint, route, emotion string) {
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
