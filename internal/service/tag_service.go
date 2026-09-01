// 打标服务：手动/自动打标、标签权重累加、打标驱动 T 向量策略。
package service

import (
	"ai-scrm/internal/cache"
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"log"
	"strings"
	"time"
)

// ============================================================
// 打标服务层 - 标签体系的业务逻辑
// 核心能力：
//   1. 手动给客户打标/移除标签
//   2. 从文本自动打标（关键词匹配TagRule）
//   3. 根据客户标签 + 权重映射，更新T向量（打标驱动策略的核心）
// ============================================================

// TagService 标签服务结构体
// 提供手动/自动打标、标签权重累加、打标驱动T向量策略等核心能力
type TagService struct{}

// DefaultTagService 默认标签服务实例
var DefaultTagService = &TagService{}

// ============================================================
// 基础打标操作
// ============================================================

// ApplyTagsToCustomer 批量给客户打标签
// 参数：customerID-客户ID, tagNames-标签名称或编码数组, source-标签来源(manual/auto/ai)
// 自动匹配标签名称或编码，已存在的标签更新来源，新标签创建关联
func (s *TagService) ApplyTagsToCustomer(customerID uint, tagNames []string, source string) error {
	if len(tagNames) == 0 {
		return nil
	}

	// 获取所有标签（从缓存）
	allTags := cache.DefaultTagCache.GetAllTags()
	// 建立名称→标签、编码→标签的映射
	nameMap := make(map[string]model.Tag)
	codeMap := make(map[string]model.Tag)
	for _, tag := range allTags {
		nameMap[tag.Name] = tag
		codeMap[tag.Code] = tag
	}

	now := time.Now()
	newTags := 0

	// 逐个处理标签
	for _, name := range tagNames {
		// 先按名称匹配，再按编码匹配
		var targetTag *model.Tag
		if t, ok := nameMap[name]; ok {
			targetTag = &t
		} else if t, ok := codeMap[name]; ok {
			targetTag = &t
		}
		if targetTag == nil {
			log.Printf("[打标服务] 标签不存在: %s, 跳过", name)
			continue
		}

		// 检查是否已存在该客户标签（用联合唯一索引去重）
		var existing model.CustomerTag
		result := db.DB.Where("customer_id = ? AND tag_id = ?", customerID, targetTag.ID).First(&existing)
		if result.Error == nil {
			// 已存在，更新来源
			existing.Source = source
			db.DB.Save(&existing)
			continue
		}

		// 新建客户标签关联
		customerTag := &model.CustomerTag{
			CustomerID: customerID,
			TagID:      targetTag.ID,
			TagName:    targetTag.Name,
			Source:     source,
			Weight:     1.0,
			CreatedAt:  now,
		}
		if err := db.DB.Create(customerTag).Error; err != nil {
			log.Printf("[打标服务] 创建客户标签失败: %v", err)
			continue
		}
		newTags++
	}

	log.Printf("[打标服务] 客户%d打标完成: 尝试%d个, 新增%d个",
		customerID, len(tagNames), newTags)

	// 同步更新客户表的Tags字段（冗余存储，方便查询）
	s.syncCustomerTagsField(customerID)

	return nil
}

// RemoveTagFromCustomer 移除客户的某个标签
// 删除customer_tags表中的关联记录，并同步更新客户表的Tags冗余字段
func (s *TagService) RemoveTagFromCustomer(customerID uint, tagID uint) error {
	result := db.DB.Where("customer_id = ? AND tag_id = ?", customerID, tagID).
		Delete(&model.CustomerTag{})
	if result.Error != nil {
		log.Printf("[打标服务] 移除标签失败: %v", result.Error)
		return result.Error
	}

	log.Printf("[打标服务] 客户%d移除标签%d, 影响行数=%d",
		customerID, tagID, result.RowsAffected)

	// 同步更新客户表的Tags字段
	s.syncCustomerTagsField(customerID)

	return nil
}

// GetCustomerTags 获取客户的所有标签
// 过滤掉已过期的标签，按创建时间倒序返回
func (s *TagService) GetCustomerTags(customerID uint) ([]model.CustomerTag, error) {
	var tags []model.CustomerTag
	err := db.DB.Where("customer_id = ?", customerID).
		Order("created_at DESC").
		Find(&tags).Error
	if err != nil {
		return nil, err
	}

	// 过滤掉已过期的标签（过期时间非零且已过）
	var validTags []model.CustomerTag
	now := time.Now()
	for _, tag := range tags {
		if !tag.ExpireAt.IsZero() && tag.ExpireAt.Before(now) {
			continue
		}
		validTags = append(validTags, tag)
	}

	return validTags, nil
}

