// 消息合并队列：25s 滑动窗口/batchID 防跨批/processing 锁 90s 自愈，多实例 Redis 协调。
package service

import (
	"ai-scrm/config"
	"ai-scrm/internal/redisclient"
	"fmt"
	"log"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ============================================================
// 消息合并队列服务
// 为什么需要？客户经常连发多条消息（一句话拆开说/连续问多个问题）
// 设计思路：
//   1. 每条消息先入队
//   2. 第一个拿到处理权的请求负责"等合并窗口+生成回复"
//   3. 后面的请求挂起等待，回复生成后一起返回
//   4. 最多合并3条，超过的走积压队列，依次处理
//
// 多实例改造（Phase M）：
//   - 队列键改为 tenantID:customerID 复合键（租户隔离）
//   - REDIS_ENABLED=true 时跨实例协调：
//       处理者 = Redis 分布式锁持有者（SETNX PX + 看门狗续期，进程死亡锁自动过期=自愈）
//       非处理者实例的消息经 Redis 待合并列表转交给处理者并入当前批次
//       回复以 seq 协议发布（lastseq/reply:{seq}），远程等待者轮询获取
//   - REDIS_ENABLED=false 时纯内存模式，行为与单机版完全一致
//   - 延迟铁律不变：合并25s / 简单8s / AI≥15s / 2min硬顶
// ============================================================

// PendingMessage 待处理消息
type PendingMessage struct {
	Content    string    // 消息内容
	ReceivedAt time.Time // 接收时间
	BatchID    uint64    // 批次ID，区分不同合并批次，防止跨批消息混合
}

// CustomerQueue 单客户消息队列
type CustomerQueue struct {
	mu                  sync.Mutex
	cond                *sync.Cond
	pending             []PendingMessage        // 待合并消息
	processed           int                     // 已处理的消息数（积压队列的偏移）
	processing          bool                    // 是否正在处理中
	lastReply           string                  // 最近一次生成的回复（用于后续请求直接取）
	lastReplyAt         time.Time               // 最近回复时间（判断是否是本次合并的回复）
	mergeCount          int                     // 当前合并批次已合并几条
	simpleProcessing    bool                    // 简单消息是否正在处理（H7：实例内同客户串行，防并发乱序/重复回复）
	deadlineExpired     bool                    // 合并窗口到期标记（由AfterFunc定时器设置，waitForMerge检查后清除）
	currentBatch        uint64                  // 当前批次ID，每次新批次递增，防止跨批次消息混合
	processingStartedAt time.Time               // 处理开始时间，用于2分钟超时自愈检测
	redisLock           *redisclient.LockHandle // 跨实例分布式锁句柄（Redis模式处理者持有）
}

// getProcessingLockTimeout 获取processing锁超时时间
// 修复 C5：改为从后台配置读取(processing_lock_timeout)，无需发版即可调节
// 根因：前一个请求卡死时，新请求不会无限等待
// 默认600秒——正常处理在最坏情况下（双供应商全失败重试 + 25s合并 + 75s模拟延迟）
// 可能达到 ~5 分钟，原默认值 90s 会误触发自愈，把正常在途批次当成卡死清空。
// 上调到 600s 后，自愈仅在 goroutine 真正死掉（如连接断开但 sleep 未结束）时触发，
// 触发后清空残留消息是安全回收（死 goroutine 不会再处理它们），避免重复/乱序回复。
func getProcessingLockTimeout(tenantID uint) time.Duration {
	sec := DefaultSystemConfigService.GetIntForTenant(tenantID, "processing_lock_timeout", 600)
	return time.Duration(sec) * time.Second
}

// tidFromKey 从复合键 "tenantID:customerID" 解析租户ID（队列内部便捷方法）
func tidFromKey(k string) uint {
	idx := strings.Index(k, ":")
	if idx <= 0 {
		return 0
	}
	n, _ := strconv.ParseUint(k[:idx], 10, 64)
	return uint(n)
}

// MessageQueueService 消息队列服务
type MessageQueueService struct {
	queues map[string]*CustomerQueue // "tenantID:customerID" → 队列
	mu     sync.Mutex
}

// DefaultMessageQueueService 全局实例
var DefaultMessageQueueService = NewMessageQueueService()

// ActiveQueueCount 当前 processing 中的队列数（/status 观测用，商业化 M4）
func (s *MessageQueueService) ActiveQueueCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, q := range s.queues {
		q.mu.Lock()
		if q.processing {
			n++
		}
		q.mu.Unlock()
	}
	return n
}

// SimpleMessageDone 简单消息处理完毕，释放同客户串行锁（H7）
func (s *MessageQueueService) SimpleMessageDone(tenantID uint, customerID uint) {
	k := queueKey(tenantID, customerID)
	q := s.getQueue(k)
	q.mu.Lock()
	q.simpleProcessing = false
	q.cond.Signal()
	q.mu.Unlock()
}

// NewMessageQueueService 创建服务
func NewMessageQueueService() *MessageQueueService {
	return &MessageQueueService{
		queues: make(map[string]*CustomerQueue),
	}
}

