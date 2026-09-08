// WebSocket Hub 路由单测（2026-09-08 本轮新增）
//
// 覆盖对象：internal/realtime/hub.go 的订阅路由语义——
//   1. 顾问端连接（UserID>0）接收本租户全部客户事件；
//   2. 客户端连接（CustomerID>0）只接收自身 customerID 的事件；
//   3. 跨租户事件不串扰（TenantID 隔离）；
//   4. PublishWithContent（消息内容推送）与 Publish（信号推送）行为一致。
// 背景：Chat/ChatTest 人工接管、客消息、AI 回复均通过 notifyWSWithContent 推送，
//       本测试守卫"推送可达性"，防止轮询兜底被误设为唯一通道后实时性回退。
package realtime

import (
	"encoding/json"
	"testing"
)

// drainRecv 从订阅者发送通道读取一条消息（非阻塞轮询，避免 HT 测试死锁）
func drainRecv(t *testing.T, ch <-chan []byte) []byte {
	t.Helper()
	select {
	case b := <-ch:
		return b
	default:
		return nil
	}
}

// decodeEv 反序列化 RealtimeEvent / RealtimeMessage（两结构共享 type/customer_id/tenant_id 字段）
func decodeEv(t *testing.T, b []byte) map[string]any {
	t.Helper()
	if b == nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("payload 非法 JSON: %v", err)
	}
	return m
}

func TestHubPublishRouteToAdvisorAndClient(t *testing.T) {
	h := NewHub()
	// 顾问端（本租户全收）：客户ID=0、UserID>0
	advisor := NewClient(1, 11, 0)
	// 客户端：租户1 客户5 与 租户1 客户6
	client5 := NewClient(1, 0, 5)
	client6 := NewClient(1, 0, 6)
	// 跨租户连接（不应收到）
	otherTenant := NewClient(2, 99, 0)

	h.Register(advisor)
	h.Register(client5)
	h.Register(client6)
	h.Register(otherTenant)
	defer func() {
		h.Unregister(advisor)
		h.Unregister(client5)
		h.Unregister(client6)
		h.Unregister(otherTenant)
	}()

	ev := RealtimeEvent{
		Type:           "new_message",
		CustomerID:     5,
		ConversationID: 100,
		SenderType:     "customer",
		TenantID:       1,
	}
	payload, _ := json.Marshal(ev)
	h.Publish(ev, payload)

	// 顾问端与本租户客户5 收到；客户6 与跨租户顾问不收到
	advisorPayload := drainRecv(t, advisor.SendQueue())
	if advisorPayload == nil {
		t.Error("顾问端应收到本租户客户事件")
	}
	if got := drainRecv(t, client5.SendQueue()); got == nil {
		t.Error("目标客户(5)应收到自身事件")
	}
	if got := drainRecv(t, client6.SendQueue()); got != nil {
		t.Error("非目标客户(6)不应收到")
	}
	if got := drainRecv(t, otherTenant.SendQueue()); got != nil {
		t.Error("跨租户顾问(tenant=2)不应收到")
	}
	// 校验推送给顾问的 payload 字段一致性（一条事件只消费一次）
	m := decodeEv(t, advisorPayload)
	if m == nil {
		t.Fatal("顾问端队列无到达 payload")
	}
	if m["type"] != "new_message" || int(m["customer_id"].(float64)) != 5 || int(m["tenant_id"].(float64)) != 1 {
		t.Errorf("事件字段不符合预期: %v", m)
	}
}

func TestHubPublishWithContentCarriesBody(t *testing.T) {
	h := NewHub()
	cl := NewClient(1, 0, 5) // 客户端 5
	h.Register(cl)
	defer h.Unregister(cl)

	ev := RealtimeMessage{
		Type:           "message_content",
		CustomerID:     5,
		ConversationID: 100,
		SenderType:     "ai",
		TenantID:       1,
		MessageID:      999,
		Content:        "您好，我来帮您查一下",
		SenderName:     "AI顾问",
	}
	h.PublishWithContent(ev)

	m := decodeEv(t, drainRecv(t, cl.SendQueue()))
	if m == nil {
		t.Fatal("客户端队列无内容推送")
	}
	if m["type"] != "message_content" {
		t.Errorf("type 字段错误: %v", m["type"])
	}
	if int(m["message_id"].(float64)) != 999 || m["content"] != ev.Content {
		t.Errorf("消息体未透传: %v", m)
	}
}

func TestHubSendBufferFullNoBlock(t *testing.T) {
	// 发送缓冲写满时 Publish 应静默丢弃（不阻塞调用方，由轮询兜底），防止推送路径卡死主流程
	h := NewHub()
	cl := NewClient(1, 0, 5)
	h.Register(cl)
	defer h.Unregister(cl)

	// 灌满容量为 16 的通道
	for i := 0; i < 32; i++ {
		h.Publish(RealtimeEvent{Type: "new_message", CustomerID: 5, TenantID: 1},
			[]byte(`{"type":"new_message"}`))
	}
	// 未阻塞即通过（此处仅验证可继续运行并被后续 drain 消费）
	if got := drainRecv(t, cl.SendQueue()); got == nil {
		t.Error("满缓冲下仍应有可消费消息（论证非阻塞）")
	}
}

func TestHubUnregisterDeliversNoMore(t *testing.T) {
	h := NewHub()
	cl := NewClient(1, 0, 5)
	h.Register(cl)
	h.Unregister(cl)
	// 注销后推送不应 panic 且无消息到达
	h.Publish(RealtimeEvent{Type: "new_message", CustomerID: 5, TenantID: 1},
		[]byte(`{"type":"new_message"}`))
	if got := drainRecv(t, cl.SendQueue()); got != nil {
		t.Error("注销后不应再收到推送")
	}
}