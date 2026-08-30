package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
)

// ============================================================
// 配置中心 - 所有可配置参数集中管理
// 包含：Θ超参数、数据库配置、AI配置、服务配置
// 为什么集中管理？策略中心是黑盒，参数调优是核心工作
// ============================================================

// Config 全局配置结构体
type Config struct {
	Server     ServerConfig     // 服务配置
	Database   DatabaseConfig   // 数据库配置
	Redis      RedisConfig      // Redis 配置（多实例部署必需：分布式锁/延迟队列/缓存失效）
	MQ         MQConfig         // 消息中心配置（Kafka 异步事件流转）
	AI         AIConfig         // AI配置
	Strategy   StrategyConfig   // 策略中心超参数Θ
	WorkTime   WorkTimeConfig   // 工作时间配置
	ReplySpeed ReplySpeedConfig // 回复速度配置（模拟人工）
	JWT        JWTConfig        // JWT配置
	Collector  CollectorConfig  // 数据飞轮聚合上报（空=关闭）
}

// CollectorConfig 数据飞轮批量上报配置（P2 collector）
// URL 为空时整体关闭（不落任何外部请求）；Key 用于上报鉴权 / 接收端校验
type CollectorConfig struct {
	URL string // COLLECTOR_URL：聚合上报端点（HTTPS）
	Key string // COLLECTOR_KEY：上报/接收鉴权令牌
}

// MQConfig 消息中心配置（SAAS_PLAN §2.5）
// Kafka 只做异步事件流转，绝不承担同步查询；禁止 Request-Reply
// Type=log 时降级打结构化日志（默认），业务代码零改动即可切换 kafka
type MQConfig struct {
	Type        string   // MQ_TYPE: log / kafka
	Brokers     []string // KAFKA_BROKERS，逗号分隔
	TopicPrefix string   // KAFKA_TOPIC_PREFIX，多环境隔离，默认 ai-scrm.
}

// RedisConfig Redis 配置
// 多实例部署的协调层：消息合并队列互斥锁、缓存跨实例失效、单例任务选主
// Enabled=false 时退化为纯内存模式（单实例部署可用，行为与改造前一致）
type RedisConfig struct {
	Enabled  bool   // 是否启用（REDIS_ENABLED，默认 false=内存模式）
	Addr     string // 地址（REDIS_ADDR，默认 localhost:6379）
	Password string // 密码（REDIS_PASSWORD）
	DB       int    // 库号（REDIS_DB，默认 0）
}

// ServerConfig 服务配置
type ServerConfig struct {
	Port string // 服务端口
	Mode string // gin模式: debug/release
}

// DatabaseConfig 数据库配置（PostgreSQL）
// SaaS 化改造：由 SQLite 单文件切换为 PostgreSQL 连接串 + 连接池
type DatabaseConfig struct {
	Host            string // 数据库主机
	Port            int    // 数据库端口，默认 5432
	User            string // 数据库用户
	Password        string // 数据库密码
	Name            string // 数据库名
	SSLMode         string // SSL模式: disable/require，默认 disable
	MaxOpenConns    int    // 最大连接数，默认 25
	MaxIdleConns    int    // 最大空闲连接数，默认 10
	ConnMaxLifetime int    // 单连接最大存活时间（秒），默认 300
}

// DSN 组装 PostgreSQL 连接串
func (d DatabaseConfig) DSN() string {
	if d.SSLMode == "" {
		d.SSLMode = "disable"
	}
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		d.Host, d.Port, d.User, d.Password, d.Name, d.SSLMode)
}

// ZhipuConfig 智谱GLM配置
// 为什么单独拆成Zhipu struct？未来可能接入多家AI，按厂商隔离更清晰
type ZhipuConfig struct {
	APIKey      string  // 智谱API Key
	BaseURL     string  // API基础URL
	Model       string  // 主模型名称，默认 glm-4.7-flash
	ModelBackup string  // 备用模型名称，默认 glm-4-flash（同平台降级）
	MaxTokens   int     // 最大输出token数，控制回复长度
	Temperature float64 // 采样温度，0-1之间，越大越随机
}

// SiliconFlowConfig 硅基流动配置（OpenAI兼容格式）
// 作为跨平台备用模型，智谱全挂了再切到这里
type SiliconFlowConfig struct {
	APIKey      string  // 硅基流动API Key
	BaseURL     string  // API基础URL，默认 https://api.siliconflow.cn/v1
	Model       string  // 主模型名称，默认 deepseek-ai/DeepSeek-V4-Flash
	ModelBackup string  // 备用模型名称（9B以下永久免费，兜底用）
	MaxTokens   int     // 最大输出token数
	Temperature float64 // 采样温度
}

