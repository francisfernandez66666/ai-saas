// Package gateway AI 网关独立服务：OpenAI 兼容 /v1/chat/completions + /v1/embeddings。
//
// 角色：平台级出网枢纽。本地/SaaS 实例不再直连厂商 Key，所有 reply/evals 等阶段请求
// 经此处转发。网关持有平台厂商 Key 并做：
//  1. 鉴权（HMAC 签名还原租户）
//  2. 余额 fail-closed（ConsumeAIQuota 原子闸，超额即拒）
//  3. 上游多模型降级（复用 ai.Router）
//  4. 计量落账（RecordUsage + DeductTokensActual 三桶扣减）
//  5. 素材采集（脱敏对话异步回流，供数据飞轮）
//
// 与本地实例职责切分：启用网关后，计费权上收网关，本地跳过自身的 ConsumeAIQuota/RecordUsage/DeductTokensActual。
package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"ai-scrm/config"
	"ai-scrm/internal/ai"
	"ai-scrm/internal/service"

	"github.com/gin-gonic/gin"
)

// Server AI 网关 HTTP 服务
type Server struct {
	engine *gin.Engine
	secret string // 共享密钥（与本地实例 LLM_GATEWAY_TOKEN 一致）
}

// NewServer 构建网关路由（鉴权 + 转发 + 计量）
func NewServer() *Server {
	gin.SetMode(config.GlobalConfig.Server.Mode)
	s := &Server{
		engine: gin.New(),
		secret: config.GlobalConfig.AI.GatewayToken,
	}
	s.engine.Use(gin.Recovery())
	s.engine.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "role": "ai-gateway"})
	})
	s.engine.POST("/v1/chat/completions", s.auth(), s.handleChatCompletions)
	s.engine.POST("/v1/embeddings", s.auth(), s.handleEmbeddings)
	return s
}

// Run 启动监听（阻塞）
func (s *Server) Run(addr string) error {
	log.Printf("[AI网关] 独立服务启动，监听 %s", addr)
	return s.engine.Run(addr)
}

// verifyToken 校验 <tenantID>.<sig> 签名，返回租户ID（0=平台内部）
func (s *Server) verifyToken(token string) (uint, bool) {
	if token == "" {
		return 0, true // 平台内部调用（无租户），透传放行
	}
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return 0, false
	}
	tenantID := uint(0)
	if _, err := fmt.Sscan(parts[0], &tenantID); err != nil || tenantID == 0 {
		return 0, false
	}
	mac := hmac.New(sha256.New, []byte(s.secret))
	mac.Write([]byte(parts[0]))
	expect := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expect), []byte(parts[1])) {
		return 0, false
	}
	return tenantID, true
}

// auth 网关鉴权中间件：从 Authorization: Bearer <tenantID>.<sig> 还原租户
func (s *Server) auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		auth := c.GetHeader("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")
		if s.secret == "" {
			// 未配置共享密钥：拒绝（fail-closed，避免无鉴权放通）
			c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"message": "网关未配置共享密钥"}})
			c.Abort()
			return
		}
		tenantID, ok := s.verifyToken(token)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"message": "鉴权失败"}})
			c.Abort()
			return
		}
		c.Set("tenant_id", tenantID)
		c.Next()
	}
}

// chatRequest OpenAI 兼容请求体
type chatRequest struct {
	Model       string           `json:"model"`
	Messages    []ai.ChatMessage `json:"messages"`
	Temperature float64          `json:"temperature"`
	Stream      bool             `json:"stream"`
}

