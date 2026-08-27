// Package model 业务领域模型与 GORM 表结构定义（SaaS 多租户，tenant_id 隔离，写入经自动盖章回调）。
package model

import (
	"encoding/json"
	"time"
)

// ============================================================
// 卖点库模型 - 产品卖点/话术素材的原子单元
// 为什么需要卖点库？话术模板用占位符引用卖点，卖点可复用
// 动态填充机制：策略中心选好模板后，从卖点库检索匹配的feature填充进去
// ============================================================

// Feature 卖点表
type Feature struct {
	ID          string `gorm:"primaryKey;size:50" json:"id"`              // 卖点ID
	TenantID    uint   `gorm:"index;not null;default:0" json:"tenant_id"` // 租户ID（0=系统预置卖点；>0=租户私有）SaaS多租户隔离
	FeatureName string `gorm:"size:100;not null" json:"feature_name"`     // 卖点名称
	Category    string `gorm:"size:50" json:"category"`                   // 分类: 外观/性能/安全/科技/服务等
	// DescTemplate 描述模板，支持占位符
	// 如: "这款车搭载{{engine}}发动机，最大马力{{horsepower}}匹"
	DescTemplate string `gorm:"type:text;not null" json:"desc_template"`
	ShortDesc    string `gorm:"type:text" json:"short_desc"` // 简短描述
	// Params 卖点参数(JSON对象)，用于填充描述模板
	Params string `gorm:"type:text" json:"params"`
	// ApplicableTags 适用标签约束(JSON数组)
	ApplicableTags string `gorm:"type:text" json:"applicable_tags"`
	// ApplicableModels 适用车型(JSON数组)
	ApplicableModels string `gorm:"type:text" json:"applicable_models"`
	// DepartmentID 部门维度（三级包架构 2026-08-26）：NULL=租户级（行业/企业包物化）；
	// 非空=部门专属（部门包物化），仅该部门链上的顾问语境可见
	DepartmentID *uint     `gorm:"index" json:"department_id"`
	Priority     int       `gorm:"default:0" json:"priority"` // 优先级
	Status       int       `gorm:"default:1" json:"status"`   // 状态
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// TableName 指定表名
func (Feature) TableName() string {
	return "features"
}

// GetParams 获取参数字典
func (f *Feature) GetParams() map[string]string {
	if f.Params == "" {
		return map[string]string{}
	}
	var params map[string]string
	json.Unmarshal([]byte(f.Params), &params)
	return params
}

// GetApplicableTags 获取适用标签
func (f *Feature) GetApplicableTags() []string {
	return jsonStringToArray(f.ApplicableTags)
}

// GetApplicableModels 获取适用车型
func (f *Feature) GetApplicableModels() []string {
	return jsonStringToArray(f.ApplicableModels)
}

// RenderDescription 渲染卖点描述（填充参数）
func (f *Feature) RenderDescription() string {
	desc := f.DescTemplate
	params := f.GetParams()
	for key, value := range params {
		// 简单的字符串替换占位符
		// 占位符格式: {{key}}
		placeholder := "{{" + key + "}}"
		for {
			idx := -1
			// 手动查找替换（避免引入额外依赖）
			for i := 0; i <= len(desc)-len(placeholder); i++ {
				if desc[i:i+len(placeholder)] == placeholder {
					idx = i
					break
				}
			}
			if idx == -1 {
				break
			}
			desc = desc[:idx] + value + desc[idx+len(placeholder):]
		}
	}
	return desc
}
