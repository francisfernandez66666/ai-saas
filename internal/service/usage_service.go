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
