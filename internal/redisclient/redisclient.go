// Package redisclient Redis 封装：分布式锁(看门狗续期)、KV/列表/版本戳、未启用时内存降级
package redisclient

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"ai-scrm/config"

	"github.com/redis/go-redis/v9"
)

// ============================================================
// Redis 客户端封装（多实例协调层）
//
// 职责边界（Phase M）：
// 1. 消息合并队列跨实例互斥锁（TryLock/Unlock，带看门狗续期防误过期）
// 2. 跨实例消息转交（LPUSH/Lua 原子取）与回复发布（seq 协议）
// 3. 缓存版本戳（INCR/GET，跨实例失效）
// 4. 单例任务选主（TryLock 复用）
//
// 降级策略：Enabled=false 或连接失败 → IsEnabled()=false，
// 所有调用方走纯内存路径（单实例模式，行为与改造前一致）
//
// O1-a(2026-09-23 批六)：连接失败不再"一次定终身"——声明启用（REDIS_ENABLED=true）
// 却 Ping 失败时启动后台自愈重探，Redis 起来后自动恢复多实例语义（编排竞态：
// docker compose 里 app 可能先于 redis 就绪，旧行为是永久降级成单实例）。
// 未声明启用（false）绝不重探，本地/CI 单实例部署零行为变化。
// ============================================================

// rdbPtr 全局 Redis 客户端（nil 表示未启用，所有方法自动降级为单实例内存路径）。
// O1-a 引入后台自愈后，写入方不再只有启动序列（Init/自愈 goroutine 都会写），
// 故用 atomic.Pointer 承载，读侧一律经 client() 取快照，避免数据竞争。
var rdbPtr atomic.Pointer[redis.Client]

// client 取当前 Redis 客户端快照（nil=未启用/未连通，调用方已按 IsEnabled 降级）
func client() *redis.Client { return rdbPtr.Load() }

// setClient 发布/清空客户端（唯一写入口）
func setClient(c *redis.Client) { rdbPtr.Store(c) }

// declaredEnabled 记录"配置是否声明启用 Redis"（与是否已连通解耦）：
// 自愈循环只在 true 时跑，readiness 位也靠它区分"没配"与"配了但连不上"。
var declaredEnabled bool

// 建连与探测接缝（单测注入：先失败后成功，验证自愈链路真的能恢复）
var (
	newClientFn = func(cfg config.RedisConfig) (*redis.Client, error) {
		c := redis.NewClient(&redis.Options{Addr: cfg.Addr, Password: cfg.Password, DB: cfg.DB})
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := c.Ping(ctx).Err(); err != nil {
			_ = c.Close()
			return nil, err
		}
		return c, nil
	}

	// selfHealInterval 自愈重探周期（单测下调以免拖慢回归）
	selfHealInterval = 10 * time.Second
)

// 自愈循环状态：同一进程只起一个 goroutine（重复 Init 不叠加）
var (
	healRunning bool
	healStop    chan struct{}
	healMu      sync.Mutex
	// recoverHooks 恢复回调（自愈成功时按注册顺序触发，用于广播"多实例语义恢复"）
	recoverHooks []func()
)

// OnRecover 注册"Redis 由不可用恢复为可用"时的回调（幂等探测语义，回调内禁止阻塞）。
// 由装配层用于刷醒一次性降级告警；包本身不反向依赖 metrics，避免 import 环。
func OnRecover(fn func()) {
	if fn == nil {
		return
	}
	healMu.Lock()
	recoverHooks = append(recoverHooks, fn)
	healMu.Unlock()
}

// DeclaredEnabled 配置是否声明启用 Redis（不代表已连通；连不通时由自愈与 readiness 暴露）。
func DeclaredEnabled() bool { return declaredEnabled }

// instanceID 本实例标识（锁值，安全解锁用：只删自己的锁）
var instanceID string

// init 进程启动时生成实例 ID（作为分布式锁值），保证仅能解锁自己持有的锁
func init() {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	instanceID = hex.EncodeToString(b)
}

