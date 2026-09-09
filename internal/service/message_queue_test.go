// Package service 消息合并队列单元测试（P2-1 自动化测试，2026-08-30）
package service

import (
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
	old := DefaultSystemConfigService
	DefaultSystemConfigService = &SystemConfigService{cache: map[string]string{}}
	defer func() { DefaultSystemConfigService = old }()

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

// TestSetReply 测试回复设置
func TestSetReply(t *testing.T) {
	svc := NewMessageQueueService()
	// 无队列时不应 panic
	svc.SetReply(1, 100, "你好")
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
