// 系统配置中心：CRUD + 内存热加载、租户覆盖层、平台级键隔离。
package runtimecfg

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"encoding/json"
	"log"
	"math/rand"
	"strconv"
	"sync"
)

// ============================================================
// 系统配置服务层 - CRUD + 内存热加载
// 核心设计：
//   1. 服务启动时从DB加载所有配置到内存（sync.RWMutex保护）
//   2. DB为空时用默认值写入DB
//   3. Admin API 更新DB后，自动重新加载内存缓存
//   4. 所有代码读配置走本服务的GetXXX方法，不直接读config.GlobalConfig
// 为什么要有内存缓存？避免每次读配置都查DB，提升性能
// ============================================================

// SystemConfigService 系统配置服务
type SystemConfigService struct {
	mu          sync.RWMutex               // 读写锁，保护内存缓存
	cache       map[string]string          // key→value（仅 tenant_id=0 系统默认，引擎/全局语义）
	tenantCache map[uint]map[string]string // 租户覆盖层：tid→key→value（P2 租户化）
	configs     []model.SystemConfig       // 完整配置列表（保留分类、描述等元信息）
}

// DefaultSystemConfigService 默认系统配置服务实例（全局单例）
var DefaultSystemConfigService *SystemConfigService

// InitSystemConfigService 初始化系统配置服务
// 启动时调用：1.确保DB有默认数据 2.加载到内存缓存
func InitSystemConfigService() {
	DefaultSystemConfigService = &SystemConfigService{
		cache:       make(map[string]string),
		tenantCache: make(map[uint]map[string]string),
		configs:     make([]model.SystemConfig, 0),
	}

	// 确保DB中有默认数据
	DefaultSystemConfigService.ensureDefaults()

	// 从DB加载到内存
	DefaultSystemConfigService.Reload()

	log.Println("[系统配置] 服务初始化完成，已加载配置项到内存")
}

// ensureDefaults 确保DB中有默认配置数据
// 如果DB为空，用 DefaultConfigs 写入初始数据
// 设计为幂等：已有数据不覆盖，缺失的补上
// 修复：用事务+强制覆盖写入，确保配置一定存在（解决用户反复反馈"后台没有配置项"的问题）
func (s *SystemConfigService) ensureDefaults() {
	// 清理已停用键的存量数据（2026-09-09）：旧版本曾写入的死配置键不再展示
	retiredDeleted := 0
	if len(retiredConfigKeys) > 0 {
		var keys []string
		for k := range retiredConfigKeys {
			keys = append(keys, k)
		}
		res := db.DB.Where("tenant_id = 0 AND \"key\" IN ?", keys).Delete(&model.SystemConfig{})
		if res.Error == nil && res.RowsAffected > 0 {
			retiredDeleted = int(res.RowsAffected)
		}
	}

	inserted := 0
	for _, cfg := range DefaultConfigs {
		// 按 key 查是否已存在
		// 修复（2026-08-23）：必须限定 tenant_id=0（系统层）——只按 key 判断会因
		// 租户覆盖行存在而误跳过，导致系统默认层缺失、全局读值静默回落代码默认
		var existing model.SystemConfig
		result := db.DB.Where("tenant_id = 0 AND \"key\" = ?", cfg.Key).First(&existing)
		if result.Error != nil {
			// 不存在，插入默认值
			if err := db.DB.Create(&cfg).Error; err != nil {
				log.Printf("[系统配置] 插入默认配置失败 key=%s: %v", cfg.Key, err)
			} else {
				inserted++
			}
		}
	}

	// 修复：插入后校验总量，防止DB有脏状态导致0条数据
	var totalCount int64
	db.DB.Model(&model.SystemConfig{}).Count(&totalCount)
	if retiredDeleted > 0 {
		log.Printf("[系统配置] 已清理 %d 个停用配置键存量数据", retiredDeleted)
	}
	if totalCount == 0 && len(DefaultConfigs) > 0 {
		log.Println("[系统配置] ⚠️ DB中0条配置数据，强制批量插入默认配置")
		// 用事务确保原子性
		tx := db.DB.Begin()
		if err := tx.Create(&DefaultConfigs).Error; err != nil {
			tx.Rollback()
			log.Printf("[系统配置] 批量插入默认配置失败: %v", err)
		} else {
			tx.Commit()
			log.Printf("[系统配置] 已强制插入 %d 条默认配置", len(DefaultConfigs))
		}
	} else if inserted > 0 {
		log.Printf("[系统配置] 已补充插入 %d 条缺失配置", inserted)
	} else {
		log.Printf("[系统配置] DB中已有 %d 条配置，无需补充", totalCount)
	}
}

