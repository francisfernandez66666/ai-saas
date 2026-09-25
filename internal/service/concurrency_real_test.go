// G-7 并发语义真实单测（合并队列侧，2026-09-24 欠账批一）
//
// 与 `concurrency_test.go` 的分工：那份文件里的 mockTenantBilling / mockEpochQueue /
// 内联 queue 闭包各自在测试内**重写了一遍被测逻辑**，断言的是玩具的行为——把
// message_queue.go 的 `if !q.processing` 判据删掉，它照样全绿。资金侧的真实版本
// 见 `internal/billing/concurrency_real_test.go`，本文件补队列侧：全部打在
// MessageQueueService 的真实方法上（EnqueueAndWait / processLocally / waitForMerge /
// SetReply / 超时自愈），并可在 `go test -race` 下跑。
//
// 真实用例能抓到而 mock 抓不到的三类事：
//  1. 并发下**处理者唯一性**（判据写错就会有两个 goroutine 同时认为自己拿到处理权，
//     客户收到两条不相关回复）；
//  2. 批次收集时**消息不丢不重**（pending 的批次边界一旦被改坏，会有消息静默蒸发
//     或同一句话被回复两次）；
//  3. 超时自愈与代际 fencing 的**联动**（自愈放行新处理者后，慢死的旧处理者回来交卷
//     必须被丢弃——这条只有真实代际流转才测得到，mock 版是自己手写 epoch=5 假装存在）。
//
// 窗口/锁超时都压到 1s 级：runtimecfg.NewStaticService 只在**进程内**替换单例，
// 不写 system_configs 表，因此不会影响同一台机器上正在跑的服务实例。
package service

import (
	"strings"
	"sync"
	"testing"
	"time"

	"ai-scrm/config"
	"ai-scrm/internal/runtimecfg"
)

// queueTestConfig 把合并窗口压到 1s、上限按参数设定，返回恢复函数。
// 两个来源都要管：窗口走热配（waitForMerge 读 runtimecfg），上限走 config.GlobalConfig。
func queueTestConfig(t *testing.T, maxMerge int, extraKV map[string]string) func() {
	t.Helper()
	oldCfg := config.GlobalConfig
	oldRC := runtimecfg.DefaultSystemConfigService
	kv := map[string]string{"merge_window_seconds": "1"}
	for k, v := range extraKV {
		kv[k] = v
	}
	config.GlobalConfig = &config.Config{ReplySpeed: config.ReplySpeedConfig{MaxMergeMessages: maxMerge}}
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(kv, nil)
	return func() {
		config.GlobalConfig = oldCfg
		runtimecfg.DefaultSystemConfigService = oldRC
	}
}

// qResult 一次 EnqueueAndWait 的真实返回（七元组按需取用）
type qResult struct {
	merged   string
	process  bool
	reply    string
	count    int
	epoch    uint64
	isSimple bool
}

// enqueueAsync 在独立 goroutine 里打一次入队，结果写回带缓冲 channel（永不阻塞发送方）。
// 用非简单消息（长句），确保走的是合并队列而不是简单消息快速通道。
func enqueueAsync(svc *MessageQueueService, tid, cid uint, content string) <-chan qResult {
	ch := make(chan qResult, 1)
	go func() {
		m, p, r, _, simple, n, e := svc.EnqueueAndWait(tid, cid, content, "")
		ch <- qResult{merged: m, process: p, reply: r, count: n, epoch: e, isSimple: simple}
	}()
	return ch
}

// waitQ 等待一个入队结果，超时即判失败（挂死=等待者永远等不到回复，比错值更严重）
func waitQ(t *testing.T, ch <-chan qResult, who string) qResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(15 * time.Second):
		t.Fatalf("%s 的 EnqueueAndWait 15s 未返回（等待者挂死或处理者未交卷）", who)
		return qResult{}
	}
}

// taggedResult 一路入队的结果 + 它是哪一路（标记而非下标：处理者不一定是第 1 路）
type taggedResult struct {
	who string
	qResult
}

