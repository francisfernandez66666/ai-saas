// 出站事件 webhook 数据模型（D6，2026-09-12）——补齐"客户集成"最后一块。
// 租户自助配置回调 URL + 订阅事件，业务关键事件（payment.paid/order.refunded/…）签名推送。
package model

import "time"

// webhook 事件类型（订阅以 CSV 存 events 列，值取自下列常量）
const (
	WebhookEventPaymentPaid   = "payment.paid"
	WebhookEventOrderRefunded = "order.refunded"
	WebhookEventLeadCaptured  = "lead.captured"
	WebhookEventHumanAssigned = "human.assigned"
)

// 投递状态
const (
	WebhookDeliveryPending = "pending"
	// WebhookDeliverySending 取单预占态（P2-3，2026-09-20 批三）：worker 同事务 FOR UPDATE SKIP LOCKED
	// 取到期 pending 即置 sending，消除多实例/崩溃换主窗口的双投；持有者崩溃超 5min 由下轮复活回 pending
	WebhookDeliverySending   = "sending"
	WebhookDeliveryDelivered = "delivered"
	WebhookDeliveryDead      = "dead"
)

// TenantWebhook 租户出站回调订阅
type TenantWebhook struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	TenantID   uint       `gorm:"index;not null" json:"tenant_id"`
	Name       string     `gorm:"size:60" json:"name"`
	URL        string     `gorm:"size:300;not null" json:"url"`
	Secret     string     `gorm:"size:200" json:"-"`      // HMAC 密钥，明文不外泄（列表回显掩码）
	Events     string     `gorm:"size:300" json:"events"` // CSV: payment.paid,order.refunded
	Active     bool       `gorm:"default:true;index" json:"active"`
	FailCount  int        `gorm:"default:0" json:"fail_count"` // 连续失败计数（成功清零）
	DisabledAt *time.Time `json:"disabled_at"`                 // 熔断停用时间（24h 失败累积）
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// TableName 指定表名
func (TenantWebhook) TableName() string { return "tenant_webhooks" }

// WebhookDelivery 单次事件投递记录（队列 + 重试 + 死信审计）
type WebhookDelivery struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	TenantID    uint       `gorm:"index;not null" json:"tenant_id"`
	WebhookID   uint       `gorm:"index;not null" json:"webhook_id"`
	Event       string     `gorm:"size:40;not null" json:"event"`
	Payload     string     `gorm:"type:text" json:"payload"` // JSON
	Status      string     `gorm:"size:20;default:pending;index" json:"status"`
	Attempts    int        `gorm:"default:0" json:"attempts"`
	NextRetryAt *time.Time `gorm:"index" json:"next_retry_at"`
	LastError   string     `gorm:"size:300" json:"last_error"`
	DeliveredAt *time.Time `json:"delivered_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// TableName 指定表名
func (WebhookDelivery) TableName() string { return "webhook_deliveries" }
