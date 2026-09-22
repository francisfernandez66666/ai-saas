// §八-7 零测试包最小单测（2026-09-18）：redisclient 的"未启用即静默安全"契约——
//  1. Init(Enabled:false) 与 Init(Enabled:true 但连不上) 之后 IsEnabled() 必须为 false，
//     且不得 panic（多实例协调层是可选组件，降级为单机是设计意图）；
//  2. 全套 API 在未启用时一律短路返回零值——调用方据此走内存路径；
//     若哪天有人把某处短路删了，未开 Redis 的单机部署会在运行时炸（历史上 TryLock
//     对 nil rdb 的 nil 接收者保护就是为此存在）；
//  3. (*LockHandle)(nil) 的 Unlock/RenewFailures 必须 nil-safe（没抢到锁时返回 nil 句柄，
//     调用方 defer h.Unlock() 是常见写法）。
//
// 不测真实 Redis 交互（那是 capacity_matrix/smoke 层），本包只钉"禁用态不伤人"。
package redisclient

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-scrm/config"
	"ai-scrm/internal/testutil"

	"github.com/redis/go-redis/v9"
)

// TestMain 统一出口：本包若有 DB 用例被 testutil 跳过，收尾把跳过条数打到 stderr（防静默绿）
func TestMain(m *testing.M) {
	os.Exit(testutil.RunMain(m))
}

// forceDisabled 把全局客户端置回"未启用"并在用例结束后恢复（测试不得互相依赖）。
// 同时停掉后台自愈循环：O1-a 起 Init(连不上) 会常驻重探，不停会让后续用例看到半路恢复。
func forceDisabled(t *testing.T) {
	t.Helper()
	old := Client()
	setClient(nil)
	StopSelfHeal()
	t.Cleanup(func() {
		StopSelfHeal()
		setClient(old)
	})
}

// TestInitDisabledKeepsSingleInstanceMode Enabled=false 不开连接、不 panic
func TestInitDisabledKeepsSingleInstanceMode(t *testing.T) {
	forceDisabled(t)
	Init(config.RedisConfig{Enabled: false, Addr: "127.0.0.1:6379"})
	if IsEnabled() {
		t.Errorf("Enabled=false 时 IsEnabled 应为 false")
	}
	if Client() != nil {
		t.Errorf("Enabled=false 时 Client() 应为 nil，实际 %v", Client())
	}
}

// TestInitUnreachableDegradesToMemoryMode 配了 Redis 但连不上：告警 + 保持禁用，不阻断启动
func TestInitUnreachableDegradesToMemoryMode(t *testing.T) {
	forceDisabled(t)
	// 127.0.0.1:1 必然拒绝连接（不依赖外部网络，离线可测）
	Init(config.RedisConfig{Enabled: true, Addr: "127.0.0.1:1", DB: 0})
	if IsEnabled() {
		t.Errorf("连不上时必须降级为内存模式（IsEnabled=false）")
	}
	if Client() != nil {
		t.Errorf("降级后全局客户端应置 nil，否则后续调用会走半死连接")
	}
	// O1-a：连不上≠没配——声明位必须为 true，readiness 靠它区分"未配"与"配了但连不上"
	if !DeclaredEnabled() {
		t.Errorf("Enabled=true 时 DeclaredEnabled 应为 true（供 /status 暴露 redis_declared_but_down）")
	}
	// 降级后 API 仍安全
	if _, err := TryLockE("any:key", time.Second); err != nil {
		t.Errorf("降级后 TryLockE 不应报错，实际 %v", err)
	}
}

// render 把多返回值归一成可比对字符串（用 | 分隔，避免 "" + "false" 与 "false" + "" 混淆）
func render(vals ...any) string {
	parts := make([]string, 0, len(vals))
	for _, v := range vals {
		parts = append(parts, fmt.Sprintf("%v", v))
	}
	return strings.Join(parts, "|")
}

