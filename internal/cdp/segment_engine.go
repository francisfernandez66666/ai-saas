package cdp

import (
	"log"
)

// SegmentEngine 客户细分引擎
type SegmentEngine interface {
	SegmentByIntent() (map[string][]uint, error)
	SegmentByValue() (map[string][]uint, error)
}

// NewSegmentEngine 创建细分引擎
func NewSegmentEngine() SegmentEngine {
	return &segmentEngine{}
}

type segmentEngine struct{}

// SegmentByIntent 按意向分分段
func (s *segmentEngine) SegmentByIntent() (map[string][]uint, error) {
	log.Println("[CDP] Segment by intent")
	return make(map[string][]uint), nil
}

// SegmentByValue 按价值分段
func (s *segmentEngine) SegmentByValue() (map[string][]uint, error) {
	log.Println("[CDP] Segment by value")
	return make(map[string][]uint), nil
}