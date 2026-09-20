// Package service 消息合并队列单元测试（P2-1 自动化测试，2026-08-30）
package service

import (
	"ai-scrm/internal/runtimecfg"
	"testing"
	"time"

	"ai-scrm/config"
)

// ============================================================
// 消息合并队列单元测试
//
// 测试覆盖：
//   1. 队列初始化
//   2. 队列键生成
//   3. 活跃队列计数
//   4. processing 锁超时
//   5. 简单消息识别
// ============================================================

// TestNewMessageQueueService 测试队列服务初始化
func TestNewMessageQueueService(t *testing.T) {
	svc := NewMessageQueueService()
	if svc == nil {
		t.Fatal("NewMessageQueueService 返回 nil")
	}
	if svc.queues == nil {
		t.Fatal("queues map 未初始化")
	}
}

// TestQueueKey 测试复合键生成
func TestQueueKey(t *testing.T) {
	tests := []struct {
		tid, cid uint
		want     string
	}{
		{1, 100, "1:100"},
		{123, 456, "123:456"},
		{0, 0, "0:0"},
	}
	for _, tt := range tests {
		got := queueKey(tt.tid, tt.cid)
		if got != tt.want {
			t.Errorf("queueKey(%d,%d)=%q 期望 %q", tt.tid, tt.cid, got, tt.want)
		}
	}
}

// TestActiveQueueCount 测试活跃队列计数
func TestActiveQueueCount(t *testing.T) {
	svc := NewMessageQueueService()
	if count := svc.ActiveQueueCount(); count != 0 {
		t.Fatalf("初始计数应为 0，实际 %d", count)
	}
}

// TestSweepIdleQueues 空闲队列回收：活跃/处理中/有积压的不删，空闲超阈值的删（2026-09-09 内存治理）
func TestSweepIdleQueues(t *testing.T) {
	svc := NewMessageQueueService()
	kIdle := queueKey(1, 100)
	kBusy := queueKey(1, 200)
	kPending := queueKey(1, 300)

	qIdle := svc.getQueue(kIdle)
	qBusy := svc.getQueue(kBusy)
	qPending := svc.getQueue(kPending)

	// 造三种状态：
	// 1) 空闲：lastActivity 改到很久以前
	qIdle.mu.Lock()
	qIdle.lastActivity = time.Now().Add(-time.Hour)
	qIdle.mu.Unlock()
	// 2) 处理中：processing=true 即便 idle 也不删
	qBusy.mu.Lock()
	qBusy.processing = true
	qBusy.lastActivity = time.Now().Add(-time.Hour)
	qBusy.mu.Unlock()
	// 3) 有积压：pending 非空即便 idle 也不删
	qPending.mu.Lock()
	qPending.pending = []PendingMessage{{Content: "x"}}
	qPending.lastActivity = time.Now().Add(-time.Hour)
	qPending.mu.Unlock()

	removed := svc.SweepIdleQueues(15 * time.Minute)
	if removed != 1 {
		t.Fatalf("应只回收 1 个空闲队列，实际 %d", removed)
	}
	// 处理中与有积压的队列必须仍在
	svc.mu.Lock()
	_, okBusy := svc.queues[kBusy]
	_, okPending := svc.queues[kPending]
	_, okIdle := svc.queues[kIdle]
	svc.mu.Unlock()
	if !okBusy || !okPending {
		t.Fatalf("处理中/有积压队列不应被回收: busy=%v pending=%v", okBusy, okPending)
	}
	if okIdle {
		t.Fatalf("空闲队列应已被回收")
	}
}

// TestSweepIdleQueuesRecentlyActive 最近活跃的队列不回收（lastActivity 在阈值内）
func TestSweepIdleQueuesRecentlyActive(t *testing.T) {
	svc := NewMessageQueueService()
	k := queueKey(1, 400)
	svc.getQueue(k) // 刚 get，lastActivity=now

	removed := svc.SweepIdleQueues(15 * time.Minute)
	if removed != 0 {
		t.Fatalf("最近活跃队列不应被回收，实际 %d", removed)
	}
	svc.mu.Lock()
	_, ok := svc.queues[k]
	svc.mu.Unlock()
	if !ok {
		t.Fatal("队列应仍在")
	}
}

