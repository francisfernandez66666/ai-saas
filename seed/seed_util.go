// Package seed：seed 模块（自动补包注释）。
package seed

import (
	"ai-scrm/internal/model"
	"encoding/json"
)

// ============================================================
// 辅助函数
// ============================================================

// jsonArr 将字符串切片序列化为 JSON 字符串，用于写入模型的 []string 类型字段（如 TriggerTags/ApplicableModels）
func jsonArr(arr []string) string {
	data, _ := json.Marshal(arr)
	return string(data)
}

// jsonParams 将键值对 map 序列化为 JSON 字符串，用于写入卖点/知识的 Params 等结构化字段
func jsonParams(params map[string]string) string {
	data, _ := json.Marshal(params)
	return string(data)
}

// jsonCompareItems 对比项数组转JSON字符串
func jsonCompareItems(items []model.CompareItem) string {
	data, _ := json.Marshal(items)
	return string(data)
}