// AIConfig AI模型配置
type AIConfig struct {
	APIKey      string            // 智谱API Key（兼容旧字段）
	BaseURL     string            // API基础URL（兼容旧字段）
	ModelName   string            // 模型名称（兼容旧字段）
	MockMode    bool              // 是否模拟模式（不调用真实AI）
	Zhipu       ZhipuConfig       // 智谱GLM详细配置
	SiliconFlow SiliconFlowConfig // 硅基流动配置（跨平台备用）

	// ---- AI 网关（云端枢纽）：本地部署/SaaS实例统一转发，持有平台厂商Key ----
	// 启用后本进程不直接持有厂商Key，所有reply阶段请求经网关转发（持有平台Key出网）
	// 网关做鉴权+余额fail-closed+上游多模型降级；本地仍按租户三桶计费（计费权在租户侧）
	GatewayURL    string // AI网关地址（OpenAI兼容 /v1/chat/completions），非空即启用网关转发
	GatewayToken  string // 网关共享密钥（HMAC 签名/验签），本地实例与网关服务端须一致
	GatewayModel  string // 网关默认模型名（缺省由网关侧决定）
	GatewayListen string // AI网关独立服务监听地址（如 :9091）；非空即在本进程内嵌启动网关（亦可独立 cmd/gateway 部署）

	// ---- 向量检索 Embedding（P0-4 双层KB）：配置即点亮，未配置回退关键词 ----
	EmbeddingURL   string // Embedding 端点（OpenAI兼容 /v1/embeddings），非空启用向量检索
	EmbeddingKey   string // Embedding 端点鉴权（Bearer）
	EmbeddingModel string // Embedding 模型名（缺省 text-embedding-3-small）
	EmbeddingDim   int    // 向量维度（pgvector 列定长，须与 EmbeddingModel 输出维度一致，缺省1536）

	// ---- 模拟真人打字 ----
	// 为什么需要？AI秒回太机械，模拟打字延迟更像真人
	SimulateTyping bool    // 是否开启模拟打字延迟
	TypingSpeed    float64 // 打字速度（字/秒），默认4字/秒
	TypingMinDelay float64 // 最小延迟（秒），再短的回复也至少等这么久
	TypingMaxDelay float64 // 最大延迟（秒），避免长回复等太久
	TypingJitter   float64 // 随机抖动比例（0-0.5），每次速度有波动更自然
}

// StrategyConfig 策略中心超参数Θ
// 这些是策略中心引擎的核心调优参数
// 所有阈值都可以在这里调整，无需改代码
type StrategyConfig struct {
	// ---- Step2: softmax温度系数 ----
	Tau float64 // τ=0.8，温度系数，越小越锐利，越大越平滑

	// ---- Step3: 软降级相关阈值 ----
	ThetaConf        float64 // θ_conf=0.5，置信度阈值
	SimThresh        float64 // sim_thresh=0.55，话术相似度阈值
	ThetaHookRateLow float64 // θ_hookrate_low=0.4，低接钩率阈值（触发软降级）
	ThetaSilent      int     // θ_silent=60，沉默时长阈值（秒，触发软降级）

	// ---- Step6: 路由决策阈值 ----
	ThetaTrust        float64 // θ_trust=0.3，信任度阈值（低于则转人工）
	ThetaRounds       int     // θ_rounds=3，回合数阈值（达到后检查接钩率）
	ThetaHookRateCrit float64 // θ_hookrate_crit=0.2，危急接钩率阈值（低于则转人工）

	// ---- L3强制切人阈值 ----
	ThetaL3Intent float64 // θ_L3_intent=0.8，L3意向分阈值
	ThetaL3Rounds int     // θ_L3_rounds=2，高意向持续轮数阈值

	// ---- Step5: 紧迫等级阈值 ----
	ThetaUrgencyL1 float64 // θ_urgency_L1=0.5，L1紧迫阈值
	ThetaUrgencyL2 float64 // θ_urgency_L2=0.7，L2紧迫阈值

	// ---- 人工超时接管 ----
	HumanTimeoutSeconds int // 人工回复超时时间（秒），超时后AI接管
}

// ============================================================
// 工作时间配置
// 用于模拟人工回复速度、判断上下班时间
// ============================================================

// WorkTimeConfig 工作时间配置
type WorkTimeConfig struct {
	WorkStartHour int    // 上班时间（小时，24小时制），默认9
	WorkEndHour   int    // 下班时间（小时，24小时制），默认18
	WorkDays      string // 工作日，默认"1,2,3,4,5"（周一到周五，周日=0）
}

