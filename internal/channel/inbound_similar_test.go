// 通道入站相似抑制自排除回归测试（D7）：锁定"首条消息不得与自身判相似"，防通道 AI 永不回复死链路回归。
package channel

import (
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// TestChannelSimilarMergedSelfExclusion 锁定 D7 修复(2026-09-14)：
// ProcessInbound 先落客户入站消息再判相似，channelSimilarMerged 必须排除"本条自身"，
// 否则与自身 100% 关键词重叠 → 每条首消息都被误抑制、AI 永不回复（通道全链路死）。
func TestChannelSimilarMergedSelfExclusion(t *testing.T) {
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	tid := testutil.CreateTenant(t)
	cust := model.Customer{TenantID: tid, Name: "单测相似抑制", Source: "unit", VisitorKey: "uk_chan_" + time.Now().Format("150405.000")}
	if err := db.DB.Create(&cust).Error; err != nil {
		t.Fatalf("建客户失败: %v", err)
	}
	conv := model.Conversation{TenantID: tid, CustomerID: cust.ID, Channel: "wecom_app", Status: "active"}
	if err := db.DB.Create(&conv).Error; err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	content := "你们这款车的续航和价格大概是多少"
	msg := model.Message{TenantID: tid, ConversationID: conv.ID, CustomerID: cust.ID, SenderType: "customer", Content: content, MessageType: "text"}
	if err := db.DB.Create(&msg).Error; err != nil {
		t.Fatalf("建消息失败: %v", err)
	}
	t.Cleanup(func() {
		db.DB.Where("conversation_id = ?", conv.ID).Delete(&model.Message{})
		db.DB.Delete(&model.Conversation{}, conv.ID)
		db.DB.Delete(&model.Customer{}, cust.ID)
	})

	// 1) 关键回归：以本条自身 ID 排除，窗口内再无其它同客户消息 → 绝不应抑制。
	if channelSimilarMerged(tid, conv.ID, cust.ID, msg.ID, content) {
		t.Fatal("首条消息与自身比对被误判为相似重复（excludeID 未生效）——通道 AI 永不回复")
	}
	// 2) 不排除自身时（excludeID=0），与刚落库的同内容消息应判相似 → 证明关键词重叠逻辑本身工作。
	if !channelSimilarMerged(tid, conv.ID, cust.ID, 0, content) {
		t.Fatal("未排除自身时应命中相似（对照组失效，可能 ExtractKeywords 或窗口查询异常）")
	}
	// 3) 真正不同的第二问，即使排除自身也不应被抑制。
	other := "门店周末营业时间是几点"
	om := model.Message{TenantID: tid, ConversationID: conv.ID, CustomerID: cust.ID, SenderType: "customer", Content: other, MessageType: "text"}
	if err := db.DB.Create(&om).Error; err != nil {
		t.Fatalf("建第二条消息失败: %v", err)
	}
	if channelSimilarMerged(tid, conv.ID, cust.ID, om.ID, other) {
		t.Fatal("内容明显不同的第二问被误抑制")
	}
}
