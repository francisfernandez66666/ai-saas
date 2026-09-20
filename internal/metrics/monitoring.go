// Package metrics 承载健康探测、阈值告警与 Prometheus 指标渲染。
package metrics

import "ai-scrm/internal/notify"

// ============================================================
// 监控与阈值告警（P1-4，2026-08-29）
//
// 现状：仅 /status 暴露 db_ok + critical_alerts_24h 计数，无阈值分级、无主动通知。
// 目标：结构化的健康探针（DB/合并队列深度/24h 严重事件/goroutine）+ 阈值分级
//       （ok/warn/crit）+ 越 crit 阈值经企微/钉钉群主动通知（复用 notifier，带冷却防刷）。
// 阈值可由 system_config 覆盖（monitor_* 前缀），缺省内置合理值。
// ============================================================

import (
	"ai-scrm/internal/runtimecfg"
	"context"
	"runtime"
	"strconv"
	"sync"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/redisclient"
)

// HealthStatus 健康分级枚举
// ok=正常，warn=告警（达到警告阈值），crit=严重（达到严重阈值，触发主动通知）
type HealthStatus string

const (
	// StatusOK 正常状态：指标在健康范围内
	StatusOK HealthStatus = "ok"
	// StatusWarn 告警状态：指标达到警告阈值，需关注但暂不影响服务
	StatusWarn HealthStatus = "warn"
	// StatusCrit 严重状态：指标达到严重阈值，触发企微/钉钉群主动通知
	StatusCrit HealthStatus = "crit"
)

// HealthCheck 单指标探测结果
// 每个指标独立探测，包含当前值、阈值和描述
type HealthCheck struct {
	Name   string       `json:"name"`    // 指标名称（如 db, merge_queue_depth, goroutines）
	Status HealthStatus `json:"status"`  // 当前健康分级
	Value  string       `json:"value"`   // 当前值（字符串格式，方便展示）
	WarnAt string       `json:"warn_at"` // 警告阈值
	CritAt string       `json:"crit_at"` // 严重阈值
	Desc   string       `json:"desc"`    // 指标描述（中文）
}

// HealthSnapshot 一次完整探测结果
// 包含所有指标的探测结果和全局状态标志
type HealthSnapshot struct {
	DBOK    bool          `json:"db_ok"`    // 数据库是否连通
	Checks  []HealthCheck `json:"checks"`   // 所有指标的探测结果
	HasCrit bool          `json:"has_crit"` // 是否存在crit级指标
	HasWarn bool          `json:"has_warn"` // 是否存在warn级指标
}

// alertCooldown 各指标通知冷却（避免每次探测都刷群），name -> 上次通知时间
// 采用sync.Map支持并发安全访问，不同指标独立冷却
var alertCooldown sync.Map

// alertCooldownSec 同一指标的冷却时间（秒）
// 防止短时间内重复发送大量通知，避免群消息刷屏
const alertCooldownSec = 600 // 10分钟冷却

// intCheck 数值型指标分级（越大越糟）
// 通用阈值分级函数：value < warn → OK, warn ≤ value < crit → WARN, value ≥ crit → CRIT
func intCheck(name string, value, warn, crit int64, desc string) HealthCheck {
	st := StatusOK
	if value >= crit {
		st = StatusCrit
	} else if value >= warn {
		st = StatusWarn
	}
	return HealthCheck{
		Name:   name,
		Status: st,
		Value:  strconv.FormatInt(value, 10),
		WarnAt: strconv.FormatInt(warn, 10),
		CritAt: strconv.FormatInt(crit, 10),
		Desc:   desc,
	}
}

// monitorCfgInt 读系统配置阈值，缺省回退 def
// 支持通过 system_configs 表动态调整监控阈值，无需重启
func monitorCfgInt(key string, def int64) int64 {
	if runtimecfg.DefaultSystemConfigService == nil {
		return def
	}
	return int64(runtimecfg.DefaultSystemConfigService.GetInt(key, int(def)))
}

