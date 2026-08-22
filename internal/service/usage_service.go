package service

import (
	"log"
	"time"

	"gorm.io/gorm"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
)

// ============================================================
// 用量计量服务（SaaS 计费数据底座）
//
// 设计要点：
//   - 原子递增走 SQL（used_ai_calls = used_ai_calls + 1），避免读改写竞态
//   - 明细表 usage_records 按天聚合（UPSERT ON CONFLICT 累加），供对账与账单
//   - 配额判定读租户行；max=0 视为不限
//   - 记录失败仅告警不阻断业务（计量是旁路，不能影响对话主链路）
// ============================================================

// RecordAICall 记一次 AI 调用：租户累计计数 + 当日明细双写
func RecordAICall(tenantID uint) {
	if tenantID == 0 {
		return
	}
	go func() {
		defer func() { _ = recover() }()
		if err := db.DB.Model(&model.Tenant{}).Where("id = ?", tenantID).
			UpdateColumn("used_ai_calls", gorm.Expr("COALESCE(used_ai_calls,0)+1")).Error; err != nil {
			log.Printf("[Usage] 累计计数失败 tenant=%d: %v", tenantID, err)
		}
		today := time.Now().Format("2006-01-02")
		err := db.DB.Exec(`INSERT INTO usage_records (tenant_id, date, metric, value, created_at)
			VALUES (?, ?, 'ai_calls', 1, NOW())
			ON CONFLICT (tenant_id, date, metric) DO UPDATE SET value = usage_records.value + 1`,
			tenantID, today).Error
		if err != nil {
			log.Printf("[Usage] 明细累加失败 tenant=%d: %v", tenantID, err)
		}
	}()
}

// CheckAIQuota 判定租户 AI 调用是否超配额
// 返回 false 表示已超限（调用方应降级为规则话术，不发起模型请求）
func CheckAIQuota(tenantID uint) bool {
	if tenantID == 0 {
		return true // 无租户语境（平台内部）不限
	}
	var row struct {
		Used int `gorm:"column:u"`
		Max  int `gorm:"column:m"`
	}
	err := db.DB.Table("tenants").
		Select("COALESCE(used_ai_calls,0) as u, COALESCE(max_ai_calls_monthly,0) as m").
		Where("id = ?", tenantID).Take(&row).Error
	if err != nil {
		log.Printf("[Usage] 配额查询失败 tenant=%d: %v", tenantID, err)
		return true // 查询异常放行（fail-open，避免计量故障影响客户对话）
	}
	if row.Max == 0 {
		return true // 0=不限
	}
	return row.Used < row.Max
}

// ResetAllTenantsMonthlyUsageIfDue 全租户月度用量重置（缺口5修复，2026-08-22）
//
// 根因：used_ai_calls 此前永不重置（usage_reset_at 字段零引用），累计值比对月配额，
// 租户用满后永久降级规则话术。
// 规则：
//   - usage_reset_at 为空 或 早于本月（< 本月1日0点）→ used_ai_calls 清零、usage_reset_at=执行日
//     （比较锚点是"评估时刻的本月起点"，记录执行日与记录1号语义等价）
//   - 单条 UPDATE 带条件判定，天然幂等；多实例并发执行安全（同月内第二次执行命中 0 行）
//
// 由 main.go 每小时 ticker 调用（Redis 选主，多实例仅主节点执行；未启用 Redis 各实例直跑亦幂等）
func ResetAllTenantsMonthlyUsageIfDue() int {
	// 注意 layout：Go 参考时间是 2006-01-02 15:04:05（01=月、02=日），别写成 2006-01-01
	monthStart := time.Now().Format("2006-01-02") + " 00:00:00"
	res := db.DB.Exec(`UPDATE tenants SET
		used_ai_calls = 0,
		usage_reset_at = ?::timestamp,
		updated_at = NOW()
		WHERE (usage_reset_at IS NULL OR usage_reset_at < ?::timestamp)
		  AND COALESCE(used_ai_calls, 0) > 0`,
		monthStart, monthStart)
	if res.Error != nil {
		log.Printf("[Usage] 月度重置执行失败: %v", res.Error)
		return 0
	}
	if res.RowsAffected > 0 {
		log.Printf("[Usage] 月度用量重置完成：%d 个租户 used_ai_calls 清零（reset_at→%s）",
			res.RowsAffected, monthStart)
	}
	return int(res.RowsAffected)
}
