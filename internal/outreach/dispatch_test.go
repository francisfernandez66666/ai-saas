// 触达派发层连库单测：裁决链（开关/客户/通道/窗口）、入队回写字段、瞬时故障重试到上限、
// 未到点不扫、以及 queued→sent/failed 的出站回执对账（含"投递记录缺失"两条分支）。
//
// SendHook 全部注入替身：派发层不该在单测里真起通道，但必须验"传进去的参数对不对"——
// 尤其 conversation_id 必须为 0（触达是客户级开口，塞 0 进消息表就复刻批六刚清掉的孤儿行问题）。
package outreach

import (
	"errors"
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/testutil"
)

// hookCall 记录一次 SendHook 调用（断言用）
type hookCall struct {
	tenantID, channelID, customerID, conversationID uint
	content, msgType                                string
}

// withHook 注入 SendHook 替身并在用例结束复原（用例之间互不污染全局钩子）
func withHook(t *testing.T, fn func(uint, uint, uint, uint, string, string) (uint, error)) {
	t.Helper()
	prev := SendHook
	SendHook = fn
	t.Cleanup(func() { SendHook = prev })
}

// newActiveChannel 建一条指定类型的活跃通道，返回通道ID
func newActiveChannel(t *testing.T, tid uint, chanType, name string) uint {
	t.Helper()
	ch := model.Channel{TenantID: tid, Name: name, Type: chanType, Status: model.ChannelStatusActive}
	if err := db.DB.Create(&ch).Error; err != nil {
		t.Fatalf("建通道失败: %v", err)
	}
	return ch.ID
}

// bindIdentity 把客户绑到通道身份上（可达性映射的真实来源）
func bindIdentity(t *testing.T, tid, channelID, customerID uint, ext string) {
	t.Helper()
	if err := db.DB.Create(&model.ChannelIdentity{
		TenantID: tid, ChannelID: channelID, CustomerID: customerID, ExternalID: ext,
	}).Error; err != nil {
		t.Fatalf("建通道身份失败: %v", err)
	}
}

// seedCustomerMessage 插一条客户入站消息（48h 窗口判定的唯一依据），created_at 可指定
func seedCustomerMessage(t *testing.T, tid, customerID uint, at time.Time) {
	t.Helper()
	conv := model.Conversation{TenantID: tid, CustomerID: customerID, Status: "active", Channel: "wechat"}
	if err := db.DB.Create(&conv).Error; err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	msg := model.Message{
		TenantID: tid, ConversationID: conv.ID, CustomerID: customerID,
		SenderType: "customer", Content: "在吗", CreatedAt: at,
	}
	if err := db.DB.Create(&msg).Error; err != nil {
		t.Fatalf("建客户消息失败: %v", err)
	}
}

// mustCreateTask 排期一条任务（走真实 Create 校验，验的是派发侧）
func mustCreateTask(t *testing.T, tid, customerID uint, content string, at time.Time) *model.OutreachTask {
	t.Helper()
	task, err := Create(db.DB, CreateInput{TenantID: tid, CustomerID: customerID, Content: content, ScheduledAt: at, Now: at})
	if err != nil {
		t.Fatalf("排期任务失败: %v", err)
	}
	return task
}

// loadTask 回读任务当前落库形态
func loadTask(t *testing.T, id uint) model.OutreachTask {
	t.Helper()
	var task model.OutreachTask
	if err := db.DB.Where("id = ?", id).First(&task).Error; err != nil {
		t.Fatalf("回读任务失败: %v", err)
	}
	return task
}

