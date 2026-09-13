// Package api 路由注册总入口（D2a，2026-09-12）
// 由 cmd/server/main.go 的 registerRoutes 拆分而来：main 只保留 gin 引擎装配、全局中间件、
// 基础设施端点（/health /status /metrics）与 SPA 托管，业务路由树全部下沉到本包按作用域分文件：
//
//	routes_public.go   免登录/匿名（auth、tenant、plans、chat 公开入口、knowledge 公开）
//	routes_openapi.go  /openapi/v1（sk_ Key 鉴权链）
//	routes_advisor.go  顾问工作台
//	routes_super.go    平台超管
//	routes_org.go      组织架构 + CDP
//	routes_admin.go    后台管理（admin 角色）
//	routes_tenant.go   登录态通用（auth/me、billing、customers、chat、strategy、flows、stats）
package api

import (
	"time"

	"github.com/gin-gonic/gin"

	"ai-scrm/internal/middleware"
)

// RegisterRoutes 注册全部业务路由（在 main 完成全局中间件装配后调用）。
// 注意：内部各 register* 的调用顺序不可随意调整——v1 组的 JWT 等鉴权中间件
// 是在"已注册 admin 组之后"才 Use 的，gin 按注册时刻捕获中间件，顺序变动会改变路由鉴权语义。
func RegisterRoutes(r *gin.Engine) {
	// ---- API v1 路由组 ----
	v1 := r.Group("/api/v1")

	registerWSAndCollector(r, v1)
	registerAuthPublic(v1)
	registerOpenAPI(r)
	registerChatPublic(v1)
	registerAdvisor(v1)
	registerSuper(v1)
	registerOrg(v1)
	registerCDP(v1)
	registerKnowledgePublic(v1)
	registerAdmin(v1)
	registerChannelCallbacks(v1) // 渠道回调（公开，须在下方 v1.Use(JWTAuth) 之前注册，靠签名验证）

	// 以下注册在鉴权链之后：登录 + 租户一致性 + 组织上下文 + 首登强改密
	v1.Use(middleware.JWTAuth(), middleware.TenantConsistency(), middleware.OrgResolve(), middleware.MustChangePasswordGuard())
	registerTenantAuthenticated(v1)
	registerChannelAuthed(v1) // 通道侧边栏 JS 配置/上下文（W7，鉴态）
}

// registerWSAndCollector WebSocket 实时推送 + 数据飞轮接收端。
// WS 不挂 JWTAuth 组（难以附带 Authorization header），改 query 参数手动校验；各挂 IPRateLimit 防握手风暴。
func registerWSAndCollector(r *gin.Engine, v1 *gin.RouterGroup) {
	v1.GET("/ws/advisor", middleware.IPRateLimit("ws_advisor", 10, time.Minute), WSAdvisor)
	v1.GET("/ws/client", middleware.IPRateLimit("ws_client", 20, time.Minute), WSClient)
	// 数据飞轮聚合接收端（P2 collector，自有 X-Collector-Key 鉴权，独立于 JWT）
	r.POST("/api/v1/collector", CollectorReceive)
}
