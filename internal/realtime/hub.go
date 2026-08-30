// Package realtime WebSocket 实时推送中枢（P1-2，2026-08-29）
//
// 设计：仅推送"该客户有新消息"通知信号，前端收到即触发已有拉取（保留轮询作兜底），
// 最小化对既有可用对话链路的侵入。多实例下每实例独立 hub（推送仅本实例连接者收到，
// 跨实例覆盖由既有 Redis 锁/合并队列保证最终一致——实时性为体验增强，非强一致依赖）。
package realtime

import (
	"encoding/json"
	"sync"
)

// RealtimeEvent 推送事件（前端收到即触发对应拉取）
type RealtimeEvent struct {
	Type           string `json:"type"`        // new_message / typing / status
	CustomerID     uint   `json:"customer_id"` // 关联客户
	ConversationID uint   `json:"conversation_id"`
	SenderType     string `json:"sender_type"` // customer / ai / human
	TenantID       uint   `json:"tenant_id"`
}

// Client 单个 WS 连接订阅者
type Client struct {
	TenantID   uint // 租户ID（隔离用）
	UserID     uint // 顾问端（>0）；客户端的 UserID=0
	CustomerID uint // 客户端（>0）；顾问端的 CustomerID=0
	send       chan []byte
}

// Hub 连接注册表
type Hub struct {
	mu      sync.RWMutex
	clients map[*Client]bool
}

// DefaultHub 全局 hub 实例
var DefaultHub = NewHub()

// NewHub 构造空 hub
func NewHub() *Hub {
	return &Hub{clients: map[*Client]bool{}}
}

// NewClient 构造一个订阅者连接（send 通道内部创建，跨包安全）
func NewClient(tenantID, userID, customerID uint) *Client {
	return &Client{
		TenantID:   tenantID,
		UserID:     userID,
		CustomerID: customerID,
		send:       make(chan []byte, 16),
	}
}

// Send 非阻塞向连接推送（缓冲满丢弃，由轮询兜底）
func (cl *Client) Send(b []byte) bool {
	select {
	case cl.send <- b:
		return true
	default:
		return false
	}
}

// SendQueue 暴露只读发送通道，供连接写泵消费（勿外发数据）
func (cl *Client) SendQueue() <-chan []byte {
	return cl.send
}

// Register 注册连接
func (h *Hub) Register(cl *Client) {
	h.mu.Lock()
	h.clients[cl] = true
	h.mu.Unlock()
}

// Unregister 注销并关闭发送通道
func (h *Hub) Unregister(cl *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[cl]; ok {
		delete(h.clients, cl)
		close(cl.send)
	}
}

// Publish 向租户内相关订阅者推送：所有顾问端 + 指定客户端连接
// 顾问端可见本租户全部客户消息（RQ 隔离语义一致）；客户端仅收到自身 customerID 的推送
func (h *Hub) Publish(ev RealtimeEvent, payload []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for cl := range h.clients {
		if cl.TenantID != ev.TenantID {
			continue
		}
		hit := cl.UserID != 0 // 顾问端全收
		if !hit && cl.CustomerID == ev.CustomerID && ev.CustomerID != 0 {
			hit = true // 该客户端连接
		}
		if hit {
			select {
			case cl.send <- payload:
			default: // 发送缓冲满则丢弃（轮询兜底保证最终到达）
			}
		}
	}
}

// ============================================================
// P1-1 WebSocket推送增强（2026-08-30）
//
// 目标：推送消息内容（而非仅信号），前端收到即更新本地状态
// 新增：
//   - PublishWithContent：推送消息内容（含消息体）
//   - PublishTyping：推送"正在输入"状态
//   - PublishStatus：推送状态变更
// ============================================================

// RealtimeMessage 推送的消息内容（P1-1）
type RealtimeMessage struct {
	Type           string `json:"type"`            // new_message / typing / status / message_content
	CustomerID     uint   `json:"customer_id"`     // 关联客户
	ConversationID uint   `json:"conversation_id"` // 关联会话
	SenderType     string `json:"sender_type"`     // customer / ai / human
	TenantID       uint   `json:"tenant_id"`       // 租户ID
	// 消息内容字段（P1-1 新增）
	MessageID  uint   `json:"message_id,omitempty"`  // 消息ID
	Content    string `json:"content,omitempty"`     // 消息内容
	SenderName string `json:"sender_name,omitempty"` // 发送方名称
	CreatedAt  string `json:"created_at,omitempty"`  // 创建时间
}

// PublishWithContent 向租户内相关订阅者推送消息内容（P1-1）
// 与 Publish 类似，但 payload 包含完整消息体，前端收到即更新本地状态
func (h *Hub) PublishWithContent(ev RealtimeMessage) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for cl := range h.clients {
		if cl.TenantID != ev.TenantID {
			continue
		}
		hit := cl.UserID != 0 // 顾问端全收
		if !hit && cl.CustomerID == ev.CustomerID && ev.CustomerID != 0 {
			hit = true // 该客户端连接
		}
		if hit {
			// 序列化消息并推送
			data, err := jsonMarshal(ev)
			if err != nil {
				continue
			}
			select {
			case cl.send <- data:
			default: // 发送缓冲满则丢弃（轮询兜底保证最终到达）
			}
		}
	}
}

// PublishTyping 向租户内相关订阅者推送"正在输入"状态（P1-1）
func (h *Hub) PublishTyping(tenantID, customerID, conversationID uint, isTyping bool) {
	ev := RealtimeMessage{
		Type:           "typing",
		CustomerID:     customerID,
		ConversationID: conversationID,
		TenantID:       tenantID,
	}
	if !isTyping {
		ev.Type = "typing_stop"
	}
	h.PublishWithContent(ev)
}

// PublishStatus 向租户内相关订阅者推送状态变更（P1-1）
func (h *Hub) PublishStatus(tenantID, customerID, conversationID uint, status string) {
	ev := RealtimeMessage{
		Type:           "status",
		CustomerID:     customerID,
		ConversationID: conversationID,
		TenantID:       tenantID,
		SenderType:     status, // 复用 SenderType 字段传递状态
	}
	h.PublishWithContent(ev)
}

// jsonMarshal JSON序列化（简化错误处理）
func jsonMarshal(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}
