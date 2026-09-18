// 消息合并队列：25s 滑动窗口/batchID 防跨批/processing 锁 600s 自愈，多实例 Redis 协调。
package service

import (
	"ai-scrm/config"
	"ai-scrm/internal/db"
	"ai-scrm/internal/logx"
	"ai-scrm/internal/metrics"
	"ai-scrm/internal/model"
	"ai-scrm/internal/redisclient"
	"ai-scrm/internal/runtimecfg"
	"fmt"
	"log"
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
	mu               sync.Mutex
	cond             *sync.Cond
	pending          []PendingMessage // 待合并消息
	processed        int              // 已处理的消息数（积压队列的偏移）
	processing       bool             // 是否正在处理中
	lastReply        string           // 最近一次生成的回复（用于后续请求直接取）
	lastReplyAt      time.Time        // 最近回复时间（判断是否是本次合并的回复）
	mergeCount       int              // 当前合并批次已合并几条
	simpleProcessing bool             // 简单消息是否正在处理（H7：实例内同客户串行，防并发乱序/重复回复）
	deadlineExpired  bool             // 合并窗口到期标记（由AfterFunc定时器设置，waitForMerge检查后清除）
	currentBatch     uint64           // 当前批次ID，每次新批次递增，防止跨批次消息混合
	// batchClosed P0-7 修复(2026-09-15)：批次"关账"标志。waitForMerge 收集完本批消息后
	// 置 true——此后 AI 生成+延迟期间（10-135s，正是客户等回复补发消息的高发窗口）到达的
	// 消息不再标进已封账批次（旧行为：等待者拿到不含自己内容的旧回复，消息以旧 BatchID
	// 滞留 pending 成孤儿，仅靠 600s 超时自愈"清除残留"或直接随空闲清扫蒸发）。
	// 关账后的新消息走既有积压接管路径，作为下一批第一个被处理。
	batchClosed         bool
	epoch               uint64                  // P1-19：处理代际号，每次处理者接管递增；SetReply 校验代际防旧处理者践踏新批次
	processingStartedAt time.Time               // 处理开始时间，用于2分钟超时自愈检测
	redisLock           *redisclient.LockHandle // 跨实例分布式锁句柄（Redis模式处理者持有）
	lastActivity        time.Time               // 最近活跃时间（空闲队列回收依据，2026-09-09）
	// deliveredFor D5(2026-09-16)：批次投递认领表——key "epoch:channelID"。
	// 通道 worker 并入合并队列后，一个批次的唯一回复可能由 web 处理者生成、通道等待者送达，
	// 也可能处理者本身就是通道 worker——"每批每通道至多投递一次"必须原子裁决，
	// 否则微信侧双发/全漏。Redis 模式走 SetNX 跨实例认领（见 ClaimReplyDelivery）。
	deliveredFor map[string]bool
}

// getProcessingLockTimeout 获取processing锁超时时间
// 修复 C5：改为从后台配置读取(processing_lock_timeout)，无需发版即可调节
// 根因：前一个请求卡死时，新请求不会无限等待
// 默认600秒——正常处理在最坏情况下（双供应商全失败重试 + 25s合并 + 75s模拟延迟）
// 可能达到 ~5 分钟，原默认值 90s 会误触发自愈，把正常在途批次当成卡死清空。
// 上调到 600s 后，自愈仅在 goroutine 真正死掉（如连接断开但 sleep 未结束）时触发，
// 触发后清空残留消息是安全回收（死 goroutine 不会再处理它们），避免重复/乱序回复。
func getProcessingLockTimeout(tenantID uint) time.Duration {
	sec := runtimecfg.DefaultSystemConfigService.GetIntForTenant(tenantID, "processing_lock_timeout", 600)
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

// init 将队列观测口径注册给 metrics 包，保持 metrics -> service 零反向依赖。
func init() {
	metrics.SetQueueDepthProvider(func() int {
		if DefaultMessageQueueService == nil {
			return 0
		}
		return DefaultMessageQueueService.ActiveQueueCount()
	})
}

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
	// 2026-09-09：标记活跃（空闲回收依据），锁内更新无竞争
	q.mu.Lock()
	q.lastActivity = time.Now()
	q.mu.Unlock()
	return q
}

