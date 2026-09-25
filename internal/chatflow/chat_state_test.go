// G-8 会话状态维护真实单测（2026-09-24 欠账批一）
//
// 覆盖两条"改错就烧钱/烧线索"的链路：
//   - UpdateConversationState：七步公式的反馈回路。它的三条硬约束（首轮强制认知阶段 /
//     每轮最多推进 1 级 / Stage≥2 需接钩确认）是"平A开大"的根因之二，旧缺陷表现是
//     客户一句"你好"就把阶段推到 3~4，阶段锁天花板随之抬高，对比锚合法化 → 新客户被硬推单。
//     这条链路一旦有人把 targetStage 直接赋给 CurrentStage，单步函数测不出来。
//   - CheckHumanTimeout：D4 修复的"判定即落库"。只改内存时，调用方一异常，
//     下轮从 DB 重载又回到人工/待接管态，超时形同虚设，销售池挂着死线索。
//     故这里全部按**字段级 DB 回读**断言，不测日志串。
package chatflow

import (
	"testing"
	"time"

	"ai-scrm/config"
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/strategytypes"
	"ai-scrm/internal/testutil"
)

// stateTestEnv 把 UpdateConversationState/CheckHumanTimeout 读的两个全局配置装齐。
// 阈值全部显式给死：否则它们跟着 config 默认值漂移，断言会变成"测默认值"而不是"测规则"。
func stateTestEnv(kv map[string]string) func() {
	oldCfg := config.GlobalConfig
	oldRC := runtimecfg.DefaultSystemConfigService
	base := map[string]string{
		"theta_l3_intent":            "0.8",
		"stage_step_enabled":         "true",
		"stage_max_increment":        "1",
		"hook_rate_stage1_threshold": "0.3",
		"force_stage0_attempts":      "1",
		"human_timeout_seconds":      "1",
	}
	for k, v := range kv {
		base[k] = v
	}
	config.GlobalConfig = &config.Config{Strategy: config.StrategyConfig{ThetaL3Intent: 0.8, HumanTimeoutSeconds: 1}}
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(base, nil)
	return func() {
		config.GlobalConfig = oldCfg
		runtimecfg.DefaultSystemConfigService = oldRC
	}
}

// runState 跑一轮状态更新（strategyOutput 只给 IntentDelta，其余走零值）
func runState(conv *model.Conversation, customer *model.Customer, intentDelta float64, input string) model.SessionState {
	return UpdateConversationState(conv, &strategytypes.StrategyOutput{IntentDelta: intentDelta}, customer, input)
}

// TestUpdateStateFirstRoundForcesCognitionStage 约束1：首轮不论意向分多高都必须是认知阶段。
// 判据取"极高意向 0.95 也不给跳"——旧实现按 IntentScore 直算阶段，这一眼就红。
func TestUpdateStateFirstRoundForcesCognitionStage(t *testing.T) {
	defer stateTestEnv(nil)()
	conv := &model.Conversation{Attempts: 0, HookCount: 0, CurrentStage: 0}
	customer := &model.Customer{IntentScore: 0.95}

	st := runState(conv, customer, 0.0, "我想这周末就去看实车，能不能直接订")
	if st.Attempts != 1 {
		t.Errorf("首轮 Attempts 应为 1，实际 %d", st.Attempts)
	}
	if st.CurrentStage != 0 {
		t.Errorf("约束1 失效：首轮意向分 0.95 就把阶段推到 %d（应为 0）", st.CurrentStage)
	}
	// 首轮即使输入"像接钩"也不计入接钩（Attempts>1 才统计，防首轮自证成功）
	if st.HookCount != 0 {
		t.Errorf("首轮不应计接钩，实际 HookCount=%d", st.HookCount)
	}
	if st.SilentDuration != 0 {
		t.Errorf("有新消息进来，沉默时长必须清零，实际 %d", st.SilentDuration)
	}
}

