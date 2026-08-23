package api

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/engine/flow"
	"ai-scrm/internal/engine/strategy"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
	"ai-scrm/internal/schema"
	"ai-scrm/internal/service"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 顾问工作台API（销售端专用）
// 路由前缀：/api/v1/advisor/
// 功能：客户管理、工作台数据、跟进提醒、对话接管、策略话术推荐
// 暂不鉴权，后续再加
// ============================================================

// ---- 请求结构体 ----

// advisorCustomerListRequest 顾问端客户列表请求
type advisorCustomerListRequest struct {
	Page     int    `form:"page" json:"page"`
	PageSize int    `form:"page_size" json:"page_size"`
	Status   string `form:"status" json:"status"`   // 筛选状态：following/pending/arrived/test_drive/quoted/all
	Keyword  string `form:"keyword" json:"keyword"` // 搜索关键词
	UserID   uint   `form:"user_id" json:"user_id"` // 销售ID（筛选该销售名下客户）
}

// editTagsRequest 编辑客户标签请求
type editTagsRequest struct {
	Tags []string `json:"tags" binding:"required"` // 标签名称列表（覆盖更新）
}

// editCustomerInfoRequest 编辑客户信息请求
type editCustomerInfoRequest struct {
	Name          string  `json:"name"`           // 客户姓名
	Phone         string  `json:"phone"`          // 手机号
	Age           int     `json:"age"`            // 年龄
	Gender        int     `json:"gender"`         // 性别: 0-未知 1-男 2-女
	Region        string  `json:"region"`         // 地域
	City          string  `json:"city"`           // 城市
	Career        string  `json:"career"`         // 职业
	InterestModel string  `json:"interest_model"` // 兴趣车型
	Budget        float64 `json:"budget"`         // 预算（万元）
	Remark        string  `json:"remark"`         // 备注
	JourneyStage  string  `json:"journey_stage"`  // 客户旅程阶段
}

// followupRequest 设置跟进提醒请求
type followupRequest struct {
	CustomerID   uint   `json:"customer_id" binding:"required"` // 客户ID
	Type         string `json:"type"`                           // 类型: manual/ai_triggered
	Method       string `json:"method"`                         // 方式: phone/wechat/store/email
	Content      string `json:"content"`                        // 跟进内容/提醒内容
	NextFollowAt string `json:"next_follow_at"`                 // 下次跟进时间（RFC3339格式）
}

// takeoverRequest 一键接管请求
type takeoverRequest struct {
	ConversationID uint `json:"conversation_id" binding:"required"` // 会话ID
}

// advisorSendMsgRequest 顾问发送消息请求
type advisorSendMsgRequest struct {
	ConversationID uint   `json:"conversation_id" binding:"required"` // 会话ID
	Content        string `json:"content" binding:"required"`         // 消息内容
}

// strategyRecommendRequest 策略话术推荐请求
type strategyRecommendRequest struct {
	CustomerID     uint `json:"customer_id" binding:"required"` // 客户ID
	ConversationID uint `json:"conversation_id"`                // 会话ID（可选）
}

// ============================================================
// 工作台数据统计
// GET /api/v1/advisor/stats?user_id=X
// 返回销售的核心指标：跟进中/已到店/已试驾/已报价等
// ============================================================
func GetAdvisorStats(c *gin.Context) {
	// user_id参数：指定顾问ID，只统计分配给该顾问的客户
	userIDStr := c.Query("user_id")
	userID, _ := strconv.Atoi(userIDStr)

	// 修复（越权）：非admin角色一律用JWT里解出来的真实身份覆盖，
	// 杜绝伪造user_id查看其他顾问的统计数据
	_, _, role := middleware.CurrentUser(c)
	if !(role == model.RoleTenantAdmin || role == model.RoleSuperAdmin) {
		jwtUserID, _ := c.Get("user_id")
		if uid, ok := jwtUserID.(uint); ok {
			userID = int(uid)
		}
	}

	// 修复：统计也只算分配给该顾问的客户（与客户列表过滤逻辑一致）
	assignedFilter := "assigned_user_id > 0"
	if userID > 0 {
		assignedFilter = fmt.Sprintf("assigned_user_id = %d", userID)
	}

	// ---- 核心指标统计 ----
	type statItem struct {
		Label string `json:"label"` // 指标名
		Value int64  `json:"value"` // 数值
		Color string `json:"color"` // 前端展示颜色标识
	}

	var stats []statItem

	// 跟进中客户数（分配给该顾问的活跃客户）
	var followingCount int64
	db.RQ(c).Model(&model.Customer{}).
		Where("status = 1 AND " + assignedFilter).
		Count(&followingCount)
	stats = append(stats, statItem{Label: "跟进中", Value: followingCount, Color: "blue"})

	// 已到店客户数
	var arrivedCount int64
	db.RQ(c).Model(&model.Customer{}).
		Where("status = 1 AND journey_stage = ? AND "+assignedFilter, "arrived").
		Count(&arrivedCount)
	stats = append(stats, statItem{Label: "已到店", Value: arrivedCount, Color: "green"})

	// 已试驾客户数（到店+已试驾子状态）
	var testDriveCount int64
	db.RQ(c).Model(&model.Customer{}).
		Where("status = 1 AND journey_stage = ? AND journey_sub_stage IN ? AND "+assignedFilter, "arrived", []string{"test_driven", "quoted"}).
		Count(&testDriveCount)
	stats = append(stats, statItem{Label: "已试驾", Value: testDriveCount, Color: "purple"})

	// 已下单客户数
	var orderedCount int64
	db.RQ(c).Model(&model.Customer{}).
		Where("status = 1 AND journey_stage IN ? AND "+assignedFilter, []string{model.JourneyOrdered, model.JourneyDelivered}).
		Count(&orderedCount)
	stats = append(stats, statItem{Label: "已下单", Value: orderedCount, Color: "orange"})

	// 今日待跟进
	var todayFollowupCount int64
	todayStart := time.Now().Truncate(24 * time.Hour)
	todayEnd := todayStart.Add(24 * time.Hour)
	db.RQ(c).Model(&model.FollowUp{}).
		Where("next_follow_at >= ? AND next_follow_at < ?", todayStart, todayEnd).
		Count(&todayFollowupCount)
	stats = append(stats, statItem{Label: "今日待跟进", Value: todayFollowupCount, Color: "red"})

	// 逾期未跟进
	var overdueCount int64
	db.RQ(c).Model(&model.FollowUp{}).
		Where("next_follow_at < ? AND next_follow_at IS NOT NULL", time.Now()).
		Count(&overdueCount)
	stats = append(stats, statItem{Label: "逾期未跟进", Value: overdueCount, Color: "red"})

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data:    stats,
	})
}

