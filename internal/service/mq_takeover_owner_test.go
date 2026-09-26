// 接管路径「同一句话恰好一行、一行恰好一批」回归用例（2026-09-26 残项2 真修）
//
// 现场（用户报"连发五条偶发双答"的第二条根因）：一个客户连发多条消息时，系统先把
// 短时间内的来话攒成一批交给 AI 回答一次；多副本部署下"谁来攒这批"由 Redis 锁裁决，
// 抢到锁的那台处理、没抢到的把话递过去等回复。递话那台如果发现处理者失联，会**接管**
// ——自己去攒这一批。缺陷就出在接管这一步：接管者那句话本来还挂在 Redis 的待合并列表里，
// 而接管代码同时又"怕它丢了"在本地再补写一份。于是一句话有两个归属地，
// 可能被两个批次各答一遍——客户看到两条内容重叠的回复，AI token 双烧。
// 旧判据 `inBatch`（"待合并里有没有**任何**积压行"）两方向都错：
//   - 批里只有别人的残留行 → 判"已在" → 自己那句没进批 = **丢答**；
//   - 自己那句还挂在 Redis → 判"不在" → 本地再写一份 = **双答**。
//
// 修法：三条分支（开新批 / 并进在途批 / 积压接管）落自己那一句时统一走 ensureOwnPendingRow
// 这一个裁决点，判据换成**自己的归属号 ReqID**；跨实例 absorb 认领到自己那句后
// **不再退回 Redis**（absorbRemotePending 的 ownReqID 参数）。
//
// 用例设计（按项目"护栏要配反证"的口径）：每条正向都配一条反向，防止实现退化成
// "把两句并成一句"（那也会让正向变绿）：
//  1. 已在 pending 里有我那行 → 收账恰好一条（双答方向）；
//  2. pending 里只有别人的积压行 → 我那行也必须进批（丢答方向）；
//  3. 两路不同 ReqID 的同内容 → 必须两行（防裁决点用内容当判据把两句吞成一句）；
//  4. 窗口计数器 mergeCount 与批内行数同源（残项2 的另一半：计数不得虚高）。
//
// 全部打在 MessageQueueService 的真实方法上（processLocally / waitForMerge / absorbRemotePending），
// 窗口压到 1s 由 queueTestConfig 完成，可在 -race 下跑。
package service

import (
	"strings"
	"testing"
	"time"

	"ai-scrm/internal/redisclient"
)

// seedPendingRow 直接往队列里摆一行，用来复刻"absorb 之后 / 上一批残留之后"的现场。
// 白盒写 pending 是刻意的：这些状态在单实例下没有公开入口能造出来
// （跨实例 absorb 需要真 Redis，超时自愈要等 600s），而本次修的正是这些状态被消费的方式。
func seedPendingRow(q *CustomerQueue, reqID, batchID uint64, content string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pending = append(q.pending, PendingMessage{
		Content:    content,
		ReceivedAt: time.Now(),
		BatchID:    batchID,
		ReqID:      reqID,
	})
}

// countRowsWithReqID 队列里属于某个归属号的行数（>1 即"同一句话写了两份"）。
func countRowsWithReqID(q *CustomerQueue, reqID uint64) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, m := range q.pending {
		if m.ReqID == reqID {
			n++
		}
	}
	return n
}

// takeoverProcResult 跑一次"接管者"处理路径并收账返回（processLocally 是接管链的真身）。
func takeoverProcResult(t *testing.T, svc *MessageQueueService, k, content string, reqID uint64) qResult {
	t.Helper()
	ch := make(chan qResult, 1)
	go func() {
		m, p, r, _, simple, cnt, e := svc.processLocally(k, content, reqID, "")
		ch <- qResult{merged: m, process: p, reply: r, count: cnt, epoch: e, isSimple: simple}
	}()
	select {
	case res := <-ch:
		if !res.process {
			t.Fatalf("接管者没能成为处理者（shouldProcess=false，reply=%q）", res.reply)
		}
		return res
	case <-time.After(15 * time.Second):
		t.Fatal("接管者 15s 未收账返回（窗口挂死）")
		return qResult{}
	}
}

