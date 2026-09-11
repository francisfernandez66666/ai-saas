// G-7 并发语义单测
// 覆盖：三桶扣减原子性、退款vs扣减竞态、epoch fencing、processLocally并发合并
package service

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---- G-7-1: 三桶扣减顺序验证（纯逻辑，无需 DB） ----

// mockTenantBilling 模拟租户三桶余额
type mockTenantBilling struct {
	freeBalance   int
	freeExpiresAt *time.Time
	monthlyQuota  int
	monthlyUsed   int
	tokenBalance  int
	mu            sync.Mutex
}

func (m *mockTenantBilling) deduct(tokens int) (fromFree, fromMonthly, fromBalance int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	rem := tokens

	// Bucket 3: Free trial
	freeAvail := m.freeBalance
	if m.freeExpiresAt != nil && m.freeExpiresAt.Before(time.Now()) {
		freeAvail = 0 // 过期归零
	}
	if freeAvail > 0 && rem > 0 {
		if freeAvail < rem {
			fromFree = freeAvail
		} else {
			fromFree = rem
		}
		rem -= fromFree
	}

	// Bucket 1: Monthly subscription
	monthlyAvail := m.monthlyQuota - m.monthlyUsed
	if monthlyAvail > 0 && rem > 0 {
		if monthlyAvail < rem {
			fromMonthly = monthlyAvail
		} else {
			fromMonthly = rem
		}
		rem -= fromMonthly
	}

	// Bucket 2: Prepaid balance
	if m.tokenBalance > 0 && rem > 0 {
		if m.tokenBalance < rem {
			fromBalance = m.tokenBalance
		} else {
			fromBalance = rem
		}
		rem -= fromBalance
	}

	m.freeBalance -= fromFree
	m.monthlyUsed += fromMonthly
	m.tokenBalance -= fromBalance
	return
}

func TestThreeBucketDeductionOrder(t *testing.T) {
	// 三桶都有余额，扣 50 token
	// free=30 → 扣30, rem=20 → monthly=100 → 扣20, rem=0 → balance 不扣
	b := &mockTenantBilling{freeBalance: 30, monthlyQuota: 100, monthlyUsed: 0, tokenBalance: 200}
	ff, fm, fb := b.deduct(50)
	if ff != 30 || fm != 20 || fb != 0 {
		t.Errorf("三桶扣减顺序: free=%d monthly=%d balance=%d, 期望 30/20/0", ff, fm, fb)
	}
	if b.freeBalance != 0 || b.monthlyUsed != 20 || b.tokenBalance != 200 {
		t.Errorf("余额更新: free=%d monthlyUsed=%d balance=%d, 期望 0/20/200",
			b.freeBalance, b.monthlyUsed, b.tokenBalance)
	}
}

func TestThreeBucketFreeExhaustedFallsToMonthly(t *testing.T) {
	// free 耗尽，扣减应走 monthly → balance
	b := &mockTenantBilling{freeBalance: 0, monthlyQuota: 100, monthlyUsed: 0, tokenBalance: 200}
	ff, fm, fb := b.deduct(50)
	if ff != 0 || fm != 50 || fb != 0 {
		t.Errorf("free耗尽后扣减: free=%d monthly=%d balance=%d, 期望 0/50/0", ff, fm, fb)
	}
}

func TestThreeBucketMonthlyExhaustedFallsToBalance(t *testing.T) {
	// free + monthly 都耗尽，走 balance
	b := &mockTenantBilling{freeBalance: 0, monthlyQuota: 100, monthlyUsed: 100, tokenBalance: 200}
	ff, fm, fb := b.deduct(50)
	if ff != 0 || fm != 0 || fb != 50 {
		t.Errorf("monthly耗尽后扣减: free=%d monthly=%d balance=%d, 期望 0/0/50", ff, fm, fb)
	}
}

