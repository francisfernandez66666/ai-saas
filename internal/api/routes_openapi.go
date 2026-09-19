// OpenAPI 开放接口路由（D2a，2026-09-12）
package api

import (
	"time"

	"github.com/gin-gonic/gin"

	"ai-scrm/internal/middleware"
)

// registerOpenAPI /openapi/v1 独立鉴权链：不走 JWTAuth/TenantResolver，租户来自 sk_ Key 归属。
// P2-11：sk_ 维度限流 60/min/sk（与站内共用 ipLimitMap，bucket=apikey）。
func registerOpenAPI(r *gin.Engine) {
	openapi := r.Group("/openapi/v1")
	openapi.Use(middleware.OpenAPIAuth())
	openapi.Use(middleware.IPRateLimit("apikey", 60, time.Minute))
	{
		openapi.GET("/customers", middleware.RequirePerm(middleware.PermCustomerRead), OpenAPICustomers)
		openapi.GET("/customers/:id/conversations", middleware.RequirePerm(middleware.PermCustomerRead), OpenAPICustomerConversations)
		openapi.GET("/cdp/profiles/:one_id", middleware.RequirePerm(middleware.PermCDPRead), OpenAPICDPProfile)
		openapi.GET("/usage", middleware.RequirePerm(middleware.PermAll), OpenAPIUsage)
		// 渠道嵌入对话端点（与站内同池同链路，复用 OrchestrateReply）
		openapi.POST("/chat/completions", middleware.RequirePerm(middleware.PermChatWrite), OpenAPIChatCompletions)
	}
}

// registerOpenAPIDoc E10（2026-09-19）：租户开放面规格端点，公开挂 /api/v1（注册须在 v1.Use(JWTAuth) 之前）。
// 内容仅 /openapi/v1 子集静态规格，无任何租户数据，公开无泄露面。
func registerOpenAPIDoc(v1 *gin.RouterGroup) {
	v1.GET("/openapi/spec", OpenAPIGetSpec)
}
