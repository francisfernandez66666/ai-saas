package ai

import (
	"ai-scrm/config"
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"math/rand"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// ============================================================
// 硅基流动 AI客户端封装
// 平台：https://api.siliconflow.cn
// 特点：OpenAI兼容API格式，多模型聚合，作为跨平台备用
// 作用：当智谱GLM全线挂掉时，自动降级到这里
// ============================================================

// SiliconFlowClient 硅基流动客户端
type SiliconFlowClient struct {
	APIKey      string       // API密钥
	BaseURL     string       // API基础URL
	ModelName   string       // 模型名称
	MaxTokens   int          // 最大输出token数
	Temperature float64      // 默认采样温度
	MaxRetries  int          // 最大重试次数
	Enabled     bool         // 是否启用（有API Key才启用）
	httpClient  *http.Client // HTTP客户端（复用连接）
}

// SiliconFlowChatRequest 请求结构（OpenAI兼容）
type SiliconFlowChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
}

// SiliconFlowChatResponse 响应结构（OpenAI兼容）
type SiliconFlowChatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int         `json:"index"`
		FinishReason string      `json:"finish_reason"`
		Message      ChatMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// SiliconFlowDefaultClient 硅基流动默认客户端
var SiliconFlowDefaultClient *SiliconFlowClient

// InitSiliconFlowClient 初始化硅基流动客户端
func InitSiliconFlowClient() {
	cfg := config.GlobalConfig.AI.SiliconFlow

	enabled := cfg.APIKey != ""

	SiliconFlowDefaultClient = &SiliconFlowClient{
		APIKey:      cfg.APIKey,
		BaseURL:     cfg.BaseURL,
		ModelName:   cfg.Model,
		MaxTokens:   cfg.MaxTokens,
		Temperature: cfg.Temperature,
		MaxRetries:  1, // 最多重试1次（共2次尝试），429快速降级到下一个模型
		Enabled:     enabled,
		httpClient: &http.Client{
			Timeout: 60 * time.Second, // 聚合平台延迟可能稍高，给60秒
		},
	}

	if enabled {
		log.Printf("[硅基流动] 客户端初始化完成, 模型: %s, 最大token: %d", cfg.Model, cfg.MaxTokens)
	} else {
		log.Printf("[硅基流动] 未配置API Key，备用模型降级未启用")
	}
}

// SetModel 动态切换模型名
func (c *SiliconFlowClient) SetModel(modelName string) {
	if modelName != "" && modelName != c.ModelName {
		log.Printf("[硅基流动] 切换模型: %s → %s", c.ModelName, modelName)
		c.ModelName = modelName
	}
}

// GetModelName 获取当前模型名
func (c *SiliconFlowClient) GetModelName() string {
	return c.ModelName
}

// GenerateText 生成文本
func (c *SiliconFlowClient) GenerateText(messages []ChatMessage, temperature float64) (string, error) {
	reply, _, err := c.GenerateTextWithUsage(messages, temperature)
	return reply, err
}

// GenerateTextWithUsage 生成并返回 token 用量（M3 计量底座）
func (c *SiliconFlowClient) GenerateTextWithUsage(messages []ChatMessage, temperature float64) (string, Usage, error) {
	if !c.Enabled {
		return "", Usage{}, fmt.Errorf("硅基流动未启用（缺少API Key）")
	}

	reply, usage, err := c.generateWithRetry(messages, temperature)
	if err != nil {
		return "", Usage{}, err
	}

	if reply == "" {
		log.Printf("[硅基流动] 警告: AI返回内容为空")
	} else {
		log.Printf("[硅基流动] 回复长度: %d字符", utf8.RuneCountInString(reply))
	}

	return reply, usage, nil
}

