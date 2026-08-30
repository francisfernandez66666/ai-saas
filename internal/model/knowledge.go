// Package model 业务领域模型与 GORM 表结构定义（SaaS 多租户，tenant_id 隔离，写入经自动盖章回调）。
package model

import (
	"encoding/json"
	"time"
)

// ============================================================
// 车型品牌知识库模型
// 为什么需要知识库？策略引擎选好话术方向后，需要从知识库注入具体的产品知识
// 让AI说的话有根有据，而不是凭空编造
//
// 知识层级：品牌 → 车型 → 规格参数 / 竞品对比 / 知识片段
// ============================================================

// ============================================================
// Brand 品牌表
// 最顶层的知识分类，一个品牌下有多款车型
// ============================================================

// Brand 品牌表
type Brand struct {
	ID          uint      `gorm:"primaryKey" json:"id"` // 注释：主键ID
	TenantID    uint      `gorm:"not null;default:0;uniqueIndex:idx_brand_tenant_name;uniqueIndex:idx_brand_tenant_code" json:"tenant_id"` // 租户ID（0=系统预置品牌；>0=租户私有）SaaS多租户隔离（J12-2026-08-26 复合唯一）
	Name        string    `gorm:"size:50;uniqueIndex:idx_brand_tenant_name;not null" json:"name"`                                          // 品牌名称（租户级唯一）
	Code        string    `gorm:"size:30;uniqueIndex:idx_brand_tenant_code" json:"code"`                                                   // 品牌编码（租户级唯一）
	Logo        string    `gorm:"size:255" json:"logo"`                                                                                    // 品牌Logo URL
	Description string    `gorm:"type:text" json:"description"`                                                                            // 品牌描述
	Country     string    `gorm:"size:30" json:"country"`                                                                                  // 品牌所属国家
	FoundedYear int       `json:"founded_year"`                                                                                            // 创立年份
	Status      int       `gorm:"default:1;index" json:"status"`                                                                           // 状态: 1启用 0禁用
	Sort        int       `gorm:"default:0" json:"sort"`                                                                                   // 排序（数字越小越靠前）
	CreatedAt   time.Time `json:"created_at"` // 注释：创建时间
	UpdatedAt   time.Time `json:"updated_at"` // 注释：更新时间
}

// TableName 指定表名
func (Brand) TableName() string {
	return "brands"
}

// ============================================================
// CarModel 车型表
// 知识库的核心实体，每款车型有自己的规格参数和竞品对比
// ============================================================

// CarModel 车型表
type CarModel struct {
	ID         uint      `gorm:"primaryKey" json:"id"` // 注释：主键ID
	TenantID   uint      `gorm:"not null;default:0;uniqueIndex:idx_carmodel_tenant_code" json:"tenant_id"` // 租户ID（0=系统预置车型；>0=租户私有）SaaS多租户隔离（J12-2026-08-26 复合唯一）
	BrandID    uint      `gorm:"index;not null" json:"brand_id"`                                           // 所属品牌ID
	BrandName  string    `gorm:"size:50" json:"brand_name"`                                                // 品牌名称（冗余）
	Name       string    `gorm:"size:100;not null" json:"name"`                                            // 车型名称
	Code       string    `gorm:"size:50;uniqueIndex:idx_carmodel_tenant_code" json:"code"`                 // 车型编码（租户级唯一）
	PriceRange string    `gorm:"size:50" json:"price_range"`                                               // 价格区间，如"25-35万"
	Level      string    `gorm:"size:20" json:"level"`                                                     // 级别: 紧凑/中型/中大型/全尺寸
	BodyType   string    `gorm:"size:20" json:"body_type"`                                                 // 车身类型: SUV/轿车/皮卡/MPV
	FuelType   string    `gorm:"size:20" json:"fuel_type"`                                                 // 燃油类型: 燃油/混动/纯电
	Status     int       `gorm:"default:1;index" json:"status"`                                            // 状态: 1启用 0禁用
	Sort       int       `gorm:"default:0" json:"sort"`                                                    // 排序
	CreatedAt  time.Time `json:"created_at"` // 注释：创建时间
	UpdatedAt  time.Time `json:"updated_at"` // 注释：更新时间
}

// TableName 指定表名
func (CarModel) TableName() string {
	return "car_models"
}

// ============================================================
// ModelSpec 车型规格参数表
// 车型的核心参数，用于对比锚话术的"事实依据"
// 按分类组织：动力/底盘/安全/尺寸/配置
// ============================================================

// ModelSpec 车型规格参数表
type ModelSpec struct {
	ID         uint      `gorm:"primaryKey" json:"id"` // 注释：主键ID
	TenantID   uint      `gorm:"index;not null;default:0" json:"tenant_id"` // 租户ID（SaaS多租户隔离）
	ModelID    uint      `gorm:"index;not null" json:"model_id"`            // 车型ID
	ParamName  string    `gorm:"size:50;not null" json:"param_name"`        // 参数名称
	ParamValue string    `gorm:"size:100" json:"param_value"`               // 参数值
	ParamUnit  string    `gorm:"size:20" json:"param_unit"`                 // 参数单位
	Category   string    `gorm:"size:20;index" json:"category"`             // 分类: 动力/底盘/安全/尺寸/配置
	IsPrice    bool      `gorm:"default:false" json:"is_price"`             // 是否价格类参数（用于价格管控过滤）
	Status     int       `gorm:"default:1;index" json:"status"`             // 状态: 1启用 0禁用
	Sort       int       `gorm:"default:0" json:"sort"`                     // 排序
	CreatedAt  time.Time `json:"created_at"` // 注释：创建时间
}

