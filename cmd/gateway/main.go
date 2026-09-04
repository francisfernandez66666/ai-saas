// AI 网关独立部署入口（P0-1）：将 internal/gateway 作为独立进程启动，
// 与业务实例（cmd/server）解耦，支持网关水平扩容、独立健康探针、故障不影响业务进程。
// 业务实例配 LLM_GATEWAY_URL 指向本进程即可把出网与计费上收网关。
package main

import (
	"ai-scrm/config"
	"ai-scrm/internal/ai"
	"ai-scrm/internal/cache"
	"ai-scrm/internal/db"
	"ai-scrm/internal/gateway"
	"ai-scrm/internal/redisclient"
	"ai-scrm/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"log"
)

// main 网关进程入口：仅初始化网关链路依赖（DB/缓存/系统配置/AI路由/Embedding/RLS/采集器）
// 不加载业务路由、不跑 seed、不启动消息消费者与编排层——网关只负责出网转发与计量。
func main() {
	log.Println("========================================")
	log.Println("  AI 网关独立服务 启动中...")
	log.Println("========================================")

	if err := godotenv.Load(); err != nil {
		log.Println("未找到.env文件，使用默认配置")
	}

	cfg := config.LoadConfig()
	gin.SetMode(cfg.Server.Mode)

	// 1. Redis（分布式锁/看门狗选主；计费闸 CheckTokenAvailability 已收敛到 token 三桶，不依赖 Redis）
	redisclient.Init(cfg.Redis)

	// 2. 数据库（计量/配额/usage 落账所需表；AutoMigrate 幂等）
	if err := db.Init(); err != nil {
		log.Fatalf("数据库初始化失败: %v", err)
	}

	// 3. 缓存（标签/知识，供上游降级路由可能命中的）
	cache.InitTagCache()
	cache.InitKnowledgeCache()

	// 4. 系统配置（配额阈值、计费开关等热参）
	service.InitSystemConfigService()

	// 4.1 实时计量批量落库（P1 计费统一 2026-09-03：网关侧三桶扣减收敛到 UsageSink）
	service.InitUsageSink()

	// 5. AI 客户端与路由（网关持有平台厂商 Key，负责多模型降级出网）
	ai.InitClient()
	ai.InitSiliconFlowClient()
	ai.InitRouter()
	service.InitEmbeddingClient()

	// 6. RLS 休眠式兜底（与本进程无强依赖，但保持与业务实例一致的安全姿态）
	service.EnableRLS()

	// 7. 数据飞轮采集器（素材回流；COLLECTOR_URL 空则空转）
	service.StartCollector()

	// 8. 启动网关（GatewayListen 必须非空，否则无意义）
	if cfg.AI.GatewayListen == "" {
		log.Fatalf("未配置 LLM_GATEWAY_LISTEN，网关无法监听；请在 .env 中设置（如 :9091）")
	}
	srv := gateway.NewServer()
	if err := srv.Run(cfg.AI.GatewayListen); err != nil {
		log.Fatalf("AI 网关启动失败: %v", err)
	}
}
