// 实时计量批量落库（UsageSink）——移植「翻译助手」billing/sink.go 架构，三桶语义落地。
//
// 背景（2026-09-03 计费统一改造）：
//
//	旧实现 DeductTokensActual 在 chat_reply.go:245 / gateway/server.go:164 均为 `go func` 异步调用，
//	并发下扣减顺序不保证，且「先回复后扣减」存在失败丢费风险。
//	本文件把实时计量改为「内存累积 + 周期批量落库」：
//	- SinkRecordUsage() 仅追加到内存缓冲，并维护每租户的内存影子余额（seed 自 DB 三桶），
//	  余额不足立即记日志（对话场景不中断回复——回复已生成，前置闸 CheckTokenAvailability 已拦截）；
//	- 后台 flusher 每 flushInterval（默认 2s）或缓冲达 maxBatch（默认 200）触发一次，
//	  按租户分组、每租户一次 DeductTokensActual（内部单事务+行锁），
//	  写事务从「每秒 N 个」降到「每周期每租户 1 个」。
//
// 计费开关（沿用 token_billing.go）：
//
//	token_billing_enabled=false        —— 完全不启用（no-op，兼容现状）
//	token_billing_enabled=true 且 billing_enforced=false —— 仅落账留痕不扣费（灰度）
//	token_billing_enabled=true 且 billing_enforced=true  —— 真正扣减三桶
//
// P0-1 改造（2026-09-20 审计批）：强制计费路径"写前挂账"——
//
//	flush 先按租户把合计数写 usage_flush_retry（迁移/建表见 model.UsageFlushRetry），
//	再逐租户在**同一事务**内"锁定行(SKIP LOCKED)→DELETE→三桶扣减"原子核销。
//	扣减失败/进程崩溃时欠账以行形态存活：快速重投 3 次 → 60s sweep 补扫 →
//	启动即全量补扫（上次崩溃遗留），超 30 分钟未清账群催办人工介入。
//	弃批从"永久漏账"降级为"延后扣"。影子余额同步升级为 Redis 共享计数
//	（sink:shadow:<tid>，多实例一致；未启用 Redis 退回内存 map）。
//	残余窗口（如实声明）：Record 入内存缓冲后、flush 落表前被 SIGKILL，
//	丢 ≤1 个 flush 周期（2s/200 条）；SIGTERM 走 Stop() 最终 flush 无损。
package billing

import (
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/notify"
	"ai-scrm/internal/redisclient"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// shadowKey Redis 共享影子余额键（P0-1 配套：多实例下影子计数以 Redis 为准）。
func shadowKey(tid uint) string { return "sink:shadow:" + strconv.FormatUint(uint64(tid), 10) }

// shadowRedisTTL 影子键 TTL：影子是缓存不是账本，过期后由 seed 竞争重建。
const shadowRedisTTL = 10 * time.Minute

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
	maxBuf        int // R16：缓冲硬上限，DB 长时间不可用时防 flush 失败堆积 OOM

	dropped int64 // R16：超上限丢弃计数（欠账留痕，非静默）

	alertedAt map[uint]time.Time // P0-1：挂账告警租户级冷却（1h），防刷群

	wake chan struct{}
	stop chan struct{}
}

// DefaultUsageSink 进程级单例（由 InitUsageSink 启动 flusher）。
var DefaultUsageSink = &UsageSink{
	shadow:        map[uint]int64{},
	shadowOk:      map[uint]bool{},
	alertedAt:     map[uint]time.Time{},
	flushInterval: 2 * time.Second,
	maxBatch:      200,
	maxBuf:        50000,
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
	DefaultUsageSink.invalidateShadowMem(tid)
}

