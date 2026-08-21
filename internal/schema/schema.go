package schema

// ============================================================
// 通用响应结构
// 所有API统一返回格式，方便前端处理
// ============================================================

// Response 通用响应结构
type Response struct {
	Code    int         `json:"code"`    // 状态码: 0-成功, 其他-失败
	Message string      `json:"message"` // 消息
	Data    interface{} `json:"data"`    // 数据
}

// PageResponse 分页响应结构
type PageResponse struct {
	Total    int64       `json:"total"`     // 总数
	Page     int         `json:"page"`      // 当前页
	PageSize int         `json:"page_size"` // 每页数量
	List     interface{} `json:"list"`      // 数据列表
}

// Pagination 分页请求参数
type Pagination struct {
	Page     int `form:"page" json:"page"`           // 页码
	PageSize int `form:"page_size" json:"page_size"` // 每页数量
}

// GetOffset 获取偏移量
func (p *Pagination) GetOffset() int {
	if p.Page <= 0 {
		p.Page = 1
	}
	if p.PageSize <= 0 {
		p.PageSize = 10
	}
	return (p.Page - 1) * p.PageSize
}

// ============================================================
// 认证相关
// ============================================================

// LoginRequest 登录请求
type LoginRequest struct {
	Username   string `json:"username" binding:"required"` // 用户名
	Password   string `json:"password" binding:"required"` // 密码
	TenantCode string `json:"tenant_code"`                 // 租户企业码（登录时必传，用于多租户区分）
}

// LoginResponse 登录响应
type LoginResponse struct {
	Token string      `json:"token"` // JWT Token
	User  interface{} `json:"user"`  // 用户信息
}

// ============================================================
// 客户相关
// ============================================================

// CreateCustomerRequest 创建客户请求
type CreateCustomerRequest struct {
	Name           string   `json:"name"`
	Phone          string   `json:"phone"`
	WechatID       string   `json:"wechat_id"`
	Gender         int      `json:"gender"`
	Age            int      `json:"age"`
	Region         string   `json:"region"`
	City           string   `json:"city"`
	Career         string   `json:"career"`
	CustomerType   string   `json:"customer_type"`
	InterestModel  string   `json:"interest_model"`
	CurrentCar     string   `json:"current_car"`
	CarAge         float64  `json:"car_age"`
	Source         string   `json:"source"`
	Budget         float64  `json:"budget"`
	DecisionCycle  int      `json:"decision_cycle"`
	AssignedUserID uint     `json:"assigned_user_id"`
	Tags           []string `json:"tags"`
	Remark         string   `json:"remark"`
}

// UpdateCustomerRequest 更新客户请求
type UpdateCustomerRequest struct {
	Name           string   `json:"name"`
	Phone          string   `json:"phone"`
	Gender         int      `json:"gender"`
	Age            int      `json:"age"`
	Region         string   `json:"region"`
	Career         string   `json:"career"`
	InterestModel  string   `json:"interest_model"`
	Budget         float64  `json:"budget"`
	DecisionCycle  int      `json:"decision_cycle"`
	IntentScore    float64  `json:"intent_score"`
	TrustLevel     float64  `json:"trust_level"`
	ResistanceType string   `json:"resistance_type"`
	Tags           []string `json:"tags"`
	Remark         string   `json:"remark"`
	AssignedUserID uint     `json:"assigned_user_id"`
	Status         int      `json:"status"`
}

// CustomerListRequest 客户列表请求
type CustomerListRequest struct {
	Pagination
	Keyword      string `form:"keyword"`       // 关键词搜索
	Status       int    `form:"status"`        // 状态筛选
	Source       string `form:"source"`        // 来源筛选
	CustomerType string `form:"customer_type"` // 类型筛选
	Tag          string `form:"tag"`           // 标签筛选
}

// ============================================================
// 对话相关
// ============================================================