func TestThreeBucketAllEmptyNoNegative(t *testing.T) {
	// 三桶全空，不应产生负数
	b := &mockTenantBilling{freeBalance: 0, monthlyQuota: 100, monthlyUsed: 100, tokenBalance: 0}
	ff, fm, fb := b.deduct(50)
	if ff != 0 || fm != 0 || fb != 0 {
		t.Errorf("三桶全空: free=%d monthly=%d balance=%d, 期望全0", ff, fm, fb)
	}
}

func TestThreeBucketFreeExpiredFallsToMonthly(t *testing.T) {
	// free 有余额但已过期，应跳过 free
	past := time.Now().Add(-time.Hour)
	b := &mockTenantBilling{freeBalance: 30, freeExpiresAt: &past, monthlyQuota: 100, monthlyUsed: 0, tokenBalance: 200}
	ff, fm, fb := b.deduct(50)
	if ff != 0 || fm != 50 || fb != 0 {
		t.Errorf("free过期后扣减: free=%d monthly=%d balance=%d, 期望 0/50/0", ff, fm, fb)
	}
}

// ---- G-7-2: 三桶并发扣减原子性（纯逻辑，无 DB） ----

func TestThreeBucketConcurrentDeduction(t *testing.T) {
	// 100 个 goroutine 并发扣 1 token，free=50, monthly=100, balance=1000
	// 总扣 100 token，余额应精确
	b := &mockTenantBilling{freeBalance: 50, monthlyQuota: 200, monthlyUsed: 0, tokenBalance: 1000}
	var wg sync.WaitGroup
	var totalFree, totalMonthly, totalBalance int32

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ff, fm, fb := b.deduct(1)
			atomic.AddInt32(&totalFree, int32(ff))
			atomic.AddInt32(&totalMonthly, int32(fm))
			atomic.AddInt32(&totalBalance, int32(fb))
		}()
	}
	wg.Wait()

	totalDeducted := int(totalFree) + int(totalMonthly) + int(totalBalance)
	if totalDeducted != 100 {
		t.Errorf("总扣减: %d, 期望 100 (free=%d monthly=%d balance=%d)",
			totalDeducted, totalFree, totalMonthly, totalBalance)
	}
	// 余额应一致
	if b.freeBalance != 50-int(totalFree) {
		t.Errorf("free余额: %d, 期望 %d", b.freeBalance, 50-int(totalFree))
	}
	if b.monthlyUsed != int(totalMonthly) {
		t.Errorf("monthlyUsed: %d, 期望 %d", b.monthlyUsed, int(totalMonthly))
	}
	if b.tokenBalance != 1000-int(totalBalance) {
		t.Errorf("balance余额: %d, 期望 %d", b.tokenBalance, 1000-int(totalBalance))
	}
}

// ---- G-7-3: epoch fencing 验证 ----

type mockEpochQueue struct {
	mu         sync.Mutex
	epoch      uint64
	lastReply  string
	processing bool
}

func (q *mockEpochQueue) setReply(reply string, epoch uint64) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if epoch != q.epoch {
		return false // stale epoch, reject
	}
	q.lastReply = reply
	q.processing = false
	return true
}

func TestEpochFencingStaleRejected(t *testing.T) {
	q := &mockEpochQueue{epoch: 5}
	// 旧 epoch 写入应被拒绝
	if q.setReply("stale reply", 3) {
		t.Error("stale epoch(3) 应被拒绝，但被接受")
	}
	if q.lastReply != "" {
		t.Error("stale epoch 写入后 lastReply 应为空")
	}
	// 当前 epoch 写入应成功
	if !q.setReply("current reply", 5) {
		t.Error("当前 epoch(5) 应被接受，但被拒绝")
	}
	if q.lastReply != "current reply" {
		t.Errorf("lastReply: %q, 期望 %q", q.lastReply, "current reply")
	}
}