// Init 初始化 Redis 连接
// cfg.Enabled=false 时不开连接；连接失败仅告警并保持禁用（不阻断启动），
// 但会起后台自愈循环周期重探（O1-a，见上方包注释）。
func Init(cfg config.RedisConfig) {
	declaredEnabled = cfg.Enabled
	if !cfg.Enabled {
		log.Println("[Redis] 未启用（REDIS_ENABLED=false），使用单实例内存模式")
		return
	}
	setClient(nil)
	c, err := newClientFn(cfg)
	if err != nil {
		log.Printf("[Redis] 连接失败（临时降级为内存模式，后台每 %s 自愈重探）: %v", selfHealInterval, err)
		startSelfHeal(cfg)
		return
	}
	setClient(c)
	log.Printf("[Redis] 连接成功: %s（多实例模式）", cfg.Addr)
}

// startSelfHeal 启动后台重探循环（幂等：已在跑则不重复起）。
// 循环在连通后立即退出，不常驻消耗连接。
func startSelfHeal(cfg config.RedisConfig) {
	healMu.Lock()
	if healRunning {
		healMu.Unlock()
		return
	}
	healRunning = true
	healStop = make(chan struct{})
	// stop 在锁内取快照并作为参数交给循环：goroutine 不得再读全局 healStop
	// （StopSelfHeal 会置 nil，并发读写即数据竞争）
	stop := healStop
	healMu.Unlock()
	// 一次性取参数快照后交给循环：自愈 goroutine 内不再读包级测试接缝
	// （用例并发改写 newClientFn/selfHealInterval 会与后台读构成数据竞争，-race 直接报）
	go healLoop(cfg, stop, selfHealInterval, newClientFn)
}

// healLoop 自愈重探循环：连上即发布客户端、触发恢复回调并退出（一次性，非常驻健康检查）。
func healLoop(cfg config.RedisConfig, stop <-chan struct{}, interval time.Duration, dial func(config.RedisConfig) (*redis.Client, error)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			// 已连通说明别处已完成装配（如运维手工重跑初始化），只收尾退出。
			healMu.Lock()
			if IsEnabled() {
				hooks := append([]func(){}, recoverHooks...)
				healRunning = false
				healMu.Unlock()
				notifyRecovered(hooks)
				return
			}
			healMu.Unlock()
			c, err := dial(cfg)
			if err != nil {
				log.Printf("[Redis] 自愈重探仍失败（下一轮 %s 后再试）: %v", interval, err)
				continue
			}
			setClient(c)
			log.Printf("[Redis] ✅ 自愈成功：已恢复多实例协调语义（锁/消息转交/缓存失效跨实例生效）")
			healMu.Lock()
			hooks := append([]func(){}, recoverHooks...)
			healRunning = false
			healMu.Unlock()
			notifyRecovered(hooks)
			return
		}
	}
}

// notifyRecovered 依次触发恢复回调（panic 吞掉，回调失败不得影响协调层本身）
func notifyRecovered(hooks []func()) {
	for _, fn := range hooks {
		func() {
			defer func() { _ = recover() }()
			fn()
		}()
	}
}

// StopSelfHeal 停止后台自愈循环（测试与优雅停机用；未运行时 no-op）。
func StopSelfHeal() {
	healMu.Lock()
	defer healMu.Unlock()
	if healRunning && healStop != nil {
		close(healStop)
	}
	healRunning = false
	healStop = nil
}

// IsEnabled Redis 是否可用
func IsEnabled() bool {
	return rdbPtr.Load() != nil
}

// Client 返回原生客户端（谨慎使用；nil 表示未启用）
func Client() *redis.Client {
	return client()
}

// ctxDefault 统一超时上下文（Redis 操作不允许阻塞业务请求过久）
func ctxDefault() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 2*time.Second)
}

// ============================================================
// 分布式锁（带看门狗续期）
// ============================================================

