// Package cache 知识库与标签内存缓存，热更新加速高频读取、降低 DB 压力。
package cache

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/redisclient"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ============================================================
// 知识库缓存管理器 - 热更新核心
// 为什么需要缓存？知识库数据量中等，但对话过程中频繁查询
// 策略引擎推理、Prompt构建都需要查知识库，内存缓存大幅提升性能
//
// 热更新设计（同标签缓存）：
//   - 内存缓存 + sync.RWMutex 读写锁保护
//   - 版本号机制：每次 Reload 版本号+1
//   - 写时复制：Reload时先查DB到临时变量，再一次性替换
//
// 多实例失效（Phase M）：同标签缓存，版本戳 cachever:knowledge
// ============================================================

// redisKeyKnowledgeVer 变量定义（自动补注释）。
const redisKeyKnowledgeVer = "cachever:knowledge"

// KnowledgeCacheManager 知识库缓存管理器
type KnowledgeCacheManager struct {
	brands        []model.Brand             // 品牌列表
	models        []model.CarModel          // 车型列表
	specs         []model.ModelSpec         // 规格参数列表
	compares      []model.CompetitorCompare // 竞品对比列表
	fragments     []model.KnowledgeFragment // 知识片段列表
	version       int64                     // 版本号
	remoteVersion int64                     // 已同步的Redis版本戳（跨实例失效）
	mu            sync.RWMutex              // 读写锁
}

// DefaultKnowledgeCache 默认知识库缓存实例
var DefaultKnowledgeCache *KnowledgeCacheManager

// InitKnowledgeCache 初始化知识库缓存
func InitKnowledgeCache() {
	DefaultKnowledgeCache = &KnowledgeCacheManager{}
	DefaultKnowledgeCache.Reload()
	DefaultKnowledgeCache.startVersionPoller()
	log.Println("知识库缓存初始化完成")
}

// Reload 重新从DB加载所有知识库数据（热更新入口）
// 多实例：同时递增 Redis 版本戳，通知其他实例重载
func (m *KnowledgeCacheManager) Reload() {
	m.reloadLocal()
	if redisclient.IsEnabled() {
		m.remoteVersion = redisclient.Incr(redisKeyKnowledgeVer)
	}
}

// reloadLocal 仅本地重载（不递增Redis版本戳，防乒乓）
func (m *KnowledgeCacheManager) reloadLocal() {
	// 1. 从DB加载数据（不加锁，IO操作不占锁）
	var brands []model.Brand
	db.DB.Where("status = ?", 1).Order("sort ASC, id ASC").Find(&brands)

	var models []model.CarModel
	db.DB.Where("status = ?", 1).Order("sort ASC, id ASC").Find(&models)

	var specs []model.ModelSpec
	db.DB.Where("status = ?", 1).Order("model_id ASC, sort ASC, id ASC").Find(&specs)

	var compares []model.CompetitorCompare
	db.DB.Where("status = ?", 1).Order("our_model_id ASC, id ASC").Find(&compares)

	var fragments []model.KnowledgeFragment
	db.DB.Where("status = ?", 1).Order("sort ASC, id ASC").Find(&fragments)

	// 2. 加写锁，一次性替换
	m.mu.Lock()
	m.brands = brands
	m.models = models
	m.specs = specs
	m.compares = compares
	m.fragments = fragments
	atomic.AddInt64(&m.version, 1)
	m.mu.Unlock()

	log.Printf("[知识库缓存] 热更新完成: 品牌=%d个, 车型=%d款, 规格=%d条, 对比=%d组, 片段=%d条, 版本=%d",
		len(brands), len(models), len(specs), len(compares), len(fragments), m.GetVersion())
}

// startVersionPoller 跨实例版本轮询（每实例一个后台协程）
func (m *KnowledgeCacheManager) startVersionPoller() {
	if !redisclient.IsEnabled() {
		return
	}
	go func() {
		for {
			time.Sleep(5 * time.Second)
			v, ok := redisclient.Get(redisKeyKnowledgeVer)
			if !ok {
				continue
			}
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n == m.remoteVersion {
				continue
			}
			log.Printf("[知识库缓存] 检测到其他实例更新(版本戳%d→%d)，本地重载", m.remoteVersion, n)
			m.remoteVersion = n
			m.reloadLocal()
		}
	}()
}

// GetVersion 获取当前缓存版本号
func (m *KnowledgeCacheManager) GetVersion() int64 {
	return atomic.LoadInt64(&m.version)
}

// ============================================================
// 品牌相关查询
// ============================================================

// GetAllBrands 获取所有启用的品牌
func (m *KnowledgeCacheManager) GetAllBrands() []model.Brand {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]model.Brand, len(m.brands))
	copy(result, m.brands)
	return result
}

// GetBrandByID 根据ID获取品牌
func (m *KnowledgeCacheManager) GetBrandByID(id uint) *model.Brand {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range m.brands {
		if m.brands[i].ID == id {
			brand := m.brands[i]
			return &brand
		}
	}
	return nil
}

// GetBrandByCode 根据编码获取品牌
func (m *KnowledgeCacheManager) GetBrandByCode(code string) *model.Brand {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range m.brands {
		if m.brands[i].Code == code {
			brand := m.brands[i]
			return &brand
		}
	}
	return nil
}

// GetBrandByName 根据名称获取品牌（模糊匹配，包含即可）
func (m *KnowledgeCacheManager) GetBrandByName(name string) *model.Brand {
	m.mu.RLock()
	defer m.mu.RUnlock()
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	for i := range m.brands {
		if strings.Contains(m.brands[i].Name, name) || strings.Contains(name, m.brands[i].Name) {
			brand := m.brands[i]
			return &brand
		}
	}
	return nil
}