// handleChatCompletions 转发对话请求，网关侧做 fail-closed 计量
func (s *Server) handleChatCompletions(c *gin.Context) {
	tenantID := c.GetUint("tenant_id")
	stage := c.GetHeader("X-Stage")
	if stage == "" {
		stage = "reply"
	}

	var req chatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "请求体解析失败: " + err.Error()}})
		return
	}
	if len(req.Messages) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "messages 不能为空"}})
		return
	}
	temp := req.Temperature
	if temp == 0 {
		temp = 0.7
	}

	// 1. fail-closed 计量闸：超额直接拒绝，本地实例不再重复判定
	if !service.ConsumeAIQuota(tenantID) {
		c.JSON(http.StatusForbidden, gin.H{
			"error": gin.H{"message": "AI 配额已用尽，请升级套餐或联系平台"},
			"code":  "quota_exhausted",
		})
		return
	}
	// 1b. Token 三桶前置检查（防零余额仍消耗厂商额度）：与本地 chat_reply 同口径
	if !service.CheckTokenAvailability(tenantID) {
		c.JSON(http.StatusForbidden, gin.H{
			"error": gin.H{"message": "AI 额度余额不足，请充值或升级套餐"},
			"code":  "token_insufficient",
		})
		return
	}

	// 2. 上游多模型降级生成（网关持有平台厂商 Key）
	start := time.Now()
	reply, provider, modelName, usage, err := ai.Router.GenerateTextForStage(stage, tenantID, req.Messages, temp)
	if err != nil || reply == "" {
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{"message": "上游模型全部不可用: " + errStr(err)},
			"code":  "upstream_unavailable",
		})
		return
	}

	// 3. 计量落账 + 三桶扣减（与本地同链路，但此处为唯一计量方）
	service.RecordUsage(tenantID, 0, 0, stage, provider, modelName,
		usage.PromptTokens, usage.CompletionTokens, time.Since(start).Milliseconds())
	go service.DeductTokensActual(tenantID, int64(usage.TotalTokens))

	// 4. 素材采集（脱敏对话异步回流，供数据飞轮；未配置 Collector.URL 自动跳过）
	go s.collectMaterial(tenantID, stage, req.Messages, reply)

	// 5. OpenAI 兼容响应
	c.JSON(http.StatusOK, gin.H{
		"id":      "gw-" + time.Now().Format("20060102150405"),
		"object":  "chat.completion",
		"model":   modelName,
		"choices": []gin.H{{"message": gin.H{"role": "assistant", "content": reply}, "finish_reason": "stop"}},
		"usage": gin.H{
			"prompt_tokens":     usage.PromptTokens,
			"completion_tokens": usage.CompletionTokens,
			"total_tokens":      usage.TotalTokens,
		},
	})
}

// embeddingRequest / embeddingResponse OpenAI 兼容向量化
type embeddingRequest struct {
	Model string      `json:"model"`
	Input interface{} `json:"input"` // string 或 []string
}

// embeddingResponse OpenAI 兼容向量化响应结构（data/model/usage）
type embeddingResponse struct {
	Data  []gin.H `json:"data"`
	Model string  `json:"model"`
	Usage gin.H   `json:"usage"`
}

// handleEmbeddings 向量化端点（复用平台 Embedding 客户端）
func (s *Server) handleEmbeddings(c *gin.Context) {
	tenantID := c.GetUint("tenant_id")
	var req embeddingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "请求体解析失败: " + err.Error()}})
		return
	}
	if service.DefaultEmbeddingClient == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": gin.H{"message": "网关未配置 Embedding 端点"}})
		return
	}
	// 归集输入文本
	texts := []string{}
	switch v := req.Input.(type) {
	case string:
		if v != "" {
			texts = append(texts, v)
		}
	case []interface{}:
		for _, it := range v {
			if s, ok := it.(string); ok && s != "" {
				texts = append(texts, s)
			}
		}
	}
	if len(texts) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "input 不能为空"}})
		return
	}

	// 计费闸（向量化也计入配额）
	if !service.ConsumeAIQuota(tenantID) {
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"message": "AI 配额已用尽"}, "code": "quota_exhausted"})
		return
	}

	data := make([]gin.H, 0, len(texts))
	totalTok := 0
	for i, t := range texts {
		vec := service.DefaultEmbeddingClient.Embed(t)
		totalTok += len([]rune(t)) / 2 // 粗略估算 token
		data = append(data, gin.H{"object": "embedding", "index": i, "embedding": vec})
	}
	service.RecordUsage(tenantID, 0, 0, "embedding", "embedding", req.Model, totalTok, 0, 0)
	go service.DeductTokensActual(tenantID, int64(totalTok))

	c.JSON(http.StatusOK, embeddingResponse{
		Data:  data,
		Model: req.Model,
		Usage: gin.H{"prompt_tokens": totalTok, "total_tokens": totalTok},
	})
}

// collectMaterial 脱敏对话异步回流（数据飞轮素材采集）；未配置 Collector.URL 跳过
// 统一走 service.Collect（与 CDP/审计同一缓冲器，自动脱敏+周期上报）
func (s *Server) collectMaterial(tenantID uint, stage string, messages []ai.ChatMessage, reply string) {
	// 仅回流最后一条用户消息与 AI 回复（脱敏在 Collect 内完成；messages 结构体不入匿名化递归，仅取长度）
	lastUser := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			lastUser = messages[i].Content
			break
		}
	}
	service.Collect("material", tenantID, map[string]any{
		"stage":        stage,
		"user_message": lastUser,
		"reply":        reply,
		"turns":        len(messages),
	})
}

// errStr 错误转字符串（nil 安全）
func errStr(err error) string {
	if err == nil {
		return "未知错误"
	}
	return err.Error()
}
