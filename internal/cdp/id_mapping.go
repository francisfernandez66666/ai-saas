package cdp

import (
	"log"
)

// IdMappingService 内部ID ↔ CDP实体ID映射服务
type IdMappingService interface {
	MapInternalToCDP(uint, string, string) error
	GetCDPID(uint, string) (string, error)
	UnmapInternal(uint, string) error
}

// NewIdMappingService 创建ID映射服务
func NewIdMappingService() IdMappingService {
	return &idMappingService{}
}

type idMappingService struct{}

// MapInternalToCDP 映射内部ID到CDP实体
func (s *idMappingService) MapInternalToCDP(internalID uint, entityType string, cdpEntityID string) error {
	log.Printf("[CDP] Map internal=%d to %s", internalID, entityType)
	return nil
}

// GetCDPID 获取CDP实体ID
func (s *idMappingService) GetCDPID(internalID uint, entityType string) (string, error) {
	log.Printf("[CDP] Get CDPID internal=%d type=%s", internalID, entityType)
	return "", nil
}

// UnmapInternal 解除映射
func (s *idMappingService) UnmapInternal(internalID uint, entityType string) error {
	log.Printf("[CDP] Unmap internal=%d type=%s", internalID, entityType)
	return nil
}
