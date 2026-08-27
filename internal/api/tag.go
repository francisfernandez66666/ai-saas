// 标签体系管理：标签/打标规则/权重映射 CRUD + 缓存热更新 + 客户打标（冗余标签名/编码）
package api

import (
	"ai-scrm/internal/cache"
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/schema"
	"ai-scrm/internal/service"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 标签管理API（管理端）
// 标签、打标规则、权重映射的CRUD + 热更新
// ============================================================

// ============================================================
// 标签 CRUD
// ============================================================

// GetTagList 获取标签列表（分页+分类筛选+关键词搜索+状态筛选）
func GetTagList(c *gin.Context) {
	var req schema.Pagination
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误", Data: nil})
		return
	}

	query := db.PQ(c).Model(&model.Tag{})

	// 分类筛选
	category := c.Query("category")
	if category != "" {
		query = query.Where("category = ?", category)
	}

	// 状态筛选
	status := c.Query("status")
	if status != "" {
		query = query.Where("status = ?", status)
	}

	// 关键词搜索
	keyword := c.Query("keyword")
	if keyword != "" {
		keyword = "%" + keyword + "%"
		query = query.Where("name LIKE ? OR code LIKE ? OR description LIKE ?", keyword, keyword, keyword)
	}

	var total int64
	query.Count(&total)

	var tags []model.Tag
	query.Order("id DESC").
		Offset(req.GetOffset()).
		Limit(req.PageSize).
		Find(&tags)

	c.JSON(http.StatusOK, schema.Response{
		Code: 0, Message: "success",
		Data: schema.PageResponse{
			Total: total, Page: req.Page, PageSize: req.PageSize, List: tags,
		},
	})
}

// GetTagDetail 获取标签详情
func GetTagDetail(c *gin.Context) {
	id := c.Param("id")

	var tag model.Tag
	if err := db.PQ(c).Scopes(db.T(c)).First(&tag, id).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "标签不存在", Data: nil})
		return
	}

	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "success", Data: tag})
}

// CreateTag 创建标签
func CreateTag(c *gin.Context) {
	var req struct {
		Name        string  `json:"name" binding:"required"`
		Code        string  `json:"code" binding:"required"`
		Category    string  `json:"category"`
		Weight      float64 `json:"weight"`
		Description string  `json:"description"`
		Status      int     `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误: " + err.Error(), Data: nil})
		return
	}

	tag := &model.Tag{
		Name:        req.Name,
		Code:        req.Code,
		Category:    req.Category,
		Weight:      req.Weight,
		Description: req.Description,
		Status:      req.Status,
	}
	// 缺省权重 1.0、状态 1（启用），避免零值导致权重失效
	if tag.Weight == 0 {
		tag.Weight = 1.0
	}
	if tag.Status == 0 {
		tag.Status = 1
	}

	if err := db.RQ(c).Create(tag).Error; err != nil {
		c.JSON(http.StatusInternalServerError, schema.Response{Code: 500, Message: "创建失败: " + err.Error(), Data: nil})
		return
	}

	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "创建成功", Data: tag})
}

// UpdateTag 更新标签
func UpdateTag(c *gin.Context) {
	id := c.Param("id")

	var tag model.Tag
	if err := db.RQ(c).Scopes(db.T(c)).First(&tag, id).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "标签不存在", Data: nil})
		return
	}

	var req struct {
		Name        string  `json:"name"`
		Category    string  `json:"category"`
		Weight      float64 `json:"weight"`
		Description string  `json:"description"`
		Status      int     `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误", Data: nil})
		return
	}

	if req.Name != "" {
		tag.Name = req.Name
	}
	if req.Category != "" {
		tag.Category = req.Category
	}
	if req.Weight != 0 {
		tag.Weight = req.Weight
	}
	if req.Description != "" {
		tag.Description = req.Description
	}
	if req.Status != 0 {
		tag.Status = req.Status
	}

	db.RQ(c).Save(&tag)

	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "更新成功", Data: tag})
}

// DeleteTag 删除标签
// 软删除：status设为0，不是物理删除
func DeleteTag(c *gin.Context) {
	id := c.Param("id")

	var tag model.Tag
	if err := db.RQ(c).Scopes(db.T(c)).First(&tag, id).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "标签不存在", Data: nil})
		return
	}

	// 软删除：状态设为0
	tag.Status = 0
	db.RQ(c).Save(&tag)

	// 删除后自动热更新缓存
	cache.DefaultTagCache.Reload()

	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "删除成功", Data: nil})
}

