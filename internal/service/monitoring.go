// Package service 提供 SCRM 业务服务层实现（计费/消息/配置/脱敏/监控/向量等）。
package service

// ============================================================
// 监控与阈值告警（P1-4，2026-08-29）
//
// 现状：仅 /status 暴露 db_ok + critical_alerts_24h 计数，无阈值分级、无主动通知。
// 目标：结构化的健康探针（DB/合并队列深度/24h 严重事件/goroutine）+ 阈值分级
//       （ok/warn/crit）+ 越 crit 阈值经企微/钉钉群主动通知（复用 notifier，带冷却防刷）。
// 阈值可由 system_config 覆盖（monitor_* 前缀），缺省内置合理值。
// ============================================================

import (
	"runtime"
	"strconv"
	"sync"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
)

// HealthStatus 健康分级
type HealthStatus string

const (
	StatusOK   HealthStatus = "ok"   // 正常
	StatusWarn HealthStatus = "warn" // 告警
	StatusCrit HealthStatus = "crit" // 严重
)

// HealthCheck 单指标探测结果
type HealthCheck struct {
	Name   string       `json:"name"`
	Status HealthStatus `json:"status"`
	Value  string       `json:"value"`
	WarnAt string       `json:"warn_at"`
	CritAt string       `json:"crit_at"`
	Desc   string       `json:"desc"`
}

// HealthSnapshot 一次完整探测
type HealthSnapshot struct {
	DBOK    bool          `json:"db_ok"`
	Checks  []HealthCheck `json:"checks"`
	HasCrit bool          `json:"has_crit"`
	HasWarn bool          `json:"has_warn"`
}

// alertCooldown 各指标通知冷却（避免每次探测都刷群），name -> 上次通知时间
var alertCooldown sync.Map

const alertCooldownSec = 600 // 同一指标 10 分钟内至多通知一次

// intCheck 数值型指标分级（越大越糟）
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
func monitorCfgInt(key string, def int64) int64 {
	if DefaultSystemConfigService == nil {
		return def
	}
	return int64(DefaultSystemConfigService.GetInt(key, int(def)))
}

// ComputeHealth 执行一次完整健康探测
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
	if DefaultMessageQueueService != nil {
		queueDepth = int64(DefaultMessageQueueService.ActiveQueueCount())
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
	return snap
}

// MaybeAlert 对 crit 级指标主动通知（带冷却）
func MaybeAlert(snap HealthSnapshot) {
	if !snap.HasCrit {
		return
	}
	var lines string
	for _, c := range snap.Checks {
		if c.Status != StatusCrit {
			continue
		}
		// 冷却：同一指标 10 分钟内只通知一次
		if t, ok := alertCooldown.Load(c.Name); ok {
			if last, ok2 := t.(time.Time); ok2 && time.Since(last) < alertCooldownSec*time.Second {
				continue
			}
		}
		alertCooldown.Store(c.Name, time.Now())
		lines += "> - **" + c.Name + "** 触发严重阈值：当前 " + c.Value + "（crit≥" + c.CritAt + "）\n"
	}
	if lines == "" {
		return
	}
	msg := "## ⚠️ AI-SCRM 健康告警\n" + lines + "> 时间：" + time.Now().Format("2006-01-02 15:04:05")
	// 双通道：企微优先，钉钉兜底（notifier 内部未配置静默跳过）
	NotifyWecom(msg)
	NotifyDingtalk(msg)
}
