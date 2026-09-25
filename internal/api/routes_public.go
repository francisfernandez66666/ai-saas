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
		// S2（2026-09-23 批二）：前端登录页文案改由"实际生效通道"驱动，不再硬编码"查看服务端日志"。
		// 只回通道种类（smtp|log），无敏感值，故免限流（与 register-config 同型静态读）。
		auth.GET("/reset-channel", GetResetChannel)
		// 注册期鉴权记录：登录/注册/验证码均为免登录，仅人机验证或 IP 限流。
		RecordAuth("POST", "/api/v1/auth/login", "ip_limit")
		RecordAuth("POST", "/api/v1/auth/register", "visitor_key", "ip_limit")
		RecordAuth("GET", "/api/v1/auth/register-config", "public")
		RecordAuth("POST", "/api/v1/auth/email-code", "visitor_key", "ip_limit")
		RecordAuth("POST", "/api/v1/auth/reset-password", "ip_limit")
		RecordAuth("POST", "/api/v1/auth/verify-reset-code", "ip_limit")
		RecordAuth("GET", "/api/v1/auth/reset-channel", "public")
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

	// 注册期鉴权记录（真实中间件链）：登录态换绑需 JWT；入驻/定价/品牌为免登录公开或仅限流。
	RecordAuth("POST", "/api/v1/auth/email/code", "jwt")
	RecordAuth("POST", "/api/v1/auth/email/change", "jwt")
	RecordAuth("POST", "/api/v1/tenant/signup", "ip_limit")
	RecordAuth("GET", "/api/v1/tenant/check-code", "ip_limit")
	RecordAuth("GET", "/api/v1/plans", "public")
	RecordAuth("GET", "/api/v1/packages", "public")
	RecordAuth("GET", "/api/v1/public/branding", "public")
}

// registerChatPublic C 端匿名对话入口：测试聊天、访客注册、欢迎、历史、延迟清零、支付回调。
// 这些都必须注册在 v1.Use(JWTAuth...) 之前（匿名/服务端回调不带 JWT）。
func registerChatPublic(v1 *gin.RouterGroup) {
	// AI对话测试（免登录）：Turnstile 防薅 + IP 限流（每条真调 AI+扣额度+写多表，是匿名刷量最大入口）
	// C1(PLAN_FIX_2026-09-21)：原名 /chat/test 易被误读为"测试桩"，实为**未授权未留资访客的
	// 正式聊天入口**（Client.tsx 在用，能力完整：会话竞态/四层分流/延迟清零/留资/OneID 合并）。
	// 改名 /chat/unauthorized 让语义自解释（免登录 + visitor_key 自证 + Turnstile + IP 限流）。
	v1.POST("/chat/unauthorized", middleware.TurnstileGuard(), middleware.IPRateLimit("chat_unauthorized", 20, time.Minute), ChatUnauthorized)
	// 兼容别名（deprecated）：旧客户端/外部集成仍在打 /chat/test，保留一版并在日志留痕，
	// 与限流桶分开计数避免互相挤占；下个版本移除。
	v1.POST("/chat/test", middleware.TurnstileGuard(), middleware.IPRateLimit("chat_test", 20, time.Minute), ChatUnauthorized)
	// 访客注册（免登录，每次打开 client 页面创建新访客）
	v1.POST("/chat/guest", middleware.TurnstileGuard(), middleware.IPRateLimit("chat_guest", 10, time.Minute), CreateGuest)
	// PIPL 删除权受理（C2，免登录 C 端 + OptionalJWTAuth 让登录态可撤回账号）：visitor_key 自证或登录放行
	v1.POST("/privacy/deletion-request", middleware.OptionalJWTAuth(), middleware.IPRateLimit("privacy_del", 5, time.Minute), PrivacyDeletionRequest)
	// C6 前端异常上报：登录态可带租户/user_id，匿名按 Host/企业码落当前租户；只入库，不发群避免刷爆。
	v1.POST("/client-errors", middleware.OptionalJWTAuth(), middleware.IPRateLimit("client_error", 20, time.Minute), ClientErrorReport)
	// Turnstile 站点键下发（免登录公开；enabled=false 时前端不渲染组件）
	// G-13：改走 RespOK 统一出口——裸写 {"code":0} 的 handler 是错误码链路上的盲区
	// （error_code/脱敏/日志三点都不经过这里），响应体只多一个 message 键，前端只读 data。
	v1.GET("/turnstile/sitekey", func(c *gin.Context) {
		enabled, siteKey := middleware.GetTurnstileSiteKey()
		RespOK(c, "ok", gin.H{"enabled": enabled, "site_key": siteKey})
	})
	// 会话欢迎接口（免登录，独立秒回，无AI处理）：IP 限流堵 DB 写放大
	v1.POST("/chat/welcome", middleware.IPRateLimit("chat_welcome", 30, time.Minute), Welcome)
	// 聊天历史（OptionalJWTAuth：有 Bearer 注入身份放行 B 端，匿名走 visitor_key 校验 C 端不变）
	// P1-3 修复(2026-09-20 审计批)：补 IP 限流 30/min——此前公开面无频控，
	// customer_id 枚举探测与 DB 读放大零成本（正常前端轮询远低于该阈值）。
	v1.GET("/chat/history", middleware.OptionalJWTAuth(), middleware.IPRateLimit("chat_history", 30, time.Minute), GetChatHistory)
	// 延迟清零（顾问/管理员"立即回复"）：IP 限流 + handler 内双重归属校验。
	// 浏览器实测批(2026-09-18)：补 OptionalJWTAuth——本路由在 v1.Use(JWTAuth) 之前注册，
	// 登录态 Bearer 此前从不解析 → CheckVisitorKey 拿不到 user_id，顾问对真实访客客户
	// （VisitorKey 非空）点"立即回复"必 403（§八-6 UI 实测捕获）。匿名 C 端 visitor_key 路径不变。
	v1.POST("/chat/clear-delay", middleware.OptionalJWTAuth(), middleware.IPRateLimit("chat_clear_delay", 30, time.Minute), ClearDelay)
	// E3：C 端"找人工"（访客身份自证，限流防刷）
	v1.POST("/chat/request-human", middleware.IPRateLimit("chat_req_human", 20, time.Minute), GuestRequestHuman)
	// 支付网关异步回调（免登录，服务端到服务端）：必须注册在 v1.Use(JWTAuth) 之前，
	// 回调不携带用户 JWT，安全性靠 HMAC 验签（挂鉴权组内会被 401 拦截导致永远无法到账）。
	// P1-3 修复(2026-09-20 审计批)：补 60/min IP 限流——验签保完整性但不保可用性，
	// 无限流时伪造签名重放可无限烧 nonce 去重表/DB 查询；正常 PSP 重试远低于该阈值。
	v1.POST("/billing/webhook/:channel", middleware.IPRateLimit("psp_webhook", 60, time.Minute), BillingWebhook)
	// 注册期鉴权记录（真实中间件链）：C 端匿名入口无 JWT，仅人机验证/可选 JWT/验签 + IP 限流。
	RecordAuth("POST", "/api/v1/chat/unauthorized", "visitor_key", "ip_limit")
	RecordAuth("POST", "/api/v1/chat/test", "visitor_key", "ip_limit")
	RecordAuth("POST", "/api/v1/chat/guest", "visitor_key", "ip_limit")
	RecordAuth("POST", "/api/v1/privacy/deletion-request", "optional_jwt", "ip_limit")
	RecordAuth("POST", "/api/v1/client-errors", "optional_jwt", "ip_limit")
	RecordAuth("GET", "/api/v1/turnstile/sitekey", "public")
	RecordAuth("POST", "/api/v1/chat/welcome", "ip_limit")
	RecordAuth("GET", "/api/v1/chat/history", "optional_jwt", "ip_limit")
	RecordAuth("POST", "/api/v1/chat/clear-delay", "optional_jwt", "ip_limit")
	RecordAuth("POST", "/api/v1/chat/request-human", "ip_limit")
	RecordAuth("POST", "/api/v1/billing/webhook/:channel", "webhook_signature", "ip_limit")
}

