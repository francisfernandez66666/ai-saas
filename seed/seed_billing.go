// Package seed 种子数据填充：本文件为商业化域——预置订阅套餐（subscription_plans）与商业包（packages）。
package seed

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"log"
)

// seedSubscriptionPlans 套餐种子（幂等）：定价见 SAAS_PLAN §8.3
func seedSubscriptionPlans() {
	var cnt int64
	db.DB.Model(&model.SubscriptionPlan{}).Count(&cnt)
	if cnt > 0 {
		return
	}
	plans := []model.SubscriptionPlan{
		{Name: "个人版", Code: "personal", Tier: "personal", PriceMonthlyCents: 9900, PriceYearlyCents: 99900, TrialDays: 7,
			MaxUsers: 1, MaxCustomers: 200, MaxAICalls: 1000, MaxDepartments: 1, MaxKnowledgeBrands: 1, MaxKnowledgeModels: 3,
			Features:   `["ai_response","customer_manage","three_tier_routing","strategy_engine","test_drive"]`,
			Highlights: `["7天免费试用","AI自动接话","200客户"]`, IsActive: true, SortOrder: 1},
		{Name: "企业标准版", Code: "enterprise_std", Tier: "enterprise", PriceMonthlyCents: 49900, PriceYearlyCents: 499900, TrialDays: 7,
			MaxUsers: 5, MaxCustomers: 500, MaxAICalls: 5000, MaxDepartments: 5, MaxKnowledgeBrands: 3, MaxKnowledgeModels: 15,
			Features:   `["ai_response","customer_manage","custom_tags","template_custom","data_export_csv"]`,
			Highlights: `["5席位","自定义标签","CSV导出"]`, IsActive: true, SortOrder: 2},
		{Name: "企业高级版", Code: "enterprise_pro", Tier: "enterprise", PriceMonthlyCents: 99900, PriceYearlyCents: 999900, TrialDays: 7,
			MaxUsers: 20, MaxCustomers: 2000, MaxAICalls: 20000, MaxDepartments: 20, MaxKnowledgeBrands: 10, MaxKnowledgeModels: 50,
			Features:   `["flow_engine","audit_log","data_export_excel","multi_user"]`,
			Highlights: `["20席位","流程引擎","审计日志"]`, IsActive: true, SortOrder: 3},
		{Name: "定制版", Code: "custom", Tier: "custom", PriceMonthlyCents: 0, PriceYearlyCents: 10000000, TrialDays: 0,
			MaxDepartments: 100, Features: `["white_label","open_api","private_deploy"]`,
			Highlights: `["白标独立部署","开放API","专属支持"]`, IsActive: true, SortOrder: 4},
	}
	for i := range plans {
		if err := db.DB.Create(&plans[i]).Error; err != nil {
			log.Printf("[seed] 套餐写入失败 %s: %v", plans[i].Code, err)
		}
	}
	log.Println("已创建 4 个订阅套餐")
}
