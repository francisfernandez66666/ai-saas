// 数据层卫生观测位（批六，2026-09-23）
//
// 为什么在 metrics 而不是 service：这两项都是"给运维看趋势、不改变业务行为"的只读探针，
// 与 ComputeHealth 同属观测面（readiness 判配置、本文件判数据形态），放 service 反而会让
// 健康探测链路反向依赖业务编排层。
//
// 两项观测：
//  1. orphan_messages —— messages 里 conversation_id=0 的行数。
//     这类行既进不了会话维度统计、又虚增"客户消息数"分母（D2 贡献度看板就因此被同类
//     口径错位坑过一次），迁移 019 已回填存量并建部分索引把计数成本从全表扫描降到索引内。
//     判级只到 warn，不判 crit：孤儿行不烧钱、不泄数据，crit 会为一件不影响客户的事刷群。
//  2. message_archive_backlog —— 超过 message_archive_days 仍在热表里的行数。
//     归档开关默认 0（关）＝"永久留在热表"是当前的有意选择，但决定要看得见：
//     本项在关闭时只报 disabled 不查库（避免为一条展示语句全表扫描），启用后按归档器
//     自己的口径数同一批行，等于"下一次归档会搬多少"的前瞻值。
//
// 缓存纪律：探针会被 /status/detail 与告警 tick 反复调用，热表计数绝不逐次现算——
// TTL 内复用上一次结果，取数失败沿用旧值并标记 stale（宁可不新，也不要每次扫全表）。
//
// 同族的第三项「企微会话存档形态」在 archive_hygiene.go（取数由 main.go 注入，
// 因为观测位不得反向 import internal/channel）；三项一起由 hygieneChecks 组装。
package metrics

import (
	"strconv"
	"sync"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
)

// 探针实现接缝：单测注入固定计数/时钟，不依赖真库（真库形态由迁移 019 与冒烟脚本覆盖）
var (
	orphanCountFn                     = queryOrphanMessages
	archiveBacklogFn                  = queryArchiveBacklog
	hygieneNow       func() time.Time = time.Now
	hygieneProbeTTL                   = 10 * time.Minute
)

// hygieneCache 三项缓存（同一次探测共享，避免 /status/detail 与告警 tick 互相打穿）
type hygieneCache struct {
	at        time.Time
	orphan    int64
	backlog   int64
	backlogOK bool
	stale     bool
}

var (
	hygieneMu    sync.Mutex
	hygieneState hygieneCache
)

// queryOrphanMessages 孤儿消息计数（走迁移 019 的部分索引 idx_messages_orphan_no_conv）
func queryOrphanMessages() (int64, error) {
	var n int64
	err := db.DB.Model(&model.Message{}).Where("conversation_id = 0").Count(&n).Error // g12:platform 平台级卫生计数：跨全部租户统计孤儿消息，不属任何请求作用域
	return n, err
}

// queryArchiveBacklog 归档前瞻计数：热表里已超过归档年龄的天数阈值行数。
// days<=0（默认关闭）返回 ok=false，调用方据此跳过查询不落库扫描。
func queryArchiveBacklog(days int) (int64, bool, error) {
	if days <= 0 {
		return 0, false, nil
	}
	var n int64
	err := db.DB.Model(&model.Message{}). // g12:platform 平台级卫生计数：归档前瞻按全表年龄统计，无租户作用域
						Where("created_at < ?", hygieneNow().AddDate(0, 0, -days)).
						Count(&n).Error
	return n, true, err
}

