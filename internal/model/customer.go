package model

import (
	"encoding/json"
	"time"
)

// ============================================================
// 客户模型 - C端客户（潜客/车主）
// 核心是T向量（d=32维客户画像），存储客户全维度信息
// 为什么用JSON存T向量？因为维度可能扩展，用JSON最灵活
// ============================================================

// Customer 客户表
type Customer struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	Name             string    `gorm:"size:50" json:"name"`                               // 客户姓名
	Phone            string    `gorm:"size:20;index" json:"phone"`                        // 手机号
	WechatID         string    `gorm:"size:50;index" json:"wechat_id"`                    // 微信号
	Gender           int       `gorm:"default:0" json:"gender"`                           // 性别: 0-未知 1-男 2-女
	Age              int       `json:"age"`                                               // 年龄
	Region           string    `gorm:"size:50" json:"region"`                             // 地域
	City             string    `gorm:"size:50" json:"city"`                               // 城市
	Career           string    `gorm:"size:50" json:"career"`                             // 职业
	CustomerType     string    `gorm:"size:20;default:potential" json:"customer_type"`    // 客户身份: potential(潜客)/owner(车主)
	InterestModel    string    `gorm:"size:50" json:"interest_model"`                     // 兴趣车型
	CurrentCar       string    `gorm:"size:50" json:"current_car"`                        // 现有车型
	CarAge           float64   `json:"car_age"`                                           // 现车年限
	Source           string    `gorm:"size:30" json:"source"`                             // 流量来源
	Budget           float64   `json:"budget"`                                            // 预算（万元）
	DecisionCycle    int       `json:"decision_cycle"`                                    // 决策周期（天）
	StoreVisited     int       `gorm:"default:0" json:"store_visited"`                    // 到店状态: 0-未到店 1-已到店 2-多次到店
	TrustLevel       float64   `gorm:"default:0.3" json:"trust_level"`                    // 信任度 0-1
	IntentScore      float64   `gorm:"default:0.2" json:"intent_score"`                   // 意向分 0-1
	PriceSensitivity float64   `gorm:"default:0.5" json:"price_sensitivity"`              // 价格敏感度 0-1
	BrandAwareness   float64   `gorm:"default:0.3" json:"brand_awareness"`                // 品牌认知度 0-1
	ResistanceType   string    `gorm:"size:20;default:none" json:"resistance_type"`       // 抗性类型: none/price/spec/service/brand
	JourneyStage     string    `gorm:"size:20;default:ai_connected" json:"journey_stage"` // 客户旅程阶段: ai_connected/human_connected/lead_captured/arrived/ordered/delivered/lost
	JourneySubStage  string    `gorm:"size:20;default:''" json:"journey_sub_stage"`       // 到店子状态: 空/test_driven/quoted（只有quoted后才允许促单）
	Tags             string    `gorm:"type:text" json:"tags"`                             // 标签列表(JSON数组)
	TVectorJSON      string    `gorm:"column:t_vector;type:text" json:"t_vector_json"`    // T向量原始JSON(32维)
	Remark           string    `gorm:"type:text" json:"remark"`                           // 备注
	AssignedUserID   uint      `json:"assigned_user_id"`                                  // 归属销售ID
	TenantID         uint      `gorm:"default:0;index" json:"-"`                          // 租户ID，0=超级管理员全局可见，非0=某租户隔离
	AssignmentReason string    `gorm:"size:20;default:''" json:"assignment_reason"`       // 分配原因: lead_captured/ai_handover
	Status           int       `gorm:"default:1" json:"status"`                           // 状态: 1-正常 0-无效
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// TableName 指定表名
func (Customer) TableName() string {
	return "customers"
}

// GetTags 获取标签列表
func (c *Customer) GetTags() []string {
	if c.Tags == "" {
		return []string{}
	}
	var tags []string
	json.Unmarshal([]byte(c.Tags), &tags)
	return tags
}

// SetTags 设置标签列表
func (c *Customer) SetTags(tags []string) {
	data, _ := json.Marshal(tags)
	c.Tags = string(data)
}

// AddTag 添加标签
func (c *Customer) AddTag(tag string) {
	tags := c.GetTags()
	for _, t := range tags {
		if t == tag {
			return // 已存在，不重复添加
		}
	}
	tags = append(tags, tag)
	c.SetTags(tags)
}

