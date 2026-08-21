package mq

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ============================================================
// 消息中心（SAAS_PLAN §2.5 / §十五）
//
// 架构真理（铁律）：
// 1. Kafka 只做异步事件流转，绝不承担同步查询；禁止 Request-Reply
// 2. CDP 写收口于 Kafka 事件消费者，业务系统零直写
// 3. Header 强制规范：tenant_id / one_id / event_id / event_time
//    tenant_id 由本包从调用参数注入，业务代码不可伪造
// 4. 分区键 key = one_id（同用户消息有序）
//
// 四类固定主题：
//   user_event        上行：业务层/平台层 → 流程引擎 + CDP 消费者
//   flow_drive        下行：流程引擎 → 业务系统（任务/触达/断点续跑）
//   flow_result       回流：业务系统 → 流程引擎 + CDP
//   tenant_cfg_event  配置流：配置中心 → 各引擎热加载
// ============================================================

// 四类固定主题常量
const (
	TopicUserEvent    = "user_event"
	TopicFlowDrive    = "flow_drive"
	TopicFlowResult   = "flow_result"
	TopicTenantCfgEvt = "tenant_cfg_event"
)

// EventHeader 事件 Header（所有消息必须携带，由 Publish 内部注入）
type EventHeader struct {
	TenantID  uint      `json:"tenant_id"`  // 租户ID（网关注入，不可伪造）
	OneID     string    `json:"one_id"`     // One ID 用户主键
	EventID   string    `json:"event_id"`   // 事件唯一 ID（幂等键）
	EventTime time.Time `json:"event_time"` // 事件发生时间
	TraceID   string    `json:"trace_id"`   // 链路追踪 ID
}

// Envelope 标准事件信封
type Envelope struct {
	Header  EventHeader `json:"header"`
	Topic   string      `json:"topic"`
	Key     string      `json:"key"`
	Payload []byte      `json:"payload"`
}

// EventHandler 事件处理回调（消费侧注册）
type EventHandler func(ctx context.Context, env Envelope) error

// MessageCenter 消息中心接口
// 系统内所有异步事件统一走此通道；MQ_TYPE=log 时降级打结构化日志
type MessageCenter interface {
	// Publish 发布事件（Header 由实现内部注入并落审计表）
	Publish(ctx context.Context, topic string, tenantID uint, oneID string, eventType string, payload interface{}) error

	// Subscribe 订阅事件（消费侧回调注册；log 模式仅登记不投递）
	Subscribe(topic string, handler EventHandler)

	// StartConsumers 启动消费循环（kafka 模式；log 模式空操作）
	StartConsumers(ctx context.Context)

	// Close 关闭连接
	Close() error
}

// ---- 事件模型（SAAS_PLAN §2.5）----

// UserEvent 用户业务行为事件（上行摄入 → CDP + 流程引擎）
type UserEvent struct {
	EventType  string         `json:"event_type"`  // behavior/environment/identity/attitude_zero_party/payment
	EventName  string         `json:"event_name"`  // page_view/add_to_cart/order_created/conversation_msg/lead_captured...
	AnchorType string         `json:"anchor_type"` // 身份锚点: phone/wechat_openid/device_id/union_id
	Attributes map[string]any `json:"attributes"`
	OccurredAt time.Time      `json:"occurred_at"`
}

// FlowDriveEvent 流程下行驱动（→ 业务系统）
type FlowDriveEvent struct {
	InstanceID uint           `json:"instance_id"`
	TaskType   string         `json:"task_type"` // send_message/follow_up/assign_advisor/requeue
	Payload    map[string]any `json:"payload"`
}

// FlowResultEvent 业务结果回传（回流 → 编排层 + CDP）
type FlowResultEvent struct {
	InstanceID uint           `json:"instance_id"`
	NodeID     string         `json:"node_id"`
	Result     string         `json:"result"` // success/failed/user_reply/lead_captured...
	Detail     map[string]any `json:"detail"`
}

// TenantCfgEvent 租户配置变更事件（配置流 → 各引擎热加载）
type TenantCfgEvent struct {
	TenantID uint   `json:"tenant_id"`
	CfgVer   string `json:"cfg_ver"`
	Action   string `json:"action"` // seed/upgrade/rollback
	Scope    string `json:"scope"`  // tags/flows/prompts/intents/params
}

// PaymentStatusEvent 支付状态事件（预留，topic=user_event 子事件）
type PaymentStatusEvent struct {
	OrderNo     string    `json:"order_no"`
	Status      string    `json:"status"` // paid/unpaid
	AmountCents int       `json:"amount_cents"`
	Channel     string    `json:"channel"` // wechat/alipay/manual
	PaidAt      time.Time `json:"paid_at"`
}

// newEventID 生成事件唯一ID（时间戳+随机，进程内唯一即可）
func newEventID() string {
	return fmt.Sprintf("%d-%04d", time.Now().UnixNano(), eventSeq.Add(1)%10000)
}

// marshalPayload payload 序列化兜底
func marshalPayload(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(fmt.Sprintf(`{"marshal_error":%q}`, err.Error()))
	}
	return b
}