// queueKey 租户复合键（多实例+多租户隔离）
func queueKey(tenantID, customerID uint) string {
	return strconv.FormatUint(uint64(tenantID), 10) + ":" + strconv.FormatUint(uint64(customerID), 10)
}

// getQueue 获取客户队列，不存在则创建
func (s *MessageQueueService) getQueue(key string) *CustomerQueue {
	s.mu.Lock()
	defer s.mu.Unlock()

	q, exists := s.queues[key]
	if !exists {
		q = &CustomerQueue{
			pending: make([]PendingMessage, 0),
		}
		q.cond = sync.NewCond(&q.mu)
		s.queues[key] = q
	}
	return q
}

// EnqueueAndWait 消息入队并等待回复
// 返回值：mergedContent 合并后的消息内容, shouldProcess 是否由本请求负责生成回复,
//
//	reply 如果是后续请求直接取回复则有值, mergeWaitDuration 合并窗口实际等待时长（供AI延迟偏移使用）
//
// 四种情况：
//  1. 简单消息（"在吗"/"那我撤了"等）→ 跳过合并，shouldProcess=true, isSimple=true，调用方走快速回复
//  2. 第一个拿到处理权 → shouldProcess=true, 调用方生成回复后调用 SetReply()，mergeWaitDuration有值
//  3. 合并窗口内的后续消息 → shouldProcess=false, 立刻返回合并状态（Bug1修复后不再等AI回复）
//  4. 超过合并上限的积压消息 → 等前面处理完，自己成为下一批的第一个
func (s *MessageQueueService) EnqueueAndWait(tenantID uint, customerID uint, content string) (mergedContent string, shouldProcess bool, reply string, mergeWaitDuration time.Duration, isSimple bool, mergeCount int) {
	k := queueKey(tenantID, customerID)

	// 简单消息快速通道（不合并、不等窗口；跨实例也不需要协调——它本来就不进批次）
	if IsSimpleMessage(content) {
		// H7修复(2026-08-26)：实例内同客户简单消息串行，防止并发乱序/重复回复
		q := s.getQueue(k)
		q.mu.Lock()
		for q.simpleProcessing {
			q.cond.Wait()
		}
		q.simpleProcessing = true
		q.mu.Unlock()
		log.Printf("[合并队列] 客户%s 简单消息快速通道: %q", k, content)
		return content, true, "", 0, true, 1
	}

	// 多实例模式：Redis 分布式锁裁决谁做处理者
	if redisclient.IsEnabled() {
		if h := redisclient.TryLock("mq:lock:"+k, getProcessingLockTimeout(tidFromKey(k))); h != nil {
			// 本实例拿到处理权
			q := s.getQueue(k)
			q.mu.Lock()
			if q.redisLock != nil {
				q.redisLock.Unlock() // 防御：释放残留的旧句柄
			}
			q.redisLock = h
			q.mu.Unlock()
			// 锁空闲期间其他实例转交的消息先收进当前批次
			s.absorbRemotePending(k, q)
			return s.processLocally(k, content, true)
		}
		// 其他实例正在处理该客户：消息转交对方合并，本请求远程等回复
		return s.waitRemotely(tenantID, customerID, content)
	}

	// 单实例内存模式（REDIS_ENABLED=false）
	return s.processLocally(k, content, true)
}