// CurrentEpoch D5 修复(2026-09-14)：读取客户队列当前处理代际（不接管处理权）。
// 离题/硬边界等"入队前直接回复"路径须携带本代际调 SetReply——旧实现传 epoch=0，
// 被 fencing 的 `epoch != 0` 短路放行：在途批次被错误唤醒（lastReply 覆盖成离题话术）、
// processing 锁提前释放（等待者二次接管，客户收到两条不相关回复）。
func (s *MessageQueueService) CurrentEpoch(tenantID, customerID uint) uint64 {
	q := s.getQueue(queueKey(tenantID, customerID))
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.epoch
}

// SweepIdleQueues 巡检删除长时间空闲的客户队列（2026-09-09 内存治理）。
// 背景：queues map 只增不删，长跑后内存随客户数单调增长。
// 规则：最近 idleTimeout 内无任何活跃 且 不在 processing/simpleProcessing 中 且 无积压待合并消息
// 的队列直接移除；正在处理的队列即便超时也保留（processing 锁 600s 自愈逻辑负责，不能误删）。
// 返回本次清理的队列数。由后台定时任务周期调用（如每 60s）。
func (s *MessageQueueService) SweepIdleQueues(idleTimeout time.Duration) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-idleTimeout)
	removed := 0
	for k, q := range s.queues {
		q.mu.Lock()
		active := q.processing || q.simpleProcessing || len(q.pending) > 0 || q.lastActivity.After(cutoff)
		q.mu.Unlock()
		if active {
			continue
		}
		delete(s.queues, k)
		removed++
	}
	if removed > 0 {
		log.Printf("[合并队列] 空闲队列回收 %d 个（空闲>%s），当前 %d 个", removed, idleTimeout, len(s.queues))
	}
	return removed
}

// EnqueueAndWait 消息入队并等待回复
// 返回值：mergedContent 合并后的消息内容, shouldProcess 是否由本请求负责生成回复,
//
//	reply 如果是后续请求直接取回复则有值, mergeWaitDuration 合并窗口实际等待时长（供AI延迟偏移使用）
//	epoch P1-19：本请求持有的处理代际号（shouldProcess=true 时有效，SetReply 须携带作 fencing 校验）
//
// 四种情况：
//  1. 简单消息（"在吗"/"那我撤了"等）→ 跳过合并，shouldProcess=true, isSimple=true，调用方走快速回复
//  2. 第一个拿到处理权 → shouldProcess=true, 调用方生成回复后调用 SetReply(epoch)，mergeWaitDuration有值
//  3. 合并窗口内的后续消息 → shouldProcess=false, 立刻返回合并状态（Bug1修复后不再等AI回复）
//  4. 超过合并上限的积压消息 → 等前面处理完，自己成为下一批的第一个
//
// traceID（E3，2026-09-19）：入口请求的 trace，只用于贯穿本请求的队列日志；无 trace 传空串。
func (s *MessageQueueService) EnqueueAndWait(tenantID uint, customerID uint, content string, traceID string) (mergedContent string, shouldProcess bool, reply string, mergeWaitDuration time.Duration, isSimple bool, mergeCount int, epoch uint64) {
	k := queueKey(tenantID, customerID)

	// 简单消息快速通道（不合并、不等窗口；跨实例也不需要协调——它本来就不进批次）
	if IsSimpleMessage(content) {
		// H7修复(2026-08-26)：实例内同客户简单消息串行，防止并发乱序/重复回复
		q := s.getQueue(k)
		q.mu.Lock()
		// P0-3 修复：简单消息等待段若 panic 不释放锁会导致该客户队列永久死锁，recover 兜底
		locked := true
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[合并队列] 客户%s 简单消息路径 panic 已恢复: %v", k, r)
				q.simpleProcessing = false
				q.cond.Broadcast()
				if locked {
					q.mu.Unlock()
				}
				panic(r)
			}
		}()
		for q.simpleProcessing {
			q.cond.Wait()
		}
		q.simpleProcessing = true
		locked = false
		q.mu.Unlock()
		log.Printf("[合并队列] 客户%s 简单消息快速通道: %q%s", k, logx.Safe(content, 40), traceTag(traceID))
		return content, true, "", 0, true, 1, 0
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
			// D6 修复(2026-09-16B，AUDIT_UAT_VERIFY_2026-09-16B)：删除抢锁后立即调用的
			// absorbRemotePending——此刻 q.processing 仍为 false，全部消息进 leftover 再
			// RPush 回队：纯空转，且与 waitRemotely 的 LPUSH 头插形成新旧消息首尾倒置风险。
			// 转交消息唯一有效吸收点=窗口收账（waitForMerge 内 absorb，批次已开 processing=true）。
			return s.processLocally(k, content, true, traceID)
		}
		// 其他实例正在处理该客户：消息转交对方合并，本请求远程等回复
		return s.waitRemotely(tenantID, customerID, content, traceID)
	}

	// 单实例内存模式（REDIS_ENABLED=false）
	return s.processLocally(k, content, true, traceID)
}

