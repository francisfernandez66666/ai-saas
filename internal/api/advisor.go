// 顾问工作台API：客户管理、数据统计、试驾单、对话接管与策略话术推荐。
package api

import "ai-scrm/internal/pii"

// 顾问工作台API（销售端）：客户管理、工作台数据统计、跟进提醒、试驾单、对话接管、AI回复触发与策略话术推荐。
// 写操作均经四级组织数据范围门禁(canOperateCustomer/customerInDataScope)防越权。

import (
	"ai-scrm/internal/chatflow"
	"ai-scrm/internal/db"
	"ai-scrm/internal/engine/flow"
	"ai-scrm/internal/engine/strategy"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
	"ai-scrm/internal/mq"
	"ai-scrm/internal/schema"
	"ai-scrm/internal/service"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ============================================================
// 顾问工作台API（销售端专用）
// 路由前缀：/api/v1/advisor/
// 功能：客户管理、工作台数据、跟进提醒、对话接管、策略话术推荐
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
	Name            string  `json:"name"`           // 客户姓名
	Phone           string  `json:"phone"`          // 手机号
	Age             int     `json:"age"`            // 年龄
	Gender          int     `json:"gender"`         // 性别: 0-未知 1-男 2-女
	Region          string  `json:"region"`         // 地域
	City            string  `json:"city"`           // 城市
	Career          string  `json:"career"`         // 职业
	InterestProduct string  `json:"interest_model"` // 兴趣产品
	Budget          float64 `json:"budget"`         // 预算（万元）
	Remark          string  `json:"remark"`         // 备注
	JourneyStage    string  `json:"journey_stage"`  // 客户旅程阶段
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
// P1-11 修复(2026-09-15)：conversation_id 从 required 改为可选并新增 customer_id——
// F10 手工建的客户/新客没有会话，前端 convId 恒 null → 固定 400，"顾问主动触达
// 新客户"整条链路断。缺会话时后端按 customer_id 找/建活跃会话（人工模式）。
type advisorSendMsgRequest struct {
	ConversationID uint   `json:"conversation_id"`                     // 会话ID（新客可为0，回落 customer_id）
	CustomerID     uint   `json:"customer_id"`                         // 客户ID（conversation_id 为 0 时必填）
	Content        string `json:"content" binding:"required,max=4000"` // 消息内容（P2：长度上限同 ChatRequest）
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
// GetAdvisorStats 返回顾问工作台核心统计。
// apidump:ts AdvisorStatItem[]
// 顾问工作台指标卡（label/value/color）。
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
	persistBypass("advisor_stats_read", 0, 0, db.RQ(c).Model(&model.Customer{}).
		Where("status = 1 AND "+assignedFilter).
		Count(&followingCount))
	stats = append(stats, statItem{Label: "跟进中", Value: followingCount, Color: "blue"})

	// 已到店客户数
	var arrivedCount int64
	persistBypass("advisor_stats_read", 0, 0, db.RQ(c).Model(&model.Customer{}).
		Where("status = 1 AND journey_stage = ? AND "+assignedFilter, "arrived").
		Count(&arrivedCount))
	stats = append(stats, statItem{Label: "已到店", Value: arrivedCount, Color: "green"})

	// 已试驾客户数（到店+已试驾子状态）
	var testDriveCount int64
	persistBypass("advisor_stats_read", 0, 0, db.RQ(c).Model(&model.Customer{}).
		Where("status = 1 AND journey_stage = ? AND journey_sub_stage IN ? AND "+assignedFilter, "arrived", []string{"test_driven", "quoted"}).
		Count(&testDriveCount))
	stats = append(stats, statItem{Label: "已试驾", Value: testDriveCount, Color: "purple"})

	// 已下单客户数
	var orderedCount int64
	persistBypass("advisor_stats_read", 0, 0, db.RQ(c).Model(&model.Customer{}).
		Where("status = 1 AND journey_stage IN ? AND "+assignedFilter, []string{model.JourneyOrdered, model.JourneyDelivered}).
		Count(&orderedCount))
	stats = append(stats, statItem{Label: "已下单", Value: orderedCount, Color: "orange"})

	// 今日待跟进
	var todayFollowupCount int64
	todayStart := time.Now().Truncate(24 * time.Hour)
	todayEnd := todayStart.Add(24 * time.Hour)
	persistBypass("advisor_stats_read", 0, 0, db.RQ(c).Model(&model.FollowUp{}).
		Where("next_follow_at >= ? AND next_follow_at < ?", todayStart, todayEnd).
		Count(&todayFollowupCount))
	stats = append(stats, statItem{Label: "今日待跟进", Value: todayFollowupCount, Color: "red"})

	// 逾期未跟进
	var overdueCount int64
	persistBypass("advisor_stats_read", 0, 0, db.RQ(c).Model(&model.FollowUp{}).
		Where("next_follow_at < ? AND next_follow_at IS NOT NULL", time.Now()).
		Count(&overdueCount))
	stats = append(stats, statItem{Label: "逾期未跟进", Value: overdueCount, Color: "red"})

	RespOK(c, "success", stats)
}

// ============================================================
// 切换AI回复开关（顾问手动控制AI是否自动回复）
// POST /api/v1/advisor/chat/toggle-ai-reply
// 场景：顾问忙碌时关闭AI回复，空闲或下班时打开AI接住客户
// ============================================================
// ToggleAiReply 切换顾问会话的 AI 自动回复开关。
// apidump:ts AiReplyToggle
// 切换结果三字段（is_ai_reply_enabled/mode/is_human_locked），非整行会话。
func ToggleAiReply(c *gin.Context) {
	var req struct {
		ConversationID uint  `json:"conversation_id" binding:"required"`
		Enabled        *bool `json:"enabled"` // nil=切换, true=开, false=关
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErrBind(c, err)
		return
	}

	var conversation model.Conversation
	if err := db.RQ(c).First(&conversation, req.ConversationID).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "会话不存在")
		return
	}

	var customer model.Customer
	if err := db.RQ(c).First(&customer, conversation.CustomerID).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "客户不存在")
		return
	}

	if !canOperateCustomer(c, customer.AssignedUserID) {
		RespErr(c, http.StatusForbidden, 403, "无权操作该客户")
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

	// 必查（FIX-5，2026-09-27，ai_reply_toggle）：本接口把这三个状态列原样回给前端渲染开关，
	// 写失败仍回 200 = 顾问以为已经把 AI 关了（或开了），库里没变，客户那边的行为与界面不符。
	// 开关是幂等动作，报错让重点一次不会有副作用；静默成功才是问题。
	if persistRequiredMsg(c, "ai_reply_toggle", conversation.CustomerID, conversation.ID,
		db.RQ(c).Model(&conversation).Updates(map[string]interface{}{
			"is_ai_reply_enabled": conversation.IsAiReplyEnabled,
			"mode":                conversation.Mode,
			"is_human_locked":     conversation.IsHumanLocked,
		}), "AI回复开关切换失败，请重试") {
		return
	}

	status := "开启"
	if !conversation.IsAiReplyEnabled {
		status = "关闭"
	}
	log.Printf("[顾问切换AI回复] 会话%d %sAI回复, mode=%s, locked=%v",
		conversation.ID, status, conversation.Mode, conversation.IsHumanLocked)

	RespOK(c, "已"+status+"AI回复", gin.H{
		"is_ai_reply_enabled": conversation.IsAiReplyEnabled,
		"mode":                conversation.Mode,
		"is_human_locked":     conversation.IsHumanLocked,
	})
}