// processLocally 本地处理路径（原有单机逻辑，含超时自愈/批次/积压）
// appendOwn=false 用于"接管"场景：本消息已在 Redis 待合并列表里，由 absorb 收回，避免重复
func (s *MessageQueueService) processLocally(k string, content string, appendOwn bool) (mergedContent string, shouldProcess bool, reply string, mergeWaitDuration time.Duration, isSimple bool, mergeCount int) {
	q := s.getQueue(k)

	q.mu.Lock()

	// 修复：processing锁超时自愈
	// 根因：用户11:13发消息，11:22才收到回复（9分钟），远超2分钟硬顶
	// 如果前一个请求的goroutine卡死（Cloudflare连接断开但sleep未结束），
	// processing锁会一直被持有，新请求无限等待
	// 超过90秒强制释放+清除残留消息，新请求接管
	if q.processing && !q.processingStartedAt.IsZero() && time.Since(q.processingStartedAt) > getProcessingLockTimeout(tidFromKey(k)) {
		log.Printf("[合并队列] 客户%s processing锁超时(%.1fs)，强制释放，清除残留消息",
			k, time.Since(q.processingStartedAt).Seconds())
		q.processing = false
		q.lastReply = ""
		q.deadlineExpired = false
		if q.redisLock != nil {
			q.redisLock.Unlock() // 过期锁句柄一并释放
			q.redisLock = nil
		}
		// 清除超时批次的残留消息（它们的batchID与下批不同，不会被取走）
		if len(q.pending) > 0 {
			log.Printf("[合并队列] 客户%s 清除%d条超时批次残留消息", k, len(q.pending))
			q.pending = q.pending[:0]
		}
		q.cond.Broadcast()
	}

	var msgIdx = -1
	if appendOwn {
		// 消息入队（带批次ID）
		msg := PendingMessage{
			Content:    content,
			ReceivedAt: time.Now(),
		}
		q.pending = append(q.pending, msg)
		msgIdx = len(q.pending) - 1 // 记录消息索引，用于后续设置BatchID
	}

	// 如果没在处理中，本请求拿处理权
	if !q.processing {
		q.processing = true
		q.currentBatch++                   // 新批次，递增batchID
		q.processingStartedAt = time.Now() // 记录处理开始时间，用于超时检测
		q.mergeCount = 1
		q.lastReply = ""
		q.deadlineExpired = false
		if msgIdx >= 0 {
			q.pending[msgIdx].BatchID = q.currentBatch // 标记消息所属批次
		}
		q.mu.Unlock()

		log.Printf("[合并队列] 客户%s 拿到处理权(批次%d)，开始合并窗口等待: %q", k, q.currentBatch, content)

		// 事件驱动合并等待（滑动窗口），返回合并内容+实际等待时长
		merged, waitDuration := s.waitForMerge(q, k)
		log.Printf("[合并队列] 客户%s 合并完成(批次%d): %d条消息, 等待%.1fs, 内容: %q", k, q.currentBatch, q.mergeCount, waitDuration.Seconds(), merged)
		return merged, true, "", waitDuration, false, q.mergeCount
	}

	// 已经在处理中了，检查是否还能合并进当前批次
	if q.mergeCount < config.GlobalConfig.ReplySpeed.MaxMergeMessages {
		q.mergeCount++
		if msgIdx >= 0 {
			q.pending[msgIdx].BatchID = q.currentBatch // 标记消息所属批次
		}
		// 立刻通知主请求：新消息到达（事件驱动，替代定时轮询）
		q.cond.Signal()

		log.Printf("[合并队列] 客户%s 消息合并进当前批次(第%d条,批次%d): %q", k, q.mergeCount, q.currentBatch, content)

		// 挂起等待回复
		for q.processing && q.lastReply == "" {
			q.cond.Wait()
		}
		reply = q.lastReply
		q.mu.Unlock()
		return "", false, reply, 0, false, q.mergeCount
	}

	// 超过合并上限，积压队列——这是下一批的第一个
	// 先挂起等待当前批次完成
	for q.processing {
		q.cond.Wait()
	}

	// 当前批次完成了，本消息成为下一批的第一个
	q.processing = true
	q.currentBatch++ // 新批次
	q.processingStartedAt = time.Now()
	q.mergeCount = 1
	q.lastReply = ""
	q.deadlineExpired = false
	if msgIdx >= 0 {
		q.pending[msgIdx].BatchID = q.currentBatch
	}
	q.mu.Unlock()

	log.Printf("[合并队列] 客户%s 积压消息拿到处理权(批次%d): %q", k, q.currentBatch, content)
	merged, waitDuration := s.waitForMerge(q, k)
	return merged, true, "", waitDuration, false, q.mergeCount
}

// waitRemotely 远程等待路径（本实例未抢到锁，消息转交处理者实例）
// 协议：
//  1. LPUSH 消息到 mq:pending:{k}，处理者会在窗口内/窗口关闭时吸收进当前批次
//  2. 轮询 mq:lastseq:{k}，序号增长后读 mq:reply:{k}:{seq} 取回复
//  3. 锁消失（持有实例死亡）→ 尝试接管成为新处理者
//
// 已知边界：若消息到达时批次已满被"退回"，会随下一批处理，但本请求可能拿到
// 上一批的回复体（前端聊天记录为准，影响极小）；单实例模式无此问题
func (s *MessageQueueService) waitRemotely(tenantID uint, customerID uint, content string) (mergedContent string, shouldProcess bool, reply string, mergeWaitDuration time.Duration, isSimple bool, mergeCount int) {
	k := queueKey(tenantID, customerID)
	lockKey := "mq:lock:" + k

	// 记录当前已发布回复的最大序号：只认"之后"的新回复，防止读到上一批旧回复
	curSeq := int64(0)
	if v, ok := redisclient.Get("mq:lastseq:" + k); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			curSeq = n
		}
	}

	// 消息转交处理者实例
	redisclient.LPush("mq:pending:"+k, content)
	log.Printf("[合并队列] 客户%s 消息转交其他实例处理: %q", k, content)

	deadline := time.Now().Add(getProcessingLockTimeout(tidFromKey(k)))
	for time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)

		// 新回复发布？
		if v, ok := redisclient.Get("mq:lastseq:" + k); ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > curSeq {
				if r, ok := redisclient.Get(fmt.Sprintf("mq:reply:%s:%d", k, n)); ok && r != "" {
					log.Printf("[合并队列] 客户%s 远程回复已取到(序号%d)", k, n)
					return "", false, r, 0, false, 1
				}
			}
		}

		// 锁消失且尚无新回复 → 原持有实例死亡，跳出循环去接管
		if !redisclient.LockExists(lockKey) {
			log.Printf("[合并队列] 客户%s 检测到处理者失联，尝试接管", k)
			break
		}
	}

	// 接管：拿到锁则成为新处理者（自己的消息已在待合并列表，appendOwn=false 防重复）
	if h := redisclient.TryLock(lockKey, getProcessingLockTimeout(tidFromKey(k))); h != nil {
		q := s.getQueue(k)
		q.mu.Lock()
		q.redisLock = h
		q.mu.Unlock()
		s.absorbRemotePending(k, q)
		return s.processLocally(k, content, false)
	}

	// 极端场景：锁被别的实例抢先拿走但仍无回复——本地兜底处理（对齐旧版强制自愈语义）
	log.Printf("[合并队列] 客户%s 远程等待超时且拿锁失败，本地兜底处理", k)
	return s.processLocally(k, content, true)
}

