// Package channel FIX-5(2026-09-27) 通道入站落库行为回归：
// 客户那句话写不进库时，本轮必须判失败（台账 failed + 错误上抛），而不是"ack success 但消息不存在"。
//
// 与主链 chat_main.go 的 customer_inbound 是同一个缺陷的分身：旧写法 `db.DB.Create(&inMsg)`
// 把 GORM 的 error 就地丢掉，回调照样回 success、worker 照样带着 ID=0 往下跑，
// 于是这句话在网页、顾问端、E8 会话存档三处永远查不到，事后没人能解释"他到底说过什么"。
//
// 注错方式：GORM Create 前置回调 + 按**消息正文前缀**判定（不加全局开关），
// 因此本文件的钩子在同包并发用例下也只可能打掉自己那两条消息——
// 反证用例（对照腿）用不同正文走同一条链路，证明失败可归因、不是链路本来就断。
package channel

import (
	"errors"
	"strings"
	"testing"
	"time"

	"ai-scrm/config"
	"ai-scrm/internal/db"
	"ai-scrm/internal/metrics"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/testutil"

	"gorm.io/gorm"
)

const (
	fix5ChanHook     = "test:fix5_channel_fail_message_create"
	fix5InjectedText = "FIX5通道注错入站句"
	fix5ControlText  = "FIX5通道对照入站句"
)

var errFix5Channel = errors.New("fix5 channel injected fault")

// installFix5ChannelFault 挂上"按正文前缀打掉消息写入"的回调，用例结束摘除。
func installFix5ChannelFault(t *testing.T) {
	t.Helper()
	if db.DB == nil {
		t.Fatalf("db.DB 未初始化（SetupTestDB 未跑？）")
	}
	if err := db.DB.Callback().Create().Before("gorm:create").Register(fix5ChanHook, func(tx *gorm.DB) {
		m, ok := tx.Statement.Dest.(*model.Message)
		if !ok || !strings.HasPrefix(m.Content, fix5InjectedText) {
			return
		}
		// 真中断：GORM 内建的 gorm:create 开头就是 `if db.Error != nil { return }`，
		// 所以 AddError 之后那一行不会写出去（本用例末尾查库自证 rows=0）。
		tx.AddError(errFix5Channel)
	}); err != nil {
		t.Fatalf("注册注错回调失败: %v", err)
	}
	t.Cleanup(func() { _ = db.DB.Callback().Create().Remove(fix5ChanHook) })
}