// ============================================================
// 试驾单相关 API
// ============================================================

// createTestDriveRequest 创建试驾单请求体：必选客户与预约时间，其余字段可选
type createTestDriveRequest struct {
	CustomerID   uint   `json:"customer_id" binding:"required"`
	ScheduledAt  string `json:"scheduled_at" binding:"required"` // RFC3339
	ModelName    string `json:"model_name"`
	ContactName  string `json:"contact_name"`
	ContactPhone string `json:"contact_phone"`
	Location     string `json:"location"`
	Note         string `json:"note"`
}

// publishCustomerBehavior 把一条客户行为事实经 user_event 发进 CDP（标签由摄入端算）。
// G-19(2026-09-24)：试驾/预约这类人工侧动作此前只在"顾问推进阶段"埋点，
// 试驾单本身的状态流转没人发布，beh_testdrive/beh_booked 标签在真实开单链上空转。
// 发布失败只记日志、不改写业务响应——业务行已落库，因旁路失败回滚用户操作反而更糟
// （与 UpdateCustomerStage 到店分支同口径）。oneID 约定 "c:{customerID}"。
func publishCustomerBehavior(c *gin.Context, customerID uint, eventName string, attrs map[string]any) {
	if customerID == 0 {
		return
	}
	if attrs == nil {
		attrs = map[string]any{}
	}
	attrs["customer_id"] = customerID
	if err := mq.Publish(middleware.CtxWithTrace(c), mq.TopicUserEvent, middleware.EffectiveTenantID(c),
		fmt.Sprintf("c:%d", customerID), eventName, mq.UserEvent{
			EventType:  "behavior",
			EventName:  eventName,
			Attributes: attrs,
			OccurredAt: time.Now(),
		}); err != nil {
		log.Printf("[MQ] %s(顾问台) 发布失败: %v", eventName, err)
	}
}

// CreateTestDrive POST /api/v1/advisor/test-drive 创建试驾单
// apidump:ts TestDriveRow
// 创建成功回整行试驾单。
func CreateTestDrive(c *gin.Context) {
	var req createTestDriveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErrBind(c, err)
		return
	}

	var customer model.Customer
	if err := db.RQ(c).First(&customer, req.CustomerID).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "客户不存在")
		return
	}

	if !canOperateCustomer(c, customer.AssignedUserID) {
		RespErr(c, http.StatusForbidden, 403, "无权操作该客户")
		return
	}

	scheduledAt, err := time.Parse(time.RFC3339, req.ScheduledAt)
	if err != nil {
		RespErr(c, http.StatusBadRequest, 400, "预约时间格式错误，请使用RFC3339格式")
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

	// 必查（FIX-5，2026-09-27，test_drive_create）：试驾单是对外承诺的凭据行，响应体把整行
	// （含 ID）回给前端。旧写法丢掉 error 后还要靠 `if td.ID > 0` 兜下面两个分支——
	// 等于"创建失败也回一句试驾单创建成功"，顾问记进日历、客户那边查无此单。
	if persistRequiredMsg(c, "test_drive_create", td.CustomerID, 0,
		db.RQ(c).Create(&td), "试驾单创建失败，请重试") {
		return
	}
	log.Printf("[试驾单] 创建试驾单 #%d 客户%d 顾问%d 时间%s", td.ID, td.CustomerID, td.AdvisorID, td.ScheduledAt.Format("2006-01-02 15:04"))
	// G-19：开单即"已预约"事实——CDP 的 beh_booked 标签此前无任何生产者，
	// 消费端分支（ingest_consumer.go 的 case "booking"）自建立起空转。
	if td.ID > 0 {
		publishCustomerBehavior(c, td.CustomerID, "booking", map[string]any{
			"test_drive_id": td.ID,
			"model_name":    td.ModelName,
			"path":          "advisor_test_drive_create",
		})
	}

	RespOK(c, "试驾单创建成功", td)
}

// GetTestDrives GET /api/v1/advisor/test-drives 试驾单列表（按客户筛选）
// apidump:ts TestDriveRow[]
// 试驾单列表（按预约时间倒序）。
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

	RespOK(c, "success", list)
}

// GetTestDrive GET /api/v1/advisor/test-drive/:id 试驾单详情
// apidump:ts TestDriveRow
// 单条试驾单。
func GetTestDrive(c *gin.Context) {
	id := c.Param("id")
	var td model.TestDrive
	if err := db.RQ(c).First(&td, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "试驾单不存在")
		return
	}

	if !customerInDataScope(c, td.AdvisorID) {
		RespErr(c, http.StatusForbidden, 403, "无权查看")
		return
	}

	RespOK(c, "success", td)
}

// validTestDriveStatus 试驾单合法状态枚举（PLAN_FIX_2026-09-21 B3）
// 取值与 model.TestDrive.Status 注释及前端三态展示对齐（待试驾/已完成/已取消）。
var validTestDriveStatus = map[string]bool{
	"pending":   true,
	"completed": true,
	"cancelled": true,
}

