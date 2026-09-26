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
	"sync/atomic"
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
	// ReqID 请求归属号（2026-09-25 残项1）：本行由哪一路 EnqueueAndWait 请求放进队列的。
	// 之前只有 BatchID，"这条消息被谁收走了"反查不到请求本身——积压等待者被唤醒后
	// 无法知道自己其实已被别人开的新批收走，只能无条件再开一批，于是同一条消息回两遍
	// （DEFECT-G7-DUP-TAKEOVER）。BatchID 回答"进了哪一批"，ReqID 回答"是谁的那一句"。
	// 0 = 无本地等待者认领本行（自愈后的补写行、老版本实例转交来的消息）。
	// 残项2 真修(2026-09-26)：跨实例接管的自己那一句**也带 ReqID**——absorb 认领它之后，
	// "我这句到底在不在批里"就有了确定的查法（见 ensureOwnPendingRow），不再靠
	// "pending 里有没有任何积压行"这种会把别人的行当成自己的判据。
	ReqID uint64
}

// CustomerQueue 单客户消息队列
type CustomerQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	pending []PendingMessage // 待合并消息
	// nextReqID 请求归属号发号器（2026-09-25 残项1）：本地入队的每条消息领一个递增号，
	// 与 PendingMessage.ReqID 配对使用。
	nextReqID uint64
	// claimedByReq 归属登记表（2026-09-25 残项1）：key=请求归属号，value=把这条消息收进
	// 批次的那个代际。只有"自己那条还停在积压里(BatchID==0)、却被别人开的新批收走"的请求
	// 会被登记——它醒来后读到这一格就该改等那一批的回复，而不是再开一批把同一句话答第二遍。
	// 消费即删；SetReply 里随 replyByEpoch 一同剪枝（活跃客户长跑不清就是内存泄漏）。
	claimedByReq     map[uint64]uint64
	processed        int       // 已处理的消息数（积压队列的偏移）
	processing       bool      // 是否正在处理中
	lastReply        string    // 最近一次生成的回复（用于后续请求直接取）
	lastReplyAt      time.Time // 最近回复时间（判断是否是本次合并的回复）
	mergeCount       int       // 当前合并批次已合并几条（滑动窗口判据；残项2 真修后一律由 syncMergeCount 从 pending 行数派生，不再各分支手工 ++，对外返回值另以 waitForMerge 收账行数为准）
	simpleProcessing bool      // 简单消息是否正在处理（H7：实例内同客户串行，防并发乱序/重复回复）
	// P1-4 配套(2026-09-20 审计批)：simple 锁的持有时点与代次令牌。
	// simpleSince 供看门狗判定持锁时长；simpleToken 每次接管递增，看门狗只复位
	// "自己那次接管"（防误伤后续正常持有者），SimpleMessageDone 语义不变。
	simpleSince     time.Time
	simpleToken     uint64
	deadlineExpired bool   // 合并窗口到期标记（由AfterFunc定时器设置，waitForMerge检查后清除）
	currentBatch    uint64 // 当前批次ID，每次新批次递增，防止跨批次消息混合
	// batchClosed P0-7 修复(2026-09-15)：批次"关账"标志。waitForMerge 收集完本批消息后
	// 置 true——此后 AI 生成+延迟期间（10-135s，正是客户等回复补发消息的高发窗口）到达的
	// 消息不再标进已封账批次（旧行为：等待者拿到不含自己内容的旧回复，消息以旧 BatchID
	// 滞留 pending 成孤儿，仅靠 600s 超时自愈"清除残留"或直接随空闲清扫蒸发）。
	// 关账后的新消息走既有积压接管路径，作为下一批第一个被处理。
	batchClosed bool
	epoch       uint64 // P1-19：处理代际号，每次处理者接管递增；SetReply 校验代际防旧处理者践踏新批次
	// replyByEpoch D8 修复(2026-09-24，G-7 真实并发单测抓到)：按**代际**发布的回复槽位。
	// 旧实现只有一格 lastReply，SetReply 写它、等待者读它，而下一批开账时会把 lastReply 清空
	// 并重新置 processing=true。于是形成一条唤醒竞态：Broadcast 唤醒了上一批的等待者，但它
	// 还没抢到锁，积压接管者已经把那一格清掉并开了新批——等待者醒来时读到
	// "processing=true 且 lastReply==空"，判定"我的批次还没好"，重新挂起，
	// 本批回复就此永久丢失（客户连发消息时表现为一部分消息再也等不到回复，
	// 或拿到下一批的回复——而下一批的回复并不含自己那句话）。
	// 改为每代一格：SetReply 按 pubEpoch 发布，等待者只读自己代际那格，
	// 下一批清不清 lastReply 与之无关。
	replyByEpoch        map[uint64]string
	processingStartedAt time.Time               // 处理开始时间，用于2分钟超时自愈检测
	redisLock           *redisclient.LockHandle // 跨实例分布式锁句柄（Redis模式处理者持有）
	lastActivity        time.Time               // 最近活跃时间（空闲队列回收依据，2026-09-09）
	// deliveredFor D5(2026-09-16)：批次投递认领表——key "epoch:channelID"。
	// 通道 worker 并入合并队列后，一个批次的唯一回复可能由 web 处理者生成、通道等待者送达，
	// 也可能处理者本身就是通道 worker——"每批每通道至多投递一次"必须原子裁决，
	// 否则微信侧双发/全漏。Redis 模式走 SetNX 跨实例认领（见 ClaimReplyDelivery）。
	deliveredFor map[string]bool
}

