// 免登录/匿名路由（D2a，2026-09-12）
package api

import (
	"time"

	"github.com/gin-gonic/gin"

	"ai-scrm/internal/middleware"
)

// registerAuthPublic 登录/注册/验证码 + 租户入驻 + 定价/商业包 + 白标（均免登录，部分挂 Turnstile/IP 限流）。
func registerAuthPublic(v1 *gin.RouterGroup) {
	auth := v1.Group("/auth")
	{
		// B5 修复(2026-09-14)：登录补 IP 聚合限流——旧实现仅按"用户名"和"IP+用户名"锁定，
		// 攻击者用 N 个用户名各撞 5 次即可绕过（且每错都吃 ~100ms bcrypt CPU 放大）。
		// 纯 IP 维度 20 次/分钟先兜住量，再叠加既有账号锁定。
		auth.POST("/login", middleware.IPRateLimit("login", 20, time.Minute), Login)
		auth.POST("/register", middleware.TurnstileGuard(), middleware.IPRateLimit("register", 10, 10*time.Minute), Register)
		auth.GET("/register-config", RegisterConfig)
		auth.POST("/email-code", middleware.TurnstileGuard(), middleware.IPRateLimit("reset_email_code", 5, 10*time.Minute), SendRegisterEmailCode)
		auth.POST("/reset-password", middleware.IPRateLimit("reset_pwd", 5, 10*time.Minute), SendResetCode)
		auth.POST("/verify-reset-code", middleware.IPRateLimit("verify_reset", 10, 10*time.Minute), VerifyResetCode)
	}

	// 邮箱换绑（登录态）：向新邮箱发码 → 校验完成绑定
	// P0-6 复核批(2026-09-15)：本组路由注册在 v1.Use(MustChangePasswordGuard) 之前
	// （gin 按注册时点快照中间件，后挂不生效），旧实现只过 JWTAuth 不核 token_version——
	// 凭证套装被盗后受害者改密，攻击者旧 token 仍可换绑自己邮箱再走 reset 完成账号接管，
	// B4"改密即驱逐"在此被反转。补 TokenRevocationCheck 硬核对。
	v1.POST("/auth/email/code", middleware.JWTAuth(), middleware.TokenRevocationCheck(), SendBindEmailCode)
	v1.POST("/auth/email/change", middleware.JWTAuth(), middleware.OrgResolve(), middleware.TokenRevocationCheck(), ChangeEmail)

	// 租户入驻与套餐（免登录公开）
	v1.POST("/tenant/signup", middleware.IPRateLimit("tenant_signup", 15, 10*time.Minute), TenantSignup)
	v1.GET("/tenant/check-code", middleware.IPRateLimit("tenant_check_code", 30, 10*time.Minute), CheckTenantCode)
	v1.GET("/plans", ListPlans)
	v1.GET("/packages", ListPackages)

	// 公开品牌配置（按 Host 解析租户白标，免登录）
	v1.GET("/public/branding", GetPublicBranding)
}

// registerChatPublic C 端匿名对话入口：测试聊天、访客注册、欢迎、历史、延迟清零、支付回调。
// 这些都必须注册在 v1.Use(JWTAuth...) 之前（匿名/服务端回调不带 JWT）。
func registerChatPublic(v1 *gin.RouterGroup) {
	// AI对话测试（免登录）：Turnstile 防薅 + IP 限流（每条真调 AI+扣额度+写多表，是匿名刷量最大入口）
	v1.POST("/chat/test", middleware.TurnstileGuard(), middleware.IPRateLimit("chat_test", 20, time.Minute), ChatTest)
	// 访客注册（免登录，每次打开 client 页面创建新访客）
	v1.POST("/chat/guest", middleware.TurnstileGuard(), middleware.IPRateLimit("chat_guest", 10, time.Minute), CreateGuest)
	// PIPL 删除权受理（C2，免登录 C 端 + OptionalJWTAuth 让登录态可撤回账号）：visitor_key 自证或登录放行
	v1.POST("/privacy/deletion-request", middleware.OptionalJWTAuth(), middleware.IPRateLimit("privacy_del", 5, time.Minute), PrivacyDeletionRequest)
	// C6 前端异常上报：登录态可带租户/user_id，匿名按 Host/企业码落当前租户；只入库，不发群避免刷爆。
	v1.POST("/client-errors", middleware.OptionalJWTAuth(), middleware.IPRateLimit("client_error", 20, time.Minute), ClientErrorReport)
	// Turnstile 站点键下发（免登录公开；enabled=false 时前端不渲染组件）
	v1.GET("/turnstile/sitekey", func(c *gin.Context) {
		enabled, siteKey := middleware.GetTurnstileSiteKey()
		c.JSON(200, gin.H{"code": 0, "data": gin.H{"enabled": enabled, "site_key": siteKey}})
	})
	// 会话欢迎接口（免登录，独立秒回，无AI处理）：IP 限流堵 DB 写放大
	v1.POST("/chat/welcome", middleware.IPRateLimit("chat_welcome", 30, time.Minute), Welcome)
	// 聊天历史（OptionalJWTAuth：有 Bearer 注入身份放行 B 端，匿名走 visitor_key 校验 C 端不变）
	v1.GET("/chat/history", middleware.OptionalJWTAuth(), GetChatHistory)
	// 延迟清零（顾问/管理员"立即回复"）：IP 限流 + handler 内双重归属校验。
	// 浏览器实测批(2026-09-18)：补 OptionalJWTAuth——本路由在 v1.Use(JWTAuth) 之前注册，
	// 登录态 Bearer 此前从不解析 → CheckVisitorKey 拿不到 user_id，顾问对真实访客客户
	// （VisitorKey 非空）点"立即回复"必 403（§八-6 UI 实测捕获）。匿名 C 端 visitor_key 路径不变。
	v1.POST("/chat/clear-delay", middleware.OptionalJWTAuth(), middleware.IPRateLimit("chat_clear_delay", 30, time.Minute), ClearDelay)
	// E3：C 端"找人工"（访客身份自证，限流防刷）
	v1.POST("/chat/request-human", middleware.IPRateLimit("chat_req_human", 20, time.Minute), GuestRequestHuman)
	// 支付网关异步回调（免登录，服务端到服务端）：必须注册在 v1.Use(JWTAuth) 之前，
	// 回调不携带用户 JWT，安全性靠 HMAC 验签（挂鉴权组内会被 401 拦截导致永远无法到账）。
	v1.POST("/billing/webhook/:channel", BillingWebhook)
}

// registerKnowledgePublic 客户端知识库公开查询接口（免鉴权）。
func registerKnowledgePublic(v1 *gin.RouterGroup) {
	knowledge := v1.Group("/knowledge")
	{
		knowledge.GET("/brands", GetPublicBrands)
		knowledge.GET("/models", GetPublicModels)
		knowledge.GET("/models/:id", GetPublicModelDetail)
		knowledge.GET("/compares", GetPublicCompares)
		knowledge.GET("/fragments/search", SearchFragments)
	}
}