// enqueueTagged 与 enqueueAsync 同语义，但把结果汇到**同一个** channel 并带上路别标记。
//
// 为什么必须这样收：并发下"谁先抢到处理权"由调度决定，按固定下标收（先收 w1[0] 当处理者）
// 会在"第 1 路其实是等待者"时先卡在一个还没被唤醒的等待者上——本用例首跑即 15s 超时，
// 是测试自己的死锁，不是产品缺陷（TestRealConcurrentMergeSingleProcessor 同坑同修）。
func enqueueTagged(svc *MessageQueueService, tid, cid uint, who, content string, res chan<- taggedResult) {
	go func() {
		m, p, r, _, simple, n, e := svc.EnqueueAndWait(tid, cid, content, "")
		res <- taggedResult{who: who, qResult: qResult{merged: m, process: p, reply: r, count: n, epoch: e, isSimple: simple}}
	}()
}

// drainWithin 在 d 内收 n 路结果，收不满就把"还没回来的那几路"点名报出来
// （挂死类缺陷必须报"谁没回"，只报"超时"等于让人回日志里猜）。
func drainWithin(t *testing.T, res <-chan taggedResult, n int, d time.Duration, expectWho []string) []taggedResult {
	t.Helper()
	got := map[string]bool{}
	var out []taggedResult
	deadline := time.After(d)
	for len(out) < n {
		select {
		case r := <-res:
			got[r.who] = true
			out = append(out, r)
		case <-deadline:
			var missing []string
			for _, w := range expectWho {
				if !got[w] {
					missing = append(missing, w)
				}
			}
			t.Fatalf("%s 内只收到 %d/%d 路结果，未返回：%v（等待者挂死=本批回复永久丢失）", d, len(out), n, missing)
			return nil
		}
	}
	return out
}

// TestRealConcurrentMergeSingleProcessor 8 个 goroutine 真并发打同一客户：只允许 1 个处理者，
// 其余 7 个全部合并进同一批、并各自拿到处理者写回的同一条回复。
//
// 这是"连发 8 条只回 1 条"这条产品承诺在单测层的唯一真实证据（此前只有通道冒烟在整链上验）。
// 判据设计：上限设为 100，故本轮不存在积压分支，8 条必然全并入批1；处理者由窗口自然到期收账。
func TestRealConcurrentMergeSingleProcessor(t *testing.T) {
	defer queueTestConfig(t, 100, nil)()
	svc := NewMessageQueueService()
	tid, cid := uint(31), uint(94701)

	const n = 8
	// 结果汇到**同一个** channel：处理者不一定是第 1 路，若按固定下标顺序收，
	// 就会先卡在一个还没被唤醒的等待者上（本用例首跑即 15s 超时——测试自己的死锁，不是产品缺陷）。
	res := make(chan qResult, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		content := "并发消息甲" + string(rune('A'+i)) + "，请详细介绍一下你们产品的报价与交付周期"
		go func(c string) {
			<-start
			m, p, r, _, simple, cnt, e := svc.EnqueueAndWait(tid, cid, c, "")
			res <- qResult{merged: m, process: p, reply: r, count: cnt, epoch: e, isSimple: simple}
		}(content)
	}
	close(start)

	// 处理者必然在窗口到期（≈1s）后收账返回；等待者在它交卷前不可能返回。
	var proc *qResult
	for proc == nil {
		select {
		case r := <-res:
			if r.process {
				rc := r
				proc = &rc
				break
			}
			t.Fatalf("处理者还没收账就有请求带回复返回（reply=%q）——等待者被提前唤醒", r.reply)
		case <-time.After(10 * time.Second):
			t.Fatal("10s 内没有任何一路成为处理者并收账返回")
		}
	}
	if proc.isSimple {
		t.Fatal("并发合并路径不得被判为简单消息（简单通道不合并）")
	}
	lines := strings.Split(strings.TrimSpace(proc.merged), "\n")
	if len(lines) != n {
		t.Fatalf("批次应合并 %d 条，实际 %d 条：%q", n, len(lines), proc.merged)
	}
	if proc.count != n {
		t.Errorf("mergeCount 应为 %d，实际 %d", n, proc.count)
	}
	// 不丢不重：8 个标记各出现且仅出现一次
	for i := 0; i < n; i++ {
		marker := "并发消息甲" + string(rune('A'+i))
		if got := strings.Count(proc.merged, marker); got != 1 {
			t.Errorf("%s 在合并结果中出现 %d 次（期望 1 次：0=丢消息，>1=重复入批）", marker, got)
		}
	}

	const replyText = "统一回复：已收到全部消息"
	svc.SetReply(tid, cid, proc.epoch, replyText)

	// 剩余 7 路：都该拿到同一条回复，且不应有人二次处理
	waiters := 0
	for i := 0; i < n-1; i++ {
		r := waitQ(t, res, "等待者")
		if r.process {
			t.Errorf("有请求二次接管处理权（shouldProcess=true，merged=%q）——处理者唯一性被破坏", r.merged)
			continue
		}
		waiters++
		if r.reply != replyText {
			t.Errorf("等待者拿到的回复=%q，期望 %q", r.reply, replyText)
		}
	}
	if waiters != n-1 {
		t.Errorf("等待者数量 %d，期望 %d", waiters, n-1)
	}
	// 交卷后队列归零：processing 释放、pending 清空（残留=下一个请求会拿到脏状态）
	q := svc.getQueue(queueKey(tid, cid))
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.processing {
		t.Error("SetReply 后 processing 仍为 true")
	}
	if len(q.pending) != 0 {
		t.Errorf("SetReply 后 pending 残留 %d 条", len(q.pending))
	}
}

