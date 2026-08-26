package model

import (
	"encoding/json"
	"time"
)

// ============================================================
// 租户模型 - SaaS 多租户基础实体
// ============================================================

// Tenant 租户表
// SaaS 化改造：系统层一等实体，非业务字段，而是整个系统的组织单元
type Tenant struct {
	ID                 uint            `gorm:"primaryKey" json:"id"`                                    // 主键
	Name               string          `gorm:"size:100;not null" json:"name"`                           // 企业/个人名称
	Code               string          `gorm:"size:50;uniqueIndex;not null" json:"code"`                // 子域名标识，注册后不可改
	CustomDomain       *string         `gorm:"size:200;uniqueIndex" json:"custom_domain"`               // 白标自定义域名（指针：NULL可多条，唯一索引不冲突）
	Tier               string          `gorm:"size:20;default:personal" json:"tier"`                    // 版本：personal/enterprise/custom
	PlanID             uint            `gorm:"index;default:0" json:"plan_id"`                          // 当前套餐ID
	LogoURL            string          `gorm:"size:255" json:"logo_url"`                                // 白标 logo
	FaviconURL         string          `gorm:"size:255" json:"favicon_url"`                             // 浏览器图标
	PrimaryColor       string          `gorm:"size:7" json:"primary_color"`                             // 主题色 #hex
	SecondaryColor     string          `gorm:"size:7" json:"secondary_color"`                           // 辅助色
	ContactName        string          `gorm:"size:50" json:"contact_name"`                             // 联系人
	ContactPhone       string          `gorm:"size:20" json:"contact_phone"`                            // 联系手机
	ContactEmail       string          `gorm:"size:100" json:"contact_email"`                           // 联系邮箱
	Industry           string          `gorm:"size:50" json:"industry"`                                 // 行业
	Scale              string          `gorm:"size:20" json:"scale"`                                    // 规模：个人/小型/中型/大型
	TrialStartAt       *time.Time      `json:"trial_start_at"`                                          // 试用开始
	TrialEndAt         *time.Time      `json:"trial_end_at"`                                            // 试用结束
	SubscribedAt       *time.Time      `json:"subscribed_at"`                                           // 首次订阅时间
	ExpiredAt          *time.Time      `json:"expired_at"`                                              // 当前套餐到期时间
	GracePeriodEndAt   *time.Time      `json:"grace_period_end_at"`                                     // 宽限期截止
	MaxUsers           int             `json:"max_users"`                                               // 配额：最大用户数
	MaxCustomers       int             `json:"max_customers"`                                           // 配额：最大客户数
	MaxDepartments     int             `json:"max_departments"`                                         // 配额：最大部门数
	MaxAICalls         int             `gorm:"column:max_ai_calls_monthly" json:"max_ai_calls_monthly"` // 配额：每月 AI 调用次数
	MaxStorageMB       int             `json:"max_storage_mb"`                                          // 配额：存储空间 MB
	MaxKnowledgeBrands int             `json:"max_knowledge_brands"`                                    // 配额：品牌数
	MaxKnowledgeModels int             `json:"max_knowledge_models"`                                    // 配额：车型数
	UsedAICalls        int             `gorm:"column:used_ai_calls" json:"used_ai_calls"`               // 当月已用 AI 调用数
	AICallBalance      int             `gorm:"column:ai_call_balance" json:"ai_call_balance"`           // AI增量包余额（买断资产，不随月重置，商业化M2）
	// ---- Token 三桶（P1.5 Token统一计费 + M-R 邀请推广，2026-08-25）----
	// 扣减优先级（P1.5 生效）：③免费体验桶(未过期) → ①月度订阅额度(月底清零) → ②预充值余额(永久) → 降级规则话术
	MonthlyTokenQuota  int64           `gorm:"default:0" json:"monthly_token_quota"`                    // ①月度订阅额度(paid套餐)：每月发放
	MonthlyTokenUsed   int64           `gorm:"default:0" json:"monthly_token_used"`                     // ①当月已用（月底随重置清零）
	TokenBalance       int64           `gorm:"default:0" json:"token_balance"`                          // ②预充值余额：付费充值包 + 永久邀请奖励，买断不过期
	FreeTokenBalance   int64           `gorm:"default:0" json:"free_token_balance"`                     // ③免费体验桶：注册赠送 + 邀请注册奖励，有有效期
	FreeTokenExpiresAt *time.Time      `json:"free_token_expires_at"`                                   // ③到期时间（邀请叠加顺延；过期后整桶不可用）
	// ---- 邀请推广（M-R，2026-08-25）：多邀多得、单邀单个 ----
	InviteCode         string          `gorm:"size:12;uniqueIndex" json:"invite_code"`                  // 邀请码（signup 时生成；存量租户首次调用 EnsureInviteCode 补发）
	InvitedByTenantID  *uint           `gorm:"index" json:"invited_by_tenant_id"`                       // 邀请人租户ID——首绑唯一：仅注册时写入一次，重复邀请无效
	ReferralPaidRewarded bool          `gorm:"default:false" json:"referral_paid_rewarded"`             // 该受邀租户的首笔 paid 套餐奖励已发放给邀请人（幂等闸门）
	UsedCustomers      int             `json:"used_customers"`                                          // 当前客户总数
	UsedStorageBytes   int64           `json:"used_storage_bytes"`                                      // 当前存储占用
	UsageResetAt       *time.Time      `json:"usage_reset_at"`                                          // 用量重置时间（每月 1 日）
	Status             string          `gorm:"size:20;default:active" json:"status"`                    // 状态：trial/active/suspended/expired/cancelled
	CancelAt           *time.Time      `json:"cancel_at"`                                               // 账号注销申请时间（P4：次日零点生效禁登录；数据保留）
	FeaturesOverride   json.RawMessage `json:"features_override"`                                       // 功能覆盖（定制版额外开关）
	WhiteLabelConfig   json.RawMessage `json:"white_label_config"`                                      // 白标配置 JSON（站点名、CSS、脚本等）
	CreatedAt          time.Time       `json:"created_at"`                                              // 创建时间
	UpdatedAt          time.Time       `json:"updated_at"`                                              // 更新时间
	DeletedAt          *time.Time      `json:"deleted_at"`                                              // 软删除
}