// ============================================================
// 切换AI回复开关（顾问手动控制AI是否自动回复）
// POST /api/v1/advisor/chat/toggle-ai-reply
// 场景：顾问忙碌时关闭AI回复，空闲或下班时打开AI接住客户
// ============================================================
func ToggleAiReply(c *gin.Context) {
	var req struct {
		ConversationID uint  `json:"conversation_id" binding:"required"`
		Enabled        *bool `json:"enabled"` // nil=切换, true=开, false=关
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误: " + err.Error()})
		return
	}

	var conversation model.Conversation
	if err := db.RQ(c).First(&conversation, req.ConversationID).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "会话不存在"})
		return
	}

	var customer model.Customer
	if err := db.RQ(c).First(&customer, conversation.CustomerID).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "客户不存在"})
		return
	}

	if !canOperateCustomer(c, customer.AssignedUserID) {
		c.JSON(http.StatusForbidden, schema.Response{Code: 403, Message: "无权操作该客户"})
		return
	}

	if req.Enabled != nil {
		conversation.IsAiReplyEnabled = *req.Enabled
	} else {
		conversation.IsAiReplyEnabled = !conversation.IsAiReplyEnabled
	}

	if conversation.IsAiReplyEnabled {
		conversation.Mode = "ai"
		conversation.IsHumanLocked = false
	} else {
		conversation.Mode = "human"
		conversation.IsHumanLocked = true
	}

	db.RQ(c).Model(&conversation).Updates(map[string]interface{}{
		"is_ai_reply_enabled": conversation.IsAiReplyEnabled,
		"mode":                conversation.Mode,
		"is_human_locked":     conversation.IsHumanLocked,
	})

	status := "开启"
	if !conversation.IsAiReplyEnabled {
		status = "关闭"
	}
	log.Printf("[顾问切换AI回复] 会话%d %sAI回复, mode=%s, locked=%v",
		conversation.ID, status, conversation.Mode, conversation.IsHumanLocked)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "已" + status + "AI回复",
		Data: gin.H{
			"is_ai_reply_enabled": conversation.IsAiReplyEnabled,
			"mode":                conversation.Mode,
			"is_human_locked":     conversation.IsHumanLocked,
		},
	})
}

// ============================================================
// 试驾单相关 API
// ============================================================

type createTestDriveRequest struct {
	CustomerID   uint   `json:"customer_id" binding:"required"`
	ScheduledAt  string `json:"scheduled_at" binding:"required"` // RFC3339
	ModelName    string `json:"model_name"`
	ContactName  string `json:"contact_name"`
	ContactPhone string `json:"contact_phone"`
	Location     string `json:"location"`
	Note         string `json:"note"`
}

// CreateTestDrive POST /api/v1/advisor/test-drive 创建试驾单
func CreateTestDrive(c *gin.Context) {
	var req createTestDriveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误: " + err.Error()})
		return
	}

	var customer model.Customer
	if err := db.RQ(c).First(&customer, req.CustomerID).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "客户不存在"})
		return
	}

	if !canOperateCustomer(c, customer.AssignedUserID) {
		c.JSON(http.StatusForbidden, schema.Response{Code: 403, Message: "无权操作该客户"})
		return
	}

	scheduledAt, err := time.Parse(time.RFC3339, req.ScheduledAt)
	if err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "预约时间格式错误，请使用RFC3339格式"})
		return
	}

	td := model.TestDrive{
		CustomerID:   req.CustomerID,
		AdvisorID:    customer.AssignedUserID,
		Status:       "pending",
		ScheduledAt:  scheduledAt,
		ModelName:    req.ModelName,
		ContactName:  req.ContactName,
		ContactPhone: req.ContactPhone,
		Location:     req.Location,
		Note:         req.Note,
	}
	if td.AdvisorID == 0 {
		jwtUserID, _, _ := middleware.CurrentUser(c)
		td.AdvisorID = jwtUserID
	}
	if td.ContactName == "" {
		td.ContactName = customer.Name
	}
	if td.ContactPhone == "" {
		td.ContactPhone = customer.Phone
	}

	db.RQ(c).Create(&td)
	log.Printf("[试驾单] 创建试驾单 #%d 客户%d 顾问%d 时间%s", td.ID, td.CustomerID, td.AdvisorID, td.ScheduledAt.Format("2006-01-02 15:04"))

	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "试驾单创建成功", Data: td})
}

// GetTestDrives GET /api/v1/advisor/test-drives 试驾单列表（按客户筛选）
func GetTestDrives(c *gin.Context) {
	customerID, _ := strconv.Atoi(c.Query("customer_id"))
	status := c.Query("status")

	query := db.RQ(c).Model(&model.TestDrive{})

	if customerID > 0 {
		query = query.Where("customer_id = ?", customerID)
	}
	if status != "" {
		query = query.Where("status = ?", status)
	}

	_, _, role := middleware.CurrentUser(c)
	if !(role == model.RoleTenantAdmin || role == model.RoleSuperAdmin) {
		jwtUserID, _ := c.Get("user_id")
		if uid, ok := jwtUserID.(uint); ok {
			query = query.Where("advisor_id = ?", uid)
		}
	}

	var list []model.TestDrive
	query.Order("scheduled_at DESC").Find(&list)

	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "success", Data: list})
}

// GetTestDrive GET /api/v1/advisor/test-drive/:id 试驾单详情
func GetTestDrive(c *gin.Context) {
	id := c.Param("id")
	var td model.TestDrive
	if err := db.RQ(c).First(&td, id).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "试驾单不存在"})
		return
	}

	if !customerInDataScope(c, td.AdvisorID) {
		c.JSON(http.StatusForbidden, schema.Response{Code: 403, Message: "无权查看"})
		return
	}

	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "success", Data: td})
}