// simpleLockTimeout P1-4(2026-09-20)：简单消息串行锁的看门狗阈值。
// 常态由 AI 110s 总预算封顶（回复生成完即 SimpleMessageDone），180s 仍不释放
// 只可能是处理 goroutine 真挂死（非 panic 路径，recover 兜不住）——
// 到点复位并广播，防该客户简单消息通道永久静默。包级变量便于单测缩短窗口。
var simpleLockTimeout = 180 * time.Second

// simpleWatchdogFired P1-4：看门狗复位累计次数（WARN 计数口径，重启清零）
var simpleWatchdogFired atomic.Int64

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

// HasInflightBatch 探测该客户当前是否"会有人回复"：本实例队列 processing/简单锁持有/待合并积压，
// 或（Redis 启用时）跨实例处理锁 mq:lock:* 存在、他实例 mq:pending:* 有积压。
// P2-1 修复(2026-09-20 批三)：web 端相似消息抑制只在确实存在在途批次时才走 merged 分支——
// 此前抑制仅看历史消息重叠度，主批次早已回完时相似句被标 merged_suppressed 却无人再答，
// 客户连发相似句只收到第一条回复（静默丢答）。探测不建队列对象、不改任何状态，Redis 错误按"无在途"fail-open。
func (s *MessageQueueService) HasInflightBatch(tenantID, customerID uint) bool {
	k := queueKey(tenantID, customerID)
	s.mu.Lock()
	q := s.queues[k]
	s.mu.Unlock()
	if q != nil {
		q.mu.Lock()
		inflight := q.processing || q.simpleProcessing || len(q.pending) > 0
		q.mu.Unlock()
		if inflight {
			return true
		}
	}
	if redisclient.IsEnabled() {
		if redisclient.LockExists("mq:lock:" + k) {
			return true
		}
		if redisclient.LLen("mq:pending:"+k) > 0 {
			return true
		}
	}
	return false
}

// ShutdownDrain P2-8 修复(2026-09-20 批三)：优雅停机排空合并队列——
// ①宽限期（grace，默认调用方给 5s）内轮询等在途批次自然收尾（处理者会自行 SetReply+放锁）；
// ②到期仍有在途的，强制释放：本地 processing/simple 复位、Redis 处理锁句柄主动 Unlock、
//
//	epoch 递增（fencing：濒死旧批次的迟到 SetReply 因代际不符被拒，不污染重启后新批次）。
//
// 背景：旧停机序列只有 HTTP Shutdown+usage flush，processing 锁残留要等 600s TTL 自愈——
// 重启窗口内该客户新消息无人接管（waitRemotely 见锁在就一直干等），部署后客户静默黑屏。
// 客户消息本身在入队前已落库（P2-8 的"消息不丢"底线由落库先行保证），本函数救的是"后续可被回答"。
func (s *MessageQueueService) ShutdownDrain(grace time.Duration) {
	deadline := time.Now().Add(grace)
	for {
		s.mu.Lock()
		var inflight []*CustomerQueue
		for _, q := range s.queues {
			q.mu.Lock()
			if q.processing || q.simpleProcessing {
				inflight = append(inflight, q)
			}
			q.mu.Unlock()
		}
		s.mu.Unlock()
		// HTTP 已先行 Shutdown：不会再有新的入队者，队列清空即排空达成，可立即返回
		if len(inflight) == 0 {
			return
		}
		if !time.Now().After(deadline) {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		// 宽限到期：强制释放残留锁
		for _, q := range inflight {
			q.mu.Lock()
			if q.redisLock != nil {
				q.redisLock.Unlock()
				q.redisLock = nil
			}
			q.processing = false
			q.simpleProcessing = false
			q.epoch++
			q.batchClosed = true
			q.cond.Broadcast()
			q.mu.Unlock()
		}
		log.Printf("[合并队列] 停机宽限 %s 到期，强制释放 %d 个在途批次锁并递增代际（P2-8）", grace, len(inflight))
		return
	}
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

// simpleWatchdog P1-4(2026-09-20)：simple 锁看门狗复位体——仅当"锁仍被持有且代次
// 令牌与自己登记的一致"才复位（旧持有者已正常释放、新持有者接管时令牌已递增，不误伤）。
// 独立成方法便于确定性单测（不依赖 AfterFunc 时序）。
func (s *MessageQueueService) simpleWatchdog(k string, token uint64) {
	q := s.getQueue(k)
	q.mu.Lock()
	if q.simpleProcessing && q.simpleToken == token {
		log.Printf("[合并队列][WARN] 客户%s 简单消息锁持有 %.0fs 未释放（处理协程疑似挂死），看门狗复位（累计触发 %d 次）",
			k, time.Since(q.simpleSince).Seconds(), simpleWatchdogFired.Add(1))
		q.simpleProcessing = false
		q.cond.Broadcast()
	}
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
// mergeCount 的口径（残项3，2026-09-26 固化）：
//   - 本批处理者（shouldProcess=true）：waitForMerge 收账时真正进批的行数，有效值；
//   - 并进本机在途批的等待者：入批那一刻本批的行数（不再读醒来时刻的窗口计数器），有效值；
//   - **跨实例等待 / 拿锁失败降级 / 随他人批次取回复**：一律 **0=未知**。
//     这些路径没跑那个批次、拿不到处理者实例的收账行数；历史上写死 1，等于把
//     "我不知道"谎报成"就一句话"。0 既不是"零句"也不是"一句"，
//     任何新下游都不得把 0 当"批内一条"消费（要条数请先按 shouldProcess 与来源分支）。
//     现知下游：internal/channel/inbound.go 的 D7 相似抑制前置 `mergeCount <= 1`
//     只在处理者分支读它（跨实例等待那两路根本走不到），CalcHumanlikeDelay 的该参数
//     自 2026-09-09 固定小延迟决策后已不参与计算。
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
		// P1-4 修复(2026-09-20)：simple 锁此前无超时自愈——自愈扫描只清 processing，
		// 等待段 `for q.simpleProcessing { Wait }` 无时限。处理 goroutine 真挂死
		// （非 panic，recover 兜不住）时该客户简单消息永久静默。接管时记时点 +
		// 一次性看门狗：超 simpleLockTimeout 仍持锁即复位并广播；令牌比对保证
		// 只复位"自己那次接管"，不误伤后续正常持有者。
		q.simpleSince = time.Now()
		q.simpleToken++
		token := q.simpleToken
		locked = false
		q.mu.Unlock()
		time.AfterFunc(simpleLockTimeout, func() { s.simpleWatchdog(k, token) })
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
			// reqID 传 0 = 本函数代发（本地入队，句柄尚未落 pending）。
			return s.processLocally(k, content, 0, traceID)
		}
		// 其他实例正在处理该客户：消息转交对方合并，本请求远程等回复
		return s.waitRemotely(tenantID, customerID, content, traceID)
	}

	// 单实例内存模式（REDIS_ENABLED=false）
	return s.processLocally(k, content, 0, traceID)
}