// updateTestDriveRequest 更新试驾单请求体：全部字段指针化，nil 表示不修改
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
// apidump:ts TestDriveRow
// 更新成功回整行试驾单。
func UpdateTestDrive(c *gin.Context) {
	id := c.Param("id")
	var td model.TestDrive
	if err := db.RQ(c).First(&td, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "试驾单不存在")
		return
	}

	if !canOperateCustomer(c, td.AdvisorID) {
		RespErr(c, http.StatusForbidden, 403, "无权修改")
		return
	}

	var req updateTestDriveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErrBind(c, err)
		return
	}

	// G-19：状态流转前留一份旧状态——事件只在"真的翻到已完成"时发一次，
	// 否则每次 PATCH 别字段都会重发 test_drive，标签虽幂等但事件流会被灌水。
	prevStatus := td.Status

	// PLAN_FIX_2026-09-21 B3：补 status 枚举校验——此前 `*req.Status` 直赋，任意字符串
	// 都能落库（同模块 UpdateCustomerStage 有 validStages 校验并返 400，标准不一致）。
	// 脏状态会让列表筛选（following/pending/…）与统计口径失配，且无法在写入侧拦截。
	if req.Status != nil {
		if !validTestDriveStatus[*req.Status] {
			RespErr(c, http.StatusBadRequest, 400, "不合法的试驾单状态，仅支持: pending/completed/cancelled")
			return
		}
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

	if saveErr := db.RQ(c).Save(&td).Error; saveErr != nil {
		// 落库失败时绝不发事件：CDP 只记"确实发生过的动作"，否则标签会领先于事实
		log.Printf("[试驾单] 更新失败 #%d: %v", td.ID, saveErr)
	} else if td.Status == "completed" && prevStatus != "completed" {
		// G-19(2026-09-24)：试驾单状态流转到"已完成"→ 发 test_drive。
		// 此前该事件只有"顾问把阶段推到到店+子状态=已试驾"一条埋点路径，
		// 真正的试驾单闭环（开单→完成）没人发布，beh_testdrive 标签与
		// 「AI 销售」的旅程阶段回填都拿不到这条事实。
		publishCustomerBehavior(c, td.CustomerID, "test_drive", map[string]any{
			"test_drive_id": td.ID,
			"model_name":    td.ModelName,
			"from_status":   prevStatus,
			"path":          "advisor_test_drive_update",
		})
	}
	log.Printf("[试驾单] 更新试驾单 #%d 状态=%s", td.ID, td.Status)

	RespOK(c, "更新成功", td)
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
		// 旁路留痕（FIX-5，2026-09-27，org_scope_check_read）：判定读失败时 cnt 保持 0 → **拒绝**，
		// 方向是 fail-closed 的（安全侧），所以不能报错打断；但"本来有权限却被拒"这件事必须可数，
		// 否则顾问只会看到一句"无权操作"，谁都不知道是库抖了。
		persistBypass("org_scope_check_read", 0, 0, db.DB.Table("tenant_users u").
			Joins("LEFT JOIN departments d ON u.department_id = d.id").
			Where("u.tenant_id = ? AND u.id = ? AND d.path LIKE ?",
				db.EffectiveTenantIDFromGin(c), assignedUserID, myPath+"%").
			Count(&cnt))
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
		// 旁路留痕（FIX-5，2026-09-27，org_scope_check_read）：判定读失败时 cnt 保持 0 → **拒绝**，
		// 方向是 fail-closed 的（安全侧），所以不能报错打断；但"本来有权限却被拒"这件事必须可数，
		// 否则顾问只会看到一句"无权操作"，谁都不知道是库抖了。
		persistBypass("org_scope_check_read", 0, 0, db.DB.Table("tenant_users u").
			Joins("LEFT JOIN departments d ON u.department_id = d.id").
			Where("u.tenant_id = ? AND u.id = ? AND d.path LIKE ?",
				db.EffectiveTenantIDFromGin(c), assignedUserID, myPath+"%").
			Count(&cnt))
		return cnt > 0
	}
	return false
}

// GetAdvisorCustomers GET /api/v1/advisor/customers 工作台客户列表（按角色数据范围裁剪）
// apidump:ts Paginated<AdvisorCustomerRow>
// 顾问客户列表：model.Customer 展开 + last_message/conv_mode/lead_status 等附加列。
//
// 行为零变化说明（2026-09-28 结构拆分）：原 164 行函数体拆为
// buildAdvisorCustomerQuery（筛选判据装配）+ enrichAdvisorCustomerRow（逐行附加列回查）两个阶段，
// 条件顺序、SQL、旁路留痕键与响应体键序逐字不变。
func GetAdvisorCustomers(c *gin.Context) {
	var req advisorCustomerListRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误")
		return
	}

	if req.Page <= 0 {
		req.Page = 1
	}
	if req.PageSize <= 0 {
		req.PageSize = 20
	}
	// FIX-4(2026-09-27)：页长口径并入全仓单点 schema.NormalizePageSize（上限 100）。
	// 旧写法只兜 <=0，**没有上限**——`/advisor/customers?page_size=50000` 实测原样回显 50000，
	// 等于把 P2-9 的防拖库红线在这一个端点上单独绕过了：一个登录用户即可让服务端单次请求
	// 拉出全量客户行（含手机号），响应体和 DB 压力都不设防。
	// 钳制后的**生效值**随 PageResponse.page_size 回显，前端按回显值决定是否续拉下一页，
	// 不再各自硬写 200/500（那样只会静默截断）。
	req.PageSize = schema.NormalizePageSize(req.PageSize)

	query := buildAdvisorCustomerQuery(c, &req)

	var total int64
	query.Count(&total)

	var customers []model.Customer
	query.Order("updated_at DESC").
		Offset((req.Page - 1) * req.PageSize).
		Limit(req.PageSize).
		Find(&customers)

	result := make([]customerWithExtra, 0, len(customers))
	for _, cust := range customers {
		result = append(result, enrichAdvisorCustomerRow(c, cust))
	}

	RespOK(c, "success", schema.PageResponse{
		Total:    total,
		Page:     req.Page,
		PageSize: req.PageSize,
		List:     result,
	})
}

// buildAdvisorCustomerQuery 装配客户列表的筛选查询（租户隔离 + 四级数据范围 + 状态/关键词条件）。
// 单独成段是因为这条链是"同一个 clone=0 句柄上的条件累加"——Count 与 Find 必须共用
// 装配完的这一棵 Where 树才能保证"名单条数==total"；把装配挪回主函数只会让分页两步
// 各自重搭条件、漂移出不一致。req 用指针传人是刻意的：RoleUser 视角强制改写
// req.UserID 必须发生在 assigned 过滤之前，顺序即语义。
func buildAdvisorCustomerQuery(c *gin.Context, req *advisorCustomerListRequest) *gorm.DB {
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
	return query
}

// customerWithExtra 顾问列表行：model.Customer 展开 + 附加列（最后消息/会话模式/线索状态/顾问名）。
type customerWithExtra struct {
	model.Customer
	LastMessage      string `json:"last_message"`       // 最近一条消息
	ConvMode         string `json:"conv_mode"`          // 当前会话模式 ai/human
	LeadStatus       string `json:"lead_status"`        // 线索状态
	LeadSubStatus    string `json:"lead_sub_status"`    // 到店子状态描述
	AssignedUserName string `json:"assigned_user_name"` // 分配顾问姓名（修复问题3：销售端显示分配给哪个顾问）
	LastMessageAt    string `json:"last_message_at"`    // 最近消息时间，前端用来判断是否有新消息
}