// ============================================================
// 回复速度配置（秒）
// 模拟人工回复的延迟时间，让AI回复更像真人
// 根据问题难度（简单/中等/复杂）和紧迫等级（L1/L2/L3）分级
// ============================================================

// ReplySpeedConfig 回复速度配置（单位：秒，[最小值, 最大值]区间内随机）
type ReplySpeedConfig struct {
	ReplyMinDelay      int // 最短回复延迟，默认15
	MergeWindowSeconds int // 消息合并窗口（秒），默认45秒内的连续消息合并
	MaxMergeMessages   int // 单次最多合并几条消息，默认3
	// L3：高紧迫（高意向客户，回复快）
	ReplySimpleL3  []int // L3简单问题 [15, 60]
	ReplyMediumL3  []int // L3中等问题 [60, 180]
	ReplyComplexL3 []int // L3复杂问题 [180, 480]
	// L2：中紧迫
	ReplySimpleL2  []int // L2简单问题 [30, 90]
	ReplyMediumL2  []int // L2中等问题 [120, 300]
	ReplyComplexL2 []int // L2复杂问题 [300, 720]
	// L1：低紧迫（低意向客户，回复慢）
	ReplySimpleL1  []int // L1简单问题 [60, 120]
	ReplyMediumL1  []int // L1中等问题 [300, 600]
	ReplyComplexL1 []int // L1复杂问题 [600, 1200]
}

// JWTConfig JWT认证配置
type JWTConfig struct {
	Secret      string // 密钥
	ExpireHours int    // 过期时间（小时）
}

// GlobalConfig 全局配置实例
var GlobalConfig *Config