// TestDispatchNoopWithoutHook 钩子未注入时整轮不动：pending 保持 pending。
// 这条锁的是"宁可不发，也不把任务改成无法解释的中间态"（启动装配漏一行时的行为）。
func TestDispatchNoopWithoutHook(t *testing.T) {
	testutil.SetupTestDB(t)
	runtimecfg.InitSystemConfigService()
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	enableOutreach(t, tid)
	cid := newCustomer(t, tid, "触达派发-无钩子")
	task := mustCreateTask(t, tid, cid, "哥，周末有活动", time.Now())

	withHook(t, nil) // 显式置空：模拟 main.go 漏一行 SendHook 装配
	res, err := DispatchDue(db.DB, time.Now(), 50)
	if err != nil {
		t.Fatalf("无钩子轮次不应报错: %v", err)
	}
	if res.Scanned != 0 || res.Queued != 0 || res.Skipped != 0 {
		t.Fatalf("未注入钩子时不应产生任何账目，得 %+v", res)
	}
	if got := loadTask(t, task.ID); got.Status != model.OutreachStatusPending || got.Attempts != 0 {
		t.Fatalf("任务应原封不动，得 status=%s attempts=%d", got.Status, got.Attempts)
	}
}

// TestDispatchQueuesAndWritesBack 可达 + 窗口内 → queued，并回读校验裁决留痕与入站参数。
func TestDispatchQueuesAndWritesBack(t *testing.T) {
	testutil.SetupTestDB(t)
	runtimecfg.InitSystemConfigService()
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	enableOutreach(t, tid)
	chID := newActiveChannel(t, tid, "wecom_app", "触达派发-企微应用")
	cid := newCustomer(t, tid, "触达派发-可达客户")
	bindIdentity(t, tid, chID, cid, "wm_dispatch_1")

	now := time.Now()
	task := mustCreateTask(t, tid, cid, "哥，上次看的车型到新车了", now)

	var calls []hookCall
	withHook(t, func(tenantID, channelID, customerID, conversationID uint, content, msgType string) (uint, error) {
		calls = append(calls, hookCall{tenantID, channelID, customerID, conversationID, content, msgType})
		return 987654, nil
	})
	res, err := DispatchDue(db.DB, now, 50)
	if err != nil {
		t.Fatalf("派发失败: %v", err)
	}
	if res.Scanned == 0 || res.Queued != 1 {
		t.Fatalf("应有 1 条入队，得 %+v", res)
	}
	if len(calls) != 1 {
		t.Fatalf("SendHook 应恰好调一次，得 %d 次", len(calls))
	}
	c := calls[0]
	if c.tenantID != tid || c.channelID != chID || c.customerID != cid {
		t.Fatalf("入站参数错位: %+v", c)
	}
	// 触达不带会话：conversation_id 必须 0（否则 channel_outbound 出现"像会话消息却无会话"的行）
	if c.conversationID != 0 {
		t.Fatalf("触达入站不得绑会话，得 conversation_id=%d", c.conversationID)
	}
	if c.msgType != "text" || c.content != "哥，上次看的车型到新车了" {
		t.Fatalf("正文/类型不符: %+v", c)
	}
	got := loadTask(t, task.ID)
	if got.Status != model.OutreachStatusQueued || got.ChannelID != chID || got.OutboundID != 987654 {
		t.Fatalf("回写错误: status=%s channel=%d outbound=%d", got.Status, got.ChannelID, got.OutboundID)
	}
	if got.Attempts != 1 {
		t.Fatalf("attempts 应记 1，得 %d", got.Attempts)
	}
}

// TestDispatchSkipsOutOfWindow 微信客服通道 + 最后入站 100h 前 → 48h 窗口外，skipped 且不调钩子。
func TestDispatchSkipsOutOfWindow(t *testing.T) {
	testutil.SetupTestDB(t)
	runtimecfg.InitSystemConfigService()
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	enableOutreach(t, tid)
	chID := newActiveChannel(t, tid, "wecom_kf", "触达派发-微信客服")
	cid := newCustomer(t, tid, "触达派发-窗口外")
	bindIdentity(t, tid, chID, cid, "wm_kf_1")
	seedCustomerMessage(t, tid, cid, time.Now().Add(-100*time.Hour))

	now := time.Now()
	task := mustCreateTask(t, tid, cid, "哥，好久没联系了", now)

	hooked := 0
	withHook(t, func(uint, uint, uint, uint, string, string) (uint, error) {
		hooked++
		return 1, nil
	})
	if _, err := DispatchDue(db.DB, now, 50); err != nil {
		t.Fatalf("派发失败: %v", err)
	}
	if hooked != 0 {
		t.Fatalf("窗口外不得调钩子，得 %d 次", hooked)
	}
	got := loadTask(t, task.ID)
	if got.Status != model.OutreachStatusSkipped || got.Reason != model.OutreachReasonOutOfWindow {
		t.Fatalf("应为 skipped/out_of_window，得 %s/%s", got.Status, got.Reason)
	}
	if got.OutboundID != 0 {
		t.Fatalf("拦下不应留下站关联，得 outbound=%d", got.OutboundID)
	}
}