// TestRealMergeOverflowTwoBatchesNoLoss 超过合并上限（3 条上限打第 4 条）：
// 前 3 条走批1，第 4 条积压成批2，**两批各一个处理者、4 条消息一条不丢一条不重**。
//
// 真实用例才走得到这条路：processLocally 的积压接管分支依赖"当前批次仍在 processing"
// 这一运行时状态，mock 版用一个闭包变量假装，等于没测。
//
// 本用例同时是 **D8 丢唤醒缺陷（2026-09-24 修）** 的回归锁：批1 的 SetReply 一广播，
// 批2 的接管者和批1 的两个等待者同时被唤醒抢锁，谁先抢到由调度决定——接管者一旦先跑，
// 就会把共享的 lastReply 清空并重开 processing，还没抢到锁的等待者醒来时读到
// "processing=true 且回复为空"，判定"我这一批还没好"重新挂起，**批1 回复就此永久丢失**
// （15s 超时，点名甲/乙/丙 中未返回的那几路）。修法=回复按代际各占一格，故这里断言
// 等待者拿到的必须是**逐字的"批1回复"**（不是"非空"也不是"任一批次的回复"）。
//
// 为什么只打到第 4 条而不是 5 条：本批用 5 条探到一处**已登记待决策的重复回复缺陷**
// （见文件末 DEFECT-G7-DUP-TAKEOVER 注释）。缺陷修好前，"5 条=2 批"这条断言必红，
// 故这里锁住 4 条这段无争议语义，不替缺陷背书。
func TestRealMergeOverflowTwoBatchesNoLoss(t *testing.T) {
	defer queueTestConfig(t, 3, nil)()
	svc := NewMessageQueueService()
	tid, cid := uint(32), uint(94702)

	// 波1：3 条——一路成处理者，另 2 条把批次撑到上限（窗口随即收账）
	res := make(chan taggedResult, 4)
	enqueueTagged(svc, tid, cid, "甲", "积压测试第一句，想了解全系的配置差异", res)
	enqueueTagged(svc, tid, cid, "乙", "积压测试第二句，另外想知道保养周期", res)
	enqueueTagged(svc, tid, cid, "丙", "积压测试第三句，还有保险方案怎么算", res)

	var b1 taggedResult
	for b1.who == "" {
		select {
		case r := <-res:
			if r.process {
				b1 = r
			} else {
				t.Fatalf("批1 尚未关账就有等待者带回复返回（who=%s reply=%q）——被提前唤醒", r.who, r.reply)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("10s 内没有任何一路成为批1处理者")
		}
	}
	if got := strings.Count(strings.TrimSpace(b1.merged), "积压测试"); got != 3 {
		t.Errorf("批1应含 3 条，实际合并结果 %q", b1.merged)
	}

	// 波2：批1 已关账且仍被持有（模拟 AI 生成中），这 1 条只能进积压
	enqueueTagged(svc, tid, cid, "丁", "积压测试第四句，麻烦一并报价", res)
	time.Sleep(500 * time.Millisecond) // 确保它已落进 pending，才会被批2收走

	svc.SetReply(tid, cid, b1.epoch, "批1回复")

	// 交卷后三路该一起回来：丁=批2处理者，另外两路=批1等待者
	others := make([]string, 0, 3)
	for _, w := range []string{"甲", "乙", "丙", "丁"} {
		if w != b1.who {
			others = append(others, w)
		}
	}
	results := drainWithin(t, res, 3, 12*time.Second, others)

	var b2 taggedResult
	waiters := 0
	for _, r := range results {
		if r.who == "丁" {
			b2 = r
			continue
		}
		if r.process {
			t.Errorf("批1等待者 %s 二次接管处理权（merged=%q）——处理者唯一性被破坏", r.who, r.merged)
			continue
		}
		waiters++
		// 逐字等值（不是"非空"）：拿到批2 的回复同样是缺陷，只是换了个形态
		if r.reply != "批1回复" {
			t.Errorf("%s 拿到的回复=%q，期望逐字 %q", r.who, r.reply, "批1回复")
		}
	}
	if waiters != 2 {
		t.Errorf("批1等待者数量 %d，期望 2", waiters)
	}
	if b2.who != "丁" {
		t.Fatal("丁 的结果未回来")
	}
	if !b2.process {
		t.Fatalf("第四条应积压接管成为批2处理者，实际 shouldProcess=false reply=%q", b2.reply)
	}
	if b2.epoch == b1.epoch {
		t.Error("批2 必须携带新一代际（旧代际交卷会被 fencing 丢弃，两条链无法区分）")
	}
	if got := strings.Count(strings.TrimSpace(b2.merged), "积压测试"); got != 1 {
		t.Errorf("批2应含 1 条，实际 %q", b2.merged)
	}
	if strings.Contains(b2.merged, "第一句") {
		t.Errorf("批2 串进了批1 的消息（批次边界失效）：%q", b2.merged)
	}
	svc.SetReply(tid, cid, b2.epoch, "批2回复")
	// 守恒：4 条消息在两个批次里各出现一次，总数=4
	total := strings.Count(b1.merged, "积压测试") + strings.Count(b2.merged, "积压测试")
	if total != 4 {
		t.Errorf("两批合计 %d 条，期望 4（少=丢消息，多=重复入批）", total)
	}
	q := svc.getQueue(queueKey(tid, cid))
	q.mu.Lock()
	residue := len(q.pending)
	q.mu.Unlock()
	if residue != 0 {
		t.Errorf("两批都交卷后 pending 残留 %d 条（孤儿消息会污染下一批）", residue)
	}
	// D8 附带断言：按代际发布的回复槽位不得单调增长（活跃客户长跑=内存泄漏）
	q.mu.Lock()
	slots := len(q.replyByEpoch)
	q.mu.Unlock()
	if slots > 2 {
		t.Errorf("replyByEpoch 残留 %d 格，期望 ≤2 格（只该保留当前与上一代）", slots)
	}
}

// TestRealSelfHealThenStaleReplyDiscarded processing 锁超时自愈放行新处理者后，
// 慢死的旧处理者回来交卷必须被真实代际 fencing 丢弃——旧回复既不落 lastReply，
// 也不许释放新处理者的锁（否则客户收到两条不相关回复，且第二批的等待者被提前唤醒）。
//
// 与 mock 版 TestEpochFencingStaleRejected 的差别：这里的 epoch 不是测试手写的常量，
// 而是 processLocally 真实流转出来的两代；自愈分支不生效时新代际根本拿不到。
func TestRealSelfHealThenStaleReplyDiscarded(t *testing.T) {
	defer queueTestConfig(t, 3, map[string]string{"processing_lock_timeout": "1"})()
	svc := NewMessageQueueService()
	tid, cid := uint(33), uint(94703)

	first := waitQ(t, enqueueAsync(svc, tid, cid, "卡死测试第一句，请介绍交付周期"), "旧处理者")
	if !first.process {
		t.Fatal("第一条应成为处理者，shouldProcess=false")
	}
	// 不交卷 = 模拟处理者挂死（AI 长尾）；processing 一直被持有，等锁超时自愈
	time.Sleep(1800 * time.Millisecond)

	second := waitQ(t, enqueueAsync(svc, tid, cid, "卡死测试第二句，另外想了解金融方案"), "新处理者")
	if !second.process {
		t.Fatalf("锁超时后新请求应自愈接管，实际 shouldProcess=false reply=%q", second.reply)
	}
	if second.epoch <= first.epoch {
		t.Errorf("自愈后代际必须推进：新 %d 旧 %d", second.epoch, first.epoch)
	}
	if !strings.Contains(second.merged, "第二句") || strings.Contains(second.merged, "第一句") {
		t.Errorf("自愈批应只含新消息，实际 %q", second.merged)
	}

	// 旧处理者此时才"慢死回来"交卷：必须被丢弃
	q := svc.getQueue(queueKey(tid, cid))
	svc.SetReply(tid, cid, first.epoch, "陈旧回复")
	q.mu.Lock()
	staleReply, stillProcessing := q.lastReply, q.processing
	q.mu.Unlock()
	if staleReply != "" {
		t.Errorf("旧代际 SetReply 竟写入了 lastReply=%q（fencing 失效）", staleReply)
	}
	if !stillProcessing {
		t.Error("旧代际 SetReply 竟释放了新处理者的 processing 锁")
	}

	// 新代际正常交卷：状态应当归零
	svc.SetReply(tid, cid, second.epoch, "正确回复")
	q.mu.Lock()
	freshReply, processingAfter := q.lastReply, q.processing
	q.mu.Unlock()
	if freshReply != "正确回复" {
		t.Errorf("当前代际 SetReply 未生效，lastReply=%q", freshReply)
	}
	if processingAfter {
		t.Error("当前代际交卷后 processing 仍为 true")
	}
}

// TestRealSimpleMessageSerializesPerCustomer 简单消息快速通道：同客户并发时持锁者唯一，
// 且 SimpleMessageDone 后锁一定释放（P1-4 看门狗之外的正常路径，靠 -race 兜数据竞争）。
func TestRealSimpleMessageSerializesPerCustomer(t *testing.T) {
	defer queueTestConfig(t, 3, nil)()
	svc := NewMessageQueueService()
	tid, cid := uint(34), uint(94704)

	// "你好" 是典型简单消息：走独立串行锁，不进合并批次
	const n = 5
	var wg sync.WaitGroup
	simples := make([]bool, n)
	procs := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, p, _, _, simple, _, _ := svc.EnqueueAndWait(tid, cid, "你好", "")
			simples[idx], procs[idx] = simple, p
			svc.SimpleMessageDone(tid, cid)
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("简单消息串行锁未在 10s 内放开（死锁）")
	}
	for i := 0; i < n; i++ {
		if !simples[i] || !procs[i] {
			t.Errorf("第%d路简单消息应直接返回处理权，实际 isSimple=%v shouldProcess=%v", i, simples[i], procs[i])
		}
	}
	q := svc.getQueue(queueKey(tid, cid))
	q.mu.Lock()
	holding := q.simpleProcessing
	q.mu.Unlock()
	if holding {
		t.Error("全部 SimpleMessageDone 后 simpleProcessing 仍为 true（锁泄漏）")
	}
}

