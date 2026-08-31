// 程序入口包：负责服务启动编排（配置→DB→各模块→路由→监听）与全部 HTTP 路由挂载
package main

import (
	"ai-scrm/config"
	"ai-scrm/internal/ai"
	"ai-scrm/internal/api"
	"ai-scrm/internal/cache"
	"ai-scrm/internal/cdp"
	"ai-scrm/internal/chatflow"
	configcenter "ai-scrm/internal/config_center"
	"ai-scrm/internal/db"
	"ai-scrm/internal/engine/flow"
	"ai-scrm/internal/engine/strategy"
	"ai-scrm/internal/gateway"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/mq"
	"ai-scrm/internal/redisclient"
	"ai-scrm/internal/service"
	statemachine "ai-scrm/internal/state_machine"
	"ai-scrm/seed"
	"context"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

// ============================================================
// 程序入口
// 启动顺序：加载配置 → 连接数据库 → 初始化各模块 → 注册路由 → 启动服务
// ============================================================

// startTime 进程启动时间（/status 观测用）
var startTime time.Time

// main 程序入口：按既定顺序编排启动（配置→DB→seed→缓存→引擎→消费者→路由→监听）
func main() {
	startTime = time.Now()
	log.Println("========================================")
	log.Println("  车企AI-SCRM系统后端 启动中...")
	log.Println("========================================")

	// 1. 加载环境变量（.env文件）
	// 如果没有.env文件，使用默认配置
	err := godotenv.Load()
	if err != nil {
		log.Println("未找到.env文件，使用默认配置")
	}

	// 2. 加载配置
	cfg := config.LoadConfig()
	log.Printf("配置加载完成，服务端口: %s", cfg.Server.Port)

	// 2.5 初始化 Redis（多实例协调层：分布式锁/消息转交/缓存失效）
	// 未启用(REDIS_ENABLED=false)或连接失败时自动降级为单实例内存模式
	redisclient.Init(cfg.Redis)

	// 2.6 初始化消息中心（SAAS_PLAN §2.5）：MQ_TYPE=log 降级 / kafka 真实总线
	mq.Init(cfg.MQ)
	defer mq.Close()

	// 3. 设置Gin模式
	gin.SetMode(cfg.Server.Mode)

	// 4. 初始化数据库
	err = db.Init()
	if err != nil {
		log.Fatalf("数据库初始化失败: %v", err)
	}

	// 5. 初始化种子数据（首次启动时）
	seed.InitSeedData()

	// 5.5 seed 建好默认租户后再次回填存量数据
	// （db.Init 内的首次回填在全新库上会因租户不存在而跳过）
	db.BackfillTenantIDs()

	// 5.6 组织架构迁移：角色四级化(sales→user) + 每租户默认根部门 + 存量用户挂载
	db.MigrateOrgData()

	// 6. 初始化缓存层（标签缓存 + 知识库缓存，热更新基础）
	// 顺序：先缓存，再引擎，因为引擎依赖缓存
	cache.InitTagCache()
	cache.InitKnowledgeCache()

	// 6.5 初始化系统配置服务（从DB加载可调参数到内存，支持热加载）
	service.InitSystemConfigService()

	// 7. 初始化AI客户端
	ai.InitClient()
	ai.InitSiliconFlowClient()
	ai.InitRouter()
	service.InitEmbeddingClient() // P0-4 向量检索：未配置 EMBEDDING_* 自动回退关键词

	// 7.5 内嵌 AI 网关（P0-1）：仅在"本进程即网关"模式启动（GatewayListen 非空 且 非网关客户端）。
	// 防环：若同时配了 LLM_GATEWAY_URL（本进程是网关客户端），则不开内嵌网关，避免自转发死循环；
	// 该场景应独立部署 cmd/gateway。内嵌网关复用本进程 ai.Router（本地平台 Key 出网）。
	if cfg.AI.GatewayListen != "" && cfg.AI.GatewayURL == "" {
		go func() {
			srv := gateway.NewServer()
			if err := srv.Run(cfg.AI.GatewayListen); err != nil {
				log.Printf("[AI网关] 内嵌网关启动失败: %v", err)
			}
		}()
		log.Printf("[AI网关] 内嵌网关已启动: %s", cfg.AI.GatewayListen)
	}

	// P2 RLS：多租户行级隔离（受 RLS_ENABLED 控制；默认关闭=应用层 db.T 保证，零行为变更）
	// 开启后业务事务内 SET LOCAL app.current_tenant 即被 DB 强制收敛（双保险）
	service.EnableRLS()

	// P2 collector：启动数据飞轮批量上报（COLLECTOR_URL 空则空转，零外部请求）
	service.StartCollector()

	// 8. 初始化策略中心引擎
	strategy.InitEngine()

	// 9. 初始化流程引擎
	flow.InitEngine()

	// 9.5 tenant_cfg_event 消费者（M3，2026-08-25）：
	// Seed/Upgrade/Rollback 配置动作 → 热加载钩子（策略引擎模板池 + 标签/知识缓存）
	// 此前三动作发布事件后零订阅者，事件链断裂；幂等可重复触发
	configcenter.RegisterHotReloadHook(func(tenantID uint, action string, scope string) {
		strategy.DefaultEngine.ReloadData()
		if cache.DefaultTagCache != nil {
			cache.DefaultTagCache.Reload()
		}
		if cache.DefaultKnowledgeCache != nil {
			cache.DefaultKnowledgeCache.Reload()
		}
	})
	configcenter.StartCfgEventConsumer()

	// 9.2 状态机巡检（SAAS_PLAN §17.3）：心跳超时实例重新入队
	// 多实例安全：Redis 选主，同一时刻只有一个实例执行巡检；未启用 Redis 时各实例直跑（幂等）
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		for range ticker.C {
			if redisclient.IsEnabled() {
				if h := redisclient.TryLock("lock:sm:sweep", 55*time.Second); h != nil {
					statemachine.SweepOnce(10 * time.Minute)
					h.Unlock()
				}
			} else {
				statemachine.SweepOnce(10 * time.Minute)
			}
		}
	}()

	// 9.3 月度用量重置（缺口5修复，2026-08-22）：
	// 每小时检查一次，跨自然月的租户 used_ai_calls 清零（幂等，详见 usage_service）
	// 9.35 商业包到期巡检（M2，2026-08-23）：到期摘除(active→expired) + 到期提醒(企微群)
	// 9.4 订单超时关闭（M4，2026-08-25）：pending 超 order_timeout_minutes(默认15分钟) 自动 closed
	go func() {
		service.ResetAllTenantsMonthlyUsageIfDue() // 启动即补一次
		service.ExpireCheck()
		service.SweepExpiredOrders() // 启动即扫一次僵尸单
		ticker := time.NewTicker(1 * time.Hour)
		sweepCount := 0
		for range ticker.C {
			run := func() {
				service.ResetAllTenantsMonthlyUsageIfDue()
				service.ExpireCheck()
			}
			if redisclient.IsEnabled() {
				if h := redisclient.TryLock("lock:usage:reset", 55*time.Minute); h != nil {
					run()
					h.Unlock()
				}
			} else {
				run()
			}
		}
		_ = sweepCount // 订单扫描走下方独立短周期 ticker（见 9.45）
	}()

	// 9.44 数据飞轮回流上报器（P3，2026-08-26）：每小时把上一窗口的配置调参/包操作
	// 审计增量 POST 到 feedback_collector_url（空=关闭）。失败仅告警不影响业务。
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		var lastID uint
		db.DB.Table("tenant_audit_logs").Select("COALESCE(MAX(id),0)").Scan(&lastID)
		for range ticker.C {
			// K8修复(2026-08-26)：上报成功才推进 lastID，失败保留以重试，避免增量审计数据漏传
			if service.ReportAuditIncrement(lastID) {
				db.DB.Table("tenant_audit_logs").Select("COALESCE(MAX(id),0)").Scan(&lastID)
			}
		}
	}()

	// P0-1 数据飞轮：批量评估定时任务（每小时评估待审素材，加速素材池流转）
	service.StartBatchEvaluator()

	// 9.45 订单超时扫描（M4，2026-08-25）：每5分钟一次（阈值 order_timeout_minutes 默认15分钟）
	// 多实例安全：Redis TryLock 选主；未启用 Redis 各实例直跑（条件 UPDATE 天然幂等）
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		for range ticker.C {
			if redisclient.IsEnabled() {
				if h := redisclient.TryLock("lock:billing:sweep", 4*time.Minute); h != nil {
					service.SweepExpiredOrders()
					h.Unlock()
				}
			} else {
				service.SweepExpiredOrders()
			}
		}
	}()

	// 8.49 订阅生命周期 + 对账（P2）：每 6 小时生成续费订单并对账补救发放失败单
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		for range ticker.C {
			runRecon := func(name string, fn func() int) {
				if redisclient.IsEnabled() {
					if h := redisclient.TryLock("lock:"+name, 5*time.Hour); h != nil {
						fn()
						h.Unlock()
					}
				} else {
					fn()
				}
			}
			runRecon("billing:renew", service.SweepSubscriptionRenewals)
			runRecon("billing:reconcile", service.ReconcileBilling)
		}
	}()

	// 9.5 AI 冷却模型恢复（P0修复，2026-08-26）：
	// 原 RecoverCoolingModels 全仓零调用——供应商连续失败5次被置 Available=false 后
	// 永远跳过、markSuccess 永远无机会执行，模型直到进程重启都是砖。
	// 现每60s检查一次，冷却到期(默认300s)自动复活重试。纯内存操作，多实例各自执行无害。
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		for range ticker.C {
			if ai.Router != nil {
				ai.Router.RecoverCoolingModels()
			}
		}
	}()

	// 8.4 CDP 摄入消费者（user_event 写收口，SAAS_PLAN §16.8）
	cdp.StartIngestConsumer()

	// 8.45 编排层事件消费者（Phase C/D，2026-08-22）：
	// 流程引擎并行订阅 user_event(心跳) + flow_result(推进主干)
	flow.StartOrchestrationConsumers()
	// 8.5b 付费成功→开通欢迎流程消费者（运营闭环 P1-2）
	flow.StartPaymentConsumer()

	// 8.46 业务层下行指令消费者（Phase C）：flow_drive → 代发消息/requeue
	chatflow.StartDriveConsumer()

	// 8.47 行业包自动应用（auto_rox 落地）：对未绑定任何包的租户绑定默认行业/企业包（幂等）
	go func() {
		defer func() { _ = recover() }()
		api.AutoApplyDefaultIndustryPack()
	}()

	// 8.5 启动消息消费循环（kafka 模式生效；log 模式空操作）
	go mq.StartConsumers(context.Background())

	// 9. 初始化Gin引擎
	r := gin.Default()

	// 10. 注册中间件
	// 顺序：CORS → TenantResolver（全局，fail-closed）
	// 登录态路由在各分组再挂 JWTAuth → TenantConsistency 完成租户一致性裁决
	r.Use(middleware.CORS())
	r.Use(middleware.TraceID()) // P1-3：全链路 trace 注入（须在鉴权前，覆盖拒登/超管路径）
	r.Use(middleware.TenantResolver())

	// 11. 注册路由
	registerRoutes(r)

	// 12. 启动服务
	log.Println("========================================")
	log.Printf("  服务启动成功！监听端口: %s", cfg.Server.Port)
	log.Println("  管理账号: admin / admin123")
	log.Println("  销售账号: sales1 / sales123")
	log.Printf("  后台管理: http://localhost:%s/admin", cfg.Server.Port)
	log.Println("========================================")

	err = r.Run(":" + cfg.Server.Port)
	if err != nil {
		log.Fatalf("服务启动失败: %v", err)
	}
}