// EnableTag 启用标签
// 上线后自动触发缓存热更新
func EnableTag(c *gin.Context) {
	id := c.Param("id")

	var tag model.Tag
	if err := db.RQ(c).Scopes(db.T(c)).First(&tag, id).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "标签不存在", Data: nil})
		return
	}

	tag.Status = 1
	db.RQ(c).Save(&tag)

	// 自动热更新缓存
	cache.DefaultTagCache.Reload()

	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "启用成功", Data: tag})
}

// DisableTag 下线标签
// 下线后自动触发缓存热更新
func DisableTag(c *gin.Context) {
	id := c.Param("id")

	var tag model.Tag
	if err := db.RQ(c).Scopes(db.T(c)).First(&tag, id).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "标签不存在", Data: nil})
		return
	}

	tag.Status = 0
	db.RQ(c).Save(&tag)

	// 自动热更新缓存
	cache.DefaultTagCache.Reload()

	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "下线成功", Data: tag})
}

// ReloadTagCache 热更新标签缓存
// 修改标签/规则/映射后调用，即时生效
func ReloadTagCache(c *gin.Context) {
	cache.DefaultTagCache.Reload()

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "标签缓存热更新成功",
		Data: gin.H{
			"version": cache.DefaultTagCache.GetVersion(),
		},
	})
}

// ============================================================
// 打标规则 CRUD
// ============================================================

// GetTagRuleList 获取打标规则列表（分页+类型筛选+状态筛选+关键词搜索）
func GetTagRuleList(c *gin.Context) {
	var req schema.Pagination
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误", Data: nil})
		return
	}

	query := db.PQ(c).Model(&model.TagRule{})

	// 按类型筛选
	ruleType := c.Query("rule_type")
	if ruleType != "" {
		query = query.Where("rule_type = ?", ruleType)
	}

	// 状态筛选
	status := c.Query("status")
	if status != "" {
		query = query.Where("status = ?", status)
	}

	// 关键词搜索
	keyword := c.Query("keyword")
	if keyword != "" {
		keyword = "%" + keyword + "%"
		query = query.Where("name LIKE ? OR target_tag_name LIKE ?", keyword, keyword)
	}

	var total int64
	query.Count(&total)

	var rules []model.TagRule
	query.Order("id DESC").
		Offset(req.GetOffset()).
		Limit(req.PageSize).
		Find(&rules)

	c.JSON(http.StatusOK, schema.Response{
		Code: 0, Message: "success",
		Data: schema.PageResponse{
			Total: total, Page: req.Page, PageSize: req.PageSize, List: rules,
		},
	})
}

// CreateTagRule 创建打标规则
func CreateTagRule(c *gin.Context) {
	var req struct {
		Name         string   `json:"name" binding:"required"`
		RuleType     string   `json:"rule_type" binding:"required"`
		MatchPattern []string `json:"match_pattern"`
		TargetTagID  uint     `json:"target_tag_id" binding:"required"`
		WeightBonus  float64  `json:"weight_bonus"`
		Status       int      `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误: " + err.Error(), Data: nil})
		return
	}

	// 查询目标标签名（冗余存储）
	var targetTag model.Tag
	targetName := ""
	if err := db.RQ(c).Scopes(db.T(c)).First(&targetTag, req.TargetTagID).Error; err == nil {
		targetName = targetTag.Name
	}

	rule := &model.TagRule{
		Name:          req.Name,
		RuleType:      req.RuleType,
		TargetTagID:   req.TargetTagID,
		TargetTagName: targetName,
		WeightBonus:   req.WeightBonus,
		Status:        req.Status,
	}
	if rule.WeightBonus == 0 {
		rule.WeightBonus = 1.0
	}
	if rule.Status == 0 {
		rule.Status = 1
	}
	rule.SetMatchPatterns(req.MatchPattern)

	if err := db.RQ(c).Create(rule).Error; err != nil {
		c.JSON(http.StatusInternalServerError, schema.Response{Code: 500, Message: "创建失败: " + err.Error(), Data: nil})
		return
	}

	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "创建成功", Data: rule})
}

// UpdateTagRule 更新打标规则
func UpdateTagRule(c *gin.Context) {
	id := c.Param("id")

	var rule model.TagRule
	if err := db.RQ(c).Scopes(db.T(c)).First(&rule, id).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "规则不存在", Data: nil})
		return
	}

	var req struct {
		Name         string   `json:"name"`
		RuleType     string   `json:"rule_type"`
		MatchPattern []string `json:"match_pattern"`
		TargetTagID  uint     `json:"target_tag_id"`
		WeightBonus  float64  `json:"weight_bonus"`
		Status       int      `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误", Data: nil})
		return
	}

	if req.Name != "" {
		rule.Name = req.Name
	}
	if req.RuleType != "" {
		rule.RuleType = req.RuleType
	}
	if len(req.MatchPattern) > 0 {
		rule.SetMatchPatterns(req.MatchPattern)
	}
	if req.TargetTagID != 0 {
		rule.TargetTagID = req.TargetTagID
		// 更新冗余标签名
		var targetTag model.Tag
		if err := db.RQ(c).Scopes(db.T(c)).First(&targetTag, req.TargetTagID).Error; err == nil {
			rule.TargetTagName = targetTag.Name
		}
	}
	if req.WeightBonus != 0 {
		rule.WeightBonus = req.WeightBonus
	}
	if req.Status != 0 {
		rule.Status = req.Status
	}

	db.RQ(c).Save(&rule)

	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "更新成功", Data: rule})
}