// ChatRequest 对话请求（客户发消息）
type ChatRequest struct {
	CustomerID     uint   `json:"customer_id" binding:"required"` // 客户ID
	ConversationID uint   `json:"conversation_id"`                // 会话ID（新会话为空）
	Content        string `json:"content" binding:"required"`     // 消息内容
	SenderType     string `json:"sender_type"`                    // 发送方，默认customer
}

// ChatResponse 对话响应
type ChatResponse struct {
	ConversationID    uint        `json:"conversation_id"`    // 会话ID
	Message           interface{} `json:"message"`            // AI回复的消息（本轮主消息）
	AssistantMessages interface{} `json:"assistant_messages"` // AI/人工回复消息列表（带真实DB ID，前端用此字段去重）
	EarlierMessages   interface{} `json:"earlier_messages"`   // 更早的消息（冷启动秒回等，按时间顺序排列）
	StrategyInfo      interface{} `json:"strategy_info"`      // 策略信息（B端可见）
	RouteResult       string      `json:"route_result"`       // 路由结果
	Mode              string      `json:"mode"`               // 当前模式
	NewTags           []string    `json:"new_tags"`           // 本轮新打上的标签（自动打标结果）
	PendingHandoff    bool        `json:"pending_handoff"`    // 是否待人工接管（软接管状态）
	Merged            bool        `json:"merged"`             // Bug 1 修复：是否为合并请求（前端据此判断是否渲染新气泡）
	MergedNote        string      `json:"merged_note"`        // Bug 1 修复：合并说明文案（merged=true 时才有值）
	MergedCustomerID  uint        `json:"merged_customer_id"` // OneID合并：留资时手机号匹配到老客户，返回老客户ID（前端切换）
	CustomerMsgID     uint        `json:"customer_msg_id"`    // 客户本条消息的真实DB ID（相似消息合并等分支用，供前端替换temp ID）
}

// StrategyInfo 策略信息（返回给前端的策略决策详情）
type StrategyInfo struct {
	AnchorType       int     `json:"anchor_type"`        // 锚类型（最终锚，经过所有降级之后）
	AnchorTypeName   string  `json:"anchor_type_name"`   // 锚类型名称
	TemplateID       string  `json:"template_id"`        // 使用的话术模板ID
	RouteResult      string  `json:"route_result"`       // 路由结果
	UrgencyLevel     string  `json:"urgency_level"`      // 紧迫等级 L1/L2/L3
	ExchangeFlag     bool    `json:"exchange_flag"`      // 是否触发条件交换
	IntentScore      float64 `json:"intent_score"`       // 当前意向分
	TrustLevel       float64 `json:"trust_level"`        // 信任度
	HookRate         float64 `json:"hook_rate"`          // 接钩率
	CurrentStage     int     `json:"current_stage"`      // 当前心智阶段
	HighIntentRounds int     `json:"high_intent_rounds"` // 高意向持续轮数
	Emotion          string  `json:"emotion"`            // 情绪
	SoftDowngrade    bool    `json:"soft_downgrade"`     // 是否触发软降级（接钩率低/沉默长/情绪负面）
	OriginalAnchor   int     `json:"original_anchor"`    // softmax原始锚（不含任何降级）
	// ---- Step2.5 阶段锁降级信息 ----
	StageDowngraded bool `json:"stage_downgraded"`  // 是否因心智阶段锁而被降级
	StageBeforeLock int  `json:"stage_before_lock"` // 阶段锁降级前的锚类型
	StageCeilingAgg int  `json:"stage_ceiling_agg"` // 当前阶段允许的aggressiveness上限
}

// ConversationListRequest 会话列表请求
type ConversationListRequest struct {
	Pagination
	CustomerID uint   `form:"customer_id"`
	Status     string `form:"status"`
	Mode       string `form:"mode"`
}

// ============================================================
// 策略中心相关
// ============================================================