// ForceResetDefaults 强制重置所有配置为默认值
// 用于后台/config/init接口，删除旧数据后重新写入
// 确保配置数据一定存在，解决"后台什么参数都没有"的问题
func (s *SystemConfigService) ForceResetDefaults() error {
	tx := db.DB.Begin()

	// 1. P2-46 修复：只删系统层(tenant_id=0)——原 Where("1=1")
	//    会把各租户的覆盖层一起删掉（对比 ResetAll 明确不动租户层的语义），
	//    重置系统默认值不应波及租户个性化配置。
	if err := tx.Where("tenant_id = 0").Delete(&model.SystemConfig{}).Error; err != nil {
		tx.Rollback()
		return err
	}

	// 2. 批量插入默认配置
	if err := tx.Create(&DefaultConfigs).Error; err != nil {
		tx.Rollback()
		return err
	}

	tx.Commit()

	// 3. 重新加载内存缓存
	s.Reload()

	log.Printf("[系统配置] 强制重置完成，已写入 %d 条默认配置", len(DefaultConfigs))
	return nil
}

// Reload 从DB重新加载所有配置到内存缓存
// 每次Admin API更新配置后调用，实现热加载
func (s *SystemConfigService) Reload() {
	s.mu.Lock()
	defer s.mu.Unlock()

	var configs []model.SystemConfig
	// 按分类和排序读取所有配置
	if err := db.DB.Order("category ASC, sort_order ASC").Find(&configs).Error; err != nil {
		log.Printf("[系统配置] 加载配置失败: %v", err)
		return
	}

	// 重建内存缓存：系统默认(0)与租户覆盖分层
	newCache := make(map[string]string)
	newTenantCache := make(map[uint]map[string]string)
	for _, cfg := range configs {
		if cfg.TenantID == 0 {
			newCache[cfg.Key] = cfg.Value
		} else {
			if newTenantCache[cfg.TenantID] == nil {
				newTenantCache[cfg.TenantID] = make(map[string]string)
			}
			newTenantCache[cfg.TenantID][cfg.Key] = cfg.Value
		}
	}

	s.cache = newCache
	s.tenantCache = newTenantCache
	s.configs = configs

	log.Printf("[系统配置] 已加载 %d 项配置到内存（含 %d 个租户覆盖层）",
		len(configs), len(newTenantCache))
}

// ============================================================
// 查询方法 - 从内存缓存读取，无需查DB
// ============================================================

// GetAll 获取所有配置（含元信息）
// 返回完整的配置列表，用于Admin API返回
func (s *SystemConfigService) GetAll() []model.SystemConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// 返回副本，避免外部修改影响缓存
	result := make([]model.SystemConfig, len(s.configs))
	copy(result, s.configs)
	return result
}

// GetByCategory 按分类获取配置列表
// category: reply_speed / strategy / mental_stage / ai_chain
// GetByCategory 按分类返回当前生效的系统配置项。
func (s *SystemConfigService) GetByCategory(category string) []model.SystemConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []model.SystemConfig
	for _, cfg := range s.configs {
		if cfg.Category == category {
			result = append(result, cfg)
		}
	}
	return result
}

// GetByKey 按key获取单个配置
// 返回配置对象指针，未找到返回nil
func (s *SystemConfigService) GetByKey(key string) *model.SystemConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for i := range s.configs {
		if s.configs[i].Key == key {
			// 返回副本
			cfg := s.configs[i]
			return &cfg
		}
	}
	return nil
}

