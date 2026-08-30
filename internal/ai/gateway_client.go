// Package ai AI 网关客户端（云端枢纽转发）：本地部署/SaaS 实例无厂商 Key 时，
// 所有 reply 阶段请求统一经网关出网，网关持有平台厂商 Key 并做鉴权+余额 fail-closed+上游降级。
// 计费权仍在租户侧（本地三桶扣减不变），网关仅作转发与平台级审计采集。
package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"ai-scrm/config"
)

// DefaultGatewayClient AI 网关默认客户端（GatewayURL 非空时装配）
var DefaultGatewayClient *GatewayClient

// GatewayClient AI 网关转发客户端（OpenAI 兼容 /v1/chat/completions）
type GatewayClient struct {
	BaseURL string       // 网关根地址
	Token   string       // 网关鉴权令牌
	Model   string       // 默认转发模型
	http    *http.Client // 复用 HTTP 客户端
}

// gatewayChatRequest OpenAI 兼容请求体
type gatewayChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	Stream      bool          `json:"stream"`
}

// gatewayUsage OpenAI 兼容 usage
type gatewayUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens       int `json:"total_tokens"`
}

// gatewayChatResponse OpenAI 兼容响应体（仅取回复与用量）
type gatewayChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage gatewayUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// InitGatewayClient 由配置装配网关客户端；GatewayURL 为空则不启用
func InitGatewayClient() {
	cfg := config.GlobalConfig.AI
	if cfg.GatewayURL == "" {
		DefaultGatewayClient = nil
		log.Println("[AI网关] 未配置 LLM_GATEWAY_URL，本地直连厂商模式")
		return
	}
	DefaultGatewayClient = &GatewayClient{
		BaseURL: cfg.GatewayURL,
		Token:   cfg.GatewayToken,
		Model:   cfg.GatewayModel,
		http:    &http.Client{Timeout: 120 * time.Second},
	}
	log.Printf("[AI网关] 已启用，转发目标: %s", cfg.GatewayURL)
}

// SetModel 切换网关默认模型（阶段覆盖兼容接口）
func (g *GatewayClient) SetModel(modelName string) {
	if modelName != "" {
		g.Model = modelName
	}
}

// GenerateTextWithUsage 经网关转发并透传 token 用量（与 SiliconFlow/GLM 客户端对齐签名）
func (g *GatewayClient) GenerateTextWithUsage(messages []ChatMessage, temperature float64) (string, Usage, error) {
	model := g.Model
	if model == "" {
		model = "default"
	}
	reqBody := gatewayChatRequest{
		Model:       model,
		Messages:    messages,
		Temperature: temperature,
		Stream:      false,
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return "", Usage{}, fmt.Errorf("网关请求序列化失败: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, g.BaseURL, bytes.NewReader(raw))
	if err != nil {
		return "", Usage{}, fmt.Errorf("网关请求构建失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.Token)

	resp, err := g.http.Do(req)
	if err != nil {
		return "", Usage{}, fmt.Errorf("网关调用失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", Usage{}, fmt.Errorf("网关响应读取失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", Usage{}, fmt.Errorf("网关返回 HTTP %d: %s", resp.StatusCode, string(body))
	}

	var gr gatewayChatResponse
	if err := json.Unmarshal(body, &gr); err != nil {
		return "", Usage{}, fmt.Errorf("网关响应解析失败: %w", err)
	}
	if gr.Error != nil && gr.Error.Message != "" {
		return "", Usage{}, fmt.Errorf("网关业务错误: %s", gr.Error.Message)
	}
	if len(gr.Choices) == 0 {
		return "", Usage{}, fmt.Errorf("网关返回空 choices")
	}
	content := gr.Choices[0].Message.Content
	if content == "" {
		return "", Usage{}, fmt.Errorf("网关返回空内容")
	}
	return content, Usage{
		PromptTokens:     gr.Usage.PromptTokens,
		CompletionTokens: gr.Usage.CompletionTokens,
		TotalTokens:      gr.Usage.TotalTokens,
	}, nil
}
