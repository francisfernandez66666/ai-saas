// 测试辅助：允许跨包构造内存配置桩，避免外部直接访问未导出字段。
package runtimecfg

import "ai-scrm/internal/model"

// NewStaticService 构造纯内存配置服务（不查 DB，不执行 ensureDefaults）。
// system 为系统层 tenant_id=0 配置；tenants 为租户覆盖层。
func NewStaticService(system map[string]string, tenants map[uint]map[string]string) *SystemConfigService {
	if system == nil {
		system = map[string]string{}
	}
	if tenants == nil {
		tenants = map[uint]map[string]string{}
	}
	return &SystemConfigService{
		cache:       system,
		tenantCache: tenants,
		configs:     make([]model.SystemConfig, 0),
	}
}

// SetDefaultForTest 替换全局配置单例，并返回恢复函数（用于 defer restore()）。
func SetDefaultForTest(s *SystemConfigService) func() {
	old := DefaultSystemConfigService
	DefaultSystemConfigService = s
	return func() { DefaultSystemConfigService = old }
}
