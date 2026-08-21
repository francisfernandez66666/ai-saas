package model

import (
	"encoding/json"
	"time"
)

// ============================================================
// 标签模型 - 客户标签体系
// 为什么需要标签？策略中心用标签做话术匹配和人群细分
// ============================================================

// Tag 标签表
type Tag struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	TenantID    uint      `gorm:"index;not null;default:0" json:"tenant_id"` // 租户ID（0=系统预置，全租户可见；>0=租户私有）SaaS多租户隔离
	Name        string    `gorm:"size:50;uniqueIndex;not null" json:"name"`  // 标签名称
	Code        string    `gorm:"size:50;uniqueIndex" json:"code"`           // 标签编码
	Category    string    `gorm:"size:30" json:"category"`                   // 分类: 意向/属性/行为/来源等
	Weight      float64   `gorm:"default:1.0" json:"weight"`                 // 标签权重
	Description string    `gorm:"size:255" json:"description"`               // 描述
	Status      int       `gorm:"default:1" json:"status"`                   // 状态
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TableName 指定表名
func (Tag) TableName() string {
	return "tags"
}

// CustomerTag 客户标签关联表
type CustomerTag struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	TenantID   uint      `gorm:"index;not null;default:0" json:"tenant_id"`                // 租户ID（SaaS多租户隔离，防跨租户打标）
	CustomerID uint      `gorm:"uniqueIndex:idx_customer_tag;not null" json:"customer_id"` // 客户ID（联合唯一索引）
	TagID      uint      `gorm:"uniqueIndex:idx_customer_tag;not null" json:"tag_id"`      // 标签ID（联合唯一索引）
	TagName    string    `gorm:"size:50" json:"tag_name"`                                  // 标签名称（冗余，方便查询）
	Source     string    `gorm:"size:30" json:"source"`                                    // 标签来源: manual/auto/ai
	Weight     float64   `gorm:"default:1.0" json:"weight"`                                // 标签权重（单客户维度，默认1.0）
	ExpireAt   time.Time `json:"expire_at"`                                                // 过期时间（可选，零值表示永不过期）
	CreatedAt  time.Time `json:"created_at"`
}

// TableName 指定表名
func (CustomerTag) TableName() string {
	return "customer_tags"
}

// ============================================================
// TagRule 自动打标规则表
// 为什么需要规则？从客户对话文本中自动识别关键词，自动打上对应标签
// 三种规则类型：
//   - keyword: 关键词匹配，命中任何一个关键词就打标
//   - intent:  意图识别，匹配特定意图表达
//   - resistance: 抗性识别，匹配客户抗性表达
// ============================================================

// TagRule 自动打标规则表
type TagRule struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	TenantID      uint      `gorm:"index;not null;default:0" json:"tenant_id"` // 租户ID（SaaS多租户隔离）
	Name          string    `gorm:"size:100;not null" json:"name"`             // 规则名称
	RuleType      string    `gorm:"size:30;index;not null" json:"rule_type"`   // 规则类型: keyword/intent/resistance
	MatchPattern  string    `gorm:"type:text" json:"match_pattern"`            // 匹配模式（JSON数组关键词）
	TargetTagID   uint      `gorm:"index;not null" json:"target_tag_id"`       // 命中后打哪个标签
	TargetTagName string    `gorm:"size:50" json:"target_tag_name"`            // 目标标签名（冗余）
	WeightBonus   float64   `gorm:"default:1.0" json:"weight_bonus"`           // 命中后给T向量加多少权重
	Status        int       `gorm:"default:1" json:"status"`                   // 状态: 1启用 0禁用
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// TableName 指定表名
func (TagRule) TableName() string {
	return "tag_rules"
}

// GetMatchPatterns 获取匹配关键词数组
// MatchPattern存储的是JSON数组，如["太贵", "价格高", "超出预算"]
func (r *TagRule) GetMatchPatterns() []string {
	return jsonStringToArray(r.MatchPattern)
}

// SetMatchPatterns 设置匹配关键词数组
func (r *TagRule) SetMatchPatterns(patterns []string) {
	data, _ := json.Marshal(patterns)
	r.MatchPattern = string(data)
}

// ============================================================
// TagWeightMapping 标签→T向量权重映射表
// 打标驱动策略的核心：客户打上某个标签后，T向量对应维度的权重会变化
// 比如：打上"价格敏感"标签 → T[1]（价格敏感度）权重+0.2
// 为什么需要这个？标签是业务概念，T向量是策略引擎的数值输入，需要桥接
// ============================================================

// TagWeightMapping 标签→T向量权重映射表
type TagWeightMapping struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	TenantID     uint      `gorm:"index;not null;default:0" json:"tenant_id"` // 租户ID（SaaS多租户隔离）
	TagID        uint      `gorm:"index;not null" json:"tag_id"`              // 标签ID
	TagCode      string    `gorm:"size:50;index" json:"tag_code"`             // 标签编码（冗余）
	TVectorIndex int       `gorm:"not null" json:"t_vector_index"`            // 对应T向量第几维(0-31)
	WeightDelta  float64   `gorm:"default:0.1" json:"weight_delta"`           // 权重变化量
	Direction    string    `gorm:"size:10;default:up" json:"direction"`       // 变化方向: up(增加)/down(减少)
	Status       int       `gorm:"default:1;index" json:"status"`             // 状态: 1启用 0禁用(软删除)
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// TableName 指定表名
func (TagWeightMapping) TableName() string {
	return "tag_weight_mappings"
}

// ApplyToVector 把权重变化应用到T向量上
// 返回变化后的值，做clamp防止越界(0-1)
func (m *TagWeightMapping) ApplyToVector(current float64) float64 {
	delta := m.WeightDelta
	if m.Direction == "down" {
		delta = -delta
	}
	result := current + delta
	// clamp到0-1范围
	if result < 0 {
		result = 0
	}
	if result > 1 {
		result = 1
	}
	return result
}
