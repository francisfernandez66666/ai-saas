package cdp

import (
	"ai-scrm/internal/model"
	"log"
)

// TagEngine 标签引擎
type TagEngine interface {
	AutoAssignTags(uint) error
	GetCustomerTags(uint) ([]model.CdpTagAssignment, error)
}

// NewTagEngine 创建标签引擎
func NewTagEngine() TagEngine {
	return &tagEngine{}
}

type tagEngine struct{}

// AutoAssignTags 自动分配标签
func (e *tagEngine) AutoAssignTags(customerID uint) error {
	log.Printf("[CDP] Auto assign tags customer=%d", customerID)
	return nil
}

// GetCustomerTags 获取客户标签
func (e *tagEngine) GetCustomerTags(customerID uint) ([]model.CdpTagAssignment, error) {
	log.Printf("[CDP] Get tags customer=%d", customerID)
	return nil, nil
}