// InvalidateAllShadows 使全部租户的影子余额失效。
// 用途：月度用量重置（bulk UPDATE 命中多个租户）后整体失效，避免残留旧影子误判。
func InvalidateAllShadows() {
	DefaultUsageSink.mu.Lock()
	tenants := make([]uint, 0, len(DefaultUsageSink.shadowOk))
	for tid := range DefaultUsageSink.shadowOk {
		tenants = append(tenants, tid)
	}
	DefaultUsageSink.shadowOk = map[uint]bool{}
	DefaultUsageSink.shadow = map[uint]int64{}
	DefaultUsageSink.mu.Unlock()
	for _, tid := range tenants { // P0-1：Redis 共享影子同步失效，多实例一起重 seed
		redisclient.Del(shadowKey(tid))
	}
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
// R16 修复(2026-09-11)：①冷 seed 的 tenantTokenRemain（DB 查询）移出锁——原实现持锁
// 读库，DB 抖动时对话热路径全部串行卡死（P1-17 把 flush 的 DB 读移出了锁，seed 路径漏了）；
// ②缓冲加硬上限 maxBuf，flush 持续失败时丢弃最旧并计数告警——宁可欠账留痕，不可 OOM 带走全站。
func (s *UsageSink) Record(r usageSinkRecord) {
	enforced := billingEnforced()
	if enforced {
		// P0-1 配套：影子 seed/扣减走 Redis 共享计数（多实例一致），未启用 Redis 退回内存
		s.seedShadowIfAbsent(r.Tid)
		if after := s.decrShadow(r.Tid, r.Tokens); after < 0 {
			log.Printf("[UsageSink] 租户%d 影子余额不足（本次 %d token），欠账 %d（前置闸已拦截，此处仅留痕）",
				r.Tid, r.Tokens, -after)
		}
	}
	// 缓冲段整体在锁内（P0-1 重构时误删了这把 Lock，189 行 Unlock 变双解 → fatal；
	// 且 len(s.buf) 无锁读与 flush 换出存在数据竞争）
	s.mu.Lock()
	if len(s.buf) >= s.maxBuf {
		s.dropped++
		d := s.dropped
		// C8 修复(2026-09-14)：溢出时丢弃**最旧**记录并收新（旧实现丢新收旧，
		// 与"丢弃最旧"注释口径相反；最新计量对欠账追补更关键，DB 恢复期优先保新账）
		copy(s.buf, s.buf[1:])
		s.buf[len(s.buf)-1] = r
		s.mu.Unlock()
		if d == 1 || d%1000 == 0 { // 首条与每千条告警，避免刷屏
			log.Printf("[UsageSink][ERROR] 缓冲超上限(%d)已丢弃最旧第 %d 条计量（DB 长时间不可用？已落挂账表的欠账由 sweep 补核）", s.maxBuf, d)
		}
		// 丢弃后立即推一次 flush，尽量把已积压的落库
		select {
		case s.wake <- struct{}{}:
		default:
		}
		return
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
// R19 配套(2026-09-11)：flush panic 若击穿本 goroutine，缓冲将永不再落库直至触顶丢弃——
// 每轮包 recover，单轮崩溃只丢该批（挂账表兜底欠账），不带走 flusher。
// P0-1 配套(2026-09-20)：启动即补扫一次挂账表（上次进程崩溃遗留），此后每 60s sweep；
// 多实例下 Redis 锁裁决由谁扫（未启用 Redis 的单实例直接扫）。
func (s *UsageSink) run() {
	s.sweepSafe(true)
	flushTicker := time.NewTicker(s.flushInterval)
	sweepTicker := time.NewTicker(time.Minute)
	defer flushTicker.Stop()
	defer sweepTicker.Stop()
	for {
		select {
		case <-s.stop:
			s.flushSafe() // 停机前最终落盘（挂账行随批落表，未核销的由下次启动补扫）
			return
		case <-s.wake:
			s.flushSafe()
		case <-flushTicker.C:
			s.flushSafe()
		case <-sweepTicker.C:
			s.sweepSafe(false)
		}
	}
}

// sweepSafe 带锁裁决 + panic 护栏的一轮补扫。atStart=true 时启动即扫（不等 30s 龄）。
func (s *UsageSink) sweepSafe(atStart bool) {
	if redisclient.IsEnabled() {
		h := redisclient.TryLock("sink:retry:sweep", 50*time.Second)
		if h == nil {
			return // 其它实例在扫；挂账行不丢，下轮抢到再核销
		}
		defer h.Unlock()
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[UsageSink][PANIC] sweep 崩溃已拦截：%v", r)
		}
	}()
	if atStart {
		s.sweepRetryRowsAt(time.Now()) // 启动补扫：不看年龄阈值，直接核销所有滞留行
		return
	}
	s.sweepRetryRows()
}

// flushSafe 带 panic 护栏的一轮 flush。
func (s *UsageSink) flushSafe() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[UsageSink][PANIC] flush 崩溃已拦截，本批跳过：%v", r)
		}
	}()
	s.flush()
}