// enrichAdvisorCustomerRow 为单个客户回查附加列（顾问名/最近消息/活跃会话/子状态文案）。
// 单独成段是因为这段是"每行最多三次旁路读"：任何一次失败只让那一格显示为空
// （persistBypass advisor_list_enrich 留痕），绝不打断整张列表——与主查询"失败即整页无数据"
// 的必查语义是两类，混在主函数里容易让人误给旁路读加上错误中断。
// ⚠ 句柄纪律：三次读各自独立取 `db.RQ(c)`（clone=0 句柄不得跨查询复用，见 AGENTS.md 红线）。
func enrichAdvisorCustomerRow(c *gin.Context, cust model.Customer) customerWithExtra {
	// 为每个客户附加最后一条消息、会话模式、线索状态和分配顾问姓名
	extra := customerWithExtra{Customer: cust}

	// 修复问题3：查分配顾问的姓名
	if cust.AssignedUserID > 0 {
		var assignedUser model.User
		// 旁路（advisor_list_enrich）：列表的附加列读失败只让这一格显示为空，
		// 不打断整张列表——客户名单本身已经读出来了。
		persistBypass("advisor_list_enrich", cust.ID, 0, db.RQ(c).Select("id, real_name, username").Limit(1).Find(&assignedUser, cust.AssignedUserID))
		if assignedUser.ID > 0 {
			extra.AssignedUserName = assignedUser.RealName
			if extra.AssignedUserName == "" {
				extra.AssignedUserName = assignedUser.Username
			}
		}
	}

	// 查最近一条消息
	// 旁路（advisor_list_enrich）+ 按**字符**截断（FIX-5(2026-09-27) 顺带真修）：
	// 旧写法 `content[:50]` 是**字节**切片，客户发中文时 50 字节正好落在多字节字符中间，
	// 列表预览尾部稳定出现一个乱码方块（U+FFFD）——冒烟抓不到（它断的是接口不是渲染），
	// 但每一家中文租户的顾问列表天天看得见。改用与 E8 存档摘要同一口径的 truncateRunes。
	var lastMsg model.Message
	lastMsgRes := db.RQ(c).Where("customer_id = ?", cust.ID).
		Order("created_at DESC").Limit(1).Find(&lastMsg)
	persistBypass("advisor_list_enrich", cust.ID, 0, lastMsgRes)
	if lastMsgRes.Error == nil && lastMsg.ID > 0 {
		extra.LastMessage = truncateRunes(lastMsg.Content, 50)
	}

	// 查当前会话模式 + 最后消息时间
	var conv model.Conversation
	persistBypass("advisor_list_enrich", cust.ID, 0, db.RQ(c).Where("customer_id = ? AND status = ?", cust.ID, "active").
		Order("updated_at DESC").Limit(1).Find(&conv))
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
	return extra
}

// ============================================================
// 客户详情（顾问端）
// GET /api/v1/advisor/customer/:id
// 包含客户基本信息、标签列表、画像数据、最近会话
// ============================================================
// GetAdvisorCustomerDetail 返回顾问视角的客户详情。
func GetAdvisorCustomerDetail(c *gin.Context) {
	id := c.Param("id")

	var customer model.Customer
	if err := db.RQ(c).First(&customer, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "客户不存在")
		return
	}
	// 四级数据范围门禁：范围外客户视为不存在（不泄露存在性）。
	// 批四 P3（DEFECT_VERIFY_2026-09-20）：此处原有第二个 customerInDataScope+403 块为
	// 双重门禁死分支——两次调用入参完全相同，第一关 404 未拦下则第二关必通过，403 恒不可达。
	// 越权语义（非admin须为指派顾问/上级，防传别人 id 看完整详情+聊天记录）已由本门禁完整承载，
	// 且"范围外=不存在"不泄露存在性，严于原 403，故删除冗余块、行为零变化。
	if !customerInDataScope(c, customer.AssignedUserID) {
		RespErr(c, http.StatusNotFound, 404, "客户不存在")
		return
	}

	// 获取客户标签
	// 旁路（advisor_detail_read）：详情页四个区块各自读，某一块失败只让那一块空，
	// 不该把整页打成 500（客户主信息此刻已经读到了）——但必须留痕可数。
	var customerTags []model.CustomerTag
	persistBypass("advisor_detail_read", customer.ID, 0, db.RQ(c).Where("customer_id = ?", customer.ID).Find(&customerTags))

	// 获取跟进记录
	var followups []model.FollowUp
	persistBypass("advisor_detail_read", customer.ID, 0, db.RQ(c).Where("customer_id = ?", customer.ID).
		Order("created_at DESC").Limit(20).Find(&followups))

	// 获取活跃会话
	var conversations []model.Conversation
	persistBypass("advisor_detail_read", customer.ID, 0, db.RQ(c).Where("customer_id = ?", customer.ID).
		Order("updated_at DESC").Limit(5).Find(&conversations))

	// 计算各类型跟进次数统计
	type followupStat struct {
		Type  string `json:"type"`
		Count int64  `json:"count"`
	}
	var followupStats []followupStat
	persistBypass("advisor_detail_read", customer.ID, 0, db.RQ(c).Model(&model.FollowUp{}).
		Select("method, count(*) as count").
		Where("customer_id = ?", customer.ID).
		Group("method").
		Scan(&followupStats))

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

	RespOK(c, "success", gin.H{
		"customer":           customer,
		"assigned_user_name": assignedUserName, // 修复问题3：分配顾问姓名
		"tags":               customerTags,
		"followups":          followups,
		"conversations":      conversations,
		"followup_stats":     followupStats,
	})
}

// ============================================================
// 编辑客户标签
// PUT /api/v1/advisor/customer/:id/tags
// 覆盖更新客户标签（传入完整标签列表）
// ============================================================
// EditCustomerTags 编辑客户标签。
// apidump:ts CustomerTagRow[]
// 覆盖式打标后回传的最终标签关联。
func EditCustomerTags(c *gin.Context) {
	id := c.Param("id")

	var customer model.Customer
	if err := db.RQ(c).First(&customer, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "客户不存在")
		return
	}
	// 四级数据范围门禁：范围外客户视为不存在（不泄露存在性）
	if !customerInDataScope(c, customer.AssignedUserID) {
		RespErr(c, http.StatusNotFound, 404, "客户不存在")
		return
	}

	var req editTagsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErrBind(c, err)
		return
	}

	// 覆盖语义更新（批四 P1）：提交列表=最终态——列表外旧标同步清除。
	// 旧实现增量 upsert 不删除，弹窗取消勾选后旧标滞留库内（uat_advisor 字节级实证）。
	if err := service.DefaultTagService.ReplaceTagsForCustomer(customer.TenantID, customer.ID, req.Tags, "manual"); err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "更新标签失败")
		return
	}

	// 返回更新后的标签列表
	// 旁路（tag_readback）：标签**写入**已在上面判错上抛（批四 P0 真修），
	// 这里只是回读最终态给前端；读失败时前端保留自己刚提交的那份列表，不该报"更新失败"。
	var updatedTags []model.CustomerTag
	persistBypass("tag_readback", customer.ID, 0, db.RQ(c).Where("customer_id = ?", customer.ID).Find(&updatedTags))

	RespOK(c, "标签更新成功", updatedTags)
}

