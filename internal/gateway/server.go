// Package gateway AI 网关独立服务：OpenAI 兼容 /v1/chat/completions + /v1/embeddings。
//
// 角色：平台级出网枢纽。本地/SaaS 实例不再直连厂商 Key，所有 reply/evals 等阶段请求
// 经此处转发。网关持有平台厂商 Key 并做：
//  1. 鉴权（HMAC 签名还原租户）
//  2. 余额 fail-closed（CheckTokenAvailability 三桶前置闸，超额即拒）
//  3. 上游多模型降级（复用 ai.Router）
//  4. 计量落账（RecordUsage + SinkRecordUsage → UsageSink 三桶扣减）
//  5. 素材采集（脱敏对话异步回流，供数据飞轮）
//
// 与本地实例职责切分：启用网关后，计费权上收网关，本地跳过自身的计量/扣减。
// （2026-09-03 计费统一：ConsumeAIQuota 已降级为统计旁路恒 true，不再作为拦截闸。）
package gateway

import "ai-scrm/internal/billing"

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"ai-scrm/config"
	"ai-scrm/internal/ai"
	"ai-scrm/internal/logx"
	"ai-scrm/internal/service"
	"ai-scrm/internal/telemetry"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
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

// verifyToken 校验 <tenantID>.<ts>.<sig> 签名，返回租户ID
// P0-5 修复(2026-09-09)：空 token 与 tenantID==0 一律拒绝——此前"空=平台内部透传放行"
// 导致任何能访问网关端口的人以 tenant_id=0 无限量白嫖平台厂商 Key（tenant 0 计费全 no-op）。
// 网关只接受真实租户签名令牌；平台内部调用不经 HTTP（内嵌网关在进程内直用 ai.Router），无需透传身份。
// P1-40 修复(2026-09-09)：token 三段式 <tenantID>.<ts>.<sig>；ts 为 Unix 秒，
// 与网关时间差 >5min 视为重放/过期拒绝。sig=HMAC(secret, "tenantID.ts")，防篡改防重放。
func (s *Server) verifyToken(token string) (uint, bool) {
	if token == "" {
		return 0, false // 空 token 拒绝（fail-closed）
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return 0, false
	}
	tenantID := uint(0)
	if _, err := fmt.Sscan(parts[0], &tenantID); err != nil || tenantID == 0 {
		return 0, false // 拒绝平台伪身份（tenant 0 计费恒放行）
	}
	var ts int64
	if _, err := fmt.Sscan(parts[1], &ts); err != nil || ts <= 0 {
		return 0, false
	}
	if diff := time.Now().Unix() - ts; diff > 300 || diff < -300 {
		log.Printf("[AI网关] 鉴权失败 tenant=%d ts=%d 超出 ±5min 窗口（防重放）", tenantID, ts)
		return 0, false
	}
	mac := hmac.New(sha256.New, []byte(s.secret))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	expect := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expect), []byte(parts[2])) {
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
// P1-40 修复：Temperature 改 *float64——区分"客户端省略"（nil→默认 0.7）与"显式传 0"（透传 0，
// 贪婪采样可表达）。原本 float64 省略与显式 0 无法区分，零温度被静默改写成 0.7。
type chatRequest struct {
	Model       string           `json:"model"`
	Messages    []ai.ChatMessage `json:"messages"`
	Temperature *float64         `json:"temperature"`
	Stream      bool             `json:"stream"`
}

// stage 白名单（P1-40：X-Stage 请求头直通 RecordUsage/GenerateTextForStage——
// 客户端可写审计字段与选模型。限定合法阶段枚举，非法值 400。）
var allowedStages = map[string]bool{
	"reply": true, "simple": true, "test": true, "eval": true, "summary": true,
	"industry_analysis": true, "chatflow_extract": true, "embedding": true,
}

// handleChatCompletions 转发对话请求，网关侧做 fail-closed 计量
func (s *Server) handleChatCompletions(c *gin.Context) {
	tenantID := c.GetUint("tenant_id")
	stage := c.GetHeader("X-Stage")
	if stage == "" {
		stage = "reply"
	} else if !allowedStages[stage] { // P1-40：非法 stage 拒绝而非透传
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "X-Stage 非法值: " + stage}, "code": "invalid_stage"})
		return
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
	// P2-2：stream=true 改为 OpenAI 兼容 SSE 分片输出（移除原 stream_unsupported 拒绝）。
	// 诚实说明：上游 llm 客户端当前均为非流式（返回完整串），故这是"完整结果分片成 SSE 帧"的
	// 投递，SSE 客户端可正常增量消费；厂商侧 token 级流式属更大改造（见 FIXPLAN P2-2）。
	var temp float64
	if req.Temperature == nil {
		temp = 0.7 // 省略默认
	} else {
		temp = *req.Temperature // 显式传值（含 0）透传
	}
	if req.Stream {
		s.handleChatStream(c, tenantID, stage, req, temp, gatewayTrace(c))
		return
	}

	// 1. 计费统一（2026-09-03）：ConsumeAIQuota 已降级为统计旁路（恒 true，仅累计计数），
	//    真正的 fail-closed 闸是下方 CheckTokenAvailability（三桶前置检查）+ SinkRecordUsage（批量扣减）
	billing.ConsumeAIQuota(tenantID)
	// 1b. Token 三桶前置检查（防零余额仍消耗厂商额度）：与本地 chat_reply 同口径
	if !billing.CheckTokenAvailability(tenantID) {
		c.JSON(http.StatusForbidden, gin.H{
			"error": gin.H{"message": "AI 额度余额不足，请充值或升级套餐"},
			"code":  "token_insufficient",
		})
		return
	}

	// 2. 上游多模型降级生成（网关持有平台厂商 Key）
	// E3(2026-09-19)：续用调用方 X-Trace-ID（调用本网关须持签名 JWT，属内部可信链路；
	// 仍做字符白名单防日志注入），trace-only ctx 下传供出站客户端串链。
	start := time.Now()
	ctx, span := telemetry.Tracer().Start(logx.ContextWithTrace(context.Background(), gatewayTrace(c)), "ai-gateway.chat")
	defer span.End()
	reply, provider, modelName, usage, err := ai.Router.GenerateTextForStage(ctx, stage, tenantID, req.Messages, temp)
	setChatSpanAttrs(span, tenantID, stage, provider, modelName, usage.PromptTokens, usage.CompletionTokens)
	if err != nil || reply == "" {
		span.RecordError(err)
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{"message": "上游模型全部不可用: " + errStr(err)},
			"code":  "upstream_unavailable",
		})
		return
	}

	// 3. 计量落账 + 三桶扣减（与本地同链路，但此处为唯一计量方）
	billing.RecordUsage(tenantID, 0, 0, stage, provider, modelName,
		usage.PromptTokens, usage.CompletionTokens, time.Since(start).Milliseconds())
	// 2026-09-03 计费统一：由异步 `go DeductTokensActual` 改为投递 UsageSink 批量落库
	billing.SinkRecordUsage(tenantID, int64(usage.TotalTokens))

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