// registerAcquisitionPublic 获客活码公开链路（批次2，2026-09-23）：落地页读码 + 扫码计数。
//
// 这一组刻意挂在 TenantResolver 的 skip 前缀下（middleware.skipTenantPrefixes）：
// 活码是印在物料上的固定链接，租户身份从**码自己**解析，而不是从来路 Host 猜。
// 因此这两个端点没有租户语境，一律用平台句柄查全局唯一键，且只回不泄露身份的最小字段。
func registerAcquisitionPublic(v1 *gin.RouterGroup) {
	// 读码：猜一个 8 位码的正确率是 31^8 分之一，但真去猜的成本必须比收益高——60/min 足够
	// 正常用户重开页面，不够脚本扫库。
	v1.GET("/acquisition/:code", middleware.IPRateLimit("acq_resolve", 60, time.Minute), ResolveAcquisitionCode)
	// 扫码计数：写一行事件表，限流比读更紧（30/min），它没有 AI 调用也没有客户可见后果，
	// 但重复刷量能虚增"渠道效果"数字，进而影响预算分配——数字被污染比被刷更贵。
	v1.POST("/acquisition/:code/scan", middleware.IPRateLimit("acq_scan", 30, time.Minute), RecordAcquisitionScan)
	// 注册期鉴权记录：免登录公开 + IP 限流（无 JWT、无租户上下文）
	RecordAuth("GET", "/api/v1/acquisition/:code", "ip_limit")
	RecordAuth("POST", "/api/v1/acquisition/:code/scan", "ip_limit")
}

// registerKnowledgePublic 客户端知识库公开查询接口（免鉴权）。
// P1-3 修复(2026-09-20 审计批)：整组挂 60/min IP 限流——匿名检索面此前无频控，
// 关键词枚举可拖库式试探品牌/车型/片段数据。
func registerKnowledgePublic(v1 *gin.RouterGroup) {
	knowledge := v1.Group("/knowledge", middleware.IPRateLimit("knowledge_public", 60, time.Minute))
	// 注册期鉴权记录：公开知识库检索面仅 IP 限流（整组共享）。
	RecordAuth("*", "/api/v1/knowledge", "ip_limit")
	{
		knowledge.GET("/brands", GetPublicBrands)
		knowledge.GET("/models", GetPublicModels)
		knowledge.GET("/models/:id", GetPublicModelDetail)
		knowledge.GET("/compares", GetPublicCompares)
		knowledge.GET("/fragments/search", SearchFragments)
	}
}
