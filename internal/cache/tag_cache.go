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

// redisKeyTagVer 标签缓存多实例失效的 Redis 版本戳键名（Reload 时 INCR，各实例轮询比对）
const redisKeyTagVer = "cachever:tags"

// TagCacheManager 标签缓存管理器
type TagCacheManager struct {
	tags           []model.Tag              // 所有标签
	tagRules       []model.TagRule          // 打标规则
	weightMappings []model.TagWeightMapping // 权重映射
	version        int64                    // 版本号（每次reload自增）
	remoteVersion  int64                    // 已同步的Redis版本戳（跨实例失效）
	lastReloadAt   time.Time                // 最近一次成功重载时间（TTL 兜底刷新依据，2026-09-09）
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
	m.lastReloadAt = time.Now()    // TTL 兜底刷新计时（2026-09-09）
	m.mu.Unlock()

	log.Printf("[标签缓存] 热更新完成: 标签=%d个, 规则=%d条, 权重映射=%d条, 版本=%d",
		len(tags), len(rules), len(mappings), m.GetVersion())
}

// ttlStale TTL 兜底是否过期（2026-09-09）：距上次重载超过 ttl 判定为陈旧需强制刷新
func (m *TagCacheManager) ttlStale(ttl time.Duration) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return time.Since(m.lastReloadAt) > ttl
}

// startVersionPoller 跨实例版本轮询 + TTL 兜底刷新（每实例一个后台协程）
func (m *TagCacheManager) startVersionPoller() {
	go func() {
		for {
			time.Sleep(5 * time.Second)
			// TTL 兜底（2026-09-09）：纯版本戳驱动的缺陷——若某次本地 DB 更新既没走
			// Reload（事件丢失）也没经 Redis 广播（Redis 未启用/事件丢失），缓存会永久陈旧。
			// 这里兜底：距上次重载超过 60s 无论版本号是否变化都强制本地重载一次（自愈陈旧数据）。
			if m.ttlStale(60 * time.Second) {
				m.reloadLocal()
			}
			if !redisclient.IsEnabled() {
				continue
			}
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
// 标签相关查询方法（P0-4 修复：全部方法增加 tenantID 过滤，
// 只返回系统预置(tenant_id=0) + 请求租户私有(tenant_id=tid)，杜绝跨租户标签串扰）
// ============================================================

// visibleTag 标签可见性判定（系统预置全租户可见 + 本租户私有）
func visibleTag(t model.Tag, tenantID uint) bool {
	return t.TenantID == 0 || t.TenantID == tenantID
}

// GetAllTags 获取所有启用的标签（按租户可见范围）
func (m *TagCacheManager) GetAllTags(tenantID uint) []model.Tag {
	m.mu.RLock()
	defer m.mu.RUnlock()
	// 返回副本，防止外部修改影响缓存
	var result []model.Tag
	for _, t := range m.tags {
		if visibleTag(t, tenantID) {
			result = append(result, t)
		}
	}
	return result
}

// GetTagByID 根据ID获取标签（按租户可见范围）
func (m *TagCacheManager) GetTagByID(tenantID uint, id uint) *model.Tag {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range m.tags {
		if m.tags[i].ID == id && visibleTag(m.tags[i], tenantID) {
			tag := m.tags[i]
			return &tag
		}
	}
	return nil
}

// GetTagByCode 根据编码获取标签（按租户可见范围）
func (m *TagCacheManager) GetTagByCode(tenantID uint, code string) *model.Tag {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range m.tags {
		if m.tags[i].Code == code && visibleTag(m.tags[i], tenantID) {
			tag := m.tags[i]
			return &tag
		}
	}
	return nil
}

// ============================================================
// 打标规则相关查询方法
// ============================================================

// visibleRule 规则可见性判定
func visibleRule(r model.TagRule, tenantID uint) bool {
	return r.TenantID == 0 || r.TenantID == tenantID
}

// GetRulesByType 按规则类型获取规则列表（按租户可见范围）
func (m *TagCacheManager) GetRulesByType(tenantID uint, ruleType string) []model.TagRule {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []model.TagRule
	for _, rule := range m.tagRules {
		if rule.RuleType == ruleType && visibleRule(rule, tenantID) {
			result = append(result, rule)
		}
	}
	return result
}

// GetAllRules 获取所有启用的规则（按租户可见范围）
func (m *TagCacheManager) GetAllRules(tenantID uint) []model.TagRule {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []model.TagRule
	for _, r := range m.tagRules {
		if visibleRule(r, tenantID) {
			result = append(result, r)
		}
	}
	return result
}

// ============================================================
// 权重映射相关查询方法
// ============================================================

// GetWeightMappingsByTagID 根据标签ID获取权重映射（按租户可见范围）
// 一个标签可能对应T向量的多个维度
func (m *TagCacheManager) GetWeightMappingsByTagID(tenantID uint, tagID uint) []model.TagWeightMapping {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []model.TagWeightMapping
	for _, mapping := range m.weightMappings {
		if mapping.TagID == tagID && (mapping.TenantID == 0 || mapping.TenantID == tenantID) {
			result = append(result, mapping)
		}
	}
	return result
}

// GetAllWeightMappings 获取所有权重映射（按租户可见范围）
func (m *TagCacheManager) GetAllWeightMappings(tenantID uint) []model.TagWeightMapping {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []model.TagWeightMapping
	for _, mapping := range m.weightMappings {
		if mapping.TenantID == 0 || mapping.TenantID == tenantID {
			result = append(result, mapping)
		}
	}
	return result
}