// LockHandle 锁句柄：持有者用于续期与释放
type LockHandle struct {
	key    string
	value  string // 锁值=instanceID+随机数，防止误删他人锁
	stop   chan struct{}
	closed chan struct{}
	// P1-41 缺口①：renewFailures 看门狗连续续期失败计数（锁可能过期易主）
	// 非并发安全，仅供持有者承办完后自查（看门狗 goroutine 独占写）
	renewFailures int
}

// luaUnlock 安全解锁 Lua：值匹配才删除（防删掉已易主的锁）
var luaUnlock = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0`)

// luaRenew 续期 Lua：值匹配才续期
var luaRenew = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0`)

// TryLock 尝试获取分布式锁（兼容旧签名：Redis 连接错误时返回 nil 锁）
// P1-41 缺口②(2026-09-09)：建议调用方改用 TryLockE 区分"没抢到"与"Redis 坏"；
// 仅当调用方确认 fail-open 可接受时才继续用简化版。
func TryLock(key string, ttl time.Duration) *LockHandle {
	h, _ := TryLockE(key, ttl)
	return h
}

// TryLockE 尝试获取分布式锁，区分失败原因：
//   - 返回 (lock, nil)：本实例拿到锁
//   - 返回 (nil, nil)：Redis 正常但锁被其他实例持有（没抢到）
//   - 返回 (nil, err)：Redis 故障（SetNX 出错）——调用方应 fail-closed 拒绝处理，
//     而不是静默走单机兜底（P1-41 缺口②：故障期 fail-open 会双实例并发处理同一客户）
//
// ttl 为锁的初始有效期；获取成功后启动看门狗，每 ttl/3 自动续期，
// 直到 Unlock 或持有方进程死亡（锁自然过期，实现跨实例故障自愈）
func TryLockE(key string, ttl time.Duration) (*LockHandle, error) {
	if !IsEnabled() {
		return nil, nil // 未启用=单实例，无锁语义；调用方自行决定
	}
	value := instanceID + ":" + randomHex(4)
	ctx, cancel := ctxDefault()
	defer cancel()
	ok, err := client().SetNX(ctx, key, value, ttl).Result()
	if err != nil {
		// Redis 故障：显式返回错误，调用方禁止按"没抢到"处理
		return nil, fmt.Errorf("Redis SetNX 失败 key=%s: %w", key, err)
	}
	if !ok {
		return nil, nil // 锁被他人持有
	}
	h := &LockHandle{
		key:    key,
		value:  value,
		stop:   make(chan struct{}),
		closed: make(chan struct{}),
	}
	// 看门狗：每 ttl/3 续期一次（处理耗时 >90s 的 AI 延迟场景）
	go func() {
		defer close(h.closed)
		ticker := time.NewTicker(ttl / 3)
		defer ticker.Stop()
		for {
			select {
			case <-h.stop:
				return
			case <-ticker.C:
				rctx, rcancel := ctxDefault()
				res, rerr := luaRenew.Run(rctx, client(), []string{key}, value, ttl.Milliseconds()).Result()
				rcancel()
				// P1-41 缺口①(2026-09-09)：续期失败不再被吞——连续失败说明锁可能已过期易主，
				// 原持有者继续执行有并发写风险。计数暴露 + 告警日志（fencing token 需任务侧
				// 支持取消检查点，本次先做"日志+计数"层面）。
				if rerr != nil || res == int64(0) {
					h.renewFailures++
					log.Printf("[Redis锁] 看门狗续期失败 key=%s count=%d err=%v——锁可能已过期易主，持有者应尽快收敛执行", key, h.renewFailures, rerr)
				} else {
					h.renewFailures = 0
				}
			}
		}
	}()
	return h, nil
}

// RenewFailures 返回本锁续期失败次数（暴露给持有者自查锁健康，P1-41 缺口①）
func (h *LockHandle) RenewFailures() int {
	if h == nil {
		return 0
	}
	return h.renewFailures
}

// Unlock 释放锁（停止看门狗 + 安全删除）
func (h *LockHandle) Unlock() {
	if h == nil {
		return
	}
	close(h.stop)
	<-h.closed // 等看门狗退出，避免释放后续期复活
	ctx, cancel := ctxDefault()
	defer cancel()
	_, _ = luaUnlock.Run(ctx, client(), []string{h.key}, h.value).Result()
}

