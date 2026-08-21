package model

import (
	"encoding/json"
	"time"
)

// ============================================================
// 话术模板模型 - 策略中心抛锚/钩话术的素材库
// 每条话术 = 锚类型 + 触发条件 + 抛话术 + 钩话术 + 采集字段
// 为什么叫模板？因为支持占位符动态填充卖点
// ============================================================

// AnchorType 锚类型枚举值
const (
	AnchorTypeNoThrow     = 0 // 不抛
	AnchorTypeSameKind    = 1 // 同类锚
	AnchorTypeScene       = 1 // 场景锚（aggressiveness与同类锚相同=1）
	AnchorTypeDisassemble = 2 // 拆解锚
	AnchorTypeCompare     = 3 // 对比锚
	AnchorTypeLoss        = 4 // 损失锚
	AnchorTypeScarcity    = 5 // 稀缺锚
	AnchorTypeSelfPay     = 6 // 代价自担锚
)

// AnchorTypeName 锚类型名称映射
var AnchorTypeName = map[int]string{
	0: "不抛",
	1: "同类/场景锚",
	2: "拆解锚",
	3: "对比锚",
	4: "损失锚",
	5: "稀缺锚",
	6: "代价自担锚",
}

// Template 话术模板表
type Template struct {
	ID         string `gorm:"primaryKey;size:50" json:"id"`              // 模板ID，字符串类型便于语义化命名
	TenantID   uint   `gorm:"index;not null;default:0" json:"tenant_id"` // 租户ID（0=系统预置话术；>0=租户私有）SaaS多租户隔离
	AnchorType int    `gorm:"index;not null" json:"anchor_type"`         // 锚类型(0-6)
	SubType    string `gorm:"size:20" json:"sub_type"`                   // 子类型: spec/service （对比锚用）
	Name       string `gorm:"size:100" json:"name"`                      // 模板名称
	Category   string `gorm:"size:50" json:"category"`                   // 分类

	// ---- 触发条件 ----
	TriggerTags      string  `gorm:"type:text" json:"trigger_tags"`      // 触发标签条件(JSON数组，满足任一即触发)
	RequiredTags     string  `gorm:"type:text" json:"required_tags"`     // 必须满足的标签(JSON数组，全部满足才触发)
	MinIntent        float64 `json:"min_intent"`                         // 最低意向分要求
	MaxIntent        float64 `json:"max_intent"`                         // 最高意向分要求
	ApplicableModels string  `gorm:"type:text" json:"applicable_models"` // 适用车型(JSON数组)

	// ---- 话术内容 ----
	// PromptTemplate 抛话术模板，支持占位符
	// 占位符格式: {{feature_name}} {{customer_name}} {{model}} {{price}} 等
	PromptTemplate string `gorm:"type:text;not null" json:"prompt_template"`
	// HookTemplate 钩话术模板（AI接话后用钩子引导客户回复）
	HookTemplate string `gorm:"type:text" json:"hook_template"`
	// HookFields 钩话术需要采集的字段(JSON数组)
	HookFields string `gorm:"type:text" json:"hook_fields"`
	// RequiredFeatures 需要的卖点feature_id列表(JSON数组)，用于动态填充
	RequiredFeatures string `gorm:"type:text" json:"required_features"`

	// ---- 元数据 ----
	Priority   int       `gorm:"default:0" json:"priority"`    // 优先级，越大越优先
	UsageCount int       `gorm:"default:0" json:"usage_count"` // 使用次数
	Status     int       `gorm:"default:1" json:"status"`      // 状态: 1-启用 0-禁用
	Version    string    `gorm:"size:20" json:"version"`       // 版本号
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// TableName 指定表名
func (Template) TableName() string {
	return "templates"
}

// GetTriggerTags 获取触发标签列表
func (t *Template) GetTriggerTags() []string {
	return jsonStringToArray(t.TriggerTags)
}

// GetRequiredTags 获取必须标签列表
func (t *Template) GetRequiredTags() []string {
	return jsonStringToArray(t.RequiredTags)
}

// GetApplicableModels 获取适用车型列表
func (t *Template) GetApplicableModels() []string {
	return jsonStringToArray(t.ApplicableModels)
}

// GetHookFields 获取钩采集字段列表
func (t *Template) GetHookFields() []string {
	return jsonStringToArray(t.HookFields)
}

// GetRequiredFeatures 获取需要的卖点ID列表
func (t *Template) GetRequiredFeatures() []string {
	return jsonStringToArray(t.RequiredFeatures)
}

// jsonStringToArray JSON字符串转数组
func jsonStringToArray(s string) []string {
	if s == "" {
		return []string{}
	}
	var arr []string
	json.Unmarshal([]byte(s), &arr)
	return arr
}