// TestTakeoverOwnedRowNotDuplicated 正向（双答方向）：自己那一句**已经**在 pending 里
// （absorb 认领后留下的积压行，BatchID==0），接管批必须恰好收它一条。
//
// 旧代码在这里会写第二行：函数入口对 appendOwn=false 不落行、开批时只标 msgIdx（=-1 空标），
// 而 `inBatch` 因"有一条 BatchID==0 的行"为真 → 不补写 → 那句留在批外等下次接管；
// 换成 absorb 已标进旧批次号的现场则是收账收到两行。两种都是这一条要钉死的形状。
func TestTakeoverOwnedRowNotDuplicated(t *testing.T) {
	defer queueTestConfig(t, 100, nil)()
	svc := NewMessageQueueService()
	tid, cid := uint(61), uint(94901)
	k := queueKey(tid, cid)
	q := svc.getQueue(k)

	const ownReqID = uint64(7)
	own := "接管这一句：周六下午两点到店可以吗，麻烦帮我确认下顾问时间"
	seedPendingRow(q, ownReqID, 0, own) // 复刻"absorb 已认领、批次尚未开账"的积压行

	res := takeoverProcResult(t, svc, k, own, ownReqID)
	if got := strings.Count(res.merged, "接管这一句"); got != 1 {
		t.Errorf("接管批里自己那句出现 %d 次（期望 1：0=丢答，2=双答）：%q", got, res.merged)
	}
	if n := len(strings.Split(strings.TrimSpace(res.merged), "\n")); n != 1 {
		t.Errorf("接管批应恰好 1 条，实际 %d 条：%q", n, res.merged)
	}
	// 收账后 pending 不得残留同一归属号的行（残留=下一批还会答它一遍）
	if n := countRowsWithReqID(q, ownReqID); n != 0 {
		t.Errorf("收账后 pending 仍残留归属号%d的行 %d 条，下一批会再答一遍", ownReqID, n)
	}
	svc.SetReply(tid, cid, res.epoch, "好的，周六两点已排")
}

// TestTakeoverWithOthersBacklogMustLandOwn 正向（丢答方向）+ 反证：pending 里只有**别人**的
// 积压行（另一路等待者还没醒来），自己的那句没在（死掉的实例已从 Redis 取走却没答）。
// 此时"我这句没进批"必须被补写进批，而不是被 `inBatch=true` 掩盖掉。
//
// 这条就是反向对照：把裁决点写回"有没有任何积压行"，本用例的 own 计数会变成 0 而红；
// 而把裁决点写成"内容相同就合并"（另一种偷工实现），上一条用例会红、这条也红（两句被并成一句）。
func TestTakeoverWithOthersBacklogMustLandOwn(t *testing.T) {
	defer queueTestConfig(t, 1, nil)() // 上限 1：让本请求必然落到"积压接管"那条分支
	svc := NewMessageQueueService()
	tid, cid := uint(62), uint(94902)
	k := queueKey(tid, cid)
	q := svc.getQueue(k)

	const (
		ownReqID   = uint64(11)
		otherReqID = uint64(12)
	)
	own := "接管补写这一句：预算二十万，想看纯电SUV的续航和质保政策"
	other := "对手积压那一句：那你们充电桩安装要另外收费吗，麻烦一起说明"

	// 现场摆成"上一批正满着、本机在排队"：批1 已在处理且已到上限（maxMerge=1），
	// pending 里只有**别人**的积压行，我自己那句不在（死掉的实例把它从 Redis 取走后没答）。
	q.mu.Lock()
	q.processing = true
	q.currentBatch = 1
	q.epoch = 1
	q.mergeCount = 1
	q.processingStartedAt = time.Now()
	q.mu.Unlock()
	seedPendingRow(q, otherReqID, 0, other)
	// 代批1 交卷释放处理权，唤醒排队的本请求去接管下一批
	go func() {
		time.Sleep(300 * time.Millisecond)
		svc.SetReply(tid, cid, 1, "批1的统一回复")
	}()

	res := takeoverProcResult(t, svc, k, own, ownReqID)
	if got := strings.Count(res.merged, "接管补写这一句"); got != 1 {
		t.Errorf("自己那句出现 %d 次（期望 1：0=被别人的行掩盖成丢答，2=重复补写）：%q", got, res.merged)
	}
	if got := strings.Count(res.merged, "对手积压那一句"); got != 1 {
		t.Errorf("别人的积压句出现 %d 次（期望 1）：%q", got, res.merged)
	}
	lines := strings.Split(strings.TrimSpace(res.merged), "\n")
	if len(lines) != 2 {
		t.Fatalf("本批应 2 条（自己+对手积压），实际 %d 条：%q", len(lines), res.merged)
	}
	// 残项2 的口径：返回的条数与批次行数同源
	if res.count != len(lines) {
		t.Errorf("mergeCount=%d 与批次实际 %d 条分家了（D7 抑制前置读的就是它）", res.count, len(lines))
	}
	// 别人的行必须登记归属，它醒来才改等本批回复而不是再开一批（残项1 机制不得被本次改动带坏）
	q.mu.Lock()
	claimEpoch, claimed := q.claimedByReq[otherReqID]
	q.mu.Unlock()
	if !claimed || claimEpoch != res.epoch {
		t.Errorf("对手积压行未登记归属代际（claimed=%v epoch=%d 期望 %d），它会再开一批双答", claimed, claimEpoch, res.epoch)
	}
	svc.SetReply(tid, cid, res.epoch, "两款都有现货，充电桩含在交付里")
}