// ============================================================
// 便捷取值方法 - 按类型直接返回解析后的值
// 所有代码读配置应走这些方法，不直接读config.GlobalConfig
// ============================================================

// GetFloat 获取float64类型的配置值
// key: 配置键名，如 "tau"
// defaultValue: key不存在时的兜底值
// ============================================================
// 租户级配置（P2）：租户覆盖优先，回退系统默认(0)
// 语义：GetInt 等老方法 = 全局系统默认（引擎/平台层用）
//       GetXxxForTenant = 租户调优后的生效值（请求链路用）
// ============================================================

// lookupTenant 解析租户生效值：租户覆盖层 → 系统默认层
func (s *SystemConfigService) lookupTenant(tenantID uint, key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if tenantID > 0 {
		if tm := s.tenantCache[tenantID]; tm != nil {
			if v, ok := tm[key]; ok {
				return v, true
			}
		}
	}
	v, ok := s.cache[key]
	return v, ok
}

// GetIntForTenant 租户级 int 配置
func (s *SystemConfigService) GetIntForTenant(tenantID uint, key string, defaultValue int) int {
	if v, ok := s.lookupTenant(tenantID, key); ok {
		var n int
		if err := json.Unmarshal([]byte(v), &n); err == nil {
			return n
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return int(f)
		}
	}
	return defaultValue
}

// GetBoolForTenant 租户级 bool 配置
func (s *SystemConfigService) GetBoolForTenant(tenantID uint, key string, defaultValue bool) bool {
	if v, ok := s.lookupTenant(tenantID, key); ok {
		var b bool
		if err := json.Unmarshal([]byte(v), &b); err == nil {
			return b
		}
		if v == "true" {
			return true
		}
		if v == "false" {
			return false
		}
	}
	return defaultValue
}

// GetStringForTenant 租户级 string 配置
func (s *SystemConfigService) GetStringForTenant(tenantID uint, key string, defaultValue string) string {
	if v, ok := s.lookupTenant(tenantID, key); ok {
		var str string
		if err := json.Unmarshal([]byte(v), &str); err == nil {
			return str
		}
		return v
	}
	return defaultValue
}

// GetFloatForTenant 租户级 float 配置
func (s *SystemConfigService) GetFloatForTenant(tenantID uint, key string, defaultValue float64) float64 {
	if v, ok := s.lookupTenant(tenantID, key); ok {
		var f float64
		if err := json.Unmarshal([]byte(v), &f); err == nil {
			return f
		}
	}
	return defaultValue
}

// BatchUpdateForTenant 租户级批量更新：upsert 到 (tenant_id, key)
// 与全局 BatchUpdate 隔离——租户改参数绝不污染系统默认
func (s *SystemConfigService) BatchUpdateForTenant(tenantID uint, items []ConfigUpdateItem) error {
	if tenantID == 0 {
		return s.BatchUpdate(items)
	}
	for _, item := range items {
		// P2-55 修复：先归一化裸字符串值（补引号），再走 json.Valid 裁决
		normVal := normalizeConfigValue(item.Key, item.Value)
		if !json.Valid([]byte(normVal)) {
			log.Printf("[系统配置] 跳过非法JSON值: tenant=%d key=%s", tenantID, item.Key)
			continue
		}
		var existing model.SystemConfig
		err := db.DB.Where("tenant_id = ? AND \"key\" = ?", tenantID, item.Key).First(&existing).Error
		if err != nil {
			// 租户首次覆盖：以系统默认行做模板克隆
			var def model.SystemConfig
			db.DB.Where("tenant_id = 0 AND \"key\" = ?", item.Key).First(&def)
			row := model.SystemConfig{
				TenantID:     tenantID,
				Category:     def.Category,
				Key:          item.Key,
				Value:        normVal,
				ValueType:    def.ValueType,
				Description:  def.Description,
				DefaultValue: def.DefaultValue,
				SortOrder:    def.SortOrder,
			}
			if row.ValueType == "" {
				row.ValueType = "string"
			}
			if err := db.DB.Create(&row).Error; err != nil {
				log.Printf("[系统配置] 租户覆盖写入失败: tenant=%d key=%s err=%v", tenantID, item.Key, err)
				return err
			}
		} else {
			if err := db.DB.Model(&existing).Update("value", normVal).Error; err != nil {
				return err
			}
		}
		log.Printf("[系统配置] 租户覆盖已更新: tenant=%d key=%s", tenantID, item.Key)
	}
	s.Reload()
	return nil
}

