// 登录态通用路由（D2a，2026-09-12）
package api

import (
	"github.com/gin-gonic/gin"

	"ai-scrm/internal/middleware"
)

// registerTenantAuthenticated 注册在 v1.Use(JWTAuth+TenantConsistency+OrgResolve+MustChangePasswordGuard)
// 之后：所有 B 端登录用户可达（当前用户、改密、反馈、收银台、客户、对话、会话、策略、流程、统计）。
// 由调用方 RegisterRoutes 在挂载鉴权中间件后调用。
func registerTenantAuthenticated(v1 *gin.RouterGroup) {
	// 当前用户信息 / 改密（M3 安全三件套）
	v1.GET("/auth/me", GetCurrentUser)
	v1.POST("/auth/change-password", ChangePassword)

	// 用户反馈（M2）：登录用户均可提交；满意度评分采集（CDP att_satisfaction 标签驱动）
	v1.POST("/feedback", CreateFeedback)
	v1.POST("/feedback/rating", CreateFeedbackRating)

	// 收银台（商业化 M1/M2）：查询/订阅入口全员可看；下单/支付/确认需管理员权限
	billing := v1.Group("/billing")
	{
		billing.GET("/my-package", MyPackage)
		billingAdmin := billing.Group("")
		billingAdmin.Use(middleware.AdminRequired())
		{
			billingAdmin.GET("/orders", ListBillingOrders)
			billingAdmin.POST("/orders", CreateBillingOrder)
			billingAdmin.GET("/orders/:id", GetBillingOrder)
			billingAdmin.POST("/orders/mock-pay", MockPayOrder)
			billingAdmin.POST("/manual-confirm", ManualConfirmPaid)
			billingAdmin.POST("/subscribe", SubscribePackage)
			billingAdmin.POST("/orders/:id/refund", RefundOrder)
			billingAdmin.POST("/orders/:id/invoice", RequestInvoice)
		}
	}

	// 客户管理
	customers := v1.Group("/customers")
	{
		customers.GET("", GetCustomerList)
		customers.POST("", CreateCustomer)
		customers.GET("/:id", GetCustomer)
		customers.PUT("/:id", UpdateCustomer)
		customers.DELETE("/:id", DeleteCustomer)
		customers.GET("/:id/conversations", GetCustomerConversations)
		customers.GET("/:id/tags", GetCustomerTags)
		customers.POST("/:id/tags", AddTagsToCustomer)
		customers.DELETE("/:id/tags/:tag_id", RemoveCustomerTag)
	}

	// 对话相关（登录态入口，挂在 v1.Use(JWTAuth) 之后——匿名访客会被 401）
	// C1 双链路（PLAN_FIX_2026-09-21）：C 端访客实际走公开组的 POST /api/v1/chat/test
	// （chat_testhandler.go，含 visitor_key 自证），本组 /chat 当前**前端零消费者**，
	// 本组 /chat 当前**前端零消费者**，但 smoke 第二十七节已用登录态覆盖（断言
	// merged 抑制与落库），不存在"零测试"风险；真正待收敛的是"两条链路能力重叠
	// 而鉴权语义不同"这个架构债，删除任一条前需先让另一条覆盖对等的访客/登录态场景。
	chat := v1.Group("/chat")
	{
		chat.POST("", Chat)
		chat.POST("/human/reply", HumanReply)
		chat.POST("/transfer/human", TransferToHuman)
		chat.POST("/transfer/ai", TransferToAI)
	}

	conversations := v1.Group("/conversations")
	{
		conversations.GET("", GetConversationList)
		conversations.GET("/:id/messages", GetMessages)
	}

	// 策略中心管理
	strategyGroup := v1.Group("/strategy")
	{
		strategyGroup.POST("/test", StrategyTest)
		strategyGroup.GET("/templates", GetTemplateList)
		strategyGroup.GET("/templates/:id", GetTemplate)
		strategyGroup.GET("/features", GetFeatureList)
		strategyGroup.GET("/stats/anchors", GetAnchorStats)
		// P1-2 修复(2026-09-19 审计批一)：模板写三路由此前对全部登录角色（sales/readonly）
		// 开放且保存即 ReloadData 热生效——低权限可改写全租户 AI 话术。
		// 收进 AdminRequired 子组（对照同函数 billingAdmin 范式）；GET 读面全员保持（顾问端要读模板）。
		strategyAdmin := strategyGroup.Group("")
		strategyAdmin.Use(middleware.AdminRequired())
		{
			strategyAdmin.POST("/templates", CreateTemplate)
			strategyAdmin.PUT("/templates/:id", UpdateTemplate)
			strategyAdmin.DELETE("/templates/:id", DeleteTemplate)
		}
	}

	// 流程引擎
	flowGroup := v1.Group("/flows")
	{
		flowGroup.GET("", GetFlowList)
		flowGroup.GET("/:id", GetFlow)
		flowGroup.POST("/start", StartFlow)
		flowGroup.POST("/advance", AdvanceFlow)
		flowGroup.GET("/instances", GetFlowInstanceList)
		flowGroup.GET("/instances/:id", GetFlowInstance)
	}

	// 统计
	stats := v1.Group("/stats")
	{
		stats.GET("/overview", GetOverview)
	}
}
