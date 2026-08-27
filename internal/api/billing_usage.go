package api

import (
	"net/http"
	"strconv"

	"ai-scrm/internal/service"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 用量看板 API（商业化第二批 M3，借鉴 SAAS_ROADMAP 阶段四分级看板）
//
//   GET /api/v1/admin/usage/summary?days=30  tenant_admin：本租户趋势+阶段分布
//   GET /api/v1/super/usage/cost?days=30     super_admin：全平台模型成本核算
// 数据源：usage_ledger（请求级台账）；计价为展示口径，不参与扣费
// ============================================================

// AdminUsageSummary GET /api/v1/admin/usage/summary —— 本租户汇总
func AdminUsageSummary(c *gin.Context) {
	days := parseDays(c)
	tid := tenantIDOf(c)

	days7, _ := service.UsageSummaryByDay(tid, days)
	stages, _ := service.UsageStageDistribution(tid, days)

	var totalTokens, totalCost, totalCalls int64
	for _, r := range days7 {
		totalTokens += r.Tokens
		totalCost += r.CostMicro
		totalCalls += r.Calls
	}

	c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{
		"days":         days,
		"total_calls":  totalCalls,
		"total_tokens": totalTokens,
		// 微元→元保留4位小数展示
		"total_cost_yuan": microToYuan(totalCost),
		"by_day":          days7,
		"by_stage":        stages,
	}})
}

// SuperUsageCost GET /api/v1/super/usage/cost?days=30 —— 全平台模型成本
func SuperUsageCost(c *gin.Context) {
	days := parseDays(c)

	rows, err := service.UsageCostByModel(days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "查询失败"})
		return
	}
	var totalCost, totalTokens, totalCalls int64
	for _, r := range rows {
		totalCost += r.CostMicro
		totalTokens += r.Tokens
		totalCalls += r.Calls
	}
	// costRow 单模型成本聚合行，按 provider/model 维度归因（含占比便于定位高成本模型）
	type costRow struct {
		Provider     string  `json:"provider"`
		Model        string  `json:"model"`
		Calls        int64   `json:"calls"`
		Tokens       int64   `json:"tokens"`
		CostYuan     float64 `json:"cost_yuan"`
		CostSharePct float64 `json:"cost_share_pct"`
	}
	list := make([]costRow, 0, len(rows))
	for _, r := range rows {
		share := 0.0
		if totalCost > 0 {
			share = float64(r.CostMicro) / float64(totalCost) * 100
		}
		list = append(list, costRow{
			Provider: r.Provider, Model: r.Model, Calls: r.Calls,
			Tokens: r.Tokens, CostYuan: microToYuan(r.CostMicro), CostSharePct: share,
		})
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{
		"days":            days,
		"total_calls":     totalCalls,
		"total_tokens":    totalTokens,
		"total_cost_yuan": microToYuan(totalCost),
		"models":          list,
	}})
}

// parseDays 解析 days 查询参数（限1~365，默认30）
func parseDays(c *gin.Context) int {
	days, _ := strconv.Atoi(c.DefaultQuery("days", "30"))
	if days < 1 || days > 365 {
		days = 30
	}
	return days
}

// microToYuan 微元→元 展示换算（保留精度由前端决定小数位）
func microToYuan(micro int64) float64 {
	return float64(micro) / 1e6
}
