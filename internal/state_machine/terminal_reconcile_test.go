// 终态收口单测（2026-10-13 修复批 P2-1 口径更正）
//
// 背景：全表 598/1021 行 status='running' AND current_node='end'，而其 flow_instances.status
// 早已 completed。扫描是 Limit(100) 且无序，这批行每轮吃光配额，真正卡住的实例永远排不上。
// terminalReconcile 的职责是把它们补标 completed、释放配额，且**绝不误伤**真卡死的实例。
//
// 变异口径（每条断言都有对应的"改回去就红"）：
//   - 去掉 Local 预筛 current_node!='end' → TestReconcileKeepsGenuinelyStuck 不红（预筛只省往返，
//     但若有人把预筛改成"无脑全查+全标完成"，TestReconcileDoesNotTouchRunningInstance 必红）。
//   - 把租户一致性检查 sm.TenantID != tg.tenantID 去掉 → TestReconcileTenantIsolation 必红。
//   - 把 WARN 的 failed++ 删掉 → TestReconcileWriteFailWarns 必红。
//   - 返回 stale 而非 remaining → TestReconcileReturnsRemaining 必红。
package state_machine

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// 建一条流程实例（用于联查 flow_instances 判定终态）
func newFlowInstance(t *testing.T, tid uint, status string) *model.FlowInstance {
	t.Helper()
	// FlowDefID/StartedAt 等非空列给合法值，避免被 NOT NULL 约束拒掉
	fi := &model.FlowInstance{
		TenantID: tid, FlowDefID: 1, CurrentNodeID: "end",
		Status: status, StartedAt: time.Now().Add(-24 * time.Hour),
	}
	if err := db.DB.Create(fi).Error; err != nil {
		t.Fatalf("建流程实例失败: %v", err)
	}
	t.Cleanup(func() {
		_ = db.DB.Where("tenant_id = ?", tid).Delete(&model.FlowInstance{}).Error
	})
	return fi
}

// 建一条心跳早已超时（24h 前）的 running 状态机行，节点名可指定
func newStaleRowAtNode(t *testing.T, tid uint, flowInstID uint, node string) *model.FlowStateMachine {
	t.Helper()
	sm := &model.FlowStateMachine{
		TenantID: tid, OneID: "reconcile_probe", FlowInstanceID: flowInstID,
		CurrentNode: node, Status: model.SMStatusRunning, Version: 1,
		HeartbeatTS: time.Now().Add(-24 * time.Hour),
	}
	if err := db.DB.Create(sm).Error; err != nil {
		t.Fatalf("建超时状态机行失败: %v", err)
	}
	t.Cleanup(func() {
		_ = db.DB.Where("tenant_id = ?", tid).Delete(&model.FlowStateMachine{}).Error
	})
	return sm
}

// TestReconcileMarksCompletedInstance 终态脏数据必须被补标 completed，且从待处理集合剔除
func TestReconcileMarksCompletedInstance(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	fi := newFlowInstance(t, tid, "completed")
	sm := newStaleRowAtNode(t, tid, fi.ID, "end")

	buf := new(bytes.Buffer)
	old := log.Writer()
	log.SetOutput(buf)
	defer log.SetOutput(old)

	remaining := terminalReconcile([]model.FlowStateMachine{*sm})
	if len(remaining) != 0 {
		t.Fatalf("已收口的行不该留在待处理集合里（它会再进 requeue 分支发无意义信号），实际残留 %d 行", len(remaining))
	}

	var got model.FlowStateMachine
	if err := db.DB.Where("id = ?", sm.ID).First(&got).Error; err != nil {
		t.Fatalf("回读收口结果失败: %v", err)
	}
	if got.Status != model.SMStatusCompleted {
		t.Errorf("流程实例早已 completed 的脏数据应被补标 completed，实际 status=%q", got.Status)
	}
	if !strings.Contains(buf.String(), "终态收口") {
		t.Errorf("收口动作必须留日志痕迹（运维要能看见在批量补标），日志: %q", buf.String())
	}
}

// TestReconcileDoesNotTouchRunningInstance 真·卡死的实例（流程还在 running）必须原样保留，
// 这是本修复最关键的"不许误伤"边界——误标完成等于把活流程判死，比不修更糟。
func TestReconcileDoesNotTouchRunningInstance(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	// 流程实例仍在 running（真卡死），但状态机节点恰好也是 end（极端但必须安全）
	fi := newFlowInstance(t, tid, "running")
	sm := newStaleRowAtNode(t, tid, fi.ID, "end")

	remaining := terminalReconcile([]model.FlowStateMachine{*sm})
	if len(remaining) != 1 {
		t.Fatalf("流程仍在 running 的实例必须留在待处理集合，实际 %d 行", len(remaining))
	}

	var got model.FlowStateMachine
	if err := db.DB.Where("id = ?", sm.ID).First(&got).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if got.Status != model.SMStatusRunning {
		t.Errorf("流程仍在 running 时不得改状态（误标完成＝把活流程判死），实际 status=%q", got.Status)
	}
}

