// Package cache 知识库与标签内存缓存，热更新加速高频读取、降低 DB 压力。
package cache

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/redisclient"
	"log"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// ============================================================
// 标签缓存管理器 - 热更新核心
// 为什么需要缓存？标签/规则/权重映射数据量不大，但访问频率极高
// 每次策略推理都要查标签，直接查DB会有性能瓶颈
//
// 热更新设计：
//   - 内存缓存 + sync.RWMutex 读写锁保护
//   - 版本号机制：每次 Reload 版本号+1，调用方可校验是否变化
//   - 写时复制：Reload时先查DB到临时变量，再一次性替换（加写锁）
//
// 多实例失效（Phase M）：
//   - 本实例 Reload 时 INCR Redis 版本戳 cachever:tags
//   - 各实例每 5s 轮询版本戳，发现其他实例的更新则本地重载（不回弹，防乒乓）
// ============================================================

// redisKeyTagVer 变量定义（自动补注释）。
const redisKeyTagVer = "cachever:tags"

// TagCacheManager 标签缓存管理器
type TagCacheManager struct {
	tags           []model.Tag              // 所有标签
	tagRules       []model.TagRule          // 打标规则
	weightMappings []model.TagWeightMapping // 权重映射
	version        int64                    // 版本号（每次reload自增）
	remoteVersion  int64                    // 已同步的Redis版本戳（跨实例失效）
	mu             sync.RWMutex             // 读写锁
}

// DefaultTagCache 默认标签缓存实例
var DefaultTagCache *TagCacheManager

// InitTagCache 初始化标签缓存
// 系统启动时调用，从DB加载所有标签相关数据到内存
func InitTagCache() {
	DefaultTagCache = &TagCacheManager{}
	DefaultTagCache.Reload()
	DefaultTagCache.startVersionPoller()
	log.Println("标签缓存初始化完成")
}

// Reload 重新从DB加载数据（热更新入口）
// 管理后台修改标签/规则/映射后，调用此方法即时生效
// 设计：先读DB到临时变量，再一次性替换（写时复制思想），减少锁持有时间
// 多实例：同时递增 Redis 版本戳，通知其他实例重载
func (m *TagCacheManager) Reload() {
	m.reloadLocal()
	if redisclient.IsEnabled() {
		m.remoteVersion = redisclient.Incr(redisKeyTagVer)
	}
}

// reloadLocal 仅本地重载（不递增Redis版本戳，供跨实例同步调用，防乒乓）
func (m *TagCacheManager) reloadLocal() {
	// 1. 从DB加载数据（不加锁，IO操作不占锁）
	var tags []model.Tag
	db.DB.Where("status = ?", 1).Find(&tags)

	var rules []model.TagRule
	db.DB.Where("status = ?", 1).Find(&rules)

	var mappings []model.TagWeightMapping
	db.DB.Where("status = ?", 1).Find(&mappings)

	// 2. 加写锁，一次性替换
	m.mu.Lock()
	m.tags = tags
	m.tagRules = rules
	m.weightMappings = mappings
	atomic.AddInt64(&m.version, 1) // 版本号原子自增
	m.mu.Unlock()

	log.Printf("[标签缓存] 热更新完成: 标签=%d个, 规则=%d条, 权重映射=%d条, 版本=%d",
		len(tags), len(rules), len(mappings), m.GetVersion())
}

// startVersionPoller 跨实例版本轮询（每实例一个后台协程）
func (m *TagCacheManager) startVersionPoller() {
	if !redisclient.IsEnabled() {
		return
	}
	go func() {
		for {
			time.Sleep(5 * time.Second)
			v, ok := redisclient.Get(redisKeyTagVer)
			if !ok {
				continue
			}
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n == m.remoteVersion {
				continue
			}
			log.Printf("[标签缓存] 检测到其他实例更新(版本戳%d→%d)，本地重载", m.remoteVersion, n)
			m.remoteVersion = n
			m.reloadLocal()
		}
	}()
}

// GetVersion 获取当前缓存版本号
// 用原子读，无需加锁
func (m *TagCacheManager) GetVersion() int64 {
	return atomic.LoadInt64(&m.version)
}

// ============================================================
// 标签相关查询方法
// ============================================================

// GetAllTags 获取所有启用的标签
func (m *TagCacheManager) GetAllTags() []model.Tag {
	m.mu.RLock()
	defer m.mu.RUnlock()
	// 返回副本，防止外部修改影响缓存
	result := make([]model.Tag, len(m.tags))
	copy(result, m.tags)
	return result
}

// GetTagByID 根据ID获取标签
func (m *TagCacheManager) GetTagByID(id uint) *model.Tag {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range m.tags {
		if m.tags[i].ID == id {
			tag := m.tags[i]
			return &tag
		}
	}
	return nil
}

// GetTagByCode 根据编码获取标签
func (m *TagCacheManager) GetTagByCode(code string) *model.Tag {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range m.tags {
		if m.tags[i].Code == code {
			tag := m.tags[i]
			return &tag
		}
	}
	return nil
}

// ============================================================
// 打标规则相关查询方法
// ============================================================

// GetRulesByType 按规则类型获取规则列表
func (m *TagCacheManager) GetRulesByType(ruleType string) []model.TagRule {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []model.TagRule
	for _, rule := range m.tagRules {
		if rule.RuleType == ruleType {
			result = append(result, rule)
		}
	}
	return result
}

// GetAllRules 获取所有启用的规则
func (m *TagCacheManager) GetAllRules() []model.TagRule {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]model.TagRule, len(m.tagRules))
	copy(result, m.tagRules)
	return result
}

// ============================================================
// 权重映射相关查询方法
// ============================================================

// GetWeightMappingsByTagID 根据标签ID获取权重映射
// 一个标签可能对应T向量的多个维度
func (m *TagCacheManager) GetWeightMappingsByTagID(tagID uint) []model.TagWeightMapping {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []model.TagWeightMapping
	for _, mapping := range m.weightMappings {
		if mapping.TagID == tagID {
			result = append(result, mapping)
		}
	}
	return result
}

// GetAllWeightMappings 获取所有权重映射
func (m *TagCacheManager) GetAllWeightMappings() []model.TagWeightMapping {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]model.TagWeightMapping, len(m.weightMappings))
	copy(result, m.weightMappings)
	return result
}