// generateWithRetry 带重试的API调用
// 重试策略：指数退避 base=4s，429限流翻倍，加随机抖动
func (c *SiliconFlowClient) generateWithRetry(messages []ChatMessage, temperature float64) (string, Usage, error) {
	var lastErr error
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		if attempt > 0 {
			// 指数退避 base=4s：4s, 8s
			waitSeconds := int(math.Pow(2, float64(attempt))) * 2
			// 429限流翻倍
			if isSiliconFlowRateLimitError(lastErr) {
				waitSeconds = waitSeconds * 2
				if waitSeconds > 60 {
					waitSeconds = 60
				}
				log.Printf("[硅基流动] 触发限流，第%d次重试等待 %d 秒...", attempt, waitSeconds)
			} else {
				log.Printf("[硅基流动] 第%d次重试，等待 %d 秒...", attempt, waitSeconds)
			}
			// ±20%随机抖动
			jitter := float64(waitSeconds) * 0.2
			r := rand.New(rand.NewSource(time.Now().UnixNano()))
			waitSeconds = int(float64(waitSeconds) - jitter + r.Float64()*2*jitter)
			time.Sleep(time.Duration(waitSeconds) * time.Second)
		}

		reply, usage, err := c.callAPI(messages, temperature)
		if err == nil {
			return reply, usage, nil
		}

		lastErr = err
		log.Printf("[硅基流动] 第%d次调用失败: %v", attempt+1, err)

		// 参数类错误不用重试
		if isSiliconFlowClientError(err) && !isSiliconFlowRateLimitError(err) {
			break
		}
	}

	return "", Usage{}, fmt.Errorf("硅基流动调用失败，已重试%d次: %v", c.MaxRetries+1, lastErr)
}

// callAPI 调用硅基流动API
func (c *SiliconFlowClient) callAPI(messages []ChatMessage, temperature float64) (string, Usage, error) {
	reqBody := SiliconFlowChatRequest{
		Model:       c.ModelName,
		Messages:    messages,
		Temperature: temperature,
		MaxTokens:   c.MaxTokens,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", Usage{}, fmt.Errorf("序列化请求失败: %v", err)
	}

	url := c.BaseURL + "/chat/completions"
	httpReq, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", Usage{}, fmt.Errorf("创建请求失败: %v", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", Usage{}, fmt.Errorf("请求API失败: %v", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", Usage{}, fmt.Errorf("读取响应失败: %v", err)
	}

	if resp.StatusCode != 200 {
		return "", Usage{}, fmt.Errorf("API返回错误: status=%d, body=%s", resp.StatusCode, string(body))
	}

	var chatResp SiliconFlowChatResponse
	err = json.Unmarshal(body, &chatResp)
	if err != nil {
		return "", Usage{}, fmt.Errorf("解析响应失败: %v", err)
	}

	if len(chatResp.Choices) > 0 {
		log.Printf("[硅基流动] 调用成功: prompt=%d, completion=%d, total=%d tokens, finish_reason=%s",
			chatResp.Usage.PromptTokens,
			chatResp.Usage.CompletionTokens,
			chatResp.Usage.TotalTokens,
			chatResp.Choices[0].FinishReason)
		content := chatResp.Choices[0].Message.Content
		// 思考模型兼容
		if content == "" && chatResp.Choices[0].Message.ReasoningContent != "" {
			log.Printf("[硅基流动] 思考模型: 思考过程=%d字符，但content为空",
				utf8.RuneCountInString(chatResp.Choices[0].Message.ReasoningContent))
		}
		usage := Usage{
			PromptTokens:     chatResp.Usage.PromptTokens,
			CompletionTokens: chatResp.Usage.CompletionTokens,
			TotalTokens:      chatResp.Usage.TotalTokens,
		}
		return content, usage, nil
	}

	return "", Usage{}, fmt.Errorf("AI未返回内容")
}

// ============================================================
// 错误类型判断
// ============================================================

// isSiliconFlowRateLimitError 是否429限流错误（重试退避时间翻倍）
func isSiliconFlowRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "429") ||
		strings.Contains(errStr, "rate limit") ||
		strings.Contains(errStr, "TPM limit") ||
		strings.Contains(errStr, "RPM limit") ||
		strings.Contains(errStr, "限流")
}

// isSiliconFlowClientError 是否参数类4xx错误（重试无意义，直接跳出）
func isSiliconFlowClientError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "status=4") && !strings.Contains(errStr, "status=429")
}
