// 统计看板接口：概览指标（租户隔离经 db.RQ + DataScope 自动盖章）
package api

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/schema"
	"net/http"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 统计API
// 数据看板相关接口
// ============================================================

/*
GetOverview 获取统计概览

用途：返回租户级统计数据，供管理后台看板展示。

数据隔离：
  - 通过 db.RQ(c) 自动注入 tenant_id 条件（租户隔离）
  - 通过 db.DataScope(c) 注入部门级数据范围裁剪

指标说明：
  - totalCustomers: 有效客户总数（status=1）
  - activeConversations: 当前活跃会话数
  - avgIntent: 平均意向分（客户价值评估）
  - transferRate: 转人工率（AI 接待能力指标）
  - conversionRate: 转化率（意向分≥0.8 视为转化）

返回：
  - code: 0 表示成功
  - data: StatsOverview 结构体
*/
func GetOverview(c *gin.Context) {
	// 总客户数（有效客户：status=1）
	var totalCustomers int64
	db.RQ(c).Scopes(db.DataScope(c)).Model(&model.Customer{}).Where("status = ?", 1).Count(&totalCustomers)

	// 活跃会话数（status=active）
	var activeConversations int64
	db.RQ(c).Scopes(db.DataScope(c)).Model(&model.Conversation{}).Where("status = ?", "active").Count(&activeConversations)

	// 平均意向分（客户价值评估指标）
	var avgIntent float64
	db.RQ(c).Scopes(db.DataScope(c)).Model(&model.Customer{}).Where("status = ?", 1).Select("AVG(intent_score)").Row().Scan(&avgIntent)

	// 转人工率：转人工会话数 / 总会话数
	var totalConversations int64
	db.RQ(c).Scopes(db.DataScope(c)).Model(&model.Conversation{}).Count(&totalConversations)
	var humanConversations int64
	db.RQ(c).Scopes(db.DataScope(c)).Model(&model.Conversation{}).Where("mode = ?", "human").Count(&humanConversations)
	var transferRate float64
	if totalConversations > 0 {
		transferRate = float64(humanConversations) / float64(totalConversations)
	}

	// 转化率：意向分 ≥ 0.8（业务转化阈值）视为转化
	var convertedCustomers int64
	db.RQ(c).Scopes(db.DataScope(c)).Model(&model.Customer{}).Where("status = ? AND intent_score >= ?", 1, 0.8).Count(&convertedCustomers)
	var conversionRate float64
	if totalCustomers > 0 {
		conversionRate = float64(convertedCustomers) / float64(totalCustomers)
	}

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data: schema.StatsOverview{
			TotalCustomers:      totalCustomers,
			NewCustomersToday:   0, // 简化，实际需按日期统计
			ActiveConversations: activeConversations,
			ConversionRate:      conversionRate,
			AvgIntentScore:      avgIntent,
			HumanTransferRate:   transferRate,
		},
	})
}