type updateTestDriveRequest struct {
	Status       *string `json:"status"`       // pending/completed/cancelled
	ScheduledAt  *string `json:"scheduled_at"` // RFC3339
	ModelName    *string `json:"model_name"`
	ContactName  *string `json:"contact_name"`
	ContactPhone *string `json:"contact_phone"`
	Location     *string `json:"location"`
	Note         *string `json:"note"`
	Result       *string `json:"result"`
}

// UpdateTestDrive PUT /api/v1/advisor/test-drive/:id 更新试驾单（状态流转）
func UpdateTestDrive(c *gin.Context) {
	id := c.Param("id")
	var td model.TestDrive
	if err := db.RQ(c).First(&td, id).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "试驾单不存在"})
		return
	}

	if !canOperateCustomer(c, td.AdvisorID) {
		c.JSON(http.StatusForbidden, schema.Response{Code: 403, Message: "无权修改"})
		return
	}

	var req updateTestDriveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误: " + err.Error()})
		return
	}

	if req.Status != nil {
		td.Status = *req.Status
	}
	if req.ScheduledAt != nil {
		t, err := time.Parse(time.RFC3339, *req.ScheduledAt)
		if err == nil {
			td.ScheduledAt = t
		}
	}
	if req.ModelName != nil {
		td.ModelName = *req.ModelName
	}
	if req.ContactName != nil {
		td.ContactName = *req.ContactName
	}
	if req.ContactPhone != nil {
		td.ContactPhone = *req.ContactPhone
	}
	if req.Location != nil {
		td.Location = *req.Location
	}
	if req.Note != nil {
		td.Note = *req.Note
	}
	if req.Result != nil {
		td.Result = *req.Result
	}

	db.RQ(c).Save(&td)
	log.Printf("[试驾单] 更新试驾单 #%d 状态=%s", td.ID, td.Status)

	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "更新成功", Data: td})
}

// ============================================================
// 客户列表（顾问端）
// GET /api/v1/advisor/customers
// 支持按状态筛选、关键词搜索
// ============================================================

// customerInDataScope 校验客户是否落在当前用户的数据范围内（四级组织树）
// 返回 false 时调用方应返回 404（不泄露存在性）
func customerInDataScope(c *gin.Context, assignedUserID uint) bool {
	roleV, _ := c.Get("role")
	role, _ := roleV.(string)
	switch role {
	case model.RoleTenantAdmin, model.RoleSuperAdmin:
		return true
	case model.RoleUser:
		uidV, _ := c.Get("user_id")
		uid, _ := uidV.(uint)
		return assignedUserID == uid && assignedUserID > 0
	case model.RoleDeptAdmin, model.RoleReadOnly:
		pathV, _ := c.Get("dept_path")
		myPath, _ := pathV.(string)
		if myPath == "" || assignedUserID == 0 {
			return false
		}
		var cnt int64
		// 注意：JOIN 查询不能走 RQ（其注入的裸 tenant_id 会与两表列名歧义），
		// 改为显式 u.tenant_id 条件
		db.DB.Table("tenant_users u").
			Joins("LEFT JOIN departments d ON u.department_id = d.id").
			Where("u.tenant_id = ? AND u.id = ? AND d.path LIKE ?",
				db.EffectiveTenantIDFromGin(c), assignedUserID, myPath+"%").
			Count(&cnt)
		return cnt > 0
	}
	return false
}

// canOperateCustomer 敏感/写操作归属判定（四级组织语义）
// tenant_admin/super：全租户可操作；user：仅本人名下；dept_admin：本部门子树；
// readonly：拒绝（写操作另有 ReadonlyWriteGuard 前置拦截，此处兜底）
func canOperateCustomer(c *gin.Context, assignedUserID uint) bool {
	roleV, _ := c.Get("role")
	role, _ := roleV.(string)
	switch role {
	case model.RoleTenantAdmin, model.RoleSuperAdmin:
		return true
	case model.RoleUser:
		uidV, _ := c.Get("user_id")
		uid, _ := uidV.(uint)
		return assignedUserID == uid && assignedUserID > 0
	case model.RoleDeptAdmin:
		pathV, _ := c.Get("dept_path")
		myPath, _ := pathV.(string)
		if myPath == "" || assignedUserID == 0 {
			return false
		}
		var cnt int64
		// 注意：JOIN 查询不能走 RQ（其注入的裸 tenant_id 会与两表列名歧义），
		// 改为显式 u.tenant_id 条件
		db.DB.Table("tenant_users u").
			Joins("LEFT JOIN departments d ON u.department_id = d.id").
			Where("u.tenant_id = ? AND u.id = ? AND d.path LIKE ?",
				db.EffectiveTenantIDFromGin(c), assignedUserID, myPath+"%").
			Count(&cnt)
		return cnt > 0
	}
	return false
}

