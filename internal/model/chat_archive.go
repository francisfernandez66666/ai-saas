// 会话存档模型（E8，2026-09-24）：企微「会话内容存档」拉回来的消息落库形态。
//
// 为什么这张表长得和 messages 不一样：messages 是**我们自己收发**的业务消息（一行一句回复），
// 存档是**企业合规留痕**——企微把整段会话（含撤回、含系统事件、含我们根本没参与的内部同事对话）
// 按 seq 顺序推给我们。两者的主键语义完全不同，硬塞进 messages 会污染 messages 的
// conversation_id/customer_id 不变式（存档里大量消息对不上站内客户）。
//
// 幂等锚是 (channel_id, msgid)：企微会重复推、我们也会重试同一 seq 段，
// 撞锚就跳过而不是 UPSERT 覆盖——已解密的正文被一次解错的空值覆盖掉，等于把证据毁了。
package model

import "time"

// ChatArchiveRecord 一条存档消息（原文解密后的结构化形态 + 解密失败留痕）。
type ChatArchiveRecord struct {
	ID           uint   `gorm:"primaryKey" json:"id"`
	TenantID     uint   `gorm:"index;not null" json:"tenant_id"`    // 归属租户（后台任务无请求 ctx，必须显式传，见 C7 红线）
	ChannelID    uint   `gorm:"index;not null" json:"channel_id"`   // 来源通道
	MsgID        string `gorm:"column:msgid;size:128" json:"msgid"` // 企微消息 ID：幂等锚（空则不参与去重）
	Seq          int64  `gorm:"index;not null" json:"seq"`          // 存档序号：游标推进依据
	PublicKeyVer int    `json:"public_key_ver"`                     // 该条用哪一版公钥加的密（对不上当前私钥即解不开）

	BizType    string `gorm:"size:16" json:"biz_type"`                   // business|system
	Action     string `gorm:"size:16" json:"action"`                     // send|recalled|switch
	FromUser   string `gorm:"column:from_user;size:64" json:"from_user"` // 发送者（企微 userid 或外部 userid）
	SenderName string `gorm:"size:64" json:"sender_name"`
	ToList     string `gorm:"type:text" json:"to_list"` // JSON 数组字符串（企微原始 to 列表）
	ChatType   string `gorm:"size:16" json:"chat_type"` // single|group（外部/内部单聊、群聊）
	ChatID     string `gorm:"column:chatid;size:64;index" json:"chatid"`

	MsgType     string `gorm:"size:24" json:"msg_type"`       // text/image/voice/video/file/link/weapp/card...
	ContentText string `gorm:"type:text" json:"content_text"` // 文本类正文（非文本类为空，只留 media_id）
	MediaID     string `gorm:"size:256" json:"media_id"`      // 媒体资源 ID（下载须存档许可+SDK，见 MediaStatus）
	MediaStatus string `gorm:"size:16" json:"media_status"`   // ""|pending|done|failed（无许可时永远空，不假装已下载）

	// DecryptError 解密失败原因（非空即这条只有信封、没有正文）。
	// 为什么失败也建行：游标按 seq 单调前移，遇到一条解不开就停住的话，
	// 同一 seq 会被反复重拉、整条同步链路永久卡死——宁可留一条"读不了"的记录，
	// 也不能让后面的全部消息跟着陪葬。
	DecryptError string     `gorm:"size:256" json:"decrypt_error"`
	MsgTime      *time.Time `gorm:"index" json:"msg_time"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// TableName 指定表名
func (ChatArchiveRecord) TableName() string { return "chat_archive_records" }

// 存档同步的媒体状态常量（媒体下载是凭据门能力，故单列状态而不是塞进 DecryptError）
const (
	ArchiveMediaNone    = ""        // 非媒体消息
	ArchiveMediaPending = "pending" // 已排期待下载（当前无 SDK 实现，不会自动进入）
	ArchiveMediaDone    = "done"    // 已下载
	ArchiveMediaFailed  = "failed"  // 下载失败
)