// traceTag E3(2026-09-19)：trace 日志片段，空 trace 返回空串——
// 不落 CustomerQueue 字段是为规避解锁后读受锁字段的 -race 竞争（P2-36 同款纪律）。
func traceTag(t string) string {
	if t == "" {
		return ""
	}
	return " trace=" + t
}

// processLocally 本地处理路径（原有单机逻辑，含超时自愈/批次/积压）
// appendOwn=false 用于"接管"场景：本消息已在 Redis 待合并列表里，由 absorb 收回，避免重复
// P1-19：返回值末尾增加 epoch（处理代际号），调用方生成回复后须携带该代际调 SetReply
func (s *MessageQueueService) processLocally(k string, content string, appendOwn bool, traceID string) (mergedContent string, shouldProcess bool, reply string, mergeWaitDuration time.Duration, isSimple bool, mergeCount int, epoch uint64) {
	q := s.getQueue(k)

	q.mu.Lock()
	locked := true
	// P0-3 修复(2026-09-09)：持锁段 panic 不永久死锁——解锁+清状态+广播后重抛（gin recovery 返回 500，请求不挂死）
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[合并队列] 客户%s processLocally panic 已恢复并释放队列锁: %v", k, r)
			q.processing = false
			q.simpleProcessing = false
			q.deadlineExpired = false
			q.cond.Broadcast()
			if locked {
				q.mu.Unlock()
			}
			panic(r)
		}
	}()

	// 修复：processing锁超时自愈
	// 根因：用户11:13发消息，11:22才收到回复（9分钟），远超2分钟硬顶
	// 如果前一个请求的goroutine卡死（Cloudflare连接断开但sleep未结束），
	// processing锁会一直被持有，新请求无限等待
	// 超过 processing_lock_timeout（默认600s）强制释放+清除残留消息，新请求接管
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
		// P0-3 修复：仅清理属于当前超时批次的残留，保留 BatchID==0 的积压消息（下一批候选），
		// 否则积压等待者苏醒后用陈旧 msgIdx 索引 q.pending 会 panic（index out of range）且锁永不释放
		if len(q.pending) > 0 {
			kept := q.pending[:0]
			for _, m := range q.pending {
				if m.BatchID != q.currentBatch {
					kept = append(kept, m)
				}
			}
			q.pending = kept
			log.Printf("[合并队列] 客户%s 清除超时批次残留消息，保留积压 %d 条", k, len(q.pending))
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
		q.batchClosed = false              // P0-7：新批次开账
		q.currentBatch++                   // 新批次，递增batchID
		q.epoch++                          // P1-19：代际递增，本批次持代返回给调用方做 SetReply 校验
		q.processingStartedAt = time.Now() // 记录处理开始时间，用于超时检测
		q.mergeCount = 1
		q.lastReply = ""
		q.deadlineExpired = false
		if msgIdx >= 0 {
			q.pending[msgIdx].BatchID = q.currentBatch // 标记消息所属批次
		}
		locked = false
		myEpoch := q.epoch
		completedBatch := q.currentBatch
		q.mu.Unlock()

		log.Printf("[合并队列] 客户%s 拿到处理权(批次%d,代%d)，开始合并窗口等待: %q%s", k, completedBatch, myEpoch, logx.Safe(content, 40), traceTag(traceID))

		// 事件驱动合并等待（滑动窗口），返回合并内容+实际等待时长
		merged, waitDuration := s.waitForMerge(q, k)
		// P2-36 修复(2026-09-09)：waitForMerge 解耦后 q.currentBatch/q.mergeCount 可能已被并发写入，
		// 读取需重新加锁快照——否则 -race 报数据竞争（解锁后读受锁保护字段）。
		q.mu.Lock()
		finalBatch, finalCount := q.currentBatch, q.mergeCount
		q.mu.Unlock()
		log.Printf("[合并队列] 客户%s 合并完成(批次%d): %d条消息, 等待%.1fs, 内容: %q%s", k, finalBatch, finalCount, waitDuration.Seconds(), logx.Safe(merged, 40), traceTag(traceID))
		return merged, true, "", waitDuration, false, finalCount, myEpoch
	}

	// 已经在处理中了，检查是否还能合并进当前批次
	// P0-7 修复(2026-09-15)：加 !q.batchClosed——批次已关账（waitForMerge 收集完毕，
	// AI 生成/延迟期间）时不再标进旧批次走"等旧回复"路径（旧回复不含本条内容，
	// 消息会以旧 BatchID 滞留成孤儿），落到下方积压接管路径作为下一批处理。
	if q.mergeCount < config.GlobalConfig.ReplySpeed.MaxMergeMessages && !q.batchClosed {
		q.mergeCount++
		if msgIdx >= 0 {
			q.pending[msgIdx].BatchID = q.currentBatch // 标记消息所属批次
		}
		// D5(2026-09-16)：记下"我合并进的是哪一批"的代际——唤醒后通道等待者据此做批次投递认领，
		// 不能用醒来时刻的 q.epoch（等待期间可能已开下一批，拿错代际会顶掉下一批处理者的投递权）
		waitEpoch := q.epoch
		// 立刻通知主请求：新消息到达（事件驱动，替代定时轮询）
		q.cond.Signal()

		log.Printf("[合并队列] 客户%s 消息合并进当前批次(第%d条,批次%d): %q%s", k, q.mergeCount, q.currentBatch, logx.Safe(content, 40), traceTag(traceID))

		// 挂起等待回复
		for q.processing && q.lastReply == "" {
			q.cond.Wait()
		}
		reply = q.lastReply
		// D5：解锁前快照（P2-36 同款纪律——return 表达式里读受锁字段是数据竞争）
		waitedCount := q.mergeCount
		locked = false
		q.mu.Unlock()
		return "", false, reply, 0, false, waitedCount, waitEpoch
	}

	// 超过合并上限，积压队列——这是下一批的第一个
	// 先挂起等待当前批次完成
	for q.processing {
		q.cond.Wait()
	}

	// 当前批次完成了，本消息成为下一批的第一个
	q.processing = true
	q.batchClosed = false // P0-7：积压接管即新批次开账
	q.currentBatch++      // 新批次
	q.epoch++             // P1-19：积压接管同样递增代际
	q.processingStartedAt = time.Now()
	q.mergeCount = 0
	q.lastReply = ""
	q.deadlineExpired = false
	// P0-3 修复：不再依赖入队时的 msgIdx（自愈清残留后索引位置可能漂移导致 panic），
	// 改为把本消息与所有仍积压(BatchID==0)的消息统一纳入新批次，语义等价且索引安全
	inBatch := false
	for i := range q.pending {
		if q.pending[i].BatchID == 0 {
			q.pending[i].BatchID = q.currentBatch
			q.mergeCount++
			inBatch = true
		}
	}
	if !inBatch {
		// 防御：本消息理论上已在 pending 中，若因极端自愈时序丢失则补入
		q.pending = append(q.pending, PendingMessage{
			Content:    content,
			ReceivedAt: time.Now(),
			BatchID:    q.currentBatch,
		})
		q.mergeCount = 1
	}
	locked = false
	myEpoch := q.epoch
	myBatch := q.currentBatch
	startCount := q.mergeCount
	q.mu.Unlock()

	log.Printf("[合并队列] 客户%s 积压消息拿到处理权(批次%d,代%d): %d条: %q%s", k, myBatch, myEpoch, startCount, logx.Safe(content, 40), traceTag(traceID))
	merged, waitDuration := s.waitForMerge(q, k)
	// D5 修复(2026-09-16B，AUDIT_UAT_VERIFY_2026-09-16B)：q.mergeCount 在窗口期由
	// absorbRemotePending/新消息累加在锁内写、这里无锁读——数据竞争（go test -race 可炸，
	// 读半值致 mergeCount 决策失真）。解锁后重取快照。
	q.mu.Lock()
	finalCount := q.mergeCount
	q.mu.Unlock()
	return merged, true, "", waitDuration, false, finalCount, myEpoch
}

