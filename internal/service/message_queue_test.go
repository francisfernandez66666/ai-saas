// Package service 消息合并队列单元测试（P2-1 自动化测试，2026-08-30）
package service

import (
	"testing"
	"time"
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