// traceTag E3(2026-09-19)：trace 日志片段，空 trace 返回空串——
// 不落 CustomerQueue 字段是为规避解锁后读受锁字段的 -race 竞争（P2-36 同款纪律）。
func traceTag(t string) string {
	if t == "" {
		return ""
	}
	return " trace=" + t
}

// allocReqID 为本请求领一个归属号（与 PendingMessage.ReqID / claimedByReq 同一套编号）。
// 发号必须在队列锁内：跨实例接管路径（waitRemotely）与本地入队路径（processLocally）
// 用的是同一个 nextReqID，号发重了归属就会认错人——认错的后果是"别人的行被当成我的，
// 我那一句再补一份"，正是要修的缺陷。
func (s *MessageQueueService) allocReqID(q *CustomerQueue) uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.nextReqID++
	return q.nextReqID
}

// ensureOwnPendingRow 「本请求这一句」进批的**唯一裁决点**（残项2 真修，2026-09-26）。
//
// 修前的形态：三条分支各写各的——开新批靠函数入口那句无条件 append、并进在途批靠
// msgIdx 标行、积压接管靠 `if !inBatch { 再 append 一次 }`。而 `inBatch` 判的是
// "pending 里有没有**任何**积压行"，不是"我这句在不在批里"，于是同一句话有两个可能的归宿：
//   - 双答：absorb 没接住自己那句（批次装不下时它会退回 Redis），防御分支又把同一句
//     补进本地 pending → 同一句话在 Redis 列表和本地各存一份，可能被两个批次各答一遍；
//   - 丢答：批次里恰好有**别人**的积压行，inBatch=true 掩盖了"我这行没进本批"，
//     本请求以为已入批，自己那句实际留在 BatchID==0 里等下一次接管。
//
// 现在三条分支统一只经这里落自己的行，判据是**自己的 ReqID**：
// pending 里已有我那行 → 只把它的 BatchID 对齐到本批（绝不再写第二行）；
// 没有 → 补写一行。因此"同一句话在 pending 里至多一行"是结构保证，不是靠调用方自觉。
//
// 返回 true=本次真的补写了一行；false=pending 里已有我那行，仅对齐批次标记（用于日志）。
func ensureOwnPendingRow(q *CustomerQueue, reqID uint64, content string, batchID uint64) bool {
	if reqID != 0 {
		for i := range q.pending {
			if q.pending[i].ReqID != reqID {
				continue
			}
			// 已在 pending：属于别的（多半是已死批次的）批次号时改标到本批。
			// 走到这里说明本请求刚判定自己才是本批处理者（processing 已归本请求），
			// 而本批以外的非零 BatchID 都是历史残留——不认领就是永久丢答。
			q.pending[i].BatchID = batchID
			if q.pending[i].Content != content {
				// 同一归属号不可能挂两句不同内容（除非上游复用 ReqID）；真发生了按本请求
				// 这句为准并留痕，宁可内容以最后一次提交为准，也不能写出第二行。
				log.Printf("[合并队列] 归属号%d的行内容与本请求不一致，按本请求内容收编(批次%d)", reqID, batchID)
				q.pending[i].Content = content
				q.pending[i].ReceivedAt = time.Now()
			}
			return false
		}
	}
	q.pending = append(q.pending, PendingMessage{
		Content:    content,
		ReceivedAt: time.Now(),
		BatchID:    batchID,
		ReqID:      reqID,
	})
	return true
}