// DeleteTagRule 删除打标规则
// 软删除：status设为0，不是物理删除
func DeleteTagRule(c *gin.Context) {
	id := c.Param("id")

	var rule model.TagRule
	if err := db.RQ(c).Scopes(db.T(c)).First(&rule, id).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "规则不存在", Data: nil})
		return
	}

	// 软删除：status设为0
	rule.Status = 0
	db.RQ(c).Save(&rule)

	// 删除后自动热更新缓存
	cache.DefaultTagCache.Reload()

	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "删除成功", Data: nil})
}

// EnableTagRule 启用打标规则
func EnableTagRule(c *gin.Context) {
	id := c.Param("id")

	var rule model.TagRule
	if err := db.RQ(c).Scopes(db.T(c)).First(&rule, id).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "规则不存在", Data: nil})
		return
	}

	rule.Status = 1
	db.RQ(c).Save(&rule)

	// 自动热更新缓存
	cache.DefaultTagCache.Reload()

	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "启用成功", Data: rule})
}

// DisableTagRule 下线打标规则
func DisableTagRule(c *gin.Context) {
	id := c.Param("id")

	var rule model.TagRule
	if err := db.RQ(c).Scopes(db.T(c)).First(&rule, id).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "规则不存在", Data: nil})
		return
	}

	rule.Status = 0
	db.RQ(c).Save(&rule)

	// 自动热更新缓存
	cache.DefaultTagCache.Reload()

	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "下线成功", Data: rule})
}

// ============================================================
// 权重映射 CRUD
// ============================================================

// GetTagWeightList 获取权重映射列表（分页+标签筛选）
func GetTagWeightList(c *gin.Context) {
	var req schema.Pagination
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误", Data: nil})
		return
	}

	query := db.PQ(c).Model(&model.TagWeightMapping{})

	// 按标签筛选
	tagID := c.Query("tag_id")
	if tagID != "" {
		query = query.Where("tag_id = ?", tagID)
	}

	// T向量维度筛选
	tVectorIndex := c.Query("t_vector_index")
	if tVectorIndex != "" {
		query = query.Where("t_vector_index = ?", tVectorIndex)
	}

	var total int64
	query.Count(&total)

	var mappings []model.TagWeightMapping
	query.Order("tag_id ASC, t_vector_index ASC").
		Offset(req.GetOffset()).
		Limit(req.PageSize).
		Find(&mappings)

	c.JSON(http.StatusOK, schema.Response{
		Code: 0, Message: "success",
		Data: schema.PageResponse{
			Total: total, Page: req.Page, PageSize: req.PageSize, List: mappings,
		},
	})
}