// TestDispatchSkipsWhenDisabledAtSendTime 排期后关开关：派发侧必须重新裁决（skipped/disabled）。
// 只在前置校验拦一次的实现会让"已排期任务"在开关关掉后继续发出去。
func TestDispatchSkipsWhenDisabledAtSendTime(t *testing.T) {
	testutil.SetupTestDB(t)
	runtimecfg.InitSystemConfigService()
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	enableOutreach(t, tid)
	chID := newActiveChannel(t, tid, "wecom_app", "触达派发-后关开关")
	cid := newCustomer(t, tid, "触达派发-开关后关")
	bindIdentity(t, tid, chID, cid, "wm_off_1")

	now := time.Now()
	task := mustCreateTask(t, tid, cid, "哥，活动明天结束", now)
	setTenantCfg(t, tid, "outreach_enabled", "false", "bool")

	withHook(t, func(uint, uint, uint, uint, string, string) (uint, error) {
		t.Fatal("开关关掉后不应入站")
		return 0, nil
	})
	if _, err := DispatchDue(db.DB, time.Now(), 50); err != nil {
		t.Fatalf("派发失败: %v", err)
	}
	got := loadTask(t, task.ID)
	if got.Status != model.OutreachStatusSkipped || got.Reason != model.OutreachReasonDisabled {
		t.Fatalf("应为 skipped/disabled，得 %s/%s", got.Status, got.Reason)
	}
}

// TestDispatchTransientRetryThenFail 入站报错只加 attempts、状态不动；
// 到上限（第 5 次）才判 failed——一次 DB 抖动不得把整批计划打成死档。
func TestDispatchTransientRetryThenFail(t *testing.T) {
	testutil.SetupTestDB(t)
	runtimecfg.InitSystemConfigService()
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	enableOutreach(t, tid)
	chID := newActiveChannel(t, tid, "wecom_app", "触达派发-入站报错")
	cid := newCustomer(t, tid, "触达派发-重试")
	bindIdentity(t, tid, chID, cid, "wm_retry_1")

	now := time.Now()
	task := mustCreateTask(t, tid, cid, "哥，再确认下时间", now)
	withHook(t, func(uint, uint, uint, uint, string, string) (uint, error) {
		return 0, errors.New("mock: 出站写库失败")
	})

	res, err := DispatchDue(db.DB, now, 50)
	if err != nil {
		t.Fatalf("派发不应上抛瞬时错: %v", err)
	}
	if res.Retried != 1 || res.Failed != 0 {
		t.Fatalf("首轮应记为重试，得 %+v", res)
	}
	got := loadTask(t, task.ID)
	if got.Status != model.OutreachStatusPending || got.Attempts != 1 {
		t.Fatalf("瞬时故障后应留 pending/attempts=1，得 %s/%d", got.Status, got.Attempts)
	}

	// 直接推到上限前一轮，验"最后一跳"判 failed
	if err := db.DB.Model(&model.OutreachTask{}).Where("id = ?", task.ID).
		Update("attempts", maxDispatchAttempts-1).Error; err != nil {
		t.Fatalf("改 attempts 失败: %v", err)
	}
	res2, err := DispatchDue(db.DB, now, 50)
	if err != nil {
		t.Fatalf("第二轮派发失败: %v", err)
	}
	if res2.Failed != 1 || res2.Retried != 0 {
		t.Fatalf("到上限应判 failed，得 %+v", res2)
	}
	got = loadTask(t, task.ID)
	if got.Status != model.OutreachStatusFailed || got.Error == "" {
		t.Fatalf("failed 终态应带错误摘要，得 status=%s error=%q", got.Status, got.Error)
	}
}