// GetAdvisorCustomers GET /api/v1/advisor/customers 工作台客户列表（按角色数据范围裁剪）
func GetAdvisorCustomers(c *gin.Context) {
	var req advisorCustomerListRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误", Data: nil})
		return
	}

	if req.Page <= 0 {
		req.Page = 1
	}
	if req.PageSize <= 0 {
		req.PageSize = 20
	}

	// 租户隔离（SaaS 安全红线）：Customer 表已含 tenant_id 列，
	// 经 TenantConsistency 后 Context 中必为生效租户，此处强制注入过滤
	query := db.RQ(c).Model(&model.Customer{}).Scopes(db.T(c), db.DataScope(c))

	// 四级数据范围（P2 组织树）：普通用户强制本人；dept_admin/readonly 由
	// db.DataScope 按部门子树过滤；tenant_admin/super_admin 可用 req.UserID 切换视角
	roleV, _ := c.Get("role")
	role, _ := roleV.(string)
	if role == model.RoleUser {
		jwtUserID, _ := c.Get("user_id")
		if uid, ok := jwtUserID.(uint); ok {
			req.UserID = uid
		}
	}

	// 修复：顾问只能看到策略引擎分配给自己的客户
	// 业务规则：策略引擎确认需要人工接管/留资成功后，才把客户分配给顾问
	// 分配之前，顾问看不到任何新客户信息、标签和聊天记录
	// assigned_user_id > 0 表示该客户已被分配给某位顾问
	// 修复问题1：admin后台需要看到所有线索（含未分配的），通过assigned=all参数控制
	// 修复（越权）：assigned=all 只对admin角色生效，普通顾问传这个参数也不能绕过
	assignedParam := c.Query("assigned")
	if assignedParam != "all" || (role != model.RoleTenantAdmin && role != model.RoleSuperAdmin) {
		query = query.Where("assigned_user_id > 0")
	}
	if req.UserID > 0 {
		// 如果指定了顾问ID，进一步过滤只看自己的客户
		query = query.Where("assigned_user_id = ?", req.UserID)
	}

	// 按状态筛选——修复：用新状态机journey_stage替代旧字段
	// 新增：待跟进/跟进中 映射到人工跟进子状态
	switch req.Status {
	case "following":
		// 跟进中：人工跟进-跟进中的客户（journey_sub_stage=following）
		query = query.Where("status = 1 AND journey_sub_stage = ?", "following")
	case "pending":
		// 待跟进：人工跟进-未跟进的客户（子状态为空或not_followed）
		query = query.Where("status = 1 AND (journey_sub_stage = '' OR journey_sub_stage = 'not_followed' OR journey_sub_stage IS NULL)")
	case "lead_captured":
		// 已留资：客户已留资待人工接管
		query = query.Where("status = 1 AND journey_stage = ?", "lead_captured")
	case "arrived":
		// 已到店
		query = query.Where("status = 1 AND journey_stage = ?", "arrived")
	case "test_drive":
		// 已试驾：到店且子状态为test_driven及以上
		query = query.Where("status = 1 AND journey_stage = ? AND journey_sub_stage IN ?", "arrived", []string{"test_driven", "quoted"})
	default:
		// 全部活跃客户
		query = query.Where("status = 1")
	}

	// 关键词搜索
	if req.Keyword != "" {
		kw := "%" + req.Keyword + "%"
		query = query.Where("name LIKE ? OR phone LIKE ?", kw, kw)
	}

	var total int64
	query.Count(&total)

	var customers []model.Customer
	query.Order("updated_at DESC").
		Offset((req.Page - 1) * req.PageSize).
		Limit(req.PageSize).
		Find(&customers)

	// 为每个客户附加最后一条消息、会话模式、线索状态和分配顾问姓名
	type customerWithExtra struct {
		model.Customer
		LastMessage      string `json:"last_message"`       // 最近一条消息
		ConvMode         string `json:"conv_mode"`          // 当前会话模式 ai/human
		LeadStatus       string `json:"lead_status"`        // 线索状态
		LeadSubStatus    string `json:"lead_sub_status"`    // 到店子状态描述
		AssignedUserName string `json:"assigned_user_name"` // 分配顾问姓名（修复问题3：销售端显示分配给哪个顾问）
		LastMessageAt    string `json:"last_message_at"`    // 最近消息时间，前端用来判断是否有新消息
	}

	result := make([]customerWithExtra, 0, len(customers))
	for _, cust := range customers {
		extra := customerWithExtra{Customer: cust}

		// 修复问题3：查分配顾问的姓名
		if cust.AssignedUserID > 0 {
			var assignedUser model.User
			db.RQ(c).Select("id, real_name, username").Limit(1).Find(&assignedUser, cust.AssignedUserID)
			if assignedUser.ID > 0 {
				extra.AssignedUserName = assignedUser.RealName
				if extra.AssignedUserName == "" {
					extra.AssignedUserName = assignedUser.Username
				}
			}
		}

		// 查最近一条消息
		var lastMsg model.Message
		if err := db.RQ(c).Where("customer_id = ?", cust.ID).
			Order("created_at DESC").Limit(1).Find(&lastMsg).Error; err == nil {
			extra.LastMessage = lastMsg.Content
			if len(extra.LastMessage) > 50 {
				extra.LastMessage = extra.LastMessage[:50] + "..."
			}
		}

		// 查当前会话模式 + 最后消息时间
		var conv model.Conversation
		db.RQ(c).Where("customer_id = ? AND status = ?", cust.ID, "active").
			Order("updated_at DESC").Limit(1).Find(&conv)
		if conv.ID > 0 {
			extra.ConvMode = conv.Mode
			if conv.LastMessageAt != nil {
				extra.LastMessageAt = conv.LastMessageAt.Format("2006-01-02T15:04:05Z07:00")
			}
		}

		// 线索状态：从journey_stage映射中文显示名
		extra.LeadStatus = cust.GetJourneyStageName()
		// 到店子状态描述
		switch cust.JourneySubStage {
		case model.SubStageTestDrive:
			extra.LeadSubStatus = "已试驾"
		case model.SubStageQuoted:
			extra.LeadSubStatus = "已报价"
		default:
			extra.LeadSubStatus = ""
		}

		result = append(result, extra)
	}

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data: schema.PageResponse{
			Total:    total,
			Page:     req.Page,
			PageSize: req.PageSize,
			List:     result,
		},
	})
}

