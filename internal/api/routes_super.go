// 平台超管路由（D2a，2026-09-12）
package api

import (
	"github.com/gin-gonic/gin"

	"ai-scrm/internal/middleware"
)

// registerSuper 平台超管后台（仅 super_admin）：租户治理、商业化订单/包、审计、成本、反馈、行业包、素材、协议、白标、监控。
func registerSuper(v1 *gin.RouterGroup) {
	super := v1.Group("/super")
	super.Use(middleware.JWTAuth(), middleware.TenantConsistency(), middleware.OrgResolve(),
		middleware.MustChangePasswordGuard(), SuperRequired())
	// 注册期鉴权记录：平台超管需登录 + 租户一致性 + 组织上下文 + 首登强改密 + 超管闸（整组共享）。
	RecordAuth("*", "/api/v1/super", "jwt", "super_required")
	{
		super.GET("/tenants", SuperTenantList)
		super.PUT("/tenants/:id/status", SuperTenantStatus)
		super.POST("/tenants/:id/grant-trial", SuperGrantTrial)
		// 商业缺口批(2026-09-16)：超管一键换套餐（席位/客户/部门配额快照同步），此前只能人肉改库
		super.GET("/plans", SuperPlans)
		super.PUT("/tenants/:id/plan", SuperUpdateTenantPlan)
		// 商业化 M1/M2/M5
		super.GET("/orders/pending", SuperPendingOrders)
		super.POST("/orders/:id/confirm", SuperConfirmOrder)
		super.POST("/billing/orders/:id/refund", SuperRefundOrder)         // B7 双轨退款：平台审批落点
		super.POST("/billing/orders/:id/refund/reject", SuperRejectRefund) // 驳回退款申请（仅清标记，2026-09-19 退款受理批）
		super.GET("/billing/refund-requests", SuperListRefundRequests)     // 超管退款申请工作队列（2026-09-19 退款受理批）
		super.POST("/billing/orders/:id/mock-webhook", SuperMockWebhook)   // §W 测试资产：模拟网关到账回调（非 release + mock 渠道双闸门）
		// §W 发票极限闭环：申请列表 + 人工开具回录 + 作废（资质到位前不接税控）
		// 冒烟护栏批(2026-09-18)修复：参数名原为 :order_id，但 handler 走 PathUintID（只读
		// c.Param("id")）→ 两路由自合入起恒 400「ID 非法」，smoke 第二十节首跑捕获；统一 :id 口径。
		super.GET("/invoices", SuperListInvoices)
		super.POST("/invoices/:id/issue", SuperIssueInvoice)
		super.POST("/invoices/:id/void", SuperVoidInvoice)
		super.GET("/packages", SuperPackageList)
		super.POST("/packages", SuperPackageCreate)
		super.PUT("/packages/:id", SuperPackageUpdate)
		super.DELETE("/packages/:id", SuperPackageDelete)
		super.GET("/audit-logs", SuperAuditLogs)
		super.GET("/usage/cost", SuperUsageCost)
		super.GET("/feedbacks", SuperFeedbackList)
		super.POST("/feedbacks/resolve", SuperResolveFeedback)
		// 行业包平台侧：上传验签/列表/启停
		super.POST("/packs", SuperPackUpload)
		super.GET("/packs", SuperPackList)
		super.GET("/materials", SuperMaterialList)
		super.POST("/materials/:id/review", SuperMaterialReview)
		super.POST("/materials/:id/evals", SuperMaterialEvals)
		super.GET("/agreements", SuperAgreementList)
		super.PUT("/tenants/:id/branding", SuperUpdateBranding)
		super.GET("/tenants/:id/branding", SuperGetBranding)
		super.GET("/packs/stats", SuperPackStats)
		super.PUT("/packs/:id/status", SuperPackStatus)
		super.PUT("/packs/:id/share", SuperPackShare)
		// 监控告警（P1-4）
		super.GET("/monitor/health", SuperMonitorHealth)
		// D3 催缴工作队列（2026-09-23）：欠费户逐档催、宽限期满自动停用，这里是运营的唯一落点。
		// nudge/reset 都不动钱也不解封（解封只认可到账），审计行在 billing 领域层写。
		super.GET("/dunning", SuperDunningQueue)
		super.POST("/dunning/:id/nudge", SuperDunningNudge)
		super.POST("/dunning/:id/reset", SuperDunningReset)
	}
}