// TestDispatchIgnoresFutureTasks 未到点的任务不得被扫到（scheduled_at 是排期承诺）。
func TestDispatchIgnoresFutureTasks(t *testing.T) {
	testutil.SetupTestDB(t)
	runtimecfg.InitSystemConfigService()
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	enableOutreach(t, tid)
	chID := newActiveChannel(t, tid, "wecom_app", "触达派发-未到点")
	cid := newCustomer(t, tid, "触达派发-未来")
	bindIdentity(t, tid, chID, cid, "wm_future_1")

	future := time.Now().Add(2 * time.Hour)
	task := mustCreateTask(t, tid, cid, "哥，明天下午两点", future)
	withHook(t, func(uint, uint, uint, uint, string, string) (uint, error) {
		t.Fatal("未到点不得发送")
		return 0, nil
	})
	res, err := DispatchDue(db.DB, time.Now(), 50)
	if err != nil {
		t.Fatalf("派发失败: %v", err)
	}
	if res.Scanned != 0 {
		t.Fatalf("未到点任务不该进扫描窗口，得 %+v", res)
	}
	if got := loadTask(t, task.ID); got.Status != model.OutreachStatusPending {
		t.Fatalf("任务应保持 pending，得 %s", got.Status)
	}
}

// TestSyncResultsFromOutboundReceipt 出站回执四态对账：
// sent→sent(+sent_at)、failed→failed(send_failed)、记录缺失但未过期→留在 queued、过期→failed。
func TestSyncResultsFromOutboundReceipt(t *testing.T) {
	testutil.SetupTestDB(t)
	runtimecfg.InitSystemConfigService()
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	enableOutreach(t, tid)
	chID := newActiveChannel(t, tid, "wecom_app", "触达派发-回执对账")

	now := time.Now()
	mk := func(name string, outboundID uint, updatedAt time.Time) uint {
		cid := newCustomer(t, tid, name)
		bindIdentity(t, tid, chID, cid, "wm_sync_"+name)
		task := mustCreateTask(t, tid, cid, "哥，到货了", now)
		// 直接摆到 queued 态（派发那一段由上面的用例负责），只验对账
		if err := db.DB.Model(&model.OutreachTask{}).Where("id = ?", task.ID).
			Updates(map[string]any{"status": model.OutreachStatusQueued, "channel_id": chID,
				"outbound_id": outboundID, "updated_at": updatedAt}).Error; err != nil {
			t.Fatalf("置 queued 失败: %v", err)
		}
		return task.ID
	}
	// 出站记录缺失分支要有 outbound_id 才进对账扫描（SyncResults 只捞 outbound_id>0 的 queued），
	// 指向一个不存在的大 ID 即模拟"投递行被清理"。
	const noSuchOutbound = uint(900000001)
	sentID := mk("触达回执-已送达", noSuchOutbound, now)
	failedID := mk("触达回执-发送失败", noSuchOutbound, now)
	freshID := mk("触达回执-记录缺失未过期", noSuchOutbound, now)
	staleID := mk("触达回执-记录缺失已过期", noSuchOutbound, now.Add(-25*time.Hour))

	mkOut := func(tenantID uint, status, errMsg string) uint {
		ob := model.ChannelOutbound{TenantID: tenantID, ChannelID: chID, Content: "哥，到货了",
			MsgType: "text", Status: status, Error: errMsg}
		if err := db.DB.Create(&ob).Error; err != nil {
			t.Fatalf("建出站记录失败: %v", err)
		}
		return ob.ID
	}
	sentOB := mkOut(tid, model.OutboundSent, "")
	failedOB := mkOut(tid, model.OutboundFailed, "errcode 45015 客服消息超出时间限制")
	// pending：仍在退避中，本轮不该动
	if err := db.DB.Model(&model.OutreachTask{}).Where("id = ?", sentID).
		Update("outbound_id", sentOB).Error; err != nil {
		t.Fatalf("回填 outbound_id 失败: %v", err)
	}
	if err := db.DB.Model(&model.OutreachTask{}).Where("id = ?", failedID).
		Update("outbound_id", failedOB).Error; err != nil {
		t.Fatalf("回填 outbound_id 失败: %v", err)
	}

	sent, failed, err := SyncResults(db.DB, time.Now(), 200)
	if err != nil {
		t.Fatalf("对账失败: %v", err)
	}
	// failed 有两条：出站回执判失败 + 出站记录缺失且过期；freshID 那条必须原样留在 queued
	if sent != 1 || failed != 2 {
		t.Fatalf("应 1 送达 2 失败，得 sent=%d failed=%d", sent, failed)
	}
	gotSent := loadTask(t, sentID)
	if gotSent.Status != model.OutreachStatusSent || gotSent.SentAt == nil {
		t.Fatalf("sent 任务应带 sent_at，得 %s/%v", gotSent.Status, gotSent.SentAt)
	}
	gotFailed := loadTask(t, failedID)
	if gotFailed.Status != model.OutreachStatusFailed || gotFailed.Reason != model.OutreachReasonSendFailed {
		t.Fatalf("failed 任务应带 send_failed，得 %s/%s", gotFailed.Status, gotFailed.Reason)
	}
	if gotFailed.Error == "" {
		t.Fatal("failed 任务应保留通道错误原文")
	}
	if got := loadTask(t, freshID); got.Status != model.OutreachStatusQueued {
		t.Fatalf("出站记录缺失但未过期应留在 queued 等下轮，得 %s", got.Status)
	}
	gotStale := loadTask(t, staleID)
	if gotStale.Status != model.OutreachStatusFailed || gotStale.Error == "" {
		t.Fatalf("过期且记录缺失应判 failed，得 %s/%q", gotStale.Status, gotStale.Error)
	}

	// sent 任务参与周内频控计数（只有真送达才吃额度）
	used, err := countSentInWindow(db.DB, tid, gotSent.CustomerID, time.Now().Add(-7*24*time.Hour))
	if err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if used != 1 {
		t.Fatalf("周内已触达应为 1，得 %d", used)
	}
}