// syncMergeCount 把窗口计数器对齐到「本批实际行数」，并对齐后返回该值。
//
// mergeCount 兼着两个身份：滑动窗口"够不够条数/有没有新消息"的判据（waitForMerge 读它），
// 和对外返回的"这批几句话"（D7 相似抑制前置读它）。历史上它靠各分支手工 ++/--，
// 接管、absorb、防御补写三条路径各算一遍，就会出现"行没进批却 +1"（虚高，残项2）
// 与"批里两条它记一条"（虚低，窗口提前关）。
// 现在每次落完自己的行就按 pending 重算一次——计数与行数不可能再分家，
// 因为它是从行数**派生**出来的，不是另记的一本账。
func syncMergeCount(q *CustomerQueue, batchID uint64) int {
	n := 0
	for _, m := range q.pending {
		if m.BatchID == batchID {
			n++
		}
	}
	q.mergeCount = n
	return n
}

// processLocally 本地处理路径（原有单机逻辑，含超时自愈/批次/积压）
// reqID：本请求那句消息的归属号，0 表示尚未发号（本函数入口代发）。
// 跨实例接管（waitRemotely）传**已发过号**的值——那一行可能已由 absorb 落到本地 pending，
// 三条分支一律经 ensureOwnPendingRow 认领它，不再"看见积压行就当自己已入批"（残项2 真修）。
// P1-19：返回值末尾增加 epoch（处理代际号），调用方生成回复后须携带该代际调 SetReply
func (s *MessageQueueService) processLocally(k string, content string, reqID uint64, traceID string) (mergedContent string, shouldProcess bool, reply string, mergeWaitDuration time.Duration, isSimple bool, mergeCount int, epoch uint64) {
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

	// 归属号：跨实例接管由调用方（waitRemotely）预先发号，本地入队在此处代发。
	// 发号点必须在锁内——见 allocReqID 注释。
	// 残项2 真修(2026-09-26)：这里**不再**无条件 append 自己那句。旧写法在函数入口就把行
	// 塞进 pending 并记下标 msgIdx，三条分支再各自用下标标批次，于是"我这句在哪一批"
	// 与"pending 里有没有我的行"是两件事（接管场景下 msgIdx 恒为 -1，行根本没人标）。
	// 现在统一由 ensureOwnPendingRow 在分支决策点落行（锁内、一次性、按 ReqID 幂等）。
	if reqID == 0 {
		q.nextReqID++
		reqID = q.nextReqID
	}

	// 如果没在处理中，本请求拿处理权
	if !q.processing {
		q.processing = true
		q.batchClosed = false              // P0-7：新批次开账
		q.currentBatch++                   // 新批次，递增batchID
		q.epoch++                          // P1-19：代际递增，本批次持代返回给调用方做 SetReply 校验
		q.processingStartedAt = time.Now() // 记录处理开始时间，用于超时检测
		q.lastReply = ""
		q.deadlineExpired = false
		// 唯一裁决点：本请求这一句落进新批。
		// 残项2(2026-09-25 DEFECT-G7-TAKEOVER-MERGECOUNT → 2026-09-26 真修)：mergeCount 此前
		// 无条件置 1，而接管场景（自己的消息还挂在 Redis 待合并列表里）批内一条都没有，虚高 1；
		// 反向，absorb 已把本句标进旧批次号时又算不到它。现在计数从行数派生（syncMergeCount），
		// 两个方向都不可能有偏差。虚高的影响面：internal/channel/inbound.go 的 D7 相似抑制前置
		// `mergeCount <= 1`（该抑制的批次跳过判定，极端下重复回复一条通道消息）。
		ensureOwnPendingRow(q, reqID, content, q.currentBatch)
		syncMergeCount(q, q.currentBatch)
		locked = false
		myEpoch := q.epoch
		completedBatch := q.currentBatch
		q.mu.Unlock()

		log.Printf("[合并队列] 客户%s 拿到处理权(批次%d,代%d)，开始合并窗口等待: %q%s", k, completedBatch, myEpoch, logx.Safe(content, 40), traceTag(traceID))

		// 事件驱动合并等待（滑动窗口），返回合并内容+实际等待时长+本批实际条数
		merged, waitDuration, batchCount := s.waitForMerge(q, k)
		// P2-36 修复(2026-09-09)：waitForMerge 解耦后 q.currentBatch 可能已被并发写入，
		// 读取需重新加锁快照——否则 -race 报数据竞争（解锁后读受锁保护字段）。
		q.mu.Lock()
		finalBatch := q.currentBatch
		q.mu.Unlock()
		log.Printf("[合并队列] 客户%s 合并完成(批次%d): %d条消息, 等待%.1fs, 内容: %q%s", k, finalBatch, batchCount, waitDuration.Seconds(), logx.Safe(merged, 40), traceTag(traceID))
		return merged, true, "", waitDuration, false, batchCount, myEpoch
	}

	// 已经在处理中了，检查是否还能合并进当前批次
	// P0-7 修复(2026-09-15)：加 !q.batchClosed——批次已关账（waitForMerge 收集完毕，
	// AI 生成/延迟期间）时不再标进旧批次走"等旧回复"路径（旧回复不含本条内容，
	// 消息会以旧 BatchID 滞留成孤儿），落到下方积压接管路径作为下一批处理。
	if q.mergeCount < config.GlobalConfig.ReplySpeed.MaxMergeMessages && !q.batchClosed {
		waitEpoch := q.epoch
		waitBatch := q.currentBatch
		// 唯一裁决点：并进在途批时同样只经这里落自己的行（ReqID 已有则只对齐批次号）。
		// 旧写法是 `q.mergeCount++` 后 `if msgIdx >= 0` 才标批次——接管场景 msgIdx 恒 -1，
		// 于是"计数 +1 而行没进批"，窗口计数器与本批真实行数分家（残项2 的另一半现场）。
		ensureOwnPendingRow(q, reqID, content, waitBatch)
		myCount := syncMergeCount(q, waitBatch)
		// 立刻通知主请求：新消息到达（事件驱动，替代定时轮询）
		q.cond.Signal()

		log.Printf("[合并队列] 客户%s 消息合并进当前批次(第%d条,批次%d): %q%s", k, myCount, waitBatch, logx.Safe(content, 40), traceTag(traceID))

		// 挂起等待回复
		// D8 修复(2026-09-24)：等的是"自己这一代那格"，不是共享的 lastReply——
		// Broadcast 到本 goroutine 抢到锁之间，积压接管者会清 lastReply 并重开 processing，
		// 只看共享格就会被误判成"批次还没好"而永久挂起（本批回复丢失）。
		// 后两个条件维持旧语义：代际已推进（超时自愈/下一批接管）时不再干等，
		// 回落读 lastReply，行为与修复前一致。
		for q.processing && q.replyByEpoch[waitEpoch] == "" && q.epoch == waitEpoch {
			q.cond.Wait()
		}
		reply = q.replyByEpoch[waitEpoch]
		if reply == "" {
			reply = q.lastReply
		}
		// D5：解锁前快照（P2-36 同款纪律——return 表达式里读受锁字段是数据竞争）
		// 残项2：返回入批那一刻算好的 myCount，不再读醒来时刻的 q.mergeCount
		// （等待期间可能已开下一批并把它清零/改数，读它就是"报别人那批的条数"）。
		locked = false
		q.mu.Unlock()
		return "", false, reply, 0, false, myCount, waitEpoch
	}

	// 超过合并上限，积压队列——本消息要么由别人开的新批带上，要么自己成为下一批的第一个。
	//
	// 残项1(2026-09-25，DEFECT-G7-DUP-TAKEOVER)：这里原来是"醒来就无条件把自己立成新批第一个"。
	// 批1 交卷那一次 Broadcast 同时唤醒停在本处的第 4、5 条，谁先抢到锁由调度决定：先醒的那路
	// 开批2，收账循环把**所有** BatchID==0 的行（连同对手那一句）一起标进批2；后醒的那路发现
	// 自己的行已不在积压集合里（inBatch=false），走下面的防御分支把同一句话**再补进** pending
	// 一遍 → 批3 诞生，客户为同一句收到两条内容重叠的回复，AI token 双烧。
	// 修法：pending 行带请求归属号 ReqID，被别人的批次收走时登记 claimedByReq[ReqID]=那一批的
	// 代际；醒来先查这张表——命中即说明"我这句已经在某一批里被答了/正被答"，改等那一批的回复。
	// 残项2 真修(2026-09-26)：同一段里当初留的"防御补写"（看不到任何积压行就再写一句自己）
	// 一并换成 ensureOwnPendingRow——补不补写只看"我的 ReqID 那行在不在批里"，不再看
	// "有没有任何积压行"。前者是Own判据，后者会把别人的残留当成自己的。
	// 排队前先把自己的那句落成"积压行"（BatchID==0＝还不在任何批次里）。
	// 为什么必须在等待之前：旧实现在 processLocally 入口就无条件 append，所以停在积压等待里
	// 的请求天生在 pending 里有一行，先醒的那个接管时能把它一起收进批2；我删掉入口盲写之后，
	// 排队中的请求在 pending 里**根本没有行**，于是先醒那路只收到自己一句，后醒那路只能再开一批
	// ——连发 5 条从 2 个批次退化成 3 个（TestRealMergeFiveMessagesTwoReplies 实测抓到）。
	// 落行点是"排队可见性"，入批动作仍由下面两处共同完成：接管那轮的标记循环（按 ReqID 登记
	// claimedByReq）+ 唯一裁决点 ensureOwnPendingRow（同 ReqID 幂等，绝不写出第二行）。
	ensureOwnPendingRow(q, reqID, content, 0)

	for {
		for q.processing {
			q.cond.Wait()
		}
		// 查归属登记表：ReqID==0（老版本转交来的行）与"从未被别人收走"两种情况都拿不到格，
		// 表里也从不写 key=0 的行，故这里无需额外分支判空。
		claimEpoch, claimed := q.claimedByReq[reqID]
		if !claimed {
			break // 没人收走我这句：本请求自己开下一批
		}
		delete(q.claimedByReq, reqID)
		// D8 同款判据：只等自己那一格，代际推进或 processing 释放都不再干等
		for q.processing && q.replyByEpoch[claimEpoch] == "" && q.epoch == claimEpoch {
			q.cond.Wait()
		}
		reply = q.replyByEpoch[claimEpoch]
		if reply == "" {
			reply = q.lastReply
		}
		if reply != "" {
			// 残项3(2026-09-26)：随批取回复的等待者返回 0=未知——此处读 q.mergeCount 拿到的是
			// **醒来时刻**那个批次的计数（收走我这句的批次早已交卷，计数早被下一批改写过），
			// 与"我这句所在那批有几条"无关。等待路径统一按 EnqueueAndWait 头注的口径回 0。
			locked = false
			q.mu.Unlock()
			log.Printf("[合并队列] 客户%s 消息已被代%d那一批收走，随批取回回复: %q%s", k, claimEpoch, logx.Safe(content, 40), traceTag(traceID))
			return "", false, reply, 0, false, 0, claimEpoch
		}
		// 极端时序：收走我这句的那一批被超时自愈清掉且从未交卷——消息不能就此蒸发。
		// 回到循环顶部重新争处理权，走下面的接管路径；我那行已被收账收走（pending 里查不到
		// ReqID），ensureOwnPendingRow 会补写一行——这是"丢了要重来"的唯一补写入口。
	}

	// 当前批次完成了，本消息成为下一批的第一个
	q.processing = true
	q.batchClosed = false // P0-7：积压接管即新批次开账
	q.currentBatch++      // 新批次
	q.epoch++             // P1-19：积压接管同样递增代际
	q.processingStartedAt = time.Now()
	q.lastReply = ""
	q.deadlineExpired = false
	// P0-3 修复：不依赖入队时的下标（自愈清残留后索引会漂移导致 panic），
	// 改为把所有仍积压(BatchID==0)的消息统一纳入新批次。
	for i := range q.pending {
		if q.pending[i].BatchID == 0 {
			q.pending[i].BatchID = q.currentBatch
			// 残项1(2026-09-25)：这一行若属于**另一路**仍停在积压等待里的请求（ReqID>0 且
			// 不是自己），登记归属代际——它醒来据此改等本批回复。两种行不登记：
			//   · ReqID==0：自愈补写行、老版本实例转交来的消息，本地没有对应等待者；
			//   · ReqID==reqID：本请求自己就是本批处理者，登记了没人消费（残留项要等
			//     两次 SetReply 才被剪掉，把"归属表用完即空"这条不变式弄脏）。
			// 残项2 真修(2026-09-26)：跨实例接管的自己那句现在也带 ReqID，故这一轮就会
			// 把它标进本批，下面的裁决点据此判定"已在批内"而不再补写第二行。
			if rid := q.pending[i].ReqID; rid != 0 && rid != reqID {
				if q.claimedByReq == nil {
					q.claimedByReq = map[uint64]uint64{}
				}
				q.claimedByReq[rid] = q.epoch
			}
		}
	}
	// 唯一裁决点（残项2 真修，替代旧 `if !inBatch { 无条件补写一句 }`）。
	// 旧判据 `inBatch` 问的是"有没有**任何**积压行"，两方向都错：
	//   · 批里有别人的残留行、没有我的 → 不补写 → 自己那句静默留在 BatchID==0 等下次接管；
	//   · 那一行此刻其实还挂在 Redis 列表里（absorb 退回过）→ 补写 → 同一句两处各一份归属。
	// 现在按**自己的 ReqID** 问"我这句在不在批里"：在就只对齐批次号，不在才补写。
	ensureOwnPendingRow(q, reqID, content, q.currentBatch)
	startCount := syncMergeCount(q, q.currentBatch)
	locked = false
	myEpoch := q.epoch
	myBatch := q.currentBatch
	q.mu.Unlock()

	log.Printf("[合并队列] 客户%s 积压消息拿到处理权(批次%d,代%d): %d条: %q%s", k, myBatch, myEpoch, startCount, logx.Safe(content, 40), traceTag(traceID))
	merged, waitDuration, batchCount := s.waitForMerge(q, k)
	log.Printf("[合并队列] 客户%s 积压批合并完成(批次%d): %d条消息, 等待%.1fs, 内容: %q%s", k, myBatch, batchCount, waitDuration.Seconds(), logx.Safe(merged, 40), traceTag(traceID))
	return merged, true, "", waitDuration, false, batchCount, myEpoch
}