// TestUpdateStateNoStageJumpAndHookGate 约束2+3：第二轮就算目标阶段是 4，
// 也只能推进 1 级；且接钩率不达标时不得进 Stage≥2。
func TestUpdateStateNoStageJumpAndHookGate(t *testing.T) {
	defer stateTestEnv(nil)()

	// 接钩率达标（1/1=1.0）→ 从 stage0 只准到 stage1（目标 stage4 被台阶钳住）
	conv := &model.Conversation{Attempts: 1, HookCount: 1, CurrentStage: 0}
	customer := &model.Customer{IntentScore: 0.95}
	st := runState(conv, customer, 0.0, "把配置表发我一份，我对比下续航")
	if st.CurrentStage > 1 {
		t.Errorf("约束2 失效：一轮从 stage0 跳到 stage%d（每轮最多 +1）", st.CurrentStage)
	}
	if st.Attempts != 2 || st.HookCount != 2 {
		t.Errorf("第二轮接钩应累加，实际 attempts=%d hook=%d", st.Attempts, st.HookCount)
	}

	// 接钩率不达标（0/2=0）→ 即使目标阶段≥2 也必须卡在 stage1
	conv2 := &model.Conversation{Attempts: 2, HookCount: 0, CurrentStage: 1}
	customer2 := &model.Customer{IntentScore: 0.95}
	st2 := runState(conv2, customer2, 0.0, "嗯嗯")
	if st2.HookRate >= 0.3 {
		t.Fatalf("前置条件破坏：这一例接钩率 %.2f 已达阈值，约束3 的断言会在别的路径上假绿", st2.HookRate)
	}
	if st2.CurrentStage != 1 {
		t.Errorf("约束3 失效：接钩率 %.2f 不够却被推进到 stage%d（应卡在 1）", st2.HookRate, st2.CurrentStage)
	}
}

// TestUpdateStateStageAllowedToAdvanceWhenHooked 反向腿：接钩率达标 + 阶段已到 1 时，
// 同一套参数必须**允许**推进到 stage2。缺这条，上一节的"卡在 1"可能只是永远卡住。
func TestUpdateStateStageAllowedToAdvanceWhenHooked(t *testing.T) {
	defer stateTestEnv(nil)()
	conv := &model.Conversation{Attempts: 2, HookCount: 2, CurrentStage: 1}
	customer := &model.Customer{IntentScore: 0.9}
	st := runState(conv, customer, 0.0, "那就约周六下午两点去店里试驾，我带上家人")
	if st.CurrentStage != 2 {
		t.Errorf("接钩率 %.2f、目标阶段够高时应推进到 stage2，实际 %d（若恒为 1 说明阶段推进整体失效）", st.HookRate, st.CurrentStage)
	}
}

// TestUpdateStateHighIntentRoundsThreshold 高意向轮数：≥θ_L3 累加，跌破即归零（L3 路由的驱动量）。
func TestUpdateStateHighIntentRoundsThreshold(t *testing.T) {
	defer stateTestEnv(nil)()

	// 0.7 + 0.2 = 0.9 ≥ 0.8 → 累加
	conv := &model.Conversation{Attempts: 1, HookCount: 1, CurrentStage: 1, HighIntentRounds: 2}
	st := runState(conv, &model.Customer{IntentScore: 0.7}, 0.2, "价格能给我个区间吗，合适就订")
	if st.HighIntentRounds != 3 {
		t.Errorf("过阈值应累加到 3，实际 %d", st.HighIntentRounds)
	}

	// 0.7 - 0.5 = 0.2 < 0.8 → 归零
	conv2 := &model.Conversation{Attempts: 1, HookCount: 1, CurrentStage: 1, HighIntentRounds: 3}
	st2 := runState(conv2, &model.Customer{IntentScore: 0.7}, -0.5, "我再想想吧，最近不太急")
	if st2.HighIntentRounds != 0 {
		t.Errorf("跌破阈值必须归零，实际 %d", st2.HighIntentRounds)
	}
}

