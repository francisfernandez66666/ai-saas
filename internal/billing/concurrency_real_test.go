// G-7 并发语义真实单测（资金侧，2026-09-24 欠账批一改写）
//
// 这批用例的存在理由：`internal/service/concurrency_test.go` 里的并发用例原先各自
// 在测试文件内**重写了一遍被测逻辑**（mockTenantBilling.deduct / mockRefundable / 内联 queue 闭包），
// 然后断言那份 mock 行为正确。生产代码把三桶扣减改错、甚至把 `clause.Locking{Strength:"UPDATE"}`
// 整行删掉，这些用例照样全绿——它们测的是自己写的玩具，不是仓库里的钱。
// 本文件把同一组判据搬到真实函数上：DeductTokensActual / deductTokensInTx / MarkOrderRefunded。
//
// 真实用例能抓到而 mock 抓不到的三类事：
//  1. 行锁缺失导致的**丢失更新**（扣减写的是"读到的值-本次扣减"这种绝对值，没有 FOR UPDATE
//     并发双方就会各写各的，账面余额凭空多出来）；
//  2. 三桶全空时哨兵错误回滚（M1 资金红线：宁可报错也不把账扣成负数、也不静默吞账）；
//  3. 退款回收与扣减同时发生时的守恒——不管谁先谁后，"扣掉的 + 回收的 = 发出去的"必须成立。
//
// 依赖 dev 库（testutil.SetupTestDB，CI 缺库判 Fatal、本地缺库跳过并计数）。
// 开关用 runtimecfg.NewStaticService 在**进程内**替换单例，不写 system_configs 表——
// 写表会连带影响同一台机器上正在跑的服务实例。
package billing

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/testutil"
)

// openRealBilling 打开"引擎总闸 + 强制计费"两道闸，返回恢复函数。
// 两道闸必须同时开：H4 双开关合一后，只开 billing_enforced 而不开总闸时扣减整体 no-op
// （这正是灰度语义，也是"用例其实没测到扣减"的经典假绿入口）。
func openRealBilling(t *testing.T) func() {
	t.Helper()
	old := runtimecfg.DefaultSystemConfigService
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(map[string]string{
		"token_billing_enabled": "true",
		"billing_enforced":      "true",
	}, nil)
	return func() { runtimecfg.DefaultSystemConfigService = old }
}

// mkBucketTenant 建一个三桶余额可控的租户
func mkBucketTenant(t *testing.T, free int64, freeExp *time.Time, quota, used, balance int64) uint {
	t.Helper()
	tid := testutil.CreateTenant(t)
	if err := db.DB.Model(&model.Tenant{}).Where("id = ?", tid).Updates(map[string]interface{}{
		"free_token_balance":    free,
		"free_token_expires_at": freeExp,
		"monthly_token_quota":   quota,
		"monthly_token_used":    used,
		"token_balance":         balance,
	}).Error; err != nil {
		t.Fatalf("设三桶余额失败: %v", err)
	}
	return tid
}

// readBuckets 读回三桶当前值（断言唯一入口，避免各处手写字段名）
func readBuckets(t *testing.T, tid uint) model.Tenant {
	t.Helper()
	var tn model.Tenant
	if err := db.DB.First(&tn, tid).Error; err != nil {
		t.Fatalf("读租户三桶失败: %v", err)
	}
	return tn
}

// TestRealThreeBucketDeductionOrder 真实扣减顺序：③免费 → ①月度 → ②余额
func TestRealThreeBucketDeductionOrder(t *testing.T) {
	testutil.SetupTestDB(t)
	defer openRealBilling(t)()
	tid := mkBucketTenant(t, 30, nil, 100, 0, 200)
	defer testutil.CleanupTenant(t, tid)

	// 扣 50：免费桶只有 30，剩下 20 走月度，余额桶一分不动
	if err := DeductTokensActual(tid, 50); err != nil {
		t.Fatalf("真实扣减失败: %v", err)
	}
	got := readBuckets(t, tid)
	if got.FreeTokenBalance != 0 || got.MonthlyTokenUsed != 20 || got.TokenBalance != 200 {
		t.Errorf("三桶扣减结果 free=%d monthlyUsed=%d balance=%d，期望 0/20/200",
			got.FreeTokenBalance, got.MonthlyTokenUsed, got.TokenBalance)
	}
	// 扣得动就不该留挂账行（M1：挂账只在扣不动时出现）
	var debts int64
	db.DB.Model(&model.UsageFlushRetry{}).Where("tenant_id = ?", tid).Count(&debts)
	if debts != 0 {
		t.Errorf("足额扣减却留下 %d 行挂账，账目口径被写坏", debts)
	}
}