// ============================================================
// 编辑客户信息
// PUT /api/v1/advisor/customer/:id/info
// 更新客户基本信息（姓名、手机号、兴趣车型等）
// ============================================================
// EditCustomerInfo 编辑客户资料。
// apidump:ts Customer
// 资料更新后回整行客户。
func EditCustomerInfo(c *gin.Context) {
	id := c.Param("id")

	var customer model.Customer
	if err := db.RQ(c).First(&customer, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "客户不存在")
		return
	}
	// 四级数据范围门禁：范围外客户视为不存在（不泄露存在性）
	if !customerInDataScope(c, customer.AssignedUserID) {
		RespErr(c, http.StatusNotFound, 404, "客户不存在")
		return
	}

	var req editCustomerInfoRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErrBind(c, err)
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
	if req.InterestProduct != "" {
		customer.InterestProduct = req.InterestProduct
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

	// 字段级更新，不用整行 Save（2026-09-23 批六收口，与 admin 侧 EditCustomerInfo 同修法）：
	// 顾问在工作台打开资料面板到提交的这段窗口里，聊天链路正在实时写 intent_score /
	// trust_level / assignment_reason / journey_sub_stage，整行 Save 会用打开那一刻的
	// 快照把这些列覆回去（AI 刚判出的意向变化静默消失，且没有任何日志）。
	// 这里只写资料表单真正拥有的列；journey_stage 例外——它是本表单的可选字段（顾问手改阶段）。
	if err := db.RQ(c).Model(&model.Customer{}).Where("id = ?", id).Updates(map[string]any{
		"name":           customer.Name,
		"phone":          customer.Phone,
		"gender":         customer.Gender,
		"age":            customer.Age,
		"region":         customer.Region,
		"city":           customer.City,
		"career":         customer.Career,
		"interest_model": customer.InterestProduct, // 泛行业化后 Go 字段改名，列名沿用旧名
		"budget":         customer.Budget,
		"remark":         customer.Remark,
		"journey_stage":  customer.JourneyStage,
		"t_vector":       customer.TVectorJSON,
	}).Error; err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "客户信息更新失败")
		return
	}

	RespOK(c, "客户信息更新成功", customer)
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
// apidump:ts Customer
// 阶段推进后回整行客户。
func UpdateCustomerStage(c *gin.Context) {
	id := c.Param("id")

	var customer model.Customer
	if err := db.RQ(c).First(&customer, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "客户不存在")
		return
	}
	// 四级数据范围门禁：范围外客户视为不存在（不泄露存在性）
	if !customerInDataScope(c, customer.AssignedUserID) {
		RespErr(c, http.StatusNotFound, 404, "客户不存在")
		return
	}

	var req updateStageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErrBind(c, err)
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
		RespErr(c, http.StatusBadRequest, 400, "不合法的阶段值，仅支持: arrived/ordered/delivered/lost")
		return
	}

	// 校验子状态（仅到店阶段有效）
	validSubStages := map[string]bool{
		"":                      true,
		model.SubStageTestDrive: true,
		model.SubStageQuoted:    true,
	}
	if req.JourneyStage == model.JourneyArrived && !validSubStages[req.JourneySubStage] {
		RespErr(c, http.StatusBadRequest, 400, "不合法的子状态，仅支持: test_driven/quoted")
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
		RespErr(c, http.StatusInternalServerError, 500, "更新状态失败")
		return
	}

	// P1-36(2026-09-09)：顾问手动推进→CDP 事件生产者最小闭环——
	// 到店 store_visit / 已试驾 test_drive 此前只有 AI 对话侧埋星，无人工推进闭环，标签空转。
	// 事件发布失败不进主链路（日志即可），与 chat_main 到店分支同口径（oneID 用 "c:{customerID}"）。
	if req.JourneyStage == model.JourneyArrived && customer.ID > 0 {
		attrs := map[string]any{"customer_id": customer.ID, "path": "advisor_stage_update"}
		eventName, eventType := "store_visit", "behavior"
		if req.JourneySubStage == model.SubStageTestDrive {
			eventName, eventType = "test_drive", "behavior"
		}
		if err := mq.Publish(middleware.CtxWithTrace(c), mq.TopicUserEvent, middleware.EffectiveTenantID(c),
			fmt.Sprintf("c:%d", customer.ID), eventName, mq.UserEvent{
				EventType:  eventType,
				EventName:  eventName,
				Attributes: attrs,
				OccurredAt: time.Now(),
			}); err != nil {
			log.Printf("[MQ] %s(顾问推进) 发布失败: %v", eventName, err)
		}
	}

	// 重新加载返回。
	// 先把刚写进去的值同步到内存里的这一行（FIX-5，2026-09-27）：旧写法把回读的 error 直接丢掉，
	// 回读失败时照样回 200，而响应体里是**更新前**那份快照——顾问点了"标记到店"，界面还是老阶段，
	// 他会再点一次（或以为没生效去后台找工单）。写本身已经成功（上面 1010 已判错），
	// 所以这里不能因为回读失败就报 500 把做完了的动作说成失败；正解是"回读失败就用已知的写入值"，
	// 同时留痕计数（stage_reload 旁路位）。
	customer.JourneyStage = req.JourneyStage
	customer.JourneySubStage = ""
	if req.JourneyStage == model.JourneyArrived {
		customer.JourneySubStage = req.JourneySubStage
		customer.OfflineTouch = 1
	}
	persistBypass("stage_reload", customer.ID, 0, db.RQ(c).First(&customer, id))

	log.Printf("[顾问修改状态] 客户%d 状态更新: journey_stage=%s, sub_stage=%s",
		customer.ID, req.JourneyStage, req.JourneySubStage)

	RespOK(c, "状态更新成功", customer)
}

// ============================================================
// 设置跟进提醒
// POST /api/v1/advisor/customer/:id/followup
// 创建一条跟进记录，并设置下次跟进时间
// ============================================================
// CreateFollowup 创建客户跟进记录。
// apidump:ts FollowUpRow
// 新建跟进记录整行。
func CreateFollowup(c *gin.Context) {
	id := c.Param("id")
	customerID, _ := strconv.Atoi(id)

	var customer model.Customer
	if err := db.RQ(c).First(&customer, customerID).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "客户不存在")
		return
	}
	// P1-21 修复(2026-09-09)：创建跟进接入客户数据范围门禁（销售仅可对名下/部门子树客户建跟进）
	if !canOperateCustomer(c, customer.AssignedUserID) {
		RespErr(c, http.StatusNotFound, 404, "客户不存在")
		return
	}

	var req followupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErrBind(c, err)
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
		RespErr(c, http.StatusInternalServerError, 500, "创建跟进记录失败")
		return
	}

	RespOK(c, "跟进提醒已创建", followup)
}

// ============================================================
// 跟进提醒列表
// GET /api/v1/advisor/followups
// 返回待跟进列表，支持按日期筛选
// ============================================================
// GetFollowups 返回客户跟进列表。
// apidump:ts FollowUpRow[]
// 指定顾问/日期的跟进记录。
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
		RespOK(c, "success", []model.FollowUp{})
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
		// 旁路（followup_readback_enrich）：待跟进列表的姓名/电话列读失败留空即可，
		// 跟进行本身已经查出来了。
		persistBypass("followup_readback_enrich", fu.CustomerID, 0, db.RQ(c).Select("name, phone, intent_score").First(&cust, fu.CustomerID))

		result = append(result, followupWithCustomer{
			FollowUp:      fu,
			CustomerName:  cust.Name,
			CustomerPhone: cust.Phone,
			IntentScore:   cust.IntentScore,
		})
	}

	RespOK(c, "success", result)
}

