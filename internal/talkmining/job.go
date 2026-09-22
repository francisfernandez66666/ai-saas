// Package talkmining —— 本文件为作业层（批五 D 接线，2026-09-23）：把 mining + draft
// 串成"一轮全租户出稿"的离线任务，供 main.go 的每日 ticker 调用。
//
// 为什么单独成文件：mining.go 零 DB、draft.go 只做过库，都不认识"作业"这件事
// （枚举租户、钳窗口、限流、日志）。作业层薄到只有装配，逻辑仍可单测。
package talkmining

import (
	"fmt"
	"log"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
)

// cfgBoolForTenant/cfgIntForTenant 租户生效值读取（nil 安全）：
// 这些 talkmining_* 键**不是**平台级键——租户管理员在后台就能改，写的是 (tenant_id,key) 覆盖层。
// 作业层若只读系统默认层，租户改了值就永远看不到变更（本仓已为此踩过两次，
// 见 config_defaults.go 的 email_verify_enabled 批注）。
// 传参 def 由调用方给"上一层已解析的值"，于是天然形成 租户覆盖 > 入参(=系统默认) 的优先级。
func cfgBoolForTenant(tid uint, key string, def bool) bool {
	svc := runtimecfg.DefaultSystemConfigService
	if svc == nil {
		return def
	}
	return svc.GetBoolForTenant(tid, key, def)
}

func cfgIntForTenant(tid uint, key string, def int) int {
	svc := runtimecfg.DefaultSystemConfigService
	if svc == nil {
		return def
	}
	return svc.GetIntForTenant(tid, key, def)
}

// draftEnabledFor 该租户是否开启出稿作业（系统默认 false → 全租户关，租户可各自开）。
func draftEnabledFor(tid uint) bool {
	return cfgBoolForTenant(tid, "talkmining_draft_enabled", false)
}

// 作业默认参数（各值理由见行内注释）
const (
	// DefaultRecordLimit 单租户单轮读取的顾问消息上限。挖掘是"找高频好话术"，
	// 按时间倒序取前 N 条足以覆盖近期打法；不设上限会把整年消息读进内存。
	DefaultRecordLimit = 5000
	// DefaultMaxTenantsPerRun 单轮处理的租户数上限（按最近有顾问消息的租户优先）。
	// 作业是锦上添花，不该在一次运行里占满 DB 连接。
	DefaultMaxTenantsPerRun = 50
)

// SweepStat 一轮全租户出稿的账目（累计各租户 DraftStat + 覆盖租户数，供日志与冒烟断言用）。
type SweepStat struct {
	Tenants             int // 实际处理的租户数（已过出稿开关闸）
	TenantsDisabled     int // 窗口内有素材但该租户未开开关的租户数（一个 token 也没烧）
	TenantsFailed       int // 读库/挖掘出错的租户数（fail-open 跳过，不中断整轮）
	Created             int // 新草稿模板总数
	SkippedInsufficient int
	SkippedExisting     int
	SkippedLLM          int
}

// activeTenantIDs 枚举窗口内有过顾问人工回复的租户 ID（按最近消息时间倒序，钳上限）。
// 只取"确实有素材"的租户，避免对空租户做无谓查询。后台作业无请求 ctx，故裸 db.DB
// 并在同一行显式带条件（G-12 A 类白名单口径），聚合查询本身不含单租户数据行。
func activeTenantIDs(since time.Time, limit int) ([]uint, error) {
	if db.DB == nil {
		return nil, fmt.Errorf("talkmining: db 未初始化")
	}
	var ids []uint
	q := db.DB.Model(&model.Message{}). // g12:platform
						Select("tenant_id").
						Where("sender_type = 'human' AND sender_id > 0 AND tenant_id > 0 AND created_at >= ?", since).
						Group("tenant_id").
						Order("MAX(created_at) DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Pluck("tenant_id", &ids).Error; err != nil {
		return nil, fmt.Errorf("talkmining: 枚举活跃租户失败: %w", err)
	}
	return ids, nil
}

// RunDraftSweep 一轮全租户话术挖掘出稿。返回累计账目；只有"枚举租户"这一步失败才算 error
// （单租户失败只计入 TenantsFailed 并继续，离线作业不该被一个脏租户整体打断）。
//
// 参数：windowDays 回溯窗口（默认建议 30）；maxDraftsPerTenant 单租户单轮出稿上限；
// minSamples 出结论组的最小客户样本数（<=0 用 DefaultMinSamples）。
// 三者都只是**系统层默认**：租户在自己后台配了 talkmining_* 覆盖时以租户值为准，
// talkmining_draft_enabled 同样按租户判定——没开的租户连素材都不读（本作业会烧真实 token）。
// GenerateDraftFunc 未注入时整轮零出稿（draft.go 自带守卫），这里仍会跑完统计便于观察候选簇。
func RunDraftSweep(windowDays, maxDraftsPerTenant, minSamples int) (SweepStat, error) {
	var stat SweepStat
	if windowDays <= 0 {
		windowDays = 30
	}
	since := time.Now().AddDate(0, 0, -windowDays)
	ids, err := activeTenantIDs(since, DefaultMaxTenantsPerRun)
	if err != nil {
		return stat, err
	}
	// 门槛基线：入参 <=0 表示"用包内默认"，租户未覆盖时沿用该基线
	baseMinSamples := minSamples
	if baseMinSamples <= 0 {
		baseMinSamples = DefaultMinSamples
	}
	for _, tid := range ids {
		// 逐租户过闸（2026-09-23 批六）：出稿会烧真实 token，开关没开的租户一个都不烧。
		// 注意这道闸在"读取素材"之前——防"先查完库再判开关"的白做功。
		if !draftEnabledFor(tid) {
			stat.TenantsDisabled++
			continue
		}
		stat.Tenants++
		// 窗口/上限/门槛都按租户生效值取：入参是系统层默认，租户覆盖优先
		win := cfgIntForTenant(tid, "talkmining_window_days", windowDays)
		if win <= 0 {
			win = windowDays
		}
		tenantSince := time.Now().AddDate(0, 0, -win)
		maxDrafts := cfgIntForTenant(tid, "talkmining_max_drafts_per_run", maxDraftsPerTenant)
		if maxDrafts <= 0 {
			maxDrafts = maxDraftsPerTenant
		}
		minSamp := cfgIntForTenant(tid, "talkmining_min_samples", baseMinSamples)
		if minSamp <= 0 {
			minSamp = baseMinSamples
		}
		records, err := LoadAdvisorRecords(tid, tenantSince, time.Now(), DefaultRecordLimit)
		if err != nil {
			stat.TenantsFailed++
			log.Printf("[talkmining] 租户%d 读取顾问消息失败(fail-open 跳过): %v", tid, err)
			continue
		}
		clusters := Mine(records, Options{MinSamples: minSamp})
		s, err := DraftTemplates(tid, clusters, maxDrafts)
		if err != nil {
			stat.TenantsFailed++
			log.Printf("[talkmining] 租户%d 出稿失败(fail-open 跳过): %v", tid, err)
			continue
		}
		stat.Created += s.Created
		stat.SkippedInsufficient += s.SkippedInsufficient
		stat.SkippedExisting += s.SkippedExisting
		stat.SkippedLLM += s.SkippedLLM
	}
	return stat, nil
}