// ============================================================
// 客户详情（顾问端）
// GET /api/v1/advisor/customer/:id
// 包含客户基本信息、标签列表、画像数据、最近会话
// ============================================================
func GetAdvisorCustomerDetail(c *gin.Context) {
	id := c.Param("id")

	var customer model.Customer
	if err := db.RQ(c).First(&customer, id).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "客户不存在", Data: nil})
		return
	}
	// 四级数据范围门禁：范围外客户视为不存在（不泄露存在性）
	if !customerInDataScope(c, customer.AssignedUserID) {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "客户不存在", Data: nil})
		return
	}

	// 修复（越权）：之前任何顾问传别人的customer id都能看到完整详情+聊天记录。
	// 现在非admin角色必须是这个客户的指派顾问才能看，否则403。
	if !customerInDataScope(c, customer.AssignedUserID) {
		c.JSON(http.StatusForbidden, schema.Response{Code: 403, Message: "无权查看该客户", Data: nil})
		return
	}

	// 获取客户标签
	var customerTags []model.CustomerTag
	db.RQ(c).Where("customer_id = ?", customer.ID).Find(&customerTags)

	// 获取跟进记录
	var followups []model.FollowUp
	db.RQ(c).Where("customer_id = ?", customer.ID).
		Order("created_at DESC").Limit(20).Find(&followups)

	// 获取活跃会话
	var conversations []model.Conversation
	db.RQ(c).Where("customer_id = ?", customer.ID).
		Order("updated_at DESC").Limit(5).Find(&conversations)

	// 计算各类型跟进次数统计
	type followupStat struct {
		Type  string `json:"type"`
		Count int64  `json:"count"`
	}
	var followupStats []followupStat
	db.RQ(c).Model(&model.FollowUp{}).
		Select("method, count(*) as count").
		Where("customer_id = ?", customer.ID).
		Group("method").
		Scan(&followupStats)

	// 修复问题3：查分配顾问姓名，详情页也要显示
	assignedUserName := ""
	if customer.AssignedUserID > 0 {
		var assignedUser model.User
		if err := db.RQ(c).Select("id, real_name, username").First(&assignedUser, customer.AssignedUserID).Error; err == nil {
			assignedUserName = assignedUser.RealName
			if assignedUserName == "" {
				assignedUserName = assignedUser.Username
			}
		}
	}

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data: gin.H{
			"customer":           customer,
			"assigned_user_name": assignedUserName, // 修复问题3：分配顾问姓名
			"tags":               customerTags,
			"followups":          followups,
			"conversations":      conversations,
			"followup_stats":     followupStats,
		},
	})
}

// ============================================================
// 编辑客户标签
// PUT /api/v1/advisor/customer/:id/tags
// 覆盖更新客户标签（传入完整标签列表）
// ============================================================
func EditCustomerTags(c *gin.Context) {
	id := c.Param("id")

	var customer model.Customer
	if err := db.RQ(c).First(&customer, id).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "客户不存在", Data: nil})
		return
	}
	// 四级数据范围门禁：范围外客户视为不存在（不泄露存在性）
	if !customerInDataScope(c, customer.AssignedUserID) {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "客户不存在", Data: nil})
		return
	}

	var req editTagsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误: " + err.Error(), Data: nil})
		return
	}

	// 使用打标服务更新标签
	if err := service.DefaultTagService.ApplyTagsToCustomer(customer.ID, req.Tags, "manual"); err != nil {
		c.JSON(http.StatusInternalServerError, schema.Response{Code: 500, Message: "更新标签失败", Data: nil})
		return
	}

	// 返回更新后的标签列表
	var updatedTags []model.CustomerTag
	db.RQ(c).Where("customer_id = ?", customer.ID).Find(&updatedTags)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "标签更新成功",
		Data:    updatedTags,
	})
}

// ============================================================
// 编辑客户信息
// PUT /api/v1/advisor/customer/:id/info
// 更新客户基本信息（姓名、手机号、兴趣车型等）
// ============================================================
func EditCustomerInfo(c *gin.Context) {
	id := c.Param("id")

	var customer model.Customer
	if err := db.RQ(c).First(&customer, id).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "客户不存在", Data: nil})
		return
	}
	// 四级数据范围门禁：范围外客户视为不存在（不泄露存在性）
	if !customerInDataScope(c, customer.AssignedUserID) {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "客户不存在", Data: nil})
		return
	}

	var req editCustomerInfoRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误: " + err.Error(), Data: nil})
		return
	}

	// 只更新非零值字段
	if req.Name != "" {
		customer.Name = req.Name
	}
	if req.Phone != "" {
		customer.Phone = req.Phone
	}
	if req.Age != 0 {
		customer.Age = req.Age
	}
	if req.Gender != 0 {
		customer.Gender = req.Gender
	}
	if req.Region != "" {
		customer.Region = req.Region
	}
	if req.City != "" {
		customer.City = req.City
	}
	if req.Career != "" {
		customer.Career = req.Career
	}
	if req.InterestModel != "" {
		customer.InterestModel = req.InterestModel
	}
	if req.Budget != 0 {
		customer.Budget = req.Budget
	}
	if req.Remark != "" {
		customer.Remark = req.Remark
	}
	if req.JourneyStage != "" {
		customer.JourneyStage = req.JourneyStage
	}

	// 更新T向量
	tVector := customer.GetTVector()
	customer.SaveTVector(tVector)

	db.RQ(c).Save(&customer)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "客户信息更新成功",
		Data:    customer,
	})
}

// ============================================================
// 顾问修改客户线索状态（已到店/已试驾/已报价）
// PUT /api/v1/advisor/customer/:id/stage
// 修复问题2：顾问端可直接推进线索状态到已到店(含子状态)
// ============================================================
// updateStageRequest 修改线索状态请求
type updateStageRequest struct {
	JourneyStage    string `json:"journey_stage" binding:"required"` // 目标阶段: arrived/ordered/delivered
	JourneySubStage string `json:"journey_sub_stage"`                // 到店子状态: test_driven/quoted（仅arrived时有效）
}