// flush 取出当前缓冲并按租户分组批量扣减（每租户一次，内部单事务+行锁）。
// P1-17 修复(2026-09-09)：①锁内只换出 byTid，DB 查询与影子写回移到锁外；②扣减失败回补影子重投。
// P0-1 改造(2026-09-20 审计批)：强制计费路径"写前挂账"——byTid 先落 usage_flush_retry，
// 再逐租户"核销挂账行+扣减"同事务原子提交。任何失败/崩溃欠账都以行形态存活，
// 由启动即扫 + 60s sweep 补扣。弃批从"永久漏账"降级为"延后扣"。
// 残余窗口（如实声明）：Record 入内存缓冲后、本 flush 落表前被 SIGKILL，丢 ≤1 个 flush 周期
// （2s/200 条）的挂账；SIGTERM 走 Stop() 最终 flush 无损。
//
// M1 修复批注(2026-09-22)：上面"失败即行存活"此前对**三桶余额不足**并不成立——
// deductTokensInTx 旧实现在扣不动时 `log + return nil`，事务照常提交，:545 的 DELETE 已把
// 挂账行删掉，于是"行灭账未扣"，注释与实现相互矛盾。现该分支改为返回哨兵错误使事务回滚
// （行保留待 sweep），部分扣减的差额则同事务补写新挂账行，总额守恒。
func (s *UsageSink) flush() {
	s.mu.Lock()
	if len(s.buf) == 0 {
		s.mu.Unlock()
		return
	}
	batch := s.buf
	s.buf = nil
	byTid := map[uint]int64{}
	for _, r := range batch {
		byTid[r.Tid] += r.Tokens
	}
	s.mu.Unlock()

	// 灰度/未强制：DeductTokensActual 本就 no-op 留痕，无需挂账开销（旧轻量路径）
	if !billingEnforced() {
		for tid, total := range byTid {
			_ = DeductTokensActual(tid, total)
		}
		return
	}

	// 影子账本自愈：锁外按租户回读真实三桶余额并重算（外部充值/退款/重置后影子不永久负化）
	s.reseedShadows(byTid)

	// 写前挂账：一批 INSERT（单事务）。DB 完全不可用时整批回插缓冲延后处理——
	// 不再"内存重投 3 次即弃"。
	rows := make([]model.UsageFlushRetry, 0, len(byTid))
	for tid, total := range byTid {
		if total <= 0 {
			continue
		}
		rows = append(rows, model.UsageFlushRetry{TenantID: tid, Tokens: total})
	}
	if len(rows) == 0 {
		return
	}
	if err := db.DB.Create(&rows).Error; err != nil {
		log.Printf("[UsageSink] 挂账表写入失败（DB 不可用？整批回插缓冲延后）: %v", err)
		s.mu.Lock()
		s.buf = append(batch, s.buf...) // 保序回插，下轮 flush 再试
		over := len(s.buf) >= s.maxBuf
		s.mu.Unlock()
		if over {
			select {
			case s.wake <- struct{}{}:
			default:
			}
		}
		return
	}
	ids := make([]uint, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	s.settleIDs(ids)
}

// settleIDs 尝试核销给定挂账行并扣减（P0-1）。返回未核销完的租户数（>0 时保留待 sweep，
// 连续失败由 sweep 侧统一催办，本函数只重试 3 次快速路径）。
func (s *UsageSink) settleIDs(ids []uint) {
	remaining := append([]uint(nil), ids...)
	for attempt := 1; attempt <= 3 && len(remaining) > 0; attempt++ {
		next := settleRetryRows(remaining)
		if len(next) == 0 {
			return
		}
		if attempt < 3 {
			log.Printf("[UsageSink] %d 笔挂账核销失败，重投（第%d/3次）", len(next), attempt)
			time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
		}
		remaining = next
	}
	if len(remaining) > 0 {
		// 不再弃批：行仍在挂账表，60s sweep 会继续补扣；此处只失效影子防超发
		for tid := range tenantsOfRetryRows(remaining) {
			s.invalidateShadowMem(tid)
		}
		log.Printf("[UsageSink][WARN] %d 笔挂账快速重投仍失败，已留存挂账表待 sweep 补扣", len(remaining))
	}
}

// sweepRetryRows 周期补扫：核销创建超 30s 的滞留挂账行（覆盖快速路径失败 + 崩溃残留）。
// 单轮上限 2000 行；超 30 分钟仍未清账的按租户群告警（每小时冷却）。
func (s *UsageSink) sweepRetryRows() {
	s.sweepRetryRowsAt(time.Now().Add(-30 * time.Second))
}

// sweepRetryRowsAt 核销 created_at 早于 threshold 的滞留行（threshold=now 即全量，启动补扫用）。
func (s *UsageSink) sweepRetryRowsAt(threshold time.Time) {
	var rows []model.UsageFlushRetry
	if err := db.DB.Where("created_at < ?", threshold).
		Order("id ASC").Limit(2000).Find(&rows).Error; err != nil {
		log.Printf("[UsageSink] sweep 读挂账表失败: %v", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	ids := make([]uint, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	leftover := settleRetryRows(ids)
	if len(leftover) > 0 {
		log.Printf("[UsageSink] sweep 后仍挂账 %d 笔（下轮继续）", len(leftover))
		tids := tenantsOfRetryRows(leftover)
		for tid, tokens := range tids {
			if s.alertDue(tid) {
				notify.NotifyGroup(fmt.Sprintf("【计费告警】租户%d 计量扣减挂账 %d tokens 持续核销失败（最近一笔残留 %v），请检查 DB/该租户行锁竞争",
					tid, tokens, leftoverAge(leftover, tid)))
			}
		}
	}
	// 超 30 分钟催办：本轮虽核销成功，但历史长龄行若仍存在则告警（单独查询）
	var stale []model.UsageFlushRetry
	db.DB.Where("created_at < ?", time.Now().Add(-30*time.Minute)).Limit(50).Find(&stale)
	for _, r := range stale {
		if s.alertDue(r.TenantID) {
			notify.NotifyGroup(fmt.Sprintf("【计费告警】租户%d 有一笔 %d tokens 计量挂账超 30 分钟未清（id=%d），人工介入排查",
				r.TenantID, r.Tokens, r.ID))
		}
	}
}

// alertDue 租户级告警冷却（1h），防刷群。
func (s *UsageSink) alertDue(tid uint) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.alertedAt == nil {
		s.alertedAt = map[uint]time.Time{}
	}
	if t, ok := s.alertedAt[tid]; ok && time.Since(t) < time.Hour {
		return false
	}
	s.alertedAt[tid] = time.Now()
	return true
}

// reseedShadows 锁外回读真实三桶余额并重算影子（含 Redis 共享影子同步）。
func (s *UsageSink) reseedShadows(byTid map[uint]int64) {
	for tid, pending := range byTid {
		remain := tenantTokenRemain(tid)
		s.setShadow(tid, remain-pending)
	}
}

// invalidateShadowMem 失效指定租户影子（下轮 Record 重 seed）。
func (s *UsageSink) invalidateShadowMem(tid uint) {
	s.mu.Lock()
	delete(s.shadowOk, tid)
	delete(s.shadow, tid)
	s.mu.Unlock()
	redisclient.Del(shadowKey(tid))
}

// setShadow 写影子余额：内存 + Redis（多实例共享，可用时以 Redis 计数为准）。
func (s *UsageSink) setShadow(tid uint, val int64) {
	s.mu.Lock()
	s.shadow[tid] = val
	s.shadowOk[tid] = true
	s.mu.Unlock()
	if redisclient.IsEnabled() {
		redisclient.SetEx(shadowKey(tid), strconv.FormatInt(val, 10), shadowRedisTTL)
	}
}

// seedShadowIfAbsent 影子缺失时从 DB seed（双检：内存标记 + Redis SetNX）。
func (s *UsageSink) seedShadowIfAbsent(tid uint) {
	s.mu.Lock()
	needSeed := !s.shadowOk[tid]
	s.mu.Unlock()
	if !needSeed {
		return
	}
	remain := tenantTokenRemain(tid)
	if redisclient.IsEnabled() {
		// Redis 已有其他实例 seed 的值则不覆盖（近似共享计数，冲突无害：影子仅留痕用）
		if ok := redisclient.SetNXEx(shadowKey(tid), strconv.FormatInt(remain, 10), shadowRedisTTL); !ok {
			if v, ok2 := redisShadowGet(tid); ok2 {
				remain = v
			}
		}
	}
	s.mu.Lock()
	if !s.shadowOk[tid] {
		s.shadow[tid] = remain
		s.shadowOk[tid] = true
	}
	s.mu.Unlock()
}

// redisShadowGet 读 Redis 共享影子值；未启用/缺失/非数字返回 (0,false)。
func redisShadowGet(tid uint) (int64, bool) {
	v, ok := redisclient.Get(shadowKey(tid))
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// decrShadow 影子扣减：Redis 可用走共享 DECRBY（多实例一致），失败/未启用退回内存。
// 返回扣减后的影子余额（仅留痕用，不阻断对话）。
func (s *UsageSink) decrShadow(tid uint, tokens int64) int64 {
	if redisclient.IsEnabled() {
		if v, ok := redisclient.DecrByWithTTL(shadowKey(tid), tokens, shadowRedisTTL); ok {
			return v
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.shadowOk[tid] {
		return 0 // 未 seed（seed 刚失败的极端窗口）：不做无依据的负值判定
	}
	s.shadow[tid] -= tokens
	return s.shadow[tid]
}

// settleRetryRows 核销给定挂账行（P0-1）：按租户分组，每租户一个事务内
// "锁定行(FOR UPDATE SKIP LOCKED) → DELETE → deductTokensInTx 扣三桶"原子提交。
// 崩溃/失败时行仍在表里，sweep 与重启补扫会再核销——欠账以行形态存活，不再弃批。
// 返回未核销成功的行 ID 子集（被并发实例锁走或事务失败）。
func settleRetryRows(ids []uint) []uint {
	if len(ids) == 0 {
		return nil
	}
	// 先按 (id, tenant_id) 读出分组（普通读，锁定在事务内重做）
	// 注意字段名须按 GORM 命名约定映射 tenant_id 列（写成 TID 会静默扫成 0 → 核销全失败）
	type rowKey struct {
		ID       uint
		TenantID uint
		Tokens   int64
	}
	var rows []rowKey
	if err := db.DB.Model(&model.UsageFlushRetry{}).
		Select("id", "tenant_id", "tokens").
		Where("id IN ?", ids).
		Scan(&rows).Error; err != nil {
		log.Printf("[UsageSink] 挂账行读取失败: %v", err)
		return ids // 整批未核销
	}
	byTid := map[uint][]rowKey{}
	for _, r := range rows {
		byTid[r.TenantID] = append(byTid[r.TenantID], r)
	}
	settled := map[uint]bool{}
	for tid, rs := range byTid {
		rowIDs := make([]uint, 0, len(rs))
		for _, r := range rs {
			rowIDs = append(rowIDs, r.ID)
		}
		err := db.DB.Transaction(func(tx *gorm.DB) error {
			// P0-1：usage_flush_retry 已入 RLS 清单——RLS_ENABLED=true 时必须先激活租户作用域，
			// 否则锁定读被策略滤成 0 行，挂账永不可核销（deductTokensInTx 内的激活晚于本查询）
			if r := db.SetTenantRLS(tx, tid); r.Error != nil {
				return r.Error
			}
			// 行级锁定 + SKIP LOCKED：并发实例/快速重投不会双扣同一行
			var locked []uint
			if err := tx.Model(&model.UsageFlushRetry{}).
				Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
				Where("id IN ?", rowIDs).
				Pluck("id", &locked).Error; err != nil {
				return err
			}
			if len(locked) == 0 {
				return nil // 全被并发实例锁走，本侧不管
			}
			// 本事务内锁定行合计（不能沿用读阶段值：可能含被抢走的行）——先 SUM 后 DELETE
			var lockTok int64
			if err := tx.Model(&model.UsageFlushRetry{}).
				Where("id IN ?", locked).
				Select("COALESCE(SUM(tokens),0)").Row().Scan(&lockTok); err != nil {
				return err
			}
			// M1(2026-09-22)：本 DELETE 与下面的扣减同事务——扣减任何失败（含三桶皆空的
			// errTokenDebtUnsettled 哨兵）都会回滚本删除，绝不会出现"行灭账未扣"。
			if err := tx.Where("id IN ?", locked).Delete(&model.UsageFlushRetry{}).Error; err != nil {
				return err
			}
			if err := deductTokensInTx(tx, tid, lockTok); err != nil {
				return err
			}
			for _, id := range locked {
				settled[id] = true
			}
			return nil
		})
		if err != nil {
			log.Printf("[UsageSink] 挂账核销失败 tenant=%d rows=%d: %v", tid, len(rowIDs), err)
		}
	}
	left := make([]uint, 0, len(ids))
	for _, id := range ids {
		if !settled[id] {
			left = append(left, id)
		}
	}
	return left
}

// tenantsOfRetryRows 读出给定挂账行 ID 对应的租户 → 合计量（告警口径用）。
func tenantsOfRetryRows(ids []uint) map[uint]int64 {
	type agg struct {
		TID    uint
		Tokens int64
	}
	var rows []agg
	if err := db.DB.Model(&model.UsageFlushRetry{}).
		Select("tenant_id AS tid, SUM(tokens) AS tokens").
		Where("id IN ?", ids).
		Group("tenant_id").
		Scan(&rows).Error; err != nil {
		return map[uint]int64{}
	}
	out := map[uint]int64{}
	for _, r := range rows {
		out[r.TID] = r.Tokens
	}
	return out
}

// leftoverAge 该租户最早滞留挂账行的年龄（告警文案用）。
func leftoverAge(ids []uint, tid uint) time.Duration {
	var r model.UsageFlushRetry
	if err := db.DB.Where("id IN ? AND tenant_id = ?", ids, tid).
		Order("id ASC").First(&r).Error; err != nil {
		return 0
	}
	return time.Since(r.CreatedAt)
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
