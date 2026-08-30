// OpenAPI 对话端点：对外渠道嵌入的 chat/completions 兼容接口。
package api

// ============================================================
// OpenAPI 对话端点（商业化 M4 渠道嵌入，2026-08-29）
//
// POST /openapi/v1/chat/completions —— 对外输出 AI 销售能力（抖音/TikTok/淘宝等渠道嵌入）
// 复用站内全链路：OrchestrateReply（计费/隔离/埋点/留资一致）；
// 响应兼容 OpenAI chat/completions 结构。
//
// 设计要点：
//   1. 鉴权：Bearer sk_（OpenAPIAuth）→ RequirePerm(chat.write)
//   2. 会话归属：external_user_id + session_id → 服务端全量持久化（Customer + Conversation）
//   3. 计费：与站内同池同链路（ConsumeAIQuota + DeductTokensActual 在 llm 层统一走）
//   4. 硬边界/留资：与站内一致（无关话题拦截、到店倾向留资合并+分配顾问）
// ============================================================

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"time"

	"ai-scrm/internal/chatflow"
	"ai-scrm/internal/db"
	"ai-scrm/internal/engine/flow"
	"ai-scrm/internal/engine/strategy"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
	"ai-scrm/internal/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// openAPIChatReq OpenAI 兼容请求（扩展渠道字段）
type openAPIChatReq struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	ExternalUserID string  `json:"external_user_id"` // 渠道侧用户ID（必填，服务端映射为 Customer）
	SessionID      string  `json:"session_id"`       // 渠道侧会话ID（可选，隔离同一用户多轮会话）
	Channel        string  `json:"channel"`          // 渠道标识：douyin/tiktok/taobao...（默认 openapi）
	Temperature    float64 `json:"temperature"`
	Stream         bool    `json:"stream"` // 是否流式返回（SSE，OpenAI 兼容逐帧；默认 false 全量返回）
}

// phoneRegex 手机号提取（留资线索捕获）
var phoneRegex = regexp.MustCompile(`1[3-9]\d{9}`)

