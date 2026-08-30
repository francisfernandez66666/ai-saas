// AI 网关独立服务入口：平台级出网枢纽，集中持有厂商 Key 与计费闸门。
// 与 cmd/server 共享同一数据库与配置；本地/SaaS 实例通过 LLM_GATEWAY_URL 指向本服务。
package main

import (
	"ai-scrm/config"
	"ai-scrm/internal/ai"
	"ai-scrm/internal/db"
	"ai-scrm/internal/gateway"
	"ai-scrm/internal/redisclient"
	"ai-scrm/internal/service"
	"log"

	"github.com/joho/godotenv"
)

func main() {
	log.Println("========================================")
	log.Println("  AI 网关独立服务 启动中...")
	log.Println("========================================")

	// 1. 加载环境变量
	if err := godotenv.Load(); err != nil {
		log.Println("未找到 .env 文件，使用默认/环境变量配置")
	}

	// 2. 配置
	cfg := config.LoadConfig()
	addr := cfg.AI.GatewayListen
	if addr == "" {
		addr = ":9091"
	}
	if cfg.AI.GatewayToken == "" {
		log.Fatalf("[AI网关] 未配置 LLM_GATEWAY_TOKEN（共享密钥），拒绝启动（fail-closed）")
	}
	log.Printf("配置加载完成，网关监听: %s", addr)

	// 3. Redis（多实例网关选主/锁，可选）
	redisclient.Init(cfg.Redis)

	// 4. 数据库（与平台共享同一 PG）
	if err := db.Init(); err != nil {
		log.Fatalf("数据库初始化失败: %v", err)
	}

	// 5. 系统配置服务（计费开关/灰度读取）
	service.InitSystemConfigService()

	// 6. AI 客户端（平台厂商 Key，网关自身出网用）
	ai.InitClient()
	ai.InitSiliconFlowClient()
	ai.InitRouter()
	// 网关自身不可再转发到另一网关（防环）：强制清空网关客户端
	ai.DefaultGatewayClient = nil
	service.InitEmbeddingClient()

	log.Println("[AI网关] 上游模型与计量就绪")

	// 7. 启动网关 HTTP 服务（阻塞）
	srv := gateway.NewServer()
	if err := srv.Run(addr); err != nil {
		log.Fatalf("AI 网关启动失败: %v", err)
	}
}
