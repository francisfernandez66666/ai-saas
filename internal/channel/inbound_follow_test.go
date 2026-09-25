// G-19(2026-09-24) 通道关注事件 → CDP 生产端回归锁定。
//
// 为什么单独测这一段：`follow` 在摄入端（internal/cdp）一直有 case，自建立起**没有生产者**——
// 这种"消费端支持、生产端空白"的链路最会骗人：翻代码看到 case "follow" 就以为通了，
// 界面上 beh_followed 标签却恒空。故本测把三条口径钉住：
//  1. 只有白名单事件键才发（否则企微一堆 change_contact 回执会把事件流灌水）；
//  2. 只有**已建档**的身份映射才发（一条系统回执不该顺带新建客户——虚增客户数与席位配额，
//     且 staff 类事件的 external_userid 根本不是客户）；
//  3. 发出去的信封带对租户、客户 oneID 与归因属性（消费端只按 these 算标签）。
//
// 断法用注入的假消息中心（mq.DefaultCenter 可直接替换），不依赖 Kafka/Redis 起没起。
package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/mq"
	"ai-scrm/internal/testutil"
)

// captureCenter 记录每次 Publish 的假消息中心（其余方法空实现，只为满足接口）
type captureCenter struct{ calls []mqCall }

// mqCall 一次发布的可断言快照（payload 已反回 mq.UserEvent，避免断 JSON 字符串形态）
type mqCall struct {
	Topic    string
	TenantID uint
	OneID    string
	Event    mq.UserEvent
}

// Publish 记下这次发布的入参快照（假中心不投递，只累积）
func (c *captureCenter) Publish(_ context.Context, topic string, tenantID uint, oneID string, _ string, payload interface{}) error {
	raw, _ := json.Marshal(payload)
	var ev mq.UserEvent
	_ = json.Unmarshal(raw, &ev) // 生产端传的就是 mq.UserEvent，解不动即为空快照，断言自会红
	c.calls = append(c.calls, mqCall{Topic: topic, TenantID: tenantID, OneID: oneID, Event: ev})
	return nil
}

// Subscribe 空实现：本测试只验生产端，不需要消费腿
func (c *captureCenter) Subscribe(string, mq.EventHandler) {}

// StartConsumers 空实现：不启任何后台 goroutine，避免用例间串扰
func (c *captureCenter) StartConsumers(context.Context) {}

// Close 空实现：假中心没有需要释放的连接
func (c *captureCenter) Close() error { return nil }

// useCaptureCenter 换掉全局消息中心并在用例结束时还原
func useCaptureCenter(t *testing.T) *captureCenter {
	t.Helper()
	old := mq.DefaultCenter
	cen := &captureCenter{}
	mq.DefaultCenter = cen
	t.Cleanup(func() { mq.DefaultCenter = old })
	return cen
}

func countCustomers(tid uint) int64 {
	var n int64
	db.DB.Model(&model.Customer{}).Where("tenant_id = ?", tid).Count(&n)
	return n
}

