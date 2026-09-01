// 用量与计量查询API：租户/平台级 Token 用量与成本看板。
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

// AdminUsageSummary 租户管理员用量汇总看板
// GET /api/v1/admin/usage/summary?days=30
// 返回本租户指定天数内的每日用量趋势和AI阶段分布
// 用于租户管理员监控自身资源消耗
func AdminUsageSummary(c *gin.Context) {
	days := parseDays(c)
	tid := tenantIDOf(c)

	// 按天聚合Token用量和成本
	days7, _ := service.UsageSummaryByDay(tid, days)
	// 按AI阶段（如意图识别、多轮对话等）分布统计
	stages, _ := service.UsageStageDistribution(tid, days)

	// 汇总计算总用量
	var totalTokens, totalCost, totalCalls int64
	for _, r := range days7 {
		totalTokens += r.Tokens
		totalCost += r.CostMicro
		totalCalls += r.Calls
	}

	RespOK(c, "", gin.H{
		"days":         days,
		"total_calls":  totalCalls,
		"total_tokens": totalTokens,
		// 微元→元保留4位小数展示
		"total_cost_yuan": microToYuan(totalCost),
		"by_day":          days7,
		"by_stage":        stages,
	})
}

// SuperUsageCost 超管全平台模型成本核算
// GET /api/v1/super/usage/cost?days=30
// 按 provider/model 维度聚合成本，计算各模型的费用占比
// 用于平台运营定位高成本模型，优化资源分配
func SuperUsageCost(c *gin.Context) {
	days := parseDays(c)

	// 按模型维度查询全平台用量数据
	rows, err := service.UsageCostByModel(days)
	if err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "查询失败")
		return
	}
	// 计算全平台总成本，用于计算各模型费用占比
	var totalCost, totalTokens, totalCalls int64
	for _, r := range rows {
		totalCost += r.CostMicro
		totalTokens += r.Tokens
		totalCalls += r.Calls
	}
	// costRow 单模型成本聚合行，按 provider/model 维度归因（含占比便于定位高成本模型）
	type costRow struct {
		Provider     string  `json:"provider"`       // 模型供应商
		Model        string  `json:"model"`          // 模型名称
		Calls        int64   `json:"calls"`          // 调用次数
		Tokens       int64   `json:"tokens"`         // Token消耗量
		CostYuan     float64 `json:"cost_yuan"`      // 费用（元）
		CostSharePct float64 `json:"cost_share_pct"` // 费用占全平台百分比
	}
	// 构建响应列表，同时计算各模型费用占比
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
	RespOK(c, "", gin.H{
		"days":            days,
		"total_calls":     totalCalls,
		"total_tokens":    totalTokens,
		"total_cost_yuan": microToYuan(totalCost),
		"models":          list,
	})
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
