// P2-3 复核批（2026-09-15）回归：会话创建互斥锁由"每客户一把、只增不删的 sync.Map"
// 改为固定分段锁——锁表有界（不再随长尾客户泄漏），同客户仍严格互斥。
package chatflow

import (
	"sync"
	"testing"
)

func TestConversationMutexSharded(t *testing.T) {
	// 同客户恒得同一把锁（互斥语义不变）
	a := GetConversationMutex(1001)
	b := GetConversationMutex(1001)
	if a != b {
		t.Fatal("同客户必须共享同一把锁")
	}
	// 跨客户取模落段：不同段必然不同锁，同段共享属预期设计
	if GetConversationMutex(1) == GetConversationMutex(2) {
		t.Fatal("不同段应为不同锁")
	}
	if GetConversationMutex(1) != GetConversationMutex(1+convMuShards) {
		t.Fatal("同段客户应共享同一把锁（分段语义）")
	}
	// 并发获取不得 panic / 不得出现锁对象逃逸变化
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(id uint) {
			defer wg.Done()
			mu := GetConversationMutex(id)
			mu.Lock()
			mu.Unlock()
		}(uint(i))
	}
	wg.Wait()
}