// TestPublishFollowEventGating 三条拒绝路径 + 一条放行路径（拒绝必须**只拒不发**）。
func TestPublishFollowEventGating(t *testing.T) {
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	tid := testutil.CreateTenant(t)
	cen := useCaptureCenter(t)

	stamp := time.Now().Format("150405.000")
	ch := model.Channel{TenantID: tid, Type: "wecom_kf", Name: "unit_g19_" + stamp, Status: "active"}
	if err := db.DB.Create(&ch).Error; err != nil {
		t.Fatalf("建通道失败: %v", err)
	}
	cust := model.Customer{TenantID: tid, Name: "单测关注事件", Source: "unit", VisitorKey: "uk_g19_" + stamp}
	if err := db.DB.Create(&cust).Error; err != nil {
		t.Fatalf("建客户失败: %v", err)
	}
	known := "wm_g19_known_" + stamp // 已映射到客户的 external_userid
	ghost := "wm_g19_ghost_" + stamp // 未映射（别家/新员工/撤销回执）
	if err := db.DB.Create(&model.ChannelIdentity{
		TenantID: tid, ChannelID: ch.ID, CustomerID: cust.ID, ExternalID: known,
	}).Error; err != nil {
		t.Fatalf("建身份映射失败: %v", err)
	}
	t.Cleanup(func() {
		db.DB.Where("channel_id = ?", ch.ID).Delete(&model.ChannelIdentity{})
		db.DB.Delete(&model.Customer{}, cust.ID)
		db.DB.Delete(&model.Channel{}, ch.ID)
	})

	base := countCustomers(tid)
	cases := []struct {
		name    string
		in      InboundMessage
		wantPub int
	}{
		{
			name:    "白名单外事件键不发（change_external_contact 是删除/变更回执，不是关注）",
			in:      InboundMessage{IsEvent: true, EventKey: "change_external_contact", ExternalID: known},
			wantPub: 0,
		},
		{
			name:    "无身份映射不发（不该为一条回执顺手建客户）",
			in:      InboundMessage{IsEvent: true, EventKey: "add_external_contact", ExternalID: ghost},
			wantPub: 0,
		},
		{
			name:    "external_userid 缺失不发（staff 类事件根本没有客户身份）",
			in:      InboundMessage{IsEvent: true, EventKey: "add_external_contact", ExternalID: ""},
			wantPub: 0,
		},
		{
			name:    "已映射客户的关注事件正常发",
			in:      InboundMessage{IsEvent: true, EventKey: "add_external_contact", ExternalID: known},
			wantPub: 1,
		},
	}
	for _, tc := range cases {
		before := len(cen.calls)
		if err := ProcessInbound(&ch, &tc.in); err != nil {
			t.Fatalf("%s: ProcessInbound 失败: %v", tc.name, err)
		}
		got := len(cen.calls) - before
		if got != tc.wantPub {
			t.Fatalf("%s: 期望发布 %d 条，实际 %d 条", tc.name, tc.wantPub, got)
		}
		if n := countCustomers(tid); n != base {
			t.Fatalf("%s: 事件链路不得建客户（客户数 %d → %d）", tc.name, base, n)
		}
	}

	// 放行那一条的信封内容：租户、oneID、事件名与归因属性逐项钉住
	last := cen.calls[len(cen.calls)-1]
	if last.Topic != mq.TopicUserEvent {
		t.Fatalf("topic 应为 %s，实际 %s", mq.TopicUserEvent, last.Topic)
	}
	if last.TenantID != tid {
		t.Fatalf("tenant_id 应为 %d，实际 %d（跨租户即标签打别人家）", tid, last.TenantID)
	}
	if want := fmt.Sprintf("c:%d", cust.ID); last.OneID != want {
		t.Fatalf("oneID 应为 %s，实际 %s（消费端按它找画像）", want, last.OneID)
	}
	if last.Event.EventName != "follow" || last.Event.EventType != "identity" {
		t.Fatalf("事件名/类型应为 follow/identity，实际 %s/%s", last.Event.EventName, last.Event.EventType)
	}
	for key, want := range map[string]any{"customer_id": float64(cust.ID), "channel_id": float64(ch.ID), "event_key": "add_external_contact", "path": "channel_inbound_event"} {
		if got := last.Event.Attributes[key]; got != want {
			t.Fatalf("属性 %s 应为 %v，实际 %v（缺归因字段则标签算得出但查不到来源）", key, want, got)
		}
	}
}

// TestPublishFollowEventIdempotentOnReplay 微信回调超时重推是常态：同一事件重放两次，
// 生产端每次都发（幂等由消费端 inbox 按 event_id 挡），但**不得因为重放而多建客户**。
// 这条记在生产侧是因为：若有人把去重挪到 publishFollowEvent 前面并用 external_id 当锚，
// 第二条合法关注（客户删了再加）就会被误吞——这里把"生产端不去重"写成显式口径。
func TestPublishFollowEventIdempotentOnReplay(t *testing.T) {
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	tid := testutil.CreateTenant(t)
	cen := useCaptureCenter(t)

	stamp := time.Now().Format("150405.001")
	ch := model.Channel{TenantID: tid, Type: "wecom_kf", Name: "unit_g19r_" + stamp, Status: "active"}
	if err := db.DB.Create(&ch).Error; err != nil {
		t.Fatalf("建通道失败: %v", err)
	}
	cust := model.Customer{TenantID: tid, Name: "单测关注重放", Source: "unit", VisitorKey: "uk_g19r_" + stamp}
	if err := db.DB.Create(&cust).Error; err != nil {
		t.Fatalf("建客户失败: %v", err)
	}
	external := "wm_g19r_" + stamp
	if err := db.DB.Create(&model.ChannelIdentity{
		TenantID: tid, ChannelID: ch.ID, CustomerID: cust.ID, ExternalID: external,
	}).Error; err != nil {
		t.Fatalf("建身份映射失败: %v", err)
	}
	t.Cleanup(func() {
		db.DB.Where("channel_id = ?", ch.ID).Delete(&model.ChannelIdentity{})
		db.DB.Delete(&model.Customer{}, cust.ID)
		db.DB.Delete(&model.Channel{}, ch.ID)
	})

	in := InboundMessage{IsEvent: true, EventKey: "add_half_external_contact", ExternalID: external}
	for i := 0; i < 2; i++ {
		if err := ProcessInbound(&ch, &in); err != nil {
			t.Fatalf("第 %d 次 ProcessInbound 失败: %v", i+1, err)
		}
	}
	if len(cen.calls) != 2 {
		t.Fatalf("扫码添加事件应逐条发布（去重在消费端 inbox），实际 %d 条", len(cen.calls))
	}
	if n := countCustomers(tid); n != 1 {
		t.Fatalf("重放不得多建客户，期望 1 实际 %d", n)
	}
}