// absorbRemotePending 吸收其他实例经 Redis 转交来的消息（原子取出，防丢）
// 调用时机：拿到处理权后（窗口开始前）、合并窗口关闭收集批次前
// 超出合并上限的消息放回 Redis 列表，留给下一批（可能由其他实例处理）
func (s *MessageQueueService) absorbRemotePending(k string, q *CustomerQueue) {
	items := redisclient.DrainList("mq:pending:" + k)
	if len(items) == 0 {
		return
	}
	maxMerge := config.GlobalConfig.ReplySpeed.MaxMergeMessages

	q.mu.Lock()
	batch := q.currentBatch
	absorbed := 0
	var leftover []string
	for _, content := range items {
		// 未在处理中或已达上限：放回列表留给下一批
		if !q.processing || q.mergeCount >= maxMerge {
			leftover = append(leftover, content)
			continue
		}
		q.mergeCount++
		q.pending = append(q.pending, PendingMessage{
			Content:    content,
			ReceivedAt: time.Now(),
			BatchID:    batch,
		})
		absorbed++
	}
	q.cond.Broadcast() // 唤醒主等待循环重新评估（新消息到达语义）
	q.mu.Unlock()

	// 放回超额部分
	for _, c := range leftover {
		redisclient.LPush("mq:pending:"+k, c)
	}
	if absorbed > 0 {
		log.Printf("[合并队列] 客户%s 吸收其他实例转交消息 %d 条(批次%d)", k, absorbed, batch)
	}
}

// waitForMerge 事件驱动的合并等待（滑动窗口 + 条件变量信号）
//
// 对齐用户需求：
//  1. 第一条消息到达 → 开启45秒合并窗口
//  2. 45秒内没有其他消息 → 生成回复，延迟偏移里减去实际等待时间
//  3. 45秒内有其他消息 → 重置45秒窗口（滑动窗口），继续等待
//     - 新窗口内有消息 → 合并并生成回复，延迟偏移减去实际等待时间
//     - 新窗口内没消息 → 生成回复，延迟偏移减去实际等待时间
//  4. 达到3条上限 → 立刻退出窗口，直接生成回复，延迟偏移减去实际等待时间
//  5. 前端无感知，一律体现为"顾问输入中..."状态
//
// 实现方式：用 sync.Cond + AfterFunc 定时器替代定时轮询
//   - 新消息入队时 cond.Signal() 立刻唤醒主请求（事件驱动，不再9秒轮询）
//   - 定时器到期时设置 deadlineExpired 标记 + cond.Broadcast() 唤醒
//   - 每次被新消息唤醒后 Reset 定时器（滑动窗口：新消息重置45秒deadline）
//   - 返回实际等待时长 mergeWaitDuration，供AI延迟偏移使用
//     （AI延迟 = 基础延迟 - 合并等待时间，最小为0，不会叠加）
func (s *MessageQueueService) waitForMerge(q *CustomerQueue, k string) (mergedContent string, mergeWaitDuration time.Duration) {
	// 修复：合并窗口从25秒合并窗口，fallback值同步更新
	// 用户明确要求：客户连发消息时，30秒滑动窗口合并，最多3条
	mergeWindow := time.Duration(DefaultSystemConfigService.GetIntForTenant(tidFromKey(k), "merge_window_seconds", 25)) * time.Second
	maxMerge := config.GlobalConfig.ReplySpeed.MaxMergeMessages

	// 记录合并等待起始时间，用于计算延迟偏移
	startTime := time.Now()

	// 定时器：合并窗口到期时设置标记并广播唤醒主请求
	// 每次收到新消息后 Reset 定时器（滑动窗口：新消息重置45秒deadline）
	deadlineTimer := time.AfterFunc(mergeWindow, func() {
		q.mu.Lock()
		q.deadlineExpired = true
		q.mu.Unlock()
		q.cond.Broadcast()
	})
	defer deadlineTimer.Stop()

	q.mu.Lock()
	lastCheckedCount := q.mergeCount // 跟踪上次检查时的合并数，用于判断是否有新消息

	for {
		// 终止条件1：达到最大合并数 → 立刻退出，不等剩余窗口时间
		if q.mergeCount >= maxMerge {
			q.mu.Unlock()
			break
		}

		// 终止条件2：窗口到期且没有新消息 → 处理当前批次
		if q.deadlineExpired && q.mergeCount <= lastCheckedCount {
			q.deadlineExpired = false
			q.mu.Unlock()
			break
		}

		// 新消息到达（mergeCount > lastCheckedCount）→ 清除到期标记 + 重置定时器（滑动窗口）
		q.deadlineExpired = false
		lastCheckedCount = q.mergeCount
		deadlineTimer.Reset(mergeWindow) // 新消息重置45秒deadline

		// 等待事件：新消息（cond.Signal）或窗口到期（cond.Broadcast）
		q.cond.Wait() // 释放锁，挂起；唤醒后自动重新持有锁
	}

	// 计算实际合并等待时长（供AI延迟偏移使用）
	mergeWaitDuration = time.Since(startTime)

	// 多实例：窗口关闭时吸收其他实例转交来的消息
	// 它们不再重置窗口（避免无限拖延），直接并入当前批次一起回复
	if redisclient.IsEnabled() {
		s.absorbRemotePending(k, q)
	}

	// 合并窗口结束，收集当前批次的所有消息
	// 修复：按批次ID收集消息，防止跨批次消息混合
	// 根因：用户发"试驾"但合并结果变成了之前的"高数题"——pending队列没有批次边界，
	// waitForMerge按序取走了上一批残留的旧消息，当前消息被跳过
	q.mu.Lock()
	batchID := q.currentBatch
	var mergedBuilder []string
	var remaining []PendingMessage
	for _, msg := range q.pending {
		if msg.BatchID == batchID {
			mergedBuilder = append(mergedBuilder, msg.Content)
		} else {
			remaining = append(remaining, msg)
		}
	}
	q.pending = remaining
	q.mu.Unlock()

	// 用换行连接多条消息（AI能看出来是连发的）
	merged := ""
	for i, c := range mergedBuilder {
		if i > 0 {
			merged += "\n"
		}
		merged += c
	}

	return merged, mergeWaitDuration
}