// TableName 指定表名
func (Tenant) TableName() string {
	return "tenants"
}

// ============================================================
// 订阅套餐模型
// ============================================================

// SubscriptionPlan 订阅套餐表
type SubscriptionPlan struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`            // 主键
	Name               string    `gorm:"size:100;not null" json:"name"`   // 显示名：个人版/企业标准版/企业高级版/定制版
	Code               string    `gorm:"size:50;uniqueIndex" json:"code"` // 标识：personal/enterprise_std/enterprise_pro/custom
	Tier               string    `gorm:"size:20" json:"tier"`             // 版本：personal/enterprise/custom
	Description        string    `json:"description"`                     // 套餐描述（Markdown）
	PriceMonthlyCents  int       `json:"price_monthly_cents"`             // 月价（分）
	PriceYearlyCents   int       `json:"price_yearly_cents"`              // 年价（分）
	TrialDays          int       `json:"trial_days"`                      // 试用天数，默认 7
	MaxUsers           int       `json:"max_users"`                       // 最大用户数
	MaxDepartments     int       `json:"max_departments"`                 // 最大部门数配额（个人版=1个根部门）
	MaxCustomers       int       `json:"max_customers"`                   // 最大客户数
	MaxAICalls         int       `json:"max_ai_calls_monthly"`            // 每月 AI 调用次数配额
	MaxStorageMB       int       `json:"max_storage_mb"`                  // 存储空间 MB 配额
	MaxKnowledgeBrands int       `json:"max_knowledge_brands"`            // 品牌数配额
	MaxKnowledgeModels int       `json:"max_knowledge_models"`            // 车型数配额
	Features           string    `json:"features"`                        // 功能列表 JSON
	Highlights         string    `json:"highlights"`                      // 卖点列表 JSON
	IsActive           bool      `json:"is_active"`                       // 是否上架
	SortOrder          int       `json:"sort_order"`                      // 排序
	CreatedAt          time.Time `json:"created_at"`                      // 创建时间
	UpdatedAt          time.Time `json:"updated_at"`                      // 更新时间
}

// ============================================================
// 租户用户模型 - 替代 sys_users
// ============================================================
// (已移至 internal/model/tenant_user.go)

// ============================================================
// API Key 模型 - 定制版开放平台用
// ============================================================

// ApiKey API Key 表
// 商业化 M4（2026-08-23）补列：call_count 调用计数（供未来按调用计费）
type ApiKey struct {
	ID          uint       `gorm:"primaryKey" json:"id"`                 // 主键
	TenantID    *uint      `gorm:"index" json:"-"`                       // 租户ID，仅定制版使用
	Name        string     `gorm:"size:100" json:"name"`                 // 密钥名称
	KeyPrefix   string     `gorm:"size:10" json:"key_prefix"`            // sk-****abcd 可辨识前缀
	KeyHash     string     `gorm:"size:200;uniqueIndex" json:"key_hash"` // SHA256(key) 唯一
	Permissions string     `json:"permissions"`                          // ["chat", "customer:read", ...]
	LastUsedAt  *time.Time `json:"last_used_at"`                         // 最后使用时间
	CallCount   int64      `gorm:"default:0" json:"call_count"`          // 累计调用次数（M4 计量）
	ExpiresAt   *time.Time `json:"expires_at"`                           // 过期时间
	IsActive    bool       `gorm:"default:true" json:"is_active"`        // 是否激活
	CreatedAt   time.Time  `json:"created_at"`                           // 创建时间
	UpdatedAt   time.Time  `json:"updated_at"`                           // 更新时间
}

// ============================================================
// 订单模型 - 订单/计费相关
// ============================================================

// BillingOrder 订单表
// 商业化 M1（2026-08-23）补列：package_id/channel/manual_confirm/qr_content
// 支撑 pay_mode 三态收银台（mock/static_qr/sdk）；autoMigrate 幂等加列
type BillingOrder struct {
	ID                  uint       `gorm:"primaryKey" json:"id"`                // 主键
	OrderNo             string     `gorm:"size:32;uniqueIndex" json:"order_no"` // 订单号
	TenantID            *uint      `gorm:"index" json:"-"`                      // 租户ID
	PlanID              uint       `gorm:"index" json:"plan_id"`                // 套餐ID（legacy subscription_plans）
	PackageID           uint       `gorm:"index" json:"package_id"`             // 商业包ID（M2 packages 表）
	AmountCents         int        `json:"amount_cents"`                        // 实付金额（分）
	OriginalAmountCents int        `json:"original_amount_cents"`               // 原价（分），用于展示优惠
	Period              string     `gorm:"size:10" json:"period"`               // monthly/yearly/once
	PayChannel          string     `gorm:"size:20" json:"pay_channel"`          // wechat/alipay/manual（legacy，新单用 channel）
	Channel             string     `gorm:"size:20" json:"channel"`              // 支付路由：mock/manual/wechat/alipay（M1 pay_mode 三态落点）
	Status              string     `gorm:"size:20" json:"status"`               // pending/paid/refunding/refunded/closed/expired
	PaidAt              *time.Time `json:"paid_at"`                             // 支付时间（unpaid 为零值）
	RefundedAt          *time.Time `json:"refunded_at"`                         // 退款时间
	ExpireAt            *time.Time `json:"expire_at"`                           // 订单超时未支付自动关闭
	PaymentData         string     `json:"payment_data"`                        // 支付平台回调原始数据
	ManualConfirm       bool       `gorm:"default:false" json:"manual_confirm"` // 「我已付费」人工确认标记（static_qr 模式）
	InvoiceRequested    bool       `json:"invoice_requested"`                   // 是否申请发票
	InvoiceStatus       string     `gorm:"size:20" json:"invoice_status"`       // 发票状态
	QRContent           string     `gorm:"type:text" json:"qr_content"`         // 收款码内容（URL/base64，下单时从系统配置快照）
	Remark              string     `json:"remark"`                              // 备注
	CreatedAt           time.Time  `json:"created_at"`                          // 创建时间
	UpdatedAt           time.Time  `json:"updated_at"`                          // 更新时间
}

// ============================================================
// 使用量明细表
// ============================================================

// UsageRecord 用量明细表
type UsageRecord struct {
	ID        uint      `gorm:"primaryKey" json:"id"`                                   // 主键
	TenantID  *uint     `gorm:"index" json:"-"`                                         // 租户ID
	Date      string    `gorm:"size:10;uniqueIndex:idx_tenant_date_metric" json:"date"` // 日期 YYYY-MM-DD
	Metric    string    `gorm:"size:50" json:"metric"`                                  // ai_calls/message_count/storage_bytes/api_calls
	Value     int64     `json:"value"`                                                  // 数值
	CreatedAt time.Time `json:"created_at"`                                             // 创建时间
}

// ============================================================
// 租户操作审计日志
// ============================================================

// TenantAuditLog 租户操作审计日志
type TenantAuditLog struct {
	ID        uint      `gorm:"primaryKey" json:"id"`       // 主键
	TenantID  uint      `gorm:"index" json:"-"`             // 租户ID
	UserID    uint      `gorm:"index" json:"-"`             // 操作人ID
	Action    string    `gorm:"size:50" json:"action"`      // login/create_customer/export/config_change 等
	Resource  string    `gorm:"size:100" json:"resource"`   // 操作资源
	Detail    string    `json:"detail"`                     // 操作详情 JSON
	IP        string    `gorm:"size:45" json:"ip"`          // 操作IP
	UserAgent string    `gorm:"size:500" json:"user_agent"` // 用户代理
	CreatedAt time.Time `json:"created_at"`                 // 创建时间
}

// ============================================================
// 客户行为事件 - 上行摄入
// ============================================================

// MessageEvent 用户业务行为事件（上行摄入 → CDP + 流程引擎）
// 业务层/平台层生成浏览/加购/下单/会话等行为；行为后必然携带 Header.OneID + Header.TenantID
type MessageEvent struct {
	EventType  string         `json:"event_type"`  // behavior / environment / identity / attitude_zero_party / payment
	EventName  string         `json:"event_name"`  // 事件名，如 page_view / add_to_cart / order_created / conversation_msg
	AnchorType string         `json:"anchor_type"` // 身份锚点: phone / wechat_openid / device_id / union_id
	Attributes map[string]any `json:"attributes"`  // 事件属性
	OccurredAt time.Time      `json:"occurred_at"` // 发生时间
}

// ============================================================
// 表名注册
// ============================================================

// TableName 指定表名
func (ApiKey) TableName() string {
	return "api_keys"
}
// TableName 指定表名：billing_orders
func (BillingOrder) TableName() string {
	return "billing_orders"
}
// TableName 指定表名：usage_records
func (UsageRecord) TableName() string {
	return "usage_records"
}
// TableName 指定表名：tenant_audit_logs
func (TenantAuditLog) TableName() string {
	return "tenant_audit_logs"
}
// TableName 指定表名：message_events
func (MessageEvent) TableName() string {
	return "message_events"
}
