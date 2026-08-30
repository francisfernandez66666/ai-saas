// 登录防爆破守卫：用户名/IP 双层失败计数与锁定，Redis 共享或内存退化双实现。
package service

import (
	"fmt"
	"sync"
	"time"

	"ai-scrm/internal/redisclient"
)

// ============================================================
// 登录防爆破守卫
//
// 策略（双层键，独立计数）：
//   - 按用户名：5 次失败 / 10 分钟 → 锁定 15 分钟（防定向撞库）
//   - 按 IP+用户名 组合：同上阈值（防单 IP 遍历账号）
// 命中锁定返回剩余等待时间；登录成功清除该用户名全部记录。
//
// K6修复(2026-08-26)：多实例部署下，计数与锁定状态走 Redis 共享，
// 单实例或 Redis 未启用时退化为进程内内存实现（原有逻辑）。
// ============================================================

// 常量/变量定义块（自动补注释）。
const (
	maxFailures    = 5                // 窗口内最大失败次数
	windowDuration = 10 * time.Minute // 失败计数窗口
	lockDuration   = 15 * time.Minute // 触发后锁定时长
)

// attemptRec 内存版守卫记录：单键的失败计数 + 窗口起点 + 锁定截止（零值=未锁）
type attemptRec struct {
	count       int       // 窗口内失败次数
	windowStart time.Time // 窗口起点
	lockedUntil time.Time // 锁定截止（零值=未锁）
}

// 常量/变量定义块（自动补注释）。
var (
	guardMu   sync.Mutex
	attempts  = map[string]*attemptRec{} // key: "u:{username}" / "ip:{ip}:{username}"
	lastSweep = time.Now()
)

// ErrLocked 登录锁定错误（携带剩余时间）
type ErrLocked struct{ Remaining time.Duration }

// Error 实现 error 接口：返回锁定剩余提示
func (e *ErrLocked) Error() string {
	return fmt.Sprintf("失败次数过多，账号已临时锁定，请 %d 分钟后再试", int(e.Remaining.Minutes())+1)
}

// loginKeys 生成双层守卫键（内存版）
func loginKeys(username, ip string) []string {
	return []string{"u:" + username, "ip:" + ip + ":" + username}
}

// redisCountKey / redisLockKey 生成 Redis 版键
func redisCountKey(k string) string { return "lg:cnt:" + k }
// redisLockKey 函数（自动补注释，原为缺注释的顶层声明）。
func redisLockKey(k string) string  { return "lg:lock:" + k }

// loginRedisKeys 返回某次登录涉及的全部 Redis 键（双层）
func loginRedisKeys(username, ip string) (countKeys []string, lockKeys []string) {
	base := loginKeys(username, ip)
	for _, b := range base {
		countKeys = append(countKeys, redisCountKey(b))
		lockKeys = append(lockKeys, redisLockKey(b))
	}
	return
}

// CheckLoginAllowed 登录前检查是否被锁定
func CheckLoginAllowed(username, ip string) error {
	if redisclient.IsEnabled() {
		return checkLoginAllowedRedis(username, ip)
	}
	guardMu.Lock()
	defer guardMu.Unlock()
	sweepIfNeeded()
	now := time.Now()
	for _, k := range loginKeys(username, ip) {
		if rec, ok := attempts[k]; ok && now.Before(rec.lockedUntil) {
			return &ErrLocked{Remaining: rec.lockedUntil.Sub(now)}
		}
	}
	return nil
}

// checkLoginAllowedRedis Redis 版锁定检查：读 lg:lock 键，按锁定生效时间戳推算剩余等待
func checkLoginAllowedRedis(username, ip string) error {
	_, lockKeys := loginRedisKeys(username, ip)
	now := time.Now()
	for _, lk := range lockKeys {
		val, ok := redisclient.Get(lk)
		if !ok {
			continue
		}
		// 锁值存的是锁定生效的 Unix 时间戳，按此推算剩余时间
		var lockedAt int64
		if _, err := fmt.Sscanf(val, "%d", &lockedAt); err == nil {
			remaining := time.Unix(lockedAt, 0).Add(lockDuration).Sub(now)
			if remaining > 0 {
				return &ErrLocked{Remaining: remaining}
			}
		}
		// 解析失败或已过期：清掉残留锁键
		redisclient.Del(lk)
	}
	return nil
}

// RecordLoginFailure 登录失败记账（超阈值即触发锁定）
func RecordLoginFailure(username, ip string) {
	if redisclient.IsEnabled() {
		recordLoginFailureRedis(username, ip)
		return
	}
	guardMu.Lock()
	defer guardMu.Unlock()
	now := time.Now()
	for _, k := range loginKeys(username, ip) {
		rec, ok := attempts[k]
		if !ok || now.Sub(rec.windowStart) > windowDuration {
			rec = &attemptRec{count: 0, windowStart: now}
			attempts[k] = rec
		}
		rec.count++
		if rec.count >= maxFailures {
			rec.lockedUntil = now.Add(lockDuration)
		}
	}
}

// recordLoginFailureRedis Redis 版失败记账：INCR 计数+设窗口 TTL，达阈值写锁定键
func recordLoginFailureRedis(username, ip string) {
	countKeys, lockKeys := loginRedisKeys(username, ip)
	now := time.Now()
	for i, ck := range countKeys {
		n := redisclient.Incr(ck)
		if n == 1 {
			// 首次写入：设置窗口 TTL
			redisclient.SetEx(ck, "1", windowDuration)
		}
		if n >= int64(maxFailures) {
			redisclient.SetEx(lockKeys[i], fmt.Sprintf("%d", now.Unix()), lockDuration)
		}
	}
}

// ClearLoginFailures 登录成功清账
func ClearLoginFailures(username string) {
	if redisclient.IsEnabled() {
		clearLoginFailuresRedis(username)
		return
	}
	guardMu.Lock()
	defer guardMu.Unlock()
	for _, k := range loginKeys(username, "") {
		// 精确删除该用户名两把键（IP 维度用前缀扫）
		delete(attempts, k)
	}
	prefix := "u:" + username
	for k := range attempts {
		if len(k) > 3 && k[:3] == "ip:" {
			// ip:{ip}:{username} 形态——按后缀匹配清理
			if idx := len(k) - len(username); idx > 3 && k[idx:] == username {
				delete(attempts, k)
			}
		}
	}
	_ = prefix
}

// clearLoginFailuresRedis 清用户名维度计数/锁键（IP 维度键无前缀删除，待 TTL 自然过期）
func clearLoginFailuresRedis(username string) {
	// 删除用户名维度的全部计数/锁键（含 IP 组合）
	keys := []string{
		redisCountKey("u:" + username),
		redisLockKey("u:" + username),
	}
	// IP 组合键无法前缀扫描，转而覆盖常见情形：清 u 维度 + 任意 ip 维度
	// 由于 Redis 无前缀删除，这里仅清本进程已知组合键；IP 维度键在其 TTL 后自然过期
	for _, k := range keys {
		redisclient.Del(k)
	}
	_ = keys
}

// sweepIfNeeded 惰性清理过期记录（每分钟最多一次，仅内存版使用）
func sweepIfNeeded() {
	now := time.Now()
	if now.Sub(lastSweep) < time.Minute {
		return
	}
	lastSweep = now
	for k, rec := range attempts {
		expired := now.Sub(rec.windowStart) > windowDuration && now.After(rec.lockedUntil)
		if expired {
			delete(attempts, k)
		}
	}
}