// TestEnsureOwnPendingRowIsPerRequest 裁决点自身的三条不变式（纯函数级，不看窗口）：
// 同 ReqID 幂等、不同 ReqID 不得互吞、批次标记只对齐不重复写。
func TestEnsureOwnPendingRowIsPerRequest(t *testing.T) {
	defer queueTestConfig(t, 100, nil)()
	svc := NewMessageQueueService()
	k := queueKey(uint(63), uint(94903))
	q := svc.getQueue(k)
	const stmt = "同一句话：我想了解一下分期方案有哪些"

	// ① 第一次落行：无中生有
	if !ensureOwnPendingRow(q, 21, stmt, 1) {
		t.Error("归属号21首次落行应返回 true（本次补写）")
	}
	// ② 第二次同 ReqID：必须幂等（这是双答的结构性封堵点）
	if ensureOwnPendingRow(q, 21, stmt, 2) {
		t.Error("同归属号第二次落行仍补写 → 同一句话两行，双答根因复活")
	}
	if n := countRowsWithReqID(q, 21); n != 1 {
		t.Fatalf("归属号21应有且仅有 1 行，实际 %d 行", n)
	}
	// ③ 批次标记必须跟着裁决点走（否则收账收不到它）
	q.mu.Lock()
	batch := q.pending[0].BatchID
	q.mu.Unlock()
	if batch != 2 {
		t.Errorf("归属号21那行批次标记=%d，期望对齐到 2（未对齐=收账收不到=丢答）", batch)
	}
	// ④ 反证：不同 ReqID 的同内容必须两行——裁决点若按内容判，这里会并成一行
	if ensureOwnPendingRow(q, 22, stmt, 2) {
		// 正常：另一路请求各有一行
	} else {
		t.Error("归属号22被并入归属号21那行：不同请求不得互吞（客户连发同一句会少答一句）")
	}
	q.mu.Lock()
	rows := len(q.pending)
	sameContent := 0
	for _, m := range q.pending {
		if m.Content == stmt {
			sameContent++
		}
	}
	q.mu.Unlock()
	if rows != 2 || sameContent != 2 {
		t.Errorf("两路同内容请求应各留一行（行数%d 同内容%d，期望 2/2）", rows, sameContent)
	}
}

// TestSyncMergeCountDerivesFromRows 窗口计数器与行数同源（残项2 的另一半）。
// 故意先把它写成虚高的 9，重算必须回到真实行数——旧实现是各分支手工 ++/--，
// 接管场景会"行没进批却 +1"（曾报 mergeCount=2 而批内实际 1 条）。
func TestSyncMergeCountDerivesFromRows(t *testing.T) {
	defer queueTestConfig(t, 100, nil)()
	svc := NewMessageQueueService()
	q := svc.getQueue(queueKey(uint(64), uint(94904)))

	seedPendingRow(q, 31, 5, "本批第一条：麻烦介绍下这款车的辅助驾驶配置")
	seedPendingRow(q, 32, 5, "本批第二条：另外问一下置换补贴现在还有额度吗")
	seedPendingRow(q, 33, 6, "下一批的那条：我再确认下交付周期是多久")

	q.mu.Lock()
	q.mergeCount = 9 // 污染：模拟"计数与行数分家"
	got := syncMergeCount(q, 5)
	actual := q.mergeCount
	q.mu.Unlock()

	if got != 2 || actual != 2 {
		t.Errorf("批次5重算得 %d（mergeCount=%d），期望 2：计数必须由本批行数派生", got, actual)
	}
}