// OpenAPIChatCompletions POST /openapi/v1/chat/completions
func OpenAPIChatCompletions(c *gin.Context) {
	tenantID := middleware.EffectiveTenantID(c)
	trace := middleware.GetTraceID(c) // P1-3：全链路 trace

	var req openAPIChatReq
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("[OpenAPI][trace=%s] 参数绑定失败 tenant=%d: %v", trace, tenantID, err)
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "error_code": "bad_request", "message": "参数错误: " + err.Error()})
		return
	}
	if req.ExternalUserID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "error_code": "bad_request", "message": "external_user_id 必填"})
		return
	}
	// 取最后一条用户消息作为本轮输入
	userInput := ""
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" && req.Messages[i].Content != "" {
			userInput = req.Messages[i].Content
			break
		}
	}
	if userInput == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "error_code": "bad_request", "message": "messages 中无用户内容"})
		return
	}
	channel := req.Channel
	if channel == "" {
		channel = "openapi"
	}

	// 1. 解析/创建客户（external_user_id 租户内映射）
	customer, err := resolveOpenAPICustomer(c, tenantID, channel, req.ExternalUserID)
	if err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "客户解析失败")
		return
	}
	// 2. 解析/创建会话（external_user_id + session_id 隔离）
	conversation, err := resolveOpenAPIConversation(c, tenantID, customer.ID, channel, req.SessionID)
	if err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "会话解析失败")
		return
	}

	// 3. 持久化客户消息
	now := time.Now()
	customerMsg := model.Message{
		TenantID:       tenantID,
		ConversationID: conversation.ID,
		CustomerID:     customer.ID,
		SenderType:     "customer",
		Content:        userInput,
		MessageType:    "text",
		CreatedAt:      now,
	}
	if err := db.RQ(c).Create(&customerMsg).Error; err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "消息落库失败")
		return
	}
	// P1-2 实时推送：外部渠道客户新消息通知本租户顾问端
	notifyWS(tenantID, customer.ID, conversation.ID, "customer")

	// 4. 持续打标（与站内一致）
	if autoTags, tagErr := service.DefaultTagService.AutoTagFromText(customer.ID, userInput); tagErr == nil && len(autoTags) > 0 {
		var uc model.Customer
		if db.RQ(c).First(&uc, customer.ID).Error == nil {
			service.DefaultTagService.ApplyTagWeightsToTVector(&uc, uc.BuildBaseTVector())
			db.RQ(c).Model(&uc).Update("t_vector", uc.TVectorJSON)
		}
	}

	// 5. 硬边界：无关话题拦截（0延迟，不走AI，不消耗配额）
	if service.IsOffTopic(userInput) {
		reply := service.GetOffTopicReply(userInput)
		persistOpenAPIAIMessage(c, conversation, customer.ID, tenantID, channel, reply, 0, "", "offtopic_hardbound")
		openAIRespond(c, req.Stream, req.Model, reply, estimateTokens(userInput), estimateTokens(reply))
		return
	}

	// 6. 到店倾向/留资（外部渠道同样捕获线索 → 合并+留资+分配顾问）
	if strategy.IsStoreVisitIntent(userInput) && !isCapturedStage(customer.JourneyStage) {
		if phone := phoneRegex.FindString(userInput); phone != "" {
			if mergedID := chatflow.MergeCustomerByPhone(customer, phone); mergedID > 0 {
				var reloaded model.Customer
				if db.RQ(c).First(&reloaded, mergedID).Error == nil {
					customer = &reloaded
					// 重新定位活跃会话（合并后可能切换）
					var conv model.Conversation
					if db.RQ(c).Where("customer_id = ? AND status = ?", customer.ID, "active").
						Order("updated_at DESC").First(&conv).Error == nil {
						conversation = &conv
					}
				}
			}
			applyOpenAPILeadCapture(c, customer, phone)
		}
	}

	// 7. 构建策略输入 → 推理 → 编排层统一话术入口（与顾问触发AI同链路）
	tVector := customer.BuildBaseTVector()
	state := conversation.GetState()
	strategyInput := strategy.StrategyInput{
		TVector:        tVector,
		State:          state,
		CustomerInput:  userInput,
		CustomerTags:   customer.GetTags(),
		CustomerID:     customer.ID,
		ConversationID: conversation.ID,
		CanPromote:     customer.CanPromote(),
		JourneyStage:   customer.JourneyStage,
		TenantID:       tenantID,
		DeptIDs:        nil, // 外部渠道无顾问部门链，纯租户语境（仅见行业+企业两层包）
	}
	strategyOutput := strategy.DefaultEngine.Infer(strategyInput)
	aiReply := flow.DefaultEngine.OrchestrateReply(customer, conversation.ID, userInput, &strategyOutput, nil)

	// 8. 持久化 AI 消息
	persistOpenAPIAIMessage(c, conversation, customer.ID, tenantID, channel, aiReply,
		strategyOutput.FinalAnchor, strategyOutput.TemplateID, "ai_triggered_by_openapi")
	// P1-2 实时推送：外部渠道 AI 回复通知客户端与顾问端
	notifyWS(tenantID, customer.ID, conversation.ID, "ai")

	// 9. 返回 OpenAI 兼容结构（stream=true 走 SSE 逐帧，false 全量 JSON）
	openAIRespond(c, req.Stream, req.Model, aiReply, estimateTokens(userInput), estimateTokens(aiReply))
}