// SetReply 设置回复结果，并唤醒所有等待的请求
// 多实例：同时以 seq 协议发布到 Redis（其他实例的等待者轮询获取），最后释放分布式锁
func (s *MessageQueueService) SetReply(tenantID uint, customerID uint, reply string) {
	k := queueKey(tenantID, customerID)
	q := s.getQueue(k)

	q.mu.Lock()
	h := q.redisLock
	q.redisLock = nil
	q.lastReply = reply
	q.lastReplyAt = time.Now()
	q.processing = false
	log.Printf("[合并队列] 客户%s 回复已设置, 唤醒所有等待者, 回复前20字: %q", k, truncateStr(reply, 20))
	// 唤醒所有等待的goroutine
	q.cond.Broadcast()
	q.mu.Unlock()

	// 多实例：先发布回复再放锁——保证远程等待者拿到锁消失信号时回复已可见
	if redisclient.IsEnabled() {
		seq := redisclient.Incr("mq:seq:" + k)
		redisclient.SetEx("mq:lastseq:"+k, strconv.FormatInt(seq, 10), 10*time.Minute)
		redisclient.SetEx(fmt.Sprintf("mq:reply:%s:%d", k, seq), reply, 5*time.Minute)
		if h != nil {
			h.Unlock()
		}
	}
}

// ============================================================
// 简单消息检测
// 短消息+关键词匹配，这类消息直接快速回复，不走合并窗口和AI大模型
// 业务规则：简单问题不需要AI思考，直接预设话术，45秒内返回
// 同时避免简单消息被合并窗口延迟，导致"回复永远是上一句"
// ============================================================

// simpleKeywords 简单消息关键词列表
var simpleKeywords = []string{
	"在吗", "在不在", "你好", "您好", "嗨", "hi", "hello",
	"好的", "好", "嗯", "哦", "行", "可以", "没问题",
	"那我撤了", "撤了", "先这样", "拜拜", "再见", "88",
	"谢谢", "感谢", "收到", "明白", "了解",
	"还在吗", "还在不", "人呢", "还在吗",
}

// IsSimpleMessage 判断是否为简单消息
// 规则：≤8个字 + 匹配关键词列表（去空格后精确匹配或包含匹配）
// 修复问题5：含车相关关键词的消息即使≤8字也不算简单消息，必须走AI
// 注意：使用已有的carRelatedKeywords白名单（与IsOffTopic共用）
func IsSimpleMessage(content string) bool {
	trimmed := strings.TrimSpace(content)

	// 修复问题5：含车相关关键词→不是简单消息，必须走AI正常回复
	// carRelatedKeywords已在IsOffTopic上方定义，此处复用
	for _, kw := range carRelatedKeywords {
		if strings.Contains(strings.ToLower(trimmed), strings.ToLower(kw)) {
			return false // 含车相关词→走AI，不走简单通道
		}
	}

	// 超过8个字不算简单消息
	if len([]rune(trimmed)) > 8 {
		return false
	}
	// 匹配关键词
	for _, kw := range simpleKeywords {
		if trimmed == kw || strings.Contains(trimmed, kw) {
			return true
		}
	}
	return false
}