// UpdateCustomerStage PUT /api/v1/advisor/customer/:id/stage 修改到店子状态（已到店/已试驾/已报价）
func UpdateCustomerStage(c *gin.Context) {
	id := c.Param("id")

	var customer model.Customer
	if err := db.RQ(c).First(&customer, id).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "客户不存在", Data: nil})
		return
	}
	// 四级数据范围门禁：范围外客户视为不存在（不泄露存在性）
	if !customerInDataScope(c, customer.AssignedUserID) {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "客户不存在", Data: nil})
		return
	}

	var req updateStageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误: " + err.Error(), Data: nil})
		return
	}

	// 校验合法的阶段值
	validStages := map[string]bool{
		model.JourneyArrived:   true,
		model.JourneyOrdered:   true,
		model.JourneyDelivered: true,
		model.JourneyLost:      true,
	}
	if !validStages[req.JourneyStage] {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "不合法的阶段值，仅支持: arrived/ordered/delivered/lost", Data: nil})
		return
	}

	// 校验子状态（仅到店阶段有效）
	validSubStages := map[string]bool{
		"":                      true,
		model.SubStageTestDrive: true,
		model.SubStageQuoted:    true,
	}
	if req.JourneyStage == model.JourneyArrived && !validSubStages[req.JourneySubStage] {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "不合法的子状态，仅支持: test_driven/quoted", Data: nil})
		return
	}

	updates := map[string]interface{}{
		"journey_stage": req.JourneyStage,
	}

	// 到店阶段才设置子状态，其他阶段清空子状态
	if req.JourneyStage == model.JourneyArrived {
		updates["journey_sub_stage"] = req.JourneySubStage
		updates["store_visited"] = 1 // 标记已到店
	} else {
		updates["journey_sub_stage"] = ""
	}

	if err := db.RQ(c).Model(&customer).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, schema.Response{Code: 500, Message: "更新状态失败", Data: nil})
		return
	}

	// 重新加载返回
	db.RQ(c).First(&customer, id)

	log.Printf("[顾问修改状态] 客户%d 状态更新: journey_stage=%s, sub_stage=%s",
		customer.ID, req.JourneyStage, req.JourneySubStage)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "状态更新成功",
		Data:    customer,
	})
}

// ============================================================
// 设置跟进提醒
// POST /api/v1/advisor/customer/:id/followup
// 创建一条跟进记录，并设置下次跟进时间
// ============================================================
func CreateFollowup(c *gin.Context) {
	id := c.Param("id")
	customerID, _ := strconv.Atoi(id)

	var customer model.Customer
	if err := db.RQ(c).First(&customer, customerID).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "客户不存在", Data: nil})
		return
	}

	var req followupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误: " + err.Error(), Data: nil})
		return
	}

	// 构建跟进记录
	followup := model.FollowUp{
		CustomerID: uint(customerID),
		UserID:     customer.AssignedUserID,
		Type:       req.Type,
		Method:     req.Method,
		Content:    req.Content,
	}

	// 默认值
	if followup.Type == "" {
		followup.Type = "manual"
	}
	if followup.Method == "" {
		followup.Method = "wechat"
	}

	// 解析下次跟进时间
	if req.NextFollowAt != "" {
		if t, err := time.Parse(time.RFC3339, req.NextFollowAt); err == nil {
			followup.NextFollowAt = &t
		}
	}

	if err := db.RQ(c).Create(&followup).Error; err != nil {
		c.JSON(http.StatusInternalServerError, schema.Response{Code: 500, Message: "创建跟进记录失败", Data: nil})
		return
	}

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "跟进提醒已创建",
		Data:    followup,
	})
}

// ============================================================
// 跟进提醒列表
// GET /api/v1/advisor/followups
// 返回待跟进列表，支持按日期筛选
// ============================================================
func GetFollowups(c *gin.Context) {
	userID, _ := strconv.Atoi(c.Query("user_id"))
	dateStr := c.Query("date") // 格式：2006-01-02，默认今天

	// 修复（越权）：非admin角色一律用JWT里解出来的真实身份覆盖，
	// 杜绝伪造user_id查看其他顾问的跟进数据
	_, _, role := middleware.CurrentUser(c)
	if !(role == model.RoleTenantAdmin || role == model.RoleSuperAdmin) {
		jwtUserID, _ := c.Get("user_id")
		if uid, ok := jwtUserID.(uint); ok {
			userID = int(uid)
		}
	}

	if userID == 0 {
		// admin未指定user_id时，返回空列表（不暴露所有顾问的数据）
		c.JSON(http.StatusOK, schema.Response{
			Code:    0,
			Message: "success",
			Data:    []model.FollowUp{},
		})
		return
	}

	query := db.RQ(c).Model(&model.FollowUp{}).
		Where("user_id = ?", userID)

	// 按日期筛选
	if dateStr != "" {
		if t, err := time.Parse("2006-01-02", dateStr); err == nil {
			dayStart := t.Truncate(24 * time.Hour)
			dayEnd := dayStart.Add(24 * time.Hour)
			query = query.Where("next_follow_at >= ? AND next_follow_at < ?", dayStart, dayEnd)
		}
	} else {
		// 默认：今天和未来的待跟进
		todayStart := time.Now().Truncate(24 * time.Hour)
		query = query.Where("next_follow_at >= ?", todayStart)
	}

	var followups []model.FollowUp
	query.Order("next_follow_at ASC").Limit(50).Find(&followups)

	// 附加客户信息
	type followupWithCustomer struct {
		model.FollowUp
		CustomerName  string  `json:"customer_name"`   // 客户姓名
		CustomerPhone string  `json:"customer_phone"`  // 客户手机号
		IntentScore   float64 `json:"customer_intent"` // 客户意向分
	}

	result := make([]followupWithCustomer, 0, len(followups))
	for _, fu := range followups {
		var cust model.Customer
		db.RQ(c).Select("name, phone, intent_score").First(&cust, fu.CustomerID)

		result = append(result, followupWithCustomer{
			FollowUp:      fu,
			CustomerName:  cust.Name,
			CustomerPhone: cust.Phone,
			IntentScore:   cust.IntentScore,
		})
	}

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data:    result,
	})
}

// ============================================================
// 一键接管对话
// POST /api/v1/advisor/chat/takeover
// 将会话从AI模式切换到人工模式
// ============================================================
func AdvisorTakeover(c *gin.Context) {
	var req takeoverRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误", Data: nil})
		return
	}

	var conversation model.Conversation
	if err := db.RQ(c).First(&conversation, req.ConversationID).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "会话不存在", Data: nil})
		return
	}

	// 修复（越权）：非admin顾问只能接管分配给自己的客户的会话
	// 四级语义：admin 家族全通；未分配(0)允许接管；已分配走 canOperateCustomer
	_, _, role := middleware.CurrentUser(c)
	var cust model.Customer
	if err := db.RQ(c).First(&cust, conversation.CustomerID).Error; err != nil {
		c.JSON(http.StatusForbidden, schema.Response{Code: 403, Message: "无权操作此会话", Data: nil})
		return
	}
	if !(role == model.RoleTenantAdmin || role == model.RoleSuperAdmin) &&
		cust.AssignedUserID != 0 && !canOperateCustomer(c, cust.AssignedUserID) {
		c.JSON(http.StatusForbidden, schema.Response{Code: 403, Message: "该客户未分配给您，无法接管", Data: nil})
		return
	}

	// 切换到人工模式并锁定
	conversation.Mode = "human"
	conversation.IsHumanLocked = true
	conversation.PendingHandoff = false
	conversation.HandoffNotifiedAt = nil
	db.RQ(c).Save(&conversation)

	log.Printf("[顾问接管] 会话%d已由AI切换到人工模式", conversation.ID)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "已接管对话",
		Data: gin.H{
			"conversation_id": conversation.ID,
			"mode":            conversation.Mode,
		},
	})
}

