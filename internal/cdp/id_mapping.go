// Package cdp CDP 数据底座：OneID 身份归并、画像/标签/事件写收口与分群引擎（四维标签体系）。
package cdp

// ============================================================
// ID-Mapping（One ID 身份解析）
// 实现位于 profile_store.go（UpsertAnchor：锚点值→OneID 归并）
// 本文件保留包内语义入口，供后续扩展关系链（推荐人/被荐人）使用
// ============================================================

import (
	"fmt"

	"ai-scrm/internal/model"
)

// ResolveOneID 解析客户的 canonical OneID（Phase B，2026-08-22）
//
// 规则：
//  1. 客户已有手机号 → 查 id_mapping 的 phone 锚点 → 返回归并后的 canonical OneID
//     （OneID 合并后老客户为 canonical，访客锚点已重指向）
//  2. 无手机号/无映射 → 回落 "c:{customerID}" 占位键（与事件发布键一致）
//
// 调用方：流程引擎状态表分片键、user_event 发布方；禁止用于鉴权语义
func ResolveOneID(tenantID uint, customerID uint) string {
	var phone string
	err := gdb().Model(&model.Customer{}).
		Select("phone").
		Where("tenant_id = ? AND id = ? AND phone <> ''", tenantID, customerID).
		Take(&phone).Error
	if err == nil && phone != "" {
		var m model.IdMapping
		if e2 := gdb().Where("tenant_id = ? AND internal_type = ?",
			tenantID, "anchor:phone:"+phone).First(&m).Error; e2 == nil && m.CdpEntityId != "" {
			return m.CdpEntityId
		}
	}
	return fmt.Sprintf("c:%d", customerID)
}