// waitRemotely 远程等待路径（本实例未抢到锁，消息转交处理者实例）
// 协议：
//  1. LPUSH 消息到 mq:pending:{k}，处理者会在窗口内/窗口关闭时吸收进当前批次
//  2. 轮询 mq:lastseq:{k}，序号增长后读 mq:reply:{k}:{seq} 取回复
//  3. 锁消失（持有实例死亡）→ 尝试接管成为新处理者
//
// 已知边界：若消息到达时批次已满被"退回"，会随下一批处理，但本请求可能拿到
// 上一批的回复体（前端聊天记录为准，影响极小）；单实例模式无此问题
func (s *MessageQueueService) waitRemotely(tenantID uint, customerID uint, content string, traceID string) (mergedContent string, shouldProcess bool, reply string, mergeWaitDuration time.Duration, isSimple bool, mergeCount int, epoch uint64) {
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
	// P1-41 缺口③：RPush 保 FIFO——LPush 时 DrainList 取出是 LIFO，合并顺序颠倒
	redisclient.RPush("mq:pending:"+k, content)
	log.Printf("[合并队列] 客户%s 消息转交其他实例处理: %q%s", k, logx.Safe(content, 40), traceTag(traceID))

	deadline := time.Now().Add(getProcessingLockTimeout(tidFromKey(k)))
	for time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)

		// 新回复发布？
		if v, ok := redisclient.Get("mq:lastseq:" + k); ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > curSeq {
				if r, ok := redisclient.Get(fmt.Sprintf("mq:reply:%s:%d", k, n)); ok && r != "" {
					// D5：读取配对 epoch（老版本处理者未发布 → 0，投递认领退化为放行，宁双不漏）
					var repEpoch uint64
					if ev, ok := redisclient.Get(fmt.Sprintf("mq:replepoch:%s:%d", k, n)); ok {
						repEpoch, _ = strconv.ParseUint(ev, 10, 64)
					}
					log.Printf("[合并队列] 客户%s 远程回复已取到(序号%d)", k, n)
					return "", false, r, 0, false, 1, repEpoch
				}
			}
		}

		// 锁消失且尚无新回复 → 原持有实例死亡，跳出循环去接管
		if !redisclient.LockExists(lockKey) {
			log.Printf("[合并队列] 客户%s 检测到处理者失联，尝试接管%s", k, traceTag(traceID))
			break
		}
	}

	// 接管：拿到锁则成为新处理者（自己的消息已在待合并列表，appendOwn=false 防重复）
	if h := redisclient.TryLock(lockKey, getProcessingLockTimeout(tidFromKey(k))); h != nil {
		q := s.getQueue(k)
		q.mu.Lock()
		q.redisLock = h
		q.mu.Unlock()
		// D6 注(2026-09-16B)：与 EnqueueAndWait 抢锁段不同，接管场景本地队列可能正有
		// processing=true 的在途批次（web 请求），此处 absorb 能把死实例转交的消息并进
		// 该批次（真语义，非空转）——保留。抢锁段的同款调用已删（见 EnqueueAndWait D6 注释）。
		s.absorbRemotePending(k, q)
		return s.processLocally(k, content, false, traceID)
	}

	// P2-38 修复(2026-09-09)：极端场景——锁被别的实例抢先拿走但仍无回复。
	// 原逻辑"本地兜底处理"会再生成一遍 AI 回复 → 跨实例双回复+双计费。
	// 改为落一条降级提示，不让客户端空等，也绝不再触发第二次 AI 调用。
	log.Printf("[合并队列] 客户%s 远程等待超时且拿锁失败，落降级提示（不重复生成AI回复）", k)
	s.WriteDegradedNotice(tenantID, customerID, "系统繁忙，请稍等片刻再试一次")
	return content, false, "", 0, false, 1, 0
}