// LockExists 查询锁是否被持有（任意实例）
func LockExists(key string) bool {
	if !IsEnabled() {
		return false
	}
	ctx, cancel := ctxDefault()
	defer cancel()
	n, err := client().Exists(ctx, key).Result()
	return err == nil && n > 0
}

// ============================================================
// KV / 列表 / 版本戳
// ============================================================

// Get 读取字符串值
func Get(key string) (string, bool) {
	if !IsEnabled() {
		return "", false
	}
	ctx, cancel := ctxDefault()
	defer cancel()
	v, err := client().Get(ctx, key).Result()
	if err != nil {
		return "", false
	}
	return v, true
}

// SetEx 写入带 TTL 的字符串值
func SetEx(key, value string, ttl time.Duration) {
	if !IsEnabled() {
		return
	}
	ctx, cancel := ctxDefault()
	defer cancel()
	_ = client().SetEx(ctx, key, value, ttl).Err()
}

// Del 删除键
func Del(key string) {
	if !IsEnabled() {
		return
	}
	ctx, cancel := ctxDefault()
	defer cancel()
	_ = client().Del(ctx, key).Err()
}

// Incr 自增并返回新值（缓存版本戳用）
func Incr(key string) int64 {
	if !IsEnabled() {
		return 0
	}
	ctx, cancel := ctxDefault()
	defer cancel()
	n, err := client().Incr(ctx, key).Result()
	if err != nil {
		return 0
	}
	return n
}

// GetInt 读取整型值，不存在或异常返回 0
func GetInt(key string) int64 {
	if !IsEnabled() {
		return 0
	}
	ctx, cancel := ctxDefault()
	defer cancel()
	n, err := client().Get(ctx, key).Int64()
	if err != nil {
		return 0
	}
	return n
}

// IncrWithTTL 自增并保证键带 TTL：第一次自增（值为1）时补设过期，
// 防止键永久残留（注册防薅每日键等场景）。
func IncrWithTTL(key string, ttl time.Duration) int64 {
	if !IsEnabled() {
		return 0
	}
	ctx, cancel := ctxDefault()
	defer cancel()
	n, err := client().Incr(ctx, key).Result()
	if err != nil {
		return 0
	}
	if n <= 1 {
		_ = client().Expire(ctx, key, ttl).Err()
	}
	return n
}

// SetNXEx 仅当键不存在时写入（带 TTL），返回是否写入成功。
// P0-1 配套(2026-09-20)：UsageSink 跨实例共享影子余额的 seed 竞争用。
func SetNXEx(key, value string, ttl time.Duration) bool {
	if !IsEnabled() {
		return false
	}
	ctx, cancel := ctxDefault()
	defer cancel()
	ok, err := client().SetNX(ctx, key, value, ttl).Result()
	if err != nil {
		return false
	}
	return ok
}

// DecrByWithTTL 原子扣减并保证键带 TTL（键缺失时以 0 起算后补 TTL 语义不可靠，
// 调用方应先 seed）。返回值 + 是否成功；Redis 不可用/异常返回 (0,false)。
// P0-1 配套(2026-09-20)：UsageSink 影子余额多实例共享扣减（DECRBY）。
func DecrByWithTTL(key string, delta int64, ttl time.Duration) (int64, bool) {
	if !IsEnabled() {
		return 0, false
	}
	ctx, cancel := ctxDefault()
	defer cancel()
	n, err := client().DecrBy(ctx, key, delta).Result()
	if err != nil {
		return 0, false
	}
	// 每次扣减都续 TTL：影子是缓存不是账本，活跃租户不应因 TTL 掉 seed 竞争
	_ = client().Expire(ctx, key, ttl).Err()
	return n, true
}

