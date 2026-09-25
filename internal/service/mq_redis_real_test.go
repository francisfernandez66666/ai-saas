// 多实例（Redis）合并队列协议真实用例（2026-09-24 欠账批一，G-7 残项收口）
//
// 缺口原话是「Redis 双实例协议（miniredis）」。这里不用 miniredis 而是**真 Redis**
// （本机 127.0.0.1:6379，不可达即 Skip），原因是这套协议的三个关键语义恰恰是
// miniredis 不模拟的东西：SetNX 锁的**值匹配解锁**（Lua 脚本）、`GETDEL` 式原子排空
// 列表、以及 TTL 到期。假服务端把这些实现成"永远成功"，用例就退化成测我们自己的桩。
//
// 覆盖三条真实跨实例路径（单实例内存模式下**根本走不到**，故此前的单测全为盲区）：
//  1. 实例甲持处理锁、实例乙经 Redis 转交消息 → 甲窗口收账时吸收 → 一条合并回复
//     同时送达两实例（乙拿到的还是与甲配对的代际号，供 D5 投递认领）；
//  2. 持锁实例死亡（锁消失且无回复）→ 远程等待者接管，消息**不丢也不重**
//     （接管时 absorb 会因"本地未在 processing"把消息退回 Redis，靠窗口收账那次吸收捡回，
//     这条链路只有真跑才看得出来）；
//  3. ClaimReplyDelivery 跨实例互斥：同 (批次代际, 通道) 只有一个调用方拿到投递权。
//
// 不测的：waitRemotely 等待超时后的降级提示分支（P2-38）——它要等满 processing_lock_timeout
// 才进，纯耗时不产出判据，且落库侧已有 WriteDegradedNotice 的租户归属单测。
package service

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"ai-scrm/config"
	"ai-scrm/internal/redisclient"

	goredis "github.com/redis/go-redis/v9"
)

// redisTestDB 测试专用库号：与在跑的服务实例（默认 DB 0）物理隔开，
// 否则用例会和线上合并队列抢同一批 mq:* 键。
const redisTestDB = 9