// LoadConfig 加载配置
// 优先级：环境变量 > 默认值
// 为什么这样设计？方便部署时通过.env文件调整参数，不用改代码重新编译
func LoadConfig() *Config {
	GlobalConfig = &Config{
		Server: ServerConfig{
			Port: getEnv("SERVER_PORT", "8080"),
			Mode: getEnv("GIN_MODE", "debug"),
		},
		Database: DatabaseConfig{
			Host:            getEnv("DB_HOST", "localhost"),
			Port:            getEnvInt("DB_PORT", 5432),
			User:            getEnv("DB_USER", "ai_scrm"),
			Password:        getEnv("DB_PASSWORD", "change_me"),
			Name:            getEnv("DB_NAME", "ai_scrm"),
			SSLMode:         getEnv("DB_SSLMODE", "disable"),
			MaxOpenConns:    getEnvInt("DB_MAX_OPEN_CONNS", 25),
			MaxIdleConns:    getEnvInt("DB_MAX_IDLE_CONNS", 10),
			ConnMaxLifetime: getEnvInt("DB_CONN_MAX_LIFETIME", 300),
		},
		MQ: MQConfig{
			Type:        getEnv("MQ_TYPE", "log"),
			Brokers:     getEnvSlice("KAFKA_BROKERS", ",", []string{"localhost:9092"}),
			TopicPrefix: getEnv("KAFKA_TOPIC_PREFIX", "ai-scrm."),
		},
		Redis: RedisConfig{
			Enabled:  getEnvBool("REDIS_ENABLED", false), // 默认关闭：单实例内存模式
			Addr:     getEnv("REDIS_ADDR", "localhost:6379"),
			Password: getEnv("REDIS_PASSWORD", ""),
			DB:       getEnvInt("REDIS_DB", 0),
		},
		AI: AIConfig{
			APIKey:    getEnv("GLM_API_KEY", ""),
			BaseURL:   getEnv("GLM_BASE_URL", "https://open.bigmodel.cn/api/paas/v4"),
			ModelName: getEnv("GLM_MODEL", "glm-4-flash"),
			MockMode:  getEnvBool("AI_MOCK_MODE", true), // 默认模拟模式，方便先跑通流程
			Zhipu: ZhipuConfig{
				APIKey:      getEnv("ZHIPU_API_KEY", getEnv("GLM_API_KEY", "")), // 兼容GLM_API_KEY
				BaseURL:     getEnv("ZHIPU_BASE_URL", "https://open.bigmodel.cn/api/paas/v4"),
				Model:       getEnv("ZHIPU_MODEL", "glm-4.7-flash"),
				ModelBackup: getEnv("ZHIPU_MODEL_BACKUP", "glm-4-flash"),
				MaxTokens:   getEnvInt("ZHIPU_MAX_TOKENS", 1024),   // 1024足够（约700-800字回复）
				Temperature: getEnvFloat("ZHIPU_TEMPERATURE", 0.7), // 默认0.7，平衡创意和稳定
			},
			SiliconFlow: SiliconFlowConfig{
				APIKey:      getEnv("SILICONFLOW_API_KEY", ""),
				BaseURL:     getEnv("SILICONFLOW_BASE_URL", "https://api.siliconflow.cn/v1"),
				Model:       getEnv("SILICONFLOW_MODEL", "THUDM/GLM-4-9B-0414"),
				ModelBackup: getEnv("SILICONFLOW_MODEL_BACKUP", "deepseek-ai/DeepSeek-V4-Flash"),
				MaxTokens:   getEnvInt("SILICONFLOW_MAX_TOKENS", 1024),
				Temperature: getEnvFloat("SILICONFLOW_TEMPERATURE", 0.7),
			},
			// AI网关（云端枢纽）：本地部署无厂商Key时统一转发
			GatewayURL:    getEnv("LLM_GATEWAY_URL", ""),
			GatewayToken:  getEnv("LLM_GATEWAY_TOKEN", ""),
			GatewayModel:  getEnv("LLM_GATEWAY_MODEL", ""),
			GatewayListen: getEnv("LLM_GATEWAY_LISTEN", ""),
			// 向量检索 Embedding
			EmbeddingURL:   getEnv("EMBEDDING_API_URL", ""),
			EmbeddingKey:   getEnv("EMBEDDING_API_KEY", ""),
			EmbeddingModel: getEnv("EMBEDDING_MODEL", "text-embedding-3-small"),
			EmbeddingDim:   getEnvInt("EMBEDDING_DIM", 1536), // pgvector 列定长维度
			// 模拟真人打字配置
			SimulateTyping: getEnvBool("AI_SIMULATE_TYPING", true),  // 默认开启，更像真人
			TypingSpeed:    getEnvFloat("AI_TYPING_SPEED", 4.0),     // 4字/秒，接近真人打字速度
			TypingMinDelay: getEnvFloat("AI_TYPING_MIN_DELAY", 0.8), // 最少等0.8秒
			TypingMaxDelay: getEnvFloat("AI_TYPING_MAX_DELAY", 4.0), // 最多等4秒，别让用户等太久
			TypingJitter:   getEnvFloat("AI_TYPING_JITTER", 0.2),    // 20%随机波动
		},
		Strategy: StrategyConfig{
			// Step2: softmax温度
			Tau: getEnvFloat("TAU", 0.8),

			// Step3: 软降级
			ThetaConf:        getEnvFloat("THETA_CONF", 0.5),
			SimThresh:        getEnvFloat("SIM_THRESH", 0.55),
			ThetaHookRateLow: getEnvFloat("THETA_HOOKRATE_LOW", 0.4),
			ThetaSilent:      getEnvInt("THETA_SILENT", 60),

			// Step6: 路由决策
			ThetaTrust:        getEnvFloat("THETA_TRUST", 0.3),
			ThetaRounds:       getEnvInt("THETA_ROUNDS", 3),
			ThetaHookRateCrit: getEnvFloat("THETA_HOOKRATE_CRIT", 0.2),

			// L3强制切人
			ThetaL3Intent: getEnvFloat("THETA_L3_INTENT", 0.8),
			ThetaL3Rounds: getEnvInt("THETA_L3_ROUNDS", 2),

			// 紧迫等级
			ThetaUrgencyL1: getEnvFloat("THETA_URGENCY_L1", 0.5),
			ThetaUrgencyL2: getEnvFloat("THETA_URGENCY_L2", 0.7),

			// 人工超时接管
			HumanTimeoutSeconds: getEnvInt("HUMAN_TIMEOUT_SECONDS", 180), // 3分钟
		},
		// 工作时间配置
		WorkTime: WorkTimeConfig{
			WorkStartHour: getEnvInt("WORK_START_HOUR", 9),  // 早上9点上班
			WorkEndHour:   getEnvInt("WORK_END_HOUR", 18),   // 下午6点下班
			WorkDays:      getEnv("WORK_DAYS", "1,2,3,4,5"), // 周一到周五（周日=0）
		},
		// 回复速度配置（模拟人工回复延迟，单位：秒）
		// 优化：整体大幅下调，避免寒暄等1分钟、复杂问题等20分钟
		ReplySpeed: ReplySpeedConfig{
			ReplyMinDelay:      getEnvInt("REPLY_MIN_DELAY", 3),       // 最短回复延迟（原来15秒太慢）
			MergeWindowSeconds: getEnvInt("MERGE_WINDOW_SECONDS", 45), // 消息合并窗口45秒
			MaxMergeMessages:   getEnvInt("MAX_MERGE_MESSAGES", 3),    // 单次最多合并3条
			// L3：高紧迫，回复快（意向客户秒回）
			ReplySimpleL3:  getEnvIntSlice("REPLY_SIMPLE_L3", []int{3, 8}),
			ReplyMediumL3:  getEnvIntSlice("REPLY_MEDIUM_L3", []int{8, 20}),
			ReplyComplexL3: getEnvIntSlice("REPLY_COMPLEX_L3", []int{20, 60}),
			// L2：中紧迫
			ReplySimpleL2:  getEnvIntSlice("REPLY_SIMPLE_L2", []int{5, 15}),
			ReplyMediumL2:  getEnvIntSlice("REPLY_MEDIUM_L2", []int{15, 40}),
			ReplyComplexL2: getEnvIntSlice("REPLY_COMPLEX_L2", []int{40, 90}),
			// L1：低紧迫，回复慢（但也不能太离谱）
			ReplySimpleL1:  getEnvIntSlice("REPLY_SIMPLE_L1", []int{5, 15}),
			ReplyMediumL1:  getEnvIntSlice("REPLY_MEDIUM_L1", []int{20, 60}),
			ReplyComplexL1: getEnvIntSlice("REPLY_COMPLEX_L1", []int{60, 120}),
		},
		JWT: JWTConfig{
			Secret:      getEnv("JWT_SECRET", "change_me_jwt_secret"),
			ExpireHours: getEnvInt("JWT_EXPIRE_HOURS", 24),
		},
		Collector: CollectorConfig{
			URL: getEnv("COLLECTOR_URL", ""),
			Key: getEnv("COLLECTOR_KEY", ""),
		},
	}

	// 安全底线（C1）：非 debug 环境下禁止保留默认/空 JWT 密钥，否则任何人可伪造 super_admin 令牌接管平台
	if GlobalConfig.Server.Mode != "debug" {
		if GlobalConfig.JWT.Secret == "" || GlobalConfig.JWT.Secret == "ai-scrm-secret-key-change-in-production" {
			log.Fatalf("[安全] 非 debug 环境下 JWT_SECRET 未配置或仍为默认值，拒绝启动；请在 .env / 部署环境变量中设置强随机密钥")
		}
	}
	// 安全警告（C2）：生产环境若仍走模拟模式将不会调用真实 AI
	if GlobalConfig.Server.Mode == "release" && GlobalConfig.AI.MockMode {
		log.Printf("[安全警告] 生产环境(GIN_MODE=release)下 AI_MOCK_MODE=true，将不会调用真实 AI，请确认配置")
	}

	return GlobalConfig
}