// TestGetProcessingLockTimeout 测试 processing 锁超时配置
func TestGetProcessingLockTimeout(t *testing.T) {
	old := runtimecfg.DefaultSystemConfigService
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(map[string]string{}, nil)
	defer func() { runtimecfg.DefaultSystemConfigService = old }()

	timeout := getProcessingLockTimeout(1)
	if timeout != 600*time.Second {
		t.Fatalf("默认超时应为 600s，实际 %v", timeout)
	}
}

// TestGetQueue 获取或创建队列
func TestGetQueue(t *testing.T) {
	svc := NewMessageQueueService()
	k := queueKey(1, 100)

	q1 := svc.getQueue(k)
	if q1 == nil {
		t.Fatal("getQueue 返回 nil")
	}

	q2 := svc.getQueue(k)
	if q1 != q2 {
		t.Fatal("相同键返回不同队列")
	}

	q3 := svc.getQueue(queueKey(1, 200))
	if q1 == q3 {
		t.Fatal("不同键返回相同队列")
	}
}

// TestSimpleMessageDone 测试简单消息完成回调
func TestSimpleMessageDone(t *testing.T) {
	svc := NewMessageQueueService()
	// 无队列时不应 panic
	svc.SimpleMessageDone(1, 100)
}

// TestSimpleLockWatchdog P1-4 回归(2026-09-20)：简单消息串行锁持有者挂死不释放时，
// 看门狗须在 simpleLockTimeout 内复位并广播，后到请求不得永久静默。
func TestSimpleLockWatchdog(t *testing.T) {
	old := simpleLockTimeout
	simpleLockTimeout = 120 * time.Millisecond
	defer func() { simpleLockTimeout = old }()

	svc := NewMessageQueueService()
	// 第一条简单消息接管处理权后"挂死"（故意不调 SimpleMessageDone）
	_, sp1, _, _, isSimple1, _, _ := svc.EnqueueAndWait(77, 9001, "在吗", "")
	if !sp1 || !isSimple1 {
		t.Fatalf("第一条简单消息应立即接管: shouldProcess=%v isSimple=%v", sp1, isSimple1)
	}
	firedBefore := simpleWatchdogFired.Load()

	acquired := make(chan bool, 1)
	go func() {
		_, sp2, _, _, _, _, _ := svc.EnqueueAndWait(77, 9001, "好的", "")
		acquired <- sp2
	}()
	select {
	case ok := <-acquired:
		if !ok {
			t.Fatal("看门狗复位后第二条应成功接管简单锁")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("第二条简单消息永久阻塞——看门狗未复位 simple 锁")
	}
	if got := simpleWatchdogFired.Load(); got <= firedBefore {
		t.Fatalf("看门狗计数未递增: before=%d after=%d", firedBefore, got)
	}
	svc.SimpleMessageDone(77, 9001)
}

// TestSimpleWatchdogTokenGuard P1-4 确定性单测：看门狗只复位登记令牌匹配的接管，
// 旧持有者已释放、新持有者接管（令牌已递增）时不得误伤。
func TestSimpleWatchdogTokenGuard(t *testing.T) {
	svc := NewMessageQueueService()
	k := queueKey(77, 9100)
	q := svc.getQueue(k)
	q.mu.Lock()
	q.simpleProcessing = true
	q.simpleToken = 5 // 当前持有者是第 5 次接管
	q.mu.Unlock()

	// 旧代际（第 4 次接管）的看门狗迟到：令牌不符 → 不复位
	svc.simpleWatchdog(k, 4)
	q.mu.Lock()
	held := q.simpleProcessing
	q.mu.Unlock()
	if !held {
		t.Fatal("令牌不符时看门狗误复位了新持有者的锁")
	}
	// 当代际看门狗到期：复位 + 计数
	before := simpleWatchdogFired.Load()
	svc.simpleWatchdog(k, 5)
	q.mu.Lock()
	held = q.simpleProcessing
	q.mu.Unlock()
	if held {
		t.Fatal("令牌相符时看门狗应复位挂死的简单锁")
	}
	if simpleWatchdogFired.Load() != before+1 {
		t.Fatalf("看门狗计数应 +1: before=%d", before)
	}
	// 幂等：锁已释放后再触发不重复计数
	svc.simpleWatchdog(k, 5)
	if simpleWatchdogFired.Load() != before+1 {
		t.Fatal("已释放的锁不应重复触发计数")
	}
}

// TestSetReply 测试回复设置（P1-19：epoch 代际校验——无队列/epoch=0 不应 panic）
func TestSetReply(t *testing.T) {
	svc := NewMessageQueueService()
	// epoch=0 不做代际拦截，无队列时不应 panic
	svc.SetReply(1, 100, 0, "你好")
}

// TestSetReplyStaleEpoch P1-19 回归：过期代际的 SetReply 必须被丢弃且不误伤当前批次
func TestSetReplyStaleEpoch(t *testing.T) {
	svc := NewMessageQueueService()
	k := queueKey(1, 200)
	q := svc.getQueue(k)
	q.mu.Lock()
	q.processing = true
	q.epoch = 5 // 当前代际 5
	q.redisLock = nil
	q.mu.Unlock()

	// 旧处理者持代际 3 的 SetReply 应被丢弃（状态不被覆盖）
	svc.SetReply(1, 200, 3, "过期回复")
	q.mu.Lock()
	if q.lastReply != "" {
		t.Fatalf("过期 SetReply 不应写入 lastReply，得到 %q", q.lastReply)
	}
	if !q.processing {
		t.Fatalf("过期 SetReply 不应释放 processing")
	}
	q.mu.Unlock()

	// 当前代际 5 的 SetReply 正常生效
	svc.SetReply(1, 200, 5, "正确回复")
	q.mu.Lock()
	if q.lastReply != "正确回复" {
		t.Fatalf("当前代际 SetReply 应生效，得到 %q", q.lastReply)
	}
	q.mu.Unlock()
}

// TestTidFromKeyFromQueueKey 测试键解析与 queueKey 对称性
func TestTidFromKeyFromQueueKey(t *testing.T) {
	// queueKey 产生的键应能被 tidFromKey 正确解析
	for _, tt := range []struct {
		tid, cid uint
	}{
		{1, 100},
		{123, 456},
	} {
		k := queueKey(tt.tid, tt.cid)
		got := tidFromKey(k)
		if got != tt.tid {
			t.Errorf("tidFromKey(%q)=%d 期望 %d", k, got, tt.tid)
		}
	}
}

// TestCalcHumanlikeDelay 模拟延迟收短：非 mock 下固定落在 5~15s 区间（2026-09-09）。
// 背景：旧公式"打字时长+线下偏移"最高可到 75s+，叠加合并窗口/AI调用后客户端要干等
// 近 2 分钟才收到回复，还阻塞 AI 消息落库与 WS 推送（不实时、存储不稳定）。
// 现改为固定小延迟模拟"输入节奏"，这里锁定 [5s, 15s] 区间防止回归到长延迟。
func TestCalcHumanlikeDelay(t *testing.T) {
	oldCfg := config.GlobalConfig
	config.GlobalConfig = &config.Config{AI: config.AIConfig{MockMode: false}}
	defer func() { config.GlobalConfig = oldCfg }()

	// 各档输入都以同一套延迟计算，验证上下界
	for i := 0; i < 50; i++ {
		d := CalcHumanlikeDelay(1, "你们有几款车，空间大不大，能载几个人？", 0, 1, false)
		if d < 5*time.Second || d > 15*time.Second {
			t.Fatalf("延迟 %v 超出 5~15s 区间", d)
		}
	}
	// 到店倾向客户同样在区间内
	for i := 0; i < 50; i++ {
		d := CalcHumanlikeDelay(1, "我想试驾", 0, 1, true)
		if d < 5*time.Second || d > 15*time.Second {
			t.Fatalf("到店倾向延迟 %v 超出 5~15s 区间", d)
		}
	}
}

// TestCalcHumanlikeDelayMock 模拟模式(AI_MOCK_MODE=true)必须 0 延迟，避免开发联调挂死
func TestCalcHumanlikeDelayMock(t *testing.T) {
	oldCfg := config.GlobalConfig
	config.GlobalConfig = &config.Config{AI: config.AIConfig{MockMode: true}}
	defer func() { config.GlobalConfig = oldCfg }()

	if d := CalcHumanlikeDelay(1, "你好", 0, 1, false); d != 0 {
		t.Fatalf("模拟模式延迟应为 0，实际 %v", d)
	}
}

// TestP07BatchClosedNoOrphans P0-7 复核批（2026-09-15）回归锁定：
// 合并窗口关账后（AI 生成/延迟期间，processing 未释放）到达的消息，
// 旧实现会被标进已封账批次并等待"不含自己内容"的旧回复——成为永远丢回复的孤儿，
// 残留至 600s 超时被清（连兜底回复都没有）。修复引入 batchClosed 关账标志后，
// 此类消息必须走积压接管、成为下一批处理者（shouldProcess=true 且只含自己的内容）。
func TestP07BatchClosedNoOrphans(t *testing.T) {
	oldCfg := config.GlobalConfig
	config.GlobalConfig = &config.Config{ReplySpeed: config.ReplySpeedConfig{MaxMergeMessages: 5, MergeWindowSeconds: 1}}
	defer func() { config.GlobalConfig = oldCfg }()
	oldRC := runtimecfg.DefaultSystemConfigService
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(map[string]string{"merge_window_seconds": "1"}, nil)
	defer func() { runtimecfg.DefaultSystemConfigService = oldRC }()

	svc := NewMessageQueueService()
	tid, cid := uint(7), uint(9407)

	type qres struct {
		merged  string
		process bool
		reply   string
		epoch   uint64
	}
	chA := make(chan qres, 1)
	go func() {
		m, p, r, _, _, _, e := svc.EnqueueAndWait(tid, cid, "第一条内容", "")
		chA <- qres{m, p, r, e}
	}()
	a := <-chA // ~1s 合并窗口到期返回：此时批次已关账、processing 仍被 A 持有（模拟 AI 生成中）
	if !a.process || a.merged != "第一条内容" {
		t.Fatalf("前置条件破坏：A 应为批1处理者，得 process=%v merged=%q", a.process, a.merged)
	}

	chB := make(chan qres, 1)
	go func() {
		m, p, r, _, _, _, e := svc.EnqueueAndWait(tid, cid, "第二条内容", "")
		chB <- qres{m, p, r, e}
	}()
	time.Sleep(200 * time.Millisecond) // 确保 B 已在关账批次之后入队（旧行为下此刻 B 已挂死等旧回复）

	svc.SetReply(tid, cid, a.epoch, "对第一条的回复") // A 交卷唤醒
	b := <-chB
	if !b.process {
		t.Fatalf("P0-7 回归：关账后到达的消息成孤儿（shouldProcess=false，只能拿旧回复 %q）", b.reply)
	}
	if b.merged != "第二条内容" {
		t.Fatalf("接管批应只含自身内容，得 %q", b.merged)
	}
	if b.epoch == a.epoch {
		t.Fatal("新批次必须携带新一代际（SetReply fencing 依据）")
	}
	svc.SetReply(tid, cid, b.epoch, "对第二条的回复") // 交还 B 批，状态归零
}
