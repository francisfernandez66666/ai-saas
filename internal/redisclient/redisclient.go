// Package redisclient Redis 封装：分布式锁(看门狗续期)、KV/列表/版本戳、未启用时内存降级
package redisclient

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
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
// ============================================================

// rdb 全局 Redis 客户端（nil 表示未启用，所有方法自动降级为单实例内存路径）
var rdb *redis.Client

// instanceID 本实例标识（锁值，安全解锁用：只删自己的锁）
var instanceID string

// init 进程启动时生成实例 ID（作为分布式锁值），保证仅能解锁自己持有的锁
func init() {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	instanceID = hex.EncodeToString(b)
}

// Init 初始化 Redis 连接
// cfg.Enabled=false 时不开连接；连接失败仅告警并保持禁用（不阻断启动）
func Init(cfg config.RedisConfig) {
	if !cfg.Enabled {
		log.Println("[Redis] 未启用（REDIS_ENABLED=false），使用单实例内存模式")
		return
	}
	rdb = redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Printf("[Redis] 连接失败（降级为内存模式）: %v", err)
		rdb = nil
		return
	}
	log.Printf("[Redis] 连接成功: %s（多实例模式）", cfg.Addr)
}

// IsEnabled Redis 是否可用
func IsEnabled() bool {
	return rdb != nil
}

// Client 返回原生客户端（谨慎使用；nil 表示未启用）
func Client() *redis.Client {
	return rdb
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
	ok, err := rdb.SetNX(ctx, key, value, ttl).Result()
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
				res, rerr := luaRenew.Run(rctx, rdb, []string{key}, value, ttl.Milliseconds()).Result()
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
	_, _ = luaUnlock.Run(ctx, rdb, []string{h.key}, h.value).Result()
}

// LockExists 查询锁是否被持有（任意实例）
func LockExists(key string) bool {
	if !IsEnabled() {
		return false
	}
	ctx, cancel := ctxDefault()
	defer cancel()
	n, err := rdb.Exists(ctx, key).Result()
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
	v, err := rdb.Get(ctx, key).Result()
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
	_ = rdb.SetEx(ctx, key, value, ttl).Err()
}

// Del 删除键
func Del(key string) {
	if !IsEnabled() {
		return
	}
	ctx, cancel := ctxDefault()
	defer cancel()
	_ = rdb.Del(ctx, key).Err()
}

// Incr 自增并返回新值（缓存版本戳用）
func Incr(key string) int64 {
	if !IsEnabled() {
		return 0
	}
	ctx, cancel := ctxDefault()
	defer cancel()
	n, err := rdb.Incr(ctx, key).Result()
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
	n, err := rdb.Get(ctx, key).Int64()
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
	n, err := rdb.Incr(ctx, key).Result()
	if err != nil {
		return 0
	}
	if n <= 1 {
		_ = rdb.Expire(ctx, key, ttl).Err()
	}
	return n
}

// LPushLeftPush 左侧推入列表（跨实例消息转交）
func LPush(key, value string) {
	if !IsEnabled() {
		return
	}
	ctx, cancel := ctxDefault()
	defer cancel()
	_ = rdb.LPush(ctx, key, value).Err()
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
	_ = rdb.RPush(ctx, key, value).Err()
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
	res, err := luaDrainList.Run(ctx, rdb, []string{key}).Result()
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

// randomHex 用 crypto/rand 生成 n 字节随机数的十六进制串（分布式锁值/唯一后缀）
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