func TestEpochFencingIncrement(t *testing.T) {
	q := &mockEpochQueue{epoch: 1}
	// epoch 1 成功
	if !q.setReply("r1", 1) {
		t.Fatal("epoch 1 应成功")
	}
	// epoch 递增后，旧 epoch 被拒
	q.mu.Lock()
	q.epoch++
	q.processing = true
	q.mu.Unlock()

	if q.setReply("stale", 1) {
		t.Error("epoch 1 在递增后应被拒绝")
	}
	if !q.setReply("current", 2) {
		t.Error("epoch 2 应被接受")
	}
}

// ---- G-7-4: processLocally 并发合并模拟 ----

func TestProcessLocallyConcurrentMerge(t *testing.T) {
	// 模拟 processLocally 的核心逻辑：第一个请求成为 processor，
	// 后续请求在 merge window 内追加
	type queue struct {
		mu           sync.Mutex
		cond         *sync.Cond
		processing   bool
		mergeCount   int
		maxMerge     int
		messages     []string
		batchID      int
		currentBatch int
		reply        string
	}

	q := &queue{maxMerge: 3}
	q.cond = sync.NewCond(&q.mu)

	// 模拟 processLocally 的追加逻辑
	tryMerge := func(msg string) (string, bool) {
		q.mu.Lock()
		defer q.mu.Unlock()

		q.messages = append(q.messages, msg)

		if !q.processing {
			// 成为 processor
			q.processing = true
			q.currentBatch++
			q.batchID = q.currentBatch
			q.mergeCount = 1
			return "", true // 表示成为 processor
		}

		if q.mergeCount < q.maxMerge {
			// 追加到当前批次
			q.mergeCount++
			q.cond.Signal()
			return "", false
		}

		// 超过合并上限
		return "", false
	}

	// 模拟 processor 完成
	_ = func() string {
		q.mu.Lock()
		defer q.mu.Unlock()
		result := ""
		for _, m := range q.messages {
			if result != "" {
				result += " "
			}
			result += m
		}
		q.reply = result
		q.processing = false
		q.messages = nil
		q.cond.Broadcast()
		return result
	}

	// 并发发送 5 条消息
	var wg sync.WaitGroup
	results := make([]bool, 5)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, isProcessor := tryMerge("msg")
			results[idx] = isProcessor
		}(i)
	}
	wg.Wait()

	// 应只有 1 个 processor
	processorCount := 0
	for _, r := range results {
		if r {
			processorCount++
		}
	}
	if processorCount != 1 {
		t.Errorf("processor 数量: %d, 期望 1", processorCount)
	}
}

// ---- G-7-5: refund vs deduction 竞态 ----

type mockRefundable struct {
	mu          sync.Mutex
	balance     int
	deducted    int
	refunded    int
	deductCount int
	refundCount int
}

func (m *mockRefundable) deduct(amount int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.balance >= amount {
		m.balance -= amount
		m.deducted += amount
		m.deductCount++
		return true
	}
	return false
}

func (m *mockRefundable) refund(amount int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deducted >= amount {
		m.deducted -= amount
		m.balance += amount
		m.refunded += amount
		m.refundCount++
		return true
	}
	return false
}

func TestRefundVsDeductRace(t *testing.T) {
	// 并发扣减+退款，余额不应为负
	m := &mockRefundable{balance: 100}
	var wg sync.WaitGroup

	// 50 个 goroutine 扣 2 token
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.deduct(2)
		}()
	}

	// 50 个 goroutine 退 1 token
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.refund(1)
		}()
	}

	wg.Wait()

	// 余额不应为负
	if m.balance < 0 {
		t.Errorf("余额为负: %d", m.balance)
	}
	// 扣减+退款后 balance + deducted 应等于初始 100
	total := m.balance + m.deducted
	if total != 100 {
		t.Errorf("balance(%d) + deducted(%d) = %d, 期望 100", m.balance, m.deducted, total)
	}
}