// ============================================================
// 顾问发送消息（人工回复）
// POST /api/v1/advisor/chat/send
// ============================================================
func AdvisorSendMessage(c *gin.Context) {
	var req advisorSendMsgRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误", Data: nil})
		return
	}

	var conversation model.Conversation
	if err := db.RQ(c).First(&conversation, req.ConversationID).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "会话不存在", Data: nil})
		return
	}

	// 修复（越权）：非admin顾问只能给自己分配到的客户发消息
	jwtUserID, _, role := middleware.CurrentUser(c)
	if role != "admin" {
		var cust model.Customer
		if err := db.RQ(c).First(&cust, conversation.CustomerID).Error; err != nil {
			c.JSON(http.StatusForbidden, schema.Response{Code: 403, Message: "无权操作此会话", Data: nil})
			return
		}
		if cust.AssignedUserID != jwtUserID {
			// 如果客户尚未分配，允许接管（先分配再发消息）
			if cust.AssignedUserID == 0 {
				db.RQ(c).Model(&cust).Updates(map[string]interface{}{
					"assigned_user_id":  jwtUserID,
					"assignment_reason": "ai_handover",
				})
				log.Printf("[顾问发消息-自动分配] 客户%d 未分配，自动分配给顾问%d", cust.ID, jwtUserID)
			} else {
				c.JSON(http.StatusForbidden, schema.Response{Code: 403, Message: "该客户已分配给其他顾问，无法发送消息", Data: nil})
				return
			}
		}
	}

	// 确保人工模式
	if conversation.Mode != "human" {
		conversation.Mode = "human"
		conversation.IsHumanLocked = true
	}

	now := time.Now()
	conversation.IsAiReplyEnabled = false
	conversation.LastHumanReplyAt = &now
	conversation.LastMessageAt = &now
	db.RQ(c).Save(&conversation)

	// 保存人工消息
	humanMsg := model.Message{
		ConversationID: conversation.ID,
		CustomerID:     conversation.CustomerID,
		SenderType:     "human",
		SenderID:       jwtUserID, // 修复：记录是哪个顾问发的消息
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

// ============================================================
// 手动触发AI回复（顾问点击"AI回复"按钮时调用）
// POST /api/v1/advisor/chat/ai-reply
// 场景：人工接管后AI被暂停，顾问想用AI辅助回复时手动触发
// ============================================================
func AdvisorTriggerAIReply(c *gin.Context) {
	var req struct {
		ConversationID uint   `json:"conversation_id" binding:"required"`
		Content        string `json:"content"` // 可选：指定AI根据什么内容回复，为空则取最近客户消息
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误: " + err.Error()})
		return
	}

	var conversation model.Conversation
	if err := db.RQ(c).First(&conversation, req.ConversationID).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "会话不存在"})
		return
	}

	var customer model.Customer
	if err := db.RQ(c).First(&customer, conversation.CustomerID).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "客户不存在"})
		return
	}

	if !canOperateCustomer(c, customer.AssignedUserID) {
		c.JSON(http.StatusForbidden, schema.Response{Code: 403, Message: "无权操作该客户"})
		return
	}

	// 优先用请求中指定的内容，否则取最近一条客户消息
	userInput := req.Content
	if userInput == "" {
		var lastCustomerMsg model.Message
		db.RQ(c).Where("conversation_id = ? AND sender_type = ?", conversation.ID, "customer").
			Order("created_at DESC").Limit(1).Find(&lastCustomerMsg)
		if lastCustomerMsg.ID == 0 {
			c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "没有找到客户消息，无法生成AI回复"})
			return
		}
		userInput = lastCustomerMsg.Content
	}

	// 构建策略输入
	tVector := customer.BuildBaseTVector()
	state := conversation.GetState()
	customerTags := customer.GetTags()

	strategyInput := strategy.StrategyInput{
		TVector:        tVector,
		State:          state,
		CustomerInput:  userInput,
		CustomerTags:   customerTags,
		CustomerID:     customer.ID,
		ConversationID: conversation.ID,
		CanPromote:     customer.CanPromote(),
		JourneyStage:   customer.JourneyStage,
	}
	strategyOutput := strategy.DefaultEngine.Infer(strategyInput)

	// 生成AI回复
	aiReply := flow.DefaultEngine.OrchestrateReply(&customer, conversation.ID, userInput, &strategyOutput)

	// 保存AI回复消息
	now := time.Now()
	aiMsg := model.Message{
		ConversationID: conversation.ID,
		CustomerID:     customer.ID,
		SenderType:     "ai",
		Content:        aiReply,
		MessageType:    "text",
		AnchorType:     strategyOutput.FinalAnchor,
		TemplateID:     strategyOutput.TemplateID,
		RouteResult:    "ai_triggered_by_advisor",
		CreatedAt:      now,
	}
	db.RQ(c).Create(&aiMsg)

	// 更新会话
	conversation.LastMessageAt = &now
	conversation.LastTid = strategyOutput.TemplateID
	conversation.LastAnchorType = strategyOutput.FinalAnchor
	db.RQ(c).Save(&conversation)

	log.Printf("[顾问触发AI回复] 会话%d 客户%d AI回复已生成并保存", conversation.ID, customer.ID)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "AI回复已生成",
		Data:    aiMsg,
	})
}