// TestRealThreeBucketFreeExpiredSkipped 免费桶过期必须整桶跳过并清零（僵尸余额不能被扣到）
func TestRealThreeBucketFreeExpiredSkipped(t *testing.T) {
	testutil.SetupTestDB(t)
	defer openRealBilling(t)()
	past := time.Now().Add(-time.Hour)
	tid := mkBucketTenant(t, 30, &past, 100, 0, 200)
	defer testutil.CleanupTenant(t, tid)

	if err := DeductTokensActual(tid, 50); err != nil {
		t.Fatalf("真实扣减失败: %v", err)
	}
	got := readBuckets(t, tid)
	// 过期免费桶直接归零（顺带清掉，不留"账面有 30 实际用不了"的糊涂账），50 全走月度
	if got.FreeTokenBalance != 0 || got.MonthlyTokenUsed != 50 || got.TokenBalance != 200 {
		t.Errorf("免费桶过期扣减 free=%d monthlyUsed=%d balance=%d，期望 0/50/200",
			got.FreeTokenBalance, got.MonthlyTokenUsed, got.TokenBalance)
	}
}

// TestRealConcurrentDeductNoLostUpdate 100 个 goroutine 真实并发扣减：总量守恒且逐桶精确。
//
// 这条是 FOR UPDATE 行锁的**唯一**运行时证据：扣减写回用的是
// `token_balance = 读到的值 - 本次扣减` 这种绝对值，锁一旦被删，两个并发事务读到同一个
// 起点、各写一次，账面就会凭空多出被覆盖掉的那部分。mock 版永远测不到这件事。
func TestRealConcurrentDeductNoLostUpdate(t *testing.T) {
	testutil.SetupTestDB(t)
	defer openRealBilling(t)()
	// 总容量 50+200+1000=1250，总需求 100×3=300；按③→①→②顺序：免费 50 全清、
	// 月度配额 200 全吃、剩余 50 落到余额桶（1000 → 950）。
	tid := mkBucketTenant(t, 50, nil, 200, 0, 1000)
	defer testutil.CleanupTenant(t, tid)

	var wg sync.WaitGroup
	errs := make([]error, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = DeductTokensActual(tid, 3)
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Fatalf("第%d发扣减在容量充足时失败: %v", i, e)
		}
	}
	got := readBuckets(t, tid)
	// 300 的需求：免费 50 → 月度 200（配额全用完）→ 余额 50
	if got.FreeTokenBalance != 0 {
		t.Errorf("免费桶期望 0，实际 %d", got.FreeTokenBalance)
	}
	if got.MonthlyTokenUsed != 200 {
		t.Errorf("月度已用期望 200（配额 200 全部吃掉），实际 %d", got.MonthlyTokenUsed)
	}
	if got.TokenBalance != 950 {
		t.Errorf("余额桶期望 950（1000-50），实际 %d —— 数字偏大即丢失更新（行锁失效）", got.TokenBalance)
	}
}