// GetSimpleReplyDelay 简单消息的延迟时间
// 修复：用户明确要求简单消息快速通道固定8秒延迟
// 修复：改为从后台配置读取(simple_msg_delay)，无需发版即可调节
// 不看工作时间，不分工作日/周末，统一固定值——简单消息的核心是"快速接住"
// 简单消息（"你好"/"在吗"等）先独立回复，不跟正式消息混排队
func GetSimpleReplyDelay() time.Duration {
	sec := DefaultSystemConfigService.GetInt("simple_msg_delay", 8)
	return time.Duration(sec) * time.Second
}

// CalcHumanlikeDelay 计算AI回复的模拟真人延迟
// 公式：总回复时长 = 打字时长(40字/分) - 合并等待抵扣 + 线下偏移
//
//	打字时长：回复字数 / 40字每分钟
//	合并等待抵扣：合并了N条消息时，扣 mergeWindow*N（客户已经等了）
//	线下偏移（工作时间）：30s(简单) / 60s(中等) / 90s(复杂)
//	线下偏移（非工作时间）：60s(简单) / 120s(中等) / 180s(复杂)
//	到店倾向客户：线下偏移=0（客户要来店了，顾问得秒回，只算打字速度）
//	非工作时间：18:00-次日9:00及周末
func CalcHumanlikeDelay(tenantID uint, replyText string, mergeWaitDuration time.Duration, mergeCount int, isStoreVisit bool) time.Duration {
	// 修复：模拟模式(开发调试)跳过真人延迟。否则 mock 下仍会 sleep 满 max_reply_delay(75s)
	// 叠加 25s 合并窗口超过客户端 60s 超时，开发联调时表现为"对话挂死"。
	// AI_MOCK_MODE=true 是开发调试信号，延迟无意义，直接返回 0。
	if config.GlobalConfig.AI.MockMode {
		log.Printf("[模拟延迟] 模拟模式开启，跳过真人延迟直接返回(0s)")
		return 0
	}

	// 1. 打字时长：回复字数 / 40字每分钟
	runeCount := len([]rune(replyText))
	if runeCount <= 0 {
		runeCount = 1
	}
	typingMinutes := float64(runeCount) / 40.0
	typingDelay := time.Duration(typingMinutes * 60.0 * float64(time.Second))

	// 2. 判断工作/非工作时间
	now := time.Now()
	hour := now.Hour()
	isWeekend := now.Weekday() == time.Saturday || now.Weekday() == time.Sunday
	isOffWork := isWeekend || hour < 9 || hour >= 18

	// 3. 线下工作偏移：模拟顾问在忙其他事
	// 修复：线下偏移6个值改为从后台配置读取，无需发版即可调节
	// 到店倾向客户：线下偏移=0，顾问必须快速响应来店客户
	var offlineOffset time.Duration
	if isStoreVisit {
		// 到店倾向：去掉线下偏移，只算打字速度-合并抵扣
		// 业务逻辑：客户说"想去看看""想试驾"，顾问必须快速响应
		offlineOffset = 0
	} else if runeCount < 20 {
		// 简单问题
		if isOffWork {
			offlineOffset = time.Duration(DefaultSystemConfigService.GetInt("offline_offset_offwork_simple", 60)) * time.Second
		} else {
			offlineOffset = time.Duration(DefaultSystemConfigService.GetInt("offline_offset_work_simple", 30)) * time.Second
		}
	} else if runeCount <= 80 {
		// 中等问题（绝大部分情况）
		if isOffWork {
			offlineOffset = time.Duration(DefaultSystemConfigService.GetInt("offline_offset_offwork_medium", 120)) * time.Second
		} else {
			offlineOffset = time.Duration(DefaultSystemConfigService.GetInt("offline_offset_work_medium", 60)) * time.Second
		}
	} else {
		// 复杂问题
		if isOffWork {
			offlineOffset = time.Duration(DefaultSystemConfigService.GetInt("offline_offset_offwork_complex", 180)) * time.Second
		} else {
			offlineOffset = time.Duration(DefaultSystemConfigService.GetInt("offline_offset_work_complex", 90)) * time.Second
		}
	}

	// 4. 合并等待抵扣：合并了N条消息时，客户已经等了 mergeWindow*N 秒
	// 修复：fallback值从8→25，与合并窗口配置同步
	var mergeCredit time.Duration
	if mergeCount > 1 {
		mergeWindow := time.Duration(DefaultSystemConfigService.GetIntForTenant(tenantID, "merge_window_seconds", 25)) * time.Second
		mergeCredit = mergeWindow * time.Duration(mergeCount)
	}

	// 5. 总延迟 = 打字 + 线下偏移 - 合并抵扣
	totalDelay := typingDelay + offlineOffset - mergeCredit
	if totalDelay < 0 {
		totalDelay = 0
	}

	// 6. AI回复最低延迟红线：15秒
	// 业务逻辑：AI回复不能秒到，必须模拟真人思考+打字时间
	// 即便合并抵扣把总延迟扣到0，正式AI回复也至少等15秒
	// 简单消息走独立快速通道(8秒)，不走这个函数
	minDelay := time.Duration(DefaultSystemConfigService.GetInt("reply_min_delay", 15)) * time.Second
	if totalDelay < minDelay {
		totalDelay = minDelay
	}

	// 7. AI回复最大延迟硬顶：75秒（后台可调）
	// 修复：总回复时长不能超过2分钟。合并窗口25秒 + AI调用~10秒 + 模拟延迟 ≤ 120秒
	// 所以模拟延迟硬顶75秒，确保：25 + 10 + 75 = 110秒 ≤ 2分钟
	// 后台调max_reply_delay即时生效，不需要改代码发版
	maxDelay := time.Duration(DefaultSystemConfigService.GetInt("max_reply_delay", 75)) * time.Second
	if totalDelay > maxDelay {
		log.Printf("[模拟延迟] 硬顶触发: 原延迟%.1fs > 硬顶%.1fs, 截断", totalDelay.Seconds(), maxDelay.Seconds())
		totalDelay = maxDelay
	}

	log.Printf("[模拟延迟] 字数=%d, 打字=%.1fs, 线下偏移=%.1fs(工作=%v,到店=%v), 合并抵扣=%.1fs(合并%d条), 最低红线=%.1fs, 硬顶=%.1fs, 总延迟=%.1fs",
		runeCount, typingDelay.Seconds(), offlineOffset.Seconds(), !isOffWork, isStoreVisit, mergeCredit.Seconds(), mergeCount, minDelay.Seconds(), maxDelay.Seconds(), totalDelay.Seconds())

	return totalDelay
}

