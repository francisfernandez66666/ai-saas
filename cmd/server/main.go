// 程序入口包：负责服务启动编排（配置→DB→各模块→路由→监听）与全部 HTTP 路由挂载
package main

import "ai-scrm/internal/billing"

import "ai-scrm/internal/metrics"

import "ai-scrm/internal/notify"

import (
	"ai-scrm/config"
	"ai-scrm/internal/ai"
	"ai-scrm/internal/api"
	"ai-scrm/internal/archive"
	"ai-scrm/internal/attribution"
	"ai-scrm/internal/cache"
	"ai-scrm/internal/cdp"
	"ai-scrm/internal/channel"
	"ai-scrm/internal/chatflow"
	configcenter "ai-scrm/internal/config_center"
	"ai-scrm/internal/contentsafety"
	"ai-scrm/internal/db"
	"ai-scrm/internal/engine/flow"
	"ai-scrm/internal/engine/strategy"
	"ai-scrm/internal/gateway"
	"ai-scrm/internal/logx"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/mq"
	"ai-scrm/internal/privacy"
	"ai-scrm/internal/realtime"
	"ai-scrm/internal/redisclient"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/service"
	statemachine "ai-scrm/internal/state_machine"
	"ai-scrm/internal/webhook"
	"ai-scrm/seed"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/getsentry/sentry-go"
	sentrygin "github.com/getsentry/sentry-go/gin"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

// ============================================================
// 程序入口
// 启动顺序：加载配置 → 连接数据库 → 初始化各模块 → 注册路由 → 启动服务
// ============================================================

// startTime 进程启动时间（/status 观测用）
var startTime time.Time

// safeRun R19 修复(2026-09-11)：后台 ticker 巡检任务统一 panic 护栏。
// 原各 goroutine 裸调用业务函数，任一轮 panic（如空指针/DB 异常解引用）会击穿整个进程——
// 一个对账/清理任务的偶发崩溃不该带走全站。此处 recover 后打全栈日志，循环继续下一轮。
func safeRun(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[PANIC-GUARD] 后台任务 %s 崩溃已拦截，本轮跳过：%v\n%s", name, r, debug.Stack())
		}
	}()
	fn()
}

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

	// 2.0 结构化日志（2026-09-15 增强批）：LOG_FORMAT=json|text，release 默认 json、debug 默认 text。
	// 全仓 556 处 std log.Printf 经 logx 桥接逐条转 slog 记录，云端可直接按行解析聚合。
	logFormat := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_FORMAT")))
	if logFormat != "text" && logFormat != "json" {
		logFormat = "text"
		if cfg.Server.Mode == "release" {
			logFormat = "json"
		}
	}
	logx.InitStructuredLogging(logFormat)

	log.Printf("配置加载完成，服务端口: %s（日志格式 %s）", cfg.Server.Port, logFormat)

	// 2.1 网关配置安全校验（P0-5 修复：配了 URL 却漏配 TOKEN 会让所有租户匿名出网、
	// 计费旁路；配了 LISTEN 却没配 TOKEN 会让内嵌网关成为无鉴权 LLM 代理——一律拒绝启动）
	if cfg.AI.GatewayURL != "" && cfg.AI.GatewayToken == "" {
		log.Fatalf("配置错误: LLM_GATEWAY_URL 已配置但 LLM_GATEWAY_TOKEN 为空——网关侧无法还原租户，计费将全部旁路。请配置共享密钥或移除网关 URL。")
	}
	if cfg.AI.GatewayListen != "" && cfg.AI.GatewayToken == "" {
		log.Fatalf("配置错误: LLM_GATEWAY_LISTEN 已配置但 LLM_GATEWAY_TOKEN 为空——将暴露无鉴权 LLM 代理（P0-5）。请配置共享密钥。")
	}

	// 2.5 初始化 Redis（多实例协调层：分布式锁/消息转交/缓存失效）
	// 未启用(REDIS_ENABLED=false)或连接失败时自动降级为单实例内存模式
	redisclient.Init(cfg.Redis)

	// 2.6 初始化消息中心（SAAS_PLAN §2.5）：MQ_TYPE=log 降级 / kafka 真实总线
	mq.Init(cfg.MQ)
	defer mq.Close()
	mq.SetOnPublishSuccess(metrics.IncKafkaPublish) // G-15：Kafka 发布计数

	// 3. 设置Gin模式
	gin.SetMode(cfg.Server.Mode)

	// C3(2026-09-12)：生产禁用 debug——SQL 全量日志即便已掩码仍不该对外，release 才合规。
	// debug 态打醒目告警，提醒部署切 GIN_MODE=release。
	if cfg.Server.Mode != "release" {
		log.Printf("⚠️  ⚠️  ⚠️  当前运行模式 GIN_MODE=%s（非 release）：SQL/业务日志按调试级别输出，生产环境务必设 GIN_MODE=release ⚠️  ⚠️  ⚠️", cfg.Server.Mode)
	}

	// G6 修复(2026-09-14)：多实例但无 Redis 的静默降级显式告警——
	// 合并队列裁决/跨实例 WS 广播/登录防爆破锁在 Redis 关闭时退化为"各实例单干"，
	// 生产多副本会造成消息双处理、跨实例推送丢失、防爆破可绕。声明副本>1 时启动即 WARN，
	// 并注入健康检查使 /status 转红（release 模式生效，避免本地误报）。
	metrics.SetDeploymentContext(cfg.Server.Replicas, cfg.Server.Mode == "release")
	if cfg.Server.Mode == "release" && cfg.Server.Replicas > 1 && !redisclient.IsEnabled() {
		log.Printf("⚠️  [G6] 声明 APP_REPLICAS=%d（多实例）但未启用 Redis：合并队列/WS 广播/登录锁将退化为单实例语义，"+
			"存在消息双处理与跨实例推送丢失风险。请设 REDIS_ENABLED=true 或修正 APP_REPLICAS。/status 已置红。", cfg.Server.Replicas)
	}

	// 3.5 P1-4 启动断言：prod 环境必须 GIN_MODE=release，否则直接拒启
	// （避免生产暴露调试日志/SQL，GIN_MODE=debug 属合规红线；断言逻辑见 config.AssertProdGinMode）
	if err := config.AssertProdGinMode(); err != nil {
		log.Fatalf("启动断言失败: %v", err)
	}

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

	// 5.7 内容安全词库加载（C1，2026-09-12）：文件缺失则空词库运行，不阻塞启动
	contentsafety.Load()

	// 6. 初始化缓存层（标签缓存 + 知识库缓存，热更新基础）
	// 顺序：先缓存，再引擎，因为引擎依赖缓存
	cache.InitTagCache()
	cache.InitKnowledgeCache()

	// 6.5 初始化系统配置服务（从DB加载可调参数到内存，支持热加载）
	runtimecfg.InitSystemConfigService()

	// 6.6 实时计量批量落库（P1 计费统一 2026-09-03：三桶扣减收敛到 UsageSink 批量落库）
	billing.InitUsageSink()

	// 7. 初始化AI客户端
	ai.InitClient()
	ai.InitSiliconFlowClient()
	ai.InitDeepSeekClient() // P1-5→批三：第三独立供应商（未配 Key 空转）
	ai.InitRouter()
	service.InitEmbeddingClient() // P0-4 向量检索：未配置 EMBEDDING_* 自动回退关键词

	// 7.3 注入 evals 阶段深度评估钩子（数据飞轮批量评估用）
	// 解环：llm→service 已成环且业务层禁直连 llm，service.evals.go 通过函数变量调用本适配器；
	// 适配器经 strategy.GenerateEvals → llm.GenerateEvalsText 走 stage_models evals 阶段模型。
	service.EvalLLMFunc = func(tenantID uint, content string) (float64, []string) {
		prompt := "你是销售话术质量评估器。对下面这条汽车销售回复打分(0-5，支持一位小数)，并给出不超过50字的理由。" +
			"评估维度：口语自然度、需求针对性、推进有效性。只输出 JSON：{\"score\":数字,\"note\":\"理由\"}\n\n回复内容：\n" + content
		messages := []ai.ChatMessage{{Role: "user", Content: prompt}}
		reply, err := strategy.GenerateEvals(tenantID, messages, 0.2)
		if err != nil {
			log.Printf("[DataFlywheel] evals LLM 评估失败 tenant=%d: %v", tenantID, err)
			return 0, nil
		}
		var out struct {
			Score float64 `json:"score"`
			Note  string  `json:"note"`
		}
		if start := strings.Index(reply, "{"); start >= 0 {
			if end := strings.LastIndex(reply, "}"); end > start {
				_ = json.Unmarshal([]byte(reply[start:end+1]), &out)
			}
		}
		if out.Score <= 0 || out.Score > 5 {
			return 0, nil // 解析失败/越界视为无效，回退纯函数预筛
		}
		reasons := []string{}
		if out.Note != "" {
			reasons = append(reasons, out.Note)
		}
		return out.Score, reasons
	}

	// D9 包质量归因旁路：指标/群告警由组合根注入，归因包不直接依赖 service。
	attribution.OnReplyRecorded = metrics.IncPackReply
	attribution.OnLeadCaptured = func(packCode string) { metrics.IncPackLeadCaptured(packCode) }
	attribution.ReplyScoreFunc = func(content string, anchors, forbidden []string) (int, []string) {
		res := service.ScoreReplyOffline(content, anchors, forbidden)
		return int(res.Score*20 + 0.5), res.Reasons
	}
	attribution.PackAlertFunc = func(packCode, message string) {
		metrics.IncPackAlert(packCode)
		notify.NotifyGroup(message)
	}

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
	db.EnableRLS()

	// P2 collector：启动数据飞轮批量上报（COLLECTOR_URL 空则空转，零外部请求）
	service.StartCollector()

	// P2-70：启动 WebSocket hub 僵尸连接清扫（30s 一轮，180s 无活动移除）
	realtime.DefaultHub.StartSweeper()
	realtime.StartRedisBroadcast(realtime.DefaultHub)

	// P2-1：启动租户解析缓存周期清扫（30s 一轮，清理过期正/负缓存条目）
	middleware.StartTenantCacheSweeper()
	// G7 收口(2026-09-16C)：订阅跨实例租户缓存失效广播（Redis 未启用时自动退化为
	// 本实例失效+30s TTL 旧语义）
	if middleware.StartTenantCacheInvalidateSubscriber() {
		log.Printf("[main] 租户缓存失效广播订阅已启动（scrm:tenant:cache:invalidate）")
	}

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
			safeRun("sm:sweep", func() {
				if redisclient.IsEnabled() {
					if h := redisclient.TryLock("lock:sm:sweep", 55*time.Second); h != nil {
						statemachine.SweepOnce(10 * time.Minute)
						h.Unlock()
					}
				} else {
					statemachine.SweepOnce(10 * time.Minute)
				}
			})
		}
	}()

	// 9.3 月度用量重置（缺口5修复，2026-08-22）：
	// 每小时检查一次，跨自然月的租户 used_ai_calls 清零（幂等，详见 usage_service）
	// 9.35 商业包到期巡检（M2，2026-08-23）：到期摘除(active→expired) + 到期提醒(企微群)
	// 9.4 订单超时关闭（M4，2026-08-25）：pending 超 order_timeout_minutes(默认15分钟) 自动 closed
	go func() {
		// P2-6 修复(2026-09-09)：启动即补一次同样加锁——原实现 9.3 三处启动即跑
		// （ResetAllTenantsMonthlyUsageIfDue/ExpireCheck/SweepExpiredOrders）无锁，
		// 多实例同时启动会各自重复执行一轮全表巡检。统一走与周期巡检相同的选主。
		run := func() {
			billing.ResetAllTenantsMonthlyUsageIfDue()
			billing.ExpireCheck()
			billing.SweepExpiredOrders() // 启动即扫一次僵尸单
		}
		if redisclient.IsEnabled() {
			if h := redisclient.TryLock("lock:usage:reset", 55*time.Minute); h != nil {
				safeRun("usage:reset+expire@startup", run)
				h.Unlock()
			}
		} else {
			safeRun("usage:reset+expire@startup", run)
		}
		ticker := time.NewTicker(1 * time.Hour)
		for range ticker.C {
			safeRun("usage:reset+expire", func() {
				runWithLock := func() {
					billing.ResetAllTenantsMonthlyUsageIfDue()
					billing.ExpireCheck()
				}
				if redisclient.IsEnabled() {
					if h := redisclient.TryLock("lock:usage:reset", 55*time.Minute); h != nil {
						runWithLock()
						h.Unlock()
					}
				} else {
					runWithLock()
				}
			})
		}
	}()

	// 9.44 数据飞轮回流上报器（P3，2026-08-26）：每小时把上一窗口的配置调参/包操作
	// 审计增量 POST 到 feedback_collector_url（空=关闭）。失败仅告警不影响业务。
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		var lastID uint
		db.DB.Table("tenant_audit_logs").Select("COALESCE(MAX(id),0)").Scan(&lastID)
		for range ticker.C {
			// K8修复(2026-08-26)：上报成功才推进 lastID，失败保留以重试，避免增量审计数据漏传
			var ok bool
			safeRun("flywheel:report", func() { ok = billing.ReportAuditIncrement(lastID) })
			if ok {
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
			safeRun("billing:sweep", func() {
				if redisclient.IsEnabled() {
					if h := redisclient.TryLock("lock:billing:sweep", 4*time.Minute); h != nil {
						billing.SweepExpiredOrders()
						h.Unlock()
					}
				} else {
					billing.SweepExpiredOrders()
				}
			})
		}
	}()

	// 9.46 消息合并队列空闲回收（2026-09-09 内存治理）：每60s删除空闲>15min的客户队列
	// 纯内存结构，多实例各自清理各自内存，无需 Redis 选主（idempotent）
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		for range ticker.C {
			safeRun("queue:sweep", func() {
				service.DefaultMessageQueueService.SweepIdleQueues(15 * time.Minute)
			})
		}
	}()

	// 9.47 消息中心审计/收件箱每日清理（P1-37，2026-09-09）
	// message_event_records 审计 30 天、inbox_events 收件箱 90 天；payload 为全量原文，
	// 无界留存违反隐私最小化。多实例用 Redis 选主兜底（条件 DELETE 幂等，未启用时各自直跑）。
	// 保留天数可经 system_configs 覆盖：mq_audit_retention_days / mq_inbox_retention_days
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		for range ticker.C {
			safeRun("mq:cleanup", func() {
				run := func() {
					service.CleanupMQTables()
				}
				if redisclient.IsEnabled() {
					if h := redisclient.TryLock("lock:mq:cleanup", 20*time.Minute); h != nil {
						run()
						h.Unlock()
					}
				} else {
					run()
				}
			})
		}
	}()

	// 9.48 消息冷数据归档（2026-09-15 增强批）：messages → messages_archive 每日搬迁。
	// 默认关闭：message_archive_days=0 直接空转；有真实量级后按租户行业节奏手动开（热配置，改完即生效）。
	// 多实例 Redis 选主，CTE 先删后插原子搬移；PIPL 匿名化已同步覆盖归档表（privacy.go）。
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		for range ticker.C {
			safeRun("archive:messages", func() {
				run := func() { archive.RunMessagesOnce() }
				if redisclient.IsEnabled() {
					if h := redisclient.TryLock("lock:archive:messages", 40*time.Minute); h != nil {
						run()
						h.Unlock()
					}
				} else {
					run()
				}
			})
		}
	}()

	// 8.49 订阅生命周期 + 对账（P2）：每 6 小时生成续费订单并对账补救发放失败单
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		for range ticker.C {
			safeRun("billing:renew+reconcile", func() {
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
				runRecon("billing:renew", billing.SweepSubscriptionRenewals)
				runRecon("billing:reconcile", billing.ReconcileBilling)
			})
		}
	}()

	// 9.5 AI 冷却模型恢复（P0修复，2026-08-26）：
	// 原 RecoverCoolingModels 全仓零调用——供应商连续失败5次被置 Available=false 后
	// 永远跳过、markSuccess 永远无机会执行，模型直到进程重启都是砖。
	// 现每60s检查一次，冷却到期(默认300s)自动复活重试。纯内存操作，多实例各自执行无害。
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		for range ticker.C {
			safeRun("ai:recover-cooling", func() {
				if ai.Router != nil {
					ai.Router.RecoverCoolingModels()
				}
			})
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
		// 泛行业化 P4：先自动上架 data/packs 预置行业包（入库 industry_packs），
		// 否则 resolveIndustry 无法识别 realty/b2b/... 等新行业，注册一律回落 general
		api.AutoRegisterLocalPacks()
		api.AutoApplyDefaultIndustryPack()
	}()

	// 8.5 启动消息消费循环（kafka 模式生效；log 模式空操作）
	go mq.StartConsumers(context.Background())

	// 8.6 通道出站队列 worker（W6，2026-09-12）：3s 取到期 pending→适配器发送→指数退避，多实例 Redis 选主
	go func() {
		tk := time.NewTicker(3 * time.Second)
		defer tk.Stop()
		for range tk.C {
			run := func() {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				channel.ProcessDueOutbound(ctx)
			}
			if redisclient.IsEnabled() {
				if h := redisclient.TryLock("lock:channel:outbound", 20*time.Second); h != nil {
					safeRun("channel:outbound", run)
					h.Unlock()
				}
			} else {
				safeRun("channel:outbound", run)
			}
		}
	}()

	// 8.7 微信客服增量拉取（W4，2026-09-12）：5s 轮询启用的 kf 通道 sync_msg 拉增量，多实例 Redis 选主
	go func() {
		tk := time.NewTicker(5 * time.Second)
		defer tk.Stop()
		for range tk.C {
			run := func() {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				channel.SyncAllActiveKfChannels(ctx)
			}
			if redisclient.IsEnabled() {
				if h := redisclient.TryLock("lock:channel:kf_sync", 25*time.Second); h != nil {
					safeRun("channel:kf_sync", run)
					h.Unlock()
				}
			} else {
				safeRun("channel:kf_sync", run)
			}
		}
	}()

	// 8.8 PIPL 删除请求日批（C2，2026-09-12）：每 15 分钟扫描到期的 pending 请求并匿名化（多实例 Redis 选主）
	go func() {
		tk := time.NewTicker(15 * time.Minute)
		defer tk.Stop()
		// 启动即跑一次，缩短验收等待（deadline 通常 +15d，这里只是扫描器节奏）
		run := func() { privacy.ProcessExpired() }
		safeRun("privacy:sweep", run)
		for range tk.C {
			if redisclient.IsEnabled() {
				if h := redisclient.TryLock("lock:privacy:sweep", 10*time.Minute); h != nil {
					safeRun("privacy:sweep", run)
					h.Unlock()
				}
			} else {
				safeRun("privacy:sweep", run)
			}
		}
	}()

	// 8.85 包质量归因小时任务（D9，2026-09-13）：评分补洞 → 物化小时快照 → 低分告警；多实例 Redis 选主。
	go func() {
		packCfgBool := func(key string, def bool) bool {
			if runtimecfg.DefaultSystemConfigService == nil {
				return def
			}
			return runtimecfg.DefaultSystemConfigService.GetBool(key, def)
		}
		packCfgInt := func(key string, def int) int {
			if runtimecfg.DefaultSystemConfigService == nil {
				return def
			}
			return runtimecfg.DefaultSystemConfigService.GetInt(key, def)
		}
		runPackQuality := func() {
			if _, err := attribution.ScoreReplyAttributions(500); err != nil {
				log.Printf("[PackQuality] 离线评分失败: %v", err)
			}
			if err := attribution.SyncPackStats(); err != nil {
				log.Printf("[PackQuality] 包效果快照失败: %v", err)
			}
			if !packCfgBool("evals_pack_alert_enabled", true) {
				return
			}
			alerts, err := attribution.CheckPackQualityAlerts(attribution.AlertFilter{
				Days:        3,
				MinSamples:  packCfgInt("evals_pack_alert_min_samples", 5),
				Threshold:   packCfgInt("evals_pack_alert_score", 60),
				Consecutive: packCfgInt("evals_pack_alert_consecutive", 3),
			})
			if err != nil {
				log.Printf("[PackQuality] 低分告警检查失败: %v", err)
			}
			for _, msg := range alerts {
				log.Printf("[PackQuality] %s", msg)
			}
		}
		if redisclient.IsEnabled() {
			if h := redisclient.TryLock("lock:pack:quality:sweep", 10*time.Minute); h != nil {
				safeRun("pack:quality:sweep@startup", runPackQuality)
				h.Unlock()
			}
		} else {
			safeRun("pack:quality:sweep@startup", runPackQuality)
		}
		tk := time.NewTicker(1 * time.Hour)
		defer tk.Stop()
		for range tk.C {
			if redisclient.IsEnabled() {
				if h := redisclient.TryLock("lock:pack:quality:sweep", 50*time.Minute); h != nil {
					safeRun("pack:quality:sweep", runPackQuality)
					h.Unlock()
				}
			} else {
				safeRun("pack:quality:sweep", runPackQuality)
			}
		}
	}()

	// 8.9 出站事件 webhook worker（D6，2026-09-12）：3s 取到期 pending→签名投递→指数退避/死信/熔断，多实例 Redis 选主
	go func() {
		tk := time.NewTicker(3 * time.Second)
		defer tk.Stop()
		for range tk.C {
			run := func() { webhook.ProcessDue() }
			if redisclient.IsEnabled() {
				if h := redisclient.TryLock("lock:webhook:deliver", 20*time.Second); h != nil {
					safeRun("webhook:deliver", run)
					h.Unlock()
				}
			} else {
				safeRun("webhook:deliver", run)
			}
		}
	}()

	// 9. 初始化Gin引擎
	r := gin.Default()

	// R4 修复(2026-09-11)：gin 默认信任所有来源的 X-Forwarded-For——未经反向代理直连部署时，
	// 客户端可伪造 XFF 绕过 login_guard 防爆破与注册 IP 限流（"每 IP N 次"形同虚设）。
	// 显式声明可信代理：TRUSTED_PROXIES=127.0.0.1,10.0.0.0/8（逗号分隔）；未配置则不信任任何代理。
	if tp := strings.TrimSpace(os.Getenv("TRUSTED_PROXIES")); tp != "" {
		if err := r.SetTrustedProxies(strings.Split(tp, ",")); err != nil {
			log.Printf("[WARN] TRUSTED_PROXIES 配置非法，回退为不信任任何代理: %v", err)
			_ = r.SetTrustedProxies(nil)
		}
	} else {
		_ = r.SetTrustedProxies(nil)
	}

	// 10. 注册中间件
	// E2(2026-09-19 增强批)：Sentry/GlitchTip 双轨错误上报。SENTRY_DSN 未配置=SDK 完全不初始化，
	// slog+/client-errors 既有链路零漂移；Repanic=true 让位外层 gin.Recovery 渲染响应（形态不变），
	// WaitForDelivery=false 保证上报异步不拖慢请求。
	if dsn := strings.TrimSpace(config.GlobalConfig.Server.SentryDSN); dsn != "" {
		if err := sentry.Init(sentry.ClientOptions{
			Dsn:         dsn,
			Release:     "ai-scrm@v2.16.0",
			Environment: config.GlobalConfig.Server.Mode,
			// 采样与面包屑用 SDK 默认；PII 红线：不采集请求 body（默认即关），
			// 错误消息里的手机号由后端既有脱敏在入日志前完成，Sentry 只见已净化文本
			SendDefaultPII: false,
		}); err != nil {
			log.Printf("[E2] Sentry 初始化失败，降级为既有通道: %v", err)
		} else {
			r.Use(sentrygin.New(sentrygin.Options{Repanic: true, WaitForDelivery: false}))
			defer sentry.Flush(2 * time.Second)
			log.Printf("[E2] Sentry 错误上报已启用（release=ai-scrm@v2.16.0）")
		}
	}
	// 顺序：CORS → TenantResolver（全局，fail-closed）
	// 登录态路由在各分组再挂 JWTAuth → TenantConsistency 完成租户一致性裁决
	r.Use(middleware.CORS())
	r.Use(middleware.TraceID()) // P1-3：全链路 trace 注入（须在鉴权前，覆盖拒登/超管路径）
	// P1-45(2026-09-09)：基础安全响应头（防嗅探/防点击劫持/凭证泄漏降权）——
	// 叠加白标 custom_css 仅超管可改的后端限制，构成 XSS 纵深防御
	r.Use(func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "SAMEORIGIN")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		// P2-9a(2026-09-15)：CSP 缺口补齐（AUDIT_GAP_VERIFICATION §4.4）。
		// 防御版策略，必须与前端既有能力兼容：
		//   - script-src 'unsafe-inline'：白标 custom_js 经 branding.tsx 动态内联注入（P1-5 产品功能，
		//     后端已限制仅超管可改），nonce 方案改动面大留后续版本；challenges.cloudflare.com 为 Turnstile
		//     人机验证脚本域（Client.tsx 动态加载）。
		//   - frame-src Turnstile widget 以 iframe 渲染；frame-ancestors 与 X-Frame-Options 同语义（防嵌他站）。
		//   - img-src data: https: http:：static_qr 收款码支持外链/内网图片（Billing.tsx <img>）。
		//   - connect-src ws:/wss:：/api/v1/ws/* 同源 WS 升级（旧浏览器 'self' 不含 ws 协议，显式放行）。
		//   - script-src https://res.wx.qq.com（§八-6 D 块，2026-09-18）：企微侧边栏 jweixin-1.6.0.js 按需注入
		//     （lib/wecomJsSdk.ts，仅顾问带 corpid 上下文时加载）；放行范围仅此主机，不放通配。
		c.Header("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'unsafe-inline' https://challenges.cloudflare.com https://res.wx.qq.com; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data: https: http:; "+
				"font-src 'self' data:; "+
				"connect-src 'self' ws: wss:; "+
				"frame-src https://challenges.cloudflare.com; "+
				"frame-ancestors 'self'; "+
				"object-src 'none'; base-uri 'self'; form-action 'self'")
		c.Next()
	})
	r.Use(middleware.TenantResolver())

	// 11. 注册路由
	registerRoutes(r)

	// 12. 启动服务（P1-4 修复(2026-09-09)：http.Server + signal.NotifyContext + Shutdown，
	// 退出序列：停接流 → UsageSink.Stop() 最终 flush（计量三桶与 usage_ledger 不丢账）→ mq.Close → 关连接池）
	log.Println("========================================")
	log.Printf("  服务启动成功！监听端口: %s", cfg.Server.Port)
	log.Println("  管理账号: admin / admin123")
	log.Println("  销售账号: sales1 / sales123")
	log.Printf("  后台管理: http://localhost:%s/admin", cfg.Server.Port)
	log.Println("========================================")

	srv := &http.Server{
		Addr:              ":" + cfg.Server.Port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		log.Println("[优雅停机] 收到退出信号，停止接收新请求（10s 宽限）...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("[优雅停机] HTTP 关闭异常: %v", err)
		}
		// P2-8(2026-09-20 批三)：HTTP 停后先排空合并队列——在途批次 5s 宽限自然收尾，
		// 残留 processing/simple 锁强制释放+代际递增（否则锁随进程死残留至 600s TTL，
		// 重启后该客户新消息干等锁过期，部署窗口=客户静默黑屏）。
		if service.DefaultMessageQueueService != nil {
			service.DefaultMessageQueueService.ShutdownDrain(5 * time.Second)
		}
		log.Println("[优雅停机] 执行计量最终 flush...")
		billing.DefaultUsageSink.Stop() // 最终 flush：三桶扣减与 usage_ledger 落账
		log.Println("[优雅停机] 关闭 MQ 消费者...")
		mq.Close()
		log.Println("[优雅停机] 关闭数据库连接池...")
		if sqlDB, err := db.DB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		log.Println("[优雅停机] 完成")
	}()

	// P2-4(2026-09-20 批三)：健康告警自检 push——旧 MaybeAlert 纯拉驱动（仅 /status handler 触发），
	// 无探活流量即永不通知。60s 周期 ComputeHealth+MaybeAlert；Redis 启用时锁选主防多实例刷群；
	// ctx 随停机信号收尾（metrics.StartAlertSelfCheck 内部 select ctx.Done）。
	metrics.StartAlertSelfCheck(ctx, 60*time.Second)

	err = srv.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		log.Fatalf("服务启动失败: %v", err)
	}
	log.Println("服务已退出")
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

	// ---- Status Page（商业化 M4，借鉴翻译助手三期§3.6）----
	// P2-4 拆分(2026-09-19 审计批三)：本端点公网免鉴权，旧版直出 readiness 全量清单
	// （pay_mode/AI_MOCK_MODE/registration_review/TRUSTED_PROXIES 实际取值）＝向攻击者
	// 公告哪些防线没开。公开面只留无敏感语义的存活/版本字段；全量体检挪 /status/detail。
	r.GET("/status", func(c *gin.Context) {
		// P1-4 健康探测：结构化探针 + 阈值告警（crit 越界经企微/钉钉主动通知，带冷却）。
		// 探测与告警仍在此执行——公开只裁剪响应体，不改探针行为。
		snap := metrics.ComputeHealth()
		metrics.MaybeAlert(snap)
		status := "ok"
		if snap.HasCrit {
			status = "crit"
		} else if snap.HasWarn {
			status = "warn"
		}
		c.JSON(200, gin.H{"code": 0, "data": gin.H{
			// 版本真源：与 README 更新日志主版本线保持一致，逐批手动 bump（此前 v2.3.0 系历史遗留未同步）
			"version":    "v2.16.0",
			"uptime_sec": int(time.Since(startTime).Seconds()),
			"status":     status,
			"ok":         snap.DBOK,
		}})
	})

	// /status/detail 全量健康+就绪清单：X-Health-Token（env HEALTH_TOKEN）守卫；
	// 未配置令牌=恒 403（fail-closed，与全站"未设置=严格态"口径一致）。
	r.GET("/status/detail", func(c *gin.Context) {
		token := config.GlobalConfig.Server.HealthToken
		if token == "" || c.GetHeader("X-Health-Token") != token {
			c.JSON(403, gin.H{"code": 403, "message": "需要 X-Health-Token"})
			return
		}
		snap := metrics.ComputeHealth()
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
		// 生产就绪探针（2026-09-15 价值批）：把 DEPLOY_CHECKLIST 里"忘开=烧钱/合规裸奔"
		// 类开关代码化逐项体检，只展示不群告警（配置红灯重启前会常亮，刷群无意义）
		readyChecks := metrics.ComputeReadiness()
		ready, rCrit, rWarn := metrics.ReadinessSummary(readyChecks)
		c.JSON(200, gin.H{"code": 0, "data": gin.H{
			"version":             "v2.16.0",
			"uptime_sec":          int(time.Since(startTime).Seconds()),
			"db_ok":               snap.DBOK,
			"redis_enabled":       redisclient.IsEnabled(),
			"replicas":            config.GlobalConfig.Server.Replicas,
			"critical_alerts_24h": crit24h,
			"status":              status,
			"ok":                  snap.DBOK,
			"ready":               ready,
			"readiness_crit":      rCrit,
			"readiness_warn":      rWarn,
			"readiness":           readyChecks,
		}})
	})

	// ---- 首页导航（根路径，给外部体验者选择入口）----
	// 修复：之前访问根路径返回404，外部用户不知道要加/client
	// 现在根路径展示三端导航页，点击即跳转
	// P-FE（2026-08-26）：React SPA 托管 —— frontend-react/dist
	// 历史路由单页应用：所有非 /api 路径回退到 index.html；
	// 静态资源（/assets/*）若存在则直接返回。旧的零构建 HTML 页已全部迁移至 React。
	// P2-9(2026-09-22)：仅压缩 SPA 静态资源（/assets/ 与根路径静态文件），绝不压缩 API/WS/SSE。
	r.Use(middleware.GzipStatic())
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
	} else {
		log.Println("[FE] 未找到 frontend-react/dist，跳过 SPA 托管（请先 cd frontend-react && npm run build）")
	}

	// ---- Prometheus 指标端点（P2 监控闭环，零依赖；文本格式由 metrics.RenderPrometheus 生成）----
	// P1-18 修复(2026-09-09)：原 /metrics 注册与指标中间件都在 dist 分支内——前端未构建时
	// 监控整体消失。上移为全局无条件注册。
	// 2026-09-09 鉴权加固：配置 METRICS_TOKEN 时须携带 Authorization: Bearer <token>；
	// 未配置时仅允许 loopback 访问（127.0.0.1/::1）——公网/容器外部无法读取内部指标，杜绝信息泄露。
	metricsToken := os.Getenv("METRICS_TOKEN")
	r.GET("/metrics", func(c *gin.Context) {
		if metricsToken != "" {
			auth := c.GetHeader("Authorization")
			if auth != "Bearer "+metricsToken {
				c.Status(http.StatusForbidden)
				return
			}
		} else {
			host := c.ClientIP()
			if host != "127.0.0.1" && host != "::1" && host != "::ffff:127.0.0.1" {
				c.Status(http.StatusForbidden)
				return
			}
		}
		c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		c.String(200, metrics.RenderPrometheus())
	})
	// 请求计数 + 延迟直方图（P1-2/P1-18：P99 来源）中间件——全局挂载（覆盖 /health /status /metrics 之外全部入口）
	r.Use(func(c *gin.Context) {
		start := time.Now()
		metrics.IncRequest()
		c.Next()
		metrics.RecordRequestLatency(time.Since(start))
	})

	// ---- 业务路由树（D2a 拆分至 internal/api/routes*.go，按作用域分文件）----
	api.RegisterRoutes(r)
}