// TestRealLostUpdateHappensWithoutRowLock 反证用例：证明上一条的等值断言有牙齿。
//
// 正向用例断"并发后余额=950"，可如果 PostgreSQL 本身就会把并发写串行化（即：删掉
// FOR UPDATE 也不会丢更新），那条断言就是在空转——生产代码怎么改都绿。
// 本用例把 deductTokensInTx 的"读余额 → 算 → 写回"照抄一遍，**故意不加行锁**，
// 并用栅栏强制两方都先读到同一个起点再各自写回。结果必须丢一次扣减（停在 900 而非 800）。
// 只有这里"确实会丢"，才能证明 950 那个数字是靠行锁挣来的。
// 被测对象是无锁副本，不动生产代码，也不与 DeductTokensActual 争抢同一租户。
func TestRealLostUpdateHappensWithoutRowLock(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := mkBucketTenant(t, 0, nil, 0, 0, 1000)
	defer testutil.CleanupTenant(t, tid)

	var wg sync.WaitGroup
	bothRead := make(chan struct{}, 2)
	goAhead := make(chan struct{})
	var writes int64
	var mu sync.Mutex
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var tn model.Tenant
			// 与生产扣减同构的读法，唯一区别：没有 clause.Locking{Strength:"UPDATE"}
			err := db.DB.Select("id, token_balance").First(&tn, tid).Error
			// 先报到再判错：读失败也放行栅栏，否则主 goroutine 会永久卡在 <-bothRead
			bothRead <- struct{}{}
			if err != nil {
				t.Errorf("读余额失败: %v", err)
				return
			}
			<-goAhead // 等对方也读完，确保两边读到的是同一个 1000
			if err := db.DB.Model(&model.Tenant{}).Where("id = ?", tid).
				Update("token_balance", tn.TokenBalance-100).Error; err != nil {
				t.Errorf("写回失败: %v", err)
				return
			}
			mu.Lock()
			writes++
			mu.Unlock()
		}()
	}
	for i := 0; i < 2; i++ {
		<-bothRead
	}
	close(goAhead)
	wg.Wait()
	// 前置自检：两笔写都落了，"余额=900"才等价于"第二笔的 100 被覆盖"；
	// 少一笔的话这一节的等式会在 900==900 上假绿。
	if writes != 2 {
		t.Fatalf("反证用例只落了 %d 笔写，两笔都必须在场", writes)
	}

	if got := readBuckets(t, tid); got.TokenBalance != 900 {
		t.Errorf("无锁读-算-写必须丢一次扣减（期望 900），实际 %d —— "+
			"若这里是 800 说明当前环境不会丢更新，TestRealConcurrentDeductNoLostUpdate 的等值断言即为空转",
			got.TokenBalance)
	}
}

// TestRealConcurrentOverCommitNeverNegative 需求远超容量时并发扣减：三桶任何一桶都不得为负。
//
// 资金红线（2026-09-23 批三 M1）：余额可以被扣光、扣不动的那部分以哨兵错误回滚或转挂账，
// 但绝不能扣成负数——负数等于凭空给客户送额度。
func TestRealConcurrentOverCommitNeverNegative(t *testing.T) {
	testutil.SetupTestDB(t)
	defer openRealBilling(t)()
	tid := mkBucketTenant(t, 0, nil, 50, 0, 50) // 容量 100
	defer testutil.CleanupTenant(t, tid)

	var wg sync.WaitGroup
	var refused, ok int
	var mu sync.Mutex
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := DeductTokensActual(tid, 5) // 需求 300
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				refused++
			} else {
				ok++
			}
		}()
	}
	wg.Wait()
	got := readBuckets(t, tid)
	if got.FreeTokenBalance < 0 || got.MonthlyTokenUsed < 0 || got.TokenBalance < 0 {
		t.Fatalf("三桶出现负数: free=%d monthlyUsed=%d balance=%d", got.FreeTokenBalance, got.MonthlyTokenUsed, got.TokenBalance)
	}
	if got.MonthlyTokenUsed > got.MonthlyTokenQuota {
		t.Fatalf("月度已用 %d 超过配额 %d（超用没被拦）", got.MonthlyTokenUsed, got.MonthlyTokenQuota)
	}
	if got.TokenBalance != 0 {
		t.Errorf("容量 100 被 300 的需求打满，余额桶应归零，实际 %d", got.TokenBalance)
	}
	// 必须"有成功的、也有被拒的"：全成功说明闸门形同虚设，全被拒说明扣减链断了
	if ok == 0 || refused == 0 {
		t.Errorf("超卖场景应既有扣成功也有被拒，实际 成功=%d 被拒=%d", ok, refused)
	}
}