// TableName 指定表名
func (ModelSpec) TableName() string {
	return "model_specs"
}

// ============================================================
// CompetitorCompare 竞品对比表
// 对比锚话术的核心素材：我方车型 vs 竞品车型的详细对比
// Content是JSON数组，每条是一个对比维度
// ============================================================

// CompareItem 单条对比项
type CompareItem struct {
	Aspect     string `json:"aspect"`      // 对比维度，如"动力"、"空间"
	OurValue   string `json:"our_value"`   // 我方值
	TheirValue string `json:"their_value"` // 对方值
	Conclusion string `json:"conclusion"`  // 结论话术，如"我们动力更强"
}

// CompetitorCompare 竞品对比表
type CompetitorCompare struct {
	ID              uint      `gorm:"primaryKey" json:"id"` // 注释：主键ID
	TenantID        uint      `gorm:"index;not null;default:0" json:"tenant_id"` // 租户ID（SaaS多租户隔离）
	OurModelID      uint      `gorm:"index;not null" json:"our_model_id"`        // 我方车型ID
	CompetitorBrand string    `gorm:"size:50;index" json:"competitor_brand"`     // 竞品品牌
	CompetitorModel string    `gorm:"size:100" json:"competitor_model"`          // 竞品车型名称
	CompareType     string    `gorm:"size:20" json:"compare_type"`               // 对比类型: 优势/劣势/持平
	Content         string    `gorm:"type:text" json:"content"`                  // 对比内容（JSON数组 CompareItem）
	ContainsPrice   bool      `gorm:"default:false" json:"contains_price"`       // 是否含价格信息（用于价格管控过滤）
	Status          int       `gorm:"default:1;index" json:"status"`             // 状态: 1启用 0禁用
	CreatedAt       time.Time `json:"created_at"` // 注释：创建时间
	UpdatedAt       time.Time `json:"updated_at"` // 注释：更新时间
}

// TableName 指定表名
func (CompetitorCompare) TableName() string {
	return "competitor_compares"
}

// GetCompareItems 获取对比项列表
func (c *CompetitorCompare) GetCompareItems() []CompareItem {
	if c.Content == "" {
		return []CompareItem{}
	}
	var items []CompareItem
	if err := json.Unmarshal([]byte(c.Content), &items); err == nil {
		return items
	}
	return []CompareItem{}
}

// SetCompareItems 设置对比项列表
func (c *CompetitorCompare) SetCompareItems(items []CompareItem) {
	data, _ := json.Marshal(items)
	c.Content = string(data)
}

// ============================================================
// KnowledgeFragment 知识片段表
// 灵活的知识库条目，可以是品牌故事、技术原理、服务政策、活动信息等
// 用标签和适用车型做关联，方便检索
// ============================================================

// KnowledgeFragment 知识片段表
type KnowledgeFragment struct {
	ID               uint      `gorm:"primaryKey" json:"id"` // 注释：主键ID
	TenantID         uint      `gorm:"index;not null;default:0" json:"tenant_id"` // 租户ID（SaaS多租户隔离）
	Category         string    `gorm:"size:20;index" json:"category"`             // 分类: 品牌/车型/技术/服务/活动
	Title            string    `gorm:"size:200;not null" json:"title"`            // 标题
	Content          string    `gorm:"type:text" json:"content"`                  // 内容（可含富文本）
	Tags             string    `gorm:"type:text" json:"tags"`                     // 标签（JSON数组）
	ApplicableModels string    `gorm:"type:text" json:"applicable_models"`        // 适用车型（JSON数组）
	Status           int       `gorm:"default:1;index" json:"status"`             // 状态: 1启用 0禁用
	Sort             int       `gorm:"default:0" json:"sort"`                     // 排序
	EmbeddingJSON    string    `gorm:"type:text" json:"embedding_json"`         // 向量检索：内容+标题embedding（JSON数组，PG无pgvector时按内存余弦混合）
	CreatedAt        time.Time `json:"created_at"` // 注释：创建时间
	UpdatedAt        time.Time `json:"updated_at"` // 注释：更新时间
}

// TableName 指定表名
func (KnowledgeFragment) TableName() string {
	return "knowledge_fragments"
}

// GetTags 获取标签数组
func (k *KnowledgeFragment) GetTags() []string {
	return jsonStringToArray(k.Tags)
}

// SetTags 设置标签数组
func (k *KnowledgeFragment) SetTags(tags []string) {
	data, _ := json.Marshal(tags)
	k.Tags = string(data)
}

// GetApplicableModels 获取适用车型数组
func (k *KnowledgeFragment) GetApplicableModels() []string {
	return jsonStringToArray(k.ApplicableModels)
}

// SetApplicableModels 设置适用车型数组
func (k *KnowledgeFragment) SetApplicableModels(models []string) {
	data, _ := json.Marshal(models)
	k.ApplicableModels = string(data)
}