// handleChatStream OpenAI 兼容 SSE 流式输出（P2-2）。
// 与 handleChatCompletions 共用计费前置闸 + 多模型降级 + 计量落账链路，仅输出改为 SSE 分片。
func (s *Server) handleChatStream(c *gin.Context, tenantID uint, stage string, req chatRequest, temp float64, trace string) {
	billing.ConsumeAIQuota(tenantID)
	if !billing.CheckTokenAvailability(tenantID) {
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"message": "AI 额度余额不足，请充值或升级套餐"}, "code": "token_insufficient"})
		return
	}

	start := time.Now()
	ctx, span := telemetry.Tracer().Start(logx.ContextWithTrace(context.Background(), trace), "ai-gateway.chat.stream")
	defer span.End()
	reply, provider, modelName, usage, err := ai.Router.GenerateTextForStage(ctx, stage, tenantID, req.Messages, temp)
	setChatSpanAttrs(span, tenantID, stage, provider, modelName, usage.PromptTokens, usage.CompletionTokens)
	if err != nil || reply == "" {
		span.RecordError(err)
		// 尚未写 SSE 头，回 JSON 错误（客户端可据此区分失败）
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": "上游模型全部不可用: " + errStr(err)}, "code": "upstream_unavailable"})
		return
	}

	// 计量落账 + 三桶扣减（与非流式同链路）
	billing.RecordUsage(tenantID, 0, 0, stage, provider, modelName,
		usage.PromptTokens, usage.CompletionTokens, time.Since(start).Milliseconds())
	billing.SinkRecordUsage(tenantID, int64(usage.TotalTokens))
	go s.collectMaterial(tenantID, stage, req.Messages, reply)

	// SSE 响应头（X-Accel-Buffering:no 禁用反代缓冲，确保分片实时到达客户端）
	id := "gw-" + time.Now().Format("20060102150405")
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	writeSSEChunk(c, id, modelName, gin.H{"role": "assistant"}, "") // 首帧：角色
	for _, part := range splitForStream(reply, 24) {                // 内容分片
		writeSSEChunk(c, id, modelName, gin.H{"content": part}, "")
	}
	writeSSEChunk(c, id, modelName, gin.H{}, "stop") // 尾帧：finish_reason
	c.Writer.WriteString("data: [DONE]\n\n")
	c.Writer.Flush()
}