// ClaimReplyDelivery D5(2026-09-16)：为 (批次代际, 通道) 认领"本批唯一回复送达该通道"的一次性权利。
// 通道 worker 并入合并队列后，批次处理者可能是 web 请求（不会出站投微信）也可能是另一路通道 worker，
// 等待者里同通道可能有 0~N 条——投递必须"每批每通道恰好一次"，由本方法原子裁决：
//   - 单实例：队列内 deliveredFor 表（随队列空闲回收自然清理）；
//   - 多实例：Redis SetNX mq:deliver:{k}:{epoch}:{chID}（TTL 10min，跨实例互斥）；
//   - epoch==0（旧协议回复无配对 epoch/降级路径）：放行投递——宁可极端双发（出站台账可查），不可静默漏发。
//
// 返回 true=本调用方负责投递；false=同批同通道已有他人认领。
//
// P2-2 修复(2026-09-19 审计批三)：原实现用 TryLock（吞 err），Redis 故障时句柄 nil
// 被误判为"他人已认领"→ 静默漏发，与本函数自述"宁可双发不可漏发"及 redisclient
// TryLockE 契约（SetNX 出错禁止按没抢到处理）三方打架。改为 TryLockE 三分支：
// 拿锁→投；锁被持有→不投；Redis 故障→降级本机 deliveredFor 裁决+warn 指标。
var tryDeliveryClaim = redisclient.TryLockE   // 测试接缝：单测注入故障/持锁三态
var claimRedisEnabled = redisclient.IsEnabled // 测试接缝：单测模拟 Redis 开/关两态