// GetSimpleReply 简单消息的预设快速回复
// 根据消息内容返回合适的预设话术，不走AI大模型
func GetSimpleReply(content string) string {
	trimmed := strings.TrimSpace(content)

	// 打招呼类
	if trimmed == "你好" || trimmed == "您好" || trimmed == "嗨" ||
		strings.EqualFold(trimmed, "hi") || strings.EqualFold(trimmed, "hello") {
		return "你好呀，有啥想了解的？"
	}

	// 确认类
	if trimmed == "好的" || trimmed == "好" || trimmed == "嗯" || trimmed == "哦" ||
		trimmed == "行" || trimmed == "可以" || trimmed == "没问题" ||
		trimmed == "收到" || trimmed == "明白" || trimmed == "了解" {
		return "👌"
	}

	// 在线确认类
	// 修复问题5：改"在的，你说"为更自然的回复，避免触及快速回复策略时显得敷衍
	if trimmed == "在吗" || trimmed == "在不在" || trimmed == "还在吗" ||
		trimmed == "还在不" || trimmed == "人呢" {
		return "在呢，你想了解啥？"
	}

	// 离开类
	if trimmed == "那我撤了" || trimmed == "撤了" || trimmed == "先这样" ||
		trimmed == "拜拜" || trimmed == "再见" || trimmed == "88" {
		return "好嘞，有问题随时找我"
	}

	// 感谢类
	if trimmed == "谢谢" || trimmed == "感谢" {
		return "不客气~"
	}

	// 兜底
	return "在的，你说"
}