// ============================================================
// 一键接管对话
// POST /api/v1/advisor/chat/takeover
// 将会话从AI模式切换到人工模式
// ============================================================
// AdvisorTakeover 顾问接管人工对话。
func AdvisorTakeover(c *gin.Context) {
	var req takeoverRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误")
		return
	}

	var conversation model.Conversation
	if err := db.RQ(c).First(&conversation, req.ConversationID).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "会话不存在")
		return
	}

	// 修复（越权）：非admin顾问只能接管分配给自己的客户的会话
	// 四级语义：admin 家族全通；未分配(0)允许接管；已分配走 canOperateCustomer
	_, _, role := middleware.CurrentUser(c)
	var cust model.Customer
	if err := db.RQ(c).First(&cust, conversation.CustomerID).Error; err != nil {
		RespErr(c, http.StatusForbidden, 403, "无权操作此会话")
		return
	}
	if !(role == model.RoleTenantAdmin || role == model.RoleSuperAdmin) &&
		cust.AssignedUserID != 0 && !canOperateCustomer(c, cust.AssignedUserID) {
		RespErr(c, http.StatusForbidden, 403, "该客户未分配给您，无法接管")
		return
	}

	// 切换到人工模式并锁定
	// P1-2 修复(2026-09-18)：整行 Save 改字段级 Updates，避免 stale 覆写并发变更
	//（接管态、OneID 迁移后的 customer_id 等）；uat §12.2 断言口径不变
	conversation.Mode = "human"
	conversation.IsHumanLocked = true
	conversation.PendingHandoff = false
	conversation.HandoffNotifiedAt = nil
	// 必查（FIX-5，2026-09-27，transfer_to_human 同 chat_human.go 一个 kind）：
	// "已接管对话"回 200 而库里仍是 ai 态时，AI 会继续答下一句——顾问正盯着这个会话准备自己跟，
	// 客户收到的却是机器话术，这是接管动作本身失败，不是状态列抖动。
	if persistRequiredMsg(c, "transfer_to_human", conversation.CustomerID, conversation.ID,
		db.RQ(c).Model(&conversation).Updates(map[string]interface{}{
			"mode":                "human",
			"is_human_locked":     true,
			"pending_handoff":     false,
			"handoff_notified_at": nil,
		}), "接管失败，请重试") {
		return
	}

	log.Printf("[顾问接管] 会话%d已由AI切换到人工模式", conversation.ID)

	RespOK(c, "已接管对话", gin.H{
		"conversation_id": conversation.ID,
		"mode":            conversation.Mode,
	})
}

// ============================================================
// 顾问发送消息（人工回复）
// POST /api/v1/advisor/chat/send
// ============================================================
// AdvisorSendMessage 以顾问身份发送人工消息。
func AdvisorSendMessage(c *gin.Context) {
	var req advisorSendMsgRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误")
		return
	}

	var conversation model.Conversation
	if req.ConversationID > 0 {
		if err := db.RQ(c).First(&conversation, req.ConversationID).Error; err != nil {
			RespErr(c, http.StatusNotFound, 404, "会话不存在")
			return
		}
	} else {
		// P1-11 修复(2026-09-15)：无会话客户（F10 手工建客/新客）按 customer_id 找活跃会话，
		// 没有则以人工模式创建一个——权限仍走下方 conversation.CustomerID 归属校验，不旁路。
		if req.CustomerID == 0 {
			RespErr(c, http.StatusBadRequest, 400, "conversation_id 与 customer_id 至少给一个")
			return
		}
		var cust model.Customer
		if err := db.RQ(c).First(&cust, req.CustomerID).Error; err != nil {
			RespErr(c, http.StatusNotFound, 404, "客户不存在")
			return
		}
		// P1-11 路径（复核续）：无活跃会话则以人工模式保障一条（G1 收口 2026-09-16C：
		// 统一 EnsureActiveConversation——进程内查插改跨实例锁+唯一索引兜底，防与
		// web/通道并发各建一条 active 会话）
		if conversation.ID == 0 {
			convEnsured, created, cerr := chatflow.EnsureActiveConversation(
				db.EffectiveTenantIDFromGin(c), cust.ID, cust.AssignedUserID,
				func(cv *model.Conversation) { cv.Mode = "human"; cv.Channel = "web" },
			)
			if cerr != nil {
				RespErr(c, http.StatusInternalServerError, 500, "会话创建失败")
				return
			}
			conversation = convEnsured
			if created {
				log.Printf("[顾问发消息-P1-11] 客户%d 无活跃会话，新建会话%d（人工模式）", cust.ID, conversation.ID)
			}
		}
	}

	// 修复（越权）：非管理员顾问只能给自己分配到的客户发消息
	// P1-16 修复(2026-09-09)：角色硬编码 "admin" 与其余 handler 的 TenantAdmin/SuperAdmin
	// 语义分裂——tenant_admin/super_admin 反被 403、legacy "admin" 直通。统一用角色常量。
	jwtUserID, _, role := middleware.CurrentUser(c)
	if role != model.RoleTenantAdmin && role != model.RoleSuperAdmin && role != "admin" {
		var cust model.Customer
		if err := db.RQ(c).First(&cust, conversation.CustomerID).Error; err != nil {
			RespErr(c, http.StatusForbidden, 403, "无权操作此会话")
			return
		}
		if cust.AssignedUserID != jwtUserID {
			// 如果客户尚未分配，允许接管（先分配再发消息）
			if cust.AssignedUserID == 0 {
				// D10 修复(2026-09-14)：读后写竞态——两顾问并发发首条消息旧实现都会"分配给自己+发送"，
				// assigned 最后写赢，客户被两人同时接。改条件抢占：仅当仍无人认领才成功，输者 403。
				res := db.RQ(c).Model(&model.Customer{}).
					Where("id = ? AND COALESCE(assigned_user_id, 0) = 0", cust.ID).
					Updates(map[string]interface{}{
						"assigned_user_id":  jwtUserID,
						"assignment_reason": "ai_handover",
					})
				if res.Error != nil {
					RespErr(c, http.StatusInternalServerError, 500, "接管失败")
					return
				}
				if res.RowsAffected == 0 {
					RespErr(c, http.StatusForbidden, 403, "该客户刚被其他顾问接管，请刷新客户列表")
					return
				}
				log.Printf("[顾问发消息-自动分配] 客户%d 未分配，自动分配给顾问%d", cust.ID, jwtUserID)
			} else {
				RespErr(c, http.StatusForbidden, 403, "该客户已分配给其他顾问，无法发送消息")
				return
			}
		}
	}

	// 确保人工模式
	// P1-2 修复(2026-09-18)：字段级 Updates——此路径读到会话后 AI 可能已被并发接管/关AI，
	// 整行 Save 会把对方刚写的列覆写回 stale 值
	now := time.Now()
	convUpdates := map[string]interface{}{
		"is_ai_reply_enabled": false,
		"last_human_reply_at": &now,
		"last_message_at":     &now,
	}
	if conversation.Mode != "human" {
		conversation.Mode = "human"
		conversation.IsHumanLocked = true
		convUpdates["mode"] = "human"
		convUpdates["is_human_locked"] = true
	}
	conversation.IsAiReplyEnabled = false
	conversation.LastHumanReplyAt = &now
	conversation.LastMessageAt = &now
	// 必查（FIX-5，2026-09-27，human_takeover_lock）：这一步没写进去，AI 回复开关就还是开的，
	// 顾问这句话发出去后同一段会话会被 AI 再答一遍（双答）。
	// 刻意排在消息落库**之前**：失败时一条消息都没写，顾问重试即干净重试。
	if persistRequiredMsg(c, "human_takeover_lock", conversation.CustomerID, conversation.ID,
		db.RQ(c).Model(&conversation).Updates(convUpdates), "接管状态更新失败，请重试") {
		return
	}

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
	// 必查（FIX-5，2026-09-27，human_reply，与 chat_human.go HumanReply 同一个 kind）：
	// 这一行的 ID 既进响应体又进 WS 推送，写失败却往下走 = 推送一个库里不存在的消息 ID，
	// 客户端点进去是空白，顾问以为发了、客户没收到。
	if persistRequiredMsg(c, "human_reply", conversation.CustomerID, conversation.ID,
		db.RQ(c).Create(&humanMsg), "人工消息发送失败，请重试") {
		return
	}
	// P1-1 实时推送：顾问真人回复通知客户端（推送消息内容，前端即时更新）
	_, senderName, _ := middleware.CurrentUser(c)
	notifyWSWithContent(middleware.EffectiveTenantID(c), conversation.CustomerID, conversation.ID, "human",
		humanMsg.ID, req.Content, senderName, now.Format("2006-01-02T15:04:05Z"))

	// P3 数据飞轮（2026-08-26）：顾问真人回复入素材池（优质回复资产回收）
	// 脱敏：手机号掩码后入库；评分/审核走超管面板
	go func(msgID uint, convID, custID, uid uint, content string, tid uint) {
		defer func() { _ = recover() }()
		masked := pii.MaskPhoneInText(content)
		// 旁路（kb_material）：素材池是数据飞轮的资产回收，失败不该影响已经发出去的人工回复；
		// 这条 goroutine 没有 gin ctx，显式带 tid 落列（C7 红线），失败经计数器可见。
		persistBypass("kb_material", custID, convID, db.DB.Create(&model.KbFeedbackMaterial{
			TenantID: tid, Conversation: convID, MessageID: msgID,
			Source: "human", Content: masked,
		}))
	}(humanMsg.ID, conversation.ID, conversation.CustomerID, jwtUserID, req.Content, conversation.TenantID)

	RespOK(c, "发送成功", humanMsg)
}