func (s *MessageQueueService) ClaimReplyDelivery(tenantID, customerID uint, epoch uint64, channelID uint) bool {
	if epoch == 0 {
		return true
	}
	k := queueKey(tenantID, customerID)
	if claimRedisEnabled() {
		h, err := tryDeliveryClaim(fmt.Sprintf("mq:deliver:%s:%d:%d", k, epoch, channelID), 10*time.Minute)
		switch {
		case err == nil && h != nil:
			return true // 本机抢到认领权
		case err == nil:
			return false // Redis 正常且锁被他人持有：他人投递
		default:
			// Redis 故障≠他人已认领：落到下方单机表裁决。故障期跨实例互斥失守，
			// 极端可双发（出站台账可稽核回溯），但绝不重演静默漏发。
			log.Printf("[合并队列] 投递认领Redis故障，降级本机裁决(宁双发不漏发) k=%s epoch=%d channel=%d: %v", k, epoch, channelID, err)
			metrics.IncReplyDeliveryDegrade()
		}
	}
	q := s.getQueue(k)
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.deliveredFor == nil {
		q.deliveredFor = map[string]bool{}
	}
	key := fmt.Sprintf("%d:%d", epoch, channelID)
	if q.deliveredFor[key] {
		return false
	}
	q.deliveredFor[key] = true
	return true
}