// truncateStr 截断字符串到指定rune长度（日志用，避免超长回复刷屏）
func truncateStr(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

// ============================================================
// 话题硬边界拦截（代码层硬约束，不靠提示词软约束）
// 为什么需要？只靠提示词告诉AI"不要聊无关话题"不够，
// AI可能还是会回答算法题、火箭发射等无关内容
// 硬边界 = 代码层拦截，命中关键词直接返回引导话术，不走AI
// ============================================================

// offTopicKeywords 无关话题关键词黑名单
// 命中任何一个 = 判定为无关话题，直接拦截
// 设计原则：宁可漏判（AI靠提示词兜底），不可误判（不能把聊车的话题拦了）
var offTopicKeywords = []string{
	// 编程/技术无关
	"算法题", "leetcode", "力扣", "写代码", "编程题", "代码实现",
	"python", "java", "javascript", "golang", "c++",
	"git", "docker", "k8s", "linux命令",
	// 学术/学科无关（数学/物理/化学等课本内容，跟买车无关）
	"高数", "微积分", "线性代数", "概率论", "数学题", "解方程",
	"物理题", "化学题", "考研", "考公", "四级", "六级", "雅思",
	"托福", "gre", "gmat", "作业", "考试题", "期末考试",
	// 学术/科普无关
	"火箭发射", "航天", "太空探索", "量子计算", "核聚变",
	"相对论", "量子力学", "黑洞",
	// 金融无关
	"股票量化", "量化交易", "期货策略", "期权定价",
	"对冲基金", "高频交易", "k线图",
	// 政治敏感
	"总统选举", "政治立场", "意识形态",
	// 娱乐无关（明确不聊的）
	"明星八卦", "综艺", "选秀",
	// 明确要AI做非销售的事
	"帮我写", "帮我生成", "帮我翻译", "帮我算", "帮我做",
	"帮我解", "求解", "解个题", "算一下",
}

// carRelatedKeywords 车相关关键词白名单
// 如果消息同时命中黑名单和白名单，白名单优先（不拦截）
// 比如客户说"帮我写个试驾报告"→命中"帮我写"但含"试驾"→放行
var carRelatedKeywords = []string{
	"车", "驾", "SUV", "越野", "四驱", "底盘", "动力",
	"续航", "充电", "电池", "油", "油耗", "油耗",
	"品牌", "车型", "配置", "选配", "内饰", "外观",
	"价格", "优惠", "首付", "贷款", "金融", "保险",
	"试驾", "到店", "看车", "提车", "订车", "交付",
	"保养", "维修", "质保", "售后",
	"极石", "坦克", "方程豹", "哈弗", "比亚迪",
	"空间", "安全", "智能", "辅助驾驶", "L2", "自动泊车",
	"竞品", "对比", "评测", "口碑",
	"家用", "通勤", "自驾", "露营", "旅行",
}

// IsOffTopic 检测用户输入是否为无关话题
// 返回：true=无关话题应拦截，false=正常话题放行
// 逻辑：先查白名单（车相关→放行），再查黑名单（无关→拦截）
// 泛行业化：委托行业语义读取层（系统默认层），行业包可配置白/黑名单
func IsOffTopic(content string) bool {
	return IsOffTopicForTenant(0, content)
}

// GetOffTopicReply 返回无关话题的硬拦截引导话术
// 不走AI，0延迟，直接返回预设话术
// 话术设计原则：自然引导回车，不生硬拒绝，不暴露拦截机制
// 泛行业化：委托行业语义读取层（系统默认层），行业包可配置话术集合
func GetOffTopicReply(content string) string {
	return GetOffTopicReplyForTenant(0, content)
}

// ============================================================
// 到店倾向两段式快速回复
// 为什么需要？客户说"试驾""到店"等高意向信号，必须快速接住
// 设计思路：
//   1. 第一段（10-15秒）：快速接住，确认试驾意向，类似简单消息
//   2. 第二段（25-45秒）：追问预约信息（姓名/电话/人数/时间/需求）
//   3. 前端收到后先显示第一段，延时后自动显示第二段（follow_up机制）
// ============================================================

// storeVisitFirstReplies 到店倾向第一段回复模板
// 核心目标：快速接住意向，让客户感受到顾问响应积极
var storeVisitFirstReplies = []string{
	"好的，那我给您约个到店试驾体验吧",
	"好嘞，我帮您安排个试驾体验",
	"没问题，给您约个到店试驾吧",
	"可以呀，试驾体验安排起来",
}

// storeVisitSecondReplies 到店倾向第二段追问模板
// 核心字段：姓名、电话、预计人数、试驾时间、其他需求——不能丢
var storeVisitSecondReplies = []string{
	"麻烦您发一下姓名和电话，预计几位过来，方便的试驾时间，还有其他想了解的也一块说下，我来给您预约",
	"您方便留个姓名和手机号吗？另外大概几位过来、啥时候方便试驾，有啥特别想了解的也告诉我，我给您安排好",
	"帮我留个姓名电话呗，还有大概几个人来、想啥时候试驾，有啥特别需求也一起说，我这边给您约好",
	"您发我一下姓名和联系方式吧，还有几个人来、方便的时间，以及其他想了解的，我来安排预约",
}

// GetStoreVisitFirstReply 获取到店倾向第一段回复（快速接住意向）
// 修复：到店倾向客户不走合并队列，直接走快速通道
func GetStoreVisitFirstReply(content string) string {
	idx := rand.Intn(len(storeVisitFirstReplies))
	return storeVisitFirstReplies[idx]
}

// GetStoreVisitSecondReply 获取到店倾向第二段追问（预约信息收集）
// 核心字段：姓名、电话、预计人数、试驾时间、其他需求
func GetStoreVisitSecondReply(content string) string {
	idx := rand.Intn(len(storeVisitSecondReplies))
	return storeVisitSecondReplies[idx]
}

// GetStoreVisitFirstDelay 到店倾向第一段回复延迟
// 修复：改为从后台配置读取(store_visit_first_delay)，[min,max]区间随机
// 到店倾向必须快速接住，第一段延迟默认10-15秒
// 比简单消息(8秒)略长——因为需要表现"确认安排"的思考感
func GetStoreVisitFirstDelay() time.Duration {
	// 秒回模式下到店快速通道零延迟
	mode := DefaultSystemConfigService.GetString("reply_delay_mode", "normal")
	if mode == "instant" {
		return 0
	}
	sec := DefaultSystemConfigService.GetInterval("store_visit_first_delay", 10, 15)
	return time.Duration(sec) * time.Second
}

// GetStoreVisitSecondDelay 到店倾向第二段追问延迟
// 修复：改为从后台配置读取(store_visit_second_delay)，[min,max]区间随机
// 第二段在第一段之后发出，模拟顾问"查档后追问"，默认25-45秒
func GetStoreVisitSecondDelay() time.Duration {
	// 秒回模式下到店快速通道零延迟
	mode := DefaultSystemConfigService.GetString("reply_delay_mode", "normal")
	if mode == "instant" {
		return 0
	}
	sec := DefaultSystemConfigService.GetInterval("store_visit_second_delay", 25, 45)
	return time.Duration(sec) * time.Second
}