// ============================================================
// 手动触发AI回复（顾问点击"AI回复"按钮时调用）
// POST /api/v1/advisor/chat/ai-reply
// 场景：人工接管后AI被暂停，顾问想用AI辅助回复时手动触发
// ============================================================
// AdvisorTriggerAIReply 手动触发一次 AI 回复建议。
func AdvisorTriggerAIReply(c *gin.Context) {
	extendWriteDeadlineForAI(c) // D4：同步 OrchestrateReply 最坏可达 AI 链总预算 110s，延长本连接写截止
	var req struct {
		ConversationID uint   `json:"conversation_id" binding:"required"`
		Content        string `json:"content"` // 可选：指定AI根据什么内容回复，为空则取最近客户消息
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErrBind(c, err)
		return
	}

	var conversation model.Conversation
	if err := db.RQ(c).First(&conversation, req.ConversationID).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "会话不存在")
		return
	}

	var customer model.Customer
	if err := db.RQ(c).First(&customer, conversation.CustomerID).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "客户不存在")
		return
	}

	if !canOperateCustomer(c, customer.AssignedUserID) {
		RespErr(c, http.StatusForbidden, 403, "无权操作该客户")
		return
	}

	// 优先用请求中指定的内容，否则取最近一条客户消息
	userInput := req.Content
	if userInput == "" {
		var lastCustomerMsg model.Message
		// 必查（last_customer_msg_read）：读失败时 lastCustomerMsg.ID 也是 0，
		// 旧写法会把它报成"没有找到客户消息"——把一次 DB 抖动说成客户没说过话，
		// 顾问据此判断"这个会话是空的"，实际只是没读到。
		if persistRequiredMsg(c, "last_customer_msg_read", customer.ID, conversation.ID,
			db.RQ(c).Where("conversation_id = ? AND sender_type = ?", conversation.ID, "customer").
				Order("created_at DESC").Limit(1).Find(&lastCustomerMsg), "客户消息读取失败，请重试") {
			return
		}
		if lastCustomerMsg.ID == 0 {
			RespErr(c, http.StatusBadRequest, 400, "没有找到客户消息，无法生成AI回复")
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
		TenantID:       customer.TenantID,                          // M1租户隔离修复：模板/卖点召回按此过滤
		DeptIDs:        service.DeptChainForUser(currentUserID(c)), // 三级包：登录顾问部门链
	}
	strategyOutput := strategy.DefaultEngine.Infer(strategyInput)

	// 生成AI回复
	aiReply := flow.DefaultEngine.OrchestrateReply(middleware.CtxWithTrace(c), &customer, conversation.ID, userInput, &strategyOutput, service.DeptChainForUser(currentUserID(c)))

	// 内容安全闸门（C1）：顾问触发的AI回复同样出站过滤；BLOCK 则发退场语并关AI等接管
	if action, out := ContentsafetyGate(aiReply, conversation.ID); aiReply != "" && action != GatePass {
		if action == GateRewrite {
			aiReply = out
		} else {
			aiReply = SafetyHandoffReply()
			// 必查（content_safety_takeover）：内容安全命中后的"关 AI + 锁人工"是**安全动作**，
			// 写失败还继续回 200，等于闸门说"这条不许发、交给人工"，而系统仍然让 AI 自动答下一句。
			if persistRequiredMsg(c, "content_safety_takeover", customer.ID, conversation.ID,
				db.RQ(c).Model(&conversation).Updates(map[string]interface{}{
					"mode": "human", "is_human_locked": true, "is_ai_reply_enabled": false,
				}), "转人工失败，请重试") {
				return
			}
			log.Printf("[顾问] 会话%d 内容安全拦截(AI代答)，已转人工", conversation.ID)
		}
	}

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
	// 必查（FIX-5，ai_reply）：同正式链——这一行是本次代答的唯一留痕，写失败必须显式失败。
	if persistRequired(c, "ai_reply", customer.ID, conversation.ID, db.RQ(c).Create(&aiMsg)) {
		return
	}

	// 更新会话（P1-2 修复 2026-09-18：字段级 Updates，仅刷新最后消息三列）
	// 旁路（conversation_state）：排序/时间戳列，失败只让会话在列表里排得靠后，不打断本次代答。
	conversation.LastMessageAt = &now
	conversation.LastTid = strategyOutput.TemplateID
	conversation.LastAnchorType = strategyOutput.FinalAnchor
	persistBypass("conversation_state", customer.ID, conversation.ID, db.RQ(c).Model(&conversation).Updates(map[string]interface{}{
		"last_message_at":  &now,
		"last_tid":         strategyOutput.TemplateID,
		"last_anchor_type": strategyOutput.FinalAnchor,
	}))

	log.Printf("[顾问触发AI回复] 会话%d 客户%d AI回复已生成并保存", conversation.ID, customer.ID)

	RespOK(c, "AI回复已生成", aiMsg)
}

