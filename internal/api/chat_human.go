// 对话核心API（人工）：人工接管与转人工相关处理端点。
package api

// 对话核心API：C端客户与B端销售共用的交互入口，链路为 客户发消息→策略中心7步推理→AI生成回复。
// 含会话竞态保护、三层分流(硬边界/到店快速通道/简单消息)、合并队列、延迟清零、留资检测与OneID合并。

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/schema"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 会话创建互斥锁（解决并发请求重复创建会话的竞态问题）
//
// 场景：客户连发多条消息时，多个并发请求同时到达，
// 如果都看到 conversation_id=0，会各自创建新会话，
// 导致同一客户出现多个活跃会话。用客户级互斥锁确保：
//  1. 同一客户同一时刻只有一个请求执行"查找或创建会话"
//  2. 先查已有活跃会话，有则复用；没有才创建新会话+冷启动秒回
//  3. Chat() 和 ChatTest() 统一使用此机制，行为一致
//
// ============================================================

// ============================================================
// 延迟清零（立即回复）机制
// 修复问题3：顾问/管理员点击"立即回复"按钮时，跳过当前客户的模拟真人延迟
// 设计：用sync.Map存储每个客户的延迟取消通道，当ClearDelay被调用时发送信号
// time.Sleep替换为select-based的可取消延迟，收到取消信号后立即继续
// ============================================================

// ============================================================
// 对话相关API
// 这是系统的核心交互接口，C端客户和B端销售共用
// 核心流程：客户发消息 → 策略中心决策 → AI生成回复 → 返回结果
// ============================================================

// Chat 对话接口（客户发消息，获取AI回复）
//
// 完整执行流程：
// 1. 验证客户身份
// 2. 获取或创建会话
// 3. 保存客户消息
// 4. 调用策略中心引擎（7步推理）
// 5. 根据路由结果决定：AI回复 / 转人工 / 养鱼
// 6. 调用AI生成最终回复（模拟模式用规则）
// 7. 更新客户画像（意向分等）
// 8. 更新会话状态
// 9. 返回结果

// Chat POST /api/v1/chat 正式对话入口（JWT链；硬边界→快速通道→简单消息→合并队列四层分流）
func HumanReply(c *gin.Context) {
	var req struct {
		ConversationID uint   `json:"conversation_id" binding:"required"`
		Content        string `json:"content" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误", Data: nil})
		return
	}

	// 获取会话
	var conversation model.Conversation
	result := db.RQ(c).First(&conversation, req.ConversationID)
	if result.Error != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "会话不存在", Data: nil})
		return
	}

	// 切换到人工模式并锁定
	conversation.Mode = "human"
	conversation.IsHumanLocked = true
	conversation.PendingHandoff = false // 人工回复后，清除待接管状态
	conversation.HandoffNotifiedAt = nil
	now := time.Now()
	conversation.LastHumanReplyAt = &now
	conversation.LastMessageAt = &now
	db.RQ(c).Save(&conversation)

	// 保存人工消息
	humanMsg := model.Message{
		ConversationID: conversation.ID,
		CustomerID:     conversation.CustomerID,
		SenderType:     "human",
		Content:        req.Content,
		MessageType:    "text",
		CreatedAt:      now,
	}
	db.RQ(c).Create(&humanMsg)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "发送成功",
		Data:    humanMsg,
	})
}

// GetMessages 获取会话消息列表
func GetMessages(c *gin.Context) {
	conversationID := c.Param("id")

	var messages []model.Message
	db.RQ(c).Where("conversation_id = ?", conversationID).
		Order("created_at ASC").
		Limit(200).
		Find(&messages)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data:    messages,
	})
}

// GetConversationList 获取会话列表
func GetConversationList(c *gin.Context) {
	var req schema.ConversationListRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误", Data: nil})
		return
	}

	query := db.RQ(c).Model(&model.Conversation{})

	if req.CustomerID > 0 {
		query = query.Where("customer_id = ?", req.CustomerID)
	}
	if req.Status != "" {
		query = query.Where("status = ?", req.Status)
	}
	if req.Mode != "" {
		query = query.Where("mode = ?", req.Mode)
	}

	var total int64
	query.Count(&total)

	var conversations []model.Conversation
	query.Order("updated_at DESC").
		Offset(req.GetOffset()).
		Limit(req.PageSize).
		Find(&conversations)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data: schema.PageResponse{
			Total:    total,
			Page:     req.Page,
			PageSize: req.PageSize,
			List:     conversations,
		},
	})
}

// TransferToHuman 手动转人工
func TransferToHuman(c *gin.Context) {
	var req struct {
		ConversationID uint `json:"conversation_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误", Data: nil})
		return
	}

	var conversation model.Conversation
	result := db.RQ(c).First(&conversation, req.ConversationID)
	if result.Error != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "会话不存在", Data: nil})
		return
	}

	conversation.Mode = "human"
	conversation.IsHumanLocked = true
	db.RQ(c).Save(&conversation)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "已转人工",
		Data:    conversation,
	})
}

// TransferToAI 切回AI
func TransferToAI(c *gin.Context) {
	var req struct {
		ConversationID uint `json:"conversation_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误", Data: nil})
		return
	}

	var conversation model.Conversation
	result := db.RQ(c).First(&conversation, req.ConversationID)
	if result.Error != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "会话不存在", Data: nil})
		return
	}

	conversation.Mode = "ai"
	conversation.IsHumanLocked = false
	db.RQ(c).Save(&conversation)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "已切回AI",
		Data:    conversation,
	})
}

// ============================================================
// AI测试接口（免登录，方便调试）
// 与正式 Chat() 接口统一使用会话竞态保护 + 冷启动秒回 + 合并状态返回
// ============================================================

// ChatTest AI对话测试接口
// 不需要登录token，直接模拟一轮对话
// 用途：快速验证AI接入、冷启动秒回、消息合并等是否正常
//
// 修复内容：
//  1. Bug 2 修复：加入会话竞态保护 + 冷启动秒回（与正式接口统一）
//     原Bug：测试接口完全没有会话管理，冷启动秒回不会出现
//     修复：用客户级互斥锁先查再建，新客户触发秒回，已有客户复用会话
//  2. Bug 1 修复：合并请求只返回合并状态，不返回完整回复
//     原Bug：三条并发请求都返回同一段 ai_reply，看起来像三条重复消息
//     修复：merged 请求只返回 merged=true 标记，主请求返回完整结果
