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

// conversationMu 客户级会话创建互斥锁（包级单例，跨请求共享）
// 修复 C4：原实现每次调用 new 一个 sync.Map，锁完全不生效，导致并发同客户请求
// 各自创建重复会话/重复 FlowStateMachine 行。改为包级共享锁。
// D8 修复(2026-09-14)：进程内锁仅约束单实例——多实例下两节点可各建一个 active 会话，
// 调用侧（chat_main.ensureActiveConversation）叠加 Redis 短锁裁决。
// P2-3 修复(2026-09-15)：原"每客户一把 Mutex 的 sync.Map"只增不删（删除与等待者
// 存在双锁竞态，无法安全回收），长尾客户下无界泄漏。此锁仅护"建会话去重"秒级临界区，
// 改 128 分段锁：同客户仍互斥、跨段并行度足够、内存有界。
const convMuShards = 128

var conversationMu [convMuShards]sync.Mutex

// D11 修复(2026-09-14)：改为"每客户一组等待通道"，用互斥锁保护，替换原
// customerID→单个 chan 的 sync.Map。旧实现同客户并发 CancellableSleep 互相覆盖：
// 后注册者 Store 顶掉前者，前者 defer Delete 又把后者的登记删掉，导致 CancelDelay
// 打错对象/漏取消，且"立即回复"信号可能谁都没收到（前者睡满）。
var (
	delayMu     sync.Mutex
	delayCancel = map[uint]map[*delayWait]struct{}{} // customerID → 等待集合
)

// delayWait 一次延迟等待的注册句柄
type delayWait struct {
	ch chan struct{}
}

// RegisterDelayCancel 注册一个延迟取消通道，返回通道
// 在time.Sleep前调用，用于替代不可取消的sleep
func RegisterDelayCancel(customerID uint) chan struct{} {
	w := &delayWait{ch: make(chan struct{}, 1)}
	delayMu.Lock()
	set := delayCancel[customerID]
	if set == nil {
		set = map[*delayWait]struct{}{}
		delayCancel[customerID] = set
	}
	set[w] = struct{}{}
	delayMu.Unlock()
	return w.ch
}

// unregisterDelay 从等待集合摘除本句柄（仅删自己，不碰同客户其它等待；集合空则删键）
func unregisterDelay(customerID uint, w *delayWait) {
	delayMu.Lock()
	if set := delayCancel[customerID]; set != nil {
		delete(set, w)
		if len(set) == 0 {
			delete(delayCancel, customerID)
		}
	}
	delayMu.Unlock()
}

// CancellableSleep 可取消的延迟：等待duration或收到取消信号
// 返回true=正常等完，false=被取消（立即回复）
func CancellableSleep(customerID uint, duration time.Duration) bool {
	if duration <= 0 {
		return true
	}
	w := &delayWait{ch: make(chan struct{}, 1)}
	delayMu.Lock()
	set := delayCancel[customerID]
	if set == nil {
		set = map[*delayWait]struct{}{}
		delayCancel[customerID] = set
	}
	set[w] = struct{}{}
	delayMu.Unlock()
	defer unregisterDelay(customerID, w)

	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true // 正常等完
	case <-w.ch:
		log.Printf("[延迟清零] 客户%d 延迟被取消，立即回复", customerID)
		return false // 被取消
	}
}

// CancelDelay 取消指定客户的延迟，使其立即回复（向该客户所有等待通道各发一次信号）
func CancelDelay(customerID uint) {
	delayMu.Lock()
	set := delayCancel[customerID]
	chans := make([]chan struct{}, 0, len(set))
	for w := range set {
		chans = append(chans, w.ch)
	}
	delayMu.Unlock()
	if len(chans) == 0 {
		log.Printf("[延迟清零] 客户%d 当前无活跃延迟", customerID)
		return
	}
	for _, ch := range chans {
		select {
		case ch <- struct{}{}:
		default: // 已排队取消，跳过
		}
	}
	log.Printf("[延迟清零] 客户%d 取消信号已发送(%d 个等待)", customerID, len(chans))
}

// GetConversationMutex 获取客户级别的会话创建互斥锁
// 同一客户共享一把锁，不同客户互不阻塞
func GetConversationMutex(customerID uint) *sync.Mutex {
	return &conversationMu[customerID%convMuShards]
}