// registerRoutes 注册所有路由
func registerRoutes(r *gin.Engine) {
	// ---- 健康检查 ----
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"status":  "ok",
			"message": "ai-scrm is running",
		})
	})

	// ---- Status Page（商业化 M4，免鉴权无敏感信息，借鉴翻译助手三期§3.6）----
	r.GET("/status", func(c *gin.Context) {
		// P1-4 健康探测：结构化探针 + 阈值告警（crit 越界经企微/钉钉主动通知，带冷却）
		snap := service.ComputeHealth()
		service.MaybeAlert(snap)
		status := "ok"
		if snap.HasCrit {
			status = "crit"
		} else if snap.HasWarn {
			status = "warn"
		}
		var crit24h int64
		for _, ch := range snap.Checks {
			if ch.Name == "critical_24h" {
				if v, err := strconv.ParseInt(ch.Value, 10, 64); err == nil {
					crit24h = v
				}
			}
		}
		c.JSON(200, gin.H{"code": 0, "data": gin.H{
			"version":             "v2.3.0",
			"uptime_sec":          int(time.Since(startTime).Seconds()),
			"goroutines":          runtime.NumGoroutine(),
			"db_ok":               snap.DBOK,
			"merge_queue_active":  service.DefaultMessageQueueService.ActiveQueueCount(),
			"critical_alerts_24h": crit24h,
			"health":              snap.Checks,
			"status":              status,
			"ok":                  snap.DBOK,
		}})
	})

	// ---- 首页导航（根路径，给外部体验者选择入口）----
	// 修复：之前访问根路径返回404，外部用户不知道要加/client
	// 现在根路径展示三端导航页，点击即跳转
	// P-FE（2026-08-26）：React SPA 托管 —— frontend-react/dist
	// 历史路由单页应用：所有非 /api 路径回退到 index.html；
	// 静态资源（/assets/*）若存在则直接返回。旧的零构建 HTML 页已全部迁移至 React。
	distDir := filepath.Join("frontend-react", "dist")
	if _, err := os.Stat(filepath.Join(distDir, "index.html")); err == nil {
		r.NoRoute(func(c *gin.Context) {
			reqPath := c.Request.URL.Path
			if strings.HasPrefix(reqPath, "/api") {
				c.Status(404)
				return
			}
			clean := filepath.Clean(reqPath)
			full := filepath.Join(distDir, clean)
			if fi, err := os.Stat(full); err == nil && !fi.IsDir() {
				c.File(full)
				return
			}
			c.File(filepath.Join(distDir, "index.html"))
		})
		log.Println("[FE] React SPA 已挂载: / + /app/")

		// ---- Prometheus 指标端点（P2 监控闭环，零依赖；文本格式由 service.RenderPrometheus 生成）----
		r.GET("/metrics", func(c *gin.Context) {
			c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
			c.String(200, service.RenderPrometheus())
		})
		// 请求计数 + 延迟直方图（P1-2：P99 来源）中间件
		r.Use(func(c *gin.Context) {
			start := time.Now()
			service.IncRequest()
			c.Next()
			service.RecordRequestLatency(time.Since(start))
		})
	} else {
		log.Println("[FE] 未找到 frontend-react/dist，跳过 SPA 托管（请先 cd frontend-react && npm run build）")
	}

	// ---- API v1 路由组 ----
	v1 := r.Group("/api/v1")

	// ---- WebSocket 实时推送（P1-2，独立鉴权：advisor 用 query token，client 用 visitor_key）----
	// 不挂 JWTAuth 组：WS 难以附带 Authorization header，改用 query 参数手动校验
	v1.GET("/ws/advisor", api.WSAdvisor)
	v1.GET("/ws/client", api.WSClient)

	// ---- 数据飞轮聚合接收端（P2 collector，自有 X-Collector-Key 鉴权，独立于 JWT）----
	r.POST("/api/v1/collector", api.CollectorReceive)

	// 认证相关（无需登录）
	auth := v1.Group("/auth")
	{
		auth.POST("/login", api.Login)                                                                                                                  // 登录
		auth.POST("/register", api.Register)                                                                                                            // 注册（邮箱验证开关开启时需验证码）
		auth.GET("/register-config", api.RegisterConfig)                                                                                                // 注册页配置下发（邮箱验证显隐）
		auth.POST("/email-code", middleware.TurnstileGuard(), middleware.IPRateLimit("reset_email_code", 5, 10*time.Minute), api.SendRegisterEmailCode) // 注册验证码发送（J6 防枚举/限频）
		auth.POST("/reset-password", middleware.IPRateLimit("reset_pwd", 5, 10*time.Minute), api.SendResetCode)                                         // 发送重置验证码（J6 防枚举+限频）
		auth.POST("/verify-reset-code", middleware.IPRateLimit("verify_reset", 10, 10*time.Minute), api.VerifyResetCode)                                // 验证验证码重置密码（J6 限频防爆破）
	}

	// 邮箱换绑（登录态）：向新邮箱发码 → 校验完成绑定
	v1.POST("/auth/email/code", middleware.JWTAuth(), api.SendBindEmailCode)
	v1.POST("/auth/email/change", middleware.JWTAuth(), middleware.OrgResolve(), api.ChangeEmail)

	// ---- 租户入驻与套餐（免登录公开）----
	v1.POST("/tenant/signup", middleware.IPRateLimit("tenant_signup", 15, 10*time.Minute), api.TenantSignup)           // J6 入驻限频防刷
	v1.GET("/tenant/check-code", middleware.IPRateLimit("tenant_check_code", 30, 10*time.Minute), api.CheckTenantCode) // J6 标识查询限频
	v1.GET("/plans", api.ListPlans)                                                                                    // 定价页：legacy plans + 商业包并存
	v1.GET("/packages", api.ListPackages)                                                                              // 公开商业包列表（M2 定价数据源）

	// ---- 公开品牌配置（按 Host 解析租户白标，免登录）----
	v1.GET("/public/branding", api.GetPublicBranding)

	// ---- OpenAPI 开放接口（M4，独立鉴权链）----
	// 不走 JWTAuth/TenantConsistency/TenantResolver：租户来自 API Key 归属
	openapi := r.Group("/openapi/v1")
	openapi.Use(middleware.OpenAPIAuth())
	{
		openapi.GET("/customers", middleware.RequirePerm(middleware.PermCustomerRead), api.OpenAPICustomers)
		openapi.GET("/customers/:id/conversations", middleware.RequirePerm(middleware.PermCustomerRead), api.OpenAPICustomerConversations)
		openapi.GET("/cdp/profiles/:one_id", middleware.RequirePerm(middleware.PermCDPRead), api.OpenAPICDPProfile)
		openapi.GET("/usage", middleware.RequirePerm(middleware.PermAll), api.OpenAPIUsage)
		// 渠道嵌入对话端点（M4 扩展 2026-08-29）：与站内同池同链路，复用 OrchestrateReply
		openapi.POST("/chat/completions", middleware.RequirePerm(middleware.PermChatWrite), api.OpenAPIChatCompletions)
	}

	// AI对话测试（免登录，方便调试）
	// TurnstileGuard：防薅人机验证（后台开关关闭时零开销直通）
	// IPRateLimit（P0安全止血 2026-08-26）：每IP每租户20次/分钟——
	// 该接口每条消息真调 AI+扣商业包额度+写多表，是匿名刷量的最大入口
	v1.POST("/chat/test", middleware.TurnstileGuard(), middleware.IPRateLimit("chat_test", 20, time.Minute), api.ChatTest)

	// 访客注册（免登录，每次打开client页面创建新访客）
	v1.POST("/chat/guest", middleware.TurnstileGuard(), middleware.IPRateLimit("chat_guest", 10, time.Minute), api.CreateGuest)

	// Turnstile 站点键下发（免登录公开；enabled=false 时前端不渲染组件）
	v1.GET("/turnstile/sitekey", func(c *gin.Context) {
		enabled, siteKey := middleware.GetTurnstileSiteKey()
		c.JSON(200, gin.H{"code": 0, "data": gin.H{"enabled": enabled, "site_key": siteKey}})
	})

	// 会话欢迎接口（免登录，独立秒回，无AI处理）
	// 前端在用户打开页面时调用，立刻返回欢迎消息，不等AI生成
	// P0安全止血(2026-08-26)：原接口连 Turnstile 都没有且每次调用插一条 Message，
	// 是最廉价的 DB 写放大入口——加 IP 限流 30 次/分钟
	v1.POST("/chat/welcome", middleware.IPRateLimit("chat_welcome", 30, time.Minute), api.Welcome)

	// 聊天历史查询（免登录，客户端和销售端共用）
	v1.GET("/chat/history", api.GetChatHistory)

	// 延迟清零接口（免登录，顾问/管理员点击"立即回复"按钮时调用）
	// 修复问题3：顾问发完人工消息后，AI的模拟延迟还没结束，客户等太久
	v1.POST("/chat/clear-delay", api.ClearDelay)

	// 支付网关异步回调（免登录，服务端到服务端）：必须挂在此处（v1.Use(JWTAuth...) 之前），
	// 支付网关回调不携带用户 JWT，安全性靠 HMAC 验签（VerifyGatewaySign），
	// 若挂在下方鉴权组内会被 JWTAuth 401 拦截导致永远无法到账（UAT 2026-08-31 修复）。
	v1.POST("/billing/webhook/:channel", api.BillingWebhook)

	// ---- 客户端/销售端页面由 React SPA 统一托管（NoRoute 回退 index.html）----

	// ============================================================
	// 顾问端 + 后台管理接口鉴权
	// 修复（安全）：这两组接口原来完全不鉴权，任何能访问到这个端口的人
	// 都能读写全部客户数据、改配置、改标签规则。现在补上JWT鉴权：
	// - advisorGroup：只要求登录（顾问和管理员都能进）
	// - admin：要求登录 + admin角色
	// 对应地，advisor.html / admin.html 前端也补了登录页 + 请求自动带Token，
	// 见 frontend/advisor.html、frontend/admin.html 里的 apiFetch()。
	// ============================================================

	// ---- 顾问工作台API（销售端专用，需登录 + 租户一致性校验）----
	// TenantConsistency：JWT Claims 租户 ↔ Host 解析租户一致性校验 + 生效租户裁决
	// M3：MustChangePasswordGuard 首登强改密拦截（全鉴权组统一挂载）
	advisorGroup := v1.Group("/advisor")
	advisorGroup.Use(middleware.JWTAuth(), middleware.TenantConsistency(), middleware.OrgResolve(),
		middleware.MustChangePasswordGuard(), middleware.ReadonlyWriteGuard())
	{
		advisorGroup.GET("/list", api.GetAdvisorList)                     // 顾问列表（切换身份用）
		advisorGroup.GET("/stats", api.GetAdvisorStats)                   // 工作台数据统计
		advisorGroup.GET("/customers", api.GetAdvisorCustomers)           // 客户列表
		advisorGroup.GET("/customer/:id", api.GetAdvisorCustomerDetail)   // 客户详情
		advisorGroup.PUT("/customer/:id/tags", api.EditCustomerTags)      // 编辑客户标签
		advisorGroup.PUT("/customer/:id/info", api.EditCustomerInfo)      // 编辑客户信息
		advisorGroup.PUT("/customer/:id/stage", api.UpdateCustomerStage)  // 修改线索状态（已到店/已试驾/已报价）
		advisorGroup.POST("/customer/:id/followup", api.CreateFollowup)   // 设置跟进提醒
		advisorGroup.GET("/followups", api.GetFollowups)                  // 跟进提醒列表
		advisorGroup.POST("/chat/takeover", api.AdvisorTakeover)          // 一键接管
		advisorGroup.POST("/chat/send", api.AdvisorSendMessage)           // 人工发送消息
		advisorGroup.POST("/chat/ai-reply", api.AdvisorTriggerAIReply)    // 手动触发AI回复
		advisorGroup.POST("/chat/toggle-ai-reply", api.ToggleAiReply)     // 手动切换AI回复开关
		advisorGroup.GET("/strategy/recommend", api.GetStrategyRecommend) // 策略话术推荐
		advisorGroup.POST("/test-drive", api.CreateTestDrive)             // 创建试驾单
		advisorGroup.GET("/test-drives", api.GetTestDrives)               // 试驾单列表
		advisorGroup.GET("/test-drive/:id", api.GetTestDrive)             // 试驾单详情
		advisorGroup.PUT("/test-drive/:id", api.UpdateTestDrive)          // 更新试驾单
	}

	// ---- 平台超管后台（仅 super_admin）----
	super := v1.Group("/super")
	super.Use(middleware.JWTAuth(), middleware.TenantConsistency(), middleware.OrgResolve(),
		middleware.MustChangePasswordGuard(), api.SuperRequired())
	{
		super.GET("/tenants", api.SuperTenantList)
		super.PUT("/tenants/:id/status", api.SuperTenantStatus)
		super.POST("/tenants/:id/grant-trial", api.SuperGrantTrial) // M1 审核模式放行（幂等）
		// ---- 商业化 M1/M2/M5 ----
		super.GET("/orders/pending", api.SuperPendingOrders)       // 待人工确认收款列表
		super.POST("/orders/:id/confirm", api.SuperConfirmOrder)   // 确认到账→幂等发放
		super.GET("/packages", api.SuperPackageList)               // 商业包全量列表
		super.POST("/packages", api.SuperPackageCreate)            // 新建商业包
		super.PUT("/packages/:id", api.SuperPackageUpdate)         // 编辑/启停
		super.DELETE("/packages/:id", api.SuperPackageDelete)      // 删除（有订单引用则转下架）
		super.GET("/audit-logs", api.SuperAuditLogs)               // 审计日志查询（全平台）
		super.GET("/usage/cost", api.SuperUsageCost)               // 模型成本核算（M3 选型依据）
		super.GET("/feedbacks", api.SuperFeedbackList)             // 用户反馈列表（M2）
		super.POST("/feedbacks/resolve", api.SuperResolveFeedback) // 反馈标记已处理
		// ---- 行业包平台侧（P1，2026-08-25）：上传验签/列表/启停 ----
		super.POST("/packs", api.SuperPackUpload)
		super.GET("/packs", api.SuperPackList)
		super.GET("/materials", api.SuperMaterialList)               // P3 素材池列表
		super.POST("/materials/:id/review", api.SuperMaterialReview) // 人工评审
		super.POST("/materials/:id/evals", api.SuperMaterialEvals)   // AI evals 评分
		super.GET("/agreements", api.SuperAgreementList)             // 协议签署台账（注册即同意审计）
		super.PUT("/tenants/:id/branding", api.SuperUpdateBranding)  // 超管设置任意租户白标（品牌名/logo/域名）
		super.GET("/tenants/:id/branding", api.SuperGetBranding)     // 超管读取任意租户白标
		super.PUT("/packs/:id/status", api.SuperPackStatus)
		super.PUT("/packs/:id/share", api.SuperPackShare) // KB继承链：跨部门共享 opt-out
		// ---- 监控告警（P1-4，2026-08-29）----
		super.GET("/monitor/health", api.SuperMonitorHealth) // 平台级结构化健康探测
	}

	// ---- 组织架构管理（四级用户体系，P2）----
	// 权限：tenant_admin=全租户；dept_admin=本子树（不含本级任命管理员）
	orgGroup := v1.Group("/org")
	orgGroup.Use(middleware.JWTAuth(), middleware.TenantConsistency(), middleware.OrgResolve(),
		middleware.MustChangePasswordGuard(), middleware.ReadonlyWriteGuard(), api.OrgManageRequired())
	{
		orgGroup.GET("/departments/tree", api.GetDepartmentTree)
		orgGroup.POST("/departments", api.CreateDepartment)
		orgGroup.PUT("/departments/:id", api.UpdateDepartment)
		orgGroup.DELETE("/departments/:id", api.DeleteDepartment)
		orgGroup.GET("/users", api.GetManagedUsers)
		orgGroup.POST("/users", api.CreateUser)
		orgGroup.PUT("/users/:id", api.UpdateUser)
	}

	// ---- CDP OpenAPI（只读出口，Phase A）----
	// 服务端二次校验租户归属 + 字段脱敏 + 只查不写；仅 B 端登录可见
	cdpGroup := v1.Group("/cdp")
	cdpGroup.Use(middleware.JWTAuth(), middleware.TenantConsistency(), middleware.OrgResolve(), middleware.MustChangePasswordGuard())
	{
		cdpGroup.GET("/profiles/:one_id", api.GetCDPProfile)
		cdpGroup.GET("/segments", api.GetCDPSegment)
		cdpGroup.GET("/tag-defs", api.ListCDPTagDefs)
	}

	// ---- 客户端知识库接口（免鉴权，公开查询）----
	knowledge := v1.Group("/knowledge")
	{
		knowledge.GET("/brands", api.GetPublicBrands)           // 品牌列表
		knowledge.GET("/models", api.GetPublicModels)           // 车型列表
		knowledge.GET("/models/:id", api.GetPublicModelDetail)  // 车型详情（含规格）
		knowledge.GET("/compares", api.GetPublicCompares)       // 竞品对比
		knowledge.GET("/fragments/search", api.SearchFragments) // 搜索知识片段
	}

	// ---- 管理端接口（需登录 + admin角色 + 租户一致性校验）----
	admin := v1.Group("/admin")
	// H1 修复：AdminRequired 必须放在 OrgResolve 之后。
	// 原顺序 AdminRequired(读JWT claim) → OrgResolve(从DB覆盖role)，
	// 导致降权/升权在重新登录前不生效。现改为 DB 角色权威：先 OrgResolve 再 AdminRequired。
	admin.Use(middleware.JWTAuth(), middleware.TenantConsistency(), middleware.OrgResolve(),
		middleware.MustChangePasswordGuard(), middleware.AdminRequired())
	{
		// ---- 审计日志查询（M5，本租户）----
		admin.GET("/audit-logs", api.AdminAuditLogs)

		// ---- 用量看板（M3，本租户：趋势+阶段分布+成本口径）----
		admin.GET("/usage/summary", api.AdminUsageSummary)

		// ---- 邀请推广（M-R，2026-08-25）：邀请码/统计 + 后端二维码 PNG ----
		admin.GET("/referral/info", api.GetReferralInfo)
		admin.GET("/referral/records", api.GetReferralRecords) // 邀请记录：受邀id/邮箱/邀请与支付/奖励发放
		admin.GET("/referral/qrcode", api.GetReferralQRCode)

		// ---- 行业包租户侧（P1 三级架构，2026-08-26）：分层列表/两级绑定/部门绑定 ----
		admin.GET("/packs", api.TenantPackList)
		admin.POST("/packs/bind", api.TenantPackBind)
		admin.POST("/packs/unbind", api.TenantPackUnbind)
		admin.GET("/packs/current", api.TenantPackCurrent)
		admin.POST("/packs/bind-dept", api.TenantPackBindDept)
		admin.POST("/packs/unbind-dept", api.TenantPackUnbindDept)

		// ---- 租户自定义知识库（P2 双层KB，2026-08-26）----
		admin.POST("/kb/upload", api.TenantKBUpload)
		admin.GET("/kb/my", api.TenantKBMy)
		admin.DELETE("/kb/my/:id", api.TenantKBDelete)

		// ---- 账号注销（P4，2026-08-26）：次日生效禁登录+同步禁用API Key+数据保留 ----
		admin.POST("/account/cancel", api.CancelAccount)

		// ---- API Key 自助管理（M4，B端开放平台）----
		apikeys := admin.Group("/apikeys")
		{
			apikeys.POST("", api.AdminCreateAPIKey)
			apikeys.GET("", api.AdminListAPIKeys)
			apikeys.POST("/:id/disable", api.AdminDisableAPIKey)
			apikeys.POST("/:id/enable", api.AdminEnableAPIKey)
			apikeys.DELETE("/:id", api.AdminDeleteAPIKey)
		}

		// ---- 系统配置管理 ----
		admin.GET("/config", api.GetSystemConfigs)             // 查询配置
		admin.PUT("/config", api.BatchUpdateSystemConfig)      // 批量更新配置
		admin.PUT("/tenant/branding", api.AdminUpdateBranding) // 租户管理员设置自身租户白标
		// J1修复(2026-08-26)：reset/init 重置/强刷平台配置，仅超管可操作（防租户admin误清全局配置）
		admin.POST("/config/reset", api.SuperRequired(), api.ResetSystemConfig) // 恢复默认值
		// 修复(2026-08-25)：rollback handler 存在但路由从未注册——补上。
		// 走 config_center.Rollback 会发布 tenant_cfg_event 驱动各引擎热加载（M3 闭环）
		admin.POST("/config/rollback", api.RollbackTenantConfig)
		// J1修复(2026-08-26)：强制初始化默认配置同样仅超管可操作
		admin.POST("/config/init", api.SuperRequired(), api.ForceInitSystemConfig) // 强制初始化默认配置
		admin.GET("/models", api.GetAvailableModels)                               // 获取可用模型列表

		// ---- 标签管理 ----
		adminTags := admin.Group("/tags")
		{
			adminTags.GET("", api.GetTagList)
			adminTags.GET("/:id", api.GetTagDetail)
			adminTags.POST("", api.CreateTag)
			adminTags.PUT("/:id", api.UpdateTag)
			adminTags.POST("/:id/enable", api.EnableTag)
			adminTags.POST("/:id/disable", api.DisableTag)
			adminTags.DELETE("/:id", api.DeleteTag)
			adminTags.POST("/reload", api.ReloadTagCache)
		}

		// ---- 打标规则管理 ----
		adminTagRules := admin.Group("/tag-rules")
		{
			adminTagRules.GET("", api.GetTagRuleList)
			adminTagRules.POST("", api.CreateTagRule)
			adminTagRules.PUT("/:id", api.UpdateTagRule)
			adminTagRules.POST("/:id/enable", api.EnableTagRule)
			adminTagRules.POST("/:id/disable", api.DisableTagRule)
			adminTagRules.DELETE("/:id", api.DeleteTagRule)
		}

		// ---- 标签权重映射管理 ----
		adminTagWeights := admin.Group("/tag-weights")
		{
			adminTagWeights.GET("", api.GetTagWeightList)
			adminTagWeights.POST("", api.CreateTagWeight)
			adminTagWeights.PUT("/:id", api.UpdateTagWeight)
			adminTagWeights.DELETE("/:id", api.DeleteTagWeight)
		}

		// ---- 知识库管理 ----
		adminKnowledge := admin.Group("/knowledge")
		{
			adminKnowledge.GET("/brands", api.GetBrandList)
			adminKnowledge.GET("/brands/:id", api.GetBrandDetail)
			adminKnowledge.POST("/brands", api.CreateBrand)
			adminKnowledge.PUT("/brands/:id", api.UpdateBrand)
			adminKnowledge.POST("/brands/:id/enable", api.EnableBrand)
			adminKnowledge.POST("/brands/:id/disable", api.DisableBrand)
			adminKnowledge.DELETE("/brands/:id", api.DeleteBrand)

			adminKnowledge.GET("/models", api.GetModelList)
			adminKnowledge.GET("/models/:id", api.GetModelDetail)
			adminKnowledge.POST("/models", api.CreateModel)
			adminKnowledge.PUT("/models/:id", api.UpdateModel)
			adminKnowledge.POST("/models/:id/enable", api.EnableModel)
			adminKnowledge.POST("/models/:id/disable", api.DisableModel)
			adminKnowledge.DELETE("/models/:id", api.DeleteModel)

			adminKnowledge.GET("/specs", api.GetSpecList)
			adminKnowledge.POST("/specs", api.CreateSpec)
			adminKnowledge.PUT("/specs/:id", api.UpdateSpec)
			adminKnowledge.POST("/specs/:id/enable", api.EnableSpec)
			adminKnowledge.POST("/specs/:id/disable", api.DisableSpec)
			adminKnowledge.DELETE("/specs/:id", api.DeleteSpec)

			adminKnowledge.GET("/compares", api.GetCompareList)
			adminKnowledge.POST("/compares", api.CreateCompare)
			adminKnowledge.PUT("/compares/:id", api.UpdateCompare)
			adminKnowledge.POST("/compares/:id/enable", api.EnableCompare)
			adminKnowledge.POST("/compares/:id/disable", api.DisableCompare)
			adminKnowledge.DELETE("/compares/:id", api.DeleteCompare)

			adminKnowledge.GET("/fragments", api.GetFragmentList)
			adminKnowledge.GET("/fragments/:id", api.GetFragmentDetail)
			adminKnowledge.POST("/fragments", api.CreateFragment)
			adminKnowledge.PUT("/fragments/:id", api.UpdateFragment)
			adminKnowledge.POST("/fragments/:id/enable", api.EnableFragment)
			adminKnowledge.POST("/fragments/:id/disable", api.DisableFragment)
			adminKnowledge.DELETE("/fragments/:id", api.DeleteFragment)

			adminKnowledge.POST("/reload", api.ReloadKnowledgeCache)
		}
	}

	// 需要登录的接口（JWT鉴权 + 租户一致性校验 + 组织上下文）
	// M3：追加 MustChangePasswordGuard——首登强改密标记=true 时，
	// 除 change-password / auth/me 外全部 403，改密成功自动解除
	v1.Use(middleware.JWTAuth(), middleware.TenantConsistency(), middleware.OrgResolve(), middleware.MustChangePasswordGuard())
	{
		// 当前用户信息
		v1.GET("/auth/me", api.GetCurrentUser)

		// ---- 修改密码（M3 安全三件套）----
		v1.POST("/auth/change-password", api.ChangePassword)

		// ---- 用户反馈（M2）：登录用户均可提交 ----
		v1.POST("/feedback", api.CreateFeedback)
		// P0-2：满意度评分采集（CDP att_satisfaction 标签驱动）
		v1.POST("/feedback/rating", api.CreateFeedbackRating)

		// ---- 收银台（商业化 M1/M2）----
		// 查询/订阅入口全员可看；下单/支付/确认需管理员权限
		billing := v1.Group("/billing")
		{
			billing.GET("/my-package", api.MyPackage) // 顶栏额度展示（全员）
			// 支付网关异步回调已上移到公开路由区（/billing/webhook/:channel，见上方注册），
			// 回调不携带用户 JWT，仅靠 HMAC 验签，不能挂在此鉴权组内。
			billingAdmin := billing.Group("")
			billingAdmin.Use(middleware.AdminRequired())
			{
				billingAdmin.GET("/orders", api.ListBillingOrders)           // 订单列表（服务端锚定租户）
				billingAdmin.POST("/orders", api.CreateBillingOrder)         // 创建订单
				billingAdmin.GET("/orders/:id", api.GetBillingOrder)         // 轮询状态
				billingAdmin.POST("/orders/mock-pay", api.MockPayOrder)      // 模拟到账（仅mock模式）
				billingAdmin.POST("/manual-confirm", api.ManualConfirmPaid)  // 「我已付费」
				billingAdmin.POST("/subscribe", api.SubscribePackage)        // 订阅商业包
				billingAdmin.POST("/orders/:id/refund", api.RefundOrder)     // 退款（幂等）
				billingAdmin.POST("/orders/:id/invoice", api.RequestInvoice) // 申请发票
			}
		}

		// ---- 客户管理 ----
		customers := v1.Group("/customers")
		{
			customers.GET("", api.GetCustomerList)
			customers.POST("", api.CreateCustomer)
			customers.GET("/:id", api.GetCustomer)
			customers.PUT("/:id", api.UpdateCustomer)
			customers.DELETE("/:id", api.DeleteCustomer)
			customers.GET("/:id/conversations", api.GetCustomerConversations)
			customers.GET("/:id/tags", api.GetCustomerTags)
			customers.POST("/:id/tags", api.AddTagsToCustomer)
			customers.DELETE("/:id/tags/:tag_id", api.RemoveCustomerTag)
		}

		// ---- 对话相关 ----
		chat := v1.Group("/chat")
		{
			chat.POST("", api.Chat)
			chat.POST("/human/reply", api.HumanReply)
			chat.POST("/transfer/human", api.TransferToHuman)
			chat.POST("/transfer/ai", api.TransferToAI)
		}

		conversations := v1.Group("/conversations")
		{
			conversations.GET("", api.GetConversationList)
			conversations.GET("/:id/messages", api.GetMessages)
		}

		// ---- 策略中心管理 ----
		strategyGroup := v1.Group("/strategy")
		{
			strategyGroup.POST("/test", api.StrategyTest)
			strategyGroup.GET("/templates", api.GetTemplateList)
			strategyGroup.GET("/templates/:id", api.GetTemplate)
			strategyGroup.POST("/templates", api.CreateTemplate)
			strategyGroup.PUT("/templates/:id", api.UpdateTemplate)
			strategyGroup.DELETE("/templates/:id", api.DeleteTemplate)
			strategyGroup.GET("/features", api.GetFeatureList)
			strategyGroup.GET("/stats/anchors", api.GetAnchorStats)
		}

		// ---- 流程引擎 ----
		flowGroup := v1.Group("/flows")
		{
			flowGroup.GET("", api.GetFlowList)
			flowGroup.GET("/:id", api.GetFlow)
			flowGroup.POST("/start", api.StartFlow)
			flowGroup.POST("/advance", api.AdvanceFlow)
			flowGroup.GET("/instances", api.GetFlowInstanceList)
			flowGroup.GET("/instances/:id", api.GetFlowInstance)
		}

		// ---- 统计 ----
		stats := v1.Group("/stats")
		{
			stats.GET("/overview", api.GetOverview)
		}
	}
}