// openAIRespond 按 stream 决定返回形态：
//   - false：直接返回全量 OpenAI chat/completions JSON（与原行为一致）
//   - true：SSE 逐帧流式返回（text/event-stream），兼容 OpenAI streaming 客户端；
//     业务后置处理（引导轮数递减/盲点兜底/反问剥离/意向分）均在生成全量回复后完成，
//     故以分块推送已定稿回复，保证所有业务语义与站内一致（零核心路径改动）。
func openAIRespond(c *gin.Context, stream bool, modelName, content string, promptTokens, completionTokens int) {
	if !stream {
		c.JSON(http.StatusOK, openAICompletion(modelName, content, promptTokens, completionTokens))
		return
	}
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		// 不支持流式刷新则降级全量 JSON
		c.JSON(http.StatusOK, openAICompletion(modelName, content, promptTokens, completionTokens))
		return
	}
	// 按字数切分逐帧推送（每帧一个 token delta），末尾发 [DONE]
	runes := []rune(content)
	step := 4
	for i := 0; i < len(runes); i += step {
		end := i + step
		if end > len(runes) {
			end = len(runes)
		}
		delta := string(runes[i:end])
		frame := gin.H{
			"id":      "chatcmpl-" + time.Now().Format("20060102150405.000000000"),
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   firstNonEmpty(modelName, "ai-scrm"),
			"choices": []gin.H{{
				"index":         0,
				"delta":         gin.H{"role": "assistant", "content": delta},
				"finish_reason": nil,
			}},
		}
		if b, err := json.Marshal(frame); err == nil {
			c.Writer.WriteString("data: " + string(b) + "\n\n")
			flusher.Flush()
		}
	}
	// 结束帧
	done := gin.H{
		"id":      "chatcmpl-" + time.Now().Format("20060102150405.000000000"),
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   firstNonEmpty(modelName, "ai-scrm"),
		"choices": []gin.H{{"index": 0, "delta": gin.H{}, "finish_reason": "stop"}},
	}
	if b, err := json.Marshal(done); err == nil {
		c.Writer.WriteString("data: " + string(b) + "\n\n")
	}
	c.Writer.WriteString("data: [DONE]\n\n")
	flusher.Flush()
}