// TestDisabledAPIShortCircuit 未启用时全套 API 短路返回零值（表驱动 + 归一比对）
func TestDisabledAPIShortCircuit(t *testing.T) {
	forceDisabled(t)
	if IsEnabled() {
		t.Fatalf("前置条件不成立：应为未启用态")
	}
	cases := []struct {
		name string
		call func() string
		want string
	}{
		{"TryLock", func() string { return render(TryLock("k", time.Second)) }, "<nil>"},
		{"TryLockE", func() string {
			h, err := TryLockE("k", time.Second)
			return render(h, err)
		}, "<nil>|<nil>"},
		{"LockExists", func() string { return render(LockExists("k")) }, "false"},
		{"Get", func() string {
			v, ok := Get("k")
			return render(v, ok)
		}, "|false"},
		{"SetEx", func() string { SetEx("k", "v", time.Second); return render() }, ""},
		{"Del", func() string { Del("k"); return render() }, ""},
		{"Incr", func() string { return render(Incr("k")) }, "0"},
		{"GetInt", func() string { return render(GetInt("k")) }, "0"},
		{"IncrWithTTL", func() string { return render(IncrWithTTL("k", time.Second)) }, "0"},
		{"LPush", func() string { LPush("k", "v"); return render() }, ""},
		{"RPush", func() string { RPush("k", "v"); return render() }, ""},
		{"DrainList", func() string { return render(DrainList("k")) }, "[]"},
		{"Publish", func() string { Publish("ch", "payload"); return render() }, ""},
	}
	for _, tc := range cases {
		if got := tc.call(); got != tc.want {
			t.Errorf("未启用时 %s 短路值异常: got=%q want=%q", tc.name, got, tc.want)
		}
	}
	// Subscribe 未启用返回 nil 通道（调用方按"无广播=维持旧行为"降级）
	if ch := Subscribe("any-channel"); ch != nil {
		t.Errorf("未启用时 Subscribe 应返回 nil 通道，实际 %v", ch)
	}
	// 未启用时锁句柄为 nil：Unlock/RenewFailures 走 nil 接收者分支
	h := TryLock("k", time.Second)
	if h != nil {
		t.Fatalf("未启用时不应拿到锁句柄")
	}
	if n := h.RenewFailures(); n != 0 {
		t.Errorf("nil 句柄 RenewFailures 应为 0，实际 %d", n)
	}
	h.Unlock() // 不得 panic
}

// TestNilLockHandleReceiverSafety 显式钉 nil 接收者安全（含 nil 句柄调 LockHandle 全套方法）
func TestNilLockHandleReceiverSafety(t *testing.T) {
	var h *LockHandle
	if h.RenewFailures() != 0 {
		t.Errorf("nil 句柄 RenewFailures 应为 0")
	}
	h.Unlock() // 不得 panic（调用方普遍 defer h.Unlock()）
}

// TestRandomHex 锁值/后缀随机段：长度与字符集（长度错会削弱锁唯一性）
func TestRandomHex(t *testing.T) {
	hexRE := regexp.MustCompile(`^[0-9a-f]+$`)
	for _, n := range []int{1, 4, 8, 16} {
		got := randomHex(n)
		if len(got) != n*2 {
			t.Errorf("randomHex(%d) 长度=%d want %d", n, len(got), n*2)
		}
		if !hexRE.MatchString(got) {
			t.Errorf("randomHex(%d) 含非 hex 字符: %q", n, got)
		}
	}
	if randomHex(8) == randomHex(8) {
		t.Errorf("randomHex(8) 两次结果相同，随机性存疑")
	}
	if randomHex(0) != "" {
		t.Errorf("randomHex(0) 应为空串")
	}
}

// TestClientAccessorConsistency Client() 与 IsEnabled() 口径一致（外部包用原生客户端的前提）
func TestClientAccessorConsistency(t *testing.T) {
	forceDisabled(t)
	if Client() != nil || IsEnabled() {
		t.Errorf("未启用时两者应一致为 nil/false")
	}
	// 手工注入客户端只验口径，不发命令（不打真实 Redis）
	old := Client()
	setClient(redis.NewClient(&redis.Options{Addr: "127.0.0.1:6399"}))
	defer setClient(old)
	if !IsEnabled() || Client() == nil {
		t.Errorf("有客户端时两者应一致为 true/非 nil")
	}
}