// hygieneSnapshot 取当前卫生观测值（带 TTL 缓存）。
// 返回 orphan=孤儿行数、backlog=待归档行数、backlogEnabled=归档是否已启用、stale=是否沿用过旧值。
func hygieneSnapshot() (orphan, backlog int64, backlogEnabled, stale bool) {
	hygieneMu.Lock()
	defer hygieneMu.Unlock()
	now := hygieneNow()
	if !hygieneState.at.IsZero() && now.Sub(hygieneState.at) < hygieneProbeTTL {
		return hygieneState.orphan, hygieneState.backlog, hygieneState.backlogOK, hygieneState.stale
	}
	next := hygieneCache{at: now, orphan: hygieneState.orphan, backlog: hygieneState.backlog, backlogOK: hygieneState.backlogOK}
	if n, err := orphanCountFn(); err == nil {
		next.orphan = n
	} else {
		next.stale = true // 取数失败沿用旧值：观测位不得因一次抖动清零假装健康
	}
	days := 0
	if runtimecfg.DefaultSystemConfigService != nil {
		days = runtimecfg.DefaultSystemConfigService.GetInt("message_archive_days", 0)
	}
	if n, ok, err := archiveBacklogFn(days); err == nil {
		next.backlog, next.backlogOK = n, ok
	} else {
		next.stale = true
	}
	hygieneState = next
	return next.orphan, next.backlog, next.backlogOK, next.stale
}

// ResetHygieneCache 清空探针缓存（测试与手工重探用；生产按 TTL 自然过期）
func ResetHygieneCache() {
	hygieneMu.Lock()
	hygieneState = hygieneCache{}
	hygieneMu.Unlock()
	// 死信观测位与卫生观测位共用"重置后立刻重探"的语义（测试与手工重探入口）
	ResetMQAuditCache()
}

// hygieneChecks 组装健康观测项（由 ComputeHealth 调用）：
// 两项数据层卫生 + 一项企微会话存档形态（archive_hygiene.go，未装配时判 not_wired 不报故障）
// + 一项消息中心台账死信（mq_audit_hygiene.go，G-15③）
func hygieneChecks() []HealthCheck {
	orphan, backlog, backlogOn, stale := hygieneSnapshot()
	out := make([]HealthCheck, 0, 4)

	warn := monitorCfgInt("monitor_orphan_warn", 100)
	ch := intCheck("orphan_messages", orphan, warn, monitorCfgInt("monitor_orphan_crit", 1<<40),
		"messages.conversation_id=0 的孤儿行数（迁移 019 回填 + 写入口护栏，只准降不准升）")
	ch.CritAt = "-" // 不判 crit：孤儿行不烧钱不泄密，crit 只会刷群
	if stale {
		ch.Value = ch.Value + "（沿用 " + hygieneProbeTTL.String() + " 内的上一次取数，本轮查询失败）"
	}
	out = append(out, ch)

	backlogCheck := HealthCheck{
		Name: "message_archive_backlog", Status: StatusOK, Value: "disabled",
		WarnAt: "-", CritAt: "-",
		Desc: "message_archive_days=0（默认）：冷数据不归档，messages 只增不减——启用前需产品确认历史可见性",
	}
	if backlogOn {
		days := 0
		if runtimecfg.DefaultSystemConfigService != nil {
			days = runtimecfg.DefaultSystemConfigService.GetInt("message_archive_days", 0)
		}
		backlogCheck.Value = strconv.FormatInt(backlog, 10)
		backlogCheck.Desc = "热表中已超过 " + strconv.Itoa(days) + " 天、下次归档会搬走的行数"
		if backlog >= monitorCfgInt("monitor_archive_warn", 200000) {
			backlogCheck.Status = StatusWarn
		}
	}
	out = append(out, backlogCheck)
	out = append(out, archiveCheck())
	out = append(out, mqAuditCheck())
	return out
}

// DataHygiene 对外暴露当前数据层卫生观测值（带 TTL 缓存，供 /status/detail 直接取字段）。
// archiveEnabled=false 表示 message_archive_days=0（归档关闭），此时 backlog 无意义不展示。
func DataHygiene() (orphan, backlog int64, archiveEnabled bool) {
	o, bl, on, _ := hygieneSnapshot()
	return o, bl, on
}