// GetDefaultModel 获取默认推荐车型（第一个品牌的第一款车型）
// 用于客户没有明确兴趣车型时，默认注入该车型的知识库
func (m *KnowledgeCacheManager) GetDefaultModel() *model.CarModel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.brands) == 0 || len(m.models) == 0 {
		return nil
	}
	// 取第一个品牌的第一款车型作为默认
	firstBrand := m.brands[0]
	for i := range m.models {
		if m.models[i].BrandID == firstBrand.ID {
			carModel := m.models[i]
			return &carModel
		}
	}
	// 没找到就返回全部车型里的第一个
	if len(m.models) > 0 {
		carModel := m.models[0]
		return &carModel
	}
	return nil
}

// ============================================================
// 车型相关查询
// ============================================================

// GetModelsByBrandID 根据品牌ID获取车型列表
func (m *KnowledgeCacheManager) GetModelsByBrandID(brandID uint) []model.CarModel {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []model.CarModel
	for _, carModel := range m.models {
		if carModel.BrandID == brandID {
			result = append(result, carModel)
		}
	}
	return result
}

// GetModelByID 根据ID获取车型
func (m *KnowledgeCacheManager) GetModelByID(id uint) *model.CarModel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range m.models {
		if m.models[i].ID == id {
			carModel := m.models[i]
			return &carModel
		}
	}
	return nil
}

// GetModelByName 根据名称获取车型
func (m *KnowledgeCacheManager) GetModelByName(name string) *model.CarModel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range m.models {
		if m.models[i].Name == name {
			carModel := m.models[i]
			return &carModel
		}
	}
	return nil
}

// GetAllModels 获取所有启用的车型
func (m *KnowledgeCacheManager) GetAllModels() []model.CarModel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]model.CarModel, len(m.models))
	copy(result, m.models)
	return result
}

// ============================================================
// 规格参数相关查询
// ============================================================

// GetSpecsByModelID 根据车型ID获取规格参数
func (m *KnowledgeCacheManager) GetSpecsByModelID(modelID uint) []model.ModelSpec {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []model.ModelSpec
	for _, spec := range m.specs {
		if spec.ModelID == modelID {
			result = append(result, spec)
		}
	}
	return result
}

// GetTopSpecsByModelID 获取车型的Top N个核心规格参数
// 用于Prompt注入时控制长度
func (m *KnowledgeCacheManager) GetTopSpecsByModelID(modelID uint, topN int) []model.ModelSpec {
	specs := m.GetSpecsByModelID(modelID)
	if len(specs) <= topN {
		return specs
	}
	return specs[:topN]
}

// ============================================================
// 竞品对比相关查询
// ============================================================

// GetComparesByModelID 根据我方车型ID获取竞品对比列表
func (m *KnowledgeCacheManager) GetComparesByModelID(ourModelID uint) []model.CompetitorCompare {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []model.CompetitorCompare
	for _, compare := range m.compares {
		if compare.OurModelID == ourModelID {
			result = append(result, compare)
		}
	}
	return result
}

// GetComparesByModelAndBrand 根据车型和竞品品牌获取对比
// 用于对比锚话术时精准定位素材
func (m *KnowledgeCacheManager) GetComparesByModelAndBrand(ourModelID uint, competitorBrand string) []model.CompetitorCompare {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []model.CompetitorCompare
	for _, compare := range m.compares {
		if compare.OurModelID == ourModelID &&
			strings.Contains(compare.CompetitorBrand, competitorBrand) {
			result = append(result, compare)
		}
	}
	return result
}

// ============================================================
// 知识片段相关查询
// ============================================================

// SearchFragments 搜索知识片段
// 支持关键词、分类、标签三种过滤条件，都是OR关系
// 简单的内存搜索，数据量不大时足够高效
func (m *KnowledgeCacheManager) SearchFragments(keyword string, category string, tags []string) []model.KnowledgeFragment {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []model.KnowledgeFragment
	keywordLower := strings.ToLower(keyword)

	for _, frag := range m.fragments {
		// 分类过滤
		if category != "" && frag.Category != category {
			continue
		}

		// 关键词匹配（标题或内容包含）
		if keyword != "" {
			titleMatch := strings.Contains(strings.ToLower(frag.Title), keywordLower)
			contentMatch := strings.Contains(strings.ToLower(frag.Content), keywordLower)
			if !titleMatch && !contentMatch {
				// 也检查标签
				tagMatch := false
				fragTags := frag.GetTags()
				for _, t := range fragTags {
					if strings.Contains(strings.ToLower(t), keywordLower) {
						tagMatch = true
						break
					}
				}
				if !tagMatch {
					continue
				}
			}
		}

		// 标签过滤（命中任意一个标签即可）
		if len(tags) > 0 {
			fragTags := frag.GetTags()
			matched := false
			for _, searchTag := range tags {
				for _, fragTag := range fragTags {
					if strings.Contains(strings.ToLower(fragTag), strings.ToLower(searchTag)) {
						matched = true
						break
					}
				}
				if matched {
					break
				}
			}
			if !matched {
				continue
			}
		}

		result = append(result, frag)
	}

	return result
}

// GetFragmentsByCategory 按分类获取知识片段
func (m *KnowledgeCacheManager) GetFragmentsByCategory(category string) []model.KnowledgeFragment {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []model.KnowledgeFragment
	for _, frag := range m.fragments {
		if frag.Category == category {
			result = append(result, frag)
		}
	}
	return result
}