// ComputeHealth 执行一次完整健康探测
// 探测项目：DB连通性、合并队列深度、24h严重事件数、goroutine数
// 返回 HealthSnapshot 包含所有指标的探测结果
func ComputeHealth() HealthSnapshot {
	snap := HealthSnapshot{Checks: []HealthCheck{}}

	// 1. DB 连通性（crit 级）
	dbOK := false
	if sqlDB, err := db.DB.DB(); err == nil {
		if pingErr := sqlDB.Ping(); pingErr == nil {
			dbOK = true
		}
	}
	snap.DBOK = dbOK
	dbStatus := StatusOK
	if !dbOK {
		dbStatus = StatusCrit
	}
	snap.Checks = append(snap.Checks, HealthCheck{
		Name:   "db",
		Status: dbStatus,
		Value:  map[bool]string{true: "up", false: "down"}[dbOK],
		WarnAt: "-",
		CritAt: "down",
		Desc:   "PostgreSQL 连通性",
	})

	// 2. 合并队列积压深度
	queueDepth := int64(0)
	if queueDepthFunc != nil {
		queueDepth = int64(queueDepthFunc())
	}
	snap.Checks = append(snap.Checks, intCheck("merge_queue_depth", queueDepth,
		monitorCfgInt("monitor_queue_warn", 50), monitorCfgInt("monitor_queue_crit", 200),
		"消息合并队列活跃会话数（积压预警）"))

	// 3. 24h 严重事件数（TenantAuditLog action 含 critical）
	var crit24h int64
	if dbOK {
		db.DB.Model(&model.TenantAuditLog{}).
			Where("action LIKE '%critical%' AND created_at >= NOW() - INTERVAL '24 hours'").
			Count(&crit24h)
	}
	snap.Checks = append(snap.Checks, intCheck("critical_24h", crit24h,
		monitorCfgInt("monitor_crit_warn", 10), monitorCfgInt("monitor_crit_crit", 50),
		"近 24h 严重审计事件数"))

	// 4. goroutine 数（内存/泄漏预警）
	gc := int64(runtime.NumGoroutine())
	snap.Checks = append(snap.Checks, intCheck("goroutines", gc,
		monitorCfgInt("monitor_goroutine_warn", 1000), monitorCfgInt("monitor_goroutine_crit", 3000),
		"运行时 goroutine 数"))

	for _, c := range snap.Checks {
		if c.Status == StatusCrit {
			snap.HasCrit = true
		}
		if c.Status == StatusWarn {
			snap.HasWarn = true
		}
	}
	// G6 修复(2026-09-14)：多实例但无 Redis 的"静默降级单实例语义"显式化——
	// release 模式且声明副本数>1 时，合并队列/WS 广播/登录锁都会各说各话（消息可能双处理、
	// 跨实例推送丢失、防爆破锁不共享），必须红灯而非无声运行。副本数经 SetDeploymentContext 注入。
	if multiInstanceNoRedis() {
		check := HealthCheck{
			Name:   "instance_coordination",
			Status: StatusCrit,
			Value:  "replicas>1_without_redis",
			WarnAt: "-",
			CritAt: "多实例但未启用 Redis",
			Desc:   "多实例部署须启用 Redis（分布式锁/合并裁决/跨实例广播）",
		}
		snap.Checks = append(snap.Checks, check)
		snap.HasCrit = true
	}
	// F6：出站死信积压水位——堆积说明有通道持续投递失败（凭据失效/端点异常），
	// 超阈值转 warn 并纳入 /status，运维可在通道页人工重发（不做 crit，避免误伤个别租户死信）。
	if dl := GetChannelDeadLetterPending(); dl >= deadLetterWarnThreshold {
		snap.Checks = append(snap.Checks, HealthCheck{
			Name:   "channel_dead_letter",
			Status: StatusWarn,
			Value:  strconv.FormatInt(dl, 10),
			WarnAt: strconv.Itoa(deadLetterWarnThreshold),
			CritAt: "-",
			Desc:   "通道出站死信积压，疑似某通道凭据失效或端点异常，请查 /admin 通道页重发",
		})
	}
	return snap
}