// ============================================================
// P2-52 修复：nil-安全读取 helper
// 背景：SystemConfigService 单例在 main 启动时初始化，但部分包（engine/strategy、
// llm/chat_reply 等）在不依赖启动顺序的单测/冷路径直接访问 DefaultSystemConfigService
// 会 nil panic。统一出口为 Safecfg*.，未初始化/被替换时回退默认值，杜绝 panic。
// ============================================================

// SafeCfgBool 安全读 bool 配置（单例未初始化回退默认值，P2-52）
func SafeCfgBool(key string, defaultValue bool) bool {
	if DefaultSystemConfigService == nil {
		return defaultValue
	}
	return DefaultSystemConfigService.GetBool(key, defaultValue)
}

// SafeCfgInt 安全读 int 配置（P2-52）
func SafeCfgInt(key string, defaultValue int) int {
	if DefaultSystemConfigService == nil {
		return defaultValue
	}
	return DefaultSystemConfigService.GetInt(key, defaultValue)
}

// SafeCfgFloat 安全读 float 配置（P2-52）
func SafeCfgFloat(key string, defaultValue float64) float64 {
	if DefaultSystemConfigService == nil {
		return defaultValue
	}
	return DefaultSystemConfigService.GetFloat(key, defaultValue)
}

// SafeCfgString 安全读 string 配置（P2-52）
func SafeCfgString(key string, defaultValue string) string {
	if DefaultSystemConfigService == nil {
		return defaultValue
	}
	return DefaultSystemConfigService.GetString(key, defaultValue)
}

// SafeCfgIntSlice 安全读 int slice 配置（P2-52）
func SafeCfgIntSlice(key string, defaultValue []int) []int {
	if DefaultSystemConfigService == nil {
		return defaultValue
	}
	return DefaultSystemConfigService.GetIntSlice(key, defaultValue)
}

// GetFloat 获取 float 配置值（key 不存在回退默认值）
func (s *SystemConfigService) GetFloat(key string, defaultValue float64) float64 {
	s.mu.RLock()
	val, exists := s.cache[key]
	s.mu.RUnlock()

	if !exists {
		return defaultValue
	}

	var f float64
	if err := json.Unmarshal([]byte(val), &f); err == nil {
		return f
	}
	return defaultValue
}

// GetInt 获取int类型的配置值
// key: 配置键名，如 "theta_rounds"
// defaultValue: key不存在时的兜底值
func (s *SystemConfigService) GetInt(key string, defaultValue int) int {
	s.mu.RLock()
	val, exists := s.cache[key]
	s.mu.RUnlock()

	if !exists {
		return defaultValue
	}

	// JSON数字默认解析为float64，先试float再转int
	var f float64
	if err := json.Unmarshal([]byte(val), &f); err == nil {
		return int(f)
	}
	return defaultValue
}

// GetBool 获取bool类型的配置值
// key: 配置键名，如 "mock_mode"
// defaultValue: key不存在时的兜底值
func (s *SystemConfigService) GetBool(key string, defaultValue bool) bool {
	s.mu.RLock()
	val, exists := s.cache[key]
	s.mu.RUnlock()

	if !exists {
		return defaultValue
	}

	var b bool
	if err := json.Unmarshal([]byte(val), &b); err == nil {
		return b
	}
	return defaultValue
}