// TestRealRefundVsDeductConservation 退款回收与扣减并发：账面守恒，且不允许丢失更新。
//
// 判据为什么可以写成"最终一定是 0"：②桶只有这一笔订单发的 1000 token，出路只有两条——
// 被扣掉、或被退款回收。所以不论两者以什么顺序交错，最终余额都必须是 0。
// 一旦锁缺失，扣减会拿退款前的旧值做绝对写回，余额"复活"成 900 这类正数——正是本断言要抓的。
func TestRealRefundVsDeductConservation(t *testing.T) {
	testutil.SetupTestDB(t)
	defer openRealBilling(t)()
	tid := mkBucketTenant(t, 0, nil, 0, 0, 1000)
	defer testutil.CleanupTenant(t, tid)

	pkg := &model.Package{Code: fmt.Sprintf("ut_race_%d", time.Now().UnixNano()), Name: "并发退款增量包",
		PType: "increment", TokenAmount: 1000, PriceCents: 19900, Enabled: true}
	if err := db.DB.Create(pkg).Error; err != nil {
		t.Fatalf("建包失败: %v", err)
	}
	oid := createPaidIncrementOrder(t, tid, pkg)

	var wg sync.WaitGroup
	// 8 路扣减（各 100）与 1 路退款回收同时打
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 与退款抢同一行锁：扣空后被拒是预期，别的错误码不是
			if err := DeductTokensActual(tid, 100); err != nil && err != errTokenDebtUnsettled {
				t.Errorf("非预期扣减错误: %v", err)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, _, err := MarkOrderRefunded(oid); err != nil {
			t.Errorf("退款回收失败: %v", err)
		}
	}()
	wg.Wait()

	got := readBuckets(t, tid)
	if got.TokenBalance != 0 {
		t.Errorf("②桶最终应为 0（1000 全部扣掉或回收），实际 %d —— 偏大即退款与扣减互相覆盖", got.TokenBalance)
	}
	if got.TokenBalance < 0 {
		t.Errorf("②桶为负数 %d", got.TokenBalance)
	}
	var o model.BillingOrder
	if err := db.DB.First(&o, oid).Error; err != nil {
		t.Fatalf("读订单失败: %v", err)
	}
	if o.Status != "refunded" {
		t.Errorf("订单应为 refunded，实际 %s", o.Status)
	}
}

// TestRealDeductGatesNoOp 两道闸任一未开时扣减必须是 no-op（灰度语义，不能偷偷扣钱）。
// 反向自证：不开闸跑同一笔扣减，余额纹丝不动；开闸后同一笔立刻生效。
func TestRealDeductGatesNoOp(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := mkBucketTenant(t, 0, nil, 0, 0, 500)
	defer testutil.CleanupTenant(t, tid)

	old := runtimecfg.DefaultSystemConfigService
	defer func() { runtimecfg.DefaultSystemConfigService = old }()

	// 只开总闸、不开强制：留痕不扣（M5 灰度）
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(map[string]string{
		"token_billing_enabled": "true", "billing_enforced": "false",
	}, nil)
	if err := DeductTokensActual(tid, 100); err != nil {
		t.Fatalf("灰度未强制时应返回 nil: %v", err)
	}
	if got := readBuckets(t, tid); got.TokenBalance != 500 {
		t.Errorf("灰度未强制却被扣减: balance=%d，期望 500", got.TokenBalance)
	}
	// 双闸齐开：同一笔必须真扣
	// 注意不要写成 `openRealBilling(t)()`——返回值是**恢复函数**，当场调用等于立刻
	// 把闸门关回去，后面的扣减会走灰度 no-op 分支，余额纹丝不动（本用例首跑就是这么红的）。
	// 恢复交给函数开头已 defer 的 old 单例。
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(map[string]string{
		"token_billing_enabled": "true", "billing_enforced": "true",
	}, nil)
	if err := DeductTokensActual(tid, 100); err != nil {
		t.Fatalf("开闸后扣减失败: %v", err)
	}
	if got := readBuckets(t, tid); got.TokenBalance != 400 {
		t.Errorf("开闸后应扣到 400，实际 %d", got.TokenBalance)
	}
}