// TestCheckHumanTimeoutHardBranchPersists 硬超时（人工模式无锁定）必须**落库**：
// mode=ai、is_human_locked=false、pending_handoff=false 三列字段级回读。
// 旧实现只改内存，调用方一异常下轮又回到人工态——这里断的是库里的值，不是返回值。
func TestCheckHumanTimeoutHardBranchPersists(t *testing.T) {
	testutil.SetupTestDB(t)
	defer stateTestEnv(nil)()
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	old := time.Now().Add(-time.Hour)
	conv := model.Conversation{
		TenantID: tid, CustomerID: 990701, Status: "active",
		Mode: "human", IsHumanLocked: false, LastHumanReplyAt: &old,
	}
	if err := db.DB.Create(&conv).Error; err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	defer db.DB.Unscoped().Delete(&model.Conversation{}, conv.ID)

	if !CheckHumanTimeout(&conv) {
		t.Fatal("人工超 1s 未回复应判超时")
	}
	var row model.Conversation
	if err := db.DB.First(&row, conv.ID).Error; err != nil {
		t.Fatalf("回读会话失败: %v", err)
	}
	if row.Mode != "ai" || row.IsHumanLocked {
		t.Errorf("硬超时未落库：mode=%q locked=%v（应为 ai/false）", row.Mode, row.IsHumanLocked)
	}
}

// TestCheckHumanTimeoutLockedNeverReopens 人工锁定态（顾问明确接管）不得被超时自动放行——
// 反向腿：不锁时同一份数据必须判超时（见上一例），两例合起来才证明"锁"这个变量真的在起作用。
func TestCheckHumanTimeoutLockedNeverReopens(t *testing.T) {
	testutil.SetupTestDB(t)
	defer stateTestEnv(nil)()
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	old := time.Now().Add(-time.Hour)
	conv := model.Conversation{
		TenantID: tid, CustomerID: 990702, Status: "active",
		Mode: "human", IsHumanLocked: true, LastHumanReplyAt: &old,
	}
	if err := db.DB.Create(&conv).Error; err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	defer db.DB.Unscoped().Delete(&model.Conversation{}, conv.ID)

	if CheckHumanTimeout(&conv) {
		t.Error("人工锁定态被超时自动接管（顾问明确接的单被 AI 抢回）")
	}
	var row model.Conversation
	if err := db.DB.First(&row, conv.ID).Error; err != nil {
		t.Fatalf("回读会话失败: %v", err)
	}
	if row.Mode != "human" {
		t.Errorf("锁定时不得改库，实际 mode=%q", row.Mode)
	}
}

// TestCheckHumanTimeoutSoftHandoffPersists 软接管超时（pending_handoff 挂了一个小时没人接）：
// 必须清掉 pending_handoff 与 handoff_notified_at，否则销售池永久挂着死线索。
func TestCheckHumanTimeoutSoftHandoffPersists(t *testing.T) {
	testutil.SetupTestDB(t)
	defer stateTestEnv(nil)()
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	notified := time.Now().Add(-time.Hour)
	conv := model.Conversation{
		TenantID: tid, CustomerID: 990703, Status: "active",
		Mode: "ai", PendingHandoff: true, HandoffNotifiedAt: &notified,
	}
	if err := db.DB.Create(&conv).Error; err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	defer db.DB.Unscoped().Delete(&model.Conversation{}, conv.ID)

	if CheckHumanTimeout(&conv) {
		t.Error("软超时后应继续 AI 模式（返回 false），不该再切人工")
	}
	var row model.Conversation
	if err := db.DB.First(&row, conv.ID).Error; err != nil {
		t.Fatalf("回读会话失败: %v", err)
	}
	if row.PendingHandoff {
		t.Error("软接管超时未落库，pending_handoff 仍为 true")
	}
	if row.HandoffNotifiedAt != nil {
		t.Error("handoff_notified_at 未清空，下轮会立刻再次判定")
	}
}