// TestProcessInboundCustomerMessagePersistFailureFailsRound 通道入站的客户消息行是这轮对话唯一的存在证明：
// 注错腿（GORM 回调强制 Create 报错）必须让 ProcessInbound 返回 error，使同步段台账判 failed、
// 微信按 attempts<5 重推时会重新认领；正常腿同时在场，防"任何错误都返回 nil"这种反向假绿。
// 旧写法返回 nil 且回调 ack success，于是那句话在网页、顾问端、会话存档三处同时不存在而没人报错。
func TestProcessInboundCustomerMessagePersistFailureFailsRound(t *testing.T) {
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	installFix5ChannelFault(t)
	tid := testutil.CreateTenant(t)

	oldCfg := config.GlobalConfig
	config.GlobalConfig = &config.Config{
		AI:         config.AIConfig{MockMode: true},
		ReplySpeed: config.ReplySpeedConfig{MaxMergeMessages: 5, MergeWindowSeconds: 1},
	}
	oldRC := runtimecfg.DefaultSystemConfigService
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(map[string]string{
		"merge_window_seconds": "1",
		"reply_delay_mode":     "instant",
		"mock_mode":            "true",
	}, nil)
	// 复原**前**必须先把后台排空（FIX-5/-race 收口批）：对照腿会派生 runInboundWorker，
	// 它要读这两个全局量；旧写法直接换回原值，于是"本用例的后台"与"下一条用例的
	// SetupTestDB 重新 LoadConfig"并发，-race 报出来像测试互踩，其实是没人等过它。
	// 两处复原合并在同一个 defer 里，保证顺序是「排空 → 复原」，且 t.Fatalf 走 Goexit 时同样生效。
	defer func() {
		drainInboundWorkers(t)
		config.GlobalConfig = oldCfg
		runtimecfg.DefaultSystemConfigService = oldRC
	}()

	ch := model.Channel{TenantID: tid, Type: "wecom_app", Name: "unit_fix5", Status: "active"}
	if err := db.DB.Create(&ch).Error; err != nil {
		t.Fatalf("建通道失败: %v", err)
	}
	stamp := time.Now().Format("150405.000")
	extBad := "wm_fix5_bad_" + stamp
	extOK := "wm_fix5_ok_" + stamp
	// 清理走 testutil 的统一口径：它按 tenant_id 动态发现含该列的表逐轮删除，
	// 覆盖 channels / channel_inbound_msgs / messages 等全部落点（本包手写清单必然漏表）。
	t.Cleanup(func() { testutil.CleanupTenant(t, tid) })

	// ① 注错腿：写不进库 → ProcessInbound 必须返回错误（同步段台账据此判 failed）
	errBad := ProcessInbound(&ch, &InboundMessage{
		ExternalID: extBad, Content: fix5InjectedText + " " + stamp, MsgID: "fix5bad_" + stamp,
	})
	if errBad == nil {
		t.Fatalf("入站消息落库失败必须让本轮判失败（旧写法返回 nil 并 ack success，消息静默蒸发）")
	}
	// 反证：那一行确实没写进去（不是"写完了才报错"这种半态）
	var badRows int64
	if err := db.DB.Model(&model.Message{}).Where("tenant_id = ? AND content LIKE ?", tid, fix5InjectedText+"%").
		Count(&badRows).Error; err != nil {
		t.Fatalf("统计注错腿消息行失败: %v", err)
	}
	if badRows != 0 {
		t.Fatalf("注错后仍写出 %d 条消息行", badRows)
	}
	// 台账必须是 failed：这是"重推会重新认领"的前提，也是唯一的线上可见性
	var led model.ChannelInboundMsg
	if err := db.DB.Where("channel_id = ? AND msg_id = ?", ch.ID, "fix5bad_"+stamp).First(&led).Error; err != nil {
		t.Fatalf("入站台账行缺失（幂等抢占本身没跑）: %v", err)
	}
	if led.Status != "failed" {
		t.Fatalf("落库失败时台账应为 failed（让重推重新认领），实得 %q", led.Status)
	}
	// 观测位：与主链同一计数器、同一维度口径
	if !strings.Contains(metrics.RenderPrometheus(), `ai_scrm_chat_persist_error_total{kind="channel_inbound"}`) {
		t.Fatalf("通道入站落库失败未进计数器观测位")
	}

	// ② 对照腿：同一条链路、不同正文 → 必须成功且恰好落 1 条客户消息
	//    （缺这条，① 的失败可能来自"链路本来就断"，断言等于空转）
	if err := ProcessInbound(&ch, &InboundMessage{
		ExternalID: extOK, Content: fix5ControlText + " " + stamp, MsgID: "fix5ok_" + stamp,
	}); err != nil {
		t.Fatalf("对照腿 ProcessInbound 应成功，实得: %v", err)
	}
	var okRows int64
	if err := db.DB.Model(&model.Message{}).
		Where("tenant_id = ? AND sender_type = 'customer' AND content LIKE ?", tid, fix5ControlText+"%").
		Count(&okRows).Error; err != nil {
		t.Fatalf("统计对照腿消息行失败: %v", err)
	}
	if okRows != 1 {
		t.Fatalf("对照腿应恰好落 1 条客户消息，实得 %d（链路不通则①的失败无从归属）", okRows)
	}
}