// CreateTagWeight 创建权重映射
func CreateTagWeight(c *gin.Context) {
	var req struct {
		TagID        uint    `json:"tag_id" binding:"required"`
		TagCode      string  `json:"tag_code"`
		TVectorIndex int     `json:"t_vector_index" binding:"required"`
		WeightDelta  float64 `json:"weight_delta" binding:"required"`
		Direction    string  `json:"direction"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误: " + err.Error(), Data: nil})
		return
	}

	// 查询标签编码（冗余存储）
	tagCode := req.TagCode
	if tagCode == "" {
		var tag model.Tag
		if err := db.RQ(c).Scopes(db.T(c)).First(&tag, req.TagID).Error; err == nil {
			tagCode = tag.Code
		}
	}

	direction := req.Direction
	if direction == "" {
		direction = "up"
	}

	mapping := &model.TagWeightMapping{
		TagID:        req.TagID,
		TagCode:      tagCode,
		TVectorIndex: req.TVectorIndex,
		WeightDelta:  req.WeightDelta,
		Direction:    direction,
	}

	if err := db.RQ(c).Create(mapping).Error; err != nil {
		c.JSON(http.StatusInternalServerError, schema.Response{Code: 500, Message: "创建失败: " + err.Error(), Data: nil})
		return
	}

	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "创建成功", Data: mapping})
}

// DeleteTagWeight 删除权重映射
// 软删除：status设为0，不是物理删除
func DeleteTagWeight(c *gin.Context) {
	id := c.Param("id")

	var mapping model.TagWeightMapping
	if err := db.RQ(c).Scopes(db.T(c)).First(&mapping, id).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "映射不存在", Data: nil})
		return
	}

	// 软删除：status设为0
	mapping.Status = 0
	db.RQ(c).Save(&mapping)

	// 删除后自动热更新缓存
	cache.DefaultTagCache.Reload()

	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "删除成功", Data: nil})
}

// UpdateTagWeight 编辑权重映射
func UpdateTagWeight(c *gin.Context) {
	id := c.Param("id")

	var mapping model.TagWeightMapping
	if err := db.RQ(c).Scopes(db.T(c)).First(&mapping, id).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "映射不存在", Data: nil})
		return
	}

	var req struct {
		TagID        uint    `json:"tag_id"`
		TagCode      string  `json:"tag_code"`
		TVectorIndex int     `json:"t_vector_index"`
		WeightDelta  float64 `json:"weight_delta"`
		Direction    string  `json:"direction"`
		Status       int     `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误: " + err.Error(), Data: nil})
		return
	}

	// 用map更新，避免零值问题
	updates := make(map[string]interface{})
	if req.TagID != 0 {
		updates["tag_id"] = req.TagID
		// 更新标签编码
		tagCode := req.TagCode
		if tagCode == "" {
			var tag model.Tag
			if err := db.RQ(c).Scopes(db.T(c)).First(&tag, req.TagID).Error; err == nil {
				updates["tag_code"] = tag.Code
			}
		} else {
			updates["tag_code"] = tagCode
		}
	}
	if req.WeightDelta != 0 {
		updates["weight_delta"] = req.WeightDelta
	}
	if req.Direction != "" {
		updates["direction"] = req.Direction
	}
	if req.Status != 0 {
		updates["status"] = req.Status
	}
	if len(updates) > 0 {
		db.RQ(c).Model(&mapping).Updates(updates)
	}

	// 自动热更新缓存
	cache.DefaultTagCache.Reload()

	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "更新成功", Data: mapping})
}

// ============================================================
// 客户端API - 客户标签相关
// ============================================================

// GetCustomerTags 获取客户标签列表
func GetCustomerTags(c *gin.Context) {
	customerID, _ := strconv.Atoi(c.Param("id"))

	tags, err := service.DefaultTagService.GetCustomerTags(uint(customerID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, schema.Response{Code: 500, Message: "查询失败", Data: nil})
		return
	}

	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "success", Data: tags})
}

// AddTagsToCustomer 手动给客户打标签
// 接收tag_ids数组
func AddTagsToCustomer(c *gin.Context) {
	customerID, _ := strconv.Atoi(c.Param("id"))

	var req struct {
		TagIDs []uint `json:"tag_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误: " + err.Error(), Data: nil})
		return
	}

	// 根据tag_ids查标签名称
	var tagNames []string
	for _, tagID := range req.TagIDs {
		tag := cache.DefaultTagCache.GetTagByID(tagID)
		if tag != nil {
			tagNames = append(tagNames, tag.Name)
		}
	}

	err := service.DefaultTagService.ApplyTagsToCustomer(uint(customerID), tagNames, "manual")
	if err != nil {
		c.JSON(http.StatusInternalServerError, schema.Response{Code: 500, Message: "打标失败", Data: nil})
		return
	}

	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "打标成功", Data: tagNames})
}

// RemoveCustomerTag 移除客户标签
func RemoveCustomerTag(c *gin.Context) {
	customerID, _ := strconv.Atoi(c.Param("id"))
	tagID, _ := strconv.Atoi(c.Param("tag_id"))

	err := service.DefaultTagService.RemoveTagFromCustomer(uint(customerID), uint(tagID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, schema.Response{Code: 500, Message: "移除失败", Data: nil})
		return
	}

	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "移除成功", Data: nil})
}