// TestReconcileSuspendedIsNotTerminal suspended 是人工挂起不是终态，挂起实例等人恢复，不得被顺手标完成
func TestReconcileSuspendedIsNotTerminal(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	fi := newFlowInstance(t, tid, "suspended")
	sm := newStaleRowAtNode(t, tid, fi.ID, "end")

	remaining := terminalReconcile([]model.FlowStateMachine{*sm})
	if len(remaining) != 1 {
		t.Fatalf("suspended 实例必须保留（不是终态），实际 %d 行", len(remaining))
	}
	var got model.FlowStateMachine
	if err := db.DB.Where("id = ?", sm.ID).First(&got).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if got.Status != model.SMStatusRunning {
		t.Errorf("suspended 流程的状态机不得被标 completed，实际 status=%q", got.Status)
	}
}

// TestReconcileTenantIsolation 实例ID跨租户撞号时，只有租户一致的才可收口。
// 注意必须用 CreateTenantCode 造两个**不同**租户：CreateTenant 走固定 code
// unit_test_tenant 且"已存在即复用"，进程内多次调用返回同一个 ID，
// 用它写隔离用例会拿到 tidA==tidB，测试在"没有隔离"的前提下假绿。
func TestReconcileTenantIsolation(t *testing.T) {
	testutil.SetupTestDB(t)
	tidA := testutil.CreateTenantCode(t, "recon_iso_a")
	defer testutil.CleanupTenant(t, tidA)
	tidB := testutil.CreateTenantCode(t, "recon_iso_b")
	defer testutil.CleanupTenant(t, tidB)
	if tidA == tidB {
		t.Fatalf("前置自检：本用例必须有两个不同租户才能验证隔离，实际 A=%d B=%d", tidA, tidB)
	}

	// B 租户的流程实例已 completed，但 A 租户恰好有同 flow_instance_id 的状态机行
	fiB := newFlowInstance(t, tidB, "completed")
	smA := newStaleRowAtNode(t, tidA, fiB.ID, "end")

	remaining := terminalReconcile([]model.FlowStateMachine{*smA})
	if len(remaining) != 1 {
		t.Fatalf("租户不一致的行不得被收口（跨租户数据污染），实际剩余 %d 行", len(remaining))
	}
	var got model.FlowStateMachine
	if err := db.DB.Where("id = ?", smA.ID).First(&got).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if got.Status != model.SMStatusRunning {
		t.Errorf("跨租户撞号行必须保持 running，实际 status=%q", got.Status)
	}
}

// TestReconcileWriteFailWarns 收口写失败必须 WARN 且该行仍留在待处理集合（下轮重试）
func TestReconcileWriteFailWarns(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	fi := newFlowInstance(t, tid, "completed")
	sm := newStaleRowAtNode(t, tid, fi.ID, "end")

	oldWriter := sweepHeartbeatWriter
	sweepHeartbeatWriter = func(id uint, fields map[string]interface{}) error {
		return errors.New("模拟写失败")
	}
	defer func() { sweepHeartbeatWriter = oldWriter }()

	buf := new(bytes.Buffer)
	oldLog := log.Writer()
	log.SetOutput(buf)
	defer log.SetOutput(oldLog)

	remaining := terminalReconcile([]model.FlowStateMachine{*sm})
	if len(remaining) != 1 {
		t.Errorf("写失败的行必须留在待处理集合供下轮重试，实际 %d 行", len(remaining))
	}
	out := buf.String()
	if !strings.Contains(out, "终态收口写失败") {
		t.Errorf("收口写失败必须 WARN（静默失败会让脏数据永久滞留、悄悄吃扫描配额），日志: %q", out)
	}
	if !strings.Contains(out, "写失败待重试") {
		t.Errorf("汇总行必须报出写失败数（运维要能看见有积压），日志: %q", out)
	}
}

// TestReconcileSkipsNonTerminalNodePreFilter 非 end 节点（典型 ai_chat）不进联查候选，
// 验证本地预筛既省往返又不影响正确性
func TestReconcileSkipsNonTerminalNodePreFilter(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	fi := newFlowInstance(t, tid, "running")
	sm := newStaleRowAtNode(t, tid, fi.ID, "ai_chat")

	remaining := terminalReconcile([]model.FlowStateMachine{*sm})
	if len(remaining) != 1 {
		t.Fatalf("ai_chat 节点必须原样保留待 requeue 处理，实际 %d 行", len(remaining))
	}
	var got model.FlowStateMachine
	if err := db.DB.Where("id = ?", sm.ID).First(&got).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if got.Status != model.SMStatusRunning {
		t.Errorf("ai_chat 节点不得被改状态，实际 status=%q", got.Status)
	}
}

// TestReconcileEmptyInput 空输入不炸（巡检无超时时每分钟都会走到这条路径）
func TestReconcileEmptyInput(t *testing.T) {
	if got := terminalReconcile(nil); len(got) != 0 {
		t.Errorf("空输入应原样返回空集合，实际 %d 行", len(got))
	}
}
