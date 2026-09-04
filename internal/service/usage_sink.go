// 实时计量批量落库（UsageSink）——移植「翻译助手」billing/sink.go 架构，三桶语义落地。
//
// 背景（2026-09-03 计费统一改造）：
//   旧实现 DeductTokensActual 在 chat_reply.go:245 / gateway/server.go:164 均为 `go func` 异步调用，
//   并发下扣减顺序不保证，且「先回复后扣减」存在失败丢费风险。
//   本文件把实时计量改为「内存累积 + 周期批量落库」：
//   - SinkRecordUsage() 仅追加到内存缓冲，并维护每租户的内存影子余额（seed 自 DB 三桶），
//     余额不足立即记日志（对话场景不中断回复——回复已生成，前置闸 CheckTokenAvailability 已拦截）；
//   - 后台 flusher 每 flushInterval（默认 2s）或缓冲达 maxBatch（默认 200）触发一次，
//     按租户分组、每租户一次 DeductTokensActual（内部单事务+行锁），
//     写事务从「每秒 N 个」降到「每周期每租户 1 个」。
//
// 计费开关（沿用 token_billing.go）：
//   token_billing_enabled=false        —— 完全不启用（no-op，兼容现状）
//   token_billing_enabled=true 且 billing_enforced=false —— 仅落账留痕不扣费（灰度）
//   token_billing_enabled=true 且 billing_enforced=true  —— 真正扣减三桶
package service

import (
	"log"
	"sync"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
)

// usageSinkRecord 单条待落库计量。
type usageSinkRecord struct {
	Tid    uint  // 租户 ID
	Tokens int64 // 本次请求真实消耗 token 数（usage.TotalTokens）
}

// UsageSink 实时计量批量缓冲（进程级单例）。
// shadow 为每租户内存影子余额（三桶可用合计），seed 自 DB，外部发放/充值/退款后经 InvalidateShadow 失效。
type UsageSink struct {
	mu       sync.Mutex
	buf      []usageSinkRecord
	shadow   map[uint]int64 // 每租户内存影子余额
	shadowOk map[uint]bool  // 影子是否已 seed

	flushInterval time.Duration
	maxBatch      int

	wake chan struct{}
	stop chan struct{}
}

// DefaultUsageSink 进程级单例（由 InitUsageSink 启动 flusher）。
var DefaultUsageSink = &UsageSink{
	shadow:        map[uint]int64{},
	shadowOk:      map[uint]bool{},
	flushInterval: 2 * time.Second,
	maxBatch:      200,
	wake:          make(chan struct{}, 1),
	stop:          make(chan struct{}),
}

// InitUsageSink 启动 flusher（main/gateway 启动时调用一次；未启用 Redis 各实例直跑亦幂等）。
func InitUsageSink() {
	go DefaultUsageSink.run()
	log.Println("[UsageSink] 实时计量批量落库已启动（2s/200条周期，每租户单事务）")
}

// InvalidateShadow 使指定租户的影子余额失效，下次 SinkRecordUsage 重新从 DB seed。
// 用途：发放试用/充值/邀请奖励/月度重置后立即调用，避免内存影子余额停留在旧值。
func InvalidateShadow(tid uint) {
	if tid == 0 {
		return
	}
	DefaultUsageSink.mu.Lock()
	delete(DefaultUsageSink.shadowOk, tid)
	delete(DefaultUsageSink.shadow, tid)
	DefaultUsageSink.mu.Unlock()
}

// InvalidateAllShadows 使全部租户的影子余额失效。
// 用途：月度用量重置（bulk UPDATE 命中多个租户）后整体失效，避免残留旧影子误判。
func InvalidateAllShadows() {
	DefaultUsageSink.mu.Lock()
	DefaultUsageSink.shadowOk = map[uint]bool{}
	DefaultUsageSink.shadow = map[uint]int64{}
	DefaultUsageSink.mu.Unlock()
}

// SinkRecordUsage 实时计量入口：投递一次 AI 调用的 token 消耗到批量缓冲。
// 替代旧 `go DeductTokensActual(tenantID, tokens)` 异步扣减。
func SinkRecordUsage(tenantID uint, tokens int64) {
	if tenantID == 0 || tokens <= 0 || !TokenBillingEnabled() {
		return
	}
	DefaultUsageSink.Record(usageSinkRecord{Tid: tenantID, Tokens: tokens})
}

// tenantTokenRemain 读租户三桶可用合计（未过期免费桶 + 月度剩余 + 永久余额）。
// 用于 seed 影子余额。读取失败返回 0（下一周期 flush 自愈回读）。
func tenantTokenRemain(tid uint) int64 {
	var t model.Tenant
	if err := db.DB.Select("free_token_balance", "free_token_expires_at",
		"monthly_token_quota", "monthly_token_used", "token_balance").
		First(&t, tid).Error; err != nil {
		log.Printf("[UsageSink] 影子余额 seed 失败 tenant=%d: %v", tid, err)
		return 0
	}
	now := time.Now()
	total := int64(0)
	if t.FreeTokenExpiresAt == nil || t.FreeTokenExpiresAt.After(now) {
		total += t.FreeTokenBalance
	}
	if avail := t.MonthlyTokenQuota - t.MonthlyTokenUsed; avail > 0 {
		total += avail
	}
	if t.TokenBalance > 0 {
		total += t.TokenBalance
	}
	return total
}

// Record 追加一条计量；仅强制计费时维护影子余额并做不足留痕。
func (s *UsageSink) Record(r usageSinkRecord) {
	s.mu.Lock()
	enforced := billingEnforced()
	if enforced {
		if !s.shadowOk[r.Tid] {
			s.shadow[r.Tid] = tenantTokenRemain(r.Tid)
			s.shadowOk[r.Tid] = true
		}
		s.shadow[r.Tid] -= r.Tokens
		if s.shadow[r.Tid] < 0 {
			log.Printf("[UsageSink] 租户%d 影子余额不足（本次 %d token），欠账 %d（前置闸已拦截，此处仅留痕）",
				r.Tid, r.Tokens, -s.shadow[r.Tid])
		}
	}
	s.buf = append(s.buf, r)
	over := len(s.buf) >= s.maxBatch
	s.mu.Unlock()
	if over {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

// run flusher 主循环。
func (s *UsageSink) run() {
	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-s.wake:
			s.flush()
		case <-ticker.C:
			s.flush()
		}
	}
}

// flush 取出当前缓冲并按租户分组批量扣减（每租户一次 DeductTokensActual，内部单事务+行锁）。
func (s *UsageSink) flush() {
	s.mu.Lock()
	if len(s.buf) == 0 {
		s.mu.Unlock()
		return
	}
	batch := s.buf
	s.buf = nil
	// 影子账本自愈：按租户回读真实三桶余额并重算（外部充值/退款/重置后影子不永久负化）
	byTid := map[uint]int64{}
	for _, r := range batch {
		byTid[r.Tid] += r.Tokens
	}
	for tid, pending := range byTid {
		s.shadow[tid] = tenantTokenRemain(tid) - pending // 真实余额 - 本批待落库量
		s.shadowOk[tid] = true
	}
	s.mu.Unlock()

	for tid, total := range byTid {
		// DeductTokensActual 内部已处理：总闸未开 no-op / 灰度仅留痕 / 强制按 ③→①→② 扣减
		DeductTokensActual(tid, total)
	}
}

// Stop 停止 flusher（优雅停机时调用，执行一次最终 flush）。
func (s *UsageSink) Stop() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	s.flush()
}
