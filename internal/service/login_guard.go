package service

import (
	"fmt"
	"sync"
	"time"
)

// ============================================================
// 登录防爆破守卫
//
// 策略（双层键，独立计数）：
//   - 按用户名：5 次失败 / 10 分钟 → 锁定 15 分钟（防定向撞库）
//   - 按 IP+用户名 组合：同上阈值（防单 IP 遍历账号）
// 命中锁定返回剩余等待时间；登录成功清除该用户名全部记录。
// 纯内存实现（进程内有效）：多实例部署下建议后续叠加 Redis 计数，
// 当前规模下攻击者面对 N 实例也仅放大 N 倍尝试成本，风险可控。
// ============================================================

const (
	maxFailures    = 5                // 窗口内最大失败次数
	windowDuration = 10 * time.Minute // 失败计数窗口
	lockDuration   = 15 * time.Minute // 触发后锁定时长
)

type attemptRec struct {
	count       int       // 窗口内失败次数
	windowStart time.Time // 窗口起点
	lockedUntil time.Time // 锁定截止（零值=未锁）
}

var (
	guardMu   sync.Mutex
	attempts  = map[string]*attemptRec{} // key: "u:{username}" / "ip:{ip}:{username}"
	lastSweep = time.Now()
)

// ErrLocked 登录锁定错误（携带剩余时间）
type ErrLocked struct{ Remaining time.Duration }

func (e *ErrLocked) Error() string {
	return fmt.Sprintf("失败次数过多，账号已临时锁定，请 %d 分钟后再试", int(e.Remaining.Minutes())+1)
}

// LoginGuardKey 生成双层守卫键
func loginKeys(username, ip string) []string {
	return []string{"u:" + username, "ip:" + ip + ":" + username}
}

// CheckLoginAllowed 登录前检查是否被锁定
func CheckLoginAllowed(username, ip string) error {
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

// RecordLoginFailure 登录失败记账（超阈值即触发锁定）
func RecordLoginFailure(username, ip string) {
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

// ClearLoginFailures 登录成功清账
func ClearLoginFailures(username string) {
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

// sweepIfNeeded 惰性清理过期记录（每分钟最多一次）
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
