// 顾问工作台路由（D2a，2026-09-12）
package api

import (
	"github.com/gin-gonic/gin"

	"ai-scrm/internal/middleware"
)

// registerAdvisor 顾问端（销售）工作台：需登录 + 租户一致性 + 组织上下文 + 首登强改密 + 只读写闸。
func registerAdvisor(v1 *gin.RouterGroup) {
	advisorGroup := v1.Group("/advisor")
	advisorGroup.Use(middleware.JWTAuth(), middleware.TenantConsistency(), middleware.OrgResolve(),
		middleware.MustChangePasswordGuard(), middleware.ReadonlyWriteGuard())
	// 注册期鉴权记录：顾问端需登录 + 租户一致性 + 组织上下文 + 首登强改密 + 只读写闸（整组共享）。
	RecordAuth("*", "/api/v1/advisor", "jwt", "readonly_write")
	{
		advisorGroup.GET("/list", GetAdvisorList)
		advisorGroup.GET("/tags", GetTagList)
		advisorGroup.GET("/stats", GetAdvisorStats)
		advisorGroup.GET("/customers", GetAdvisorCustomers)
		advisorGroup.GET("/customer/:id", GetAdvisorCustomerDetail)
		advisorGroup.PUT("/customer/:id/tags", EditCustomerTags)
		advisorGroup.PUT("/customer/:id/info", EditCustomerInfo)
		advisorGroup.PUT("/customer/:id/stage", UpdateCustomerStage)
		// 商机与报价（商机批 批次2）：客户台面上看这个人的单、开单、推进、出报价。
		// 读侧一律经 deal.Scope.CustomerScope（DataScope 落在 customers 上）裁剪——
		// opportunities 没有归属列，单子跟着客户走（理由见 internal/model/deal.go 文件头）。
		advisorGroup.GET("/customer/:id/deals", AdvisorCustomerDeals)
		advisorGroup.POST("/customer/:id/deals", AdvisorCreateDeal)
		advisorGroup.POST("/deals/:id/move", AdvisorMoveDeal)
		advisorGroup.POST("/deals/:id/quotes", AdvisorCreateQuote)
		advisorGroup.POST("/customer/:id/followup", CreateFollowup)
		advisorGroup.GET("/followups", GetFollowups)
		advisorGroup.POST("/chat/takeover", AdvisorTakeover)
		advisorGroup.POST("/chat/send", AdvisorSendMessage)
		advisorGroup.POST("/chat/ai-reply", AdvisorTriggerAIReply)
		advisorGroup.POST("/chat/toggle-ai-reply", ToggleAiReply)
		advisorGroup.GET("/strategy/recommend", GetStrategyRecommend)
		advisorGroup.POST("/test-drive", CreateTestDrive)
		advisorGroup.GET("/test-drives", GetTestDrives)
		advisorGroup.GET("/test-drive/:id", GetTestDrive)
		advisorGroup.PUT("/test-drive/:id", UpdateTestDrive)
		// 邀请推广只读（P1-42）：个人推广资产，移动端非管理员角色也必须能看邀请/二维码
		advisorGroup.GET("/referral/info", GetReferralInfo)
		advisorGroup.GET("/referral/records", GetReferralRecords)
		advisorGroup.GET("/referral/qrcode", GetReferralQRCode)
	}
}