// syncCustomerTagsField 同步客户表的Tags冗余字段
// customer_tags表是主存储，customers.tags是冗余的JSON数组，方便快速查询
func (s *TagService) syncCustomerTagsField(customerID uint) {
	customerTags, err := s.GetCustomerTags(customerID)
	if err != nil {
		log.Printf("[打标服务] 同步标签字段失败: %v", err)
		return
	}

	var tagNames []string
	for _, ct := range customerTags {
		tagNames = append(tagNames, ct.TagName)
	}

	// 更新客户表的tags字段
	var customer model.Customer
	if err := db.DB.First(&customer, customerID).Error; err != nil {
		return
	}
	customer.SetTags(tagNames)
	db.DB.Model(&customer).Update("tags", customer.Tags)
}

// ============================================================
// 自动打标 - 从文本中识别标签
// ============================================================

// AutoTagFromText 从客户输入文本中自动打标
// 遍历所有启用的TagRule，命中任何一个关键词就打上对应标签
// 已有标签的话，累加权重（每次+0.5，上限5.0），而不是跳过
// 支持三种规则类型：keyword（关键词匹配）、intent（意图识别）、resistance（抗性识别）
// 返回：新打上的标签名称列表（供前端展示）
func (s *TagService) AutoTagFromText(customerID uint, text string) ([]string, error) {
	if text == "" {
		return []string{}, nil
	}

	textLower := strings.ToLower(text)

	// 获取所有启用的打标规则（从缓存）
	rules := cache.DefaultTagCache.GetAllRules()
	if len(rules) == 0 {
		return []string{}, nil
	}

	// 获取客户已有标签，用于判断是否是新标签
	existingTags, err := s.GetCustomerTags(customerID)
	if err != nil {
		return []string{}, err
	}
	existingTagMap := make(map[uint]model.CustomerTag)
	for _, ct := range existingTags {
		existingTagMap[ct.TagID] = ct
	}

	// 遍历规则，检查是否命中
	var newTagNames []string // 本轮新打的标签名（首次打上）
	var hitTagIDs []uint     // 本轮命中的所有标签ID（含已有和新增）

	for _, rule := range rules {
		patterns := rule.GetMatchPatterns()
		hit := false

		// keyword类型：关键词匹配，命中任何一个就算
		if rule.RuleType == "keyword" {
			for _, pattern := range patterns {
				if strings.Contains(textLower, strings.ToLower(pattern)) {
					hit = true
					break
				}
			}
		}

		// intent类型：意图识别，关键词匹配 + 上下文判断（简化版，后续可升级为AI分类）
		if rule.RuleType == "intent" {
			matchCount := 0
			for _, pattern := range patterns {
				if strings.Contains(textLower, strings.ToLower(pattern)) {
					matchCount++
				}
			}
			// 意图类型需要命中至少1个关键词（简化处理）
			if matchCount >= 1 {
				hit = true
			}
		}

		// resistance类型：抗性识别，和策略引擎的DetectResistance类似
		if rule.RuleType == "resistance" {
			for _, pattern := range patterns {
				if strings.Contains(textLower, strings.ToLower(pattern)) {
					hit = true
					break
				}
			}
		}

		if hit {
			hitTagIDs = append(hitTagIDs, rule.TargetTagID)
			// 检查是否是首次打上的标签
			if _, exists := existingTagMap[rule.TargetTagID]; !exists {
				newTagNames = append(newTagNames, rule.TargetTagName)
			}
			log.Printf("[自动打标] 客户%d命中规则[%s], 标签[%s]",
				customerID, rule.Name, rule.TargetTagName)
		}
	}

	// 处理命中的标签：已有标签累加权重，新标签创建
	if len(hitTagIDs) > 0 {
		s.applyHitTagsWithWeight(customerID, hitTagIDs, existingTagMap, "auto")
	}

	return newTagNames, nil
}

// applyHitTagsWithWeight 应用命中的标签，已有标签累加权重，新标签创建
// 权重累加规则：每次+0.5，上限5.0
// 已有标签更新权重，新标签创建关联记录
func (s *TagService) applyHitTagsWithWeight(
	customerID uint,
	hitTagIDs []uint,
	existingTagMap map[uint]model.CustomerTag,
	source string,
) {
	now := time.Now()

	for _, tagID := range hitTagIDs {
		if existing, exists := existingTagMap[tagID]; exists {
			// 已有标签：累加权重（+0.5，上限5.0）
			newWeight := existing.Weight + 0.5
			if newWeight > 5.0 {
				newWeight = 5.0
			}
			if newWeight != existing.Weight {
				db.DB.Model(&existing).Update("weight", newWeight)
				log.Printf("[自动打标] 客户%d标签[%s]权重累加: %.1f → %.1f",
					customerID, existing.TagName, existing.Weight, newWeight)
			}
		} else {
			// 新标签：从缓存查标签信息，创建记录
			tag := cache.DefaultTagCache.GetTagByID(tagID)
			if tag == nil {
				continue
			}
			customerTag := &model.CustomerTag{
				CustomerID: customerID,
				TagID:      tagID,
				TagName:    tag.Name,
				Source:     source,
				Weight:     1.0,
				CreatedAt:  now,
			}
			if err := db.DB.Create(customerTag).Error; err != nil {
				log.Printf("[自动打标] 创建客户标签失败: %v", err)
			}
		}
	}

	// 同步更新客户表的Tags冗余字段
	s.syncCustomerTagsField(customerID)
}