// TestWaiterMergedCountIsSnapshotAtMergeTime 「并进在途批」的等待者返回的条数
// 必须是**入批那一刻**本批的行数，不是醒来时刻的窗口计数器（残项2 真修的第三条分支口径）。
//
// 为什么值得单独立一条：等待者 parked 期间，队里可能发生下一批开账、absorb 收编、
// 超时自愈清行——`q.mergeCount` 是**当时那个批次**的窗口计数器，醒来再读它，
// 报出去的就是"别人那批的条数"。旧写法 `waitedCount := q.mergeCount` 正是这个形状。
// 本用例用"parked 之后把计数器污染成 9"来判定读的是哪一份：
//   - 新实现返回 2（处理者那一句 + 我自己这一句，入批时算好）；
//   - 改回读醒来时刻的计数器即返回 9 而红（实测：反证见 FIXLOG_2026-09-26_QUEUE_TAKEOVER.md）。
func TestWaiterMergedCountIsSnapshotAtMergeTime(t *testing.T) {
	defer queueTestConfig(t, 100, nil)() // 上限放宽：让本请求必然走"并进在途批"分支
	svc := NewMessageQueueService()
	tid, cid := uint(66), uint(94906)
	k := queueKey(tid, cid)
	q := svc.getQueue(k)

	// 现场：批1 正在处理中，pending 里已有处理者那一句
	q.mu.Lock()
	q.processing = true
	q.currentBatch = 1
	q.epoch = 1
	q.mergeCount = 1
	q.processingStartedAt = time.Now()
	q.mu.Unlock()
	seedPendingRow(q, 51, 1, "批1处理者那一句：想了解全系配置的差异和价格区间")

	resCh := make(chan qResult, 1)
	go func() {
		m, p, r, d, simple, cnt, e := svc.processLocally(k, "等待者那一句：另外问一下保养周期是多久", 0, "")
		_ = d // 等待时长不是本用例的判据（另有用例覆盖）
		resCh <- qResult{merged: m, process: p, reply: r, isSimple: simple, count: cnt, epoch: e}
	}()
	time.Sleep(400 * time.Millisecond) // 确保它已入批并 parked

	// parked 期间污染窗口计数器：模拟"下一批开账/收编把 q.mergeCount 改写成别的值"
	q.mu.Lock()
	q.mergeCount = 9
	q.mu.Unlock()
	svc.SetReply(tid, cid, 1, "批1的统一回复")

	var res qResult
	select {
	case res = <-resCh:
	case <-time.After(15 * time.Second):
		t.Fatal("等待者 15s 未从批1 的交卷中醒来")
	}
	if res.process {
		t.Errorf("等待者二次接管处理权（shouldProcess=true）——同一批会生成两条回复")
	}
	if res.reply != "批1的统一回复" {
		t.Errorf("等待者拿到的回复=%q，期望逐字 %q", res.reply, "批1的统一回复")
	}
	if res.count != 2 {
		t.Errorf("等待者返回的条数=%d，期望 2（入批时刻本批行数：处理者+自己；读到 9 说明读的是醒来时刻的窗口计数器）", res.count)
	}
	if res.epoch != 1 {
		t.Errorf("等待者返回的代际=%d，期望 1（必须是自己并入那一批的代际，D5 投递认领按它认领）", res.epoch)
	}
}

// TestAbsorbOwnRowStaysLocalWhenBatchFull 跨实例认领（absorb）不得把接管者自己那句退回 Redis。
// 需要真 Redis（DrainList 是 Redis 原语），连不上即 Skip——与同目录其它多实例用例同一口径。
func TestAbsorbOwnRowStaysLocalWhenBatchFull(t *testing.T) {
	enableRealRedis(t)
	cleanMQKeys(t)
	defer cleanMQKeys(t)
	defer queueTestConfig(t, 100, nil)()

	svc := NewMessageQueueService()
	tid, cid := uint(65), uint(94905)
	k := queueKey(tid, cid)
	q := svc.getQueue(k)

	const ownReqID = uint64(41)
	own := "接管认领这一句：周六到店看车，需要提前预约哪位顾问"
	other := "别人转交的那一句：顺便问下有现车还是需要提前订车吗"
	// 模拟 waitRemotely：自己那句也走 Redis 转交（RPush 保 FIFO）
	redisclient.RPush("mq:pending:"+k, other)
	redisclient.RPush("mq:pending:"+k, own)

	// 现场：本地没有开批（q.processing=false）→ 旧实现把所有行退回 Redis，
	// 自己那句于是"在 Redis 有一份、随后本地补写又有一份"。
	q.mu.Lock()
	q.processing = false
	q.mu.Unlock()
	svc.absorbRemotePending(k, q, ownReqID, own)

	if n := redisclient.LLen("mq:pending:" + k); n != 1 {
		t.Errorf("Redis 待合并列表残留 %d 条（期望 1：只有别人的那句该退回，自己那句必须留本地）", n)
	}
	if n := countRowsWithReqID(q, ownReqID); n != 1 {
		t.Errorf("本地 pending 里自己那句 %d 行（期望 1：0=退回 Redis 后又被补写的双答源头，>1=重复认领）", n)
	}
	q.mu.Lock()
	var ownBatch uint64
	var otherLocal int
	for _, m := range q.pending {
		if m.ReqID == ownReqID {
			ownBatch = m.BatchID
		}
		if m.Content == other {
			otherLocal++
		}
	}
	q.mu.Unlock()
	if ownBatch != 0 {
		t.Errorf("自己那句被标进批次%d，期望 0（本地没开批时只能留作待接管积压行）", ownBatch)
	}
	if otherLocal != 0 {
		t.Errorf("别人的那句被本地收编 %d 行，期望 0（未开批时应退回 Redis 交给处理者）", otherLocal)
	}
}