// SetNXExE 与 SetNXEx 同语义但把 Redis 错误显式带回（acquired, err 互斥：出错判未获取）。
// P2-4(2026-09-20 批三)：健康告警冷却表用——故障期必须与"键已存在（冷却中）"区分，
// 前者退回调用方本机内存冷却兜底（宁可多报不漏报），后者才允许静默跳过。
func SetNXExE(key, value string, ttl time.Duration) (bool, error) {
	if !IsEnabled() {
		return false, errors.New("redis disabled")
	}
	ctx, cancel := ctxDefault()
	defer cancel()
	ok, err := client().SetNX(ctx, key, value, ttl).Result()
	if err != nil {
		return false, err
	}
	return ok, nil
}

// LPushLeftPush 左侧推入列表（跨实例消息转交）
func LPush(key, value string) {
	if !IsEnabled() {
		return
	}
	ctx, cancel := ctxDefault()
	defer cancel()
	_ = client().LPush(ctx, key, value).Err()
}

// RPushRightPush 右侧推入列表
// P1-41 缺口③(2026-09-09)：跨实例消息转交必须用 RPush（FIFO）——原 LPush+LRANGE 取出为 LIFO，
// 最新一条排最前，合并后的消息顺序颠倒。生产端 RPush、消费端 DrainList 仍 LRANGE(0,-1) 头→尾=先进先出。
func RPush(key, value string) {
	if !IsEnabled() {
		return
	}
	ctx, cancel := ctxDefault()
	defer cancel()
	_ = client().RPush(ctx, key, value).Err()
}

// LLen 返回列表当前长度（Redis 未启用或出错一律 0，只读探测语义 fail-open）。
// P2-1(2026-09-20 批三)：合并队列用它探测跨实例 pending 积压是否有在途批次，探测失败按"无积压"处理不阻断主流程。
func LLen(key string) int64 {
	if !IsEnabled() {
		return 0
	}
	ctx, cancel := ctxDefault()
	defer cancel()
	n, err := client().LLen(ctx, key).Result()
	if err != nil {
		return 0
	}
	return n
}

// luaDrainList 原子取出整个列表（LRANGE+DEL 非原子会丢消息，必须 Lua）
var luaDrainList = redis.NewScript(`
local v = redis.call("LRANGE", KEYS[1], 0, -1)
redis.call("DEL", KEYS[1])
return v`)

// DrainList 原子清空并返回整个列表
func DrainList(key string) []string {
	if !IsEnabled() {
		return nil
	}
	ctx, cancel := ctxDefault()
	defer cancel()
	res, err := luaDrainList.Run(ctx, client(), []string{key}).Result()
	if err != nil {
		return nil
	}
	if arr, ok := res.([]interface{}); ok {
		out := make([]string, 0, len(arr))
		for _, item := range arr {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// Publish G7(2026-09-16C)：向频道发布一条广播（未启用 Redis 时静默 no-op，单实例无需广播）。
func Publish(channel, payload string) {
	if !IsEnabled() {
		return
	}
	ctx, cancel := ctxDefault()
	defer cancel()
	if err := client().Publish(ctx, channel, payload).Err(); err != nil {
		log.Printf("[redisclient] Publish %s 失败: %v", channel, err)
	}
}

// Subscribe G7(2026-09-16C)：订阅频道，返回消息文本通道（连接失败/未启用返回 nil，
// 调用方按"无广播=维持旧行为"降级）。channel 关闭即停止。
func Subscribe(channel string) <-chan string {
	if !IsEnabled() {
		return nil
	}
	ctx, cancel := ctxDefault()
	pubsub := client().Subscribe(ctx, channel)
	if _, err := pubsub.Receive(ctx); err != nil {
		log.Printf("[redisclient] Subscribe %s 失败(降级无广播): %v", channel, err)
		_ = pubsub.Close()
		cancel()
		return nil
	}
	out := make(chan string, 16)
	go func() {
		defer close(out)
		defer cancel()
		defer pubsub.Close()
		ch := pubsub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				select {
				case out <- msg.Payload:
				default: // 消费慢：丢弃积压广播（缓存失效幂等，丢一条下次再触发）
				}
			}
		}
	}()
	return out
}

// randomHex 用 crypto/rand 生成 n 字节随机数的十六进制串（分布式锁值/唯一后缀）
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