// waitRemotely 远程等待路径（本实例未抢到锁，消息转交处理者实例）
// 协议：
//  1. LPUSH 消息到 mq:pending:{k}，处理者会在窗口内/窗口关闭时吸收进当前批次
//  2. 轮询 mq:lastseq:{k}，序号增长后读 mq:reply:{k}:{seq} 取回复
//  3. 锁消失（持有实例死亡）→ 尝试接管成为新处理者
//
// 已知边界：若转交消息到达时批次已满被"退回"，会随下一批处理，但本请求可能拿到
// 上一批的回复体（前端聊天记录为准，影响极小）；单实例模式无此问题。
// 例外（残项2 真修，2026-09-26）：**接管者自己那一句**即使批次已满也不退回 Redis，
// 而是就地留成本地待接管积压行——同一句话只能有一个归属地，两处各存一份就是双答。
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
					// 残项3(2026-09-26)：原先这里硬编码 mergeCount=1，是把"我不知道批内有几句"
					// 谎报成"就一句"。等待路径拿不到处理者实例的收账行数，真值只能由处理者
					// 那条返回（waitForMerge 收账行数）。现在返回 0=未知，并被下面的
					// EnqueueAndWait 文档注释固化为契约：0 不是"零句"也不是"一句"，
					// 禁止任何下游把等待路径的该值当有效条数消费。
					return "", false, r, 0, false, 0, repEpoch
				}
			}
		}

		// 锁消失且尚无新回复 → 原持有实例死亡，跳出循环去接管
		if !redisclient.LockExists(lockKey) {
			log.Printf("[合并队列] 客户%s 检测到处理者失联，尝试接管%s", k, traceTag(traceID))
			break
		}
	}

	// 接管：拿到锁则成为新处理者
	if h := redisclient.TryLock(lockKey, getProcessingLockTimeout(tidFromKey(k))); h != nil {
		q := s.getQueue(k)
		q.mu.Lock()
		q.redisLock = h
		q.mu.Unlock()
		// 残项2 真修(2026-09-26)：接管前先给本请求发一个归属号，交给 absorb 认领
		// "列表里哪一条是我刚 RPush 的那句"。旧写法把认领完全交给 absorb +
		// processLocally 的防御补写：absorb 因"本地批次未在 processing"把这句退回 Redis，
		// 而 processLocally 又把它补进本地 pending → 同一句话在 Redis 和本地各存一份归属，
		// 后续可能被两个批次各答一遍（客户看到双答、AI token 双烧）。
		myReqID := s.allocReqID(q)
		// D6 注(2026-09-16B)：与 EnqueueAndWait 抢锁段不同，接管场景本地队列可能正有
		// processing=true 的在途批次（web 请求），此处 absorb 能把死实例转交的消息并进
		// 该批次（真语义，非空转）——保留。抢锁段的同款调用已删（见 EnqueueAndWait D6 注释）。
		s.absorbRemotePending(k, q, myReqID, content)
		return s.processLocally(k, content, myReqID, traceID)
	}

	// P2-38 修复(2026-09-09)：极端场景——锁被别的实例抢先拿走但仍无回复。
	// 原逻辑"本地兜底处理"会再生成一遍 AI 回复 → 跨实例双回复+双计费。
	// 改为落一条降级提示，不让客户端空等，也绝不再触发第二次 AI 调用。
	log.Printf("[合并队列] 客户%s 远程等待超时且拿锁失败，落降级提示（不重复生成AI回复）", k)
	s.WriteDegradedNotice(tenantID, customerID, "系统繁忙，请稍等片刻再试一次")
	// 残项3(2026-09-26)：mergeCount 由硬编码 1 改为 0=未知（同上，等待路径无收账行数）。
	return content, false, "", 0, false, 0, 0
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