// StrategyTestRequest 策略测试请求
// 支持直接传参数覆盖T向量和S状态，方便测试不同场景
type StrategyTestRequest struct {
	CustomerID     uint   `json:"customer_id" binding:"required"`
	ConversationID uint   `json:"conversation_id"`
	CustomerInput  string `json:"customer_input"` // 客户输入（用于模拟）

	// ---- 以下为可选覆盖参数，传了就用，不传用客户默认值 ----
	IntentScore      *float64 `json:"intent_score"`       // 意向分覆盖
	TrustLevel       *float64 `json:"trust_level"`        // 信任度覆盖
	ResistanceType   *int     `json:"resistance_type"`    // 抗性类型覆盖 (0=无,1=价格,2=规格,3=服务,4=品牌)
	PriceSensitivity *float64 `json:"price_sensitivity"`  // 价格敏感度覆盖
	CurrentStage     *int     `json:"current_stage"`      // 心智阶段覆盖
	Attempts         *int     `json:"attempts"`           // 已抛锚次数
	HookRate         *float64 `json:"hook_rate"`          // 接钩率
	HighIntentRounds *int     `json:"high_intent_rounds"` // 高意向持续轮数
	Emotion          *string  `json:"emotion"`            // 情绪: neutral/positive/negative
	SilentDuration   *int     `json:"silent_duration"`    // 沉默时长(秒)
}

// TemplateListRequest 话术模板列表请求
type TemplateListRequest struct {
	Pagination
	AnchorType int    `form:"anchor_type"`
	Category   string `form:"category"`
	Status     int    `form:"status"`
	Keyword    string `form:"keyword"`
}

// CreateTemplateRequest 创建话术模板请求
type CreateTemplateRequest struct {
	ID               string   `json:"id" binding:"required"`
	AnchorType       int      `json:"anchor_type" binding:"required"`
	SubType          string   `json:"sub_type"`
	Name             string   `json:"name" binding:"required"`
	Category         string   `json:"category"`
	TriggerTags      []string `json:"trigger_tags"`
	RequiredTags     []string `json:"required_tags"`
	MinIntent        float64  `json:"min_intent"`
	MaxIntent        float64  `json:"max_intent"`
	ApplicableModels []string `json:"applicable_models"`
	PromptTemplate   string   `json:"prompt_template" binding:"required"`
	HookTemplate     string   `json:"hook_template"`
	HookFields       []string `json:"hook_fields"`
	RequiredFeatures []string `json:"required_features"`
	Priority         int      `json:"priority"`
	Status           int      `json:"status"`
}

// ============================================================
// 流程引擎相关
// ============================================================

// FlowStartRequest 启动流程请求
type FlowStartRequest struct {
	FlowCode       string `json:"flow_code"`       // 流程编码
	CustomerID     uint   `json:"customer_id"`     // 客户ID
	ConversationID uint   `json:"conversation_id"` // 会话ID
}

// FlowAdvanceRequest 推进流程请求
type FlowAdvanceRequest struct {
	InstanceID uint   `json:"instance_id" binding:"required"` // 流程实例ID
	Route      string `json:"route"`                          // 路由结果（用于条件判断）
}

// ============================================================
// 统计相关
// ============================================================

// StatsOverview 统计概览
type StatsOverview struct {
	TotalCustomers      int64   `json:"total_customers"`      // 总客户数
	NewCustomersToday   int64   `json:"new_customers_today"`  // 今日新增
	ActiveConversations int64   `json:"active_conversations"` // 活跃会话数
	ConversionRate      float64 `json:"conversion_rate"`      // 转化率
	AvgIntentScore      float64 `json:"avg_intent_score"`     // 平均意向分
	HumanTransferRate   float64 `json:"human_transfer_rate"`  // 转人工率
}

// AnchorStats 锚类型统计
type AnchorStats struct {
	AnchorType     int     `json:"anchor_type"`
	AnchorName     string  `json:"anchor_name"`
	UsageCount     int64   `json:"usage_count"`
	HookRate       float64 `json:"hook_rate"`
	ConversionRate float64 `json:"conversion_rate"`
}
