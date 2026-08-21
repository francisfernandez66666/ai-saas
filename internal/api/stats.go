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

// GetOverview 获取统计概览
func GetOverview(c *gin.Context) {
	// 总客户数
	var totalCustomers int64
	db.RQ(c).Model(&model.Customer{}).Where("status = ?", 1).Count(&totalCustomers)

	// 活跃会话数
	var activeConversations int64
	db.RQ(c).Model(&model.Conversation{}).Where("status = ?", "active").Count(&activeConversations)

	// 平均意向分
	var avgIntent float64
	db.RQ(c).Model(&model.Customer{}).Where("status = ?", 1).Select("AVG(intent_score)").Row().Scan(&avgIntent)

	// 转人工率
	var totalConversations int64
	db.RQ(c).Model(&model.Conversation{}).Count(&totalConversations)
	var humanConversations int64
	db.RQ(c).Model(&model.Conversation{}).Where("mode = ?", "human").Count(&humanConversations)
	var transferRate float64
	if totalConversations > 0 {
		transferRate = float64(humanConversations) / float64(totalConversations)
	}

	// 转化率（意向分>0.8的视为转化）
	var convertedCustomers int64
	db.RQ(c).Model(&model.Customer{}).Where("status = ? AND intent_score >= ?", 1, 0.8).Count(&convertedCustomers)
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