// TestSyncResultsIgnoresOtherTenantOutbound 出站行必须同租户才认账：
// 跨租户 id 撞号（自增主键下不会，但历史迁移/手动插行可能）不得把别人的送达记到自己头上。
func TestSyncResultsIgnoresOtherTenantOutbound(t *testing.T) {
	testutil.SetupTestDB(t)
	runtimecfg.InitSystemConfigService()
	tidA := testutil.CreateTenantCode(t, "outreach_a")
	tidB := testutil.CreateTenantCode(t, "outreach_b")
	defer testutil.CleanupTenant(t, tidA)
	defer testutil.CleanupTenant(t, tidB)
	enableOutreach(t, tidA)

	now := time.Now()
	cid := newCustomer(t, tidA, "触达跨租户回执")
	task := mustCreateTask(t, tidA, cid, "哥，到货了", now)
	ob := model.ChannelOutbound{TenantID: tidB, ChannelID: 1, Content: "别人的消息",
		MsgType: "text", Status: model.OutboundSent}
	if err := db.DB.Create(&ob).Error; err != nil {
		t.Fatalf("建他租户出站记录失败: %v", err)
	}
	if err := db.DB.Model(&model.OutreachTask{}).Where("id = ?", task.ID).
		Updates(map[string]any{"status": model.OutreachStatusQueued, "outbound_id": ob.ID}).Error; err != nil {
		t.Fatalf("置 queued 失败: %v", err)
	}
	sent, failed, err := SyncResults(db.DB, now, 200)
	if err != nil {
		t.Fatalf("对账失败: %v", err)
	}
	if sent != 0 || failed != 0 {
		t.Fatalf("他租户出站行不得认账，得 sent=%d failed=%d", sent, failed)
	}
	if got := loadTask(t, task.ID); got.Status != model.OutreachStatusQueued {
		t.Fatalf("任务应仍留在 queued，得 %s", got.Status)
	}
}