// WriteDegradedNotice 向客户最新会话落一条降级提示消息（P2-38）
// 仅在极端超时兜底路径调用；带租户过滤查最新活跃会话，避免空等也避免二次计费。
func (s *MessageQueueService) WriteDegradedNotice(tenantID, customerID uint, notice string) {
	var conv model.Conversation
	if err := db.DB.Where("tenant_id = ? AND customer_id = ? AND status = 'active'", tenantID, customerID).
		Order("updated_at DESC").First(&conv).Error; err != nil {
		log.Printf("[合并队列] 降级提示未找到活跃会话(tenant=%d customer=%d): %v", tenantID, customerID, err)
		return
	}
	msg := model.Message{
		// D6 护栏(2026-09-16)命中：db.DB 无请求 ctx，盖章回调取到 0 → 降级提示落 tenant_id=0
		// （C7 同款第三处）——客户侧 RQ 查询看不见这条"等待中"提示，降级体验失效且污染平台视图。
		// 后台路径必须显式传租户，与 billing/privacy/chat_lead(P1-5) 同款纪律。
		TenantID:       tenantID,
		ConversationID: conv.ID,
		CustomerID:     customerID,
		SenderType:     "system",
		Content:        notice,
		MessageType:    "text",
		CreatedAt:      time.Now(),
	}
	if err := db.DB.Create(&msg).Error; err != nil {
		log.Printf("[合并队列] 降级提示落库失败(conversation=%d): %v", conv.ID, err)
	}
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
		// P1-41 缺口③：放回同样 RPush 保原顺序（下一批经 DrainList 仍 FIFO）
		redisclient.RPush("mq:pending:"+k, c)
	}
	if absorbed > 0 {
		log.Printf("[合并队列] 客户%s 吸收其他实例转交消息 %d 条(批次%d)", k, absorbed, batch)
	}
}

