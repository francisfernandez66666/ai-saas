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
// 智谱GLM AI客户端封装
// 为什么单独封装？AI接口可能更换，隔离变化
// 设计原则：上层代码只调用GenerateText，不关心底层是哪个模型
// ============================================================

// GLMClient 智谱GLM客户端
type GLMClient struct {
	APIKey      string       // API密钥
	BaseURL     string       // API基础URL
	ModelName   string       // 模型名称
	MockMode    bool         // 模拟模式（不调用真实API）
	MaxTokens   int          // 最大输出token数
	Temperature float64      // 默认采样温度
	MaxRetries  int          // 最大重试次数
	httpClient  *http.Client // HTTP客户端（复用连接）
}

// ChatMessage 对话消息结构
type ChatMessage struct {
	Role             string `json:"role"`              // user/assistant/system
	Content          string `json:"content"`           // 消息内容（最终回复）
	ReasoningContent string `json:"reasoning_content"` // 思考过程（思考模型专用）
}

// ChatRequest 请求结构
type ChatRequest struct {
	Model       string        `json:"model"`       // 模型名
	Messages    []ChatMessage `json:"messages"`    // 消息列表
	Temperature float64       `json:"temperature"` // 温度
	MaxTokens   int           `json:"max_tokens"`  // 最大输出token
}

// ChatResponse 响应结构
type ChatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int         `json:"index"`
		FinishReason string      `json:"finish_reason"` // stop/length/content_filter
		Message      ChatMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// DefaultClient 默认客户端实例
var DefaultClient *GLMClient

// InitClient 初始化AI客户端
// 从全局配置读取参数，优先使用Zhipu详细配置，兼容旧配置
func InitClient() {
	cfg := config.GlobalConfig.AI

	// 优先用Zhipu详细配置，没有就降级用旧配置
	apiKey := cfg.Zhipu.APIKey
	if apiKey == "" {
		apiKey = cfg.APIKey
	}
	baseURL := cfg.Zhipu.BaseURL
	if baseURL == "" {
		baseURL = cfg.BaseURL
	}
	model := cfg.Zhipu.Model
	if model == "" {
		model = cfg.ModelName
	}
	maxTokens := cfg.Zhipu.MaxTokens
	if maxTokens == 0 {
		maxTokens = 500
	}
	temperature := cfg.Zhipu.Temperature
	if temperature == 0 {
		temperature = 0.7
	}

	DefaultClient = &GLMClient{
		APIKey:      apiKey,
		BaseURL:     baseURL,
		ModelName:   model,
		MockMode:    cfg.MockMode,
		MaxTokens:   maxTokens,
		Temperature: temperature,
		MaxRetries:  1, // 最多重试1次（共2次尝试），429内部重试没用，快速降级到其他模型更高效
		httpClient: &http.Client{
			Timeout: 45 * time.Second, // GLM-4-Flash速度快，正常几秒返回，45秒超时足够
		},
	}

	log.Printf("AI客户端初始化完成, 模拟模式: %v, 模型: %s, 最大token: %d",
		cfg.MockMode, model, maxTokens)
}

// SetModel 动态切换模型名（用于同平台主备模型切换）
func (c *GLMClient) SetModel(modelName string) {
	if modelName != "" && modelName != c.ModelName {
		log.Printf("[智谱AI] 切换模型: %s → %s", c.ModelName, modelName)
		c.ModelName = modelName
	}
}

// GetModelName 获取当前模型名
func (c *GLMClient) GetModelName() string {
	return c.ModelName
}

// GenerateText 生成文本（核心接口）
// 输入：对话历史消息列表
// 输出：AI回复的文本内容
// 错误处理：失败自动重试MaxRetries次，仍失败则返回错误
// 注意：延迟前置（调用前等待），由调用方通过utils.GetReplyDelay控制
// 好处：1. 用户体验一样（都是等一会儿才回复）
//
//  2. 自然摊平请求高峰，减少429限流
//  3. 可以配合"正在输入中"状态做先接住再回复
func (c *GLMClient) GenerateText(messages []ChatMessage, temperature float64) (string, error) {
	reply, _, err := c.GenerateTextWithUsage(messages, temperature)
	return reply, err
}

// GenerateTextWithUsage 生成并返回 token 用量（M3 计量底座；mock 路径用量为零值）
func (c *GLMClient) GenerateTextWithUsage(messages []ChatMessage, temperature float64) (string, Usage, error) {
	var reply string
	var usage Usage
	var err error

	// 如果是模拟模式，直接返回模拟回复
	// 模拟模式的目的：先跑通整个流程，再接真实API
	if c.MockMode {
		reply = c.mockGenerate(messages)
	} else if c.APIKey == "" {
		// 没有API Key，返回提示
		reply = "【AI服务未配置，请设置ZHIPU_API_KEY环境变量后重启服务】"
	} else {
		// 带重试调用（指数退避 + 429特殊处理）
		reply, usage, err = c.generateWithRetry(messages, temperature)
		if err != nil {
			return "", Usage{}, err
		}
	}

	// 空回复兜底（上层会处理降级，这里只记日志不拦截）
	if reply == "" {
		log.Printf("[智谱AI] 警告: AI返回内容为空，上层将降级使用模板回复")
	} else {
		log.Printf("[智谱AI] 回复长度: %d字符", utf8.RuneCountInString(reply))
	}

	return reply, usage, nil
}

