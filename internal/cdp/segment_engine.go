package cdp

import (
	"ai-scrm/internal/model"
)

// SegmentEngine 分群引擎（SAAS_PLAN §16.4）
// 分群=查询或常驻计算结果，不落静态标签；派生结论（LTV/AIPL）在此层计算
type SegmentEngine interface {
	SegmentByTag(tagCode string) ([]string, error) // 按原子标签圈选 OneID 列表
}

type segmentEngine struct{}

func NewSegmentEngine() SegmentEngine { return &segmentEngine{} }

// SegmentByTag 圈选持有指定标签的全部 OneID
func (s *segmentEngine) SegmentByTag(tagCode string) ([]string, error) {
	var oneIDs []string
	err := gdb().Table("cdp_tag_assignments a").
		Joins("LEFT JOIN cdp_profiles p ON a.cdp_profile_id = p.id").
		Joins("LEFT JOIN cdp_tag_definitions df ON a.definition_id = df.id").
		Where("df.code = ?", tagCode).
		Distinct("p.cdp_id").Pluck("cdp_id", &oneIDs).Error
	return oneIDs, err
}

var _ = model.CdpProfile{}
