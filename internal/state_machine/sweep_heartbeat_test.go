// FIX-7 反证单测（2026-09-29 审计批二）：巡检两处心跳/标记写入失败必须各自出 WARN。
// 旧写法 `_ = ...Updates(...)` 吞错——发布 requeue 成功后心跳没刷 = 下轮对同一实例再发一条
// flow_requeue（at-least-once 既定语义，消费端需幂等），但"会重发"原先没人知道。
//
// 为什么这组用例敢真跑 SweepOnce（旧注释"不做单测——扫描无租户过滤会污染共享库其它实例行"）：
// 用例把 sweepHeartbeatWriter 桩成**必失败**——对本库所有超时行零写入，扫描是只读的、发布进
// 内存假 center，整条链路不落任何 DB 副作用；反而"写失败必红"正是本组要证的分支。
// 变异口径：把 WARN 那两行删掉（回到吞错），①②各自立即红。
package state_machine

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/mq"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/testutil"
)

// stubFailingWriter 把心跳写入桩成必失败，并登记清理还原全局变量
func stubFailingWriter(t *testing.T) {
	t.Helper()
	old := sweepHeartbeatWriter
	sweepHeartbeatWriter = func(id uint, fields map[string]interface{}) error {
		return context.DeadlineExceeded // 任意非 nil 错误即可，测试只判"有没有出声"
	}
	t.Cleanup(func() { sweepHeartbeatWriter = old })
}

// captureLog 捕获 std log 输出（SweepOnce 用 log.Printf），返回缓冲区
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	old := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return buf
}

// newStaleRunningRow 建一条心跳早已超时（24h 前）的 running 实例行，供 SweepOnce 扫到
func newStaleRunningRow(t *testing.T, tid uint, oneID string) *model.FlowStateMachine {
	t.Helper()
	sm := &model.FlowStateMachine{
		TenantID: tid, OneID: oneID, FlowInstanceID: uniqueFlowInstID(),
		CurrentNode: "start", Status: model.SMStatusRunning, Version: 1,
		HeartbeatTS: time.Now().Add(-24 * time.Hour),
	}
	if err := db.DB.Create(sm).Error; err != nil {
		t.Fatalf("建超时实例行失败: %v", err)
	}
	t.Cleanup(func() {
		_ = db.DB.Where("tenant_id = ?", tid).Delete(&model.FlowStateMachine{}).Error
	})
	return sm
}

// fakeCenter 内存假消息中心：Publish 恒成功并计数，其余方法空操作（不落 Kafka 不写审计表）
type fakeCenter struct{ published int }

// Publish 记录一次发布并返回成功：测试用它数「sweep 真的把事件发出去了没有」
func (f *fakeCenter) Publish(ctx context.Context, topic string, tenantID uint, oneID string, eventType string, payload interface{}) error {
	f.published++
	return nil
}

// Subscribe 空实现：本测试只验发布侧，消费回调注册不需要行为
func (f *fakeCenter) Subscribe(topic string, handler mq.EventHandler) {}

// StartConsumers 空实现：不起任何消费循环，避免测试进程挂后台 goroutine
func (f *fakeCenter) StartConsumers(ctx context.Context) {}

// Close 空实现：内存假件无连接资源可释放
func (f *fakeCenter) Close() error { return nil }

// TestSweepHeartbeatWriteFailWarnsRecheck 开关关（默认）：requeue 关闭分支的心跳/标记写失败必须 WARN
func TestSweepHeartbeatWriteFailWarnsRecheck(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	old := runtimecfg.DefaultSystemConfigService
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(map[string]string{}, nil)
	defer func() { runtimecfg.DefaultSystemConfigService = old }()

	newStaleRunningRow(t, tid, "fix7_recheck")
	stubFailingWriter(t)
	buf := captureLog(t)

	SweepOnce(30 * time.Minute)
	out := buf.String()
	if !strings.Contains(out, "心跳/标记写入失败") {
		t.Errorf("requeue 关闭分支写失败必须 WARN（吞掉即同一实例反复超时却查不出为何压不下去），日志: %q", out)
	}
}

// TestSweepPublishOKHeartbeatFailWarnsResend 开关开 + 发布成功：心跳写失败必须 WARN「下轮将重发（消费端需幂等）」
func TestSweepPublishOKHeartbeatFailWarnsResend(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	old := runtimecfg.DefaultSystemConfigService
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(
		map[string]string{"sm_sweep_requeue_enabled": "true"}, nil)
	defer func() { runtimecfg.DefaultSystemConfigService = old }()

	oldCenter := mq.DefaultCenter
	fc := &fakeCenter{}
	mq.DefaultCenter = fc
	defer func() { mq.DefaultCenter = oldCenter }()

	newStaleRunningRow(t, tid, "fix7_resend")
	stubFailingWriter(t)
	buf := captureLog(t)

	SweepOnce(30 * time.Minute)
	if fc.published == 0 {
		t.Fatalf("前置自检：假 center 必须真的收到过 flow_requeue 发布，否则 WARN 断言在空集上绿")
	}
	out := buf.String()
	if !strings.Contains(out, "心跳未刷新") || !strings.Contains(out, "下轮将重发（消费端需幂等）") {
		t.Errorf("发布成功但心跳没刷 = 下轮必重发，这条 WARN 是 at-least-once 语义唯一的可见面，日志: %q", out)
	}
}