// generateWithRetry 带重试的API调用
// 重试策略：指数退避(2s→4s→8s)，429限流翻倍(4s→8s→16s)
// 参数类错误(4xx非429)不重试
func (c *GLMClient) generateWithRetry(messages []ChatMessage, temperature float64) (string, Usage, error) {
	var lastErr error
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		if attempt > 0 {
			// 计算等待时间：指数退避 base=8s，即 8s, 16s, 32s
			// 为什么基数8秒？429限流通常持续10-30秒，太短的退避等于继续撞墙
			waitSeconds := int(math.Pow(2, float64(attempt))) * 4
			// 429限流已经是base=8秒起步，额外再×2更保险（16s→32s→64s）
			if isRateLimitError(lastErr) {
				waitSeconds = waitSeconds * 2
				if waitSeconds > 60 {
					waitSeconds = 60
				}
				log.Printf("[智谱AI] 触发限流，第%d次重试等待 %d 秒...", attempt, waitSeconds)
			} else {
				log.Printf("[智谱AI] 第%d次重试，等待 %d 秒...", attempt, waitSeconds)
			}
			// 加±20%随机抖动，避免所有请求在同一时间恢复撞限流
			jitter := float64(waitSeconds) * 0.2
			r := rand.New(rand.NewSource(time.Now().UnixNano()))
			waitSeconds = int(float64(waitSeconds) - jitter + r.Float64()*2*jitter)
			time.Sleep(time.Duration(waitSeconds) * time.Second)
		}

		reply, u, err := c.callAPI(messages, temperature)
		if err == nil {
			return reply, u, nil
		}

		lastErr = err
		log.Printf("[智谱AI] 第%d次调用失败: %v", attempt+1, err)

		// 参数类错误不用重试（4xx非429），直接返回
		if isClientError(err) && !isRateLimitError(err) {
			break
		}
	}

	return "", Usage{}, fmt.Errorf("智谱AI调用失败，已重试%d次: %v", c.MaxRetries+1, lastErr)
}

// calcTypingDelay 计算模拟打字延迟
// 算法：
//  1. 基础延迟 = 字数 / 打字速度
//  2. clamp到 [MinDelay, MaxDelay] 之间
//  3. 乘以 (1 ± jitter) 的随机因子（增加真实感）
func calcTypingDelay(text string, speed float64, minDelay float64, maxDelay float64, jitter float64) float64 {
	// 计算回复字数（中文按字算，utf8.RuneCountInString处理多字节字符）
	charCount := utf8.RuneCountInString(text)
	if charCount <= 0 {
		return minDelay
	}

	// 基础延迟 = 字数 / 打字速度
	baseDelay := float64(charCount) / speed

	// clamp到最小和最大延迟之间
	if baseDelay < minDelay {
		baseDelay = minDelay
	}
	if baseDelay > maxDelay {
		baseDelay = maxDelay
	}

	// 随机抖动：在 (1-jitter) 到 (1+jitter) 之间随机
	// 让每次打字速度有波动，更像真人
	if jitter > 0 {
		// 用当前时间做随机种子
		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		factor := 1.0 - jitter + r.Float64()*2*jitter
		baseDelay = baseDelay * factor
	}

	return baseDelay
}