// waitForMerge 事件驱动的合并等待（滑动窗口 + 条件变量信号）
//
// P3-18 修复：陈旧注释"45秒窗口"→ 实际实现 25 秒（配置键 merge_wait_seconds）。
//
// 对齐用户需求：
//  1. 第一条消息到达 → 开启25秒合并窗口
//  2. 25秒内没有其他消息 → 生成回复，延迟偏移里减去实际等待时间
//  3. 25秒内有其他消息 → 重置25秒窗口（滑动窗口），继续等待
//     - 新窗口内有消息 → 合并并生成回复，延迟偏移减去实际等待时间
//     - 新窗口内没消息 → 生成回复，延迟偏移减去实际等待时间
//  4. 达到3条上限 → 立刻退出窗口，直接生成回复，延迟偏移减去实际等待时间
//  5. 前端无感知，一律体现为"顾问输入中..."状态
//
// 实现方式：用 sync.Cond + AfterFunc 定时器替代定时轮询
//   - 新消息入队时 cond.Signal() 立刻唤醒主请求（事件驱动，不再9秒轮询）
//   - 定时器到期时设置 deadlineExpired 标记 + cond.Broadcast() 唤醒
//   - 每次被新消息唤醒后 Reset 定时器（滑动窗口：新消息重置25秒deadline）
//   - 返回实际等待时长 mergeWaitDuration，供AI延迟偏移使用
//     （AI延迟 = 基础延迟 - 合并等待时间，最小为0，不会叠加）
func (s *MessageQueueService) waitForMerge(q *CustomerQueue, k string) (mergedContent string, mergeWaitDuration time.Duration) {
	// 修复：合并窗口从25秒合并窗口，fallback值同步更新
	// 用户明确要求：客户连发消息时，30秒滑动窗口合并，最多3条
	mergeWindow := time.Duration(runtimecfg.DefaultSystemConfigService.GetIntForTenant(tidFromKey(k), "merge_window_seconds", 25)) * time.Second
	maxMerge := config.GlobalConfig.ReplySpeed.MaxMergeMessages

	// 记录合并等待起始时间，用于计算延迟偏移
	startTime := time.Now()

	// P2-37 修复(2026-09-09)：滑动窗口定时器改"每轮重建 + stop 通道取消失效回调"。
	// 原 time.AfterFunc + Reset 竞态：旧计时器回调若在 Reset 前后触发，会把
	// deadlineExpired 置 true 而新窗口刚开启 → 合并窗口提前关闭。
	// 新实现：每轮新消息关闭上一轮 stopCh 作废其回调，再新建一句式 AfterFunc；
	// 回调持锁后先查 stopCh——已关闭则直接返回（旧窗口到期不误伤新窗口）。
	armDeadline := func() (stop *chan struct{}) {
		c := make(chan struct{})
		time.AfterFunc(mergeWindow, func() {
			q.mu.Lock()
			defer q.mu.Unlock()
			select {
			case <-c:
				return // 已被新窗口取代，作废本次到期
			default:
			}
			q.deadlineExpired = true
			q.cond.Broadcast()
		})
		return &c
	}

	// 首次开启合并窗口
	deadlineStop := armDeadline()
	defer func() {
		if deadlineStop != nil {
			close(*deadlineStop) // 退出时作废可能仍在飞的到期回调
		}
	}()

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
		if deadlineStop != nil {
			close(*deadlineStop) // 作废旧窗口的到期回调
		}
		deadlineStop = armDeadline() // 新窗口定时器重置

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
	// P0-7 修复(2026-09-15)：批次关账——本批收集到此为止，后续到达的消息改投下一批
	q.batchClosed = true
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
// P1-19 修复(2026-09-09)：新增 epoch 参数做代际 fencing——自愈清锁后原处理者若只是慢（600s+ 后返回），
// 不可能持有当前代际号，其 SetReply 会被丢弃，杜绝旧处理者覆盖新批次状态/释放新处理者的 Redis 锁。
// signature: SetReply(tenantID, customerID, epoch, reply)
// SetReply 写入客户合并队列的生成结果并唤醒等待请求。
func (s *MessageQueueService) SetReply(tenantID uint, customerID uint, epoch uint64, reply string) {
	k := queueKey(tenantID, customerID)
	q := s.getQueue(k)

	q.mu.Lock()
	// P1-19 代际 fencing：调用方持有的 epoch 与队列当前代际不符 → 旧处理者复活，丢弃
	if epoch != 0 && epoch != q.epoch {
		q.mu.Unlock()
		log.Printf("[合并队列] 客户%s 收到过期 SetReply(代%d vs 当前代%d)，丢弃防踩踏", k, epoch, q.epoch)
		return
	}
	h := q.redisLock
	q.redisLock = nil
	q.lastReply = reply
	q.lastReplyAt = time.Now()
	q.processing = false
	// D5：发布用代际在锁内快照（P2-36 教训：解锁后读受锁字段 = 数据竞争）
	pubEpoch := q.epoch
	log.Printf("[合并队列] 客户%s 回复已设置(代%d), 唤醒所有等待者, 回复前20字: %q", k, epoch, truncateStr(reply, 20))
	// 唤醒所有等待的goroutine
	q.cond.Broadcast()
	q.mu.Unlock()

	// 多实例：先发布回复再放锁——保证远程等待者拿到锁消失信号时回复已可见
	if redisclient.IsEnabled() {
		seq := redisclient.Incr("mq:seq:" + k)
		redisclient.SetEx("mq:lastseq:"+k, strconv.FormatInt(seq, 10), 10*time.Minute)
		redisclient.SetEx(fmt.Sprintf("mq:reply:%s:%d", k, seq), reply, 5*time.Minute)
		// D5(2026-09-16)：epoch 随回复配对发布——远程通道等待者据此做"每批每通道一次"投递认领
		redisclient.SetEx(fmt.Sprintf("mq:replepoch:%s:%d", k, seq), strconv.FormatUint(pubEpoch, 10), 5*time.Minute)
		if h != nil {
			h.Unlock()
		}
	}
}