// firstNonEmpty 返回首个非空字符串
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// resolveOpenAPICustomer external_user_id 租户内解析/创建客户
func resolveOpenAPICustomer(c *gin.Context, tenantID uint, channel, externalUserID string) (*model.Customer, error) {
	var cust model.Customer
	err := db.RQ(c).Where("external_user_id = ?", externalUserID).First(&cust).Error
	if err == nil {
		return &cust, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	name := channel + ":" + externalUserID
	if len(name) > 50 {
		name = name[:50]
	}
	cust = model.Customer{
		TenantID:       tenantID,
		Name:           name,
		Source:         channel,
		ExternalUserID: externalUserID,
		JourneyStage:   model.JourneyAIConnected,
		Status:         1,
		VisitorKey:     model.GenerateVisitorKey(),
	}
	if err := db.RQ(c).Create(&cust).Error; err != nil {
		return nil, err
	}
	return &cust, nil
}

// resolveOpenAPIConversation external_user_id + session_id 解析/创建会话
func resolveOpenAPIConversation(c *gin.Context, tenantID, customerID uint, channel, sessionID string) (*model.Conversation, error) {
	var conv model.Conversation
	q := db.RQ(c).Where("customer_id = ?", customerID)
	if sessionID != "" {
		q = q.Where("session_id = ?", sessionID)
	} else {
		q = q.Where("session_id = '' OR session_id IS NULL")
	}
	err := q.Order("updated_at DESC").First(&conv).Error
	if err == nil {
		return &conv, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	conv = model.Conversation{
		TenantID:       tenantID,
		CustomerID:     customerID,
		Status:         "active",
		Mode:           "ai",
		Channel:        channel,
		SessionID:      sessionID,
		GuidedDisabled: true, // 外部渠道默认关闭引导式反问（渠道侧自管节奏）
	}
	if err := db.RQ(c).Create(&conv).Error; err != nil {
		return nil, err
	}
	return &conv, nil
}

// applyOpenAPILeadCapture 留资标记 + 轮询分配顾问（与站内一致）
func applyOpenAPILeadCapture(c *gin.Context, customer *model.Customer, phone string) {
	leadUpdates := map[string]interface{}{
		"phone":             phone,
		"journey_stage":     model.JourneyLeadCaptured,
		"assignment_reason": "lead_captured",
	}
	if customer.AssignedUserID == 0 {
		var salesUsers []model.User
		db.RQ(c).Where("role = ? AND status = 1", model.RoleSales).Find(&salesUsers)
		if len(salesUsers) > 0 {
			minCount := -1
			best := salesUsers[0].ID
			for _, u := range salesUsers {
				var count int64
				db.RQ(c).Model(&model.Customer{}).Where("assigned_user_id = ? AND status = 1", u.ID).Count(&count)
				if minCount < 0 || int(count) < minCount {
					minCount = int(count)
					best = u.ID
				}
			}
			leadUpdates["assigned_user_id"] = best
		} else {
			leadUpdates["assigned_user_id"] = uint(2) // 兜底：张伟(ID=2)
		}
	}
	if err := db.RQ(c).Model(customer).Updates(leadUpdates).Error; err != nil {
		log.Printf("[OpenAPI] 留资更新失败 customer=%d: %v", customer.ID, err)
		return
	}
	if v, ok := leadUpdates["phone"].(string); ok {
		customer.Phone = v
	}
	if v, ok := leadUpdates["journey_stage"].(string); ok {
		customer.JourneyStage = v
	}
	if v, ok := leadUpdates["assigned_user_id"].(uint); ok {
		customer.AssignedUserID = v
	}
	log.Printf("[OpenAPI] 客户%d 留资成功 channel 线索: phone=%s stage=lead_captured assigned=%d",
		customer.ID, service.MaskPhone(phone), customer.AssignedUserID)
}

// persistOpenAPIAIMessage 持久化 AI 回复并刷新会话
func persistOpenAPIAIMessage(c *gin.Context, conv *model.Conversation, customerID, tenantID uint, channel, content string, anchor int, tid, route string) {
	msg := model.Message{
		TenantID:       tenantID,
		ConversationID: conv.ID,
		CustomerID:     customerID,
		SenderType:     "ai",
		Content:        content,
		MessageType:    "text",
		AnchorType:     anchor,
		TemplateID:     tid,
		RouteResult:    route,
		CreatedAt:      time.Now(),
	}
	if err := db.RQ(c).Create(&msg).Error; err != nil {
		log.Printf("[OpenAPI] AI消息落库失败 conv=%d: %v", conv.ID, err)
		return
	}
	now := time.Now()
	conv.LastMessageAt = &now
	conv.LastTid = tid
	conv.LastAnchorType = anchor
	db.RQ(c).Save(conv)
}

// isCapturedStage 是否已过留资阶段（无需重复捕获）
func isCapturedStage(stage string) bool {
	switch stage {
	case model.JourneyLeadCaptured, model.JourneyArrived, model.JourneyOrdered, model.JourneyDelivered:
		return true
	}
	return false
}

// estimateTokens 粗略 token 估算（中文按字计，仅用于 OpenAI 兼容 usage 字段）
func estimateTokens(s string) int {
	return len([]rune(s))
}

// openAICompletion 组装 OpenAI chat/completions 兼容响应
func openAICompletion(modelName, content string, promptTokens, completionTokens int) gin.H {
	if modelName == "" {
		modelName = "ai-scrm"
	}
	return gin.H{
		"id":      "chatcmpl-" + time.Now().Format("20060102150405.000000000"),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   modelName,
		"choices": []gin.H{{
			"index": 0,
			"message": gin.H{
				"role":    "assistant",
				"content": content,
			},
			"finish_reason": "stop",
		}},
		"usage": gin.H{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		},
	}
}
