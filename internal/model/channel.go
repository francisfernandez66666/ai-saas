// 通道模型（W2/W6，2026-09-12）：企微自建应用/微信客服/微信公众号 接入配置与出站队列。
// 凭据密文列（*_cipher）以 pkg/crypto(AES-256-GCM, JWT_SECRET 派生) 落库，出接口一律掩码，明文仅创建时一次性回显。
package model

import "time"

// 通道类型常量（对应 SCRM 三大微信生态入口）
const (
	ChannelTypeWecomApp = "wecom_app" // 企业微信自建应用（客户企业内部 + 互联）
	ChannelTypeWecomKf  = "wecom_kf"  // 微信客服（企微生态下的外部微信用户）
	ChannelTypeWechatMP = "wechat_mp" // 微信公众号（订阅号/服务号）
)

// 通道状态常量
const (
	ChannelStatusActive     = "active"     // 启用（入站可路由、出站可发）
	ChannelStatusDisabled   = "disabled"   // 停用
	ChannelStatusUnverified = "unverified" // 已录入但未通过连通性验证
)

// Channel 接入通道配置表
type Channel struct {
	ID           uint   `gorm:"primaryKey" json:"id"`
	TenantID     uint   `gorm:"index;not null" json:"tenant_id"`           // 归属租户
	Type         string `gorm:"size:20;not null;index" json:"type"`        // wecom_app|wecom_kf|wechat_mp
	Name         string `gorm:"size:100;not null" json:"name"`             // 展示名（顾问端可见，不露渠道痕迹由前端控制）
	CorpID       string `gorm:"column:corpid;size:64;index" json:"corpid"` // 企微 corpid / 公众号主体标识（明文，非机密）
	AppID        string `gorm:"column:appid;size:64;index" json:"appid"`   // 公众号 appid / 企微应用 agentid 关联（明文）
	SecretCipher string `gorm:"column:secret_cipher;size:512" json:"-"`    // 应用/公众号 secret（密文，出接口不回显）
	TokenCipher  string `gorm:"column:token_cipher;size:512" json:"-"`     // 回调 Token（密文）
	AesKeyCipher string `gorm:"column:aeskey_cipher;size:512" json:"-"`    // EncodingAESKey（密文）
	Status       string `gorm:"size:20;default:unverified" json:"status"`  // active|disabled|unverified
	DepartmentID uint   `gorm:"index" json:"department_id"`                // 归属部门（数据范围对齐 OrgResolve）
	ConfigJSON   string `gorm:"type:text" json:"config_json"`              // 通道差异化扩展（如 48h 窗口、菜单 ID）
	// KfCursor D14 修复(2026-09-14)：微信客服同步游标独立成列——旧实现读改写整个
	// config_json（与管理员编辑凭据/参数并发时互相覆盖丢键），列写天然原子、无丢失更新。
	KfCursor string `gorm:"column:kf_cursor;size:256" json:"-"`

	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `gorm:"index" json:"-"` // 软删（保留审计）
}

// TableName 指定表名
func (Channel) TableName() string { return "channels" }

// 出站消息状态常量（W6）
const (
	OutboundPending = "pending" // 待发送
	// OutboundSending 取单预占态（P2-3，2026-09-20 批三）：FOR UPDATE SKIP LOCKED 取单同事务置
	// sending，"取到即锁定"消除多实例/崩溃换主窗口的双发；持有者崩溃由 worker 按 updated_at 超 5min 复活回 pending
	OutboundSending = "sending"
	OutboundSent    = "sent"   // 已发送
	OutboundFailed  = "failed" // 超过最大重试，进死信（/admin 通道页可见，可人工重发）
)

// ChannelOutbound 出站消息队列：把"回复产生"与"通道发送"解耦，可靠性靠 main.go 后台 ticker 出站 worker 重试。
type ChannelOutbound struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	TenantID       uint       `gorm:"index;not null" json:"tenant_id"`
	ChannelID      uint       `gorm:"index;not null" json:"channel_id"`
	CustomerID     uint       `gorm:"index" json:"customer_id"`
	ConversationID uint       `gorm:"index" json:"conversation_id"`
	Content        string     `gorm:"type:text" json:"content"`                    // 出站正文（本方消息，非客户 PII）
	MsgType        string     `gorm:"size:20;default:text" json:"msg_type"`        // text|markdown|image...
	Status         string     `gorm:"size:20;default:pending;index" json:"status"` // pending|sent|failed
	Retries        int        `gorm:"default:0" json:"retries"`                    // 已重试次数（≤5 退避，超则 failed）
	NextRetryAt    *time.Time `gorm:"index" json:"next_retry_at"`                  // 下次可发送时间（指数退避锚点）
	Error          string     `gorm:"size:500" json:"error"`                       // 最近失败原因
	SentAt         *time.Time `json:"sent_at"`                                     // 成功发送时间
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// TableName 指定表名
func (ChannelOutbound) TableName() string { return "channel_outbound" }

// ChannelIdentity 渠道客户标识 ↔ 站内客户 映射（OneID 桥，W2 配套）。
// 唯一锚 (channel_id, external_id)：同一渠道同一外部号只对应一个站内客户，入站据此复用/建档。
type ChannelIdentity struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	TenantID   uint      `gorm:"index;not null" json:"tenant_id"`
	ChannelID  uint      `gorm:"index;not null" json:"channel_id"`
	CustomerID uint      `gorm:"index;not null" json:"customer_id"`
	ExternalID string    `gorm:"size:128;not null" json:"external_id"` // external_userid / openid / open_kfid
	StaffID    string    `gorm:"size:128" json:"staff_id"`             // 企微接待成员 userid（顾问归属）
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// TableName 指定表名
func (ChannelIdentity) TableName() string { return "channel_identities" }

// ChannelInboundMsg D4 修复(2026-09-14)：入站幂等去重表。
// 微信/企微回调超时重推是常态——旧实现无 MsgId 去重，一次重推=客户收到双份 AI 回复。
// 唯一锚 (channel_id, msg_id)：ProcessInbound 先抢占插入，冲突即视为重放直接 ack 丢弃。
// D2 配套：失败计数记录处理失败，同批重拉时成功者被 processed 跳过、失败者有限重试，
// 超限(5 次)进死信不再阻塞游标推进（防毒消息卡死轮询）。
type ChannelInboundMsg struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	TenantID  uint      `gorm:"index;not null" json:"tenant_id"`
	ChannelID uint      `gorm:"not null;uniqueIndex:idx_chan_msg" json:"channel_id"`
	MsgID     string    `gorm:"size:128;not null;uniqueIndex:idx_chan_msg" json:"msg_id"`
	Status    string    `gorm:"size:20;default:processing;index" json:"status"` // processing|processed|failed
	Attempts  int       `gorm:"default:0" json:"attempts"`                      // 处理尝试次数（首次=1）
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName 指定表名
func (ChannelInboundMsg) TableName() string { return "channel_inbound_msgs" }
