// Package chatflow 聊天流模块：延迟取消/留资检测与 OneID 合并/会话状态维护/业务驱动消费
package chatflow

import (
	"log"
	"sync"
	"time"
)

// ============================================================
// 延迟取消注册表 + 会话互斥锁（Phase C 自 chat.go 下沉）
// 设计：通过channel机制通知正在sleep的goroutine立即结束延迟
// 不同于DB-based的message_queue方案，本系统延迟在goroutine内实现
// 所以用channel取消机制替代DB scheduled_at更新
// ============================================================

var delayCancelMap sync.Map // key: customerID(uint), value: chan struct{}

// RegisterDelayCancel 注册一个延迟取消通道，返回通道
// 在time.Sleep前调用，用于替代不可取消的sleep
func RegisterDelayCancel(customerID uint) chan struct{} {
	ch := make(chan struct{}, 1) // 带缓冲，避免发送方阻塞
	delayCancelMap.Store(customerID, ch)
	return ch
}

// CancellableSleep 可取消的延迟：等待duration或收到取消信号
// 返回true=正常等完，false=被取消（立即回复）
func CancellableSleep(customerID uint, duration time.Duration) bool {
	if duration <= 0 {
		return true
	}
	ch := RegisterDelayCancel(customerID)
	defer delayCancelMap.Delete(customerID) // 清理

	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true // 正常等完
	case <-ch:
		log.Printf("[延迟清零] 客户%d 延迟被取消，立即回复", customerID)
		return false // 被取消
	}
}

// CancelDelay 取消指定客户的延迟，使其立即回复
func CancelDelay(customerID uint) {
	if v, ok := delayCancelMap.Load(customerID); ok {
		ch := v.(chan struct{})
		select {
		case ch <- struct{}{}:
			log.Printf("[延迟清零] 客户%d 取消信号已发送", customerID)
		default:
			log.Printf("[延迟清零] 客户%d 取消通道已满，可能已被处理", customerID)
		}
	} else {
		log.Printf("[延迟清零] 客户%d 当前无活跃延迟", customerID)
	}
}

// GetConversationMutex 获取客户级别的会话创建互斥锁
// 同一客户共享一把锁，不同客户互不阻塞
func GetConversationMutex(customerID uint) *sync.Mutex {
	// 函数级会话互斥表（每次调用新建，作用于本次获取流程，不同客户互不阻塞）
	var conversationMu sync.Map // key: customerID(uint), value: *sync.Mutex

	mu, _ := conversationMu.LoadOrStore(customerID, &sync.Mutex{})
	return mu.(*sync.Mutex)
}
