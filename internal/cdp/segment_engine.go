// Package cdp CDP 数据底座：OneID 身份归并、画像/标签/事件写收口与分群引擎（四维标签体系）。
package cdp

import (
	"ai-scrm/internal/model"
)

// SegmentEngine 分群引擎（SAAS_PLAN §16.4）
// 分群=查询或常驻计算结果，不落静态标签；派生结论（LTV/AIPL）在此层计算
type SegmentEngine interface {
	SegmentByTag(tenantID uint, tagCode string) ([]string, error) // 按原子标签圈选 OneID 列表（租户隔离）
}

// segmentEngine 分群引擎默认实现（CDP 写收口之上的只读查询层）
type segmentEngine struct{}

// NewSegmentEngine 构造分群引擎默认实现（CDP 写收口之上的只读查询层入口）
func NewSegmentEngine() SegmentEngine { return &segmentEngine{} }

// SegmentByTag 圈选持有指定标签的全部 OneID
// 修复（2026-08-22）：补租户过滤——原实现无 tenant_id 条件，分群会跨租户串数据
func (s *segmentEngine) SegmentByTag(tenantID uint, tagCode string) ([]string, error) {
	var oneIDs []string
	err := gdb().Table("cdp_tag_assignments a").
		Joins("LEFT JOIN cdp_profiles p ON a.cdp_profile_id = p.id").
		Joins("LEFT JOIN cdp_tag_definitions df ON a.definition_id = df.id").
		Where("df.code = ? AND p.tenant_id = ?", tagCode, tenantID).
		Distinct("p.cdp_id").Pluck("cdp_id", &oneIDs).Error
	return oneIDs, err
}

var _ = model.CdpProfile{}
