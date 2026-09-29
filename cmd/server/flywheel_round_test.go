package main

import "testing"

// flywheelRound 的分支判据（FIX-4，2026-09-29 端到端审计批）。
// 这一轮没有 HTTP 触发端点（有端点＝给测试一条真往 collector 外发的通路），
// 所以多实例去重这条新加的腿只能在这里逐路打中——只写注释等于没验收。
// 变异口径（每条都对应一句"改了实现就会红"）：
//   - 把"没抢到锁就 return"删掉 → ②红（同一批审计行又被外发一次，正是本批要修的缺陷）；
//   - 把 defer release 改成不上报时才释放 → ③红（上报失败后锁被留在手里到 TTL，全集群静默 50 分钟）；
//   - 把成功判据改成"不管 report 结果都推进游标" → ④红（漏账：失败的增量永久跳过）；
//   - 把无 Redis 分支的告警去掉 → ⑤红（退化轨不可见，本批审计反复抓到的同一形态）。
func TestFlywheelRound(t *testing.T) {
	// ① 抢到锁：恰好上报一次、游标推进、锁释放
	t.Run("抢到锁则上报一次并释放", func(t *testing.T) {
		var got []uint
		released := 0
		cur := uint(10)
		ok := flywheelRound(&cur,
			func() bool { return true },
			func() func() { return func() { released++ } },
			func() { t.Fatal("Redis 在场不应走退化告警分支") },
			func(id uint) bool { got = append(got, id); return true },
			func() uint { return 42 },
		)
		if !ok || len(got) != 1 || got[0] != 10 {
			t.Errorf("应恰好以旧游标上报一次，实得 ran=%v calls=%v ids=%v", ok, len(got), got)
		}
		if cur != 42 {
			t.Errorf("上报成功后游标应推进到当前最大 id=42，实得 %d", cur)
		}
		if released != 1 {
			t.Errorf("锁必须释放恰好一次（不释放=全集群 50 分钟不再上报），实得 %d", released)
		}
	})

	// ② 没抢到锁：一次都不上报，游标也不动
	t.Run("没抢到锁则完全不上报", func(t *testing.T) {
		calls := 0
		cur := uint(7)
		ok := flywheelRound(&cur,
			func() bool { return true },
			func() func() { return nil }, // 别的实例持有锁
			func() { t.Fatal("Redis 在场不应走退化告警分支") },
			func(uint) bool { calls++; return true },
			func() uint { return 99 },
		)
		if ok || calls != 0 {
			t.Errorf("没抢到锁却上报了：ran=%v calls=%d（多实例重复外发的现场）", ok, calls)
		}
		if cur != 7 {
			t.Errorf("本轮没上报，游标不应推进，实得 %d", cur)
		}
	})

	// ③ 上报失败：游标不动，但锁照样释放
	t.Run("上报失败也要释放锁", func(t *testing.T) {
		released := 0
		cur := uint(3)
		ok := flywheelRound(&cur,
			func() bool { return true },
			func() func() { return func() { released++ } },
			func() { t.Fatal("Redis 在场不应走退化告警分支") },
			func(uint) bool { return false },
			func() uint { return 88 },
		)
		if ok {
			t.Error("report 返回 false 时本轮不得报成功")
		}
		if released != 1 {
			t.Errorf("失败分支同样必须释放锁，实得释放次数=%d", released)
		}
		if cur != 3 {
			t.Errorf("失败时游标必须留在原地以便下轮重试，实得 %d", cur)
		}
	})

	// ④ 成功判据只认 report：推进值取自 maxID，而非"report 的入参+1"之类的臆造
	t.Run("新游标只由 maxID 决定", func(t *testing.T) {
		cur := uint(5)
		flywheelRound(&cur,
			func() bool { return true },
			func() func() { return func() {} },
			func() { t.Fatal("Redis 在场不应走退化告警分支") },
			func(uint) bool { return true },
			func() uint { return 5 }, // 表里没有新行：游标应原样
		)
		if cur != 5 {
			t.Errorf("无新增审计行时游标应保持 5，实得 %d", cur)
		}
	})

	// ⑤ 无 Redis：照常上报（单实例不得被拦死），且退化告警必须响
	t.Run("无Redis时照常上报并如实告警", func(t *testing.T) {
		warned := 0
		calls := 0
		cur := uint(1)
		ok := flywheelRound(&cur,
			func() bool { return false },
			func() func() { t.Fatal("无 Redis 时不该去取锁"); return nil },
			func() { warned++ },
			func(uint) bool { calls++; return true },
			func() uint { return 9 },
		)
		if !ok || calls != 1 {
			t.Errorf("无 Redis 单实例应照常上报，实得 ran=%v calls=%d", ok, calls)
		}
		if warned != 1 {
			t.Errorf("退化轨道必须告警（本批审计的同一形态：静默退化＝缺陷藏起来），实得 %d 次", warned)
		}
		if cur != 9 {
			t.Errorf("游标应推进，实得 %d", cur)
		}
	})

	// ⑥ fail-closed：Redis 声明启用却拿不到取锁函数 → 本轮不得外发
	t.Run("取锁函数缺失时不外发", func(t *testing.T) {
		calls := 0
		cur := uint(2)
		ok := flywheelRound(&cur, func() bool { return true }, nil,
			func() { t.Fatal("这不是退化轨道，不该走告警分支") },
			func(uint) bool { calls++; return true },
			func() uint { return 100 },
		)
		if ok || calls != 0 {
			t.Errorf("不确定谁在发就不该发：ran=%v calls=%d", ok, calls)
		}
	})
}
