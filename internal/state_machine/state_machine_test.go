// §八-7 零测试包最小单测（2026-09-18）：state_machine 的两条核心不变式——
//  1. 巡检 requeue 开关默认关（Bug3 止血：waiting 是正常暂停语义，无脑 requeue 曾半小时
//     产生 30 条垃圾 flow_requeue 事件）；配置服务未初始化时必须返回 false（fail-closed）；
//  2. Advance 的乐观锁：version 不匹配 → 0 行受影响 → 返回 ErrVersionConflict（可 errors.Is），
//     匹配则推进节点并把版本 +1。这是流程引擎并发推进不被踩的唯一屏障。
//
// DB 用例走 testutil.SetupTestDB（本地无 DB 自动跳过，TestMain 收尾打印跳过条数）。
// 注：SweepOnce 不做单测——其扫描无租户过滤（跨全库回收心跳），单测里真跑会污染
// 共享 dev 库的其它实例行；该链路的行为由 E2E 层覆盖。
package state_machine

import (
	"errors"
	"os"
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/testutil"
)

// TestMain 统一出口：DB 不可用被跳过的用例数在收尾显式打印（防静默绿）
func TestMain(m *testing.M) {
	os.Exit(testutil.RunMain(m))
}

// TestIsRequeueEnabledDefaultFalse 开关默认关 / 配置服务未初始化一律 false
func TestIsRequeueEnabledDefaultsFalse(t *testing.T) {
	old := runtimecfg.DefaultSystemConfigService
	defer func() { runtimecfg.DefaultSystemConfigService = old }()

	runtimecfg.DefaultSystemConfigService = nil
	if isRequeueEnabled() {
		t.Errorf("配置服务未初始化时 requeue 必须为 false（fail-closed，防启动早期误发事件）")
	}

	restore := runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(nil, nil))
	if isRequeueEnabled() {
		t.Errorf("缺 sm_sweep_requeue_enabled 键时应取默认 false")
	}
	restore()

	restore2 := runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(
		map[string]string{"sm_sweep_requeue_enabled": "true"}, nil))
	defer restore2()
	if !isRequeueEnabled() {
		t.Errorf("sm_sweep_requeue_enabled=true 时应为 true（否则断点续跑链路永远打不开）")
	}
}

// uniqueFlowInstID 造一个足够独特的流程实例ID（flow_instance_id 全局唯一索引，
// 且 testutil.CleanupTenant 的级联清单不含 flow_state_machines，故用例内自行 defer 删）
func uniqueFlowInstID() uint {
	return uint(time.Now().UnixNano() % 1e9) //nolint:gosec // 测试用低 30 位足够
}

// newSMRow 在指定租户下建一条状态机行，并登记清理
func newSMRow(t *testing.T, tid uint, oneID string, node string) *model.FlowStateMachine {
	t.Helper()
	sm := &model.FlowStateMachine{
		TenantID: tid, OneID: oneID, FlowInstanceID: uniqueFlowInstID(),
		CurrentNode: node, Status: model.SMStatusRunning, Version: 1,
		HeartbeatTS: time.Now(),
	}
	if err := db.DB.Create(sm).Error; err != nil {
		t.Fatalf("建状态机行失败: %v", err)
	}
	t.Cleanup(func() {
		_ = db.DB.Where("tenant_id = ?", tid).Delete(&model.FlowStateMachine{}).Error
	})
	return sm
}

