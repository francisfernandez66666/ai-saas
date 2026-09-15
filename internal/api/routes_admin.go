// 后台管理路由（D2a，2026-09-12）
package api

import (
	"github.com/gin-gonic/gin"

	"ai-scrm/internal/middleware"
)

// registerAdmin 管理端（admin 角色）：审计、用量、行业包绑定、知识库 CRUD、标签体系、系统配置、API Key。
// 鉴权链顺序不可调换：OrgResolve(DB 角色权威) 必须在 AdminRequired 之前，否则降权在重新登录前不生效(H1)。
func registerAdmin(v1 *gin.RouterGroup) {
	admin := v1.Group("/admin")
	admin.Use(middleware.JWTAuth(), middleware.TenantConsistency(), middleware.OrgResolve(),
		middleware.MustChangePasswordGuard(), middleware.AdminRequired())
	{
		// 审计日志 / 用量看板（M5/M3，本租户）
		admin.GET("/audit-logs", AdminAuditLogs)
		admin.GET("/usage/summary", AdminUsageSummary)

		// 数据导出（D7，企业交付项：客户/会话 CSV 流式下载，PII 掩码+数据范围双闸）
		admin.GET("/export/customers.csv", AdminExportCustomers)
		admin.GET("/export/conversations.csv", AdminExportConversations)

		// 隐私合规 / PIPL 删除请求（C2：列表 + 手动立即执行）
		admin.GET("/privacy/deletion-requests", AdminListDeletionRequests)
		admin.POST("/privacy/deletion-requests/:id/execute", AdminExecuteDeletionRequest)

		// 出站事件 webhook（D6：订阅 CRUD + 测试 ping + 投递记录；集成设置页 F 批对接）
		admin.GET("/webhooks", ListWebhooks)
		admin.POST("/webhooks", CreateWebhook)
		admin.PUT("/webhooks/:id", UpdateWebhook)
		admin.DELETE("/webhooks/:id", DeleteWebhook)
		admin.POST("/webhooks/:id/test", TestWebhook)
		admin.GET("/webhooks/:id/deliveries", ListWebhookDeliveries)

		// 行业包租户侧：分层列表 / 两级绑定 / 部门绑定
		admin.GET("/packs", TenantPackList)
		admin.POST("/packs/bind", TenantPackBind)
		admin.POST("/packs/unbind", TenantPackUnbind)
		admin.GET("/packs/current", TenantPackCurrent)
		admin.POST("/packs/bind-dept", TenantPackBindDept)
		admin.POST("/packs/unbind-dept", TenantPackUnbindDept)
		admin.GET("/packs/stats", AdminPackStats)

		// 租户自定义知识库（P2 双层 KB）
		admin.POST("/kb/upload", TenantKBUpload)
		admin.GET("/kb/my", TenantKBMy)
		admin.DELETE("/kb/my/:id", TenantKBDelete)

		// 账号注销（P4：次日生效禁登录 + 同步禁用 API Key）
		admin.POST("/account/cancel", CancelAccount)

		// API Key 自助管理（M4 开放平台）
		apikeys := admin.Group("/apikeys")
		{
			apikeys.POST("", AdminCreateAPIKey)
			apikeys.GET("", AdminListAPIKeys)
			apikeys.POST("/:id/disable", AdminDisableAPIKey)
			apikeys.POST("/:id/enable", AdminEnableAPIKey)
			apikeys.DELETE("/:id", AdminDeleteAPIKey)
		}

		// 系统配置管理（reset/init 平台级操作叠加 SuperRequired，防租户 admin 误清全局配置）
		admin.GET("/config", GetSystemConfigs)
		admin.PUT("/config", BatchUpdateSystemConfig)
		admin.PUT("/tenant/branding", AdminUpdateBranding)
		admin.GET("/tenant/branding", AdminGetBranding) // A2：租户侧带登录态读取本租户白标（与 PUT 对称）
		admin.POST("/config/reset", SuperRequired(), ResetSystemConfig)
		admin.POST("/config/rollback", RollbackTenantConfig)
		admin.POST("/config/init", SuperRequired(), ForceInitSystemConfig)
		admin.GET("/models", GetAvailableModels)

		// 标签管理
		adminTags := admin.Group("/tags")
		{
			adminTags.GET("", GetTagList)
			adminTags.GET("/:id", GetTagDetail)
			adminTags.POST("", CreateTag)
			adminTags.PUT("/:id", UpdateTag)
			adminTags.POST("/:id/enable", EnableTag)
			adminTags.POST("/:id/disable", DisableTag)
			adminTags.DELETE("/:id", DeleteTag)
			adminTags.POST("/reload", ReloadTagCache)
		}

		// 打标规则管理
		adminTagRules := admin.Group("/tag-rules")
		{
			adminTagRules.GET("", GetTagRuleList)
			adminTagRules.POST("", CreateTagRule)
			adminTagRules.PUT("/:id", UpdateTagRule)
			adminTagRules.POST("/:id/enable", EnableTagRule)
			adminTagRules.POST("/:id/disable", DisableTagRule)
			adminTagRules.DELETE("/:id", DeleteTagRule)
		}

		// 标签权重映射管理
		adminTagWeights := admin.Group("/tag-weights")
		{
			adminTagWeights.GET("", GetTagWeightList)
			adminTagWeights.POST("", CreateTagWeight)
			adminTagWeights.PUT("/:id", UpdateTagWeight)
			adminTagWeights.DELETE("/:id", DeleteTagWeight)
			// P1-12 修复(2026-09-15)：补齐前端 EntityCrud 通用启停用路由（此前恒 404）
			adminTagWeights.POST("/:id/enable", EnableTagWeight)
			adminTagWeights.POST("/:id/disable", DisableTagWeight)
		}

		// 知识库管理
		adminKnowledge := admin.Group("/knowledge")
		{
			adminKnowledge.GET("/brands", GetBrandList)
			adminKnowledge.GET("/brands/:id", GetBrandDetail)
			adminKnowledge.POST("/brands", CreateBrand)
			adminKnowledge.PUT("/brands/:id", UpdateBrand)
			adminKnowledge.POST("/brands/:id/enable", EnableBrand)
			adminKnowledge.POST("/brands/:id/disable", DisableBrand)
			adminKnowledge.DELETE("/brands/:id", DeleteBrand)

			adminKnowledge.GET("/models", GetModelList)
			adminKnowledge.GET("/models/:id", GetModelDetail)
			adminKnowledge.POST("/models", CreateModel)
			adminKnowledge.PUT("/models/:id", UpdateModel)
			adminKnowledge.POST("/models/:id/enable", EnableModel)
			adminKnowledge.POST("/models/:id/disable", DisableModel)
			adminKnowledge.DELETE("/models/:id", DeleteModel)

			adminKnowledge.GET("/specs", GetSpecList)
			adminKnowledge.POST("/specs", CreateSpec)
			adminKnowledge.PUT("/specs/:id", UpdateSpec)
			adminKnowledge.POST("/specs/:id/enable", EnableSpec)
			adminKnowledge.POST("/specs/:id/disable", DisableSpec)
			adminKnowledge.DELETE("/specs/:id", DeleteSpec)

			adminKnowledge.GET("/compares", GetCompareList)
			adminKnowledge.POST("/compares", CreateCompare)
			adminKnowledge.PUT("/compares/:id", UpdateCompare)
			adminKnowledge.POST("/compares/:id/enable", EnableCompare)
			adminKnowledge.POST("/compares/:id/disable", DisableCompare)
			adminKnowledge.DELETE("/compares/:id", DeleteCompare)

			adminKnowledge.GET("/fragments", GetFragmentList)
			adminKnowledge.GET("/fragments/:id", GetFragmentDetail)
			adminKnowledge.POST("/fragments", CreateFragment)
			adminKnowledge.PUT("/fragments/:id", UpdateFragment)
			adminKnowledge.POST("/fragments/:id/enable", EnableFragment)
			adminKnowledge.POST("/fragments/:id/disable", DisableFragment)
			adminKnowledge.DELETE("/fragments/:id", DeleteFragment)

			adminKnowledge.POST("/reload", ReloadKnowledgeCache)
		}

		// ---- 通道接入（W2/F11）：CRUD + 连通测试 + 出站死信 ----
		// 注：死信列表独立父节点 /admin/channel-dlq，避免与 /admin/channels/:id 的静态-通配同层冲突
		admin.GET("/channels", ListChannels)
		admin.POST("/channels", CreateChannel)
		admin.PUT("/channels/:id", UpdateChannel)
		admin.DELETE("/channels/:id", DeleteChannel)
		admin.PUT("/channels/:id/status", SetChannelStatus)
		admin.POST("/channels/:id/verify", VerifyChannel)
		admin.GET("/channel-dlq", ListChannelDeadLetters)
		admin.POST("/channel-dlq/:id/retry", RetryChannelDeadLetter)
	}
}
