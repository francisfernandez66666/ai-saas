// Package realtime WebSocket 实时推送中枢（P1-2，2026-08-29）
//
// 设计：仅推送"该客户有新消息"通知信号，前端收到即触发已有拉取（保留轮询作兜底），
// 最小化对既有可用对话链路的侵入。多实例下本地 hub 命中即投，并通过 Redis 广播旁路
// 让其它实例补齐连接；Redis 未启用时自动退化为单机语义（轮询仍是最终兜底）。
package realtime

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"
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
	// B2 修复(2026-09-14)：顾问连接携带组织角色/部门路径，deliver 按数据范围过滤——
	// 旧实现"顾问端全收"，普通 sales 的 WS 能收到全租户客户消息正文，绕过 HTTP 侧 DataScope。
	Role     string // super_admin/tenant_admin/dept_admin/user/readonly；客户端为空
	DeptPath string // dept_admin 物化路径（子树判定）
	send     chan []byte
	lastSeen atomic.Int64 // 最近活动时间（UnixNano，P2-70 心跳：清扫僵尸连接）
}

// AdvisorScopeFunc 判定顾问连接对某客户的事件是否可见（B2 注入点）。
// 由 api 层注入（依赖 db/组织树），realtime 保持零业务依赖；
// 未注入时保持旧行为（全收），仅供单测与降级兜底——生产由 api init 强制注入。
type AdvisorScopeFunc func(role, deptPath string, userID, tenantID, customerID uint) bool

var advisorScope atomic.Value // 存 AdvisorScopeFunc

// SetAdvisorScope 注入数据范围判定函数（api 包 init 调用）
func SetAdvisorScope(fn AdvisorScopeFunc) { advisorScope.Store(fn) }

// getAdvisorScope 读取注入函数（未注入返回 nil）
func getAdvisorScope() AdvisorScopeFunc {
	if v := advisorScope.Load(); v != nil {
		fn, _ := v.(AdvisorScopeFunc)
		return fn
	}
	return nil
}

// Touch 标记连接活跃（读泵每收到一次消息调用；P2-70）
func (cl *Client) Touch() {
	cl.lastSeen.Store(time.Now().UnixNano())
}

// IdleFor 返回连接空闲时长（P2-70）
func (cl *Client) IdleFor() time.Duration {
	last := cl.lastSeen.Load()
	if last == 0 {
		return 0
	}
	return time.Since(time.Unix(0, last))
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

// NewAdvisorClient 构造顾问端连接并携带组织上下文（B2：deliver 按 Role/DeptPath 过滤）
func NewAdvisorClient(tenantID, userID uint, role, deptPath string) *Client {
	cl := NewClient(tenantID, userID, 0)
	cl.Role = role
	cl.DeptPath = deptPath
	return cl
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
	cl.Touch() // P2-70：登记即记为活跃
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

// SweepStale 清理僵尸连接（空闲超过 idleFor 的订阅者，P2-70 心跳）
// 僵尸连接通常来自客户端异常断线（网络抖动/进程被杀）未被读泵捕获，
// 长期驻留占着 send 通道与 map 槽位。
func (h *Hub) SweepStale(idleFor time.Duration) {
	var stale []*Client
	h.mu.RLock()
	for cl := range h.clients {
		if cl.IdleFor() > idleFor {
			stale = append(stale, cl)
		}
	}
	h.mu.RUnlock()
	for _, cl := range stale {
		h.Unregister(cl)
	}
}

// StartSweeper 启动定期僵尸清扫（main 启动时调用；30s 一轮，180s 无活动视为僵尸）
func (h *Hub) StartSweeper() {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			h.SweepStale(180 * time.Second)
		}
	}()
}

// deliver 按租户/客户路由把已序列化 payload 投递给本实例连接。
// B2 修复(2026-09-14)：顾问端不再"全收"——按注入的数据范围函数过滤
// （sales 仅本人名下客户；dept_admin 仅部门子树；tenant_admin/super 全租户）。
// 未注入判定函数时退回全收（单实例兜底/测试），生产由 api 包 init 注入。
func (h *Hub) deliver(tenantID, customerID uint, data []byte) {
	scope := getAdvisorScope()
	h.mu.RLock()
	defer h.mu.RUnlock()
	for cl := range h.clients {
		if cl.TenantID != tenantID {
			continue
		}
		hit := false
		if cl.UserID != 0 { // 顾问端
			hit = true
			if scope != nil {
				hit = scope(cl.Role, cl.DeptPath, cl.UserID, cl.TenantID, customerID)
			}
		}
		if !hit && cl.CustomerID == customerID && customerID != 0 {
			hit = true // 该客户端连接
		}
		if hit {
			select {
			case cl.send <- data:
			default: // 发送缓冲满则丢弃（轮询兜底保证最终到达）
			}
		}
	}
}

// Publish 向租户内相关订阅者推送：所有顾问端 + 指定客户端连接
// 顾问端可见本租户全部客户消息（RQ 隔离语义一致）；客户端仅收到自身 customerID 的推送
func (h *Hub) Publish(ev RealtimeEvent, payload []byte) {
	h.deliver(ev.TenantID, ev.CustomerID, payload)
	h.broadcast(ev.TenantID, ev.CustomerID, payload)
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
	data, err := jsonMarshal(ev)
	if err != nil {
		return
	}
	h.deliver(ev.TenantID, ev.CustomerID, data)
	h.broadcast(ev.TenantID, ev.CustomerID, data)
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
