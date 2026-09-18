// 通道适配器接口与注册表（W3-5 共用底座，2026-09-12）
// 三类通道（企微自建应用/微信客服/公众号）差异收口到 Adapter 实现；
// 入站解析、出站发送、token 获取统一走此接口，便于 httptest mock 全适配器回归（T8）。
package channel

import (
	"context"

	"ai-scrm/internal/model"
)

// InboundMessage 入站消息统一形态（各适配器解密解析后归一化，再喂给处理链）
type InboundMessage struct {
	ChannelID   uint
	TenantID    uint
	ChannelType string
	ExternalID  string // 渠道侧客户唯一标识（企微 external_userid / 公众号 openid / kf open_kfid 关联）
	StaffID     string // 企微：接待成员 userid（客服/侧边栏归属）
	MsgType     string // text/image/voice/event...
	Content     string // 文本正文（事件类为空）
	MsgID       string // D4 修复(2026-09-14)：渠道侧消息 ID，入站幂等去重锚（回调重推/轮询重拉共用）
	// EnvelopeID P1-7 修复(2026-09-15)：原始加密信封（密文+时间戳+nonce 的整段回调 body）
	// 摘要，作 MsgID 为空（老协议）时的去重兜底锚——字节级相同才判重放，
	// 客户连发同文案两条的信封必然不同（nonce/时间戳不同），不会误杀。
	EnvelopeID string
	IsEvent    bool   // 是否订阅/系统事件（change_contact/follow 等，走 CDP 摄入非对话）
	EventKey   string // 事件类型键
	ReceiveID  string // 校验用 corpid/appid（解密 receive_id）
	// TraceID E3(2026-09-19)：入站回调请求的 trace（回调 handler 注入；轮询等无请求上下文
	// 的来源由 ProcessInbound 自造）。headless worker 据此贯穿合并队列/AI 出站日志。
	TraceID string
}

// SendResult 出站发送结果
type SendResult struct {
	Sent  bool
	MsgID string
	Err   error // 网络/接口错误
	Fatal bool  // 不可重试（如 48h 窗口外、参数非法），直接进死信不空转退避
}

// Adapter 单通道类型适配器（无状态；凭据由入参传入，便于多租户复用同一实例）
type Adapter interface {
	Type() string
	// DecryptInbound 校验 msg_signature 并解密回调体，返回归一化入站消息。
	// rawQuery 提供 timestamp/nonce/msg_signature（query 参数），body 为回调 XML。
	DecryptInbound(cred *Credential, timestamp, nonce, msgSignature string, body []byte) (*InboundMessage, error)
	// VerifyURLEcho URL 验证：回显解密后的 echostr（明文模式返回原 echostr）。
	VerifyURLEcho(cred *Credential, msgSignature, timestamp, nonce, echostr string) (string, error)
	// SendText 发送一条文本/markdown 出站消息到 externalID 客户。
	SendText(ctx context.Context, cred *Credential, externalID, content, msgType string) SendResult
}

// registry 通道类型 → 适配器（启动装配，只读）
var registry = map[string]Adapter{}

// Register 注册适配器（各适配器 init() 调用）。
func Register(a Adapter) { registry[a.Type()] = a }

// AdapterFor 按通道记录取适配器。
func AdapterFor(ch *model.Channel) (Adapter, bool) {
	a, ok := registry[ch.Type]
	return a, ok
}
