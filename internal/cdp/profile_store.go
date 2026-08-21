package cdp

import (
	"ai-scrm/internal/model"
	"log"
)

// ProfileStore 客户画像持久化层
type ProfileStore interface {
	Create(*model.CdpProfile) error
	GetByCustomerID(uint) (*model.CdpProfile, error)
	Update(*model.CdpProfile) error
}

// NewProfileStore 创建画像持久化层
func NewProfileStore() ProfileStore {
	return &profileStore{}
}

type profileStore struct{}

// Create 创建客户画像
func (s *profileStore) Create(pro *model.CdpProfile) error {
	log.Printf("[CDP] Create profile customer=%d", pro.CustomerID)
	return nil
}

// GetByCustomerID 获取客户画像
func (s *profileStore) GetByCustomerID(customerID uint) (*model.CdpProfile, error) {
	log.Printf("[CDP] Get profile customer=%d", customerID)
	return nil, nil
}

// Update 更新客户画像
func (s *profileStore) Update(pro *model.CdpProfile) error {
	log.Printf("[CDP] Update profile customer=%d", pro.CustomerID)
	return nil
}