// ============================================================
// 策略话术推荐
// GET /api/v1/advisor/strategy/recommend?customer_id=X&conversation_id=X
// 根据客户画像和会话状态，推荐当前最合适的话术
// ============================================================
// GetStrategyRecommend 返回客户当前策略推荐动作。
func GetStrategyRecommend(c *gin.Context) {
	customerID, _ := strconv.Atoi(c.Query("customer_id"))
	conversationID, _ := strconv.Atoi(c.Query("conversation_id"))

	if customerID == 0 {
		RespErr(c, http.StatusBadRequest, 400, "customer_id必填")
		return
	}

	// 获取客户
	var customer model.Customer
	if err := db.RQ(c).First(&customer, customerID).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "客户不存在")
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
		TenantID:       customer.TenantID,                          // M1租户隔离修复：模板/卖点召回按此过滤
		DeptIDs:        service.DeptChainForUser(currentUserID(c)), // 三级包：登录顾问部门链
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
	persistBypass("template_recommend_read", 0, 0, db.RQ(c).Where("anchor_type = ? AND status = 1", output.FinalAnchor).
		Order("priority DESC").
		Limit(2).
		Find(&altTemplates))

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

	RespOK(c, "success", gin.H{
		"customer_id":   customer.ID,
		"intent_score":  customer.IntentScore,
		"urgency_level": output.UrgencyLevel,
		"route_result":  output.RouteResult,
		"recommends":    recommends,
	})
}

// ============================================================
// 聊天历史查询（客户端和销售端共用）
// GET /api/v1/chat/history?customer_id=X&conversation_id=X&limit=50
// ============================================================
// GetChatHistory 返回顾问可见的聊天记录。
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

	// 参数缺失先按 400 回，不能被下面的归属门禁抢先判成 403：
	// "你没说要查谁"是入参问题（前端据此修调用），"你不能查这个人"是权限问题（据此提示换客户），
	// 两者混成一个码会让前端把漏参弹成"无权限"，用户完全无从自救。
	if customerID <= 0 && conversationID <= 0 {
		RespErr(c, http.StatusBadRequest, 400, "需要传customer_id或conversation_id")
		return
	}

	// FIX-5(2026-09-27)：归属门禁改为 fail-closed——「读得到客户」是放行的前提，不是可选步骤。
	// 旧写法把两路校验整段包在 `if db.RQ(c).First(&cust, targetCustomerID).Error == nil { … }` 里：
	// 客户行一旦读失败（本仓 2026-09-26 刚实测抓到过 SQLSTATE 53300 连接槽打满、以及语句超时），
	// error 非 nil 就让整段门禁静默跳过，请求继续往下把该会话的全量聊天记录返回成 200——
	// 安全校验恰好在这个接口最脆弱的时候失效。
	// 现改为：目标会话/客户解析不出、读失败、读不到 → 一律 403。
	// 统一回 403 而不区分 404/500，是为了不把"这个 ID 存不存在"泄露给探测方
	// （口径与 chat_main.go:187-197 的会话 IDOR 防线一致：那里也是合并成同一个不可区分的拒绝）。
	targetCustomerID := uint(customerID)
	if conversationID > 0 {
		var conv model.Conversation
		if err := db.RQ(c).First(&conv, conversationID).Error; err != nil {
			log.Printf("[聊天历史-告警] 会话读取失败或不存在: 会话%d err=%v", conversationID, err)
			RespErr(c, http.StatusForbidden, 403, "无权访问该客户聊天记录")
			return
		}
		targetCustomerID = conv.CustomerID
	}
	if targetCustomerID == 0 {
		RespErr(c, http.StatusForbidden, 403, "无权访问该客户聊天记录")
		return
	}
	var cust model.Customer
	if err := db.RQ(c).First(&cust, targetCustomerID).Error; err != nil {
		log.Printf("[聊天历史-告警] 目标客户读取失败或不存在: 客户%d err=%v", targetCustomerID, err)
		RespErr(c, http.StatusForbidden, 403, "无权访问该客户聊天记录")
		return
	}
	// C3：横向越权防线。匿名请求必须携带与目标客户一致的 visitor_key；
	// 登录用户（顾问/管理员）在此短路放行，其数据范围由下面的 P1-15 门禁控制。
	if !middleware.CheckVisitorKey(c, cust.VisitorKey) {
		RespErr(c, http.StatusForbidden, 403, "无权访问该客户聊天记录")
		return
	}
	// P1-15 修复(2026-09-09)：登录态（顾问/管理员）分支此前只过 CheckVisitorKey——
	// 其登录分支恒放行，导致任意 sales 可拉租户内任意客户全量聊天记录（与
	// "顾问只见名下客户"红线冲突）。现补四级数据范围门禁。
	if uidV, ok := c.Get("user_id"); ok {
		if uid, _ := uidV.(uint); uid > 0 {
			if !customerInDataScope(c, cust.AssignedUserID) {
				RespErr(c, http.StatusForbidden, 403, "无权访问该客户聊天记录")
				return
			}
		}
	}

	query := db.RQ(c).Model(&model.Message{})

	// 优先按会话ID查询
	if conversationID > 0 {
		query = query.Where("conversation_id = ?", conversationID)
	} else if customerID > 0 {
		// 按客户ID查询：找最近的活跃会话
		// FIX-5(2026-09-27)：这两处读同样是"错误即静默"——旧写法把 error 丢掉，
		// 库一抖就回 200 + 空列表，顾问/客户端看到的是"这个客户什么都没说"，
		// 与消息落库失败那个缺陷是同一个现象（丢了却报成功）。读失败必须显式失败。
		var conv model.Conversation
		if err := db.RQ(c).Where("customer_id = ? AND status = ?", customerID, "active").
			Order("updated_at DESC").Limit(1).Find(&conv).Error; err != nil {
			RespErrInternal(c, err, "聊天记录读取失败，请重试")
			return
		}
		if conv.ID == 0 {
			// 没有活跃会话，返回空
			RespOK(c, "success", []model.Message{})
			return
		}
		query = query.Where("conversation_id = ?", conv.ID)
	} else {
		RespErr(c, http.StatusBadRequest, 400, "需要传customer_id或conversation_id")
		return
	}

	var messages []model.Message
	if err := query.Order("created_at ASC").Limit(limit).Find(&messages).Error; err != nil {
		RespErrInternal(c, err, "聊天记录读取失败，请重试")
		return
	}

	RespOK(c, "success", messages)
}

// ============================================================
// 顾问列表（供顾问端切换身份）
// GET /api/v1/advisor/list
// 返回所有role=sales的用户列表(id, real_name, username)
// ============================================================
// GetAdvisorList 返回可分配顾问列表。
// apidump:ts AdvisorRef[]
// 顾问下拉列表（id/real_name/username）。
func GetAdvisorList(c *gin.Context) {
	var users []model.User
	// 修复Bug1（2026-08-22）：角色改用 model.RoleSales 常量。
	// 根因：组织迁移 sales→user 后硬编码"sales"查空，顾问端列表一直为空
	// 旁路（advisor_dict_read）：下拉字典读失败回空列表——比报错打断这条页面更好，
	// 但"顾问分配下拉凭空变空"必须可数，否则运营会以为这家租户没招销售。
	persistBypass("advisor_dict_read", 0, 0, db.RQ(c).Where("role = ? AND status = 1", model.RoleSales).
		Select("id, real_name, username").
		Order("id ASC").
		Find(&users))

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

	RespOK(c, "success", result)
}