// ============================================================
// 以下是辅助函数，从环境变量读取配置，带默认值
// ============================================================

func getEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

// getEnvInt 从环境变量读取 int，转换失败或缺失时返回默认值
func getEnvInt(key string, defaultValue int) int {
	if value, exists := os.LookupEnv(key); exists {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

// getEnvFloat 从环境变量读取 float64，转换失败或缺失时返回默认值
func getEnvFloat(key string, defaultValue float64) float64 {
	if value, exists := os.LookupEnv(key); exists {
		if floatValue, err := strconv.ParseFloat(value, 64); err == nil {
			return floatValue
		}
	}
	return defaultValue
}

// getEnvBool 从环境变量读取 bool，转换失败或缺失时返回默认值
func getEnvBool(key string, defaultValue bool) bool {
	if value, exists := os.LookupEnv(key); exists {
		if boolValue, err := strconv.ParseBool(value); err == nil {
			return boolValue
		}
	}
	return defaultValue
}

// getEnvSlice 从环境变量读取字符串数组（sep 分隔）
func getEnvSlice(key string, sep string, defaultValue []string) []string {
	if value, exists := os.LookupEnv(key); exists && strings.TrimSpace(value) != "" {
		parts := strings.Split(value, sep)
		result := make([]string, 0, len(parts))
		for _, p := range parts {
			if s := strings.TrimSpace(p); s != "" {
				result = append(result, s)
			}
		}
		if len(result) > 0 {
			return result
		}
	}
	return defaultValue
}

// getEnvIntSlice 从环境变量读取int数组（逗号分隔，如"15,60"）
func getEnvIntSlice(key string, defaultValue []int) []int {
	if value, exists := os.LookupEnv(key); exists {
		parts := strings.Split(value, ",")
		result := make([]int, 0, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if intValue, err := strconv.Atoi(part); err == nil {
				result = append(result, intValue)
			}
		}
		if len(result) >= 2 {
			return result
		}
	}
	return defaultValue
}