// writeSSEChunk 写一帧 OpenAI 兼容 chat.completion.chunk（delta 可为空；finish 空=中间帧发 null）。
func writeSSEChunk(c *gin.Context, id, model string, delta gin.H, finish string) {
	frame := gin.H{
		"id":     id,
		"object": "chat.completion.chunk",
		"model":  model,
		"choices": []gin.H{{
			"index":         0,
			"delta":         delta,
			"finish_reason": finishOrNil(finish),
		}},
	}
	b, err := json.Marshal(frame)
	if err != nil {
		return
	}
	c.Writer.WriteString("data: " + string(b) + "\n\n")
	c.Writer.Flush()
}

// finishOrNil 中间帧 finish_reason 发 null（OpenAI 兼容），终帧发 "stop"。
func finishOrNil(f string) interface{} {
	if f == "" {
		return nil
	}
	return f
}

// splitForStream 把回复按 rune 切成固定步长分片（流式投递用；纯函数，rune 安全，可单测）。
func splitForStream(s string, step int) []string {
	if step <= 0 {
		step = 24
	}
	runes := []rune(s)
	out := make([]string, 0, (len(runes)+step-1)/step)
	for i := 0; i < len(runes); i += step {
		end := i + step
		if end > len(runes) {
			end = len(runes)
		}
		out = append(out, string(runes[i:end]))
	}
	return out
}

// setChatSpanAttrs 给 AI 调用 span 打属性（P2-7）：租户/阶段/供应商/模型/token，供 span 拓扑与成本归因。
func setChatSpanAttrs(span trace.Span, tenantID uint, stage, provider, model string, promptTok, completionTok int) {
	span.SetAttributes(
		attribute.Int("tenant.id", int(tenantID)),
		attribute.String("ai.stage", stage),
		attribute.String("ai.provider", provider),
		attribute.String("ai.model", model),
		attribute.Int("ai.prompt_tokens", promptTok),
		attribute.Int("ai.completion_tokens", completionTok),
	)
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

	// P1-35 修复(2026-09-09)：向量化也消耗配额，必须过三桶前置闸——
	// 否则零余额租户对话被拦但 embedding 照调厂商，绕过计费。
	// 与 handleChatCompletions 同口径：不足返回 HTTP 429 + token_insufficient。
	billing.ConsumeAIQuota(tenantID)
	if !billing.CheckTokenAvailability(tenantID) {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error": gin.H{"message": "AI 额度余额不足，请充值或升级套餐"},
			"code":  "token_insufficient",
		})
		return
	}

	data := make([]gin.H, 0, len(texts))
	totalTok := 0
	for i, t := range texts {
		vec := service.DefaultEmbeddingClient.Embed(t)
		totalTok += len([]rune(t)) / 2 // 粗略估算 token（P2-18 可换 tiktoken 精确口径）
		data = append(data, gin.H{"object": "embedding", "index": i, "embedding": vec})
	}
	billing.RecordUsage(tenantID, 0, 0, "embedding", "embedding", req.Model, totalTok, 0, 0)
	// 2026-09-03 计费统一：由异步 `go DeductTokensActual` 改为投递 UsageSink 批量落库
	billing.SinkRecordUsage(tenantID, int64(totalTok))

	// 响应头回显估算口径（P1-35：token 估算透明化，避免客户端误以为精确计费）
	c.Header("X-Token-Estimate", fmt.Sprintf("rough:%s_proxy:%d", "len/2", totalTok))

	c.JSON(http.StatusOK, embeddingResponse{
		Data:  data,
		Model: req.Model,
		Usage: gin.H{"prompt_tokens": totalTok, "total_tokens": totalTok, "estimation": "rough"},
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

// gatewayTrace E3(2026-09-19)：取调用方 X-Trace-ID 并按白名单校验（^[a-zA-Z0-9-]{8,64}$，
// 与 middleware.TraceID 同口径），非法返回空串（不写入 ctx 即不出头）。
func gatewayTrace(c *gin.Context) string {
	s := c.GetHeader("X-Trace-ID")
	if len(s) < 8 || len(s) > 64 {
		return ""
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return ""
	}
	return s
}