// GetString 获取string类型的配置值
// key: 配置键名
// defaultValue: key不存在时的兜底值
func (s *SystemConfigService) GetString(key string, defaultValue string) string {
	s.mu.RLock()
	val, exists := s.cache[key]
	s.mu.RUnlock()

	if !exists {
		return defaultValue
	}

	var s2 string
	if err := json.Unmarshal([]byte(val), &s2); err == nil {
		return s2
	}
	return val // 不是JSON字符串，直接返回原始值
}

// GetIntSlice 获取int数组类型的配置值
// key: 配置键名，如 "l3_simple_delay"
// defaultValue: key不存在时的兜底值
func (s *SystemConfigService) GetIntSlice(key string, defaultValue []int) []int {
	s.mu.RLock()
	val, exists := s.cache[key]
	s.mu.RUnlock()

	if !exists {
		return defaultValue
	}

	// 先尝试直接解析为[]int
	var intSlice []int
	if err := json.Unmarshal([]byte(val), &intSlice); err == nil {
		return intSlice
	}

	// 再尝试[]float64转[]int（JSON数字默认为float64）
	var floatSlice []float64
	if err := json.Unmarshal([]byte(val), &floatSlice); err == nil {
		result := make([]int, len(floatSlice))
		for i, v := range floatSlice {
			result[i] = int(v)
		}
		return result
	}

	return defaultValue
}

// GetStringSlice 获取string数组类型的配置值
// key: 配置键名，如 "model_priority"
// defaultValue: key不存在时的兜底值
func (s *SystemConfigService) GetStringSlice(key string, defaultValue []string) []string {
	s.mu.RLock()
	val, exists := s.cache[key]
	s.mu.RUnlock()

	if !exists {
		return defaultValue
	}

	var slice []string
	if err := json.Unmarshal([]byte(val), &slice); err == nil {
		return slice
	}
	return defaultValue
}

// GetJSON 获取任意JSON类型的配置值
// 通用反序列化方法，将配置值解析到target指针指向的结构中
// 用法：var weights []AnchorWeight; svc.GetJSON("anchor_weights", &weights)
// 修复：锚权重等复杂配置从硬编码→后台可调，需要通用JSON读取能力
func (s *SystemConfigService) GetJSON(key string, target interface{}) bool {
	s.mu.RLock()
	val, exists := s.cache[key]
	s.mu.RUnlock()

	if !exists {
		return false
	}

	if err := json.Unmarshal([]byte(val), target); err != nil {
		log.Printf("[系统配置] JSON解析失败: key=%s, error=%v", key, err)
		return false
	}
	return true
}

// ============================================================
// 更新方法 - 写DB + 热加载内存
// ============================================================

// BatchUpdate 批量更新配置
// items: [{key, value}, ...] 只更新Value字段，不改变其他元信息
// 更新后自动重载内存缓存
func (s *SystemConfigService) BatchUpdate(items []ConfigUpdateItem) error {
	if len(items) == 0 {
		return nil
	}

	// 逐项更新DB
	for _, item := range items {
		if item.Key == "" {
			continue
		}

		// P2-55 修复：先归一化裸字符串值（补引号），再校验合法JSON——
		// 原直接 json.Valid 会静默丢弃未引号的 string 提交（前端/脚本形态不一）
		normVal := normalizeConfigValue(item.Key, item.Value)

		// 修复（2026-08-23）：限定系统默认层(tenant_id=0)——本方法语义是"写系统层"，
		// 不带租户过滤会误改所有租户覆盖行
		result := db.DB.Model(&model.SystemConfig{}).
			Where("tenant_id = 0 AND \"key\" = ?", item.Key).
			Update("value", normVal)
		if result.Error != nil {
			log.Printf("[系统配置] 更新失败: key=%s, error=%v", item.Key, result.Error)
			return result.Error
		}
		// 修复（2026-09-15）：UPDATE-only 在行缺失时静默 no-op（返回成功但什么都没写）——
		// 新增默认键若未及 seed、或行被清理脚本误删，admin API 将永远写不回去。
		// 改为 upsert：0 行受影响即 INSERT（元数据尽量取 DefaultConfigs）
		if result.RowsAffected == 0 {
			row := model.SystemConfig{TenantID: 0, Key: item.Key, Value: normVal, ValueType: "string", Category: "custom"}
			for _, d := range DefaultConfigs {
				if d.Key == item.Key {
					row.Category = d.Category
					row.ValueType = d.ValueType
					row.Description = d.Description
					row.DefaultValue = d.DefaultValue
					row.SortOrder = d.SortOrder
					break
				}
			}
			if err := db.DB.Create(&row).Error; err != nil {
				log.Printf("[系统配置] 补建失败: key=%s, error=%v", item.Key, err)
				return err
			}
			log.Printf("[系统配置] 系统层缺失该行，已补建: key=%s", item.Key)
		}

		log.Printf("[系统配置] 已更新: key=%s, value=%s", item.Key, item.Value)
	}

	// 更新后重新加载内存缓存
	s.Reload()

	return nil
}