// deadLetterWarnThreshold 出站死信积压告警阈值（超过计入 /status warn）。
const deadLetterWarnThreshold = 50

// 部署上下文（G6）：由 main 启动时注入，供健康检查判断多实例降级。
var (
	deployReplicas  int
	deployIsRelease bool
)

// SetDeploymentContext 注入副本数与是否 release，供 ComputeHealth 判定多实例降级红灯。
func SetDeploymentContext(replicas int, isRelease bool) {
	deployReplicas = replicas
	deployIsRelease = isRelease
}

// multiInstanceNoRedis 声明副本>1 且当前无 Redis 视为危险降级（release 才判，避免本地误报）。
func multiInstanceNoRedis() bool {
	return deployIsRelease && deployReplicas > 1 && !redisclient.IsEnabled()
}

// MaybeAlert 对 crit 级指标主动通知（带冷却）
// 仅当存在crit级指标时触发通知，通过企微和钉钉双通道发送
// 同一指标10分钟内只通知一次，避免刷屏
func MaybeAlert(snap HealthSnapshot) {
	if !snap.HasCrit {
		return
	}
	var lines string
	for _, c := range snap.Checks {
		if c.Status != StatusCrit {
			continue
		}
		// 冷却：同一指标 10 分钟内只通知一次，避免刷屏。
		// P2-4(2026-09-20 批三)：冷却表双轨——Redis 可用走 alert:cd:<name>（SETNX+TTL，跨实例共享，
		// 多实例不再各刷一次群）；键已存在=冷却中跳过，Redis 出错退回本机内存冷却兜底（宁可多报不漏报，
		// 与 ClaimReplyDelivery 的"故障≠他人已认领"同口径）。旧内存 sync.Map 保留为降级路径。
		coolKey := "alert:cd:" + c.Name
		coolMem := func() bool {
			if t, ok := alertCooldown.Load(c.Name); ok {
				if last, ok2 := t.(time.Time); ok2 && time.Since(last) < alertCooldownSec*time.Second {
					return false
				}
			}
			alertCooldown.Store(c.Name, time.Now())
			return true
		}
		if redisclient.IsEnabled() {
			acquired, err := redisclient.SetNXExE(coolKey, "1", alertCooldownSec*time.Second)
			if err == nil && !acquired {
				continue // Redis 冷却生效（本窗口已有人发过）
			}
			if err != nil {
				if !coolMem() {
					continue // Redis 故障降级：本机冷却中
				}
			}
		} else if !coolMem() {
			continue // 单实例内存冷却中（原语义）
		}
		lines += "> - **" + c.Name + "** 触发严重阈值：当前 " + c.Value + "（crit≥" + c.CritAt + "）\n"
	}
	if lines == "" {
		return
	}
	msg := "## ⚠️ AI-SCRM 健康告警\n" + lines + "> 时间：" + time.Now().Format("2006-01-02 15:04:05")
	// 双通道：企微优先，钉钉兜底（notifier 内部未配置静默跳过）
	notify.NotifyWecom(msg)
	notify.NotifyDingtalk(msg)
}

// StartAlertSelfCheck P2-4 修复(2026-09-20 批三)：告警从"纯拉驱动"补上后台自检 push——
// 旧 MaybeAlert 唯一触发点是 /status handler，没有探活流量（部署在内网/监控未接）就永不通知。
// 现每 interval 自检一次 ComputeHealth+MaybeAlert；Redis 可用时先抢 metrics:alert:tick 短锁选主，
// 多实例只有一台执行（冷却键 alert:cd:* 虽已跨实例共享，选主仍省掉 N 份探针开销与通知竞争）。
// ctx 取消即停，随停机信号收尾；本函数不阻塞调用方。
func StartAlertSelfCheck(ctx context.Context, interval time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if redisclient.IsEnabled() {
					h := redisclient.TryLock("metrics:alert:tick", interval-time.Second)
					if h == nil {
						continue // 本窗口他实例已自检
					}
					MaybeAlert(ComputeHealth())
					h.Unlock()
					continue
				}
				MaybeAlert(ComputeHealth())
			}
		}
	}()
}
