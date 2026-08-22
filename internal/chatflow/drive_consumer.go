package chatflow

// ============================================================
// 业务层 flow_drive 消费者（Phase C，2026-08-22）
//
// 架构定位：业务系统消费 Kafka 下行指令执行动作（目标架构·业务层）
// 任务类型：
//   send_message —— 代发消息落库（客户轮询可见；content 不再经 CDP）
//   requeue      —— 刷新心跳+日志（真续跑依赖编排层上下文，专项）
// 幂等：send_message 走 Inbox 抢占防重复代发
// ============================================================

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/mq"
	"ai-scrm/internal/model"
)

// StartDriveConsumer 注册 flow_drive 消费者（main 启动调用一次）
func StartDriveConsumer() {
	mq.Subscribe(mq.TopicFlowDrive, func(ctx context.Context, env mq.Envelope) error {
		return mq.WithInbox(ctx, env, handleDriveEvent)
	})
	log.Println("[业务驱动] flow_drive 消费者已注册（下行指令执行口）")
}

// handleDriveEvent 单条下行指令处理
func handleDriveEvent(ctx context.Context, env mq.Envelope) error {
	var evt struct {
		Data mq.FlowDriveEvent `json:"data"`
	}
	if err := json.Unmarshal(env.Payload, &evt); err != nil {
		return err
	}
	drive := evt.Data
	tid := env.Header.TenantID

	switch drive.TaskType {
	case "send_message":
		return driveSendMessage(tid, drive)
	case "requeue":
		// 巡检回收信号：心跳已由巡检重置，此处仅留执行痕迹
		log.Printf("[业务驱动] requeue 信号 instance=%d node=%v（真续跑待编排层上下文专项）",
			drive.InstanceID, drive.Payload["current_node"])
		return nil
	default:
		log.Printf("[业务驱动] 未知任务类型 %q，忽略", drive.TaskType)
		return nil
	}
}

// driveSendMessage 代发消息：落库即对客户端轮询可见
func driveSendMessage(tenantID uint, drive mq.FlowDriveEvent) error {
	cid, _ := drive.Payload["customer_id"].(float64)
	convID, _ := drive.Payload["conversation_id"].(float64)
	content, _ := drive.Payload["content"].(string)
	senderType, _ := drive.Payload["sender_type"].(string)
	if cid == 0 || convID == 0 || content == "" {
		log.Printf("[业务驱动] send_message 参数不全，丢弃: %+v", drive.Payload)
		return nil
	}
	if senderType == "" {
		senderType = "ai" // 下行指令默认 AI 代发语义
	}
	msg := model.Message{
		TenantID:       tenantID,
		ConversationID: uint(convID),
		CustomerID:     uint(cid),
		SenderType:     senderType,
		Content:        content,
		MessageType:    "text",
		CreatedAt:      time.Now(),
	}
	if err := db.DB.Create(&msg).Error; err != nil {
		return err
	}
	log.Printf("[业务驱动] send_message 已执行: 消息%d → 会话%v(客户%v)", msg.ID, convID, cid)
	return nil
}