// ResetAll 恢复所有配置为默认值
// 用DefaultConfigs中的DefaultValue覆盖当前Value
// 修复（2026-08-23）：限定系统默认层(tenant_id=0)，租户覆盖层不动
func (s *SystemConfigService) ResetAll() error {
	for _, cfg := range DefaultConfigs {
		result := db.DB.Model(&model.SystemConfig{}).
			Where("tenant_id = 0 AND \"key\" = ?", cfg.Key).
			Update("value", cfg.DefaultValue)
		if result.Error != nil {
			log.Printf("[系统配置] 重置失败: key=%s, error=%v", cfg.Key, result.Error)
			return result.Error
		}
		// 修复（2026-09-15）：与 BatchUpdate 同口径——行缺失时补建，不再静默 no-op
		if result.RowsAffected == 0 {
			row := model.SystemConfig{TenantID: 0, Key: cfg.Key, Value: cfg.DefaultValue,
				ValueType: cfg.ValueType, Category: cfg.Category, Description: cfg.Description,
				DefaultValue: cfg.DefaultValue, SortOrder: cfg.SortOrder}
			if err := db.DB.Create(&row).Error; err != nil {
				log.Printf("[系统配置] 重置补建失败: key=%s, error=%v", cfg.Key, err)
				return err
			}
		}
	}

	// 重新加载内存缓存
	s.Reload()

	log.Println("[系统配置] 所有配置已恢复默认值")
	return nil
}

// ConfigUpdateItem 配置更新项
// Admin API批量更新时的请求体结构
type ConfigUpdateItem struct {
	Key   string `json:"key" binding:"required"`   // 配置键名
	Value string `json:"value" binding:"required"` // 新值（JSON字符串）
}

// normalizeConfigValue 归一化提交值：非JSON的裸字符串值自动补引号。
// P2-55 修复(2026-09-09)：历史提交形态不统一——`pay_mode` 带引号 `"mock"`、
// `reply_delay_mode` 不带引号——json.Valid 静默丢弃未引号提交。
// 第一个返回值是归一化后的提交值（非JSON裸字符串补引号，合法JSON原样返回）。
func normalizeConfigValue(key, value string) string {
	if json.Valid([]byte(value)) {
		return value
	}
	// 裸字符串 → 补引号（转义安全）
	if b, err := json.Marshal(value); err == nil {
		return string(b)
	}
	return value
}

// GetInterval 解析[min,max]格式的配置项，返回区间内的随机整数值
// 修复：到店倾向延迟等参数改为后台可调区间，统一用此方法解析
// fallback: 解析失败时返回defaultValue
func (s *SystemConfigService) GetInterval(key string, defaultMin, defaultMax int) int {
	var interval [2]int
	if s.GetJSON(key, &interval) && interval[0] >= 0 && interval[1] > interval[0] {
		return interval[0] + rand.Intn(interval[1]-interval[0]+1)
	}
	// fallback：用默认区间
	return defaultMin + rand.Intn(defaultMax-defaultMin+1)
}