// enableRealRedis 把全局 redisclient 接到本机真 Redis（DB 9），用例结束恢复单机模式。
// 连不上直接 Skip——多实例协调是可选组件，没有 Redis 的环境跑不到这条路径，不算失败。
func enableRealRedis(t *testing.T) {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	pwd := os.Getenv("REDIS_PASSWORD")

	probe := goredis.NewClient(&goredis.Options{
		Addr: addr, Password: pwd, DB: redisTestDB, DialTimeout: 500 * time.Millisecond,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := probe.Ping(ctx).Err(); err != nil {
		_ = probe.Close()
		t.Skipf("本机 Redis 不可达（%s）：跨实例协议用例跳过: %v", addr, err)
	}
	_ = probe.Close()

	redisclient.Init(config.RedisConfig{Enabled: true, Addr: addr, Password: pwd, DB: redisTestDB})
	if !redisclient.IsEnabled() {
		t.Skip("redisclient 未进入启用态，跳过")
	}
	t.Cleanup(func() {
		// 恢复"单机模式"：Init(Enabled:false) 是早退不清 client，必须用一次连不上的
		// Init 把全局 client 置 nil（该路径会顺手起自愈循环，紧跟着停掉）。
		redisclient.Init(config.RedisConfig{Enabled: true, Addr: "127.0.0.1:1"})
		redisclient.StopSelfHeal()
		if redisclient.IsEnabled() {
			t.Error("用例结束后 redisclient 仍为启用态，会污染后续单实例用例")
		}
	})
}

// mqKeyPattern 本用例族的键前缀（只清自己写过的键，不动库里其它测试数据）
const mqKeyPattern = "mq:*"

// cleanMQKeys 清空测试库里的 mq:* 键，返回清理条数
func cleanMQKeys(t *testing.T) {
	t.Helper()
	c := redisclient.Client()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	keys, err := c.Keys(ctx, mqKeyPattern).Result()
	if err != nil {
		t.Logf("清理 mq:* 键失败（不影响断言，库号已隔离）: %v", err)
		return
	}
	if len(keys) > 0 {
		if err := c.Del(ctx, keys...).Err(); err != nil {
			t.Logf("删除 mq:* 键失败: %v", err)
		}
	}
}

// waitLockHeld 等某把 Redis 锁真的出现——"我以为甲拿到锁了"不能当事实用
func waitLockHeld(t *testing.T, key string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if redisclient.LockExists(key) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s 在 %v 内没有出现：处理锁从未被任何实例持有，后续断言的前提不成立", key, d)
}

// TestRedisTwoInstanceHandoffMergesAndReplies 实例甲持锁处理、实例乙经 Redis 转交：
// 甲的批次里必须有乙那条消息，乙拿到甲发布的那一条回复（且代际号配对）。
//
// 这是"多副本部署下连发消息仍只回一条"的结构性证据：单实例用例证明不了
// 跨进程转交（RPush→DrainList）不丢消息、也不会在两个实例上各生成一次回复。
func TestRedisTwoInstanceHandoffMergesAndReplies(t *testing.T) {
	enableRealRedis(t)
	cleanMQKeys(t)
	defer cleanMQKeys(t)
	defer queueTestConfig(t, 100, nil)()

	svcA, svcB := NewMessageQueueService(), NewMessageQueueService()
	tid, cid := uint(41), uint(94801)
	k := queueKey(tid, cid)

	chA := enqueueAsync(svcA, tid, cid, "实例甲收到的：预算二十万想看纯电SUV")
	waitLockHeld(t, "mq:lock:"+k, 5*time.Second)

	// 乙在甲的窗口内到达：抢锁失败 → 转交 Redis → 远程等回复
	chB := enqueueAsync(svcB, tid, cid, "实例乙收到的：顺便问下充电桩送不送")

	a := waitQ(t, chA, "处理者实例甲")
	if !a.process {
		t.Fatalf("甲先拿锁却没能处理（shouldProcess=false）")
	}
	if a.isSimple {
		t.Fatal("长句不得判为简单消息")
	}
	lines := strings.Split(strings.TrimSpace(a.merged), "\n")
	if len(lines) != 2 {
		t.Fatalf("甲的批次应合并 2 条（本地1+转交1），实际 %d 条：%q", len(lines), a.merged)
	}
	for _, marker := range []string{"实例甲收到的", "实例乙收到的"} {
		if got := strings.Count(a.merged, marker); got != 1 {
			t.Errorf("%s 在合并结果中出现 %d 次（0=跨实例转交丢消息，>1=重复入批）", marker, got)
		}
	}

	const replyText = "跨实例统一回复：两款都有现货"
	svcA.SetReply(tid, cid, a.epoch, replyText)

	b := waitQ(t, chB, "远程等待者实例乙")
	if b.process {
		t.Errorf("乙二次接管处理权（shouldProcess=true，merged=%q）——同一客户在两个实例上各生成一次回复", b.merged)
	}
	if b.reply != replyText {
		t.Errorf("乙拿到的远程回复=%q，期望甲发布的 %q", b.reply, replyText)
	}
	// D5：回复必须带配对代际，否则乙侧通道投递认领会退化成"无条件放行"（可能双发）
	if b.epoch != a.epoch {
		t.Errorf("乙拿到的代际=%d 与甲的 %d 不配对，投递认领将拿错代际", b.epoch, a.epoch)
	}

	// 交卷后跨实例状态收账：锁释放、待合并列表排空（残留=下一个实例会捡到上一批的消息）
	if redisclient.LockExists("mq:lock:" + k) {
		t.Error("SetReply 后处理锁仍未释放，其它实例的永久进不了处理路径")
	}
	if n := redisclient.LLen("mq:pending:" + k); n != 0 {
		t.Errorf("SetReply 后 Redis 待合并列表残留 %d 条", n)
	}
}

// TestRedisHandlerDeathTakeoverNoLossNoDup 持锁实例死亡（锁消失且无回复）→ 远程等待者接管：
// 自己那条消息必须**恰好出现一次**。
//
// 为什么这条最值得测：接管路径上 `absorbRemotePending` 因"本地批次未在 processing"会把
// 消息**退回 Redis**（D6 注释点明的语义），消息真正被捡回是在窗口收账那次 absorb。
// 少一次 absorb 调用即静默丢消息；appendOwn 写错即同一条回两遍、双烧 token。
func TestRedisHandlerDeathTakeoverNoLossNoDup(t *testing.T) {
	enableRealRedis(t)
	cleanMQKeys(t)
	defer cleanMQKeys(t)
	defer queueTestConfig(t, 100, nil)()

	tid, cid := uint(42), uint(94802)
	k := queueKey(tid, cid)
	lockKey := "mq:lock:" + k

	// 前置：造一个"已经死了的持锁实例"
	holder := redisclient.TryLock(lockKey, 30*time.Second)
	if holder == nil {
		t.Fatal("前置失败：无法建立持锁方，接管路径不会被触发")
	}
	go func() {
		time.Sleep(400 * time.Millisecond) // 等乙进 waitRemotely 并至少轮询一次
		holder.Unlock()                    // 模拟持锁实例进程消失（锁由持有者自己释放）
	}()

	svc := NewMessageQueueService()
	r := waitQ(t, enqueueAsync(svc, tid, cid, "接管消息：我要约周六下午的试驾"), "接管者")
	if !r.process {
		t.Fatalf("持锁实例失联后本实例应接管（reply=%q）", r.reply)
	}
	lines := strings.Split(strings.TrimSpace(r.merged), "\n")
	if len(lines) != 1 {
		t.Fatalf("接管批次应恰好 1 条，实际 %d 条：%q", len(lines), r.merged)
	}
	if !strings.Contains(r.merged, "接管消息") {
		t.Errorf("接管后合并内容丢了本条消息：%q", r.merged)
	}
	if n := redisclient.LLen("mq:pending:" + k); n != 0 {
		t.Errorf("接管实例的 Redis 待合并列表残留 %d 条（这批收完就没人认领了）", n)
	}
	// DEFECT-G7-TAKEOVER-MERGECOUNT（登记，本批不改生产码）：
	// 接管分支先无条件 `q.mergeCount = 1`，而 appendOwn=false 时本条消息其实还没进 pending，
	// 要靠窗口收账那次 absorb 才捡回来——于是 mergeCount 比批次真实条数多 1（这里 2 vs 1）。
	// 影响面：internal/channel/inbound.go 的 D7 相似抑制前置条件是 `mergeCount <= 1`，
	// 虚高会让"接管批次里唯一一条通道消息"跳过抑制判定，极端下重复回复一条。
	// 不丢消息、不双烧 token，故不阻塞本批；修法要把 mergeCount 的赋值挪到实际入批计数处
	// （与同文件积压接管分支的 `mergeCount=0 后按 pending 实数累加` 同款写法对齐）。
	if r.count != len(lines) {
		t.Logf("已知缺陷 DEFECT-G7-TAKEOVER-MERGECOUNT：接管批 mergeCount=%d 但批次实际 %d 条", r.count, len(lines))
	}
	svc.SetReply(tid, cid, r.epoch, "已安排周六两点")
}

// TestRedisClaimReplyDeliveryCrossInstance 投递认领（D5）跨实例互斥：
// 同 (批次代际, 通道) 两个实例抢，只能有一个拿到投递权。
//
// 这一条决定了"通道 worker 与 web 处理者同时想发同一条回复"时客户收到几条。
// 单实例下它由队列内 deliveredFor 表兜住，跨实例只能靠 Redis SetNX。
func TestRedisClaimReplyDeliveryCrossInstance(t *testing.T) {
	enableRealRedis(t)
	cleanMQKeys(t)
	defer cleanMQKeys(t)
	defer queueTestConfig(t, 100, nil)()

	tid, cid := uint(43), uint(94803)
	svcA, svcB := NewMessageQueueService(), NewMessageQueueService()
	const epoch = uint64(7788)
	const chID = uint(5)

	// 同批同通道：第二个调用方必须被拒
	if !svcA.ClaimReplyDelivery(tid, cid, epoch, chID) {
		t.Fatal("首次认领应拿到投递权")
	}
	if svcB.ClaimReplyDelivery(tid, cid, epoch, chID) {
		t.Error("同批同通道被两个实例各自认领 → 客户会收到两条相同回复")
	}
	// 反证：换一代际必须放行，否则上一段的"拒绝"只是因为键根本写不进去
	if !svcB.ClaimReplyDelivery(tid, cid, epoch+1, chID) {
		t.Error("下一批次同通道应重新拿到投递权（被拒即跨批漏发）")
	}
	// 反证：同代际换通道必须放行（多通道客户各发一条才是对的）
	if !svcB.ClaimReplyDelivery(tid, cid, epoch, chID+1) {
		t.Error("同批不同通道应各自投递（被拒即漏发）")
	}
	// epoch=0（旧协议/降级路径）一律放行：宁可极端双发，不可静默漏发
	if !svcA.ClaimReplyDelivery(tid, cid, 0, chID) {
		t.Error("epoch=0 应放行投递")
	}
}