// ============================================================
// 策略话术推荐
// GET /api/v1/advisor/strategy/recommend?customer_id=X&conversation_id=X
// 根据客户画像和会话状态，推荐当前最合适的话术
// ============================================================
func GetStrategyRecommend(c *gin.Context) {
	customerID, _ := strconv.Atoi(c.Query("customer_id"))
	conversationID, _ := strconv.Atoi(c.Query("conversation_id"))

	if customerID == 0 {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "customer_id必填", Data: nil})
		return
	}

	// 获取客户
	var customer model.Customer
	if err := db.RQ(c).First(&customer, customerID).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "客户不存在", Data: nil})
		return
	}

	// 获取会话状态
	tVector := customer.GetTVector()
	state := model.SessionState{
		Attempts:     1,
		HookCount:    0,
		HookRate:     0,
		Emotion:      "neutral",
		CurrentStage: 0,
	}

	// 如果有会话ID，用真实状态
	if conversationID > 0 {
		var conv model.Conversation
		if err := db.RQ(c).First(&conv, conversationID).Error; err == nil {
			state = conv.GetState()
		}
	}

	// 调用策略引擎推理
	customerTags := customer.GetTags()
	input := strategy.StrategyInput{
		TVector:        tVector,
		State:          state,
		CustomerInput:  "", // 推荐场景没有客户输入，用空字符串
		CustomerTags:   customerTags,
		CustomerID:     customer.ID,
		ConversationID: uint(conversationID),
	}

	output := strategy.DefaultEngine.Infer(input)

	// 获取推荐话术模板
	type recommendItem struct {
		AnchorType     int    `json:"anchor_type"`     // 锚类型
		AnchorName     string `json:"anchor_name"`     // 锚类型名
		TemplateID     string `json:"template_id"`     // 话术模板ID
		TemplateName   string `json:"template_name"`   // 话术模板名
		PromptTemplate string `json:"prompt_template"` // 推荐话术内容
		HookTemplate   string `json:"hook_template"`   // 钩话术
		Priority       int    `json:"priority"`        // 优先级
	}

	var recommends []recommendItem

	// 获取策略引擎推荐的话术模板
	if output.TemplateID != "" {
		var tpl model.Template
		if err := db.RQ(c).First(&tpl, "id = ?", output.TemplateID).Error; err == nil {
			recommends = append(recommends, recommendItem{
				AnchorType:     output.FinalAnchor,
				AnchorName:     strategy.GetAnchorName(output.FinalAnchor),
				TemplateID:     tpl.ID,
				TemplateName:   tpl.Name,
				PromptTemplate: tpl.PromptTemplate,
				HookTemplate:   tpl.HookTemplate,
				Priority:       10,
			})
		}
	}

	// 同时提供1-2个备选话术（同类型或下一级）
	var altTemplates []model.Template
	db.RQ(c).Where("anchor_type = ? AND status = 1", output.FinalAnchor).
		Order("priority DESC").
		Limit(2).
		Find(&altTemplates)

	for _, tpl := range altTemplates {
		// 跳过已推荐的主话术
		if len(recommends) > 0 && tpl.ID == recommends[0].TemplateID {
			continue
		}
		recommends = append(recommends, recommendItem{
			AnchorType:     tpl.AnchorType,
			AnchorName:     strategy.GetAnchorName(tpl.AnchorType),
			TemplateID:     tpl.ID,
			TemplateName:   tpl.Name,
			PromptTemplate: tpl.PromptTemplate,
			HookTemplate:   tpl.HookTemplate,
			Priority:       tpl.Priority,
		})
	}

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data: gin.H{
			"customer_id":   customer.ID,
			"intent_score":  customer.IntentScore,
			"urgency_level": output.UrgencyLevel,
			"route_result":  output.RouteResult,
			"recommends":    recommends,
		},
	})
}

// ============================================================
// 聊天历史查询（客户端和销售端共用）
// GET /api/v1/chat/history?customer_id=X&conversation_id=X&limit=50
// ============================================================
func GetChatHistory(c *gin.Context) {
	customerID, _ := strconv.Atoi(c.Query("customer_id"))
	conversationID, _ := strconv.Atoi(c.Query("conversation_id"))
	limit, _ := strconv.Atoi(c.Query("limit"))

	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	query := db.RQ(c).Model(&model.Message{})

	// 优先按会话ID查询
	if conversationID > 0 {
		query = query.Where("conversation_id = ?", conversationID)
	} else if customerID > 0 {
		// 按客户ID查询：找最近的活跃会话
		var conv model.Conversation
		db.RQ(c).Where("customer_id = ? AND status = ?", customerID, "active").
			Order("updated_at DESC").Limit(1).Find(&conv)
		if conv.ID == 0 {
			// 没有活跃会话，返回空
			c.JSON(http.StatusOK, schema.Response{
				Code:    0,
				Message: "success",
				Data:    []model.Message{},
			})
			return
		}
		query = query.Where("conversation_id = ?", conv.ID)
	} else {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "需要传customer_id或conversation_id", Data: nil})
		return
	}

	var messages []model.Message
	query.Order("created_at ASC").Limit(limit).Find(&messages)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data:    messages,
	})
}

// ============================================================
// 顾问列表（供顾问端切换身份）
// GET /api/v1/advisor/list
// 返回所有role=sales的用户列表(id, real_name, username)
// ============================================================
func GetAdvisorList(c *gin.Context) {
	var users []model.User
	// 修复Bug1（2026-08-22）：角色改用 model.RoleSales 常量。
	// 根因：组织迁移 sales→user 后硬编码"sales"查空，顾问端列表一直为空
	db.RQ(c).Where("role = ? AND status = 1", model.RoleSales).
		Select("id, real_name, username").
		Order("id ASC").
		Find(&users)

	type advisorItem struct {
		ID       uint   `json:"id"`
		RealName string `json:"real_name"`
		Username string `json:"username"`
	}

	result := make([]advisorItem, 0, len(users))
	for _, u := range users {
		name := u.RealName
		if name == "" {
			name = u.Username
		}
		result = append(result, advisorItem{
			ID:       u.ID,
			RealName: name,
			Username: u.Username,
		})
	}

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data:    result,
	})
}