// TestAdvanceOptimisticLock Advance 版本冲突与正常推进（DB 门控）
func TestAdvanceOptimisticLock(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	sm := newSMRow(t, tid, "one_sm_adv", "node_a")
	flowInstID := sm.FlowInstanceID

	// 1) 版本不匹配（模拟并发另一方已推进）→ ErrVersionConflict，且行不动
	if err := Advance(tid, flowInstID, "node_stale", 99, `{"x":1}`); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("版本不匹配应返回 ErrVersionConflict，实际 %v", err)
	}
	var row model.FlowStateMachine
	if err := db.DB.Where("flow_instance_id = ? AND tenant_id = ?", flowInstID, tid).First(&row).Error; err != nil {
		t.Fatalf("回读状态机失败: %v", err)
	}
	if row.Version != 1 || row.CurrentNode != "node_a" {
		t.Errorf("冲突推进不得改动原行: version=%d node=%q", row.Version, row.CurrentNode)
	}

	// 2) 版本匹配 → 推进成功，版本 +1，节点/状态快照落地
	if err := Advance(tid, flowInstID, "node_b", 1, `{"step":2}`); err != nil {
		t.Fatalf("正常推进不应报错: %v", err)
	}
	if err := db.DB.Where("flow_instance_id = ? AND tenant_id = ?", flowInstID, tid).First(&row).Error; err != nil {
		t.Fatalf("回读状态机失败: %v", err)
	}
	if row.Version != 2 || row.CurrentNode != "node_b" || row.StateJSON != `{"step":2}` {
		t.Fatalf("推进后字段异常: version=%d node=%q state=%q", row.Version, row.CurrentNode, row.StateJSON)
	}

	// 3) 拿旧版本再推一次 → 依然冲突（调用方须重读后重试）
	if err := Advance(tid, flowInstID, "node_c", 1, "{}"); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("用已被推进的旧版本应冲突，实际 %v", err)
	}

	// 4) 跨租户隔离：别的租户拿正确版本也不能推进本租户实例
	otherTID := testutil.CreateTenantCode(t, "sm_other")
	defer testutil.CleanupTenant(t, otherTID)
	if err := Advance(otherTID, flowInstID, "node_x", 2, "{}"); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("跨租户推进应失败（0 行受影响→版本冲突），实际 %v", err)
	}
}

// TestInitIdempotentAndHeartbeat Init 幂等（已存在返回现有行）+ 心跳续期（DB 门控）
func TestInitIdempotentAndHeartbeat(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	t.Cleanup(func() {
		_ = db.DB.Where("tenant_id = ?", tid).Delete(&model.FlowStateMachine{}).Error
	})

	flowInstID := uniqueFlowInstID() + 3
	first, err := Init(tid, "one_sm_init", flowInstID, "start")

	if err != nil || first == nil {
		t.Fatalf("首次 Init 失败: row=%v err=%v", first, err)
	}
	if first.Version != 1 || first.CurrentNode != "start" || first.Status != model.SMStatusRunning {
		t.Errorf("Init 初值异常: version=%d node=%q status=%q", first.Version, first.CurrentNode, first.Status)
	}
	again, err := Init(tid, "one_sm_init_other", flowInstID, "other_node")
	if err != nil {
		t.Fatalf("重复 Init 不应报错: %v", err)
	}
	if again.ID != first.ID || again.CurrentNode != "start" || again.OneID != "one_sm_init" {
		t.Errorf("重复 Init 应幂等返回现有行，实际 id=%d node=%q one=%q", again.ID, again.CurrentNode, again.OneID)
	}

	// 心跳：只推进 heartbeat_ts，不动节点/版本
	oldBeat := again.HeartbeatTS
	time.Sleep(2 * time.Millisecond)
	if err := Heartbeat(tid, flowInstID); err != nil {
		t.Fatalf("Heartbeat 失败: %v", err)
	}
	var row model.FlowStateMachine
	if err := db.DB.Where("id = ?", first.ID).First(&row).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if !row.HeartbeatTS.After(oldBeat) {
		t.Errorf("心跳未续期: old=%v new=%v", oldBeat, row.HeartbeatTS)
	}
	if row.CurrentNode != "start" || row.Version != 1 {
		t.Errorf("心跳不应改节点/版本: node=%q version=%d", row.CurrentNode, row.Version)
	}

	// 完成态标记（巡检只扫 running，故 complete 后不应再被回收）
	if err := Complete(tid, flowInstID); err != nil {
		t.Fatalf("Complete 失败: %v", err)
	}
	if err := db.DB.Where("id = ?", first.ID).First(&row).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if row.Status != model.SMStatusCompleted {
		t.Errorf("Complete 后状态应为 completed，实际 %q", row.Status)
	}

	// Get：不存在返回 (nil,nil) 而非 error（调用方以"无实例"分支处理）
	got, err := Get(tid, flowInstID+999999)
	if err != nil || got != nil {
		t.Errorf("Get 不存在的实例应返回 (nil,nil)，实际 got=%v err=%v", got, err)
	}
}
