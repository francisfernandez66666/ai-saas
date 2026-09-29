package main

import "log"

// flywheelRound 是「数据飞轮回流上报」每一轮要做的判断，从 main.go 的裸 ticker 里抽出来
// 单独成函数并全依赖注入（FIX-4，2026-09-29 端到端审计批）。
//
// 为什么不能留在 ticker 里：这一轮的对外副作用是「往 collector POST 审计增量」，
// 因此**刻意没有 HTTP 触发端点**（有端点等于给测试一条真外发通路，与 D3 催缴 sweep
// 「不翻总开关、状态机留给单测」同一口径）。可 FIX-4 修的正是"多实例会把同一批审计行
// 外发 N 次"——如果判据只是一句注释加一段读不出结果的代码，那这条修有没有真的生效，
// 谁也不知道。把裁决点抽成纯函数后，四条分支各有机器判据（见 flywheel_round_test.go）：
// 　① Redis 在场且抢到锁 → 上报恰好一次，且锁必须被释放（不释放 = 50 分钟内全集群不再上报，
// 　　这比重复外发更糟，所以释放也在判据里）；
// 　② Redis 在场但没抢到 → 一律不上报（这就是多实例去重本身）；
// 　③ 上报失败 → 游标 lastID 不推进（K8 语义：宁可下轮重发，不可漏账）；
// 　④ 无 Redis → 照常上报，但必须**如实告警一次**（单实例不该被拦死，而"多实例会重复外发"
// 　　这个前提必须看得见——静默的退化轨正是这批审计反复抓到的那一类缺陷）。
//
// 参数（全注入，便于逐路打中）：
//   - lastID：上一轮成功上报后的游标（in-out，成功才推进）
//   - redisOn：当前是否走 Redis 轨道（生产传 redisclient.IsEnabled）
//   - tryLock：尝试取选主锁，返回释放函数；返回 nil 表示没抢到
//   - warnNoRedis：无 Redis 轨道的告警（生产用 sync.Once 包一次）
//   - report：真正的上报动作，返回是否成功（生产传 billing.ReportAuditIncrement）
//   - maxID：读当前审计表最大 id，作为成功后的新游标
//     返回：本轮是否真的执行了上报（用于测试与日志，不参与业务分支）
func flywheelRound(
	lastID *uint,
	redisOn func() bool,
	tryLock func() func(),
	warnNoRedis func(),
	report func(uint) bool,
	maxID func() uint,
) bool {
	if lastID == nil || report == nil || maxID == nil {
		// 装配缺失属于编程错误：宁可如实报错，也不能"看起来跑了一轮"却什么都没发
		log.Printf("[飞轮回流] 装配缺失（lastID/report/maxID 为 nil），本轮跳过")
		return false
	}
	if redisOn != nil && redisOn() {
		if tryLock == nil {
			log.Printf("[飞轮回流] Redis 已启用但取锁函数缺失，本轮跳过（fail-closed：不确定谁在发就别发）")
			return false
		}
		release := tryLock()
		if release == nil {
			return false // 别的实例正在发：本轮跳过，这就是多实例去重
		}
		// defer 释放：上报里还有 safeRun 兜 panic，panic 也不能把锁留到 TTL 自然过期
		defer release()
		if report(*lastID) {
			*lastID = maxID()
			return true
		}
		return false
	}
	// 无 Redis 轨道：单实例照常直发，但把"多实例部署下会重复外发"如实说一次
	if warnNoRedis != nil {
		warnNoRedis()
	}
	if report(*lastID) {
		*lastID = maxID()
		return true
	}
	return false
}