// TestSelfHealRecoversAfterDeclaredFailure O1-a(2026-09-23 批六)：
// 声明启用但首探失败 → 后台自愈必须能在 Redis 起来后恢复多实例语义，并触发一次恢复回调。
// 这是编排竞态的唯一兜底（compose 里 app 先于 redis ready 时，旧实现永久降级成单机）。
func TestSelfHealRecoversAfterDeclaredFailure(t *testing.T) {
	forceDisabled(t)
	oldFn, oldInterval := newClientFn, selfHealInterval
	t.Cleanup(func() { newClientFn, selfHealInterval = oldFn, oldInterval })

	// 探测次数用原子计数——勿用带缓冲 channel 当信号：生产者写满即阻塞，
	// 且消费方"取到信号但未及看到状态"会永久等不到下一次推送（首版即踩此坑致用例挂死）。
	var probes, recovered int32
	newClientFn = func(config.RedisConfig) (*redis.Client, error) {
		if atomic.AddInt32(&probes, 1) < 2 {
			return nil, fmt.Errorf("模拟：Redis 尚未就绪")
		}
		// 只验指针发布语义，不发命令（addr 指向空闲端口，永不实际连通）
		return redis.NewClient(&redis.Options{Addr: "127.0.0.1:6399"}), nil
	}
	selfHealInterval = 5 * time.Millisecond
	OnRecover(func() { atomic.AddInt32(&recovered, 1) })

	Init(config.RedisConfig{Enabled: true, Addr: "127.0.0.1:6399"})
	if IsEnabled() {
		t.Fatalf("首探失败后应立即降级（IsEnabled=false）")
	}
	if got := atomic.LoadInt32(&probes); got != 1 {
		t.Fatalf("Init 应恰好探测一次，实际 %d 次", got)
	}

	// 自愈必须在限期内把连接恢复回来（否则就是被修掉的"探测一次定终身"缺陷）
	waitFor(t, 3*time.Second, func() bool { return IsEnabled() },
		"自愈循环未在限期内恢复连接（声明启用却永久降级）")
	waitFor(t, 2*time.Second, func() bool { return atomic.LoadInt32(&recovered) == 1 },
		"恢复回调未触发（装配层收不到多实例语义恢复提示）")

	// 连上即退出，不常驻重探：再等 20 个周期也不应新增探测
	before := atomic.LoadInt32(&probes)
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&probes); got != before {
		t.Errorf("自愈成功后应立即退出循环，不应常驻重探（新增 %d 次探测）", got-before)
	}
	if n := atomic.LoadInt32(&recovered); n != 1 {
		t.Errorf("恢复回调应恰好触发一次，实际 %d 次", n)
	}
}

// waitFor 轮询等待条件成立（超时即 Fatalf；勿写死 sleep，慢机器会假红）
func waitFor(t *testing.T, timeout time.Duration, ok func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%s（等待 %s 超时）", msg, timeout)
}

// TestInitDisabledDoesNotStartSelfHeal REDIS_ENABLED=false 时绝不重探（本地/CI 单实例零行为变化）
func TestInitDisabledDoesNotStartSelfHeal(t *testing.T) {
	forceDisabled(t)
	oldFn, oldInterval := newClientFn, selfHealInterval
	t.Cleanup(func() { newClientFn, selfHealInterval = oldFn, oldInterval })

	var dialed int32
	newClientFn = func(config.RedisConfig) (*redis.Client, error) {
		atomic.AddInt32(&dialed, 1)
		return nil, fmt.Errorf("不应被调用")
	}
	selfHealInterval = 5 * time.Millisecond

	Init(config.RedisConfig{Enabled: false, Addr: "127.0.0.1:6399"})
	if DeclaredEnabled() {
		t.Errorf("Enabled=false 时 DeclaredEnabled 应为 false")
	}
	time.Sleep(60 * time.Millisecond)
	if got := atomic.LoadInt32(&dialed); got != 0 {
		t.Errorf("未声明启用却重探了 %d 次（会把未配 Redis 的部署拖进无谓连接风暴）", got)
	}
}
