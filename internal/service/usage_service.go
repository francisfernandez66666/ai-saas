// 用量计量底座：AI 调用计数/配额判定/月度重置 + usage_ledger 落账与分级看板。
package service

import (
	"fmt"
	"log"
	"strings"
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

// RecordAICall 记一次 AI 调用的当日明细（仅 usage_records 聚合，不含累计计数）。
// 累计计数 used_ai_calls 的原子预留改由 ConsumeAIQuota 统一负责，避免"读判+后增"竞态。
func RecordAICall(tenantID uint) {
	if tenantID == 0 {
		return
	}
	go func() {
		defer func() { _ = recover() }()
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

// ConsumeAIQuota 用量累计统计（P1 计费统一 2026-09-03 起降级为统计旁路）
//
// 历史职责（H4）：原子地"判定+预留+计量"一次 AI 调用，超额即返回 false 降级规则话术。
// 计费统一后：token 三桶扣减（UsageSink 批量落库）成为唯一计费闸，
// 本函数**恒放行**（不再拦截），仅负责 used_ai_calls 累计计数 + usage_records 明细（看板口径）。
//
// 返回值恒 true：调用方（chat_reply/gateway）不再需要按"次"降级，改由 CheckTokenAvailability 前置闸拦截。
func ConsumeAIQuota(tenantID uint) bool {
	if tenantID == 0 {
		return true // 无租户语境（平台内部）不限
	}
	// SQL 原子递增（并发安全），仅统计不拦截
	if err := db.DB.Model(&model.Tenant{}).Where("id = ?", tenantID).
		UpdateColumn("used_ai_calls", gorm.Expr("COALESCE(used_ai_calls,0)+1")).Error; err != nil {
		log.Printf("[Usage] 计数失败 tenant=%d: %v", tenantID, err)
	}
	RecordAICall(tenantID)
	return true
}

// ResetAllTenantsMonthlyUsageIfDue 全租户月度用量重置（H4 修复，2026-08-22 初版）
//
// 根因：used_ai_calls 此前永不重置（usage_reset_at 字段零引用），累计值比对月配额，
// 租户用满后永久降级规则话术。H4 补充：订阅 token 桶(monthly_token_used) 当初同样
// 只会在发放付费包时重置，月底从不自动清零，导致付费租户第二月 token 配额永久耗尽。
// 规则：
//   - usage_reset_at 为空 或 早于本月（< 本月1日0点）→ ① used_ai_calls 清零
//     ② monthly_token_used 清零（订阅桶月底清零）、usage_reset_at=执行日
//   - 单条 UPDATE 带条件判定，天然幂等；多实例并发执行安全（同月内第二次执行命中 0 行）
//
// 由 main.go 每小时 ticker 调用（Redis 选主，多实例仅主节点执行；未启用 Redis 各实例直跑亦幂等）
func ResetAllTenantsMonthlyUsageIfDue() int {
	// 注意 layout：Go 参考时间是 2006-01-02 15:04:05（01=月、02=日），别写成 2006-01-01
	monthStart := time.Now().Format("2006-01-02") + " 00:00:00"
	res := db.DB.Exec(`UPDATE tenants SET
		used_ai_calls = 0,
		monthly_token_used = 0,
		usage_reset_at = ?::timestamp,
		updated_at = NOW()
		WHERE (usage_reset_at IS NULL OR usage_reset_at < ?::timestamp)`,
		monthStart, monthStart)
	if res.Error != nil {
		log.Printf("[Usage] 月度重置执行失败: %v", res.Error)
		return 0
	}
	if res.RowsAffected > 0 {
		log.Printf("[Usage] 月度用量重置完成：%d 个租户 used_ai_calls/monthly_token_used 清零（reset_at→%s）",
			res.RowsAffected, monthStart)
		InvalidateAllShadows() // 计费统一：月度重置批量改写三桶后整体失效影子余额
	}
	return int(res.RowsAffected)
}

// ============================================================
// Token 计量底座（商业化第二批 M3）
// usage_ledger 请求级落账：只记账不扣费——商业包"次"扣减逻辑不变；
// 计价仅为成本核算展示口径：cost = total_tokens ÷1000 × 单价 × markup
// ============================================================

// RecordUsage 异步落账一条 LLM 调用记录（失败仅告警，绝不阻断对话主链路）
func RecordUsage(tenantID, customerID, userID uint, stage, provider, modelName string,
	promptTokens, completionTokens int, latencyMs int64) {
	if tenantID == 0 || (promptTokens <= 0 && completionTokens <= 0) {
		return // mock/规则兜底路径无用量
	}
	go func() {
		defer func() { _ = recover() }()
		total := promptTokens + completionTokens
		cost := estimateCostMicro(provider, total)
		row := model.UsageLedger{
			TenantID: tenantID, CustomerID: customerID, UserID: userID,
			Stage: stage, Provider: provider, Model: modelName,
			PromptTokens: promptTokens, CompletionTokens: completionTokens,
			TotalTokens: total, CostMicro: cost, LatencyMs: int(latencyMs),
			CreatedAt: time.Now(),
		}
		if err := db.DB.Create(&row).Error; err != nil {
			log.Printf("[Usage] ledger落账失败 tenant=%d: %v", tenantID, err)
		}
	}()
}

// estimateCostMicro 成本估算（微元）= total_tokens ÷1000 × 单价(微元/千token) × 均摊系数
func estimateCostMicro(provider string, totalTokens int) int64 {
	var unitPrice int64 = 8000 // 默认硅基流动档
	if strings.Contains(strings.ToLower(provider), "zhipu") {
		unitPrice = int64(DefaultSystemConfigService.GetInt("price_micro_per_ktok_zhipu", 15000))
	} else {
		unitPrice = int64(DefaultSystemConfigService.GetInt("price_micro_per_ktok_siliconflow", 8000))
	}
	markup := DefaultSystemConfigService.GetFloat("billing_markup_multiplier", 1.5)
	return int64(float64(totalTokens) / 1000 * float64(unitPrice) * markup)
}

// UsageDayRow 按日聚合行
type UsageDayRow struct {
	Date      string `json:"date"`
	Calls     int64  `json:"calls"`
	Tokens    int64  `json:"tokens"`
	CostMicro int64  `json:"cost_micro"`
}

// UsageStageRow 按阶段聚合行
type UsageStageRow struct {
	Stage     string `json:"stage"`
	Calls     int64  `json:"calls"`
	Tokens    int64  `json:"tokens"`
	CostMicro int64  `json:"cost_micro"`
}

// UsageCostByModelRow 模型维度成本行（超管选型依据）
type UsageCostByModelRow struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Calls     int64  `json:"calls"`
	Tokens    int64  `json:"tokens"`
	CostMicro int64  `json:"cost_micro"`
}

// UsageSummaryByDay 租户按日趋势
func UsageSummaryByDay(tenantID uint, days int) ([]UsageDayRow, error) {
	rows := []UsageDayRow{}
	err := db.DB.Model(&model.UsageLedger{}).
		Select(`TO_CHAR(created_at,'YYYY-MM-DD') AS date,
			COUNT(*) AS calls, COALESCE(SUM(total_tokens),0) AS tokens,
			COALESCE(SUM(cost_micro),0) AS cost_micro`).
		Where("tenant_id = ? AND created_at >= NOW() - ?::interval", tenantID, fmt.Sprintf("%d days", days)).
		Group("date").Order("date ASC").Scan(&rows).Error
	return rows, err
}

// UsageStageDistribution 租户内阶段分布
func UsageStageDistribution(tenantID uint, days int) ([]UsageStageRow, error) {
	rows := []UsageStageRow{}
	err := db.DB.Model(&model.UsageLedger{}).
		Select(`COALESCE(stage,'unknown') AS stage,
			COUNT(*) AS calls, COALESCE(SUM(total_tokens),0) AS tokens,
			COALESCE(SUM(cost_micro),0) AS cost_micro`).
		Where("tenant_id = ? AND created_at >= NOW() - ?::interval", tenantID, fmt.Sprintf("%d days", days)).
		Group("stage").Order("tokens DESC").Scan(&rows).Error
	return rows, err
}

// UsageCostByModel 全平台模型成本核算（超管）
func UsageCostByModel(days int) ([]UsageCostByModelRow, error) {
	rows := []UsageCostByModelRow{}
	err := db.DB.Model(&model.UsageLedger{}).
		Select(`provider, model, COUNT(*) AS calls,
			COALESCE(SUM(total_tokens),0) AS tokens,
			COALESCE(SUM(cost_micro),0) AS cost_micro`).
		Where("created_at >= NOW() - ?::interval", fmt.Sprintf("%d days", days)).
		Group("provider, model").Order("cost_micro DESC").Scan(&rows).Error
	return rows, err
}