// callAPI 调用智谱API
// 注意：使用复用的httpClient，避免每次创建连接
func (c *GLMClient) callAPI(messages []ChatMessage, temperature float64) (string, Usage, error) {
	reqBody := ChatRequest{
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

	// 使用复用的HTTP客户端
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

	var chatResp ChatResponse
	err = json.Unmarshal(body, &chatResp)
	if err != nil {
		return "", Usage{}, fmt.Errorf("解析响应失败: %v", err)
	}

	if len(chatResp.Choices) > 0 {
		// 记录token用量（便于成本监控）
		log.Printf("[智谱AI] 调用成功: prompt=%d, completion=%d, total=%d tokens, finish_reason=%s",
			chatResp.Usage.PromptTokens,
			chatResp.Usage.CompletionTokens,
			chatResp.Usage.TotalTokens,
			chatResp.Choices[0].FinishReason)
		content := chatResp.Choices[0].Message.Content
		// 思考模型兼容：如果content为空但有reasoning_content，说明模型还在思考就被截断了
		// 这种情况返回空内容，让上层降级处理（正常情况content会有最终回复）
		if content == "" && chatResp.Choices[0].Message.ReasoningContent != "" {
			log.Printf("[智谱AI] 思考模型: 思考过程=%d字符，但content为空（可能token不够被截断）",
				utf8.RuneCountInString(chatResp.Choices[0].Message.ReasoningContent))
		}
		// 调试：如果token数正常但内容为空，打原始响应用于排查
		if content == "" && chatResp.Usage.CompletionTokens > 0 {
			log.Printf("[智谱AI] 调试: completion=%d但content为空，原始响应前500字符: %s",
				chatResp.Usage.CompletionTokens,
				truncateString(string(body), 500))
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
// 错误类型判断辅助函数
// 用于重试策略：网络错误/5xx/429 可以重试，参数类4xx不重试
// ============================================================

// isRateLimitError 判断是否为限流错误（429）
func isRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	// 智谱限流错误码1302，HTTP状态码429
	return strings.Contains(errStr, "429") ||
		strings.Contains(errStr, "1302") ||
		strings.Contains(errStr, "速率限制") ||
		strings.Contains(errStr, "rate limit")
}

// isClientError 判断是否为客户端错误（4xx）
// 客户端错误一般是参数问题，重试没用
func isClientError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "status=4") && !strings.Contains(errStr, "status=429")
}

// isNetworkError 判断是否为网络/超时类错误
// 这类错误通常可以通过重试解决
func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "deadline") ||
		strings.Contains(errStr, "connection") ||
		strings.Contains(errStr, "EOF") ||
		strings.Contains(errStr, "500") ||
		strings.Contains(errStr, "502") ||
		strings.Contains(errStr, "503") ||
		strings.Contains(errStr, "504")
}

// mockGenerate 模拟AI回复
// 为什么需要模拟模式？开发阶段不需要真实API Key，也能跑通完整流程
// 回复逻辑：根据最后一条用户消息，给出简单的规则化回复
func (c *GLMClient) mockGenerate(messages []ChatMessage) string {
	if len(messages) == 0 {
		return "您好！很高兴为您服务。请问有什么可以帮您的？"
	}

	// 获取最后一条用户消息
	lastUserMsg := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			lastUserMsg = messages[i].Content
			break
		}
	}

	// 简单的规则模拟回复
	switch {
	case containsKeyword(lastUserMsg, "价格", "多少钱", "贵", "优惠"):
		return "关于价格方面，我们目前有几个不同的配置版本，具体价格根据您选择的配置有所不同。请问您比较关注哪个配置呢？我可以为您详细介绍一下。"
	case containsKeyword(lastUserMsg, "配置", "性能", "参数", "发动机"):
		return "这款车的配置非常丰富。动力方面搭载了高性能发动机，配合智能四驱系统，越野能力非常出色。安全配置也很全面，包括主动刹车、车道保持等。您对哪方面的配置比较感兴趣呢？"
	case containsKeyword(lastUserMsg, "到店", "试驾", "看车"):
		return "非常欢迎您到店试驾体验！我们门店地址在市中心汽车城，您看什么时间方便？我帮您预约一下，到时候会有专业顾问为您一对一服务。"
	case containsKeyword(lastUserMsg, "对比", "比较", "和XX"):
		return "您考虑得很周全。相比同级别车型，我们的优势主要体现在越野性能和品质可靠性上。特别是在复杂路况下的表现，您试过就知道区别了。要不要我给您详细对比一下？"
	case containsKeyword(lastUserMsg, "你好", "您好", "在吗", "hi", "hello"):
		return "您好！很高兴为您服务。我是您的专属智能顾问，关于车型、价格、配置、试驾等任何问题都可以问我哦。请问您对哪款车型比较感兴趣呢？"
	case containsKeyword(lastUserMsg, "谢谢", "感谢", "好的"):
		return "不客气的~ 如果有其他问题随时问我。对了，您目前是已经有目标车型了，还是想让我给您推荐一下呢？"
	default:
		return "明白了，您的需求我记录下来了。为了更好地帮到您，请问您的购车预算大概是多少呢？另外有没有比较倾向的车型类型？"
	}
}

// containsKeyword 检查是否包含关键词
func containsKeyword(text string, keywords ...string) bool {
	for _, kw := range keywords {
		if len(kw) > 0 && containsIgnoreCase(text, kw) {
			return true
		}
	}
	return false
}

// containsIgnoreCase 大小写不敏感子串包含判断
func containsIgnoreCase(s, substr string) bool {
	return len(s) >= len(substr) &&
		(len(s) == 0 || len(substr) == 0 ||
			indexOfIgnoreCase(s, substr) >= 0)
}

// indexOfIgnoreCase 大小写不敏感子串定位（-1=不存在）
func indexOfIgnoreCase(s, substr string) int {
	sLower := toLower(s)
	subLower := toLower(substr)
	for i := 0; i <= len(sLower)-len(subLower); i++ {
		if sLower[i:i+len(subLower)] == subLower {
			return i
		}
	}
	return -1
}

// toLower ASCII 小写转换（仅处理A-Z，避免unicode开销）
func toLower(s string) string {
	result := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			result[i] = c + 32
		} else {
			result[i] = c
		}
	}
	return string(result)
}

// truncateString 截断字符串（用于日志输出，避免打太多）
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