// GetTVector 获取T向量（32维float数组）
// T[0]=意向分, T[1]=价格敏感度, T[2]=品牌认知度, T[3]=兴趣车型编码
// T[4]=到店状态, T[5]=决策周期, T[6]=信任度, T[7]=流量来源编码
// T[8]=性别, T[9]=年龄段, T[10]=地域编码, T[11]=职业圈层编码
// T[12]=用车场景(multi-hot展开后单值), T[13]=现车年限
// T[14]=抗性类型编码, T[15]=客户身份编码, T[16]=地域商圈编码
// T[17-31]=预留
func (c *Customer) GetTVector() [32]float64 {
	var t [32]float64
	if c.TVectorJSON != "" {
		var vec []float64
		if err := json.Unmarshal([]byte(c.TVectorJSON), &vec); err == nil {
			for i := 0; i < len(vec) && i < 32; i++ {
				t[i] = vec[i]
			}
			return t
		}
	}
	return c.BuildBaseTVector()
}

// BuildBaseTVector 从客户结构化字段直接计算"基准T向量"，不读取TVectorJSON
// 修复：ApplyTagWeightsToTVector 之前一直在已保存的（可能已叠加过标签权重的）
// TVectorJSON 上继续叠加，导致多轮对话后0-1维度被反复推向0或1。
// 现在打标计算一律以这个"基准值"为起点重新算，保证结果可重复、不越滚越偏。
func (c *Customer) BuildBaseTVector() [32]float64 {
	var t [32]float64
	t[0] = c.IntentScore                     // 意向分
	t[1] = c.PriceSensitivity                // 价格敏感度
	t[2] = c.BrandAwareness                  // 品牌认知度
	t[3] = modelCode(c.InterestModel)        // 兴趣车型编码
	t[4] = float64(c.StoreVisited)           // 到店状态
	t[5] = float64(c.DecisionCycle)          // 决策周期
	t[6] = c.TrustLevel                      // 信任度
	t[7] = sourceCode(c.Source)              // 流量来源编码
	t[8] = float64(c.Gender)                 // 性别
	t[9] = ageGroupCode(c.Age)               // 年龄段编码
	t[10] = regionCode(c.Region)             // 地域编码
	t[11] = careerCode(c.Career)             // 职业圈层编码
	t[12] = 0                                // 用车场景（multi-hot简化为单值）
	t[13] = c.CarAge                         // 现车年限
	t[14] = resistanceCode(c.ResistanceType) // 抗性类型编码
	t[15] = customerTypeCode(c.CustomerType) // 客户身份编码
	t[16] = 0                                // 地域商圈编码
	// T[17-31] 预留
	return t
}

// SaveTVector 保存T向量到数据库
func (c *Customer) SaveTVector(t [32]float64) {
	vec := t[:]
	data, _ := json.Marshal(vec)
	c.TVectorJSON = string(data)
}

// ============================================================
// 以下是辅助编码函数，将文本映射为数值
// 为什么要编码？T向量是数值向量，需要做特征编码
// ============================================================

func modelCode(model string) float64 {
	modelMap := map[string]float64{
		"":         0,
		"越野SUV-X1": 1,
		"越野SUV-X3": 2,
		"越野SUV-X5": 3,
		"越野SUV-X7": 4,
		"皮卡系列":     5,
	}
	if code, ok := modelMap[model]; ok {
		return code
	}
	return 99 // 其他车型
}

func sourceCode(source string) float64 {
	sourceMap := map[string]float64{
		"":      0,
		"抖音":    1,
		"小红书":   2,
		"微信":    3,
		"百度":    4,
		"门店自然":  5,
		"老客转介绍": 6,
	}
	if code, ok := sourceMap[source]; ok {
		return code
	}
	return 99
}

func ageGroupCode(age int) float64 {
	if age <= 0 {
		return 0
	}
	if age < 25 {
		return 1
	}
	if age < 35 {
		return 2
	}
	if age < 45 {
		return 3
	}
	if age < 55 {
		return 4
	}
	return 5
}

func regionCode(region string) float64 {
	regionMap := map[string]float64{
		"":   0,
		"华东": 1,
		"华南": 2,
		"华北": 3,
		"华中": 4,
		"西南": 5,
		"西北": 6,
		"东北": 7,
	}
	if code, ok := regionMap[region]; ok {
		return code
	}
	return 99
}

func careerCode(career string) float64 {
	careerMap := map[string]float64{
		"":     0,
		"企业主":  1,
		"高管":   2,
		"白领":   3,
		"自由职业": 4,
		"公务员":  5,
		"工程师":  6,
		"医生":   7,
		"教师":   8,
	}
	if c, ok := careerMap[career]; ok {
		return c
	}
	return 99
}

