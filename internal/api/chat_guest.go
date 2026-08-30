// 对话核心API（客户侧）：C端访客欢迎、留资与会话入口。
package api

// 对话核心API：C端客户与B端销售共用的交互入口，链路为 客户发消息→策略中心7步推理→AI生成回复。
// 含会话竞态保护、三层分流(硬边界/到店快速通道/简单消息)、合并队列、延迟清零、留资检测与OneID合并。

import (
	"ai-scrm/internal/chatflow"
	"ai-scrm/internal/db"
	"ai-scrm/internal/engine/flow"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
	"ai-scrm/internal/mq"
	"ai-scrm/internal/schema"
	"ai-scrm/internal/service"
	"ai-scrm/pkg/utils"
	"context"
	"fmt"
	"log"
	"math/rand"
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
func Welcome(c *gin.Context) {
	var req struct {
		CustomerID uint `json:"customer_id"` // 客户ID，默认1号
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误: "+err.Error())
		return
	}

	// 默认用1号客户
	customerID := req.CustomerID
	if customerID == 0 {
		customerID = 1
	}

	// 查询客户
	var customer model.Customer
	result := db.RQ(c).First(&customer, customerID)
	if result.Error != nil {
		RespErr(c, http.StatusNotFound, 404, "客户不存在")
		return
	}

	// C3：懒下发访客密钥（历史客户可能没有），但匿名访问必须携带匹配密钥
	if customer.VisitorKey == "" {
		customer.VisitorKey = model.GenerateVisitorKey()
		db.RQ(c).Model(&customer).Update("visitor_key", customer.VisitorKey)
	}
	// 横向越权校验：登录用户(顾问/管理员)放行；匿名必须 ?visitor_key= 与目标一致。
	// 注意：Welcome 不向匿名返回 visitor_key（密钥只在 /chat/guest、/chat 创建/加载时下发），
	// 避免凭 customer_id 即可窃取的密钥导致防线失效。
	if !middleware.CheckVisitorKey(c, customer.VisitorKey) {
		RespErr(c, http.StatusForbidden, 403, "无权访问该客户会话")
		return
	}

	// 查找或创建会话（竞态保护，与Chat/ChatTest统一机制）
	var conversation model.Conversation
	isNewConversation := false // 标记是否新创建的会话（返回给前端，供UI区分）

	convMu := chatflow.GetConversationMutex(customer.ID)
	convMu.Lock()

	// 先查该客户是否已有活跃会话
	db.RQ(c).Where("customer_id = ? AND status = ?", customer.ID, "active").
		Order("updated_at DESC").
		Limit(1).Find(&conversation)

	if conversation.ID == 0 {
		// 没有活跃会话 → 创建新会话
		conversation = model.Conversation{
			CustomerID:     customer.ID,
			AssignedUserID: customer.AssignedUserID,
			Status:         "active",
			Mode:           "ai",
			Channel:        "web",
		}
		db.RQ(c).Create(&conversation)
		isNewConversation = true
		log.Printf("[欢迎] 创建新会话 %d, 客户 %d", conversation.ID, customer.ID)

		// 启动默认流程（新会话的初始流程引擎）
		flowCtx := &flow.FlowContext{
			TenantID:       db.EffectiveTenantIDFromGin(c),
			CustomerID:     customer.ID,
			ConversationID: conversation.ID,
			RouteResult:    "ai",
		}
		flow.DefaultEngine.StartFlow("default_chat_flow", flowCtx)
	} else {
		// 已有活跃会话，直接复用
		log.Printf("[欢迎] 复用已有会话 %d, 客户 %d", conversation.ID, customer.ID)
	}

	convMu.Unlock()

	// 创建欢迎消息（每次调用都创建一条新的）
	// 前端控制调用时机：仅在页面打开时调一次，不关闭页面不重复调
	// 非工作时间追加提示，让客户有心理预期
	welcomeText := "你好，请稍等，顾问正在接通中，你可以先说说你的问题。"
	if !utils.IsWorkTime() {
		welcomeText += "由于现在是非工作时间，回复可能较慢，请见谅。"
	}
	welcomeMsg := model.Message{
		ConversationID: conversation.ID,
		CustomerID:     customer.ID,
		SenderType:     "system",
		Content:        welcomeText,
		MessageType:    "text",
		CreatedAt:      time.Now(),
	}
	db.RQ(c).Create(&welcomeMsg)
	log.Printf("[欢迎] 秒回消息已插入, 会话 %d", conversation.ID)

	// 立刻返回（无AI处理，毫秒级响应）
	RespOK(c, "success", gin.H{
		"conversation_id":     conversation.ID,   // 会话ID（供后续 /chat/test 调用时传入）
		"welcome_message":     welcomeMsg,        // 欢迎消息（前端直接渲染到聊天窗口）
		"is_new_conversation": isNewConversation, // 是否新创建的会话（前端可据此做UI区分）
	})
}

// ============================================================
// 访客注册接口 - 每次打开client页面创建新访客
// 业务逻辑：外部体验者打开页面 → 自动创建临时访客客户 → 拿到customer_id开始聊天
// 留资时如果手机号匹配到老客户 → OneID合并（聊天记录+标签+线索全部迁移到老客户）
// ============================================================

// CreateGuest 创建访客客户（免登录）
// POST /api/v1/chat/guest
// publishConversationMsg 已迁至 chat_notify.go（P2 巨石拆分第一步，纯同包搬迁，行为不变）

// CreateGuest POST /api/v1/chat/guest 访客自动注册（免登录；TurnstileGuard 视开关前置拦截）
func CreateGuest(c *gin.Context) {
	// 生成唯一访客名：访客_后4位随机数
	suffix := rand.Intn(9000) + 1000 // 1000-9999
	guestName := fmt.Sprintf("访客_%d", suffix)

	// 修复：未留资访客不分配顾问（铁律：未留资→AI接管不给顾问）
	// 只有留资后才分配顾问，未留资访客AssignedUserID=0
	// 顾问端只看到assigned_user_id > 0的客户，未留资访客不可见
	var assignedUserID uint = 0 // 未留资访客不分配顾问

	customer := model.Customer{
		Name:             guestName,
		CustomerType:     "potential",
		JourneyStage:     model.JourneyAIConnected, // AI建联（起点）
		Source:           "外部体验",
		TrustLevel:       0.3,
		IntentScore:      0.2,
		PriceSensitivity: 0.5,
		BrandAwareness:   0.3,
		ResistanceType:   "none",
		AssignedUserID:   assignedUserID,             // 0=未分配顾问，留资后才分配
		VisitorKey:       model.GenerateVisitorKey(), // C3：创建即下发访客密钥
		Status:           1,
	}

	if err := db.RQ(c).Create(&customer).Error; err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "创建访客失败: "+err.Error())
		return
	}

	log.Printf("[访客注册] 新访客创建成功: ID=%d, Name=%s, 分配顾问=%d", customer.ID, service.MaskName(customer.Name), customer.AssignedUserID)

	// 缺口4修复（2026-08-22）：guest_created 事件上行（激活 CDP idm_guest 标签）
	// 此前 IngestConsumer 支持该事件但全仓无发布点，访客身份标签是死代码
	if err := mq.Publish(context.Background(), mq.TopicUserEvent, middleware.EffectiveTenantID(c),
		fmt.Sprintf("c:%d", customer.ID), "guest_created",
		mq.UserEvent{EventType: "identity", EventName: "guest_created", AnchorType: "device",
			Attributes: map[string]any{"customer_id": customer.ID, "source": customer.Source},
			OccurredAt: time.Now()}); err != nil {
		log.Printf("[MQ] guest_created 事件发布失败: %v", err)
	}

	c.JSON(http.StatusOK, gin.H{
		"code":        0,
		"customer_id": customer.ID,
		"name":        customer.Name,
		"visitor_key": customer.VisitorKey, // C3: 返回密钥供客户端持久化并在 history/welcome 携带
		"message":     "访客创建成功",
	})
}

// ============================================================
// OneID合并 - 留资时手机号匹配到老客户，合并所有数据
// 业务逻辑：
// 1. 访客A留资提供手机号 → 查到老客户B同手机号
// 2. 把访客A的会话、消息、线索(FollowUp)全部迁移到老客户B
// 3. 合并标签（去重）
// 4. 取最高旅程阶段
// 5. 删除访客A（标记无效）
// 6. 返回老客户B的ID，前端切换

// ============================================================
// ClearDelay 清除客户所有待发延迟消息，实现立即回复
// 修复问题3：顾问/管理员点击"立即回复"按钮时调用
// 设计：通过channel机制通知正在sleep的goroutine立即结束延迟
// 不同于DB-based的message_queue方案，本系统延迟在goroutine内实现
// 所以用channel取消机制替代DB scheduled_at更新
// ============================================================
func ClearDelay(c *gin.Context) {
	var req struct {
		CustomerID uint `json:"customer_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误", Data: nil})
		return
	}

	// 取消该客户的当前延迟（如果有）
	chatflow.CancelDelay(req.CustomerID)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "已清除延迟，消息将立即发出",
		Data:    nil,
	})
}