// ClaimReplyDelivery 以「会话:epoch:渠道」为维度认领发送权，保证同一条 AI 回复在一个合并批次内只投递一次；epoch=0（未启用纪元防栅）直接放行。
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
// 调用时机：跨实例接管时（waitRemotely，带自己的归属号）、合并窗口关闭收集批次前（不带）
// 超出合并上限的消息放回 Redis 列表，留给下一批（可能由其他实例处理）
//
// ownReqID/ownContent（残项2 真修，2026-09-26）：接管者**自己那一句**的归属号与内容。
// 列表里第一条与 ownContent 相等、且尚未认领过的行就是它（同客户重复发同一句时
// 认领第一处即可——两句话都是这个客户真实发的，各自都会被某个批次答一次，
// 认错的只是"哪一份算我的"，不会多答也不会少答）。
// 关键差别：**这一句即使当前批次装不下也不退回 Redis**，而是就地留成待接管积压行
// (BatchID=0)。退回等于让同一句话同时在"Redis 列表"和"本地 pending（随后的补写）"
// 两处各存一份归属，那正是双答的源头。
func (s *MessageQueueService) absorbRemotePending(k string, q *CustomerQueue, ownReqID uint64, ownContent string) {
	items := redisclient.DrainList("mq:pending:" + k)
	if len(items) == 0 {
		return
	}
	maxMerge := config.GlobalConfig.ReplySpeed.MaxMergeMessages

	q.mu.Lock()
	batch := q.currentBatch
	absorbed := 0
	ownPlaced := false // 自己那句是否已落到本地某一行上（只认领第一次出现）
	var leftover []string
	for _, content := range items {
		isOwn := ownReqID != 0 && ownContent != "" && !ownPlaced && content == ownContent
		// 未在处理中或已达上限：放回列表留给下一批（自己那句例外——见函数头注释）
		if !q.processing || q.mergeCount >= maxMerge {
			if isOwn {
				q.pending = append(q.pending, PendingMessage{
					Content:    content,
					ReceivedAt: time.Now(),
					BatchID:    0, // 未开批：积压行，由 processLocally 的裁决点标进本批
					ReqID:      ownReqID,
				})
				ownPlaced = true
				continue
			}
			leftover = append(leftover, content)
			continue
		}
		var rid uint64
		if isOwn {
			rid = ownReqID
			ownPlaced = true
			// 并进的是**别人正在跑的在途批次**（本函数只在 processing=true 时走到这里）。
			// 必须同时在归属登记表留下"我这句被这一代收走了"，否则接管者随后发现批次已满
			// 落到积压等待分支时，只会被那一批收账收走自己的行、却查不到该等哪个代际，
			// 于是又开一个批次把自己那句**再答一遍**——与 claimedByReq 的既有用法语义一致。
			if q.claimedByReq == nil {
				q.claimedByReq = map[uint64]uint64{}
			}
			q.claimedByReq[ownReqID] = q.epoch
		}
		q.mergeCount++
		q.pending = append(q.pending, PendingMessage{
			Content:    content,
			ReceivedAt: time.Now(),
			BatchID:    batch,
			ReqID:      rid,
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
//     返回的 batchCount 是**本批实际收进几句**（收账时的行数），不是窗口计数器 mergeCount：
//     后者还要承担滑动窗口的"够不够条数"判据，接管场景下会与本批真实行数不一致（残项2）。
//     下游（D7 相似抑制前置、CalcHumanlikeDelay）要的是"这批几句话"，必须用这个返回值。
//     另一半不变式在 processLocally：同一句话在 pending 里至多一行（ensureOwnPendingRow），
//     否则本函数按 BatchID 收账时会同一句收进两行——"行数=条数"就又成了空话。
func (s *MessageQueueService) waitForMerge(q *CustomerQueue, k string) (mergedContent string, mergeWaitDuration time.Duration, batchCount int) {
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
	// ownReqID 传 0：这里收账的是**本批处理者**的窗口，转交来的消息都归别人所有，
	// 不存在"本调用自己的那一句"要认领（那一句在 waitRemotely 接管处已认领过）。
	if redisclient.IsEnabled() {
		s.absorbRemotePending(k, q, 0, "")
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
	// 残项2(2026-09-25)：本批真实条数以收账到的行数为数（mergeCount 是窗口计数器，
	// 接管/自愈场景下与行数不等，见函数头注释）
	batchCount = len(mergedBuilder)
	q.mu.Unlock()

	// 用换行连接多条消息（AI能看出来是连发的）
	merged := ""
	for i, c := range mergedBuilder {
		if i > 0 {
			merged += "\n"
		}
		merged += c
	}

	return merged, mergeWaitDuration, batchCount
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
	// D8 修复(2026-09-24)：回复额外按代际落一格，等待者读自己那格——见 replyByEpoch 字段注释。
	// 只保留当前与上一代：更老代际的等待者早已离场，而活跃客户的队列会长跑，
	// 不清就是单调增长的内存泄漏（pubEpoch<=1 时不做剪枝，防 uint 下溢把全表删空）。
	if q.replyByEpoch == nil {
		q.replyByEpoch = map[uint64]string{}
	}
	q.replyByEpoch[pubEpoch] = reply
	if pubEpoch > 1 {
		for e := range q.replyByEpoch {
			if e < pubEpoch-1 {
				delete(q.replyByEpoch, e)
			}
		}
		// 残项1(2026-09-25)：归属登记表与回复槽同窗剪枝——比"上一代"更早的批次，其等待者
		// 要么已经取回复离场，要么判据（q.epoch==claimEpoch）已不成立，留着只会单调增长。
		for rid, e := range q.claimedByReq {
			if e < pubEpoch-1 {
				delete(q.claimedByReq, rid)
			}
		}
	}
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