// ============================================================
// 打标驱动策略 - 核心方法
// 根据客户的所有标签 + 权重映射，更新T向量
// 这是"标签驱动策略"的核心：标签影响T向量，T向量影响策略决策
// ============================================================

// ApplyTagWeightsToTVector 根据客户标签 + 权重映射，更新T向量
// 流程：
//  1. 获取客户所有标签
//  2. 对每个标签，查找对应的权重映射
//  3. 在传入的"基准向量baseVector"上按权重映射逐个更新T向量的对应维度
//  4. 保存更新后的T向量到客户对象（内存中，不存DB）
//
// 注意：这个方法只修改传入的customer对象的T向量，不写入DB
// 写入DB由调用方决定（策略引擎只需要内存中的T向量做推理）
// baseVector 必须由调用方显式传入客户的"基准T向量"（通常是
// customer.BuildBaseTVector() 的结果），不能在函数内部自己用
// customer.GetTVector()/BuildBaseTVector() 现算——
// 原因1：GetTVector()读的是上次已经叠加过标签权重的持久化结果，每调用一次就在
// 已经偏移过的基础上再加一次全部标签delta，多轮对话后0-1维度必然被推到0或1；
// 原因2：策略引擎(strategy.go)在推理时构造的customer只是个只带ID的临时对象，
// 结构化字段(预算/意向分等)全是空，如果这里再调用BuildBaseTVector()会现算出
// 一个全零的"假基准"，把客户真实画像清空。
// 所以基准值必须由持有真实客户结构化字段的一方（chat.go）算好后传进来
func (s *TagService) ApplyTagWeightsToTVector(customer *model.Customer, baseVector [32]float64) error {
	if customer == nil {
		return nil
	}

	// 获取客户所有标签
	customerTags, err := s.GetCustomerTags(customer.ID)
	if err != nil {
		log.Printf("[打标驱动] 获取客户标签失败: %v", err)
		return err
	}

	if len(customerTags) == 0 {
		return nil
	}

	tVector := baseVector
	original := tVector // 保存原始值用于日志对比

	// 对每个标签，应用权重映射
	for _, ct := range customerTags {
		mappings := cache.DefaultTagCache.GetWeightMappingsByTagID(ct.TagID)
		if len(mappings) == 0 {
			continue
		}

		// 客户标签权重（单客户维度可调整）
		tagWeight := ct.Weight
		if tagWeight <= 0 {
			tagWeight = 1.0
		}

		for _, mapping := range mappings {
			idx := mapping.TVectorIndex
			if idx < 0 || idx >= 32 {
				continue // 越界保护
			}

			// 计算实际权重变化 = 映射权重变化 * 标签权重
			delta := mapping.WeightDelta * tagWeight
			if mapping.Direction == "down" {
				delta = -delta
			}

			tVector[idx] += delta

			// clamp到0-1范围（对0-1的维度做限制）
			// 注意：T向量中有些维度不是0-1的（如车型编码、决策周期等）
			// 这里只对已知的0-1维度做clamp
			if isZeroOneDimension(idx) {
				if tVector[idx] < 0 {
					tVector[idx] = 0
				}
				if tVector[idx] > 1 {
					tVector[idx] = 1
				}
			}
		}
	}

	// 将更新后的T向量写回customer对象
	customer.SaveTVector(tVector)

	// 日志：记录变化较大的维度
	log.Printf("[打标驱动] 客户%d的T向量已应用标签权重 (标签数=%d, 映射影响维度: 意向分%.2f→%.2f, 价格敏感%.2f→%.2f, 品牌认知%.2f→%.2f)",
		customer.ID, len(customerTags),
		original[0], tVector[0],
		original[1], tVector[1],
		original[2], tVector[2],
	)

	return nil
}

// isZeroOneDimension 判断T向量某维是否是0-1范围的维度
// T向量说明：
//
//	T[0]=意向分(0-1), T[1]=价格敏感度(0-1), T[2]=品牌认知度(0-1)
//	T[3]=车型编码(数值), T[4]=到店状态(0/1/2), T[5]=决策周期(天)
//	T[6]=信任度(0-1), T[7]=流量来源编码(数值)
//	T[8]=性别(0/1/2), T[9]=年龄段(数值), T[10]=地域编码(数值)...
//	T[14]=抗性类型编码(数值), T[15]=客户身份编码(0/1)
func isZeroOneDimension(idx int) bool {
	zeroOneDims := map[int]bool{
		0: true, // 意向分
		1: true, // 价格敏感度
		2: true, // 品牌认知度
		6: true, // 信任度
	}
	return zeroOneDims[idx]
}