// DEFECT-G7-DUP-TAKEOVER（2026-09-24 欠账批一探到，已登记待决策，本批未动生产代码）
//
// 现象：合并上限 3、同客户连发 5 条时，第 5 条会被答两次——它既进了批2 的合并结果
// （批2 merged = 第四句\n第五句），又在批2 交卷后被唤醒、自己接管成批3
// （shouldProcess=true，merged = 第五句）。客户收到两条内容重叠的回复，AI token 双烧。
// 实测证据（窗口 1s / 上限 3，本批探针跑出的真实返回）：
//
//	返回1: idx=0 process=true merged="一号\n二号\n三号" epoch=1
//	返回2: idx=3 process=true merged="四号\n五号"       epoch=2  ← 批2 已含第 5 条
//	返回3: idx=4 process=true merged="五号"             epoch=3  ← 第 5 条又被单独处理一次
//
// 根因：processLocally 的积压分支 `for q.processing { Wait }` 醒来后**无条件**把自己立成
// 新批的第一个（message_queue.go:533-564）。第 5 条的 pending 行早被批2 的收账循环改成了
// 批2 的 BatchID，于是 `inBatch == false` 那段防御分支把它**再补进** pending 一遍。
//
// 为何不在本批直接修：修法要新增"这条 pending 属于哪个请求"的归属标识（当前只有 BatchID，
// 反查不到请求本身），属队列核心状态机改动，且与 D5 投递认领 / epoch fencing 联动，
// 必须配双实例 Redis 回归，塞进"补单测"这批做等于蒙着改。
//
// 修法前置：pending 行带请求侧 msgID 随返回值传出，或醒来时按"我的行已被并入某批且该批
// 尚未交卷"改走等回复分支；两条路都要补冒烟断言"连发 5 条只回 2 条"。
