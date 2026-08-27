// Package model 业务领域模型与 GORM 表结构定义（SaaS 多租户，tenant_id 隔离，写入经自动盖章回调）。
package model

import (
	"encoding/json"
	"time"
)

// ============================================================
// 系统配置模型 - 所有可调参数的持久化存储
// 用于后台管理页面动态调整系统参数，无需重启服务
// 四大分类：reply_speed / strategy / mental_stage / ai_chain
// 为什么用DB存而不是纯配置文件？支持热加载、管理界面编辑、历史追溯
// ============================================================

// SystemConfig 系统配置表
type SystemConfig struct {
	ID           uint      `gorm:"primaryKey" json:"id"`                                           // 主键
	TenantID     uint      `gorm:"uniqueIndex:idx_tenant_key;not null;default:0" json:"tenant_id"` // 租户ID（0=系统默认配置；>0=租户级覆盖）SaaS多租户隔离
	Category     string    `gorm:"size:50;not null;index" json:"category"`                         // 分类：reply_speed / strategy / mental_stage / ai_chain
	Key          string    `gorm:"size:100;not null;uniqueIndex:idx_tenant_key" json:"key"`        // 参数键名（与tenant_id联合唯一：同租户内唯一，不同租户可同名）
	Value        string    `gorm:"type:text;not null" json:"value"`                                // 参数值（JSON字符串，支持数字/布尔/数组/对象）
	ValueType    string    `gorm:"size:20;not null;default:number" json:"value_type"`              // 值类型：number / bool / string / json
	Description  string    `gorm:"type:text;not null;default:''" json:"description"`               // 参数说明（中文）
	DefaultValue string    `gorm:"type:text;not null;default:''" json:"default_value"`             // 默认值（JSON字符串）
	SortOrder    int       `gorm:"not null;default:0" json:"sort_order"`                           // 同分类内排序（越小越靠前）
	CreatedAt    time.Time `json:"created_at"`                                                     // 创建时间
	UpdatedAt    time.Time `json:"updated_at"`                                                     // 更新时间
}

// TableName 指定表名
func (SystemConfig) TableName() string {
	return "system_configs"
}

// GetFloatValue 获取float64类型的配置值
// 适用于 value_type=number 的配置项
func (c *SystemConfig) GetFloatValue() float64 {
	var val float64
	if err := json.Unmarshal([]byte(c.Value), &val); err == nil {
		return val
	}
	return 0
}

// GetIntValue 获取int类型的配置值
// 适用于 value_type=number 的整数配置项
func (c *SystemConfig) GetIntValue() int {
	// 先尝试直接解析为int
	var intVal int
	if err := json.Unmarshal([]byte(c.Value), &intVal); err == nil {
		return intVal
	}
	// 再尝试float转int（JSON数字默认解析为float64）
	var floatVal float64
	if err := json.Unmarshal([]byte(c.Value), &floatVal); err == nil {
		return int(floatVal)
	}
	return 0
}

// GetBoolValue 获取bool类型的配置值
// 适用于 value_type=bool 的配置项
func (c *SystemConfig) GetBoolValue() bool {
	var val bool
	if err := json.Unmarshal([]byte(c.Value), &val); err == nil {
		return val
	}
	return false
}

// GetStringValue 获取string类型的配置值
// 适用于 value_type=string 的配置项
func (c *SystemConfig) GetStringValue() string {
	var val string
	if err := json.Unmarshal([]byte(c.Value), &val); err == nil {
		return val
	}
	return c.Value // 兜底直接返回原始字符串
}

// GetJSONValue 获取JSON数组/对象类型的配置值
// 适用于 value_type=json 的配置项，返回原始JSON字符串
func (c *SystemConfig) GetJSONValue() string {
	// 验证是否是合法JSON
	var js json.RawMessage
	if json.Unmarshal([]byte(c.Value), &js) == nil {
		return c.Value
	}
	return c.Value // 不是合法JSON也返回原始值
}

// GetIntSliceValue 获取int数组类型的配置值
// 适用于 value_type=json 且值为int数组的配置项（如延迟区间 [3,8]）
func (c *SystemConfig) GetIntSliceValue() []int {
	var val []int
	if err := json.Unmarshal([]byte(c.Value), &val); err == nil {
		return val
	}
	// 尝试float64数组再转int（JSON数字默认解析为float64）
	var floatVal []float64
	if err := json.Unmarshal([]byte(c.Value), &floatVal); err == nil {
		result := make([]int, len(floatVal))
		for i, v := range floatVal {
			result[i] = int(v)
		}
		return result
	}
	return []int{}
}

// GetStringSliceValue 获取string数组类型的配置值
// 适用于 value_type=json 且值为string数组的配置项（如模型优先级列表）
func (c *SystemConfig) GetStringSliceValue() []string {
	var val []string
	if err := json.Unmarshal([]byte(c.Value), &val); err == nil {
		return val
	}
	return []string{}
}