func resistanceCode(resistance string) float64 {
	resistanceMap := map[string]float64{
		"none":    0, // 无抗性
		"price":   1, // 价格抗性
		"spec":    2, // 规格/配置抗性
		"service": 3, // 服务抗性
		"brand":   4, // 品牌抗性
	}
	if code, ok := resistanceMap[resistance]; ok {
		return code
	}
	return 0
}

func customerTypeCode(ct string) float64 {
	switch ct {
	case "potential":
		return 0 // 潜客
	case "owner":
		return 1 // 车主
	default:
		return 0
	}
}

// ============================================================
// 客户旅程阶段定义
// 阶段顺序：lead（线索）→ lead_captured（留资）→ invited（邀约）
//         → arrived（到店）→ test_drive（试驾）→ ordered（下定）→ delivered（交付）
// 用于价格管控：未到店客户不能获取具体价格信息
// ============================================================

// ============================================================
// 客户线索状态机（7+1状态，配合心智建立阶段）
// 设计原则：
//   1. AI建联是起点，所有客户首先进入ai_connected
//   2. 人工建联和已留资没有必然前后顺序（AI可能惹怒客户需先人工安抚，
//      正常AI接住则先留资后人工确认）
//   3. 已到店之后的状态严格有序
//   4. 已战败是独立分支，任何状态都可能直接转入
//   5. 已到店有子状态：空(刚到店) / test_driven(已试驾) / quoted(已报价)
//      只有quoted子状态之后，策略引擎才允许促单
// ============================================================

// 旅程阶段常量
const (
	JourneyAIConnected    = "ai_connected"    // AI建联（起点，首次对话自动进入）
	JourneyHumanConnected = "human_connected" // 人工建联（与已留资无固定顺序）
	JourneyLeadCaptured   = "lead_captured"   // 已留资（与人工建联无固定顺序）
	JourneyArrived        = "arrived"         // 已到店（子状态：test_driven/quoted）
	JourneyOrdered        = "ordered"         // 已下单
	JourneyDelivered      = "delivered"       // 已交车
	JourneyLost           = "lost"            // 已战败（独立分支，任何状态可转入）
)

// JourneySubStage 到店子状态常量
const (
	SubStageEmpty     = ""            // 刚到店，尚未试驾/报价
	SubStageTestDrive = "test_driven" // 已试驾
	SubStageQuoted    = "quoted"      // 已报价（此子状态后才允许促单）
)

// JourneyStageOrder 旅程阶段顺序（数值越大越靠后）
// 注意：human_connected和lead_captured同级别(=2)，没有必然前后顺序
// lost独立分支，order=-1，不参与正向比较
var JourneyStageOrder = map[string]int{
	JourneyAIConnected:    1,
	JourneyHumanConnected: 2,
	JourneyLeadCaptured:   2, // 与human_connected同级
	JourneyArrived:        3,
	JourneyOrdered:        4,
	JourneyDelivered:      5,
	JourneyLost:           -1, // 独立分支
}

// JourneyStageNames 旅程阶段中文显示名
var JourneyStageNames = map[string]string{
	JourneyAIConnected:    "AI建联",
	JourneyHumanConnected: "人工建联",
	JourneyLeadCaptured:   "已留资",
	JourneyArrived:        "已到店",
	JourneyOrdered:        "已下单",
	JourneyDelivered:      "已交车",
	JourneyLost:           "已战败",
}

// HasArrived 是否已到店（用于价格管控判断）
// 到店及以后阶段（>=arrived）可以查看价格
func (c *Customer) HasArrived() bool {
	stage := c.JourneyStage
	if stage == "" {
		stage = JourneyAIConnected
	}
	currentOrder, ok := JourneyStageOrder[stage]
	if !ok {
		return false
	}
	return currentOrder >= JourneyStageOrder[JourneyArrived]
}

// CanPromote 是否允许促单（稀缺锚/代价自担锚/条件交换）
// 业务规则：只有"已到店+已报价"之后，AI才能促单
// 防止AI在到店体验阶段就推"专属优惠""限时优惠"等促单话术
func (c *Customer) CanPromote() bool {
	// 只有"已到店+已报价"才允许促单，已下单/已交车不需要促单
	return c.JourneyStage == JourneyArrived && c.JourneySubStage == SubStageQuoted
}

// IsLost 是否已战败
func (c *Customer) IsLost() bool {
	return c.JourneyStage == JourneyLost
}

// GetJourneyStageName 获取旅程阶段的中文名
func (c *Customer) GetJourneyStageName() string {
	if name, ok := JourneyStageNames[c.JourneyStage]; ok {
		return name
	}
	return c.JourneyStage
}
