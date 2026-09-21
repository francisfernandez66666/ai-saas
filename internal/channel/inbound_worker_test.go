// D5(2026-09-16) 通道 headless worker 回归锁定：入站并入合并队列后，
// 简单消息路径必须"落库+出站各一次"（与 web 同款语义），ProcessInbound 必须即刻返回（异步化，
// 不再阻塞回调线程做生成——微信 5s ack 压力解除）。
package channel

import (
	"testing"
	"time"

	"ai-scrm/config"
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/testutil"
)

// TestProcessInboundSimpleAsyncDelivery 验证渠道入站简单消息的异步投递链路（worker 消费后回复正确落地）。
func TestProcessInboundSimpleAsyncDelivery(t *testing.T) {
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	tid := testutil.CreateTenant(t)

	oldCfg := config.GlobalConfig
	config.GlobalConfig = &config.Config{
		AI:         config.AIConfig{MockMode: true},
		ReplySpeed: config.ReplySpeedConfig{MaxMergeMessages: 5, MergeWindowSeconds: 1},
	}
	defer func() { config.GlobalConfig = oldCfg }()
	oldRC := runtimecfg.DefaultSystemConfigService
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(map[string]string{
		"merge_window_seconds": "1",
		"reply_delay_mode":     "instant",
		"mock_mode":            "true",
	}, nil)
	defer func() { runtimecfg.DefaultSystemConfigService = oldRC }()

	ch := model.Channel{TenantID: tid, Type: "wecom_app", Name: "unit_d5", Status: "active"}
	if err := db.DB.Create(&ch).Error; err != nil {
		t.Fatalf("建通道失败: %v", err)
	}
	external := "wm_unit_d5_" + time.Now().Format("150405.000")
	t.Cleanup(func() {
		var custs []model.Customer
		db.DB.Where("tenant_id = ?", tid).Find(&custs)
		for _, c := range custs {
			db.DB.Where("customer_id = ?", c.ID).Delete(&model.Message{})
			db.DB.Where("customer_id = ?", c.ID).Delete(&model.Conversation{})
			db.DB.Where("customer_id = ?", c.ID).Delete(&model.CustomerIdentity{})
			db.DB.Delete(&model.Customer{}, c.ID)
		}
		db.DB.Where("channel_id = ?", ch.ID).Delete(&model.ChannelOutbound{})
		db.DB.Delete(&model.Channel{}, ch.ID)
	})

	// ProcessInbound 必须快速返回（异步化断言：同步段仅做整备，不含 AI/队列等待）
	start := time.Now()
	if err := ProcessInbound(&ch, &InboundMessage{ExternalID: external, Content: "在吗", MsgID: "d5msg_" + external}); err != nil {
		t.Fatalf("ProcessInbound 失败: %v", err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("ProcessInbound 应异步快速返回，实际耗时 %v", el)
	}

	// worker 等简单消息串行锁 + 出站行落库：给足轮询窗口
	var aiMsgs int64
	var outs int64
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		db.DB.Model(&model.Message{}).Where("tenant_id = ? AND sender_type = 'ai' AND route_result = ?", tid, "channel_simple").Count(&aiMsgs)
		db.DB.Model(&model.ChannelOutbound{}).Where("tenant_id = ? AND channel_id = ?", tid, ch.ID).Count(&outs)
		if aiMsgs == 1 && outs == 1 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if aiMsgs != 1 {
		t.Fatalf("简单消息应恰好落 1 条 AI 回复(channel_simple)，实际 %d", aiMsgs)
	}
	if outs != 1 {
		t.Fatalf("简单消息应恰好投 1 条出站，实际 %d", outs)
	}
	// 客户消息也须已落库（同步段落库，先于入队）
	var custMsgs int64
	db.DB.Model(&model.Message{}).Where("tenant_id = ? AND sender_type = 'customer'", tid).Count(&custMsgs)
	if custMsgs != 1 {
		t.Fatalf("客户入站消息应落库 1 条，实际 %d", custMsgs)
	}
}
