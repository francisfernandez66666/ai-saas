// 车型品牌知识库API：品牌/车型/竞品/知识片段的CRUD与查询。
package api

// 车型品牌知识库API：管理端品牌/车型/规格/竞品对比/知识片段的CRUD与删除后缓存热更新，
// 客户端品牌/车型/详情/竞品对比/知识搜索（读缓存DefaultKnowledgeCache）。

import (
	"ai-scrm/internal/cache"
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/schema"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 车型品牌知识库API
// 管理端：品牌/车型/规格/竞品/知识片段的CRUD + 热更新
// 客户端：品牌列表、车型列表、车型详情、竞品对比、知识搜索
// ============================================================

// ============================================================
// 管理端 - 品牌 CRUD
// ============================================================

// GetBrandList 获取品牌列表（管理端）
func GetBrandList(c *gin.Context) {
	var req schema.Pagination
	if err := c.ShouldBindQuery(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误")
		return
	}

	query := db.PQ(c).Model(&model.Brand{})

	keyword := c.Query("keyword")
	if keyword != "" {
		keyword = "%" + keyword + "%"
		query = query.Where("name LIKE ? OR code LIKE ?", keyword, keyword)
	}

	status := c.Query("status")
	if status != "" {
		query = query.Where("status = ?", status)
	}

	var total int64
	query.Count(&total)

	var brands []model.Brand
	query.Order("sort ASC, id ASC").
		Offset(req.GetOffset()).
		Limit(req.PageSize).
		Find(&brands)

	RespOK(c, "success", schema.PageResponse{
		Total: total, Page: req.Page, PageSize: req.PageSize, List: brands,
	})
}

// GetBrandDetail 获取品牌详情
func GetBrandDetail(c *gin.Context) {
	id := c.Param("id")

	var brand model.Brand
	if err := db.PQ(c).Scopes(db.T(c)).First(&brand, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "品牌不存在")
		return
	}

	RespOK(c, "success", brand)
}

// CreateBrand 创建品牌
func CreateBrand(c *gin.Context) {
	var req struct {
		Name        string `json:"name" binding:"required"`
		Code        string `json:"code" binding:"required"`
		Logo        string `json:"logo"`
		Description string `json:"description"`
		Country     string `json:"country"`
		FoundedYear int    `json:"founded_year"`
		Status      int    `json:"status"`
		Sort        int    `json:"sort"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误: "+err.Error())
		return
	}

	brand := &model.Brand{
		Name:        req.Name,
		Code:        req.Code,
		Logo:        req.Logo,
		Description: req.Description,
		Country:     req.Country,
		FoundedYear: req.FoundedYear,
		Status:      req.Status,
		Sort:        req.Sort,
	}
	if brand.Status == 0 {
		brand.Status = 1
	}

	if err := db.RQ(c).Create(brand).Error; err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "创建失败: "+err.Error())
		return
	}

	RespOK(c, "创建成功", brand)
}

// UpdateBrand 更新品牌
func UpdateBrand(c *gin.Context) {
	id := c.Param("id")

	var brand model.Brand
	if err := db.RQ(c).Scopes(db.T(c)).First(&brand, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "品牌不存在")
		return
	}

	var req struct {
		Name        string `json:"name"`
		Logo        string `json:"logo"`
		Description string `json:"description"`
		Country     string `json:"country"`
		FoundedYear int    `json:"founded_year"`
		Status      int    `json:"status"`
		Sort        int    `json:"sort"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误")
		return
	}

	if req.Name != "" {
		brand.Name = req.Name
	}
	if req.Logo != "" {
		brand.Logo = req.Logo
	}
	if req.Description != "" {
		brand.Description = req.Description
	}
	if req.Country != "" {
		brand.Country = req.Country
	}
	if req.FoundedYear != 0 {
		brand.FoundedYear = req.FoundedYear
	}
	if req.Status != 0 {
		brand.Status = req.Status
	}
	if req.Sort != 0 {
		brand.Sort = req.Sort
	}

	db.RQ(c).Save(&brand)

	RespOK(c, "更新成功", brand)
}

// DeleteBrand 删除品牌
// 软删除：status设为0，不是物理删除
func DeleteBrand(c *gin.Context) {
	id := c.Param("id")

	var brand model.Brand
	if err := db.RQ(c).Scopes(db.T(c)).First(&brand, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "品牌不存在")
		return
	}

	brand.Status = 0
	db.RQ(c).Save(&brand)

	// 删除后自动热更新缓存
	cache.DefaultKnowledgeCache.Reload()

	RespOK(c, "删除成功", nil)
}

// EnableBrand 启用品牌
func EnableBrand(c *gin.Context) {
	id := c.Param("id")

	var brand model.Brand
	if err := db.RQ(c).Scopes(db.T(c)).First(&brand, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "品牌不存在")
		return
	}

	brand.Status = 1
	db.RQ(c).Save(&brand)

	// 自动热更新缓存
	cache.DefaultKnowledgeCache.Reload()

	RespOK(c, "启用成功", brand)
}

// DisableBrand 下线品牌
func DisableBrand(c *gin.Context) {
	id := c.Param("id")

	var brand model.Brand
	if err := db.RQ(c).Scopes(db.T(c)).First(&brand, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "品牌不存在")
		return
	}

	brand.Status = 0
	db.RQ(c).Save(&brand)

	// 自动热更新缓存
	cache.DefaultKnowledgeCache.Reload()

	RespOK(c, "下线成功", brand)
}

// ============================================================
// 管理端 - 车型 CRUD
// ============================================================

// GetModelList 获取车型列表（管理端）
func GetModelList(c *gin.Context) {
	var req schema.Pagination
	if err := c.ShouldBindQuery(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误")
		return
	}

	query := db.PQ(c).Model(&model.CarModel{})

	brandID := c.Query("brand_id")
	if brandID != "" {
		query = query.Where("brand_id = ?", brandID)
	}

	keyword := c.Query("keyword")
	if keyword != "" {
		keyword = "%" + keyword + "%"
		query = query.Where("name LIKE ? OR code LIKE ?", keyword, keyword)
	}

	var total int64
	query.Count(&total)

	var models []model.CarModel
	query.Order("sort ASC, id ASC").
		Offset(req.GetOffset()).
		Limit(req.PageSize).
		Find(&models)

	RespOK(c, "success", schema.PageResponse{
		Total: total, Page: req.Page, PageSize: req.PageSize, List: models,
	})
}

// GetModelDetail 获取车型详情
func GetModelDetail(c *gin.Context) {
	id := c.Param("id")

	var carModel model.CarModel
	if err := db.PQ(c).Scopes(db.T(c)).First(&carModel, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "车型不存在")
		return
	}

	RespOK(c, "success", carModel)
}

// CreateModel 创建车型
func CreateModel(c *gin.Context) {
	var req struct {
		BrandID    uint   `json:"brand_id" binding:"required"`
		Name       string `json:"name" binding:"required"`
		Code       string `json:"code" binding:"required"`
		PriceRange string `json:"price_range"`
		Level      string `json:"level"`
		BodyType   string `json:"body_type"`
		FuelType   string `json:"fuel_type"`
		Status     int    `json:"status"`
		Sort       int    `json:"sort"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误: "+err.Error())
		return
	}

	// 查询品牌名（冗余存储）
	var brand model.Brand
	brandName := ""
	if err := db.RQ(c).Scopes(db.T(c)).First(&brand, req.BrandID).Error; err == nil {
		brandName = brand.Name
	}

	carModel := &model.CarModel{
		BrandID:    req.BrandID,
		BrandName:  brandName,
		Name:       req.Name,
		Code:       req.Code,
		PriceRange: req.PriceRange,
		Level:      req.Level,
		BodyType:   req.BodyType,
		FuelType:   req.FuelType,
		Status:     req.Status,
		Sort:       req.Sort,
	}
	if carModel.Status == 0 {
		carModel.Status = 1
	}

	if err := db.RQ(c).Create(carModel).Error; err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "创建失败: "+err.Error())
		return
	}

	RespOK(c, "创建成功", carModel)
}

// UpdateModel 更新车型
func UpdateModel(c *gin.Context) {
	id := c.Param("id")

	var carModel model.CarModel
	if err := db.RQ(c).Scopes(db.T(c)).First(&carModel, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "车型不存在")
		return
	}

	var req struct {
		Name       string `json:"name"`
		PriceRange string `json:"price_range"`
		Level      string `json:"level"`
		BodyType   string `json:"body_type"`
		FuelType   string `json:"fuel_type"`
		Status     int    `json:"status"`
		Sort       int    `json:"sort"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误")
		return
	}

	if req.Name != "" {
		carModel.Name = req.Name
	}
	if req.PriceRange != "" {
		carModel.PriceRange = req.PriceRange
	}
	if req.Level != "" {
		carModel.Level = req.Level
	}
	if req.BodyType != "" {
		carModel.BodyType = req.BodyType
	}
	if req.FuelType != "" {
		carModel.FuelType = req.FuelType
	}
	if req.Status != 0 {
		carModel.Status = req.Status
	}
	if req.Sort != 0 {
		carModel.Sort = req.Sort
	}

	db.RQ(c).Save(&carModel)

	RespOK(c, "更新成功", carModel)
}

// DeleteModel 删除车型
// 软删除：status设为0，不是物理删除
func DeleteModel(c *gin.Context) {
	id := c.Param("id")

	var carModel model.CarModel
	if err := db.RQ(c).Scopes(db.T(c)).First(&carModel, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "车型不存在")
		return
	}

	carModel.Status = 0
	db.RQ(c).Save(&carModel)

	// 删除后自动热更新缓存
	cache.DefaultKnowledgeCache.Reload()

	RespOK(c, "删除成功", nil)
}

// EnableModel 启用车型
func EnableModel(c *gin.Context) {
	id := c.Param("id")

	var carModel model.CarModel
	if err := db.RQ(c).Scopes(db.T(c)).First(&carModel, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "车型不存在")
		return
	}

	carModel.Status = 1
	db.RQ(c).Save(&carModel)

	// 自动热更新缓存
	cache.DefaultKnowledgeCache.Reload()

	RespOK(c, "启用成功", carModel)
}

// DisableModel 下线车型
func DisableModel(c *gin.Context) {
	id := c.Param("id")

	var carModel model.CarModel
	if err := db.RQ(c).Scopes(db.T(c)).First(&carModel, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "车型不存在")
		return
	}

	carModel.Status = 0
	db.RQ(c).Save(&carModel)

	// 自动热更新缓存
	cache.DefaultKnowledgeCache.Reload()

	RespOK(c, "下线成功", carModel)
}

// ============================================================
// 管理端 - 规格参数 CRUD
// ============================================================

// GetSpecList 获取规格参数列表（分页+车型筛选+分类筛选+关键词搜索+状态筛选）
func GetSpecList(c *gin.Context) {
	var req schema.Pagination
	if err := c.ShouldBindQuery(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误")
		return
	}

	query := db.PQ(c).Model(&model.ModelSpec{})

	modelID := c.Query("model_id")
	if modelID != "" {
		query = query.Where("model_id = ?", modelID)
	}

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
		query = query.Where("param_name LIKE ? OR param_value LIKE ?", keyword, keyword)
	}

	var total int64
	query.Count(&total)

	var specs []model.ModelSpec
	query.Order("model_id ASC, category ASC, sort ASC, id ASC").
		Offset(req.GetOffset()).
		Limit(req.PageSize).
		Find(&specs)

	RespOK(c, "success", schema.PageResponse{
		Total: total, Page: req.Page, PageSize: req.PageSize, List: specs,
	})
}

// UpdateSpec 更新规格参数
func UpdateSpec(c *gin.Context) {
	id := c.Param("id")

	var spec model.ModelSpec
	if err := db.RQ(c).Scopes(db.T(c)).First(&spec, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "参数不存在")
		return
	}

	var req struct {
		ParamName  string `json:"param_name"`
		ParamValue string `json:"param_value"`
		ParamUnit  string `json:"param_unit"`
		Category   string `json:"category"`
		IsPrice    *bool  `json:"is_price"` // 用指针区分"未传"和"传了false"
		Sort       int    `json:"sort"`
		Status     int    `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误: "+err.Error())
		return
	}

	updates := make(map[string]interface{})
	if req.ParamName != "" {
		updates["param_name"] = req.ParamName
	}
	if req.ParamValue != "" {
		updates["param_value"] = req.ParamValue
	}
	if req.ParamUnit != "" {
		updates["param_unit"] = req.ParamUnit
	}
	if req.Category != "" {
		updates["category"] = req.Category
	}
	if req.IsPrice != nil {
		updates["is_price"] = *req.IsPrice
	}
	if req.Sort != 0 {
		updates["sort"] = req.Sort
	}
	if req.Status != 0 {
		updates["status"] = req.Status
	}
	if len(updates) > 0 {
		db.RQ(c).Model(&spec).Updates(updates)
	}

	// 自动热更新缓存
	cache.DefaultKnowledgeCache.Reload()

	RespOK(c, "更新成功", spec)
}

// EnableSpec 启用规格参数
func EnableSpec(c *gin.Context) {
	id := c.Param("id")

	var spec model.ModelSpec
	if err := db.RQ(c).Scopes(db.T(c)).First(&spec, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "参数不存在")
		return
	}

	spec.Status = 1
	db.RQ(c).Save(&spec)

	// 自动热更新缓存
	cache.DefaultKnowledgeCache.Reload()

	RespOK(c, "启用成功", spec)
}

// DisableSpec 下线规格参数
func DisableSpec(c *gin.Context) {
	id := c.Param("id")

	var spec model.ModelSpec
	if err := db.RQ(c).Scopes(db.T(c)).First(&spec, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "参数不存在")
		return
	}

	spec.Status = 0
	db.RQ(c).Save(&spec)

	// 自动热更新缓存
	cache.DefaultKnowledgeCache.Reload()

	RespOK(c, "下线成功", spec)
}

// CreateSpec 创建规格参数
func CreateSpec(c *gin.Context) {
	var req struct {
		ModelID    uint   `json:"model_id" binding:"required"`
		ParamName  string `json:"param_name" binding:"required"`
		ParamValue string `json:"param_value"`
		ParamUnit  string `json:"param_unit"`
		Category   string `json:"category"`
		Sort       int    `json:"sort"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误: "+err.Error())
		return
	}

	spec := &model.ModelSpec{
		ModelID:    req.ModelID,
		ParamName:  req.ParamName,
		ParamValue: req.ParamValue,
		ParamUnit:  req.ParamUnit,
		Category:   req.Category,
		Sort:       req.Sort,
	}

	if err := db.RQ(c).Create(spec).Error; err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "创建失败: "+err.Error())
		return
	}

	RespOK(c, "创建成功", spec)
}

// DeleteSpec 删除规格参数
// 软删除：status设为0，不是物理删除
func DeleteSpec(c *gin.Context) {
	id := c.Param("id")

	var spec model.ModelSpec
	if err := db.RQ(c).Scopes(db.T(c)).First(&spec, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "参数不存在")
		return
	}

	// 软删除：status设为0
	spec.Status = 0
	db.RQ(c).Save(&spec)

	// 删除后自动热更新缓存
	cache.DefaultKnowledgeCache.Reload()

	RespOK(c, "删除成功", nil)
}

// ============================================================
// 管理端 - 竞品对比 CRUD
// ============================================================

// GetCompareList 获取竞品对比列表（分页+车型筛选+竞品筛选+状态筛选）
func GetCompareList(c *gin.Context) {
	var req schema.Pagination
	if err := c.ShouldBindQuery(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误")
		return
	}

	query := db.PQ(c).Model(&model.CompetitorCompare{})

	modelID := c.Query("model_id")
	if modelID != "" {
		query = query.Where("our_model_id = ?", modelID)
	}

	competitorBrand := c.Query("competitor_brand")
	if competitorBrand != "" {
		query = query.Where("competitor_brand LIKE ?", "%"+competitorBrand+"%")
	}

	// 状态筛选
	status := c.Query("status")
	if status != "" {
		query = query.Where("status = ?", status)
	}

	// 价格信息筛选
	containsPrice := c.Query("contains_price")
	if containsPrice != "" {
		query = query.Where("contains_price = ?", containsPrice == "true" || containsPrice == "1")
	}

	var total int64
	query.Count(&total)

	var compares []model.CompetitorCompare
	query.Order("our_model_id ASC, id ASC").
		Offset(req.GetOffset()).
		Limit(req.PageSize).
		Find(&compares)

	RespOK(c, "success", schema.PageResponse{
		Total: total, Page: req.Page, PageSize: req.PageSize, List: compares,
	})
}

// CreateCompare 创建竞品对比
func CreateCompare(c *gin.Context) {
	var req struct {
		OurModelID      uint                `json:"our_model_id" binding:"required"`
		CompetitorBrand string              `json:"competitor_brand"`
		CompetitorModel string              `json:"competitor_model"`
		CompareType     string              `json:"compare_type"`
		ContainsPrice   bool                `json:"contains_price"` // 是否含价格信息
		Items           []model.CompareItem `json:"items"`
		Status          int                 `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误: "+err.Error())
		return
	}

	compare := &model.CompetitorCompare{
		OurModelID:      req.OurModelID,
		CompetitorBrand: req.CompetitorBrand,
		CompetitorModel: req.CompetitorModel,
		CompareType:     req.CompareType,
		ContainsPrice:   req.ContainsPrice,
		Status:          req.Status,
	}
	if compare.Status == 0 {
		compare.Status = 1
	}
	compare.SetCompareItems(req.Items)

	if err := db.RQ(c).Create(compare).Error; err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "创建失败: "+err.Error())
		return
	}

	RespOK(c, "创建成功", compare)
}

// UpdateCompare 更新竞品对比
func UpdateCompare(c *gin.Context) {
	id := c.Param("id")

	var compare model.CompetitorCompare
	if err := db.RQ(c).Scopes(db.T(c)).First(&compare, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "对比不存在")
		return
	}

	var req struct {
		CompetitorBrand string              `json:"competitor_brand"`
		CompetitorModel string              `json:"competitor_model"`
		CompareType     string              `json:"compare_type"`
		ContainsPrice   *bool               `json:"contains_price"` // 指针区分未传和false
		Items           []model.CompareItem `json:"items"`
		Status          int                 `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误")
		return
	}

	if req.CompetitorBrand != "" {
		compare.CompetitorBrand = req.CompetitorBrand
	}
	if req.CompetitorModel != "" {
		compare.CompetitorModel = req.CompetitorModel
	}
	if req.CompareType != "" {
		compare.CompareType = req.CompareType
	}
	if req.ContainsPrice != nil {
		compare.ContainsPrice = *req.ContainsPrice
	}
	if len(req.Items) > 0 {
		compare.SetCompareItems(req.Items)
	}
	if req.Status != 0 {
		compare.Status = req.Status
	}

	db.RQ(c).Save(&compare)

	// 自动热更新缓存
	cache.DefaultKnowledgeCache.Reload()

	RespOK(c, "更新成功", compare)
}

// DeleteCompare 删除竞品对比
// 软删除：status设为0，不是物理删除
func DeleteCompare(c *gin.Context) {
	id := c.Param("id")

	var compare model.CompetitorCompare
	if err := db.RQ(c).Scopes(db.T(c)).First(&compare, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "对比不存在")
		return
	}

	// 软删除：status设为0
	compare.Status = 0
	db.RQ(c).Save(&compare)

	// 删除后自动热更新缓存
	cache.DefaultKnowledgeCache.Reload()

	RespOK(c, "删除成功", nil)
}

// EnableCompare 启用竞品对比
func EnableCompare(c *gin.Context) {
	id := c.Param("id")

	var compare model.CompetitorCompare
	if err := db.RQ(c).Scopes(db.T(c)).First(&compare, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "对比不存在")
		return
	}

	compare.Status = 1
	db.RQ(c).Save(&compare)

	// 自动热更新缓存
	cache.DefaultKnowledgeCache.Reload()

	RespOK(c, "启用成功", compare)
}

// DisableCompare 下线竞品对比
func DisableCompare(c *gin.Context) {
	id := c.Param("id")

	var compare model.CompetitorCompare
	if err := db.RQ(c).Scopes(db.T(c)).First(&compare, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "对比不存在")
		return
	}

	compare.Status = 0
	db.RQ(c).Save(&compare)

	// 自动热更新缓存
	cache.DefaultKnowledgeCache.Reload()

	RespOK(c, "下线成功", compare)
}

// ============================================================
// 管理端 - 知识片段 CRUD
// ============================================================

// GetFragmentList 获取知识片段列表（分页+分类筛选+标签筛选+关键词搜索+状态筛选）
func GetFragmentList(c *gin.Context) {
	var req schema.Pagination
	if err := c.ShouldBindQuery(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误")
		return
	}

	query := db.PQ(c).Model(&model.KnowledgeFragment{})

	category := c.Query("category")
	if category != "" {
		query = query.Where("category = ?", category)
	}

	// 状态筛选
	status := c.Query("status")
	if status != "" {
		query = query.Where("status = ?", status)
	}

	keyword := c.Query("keyword")
	if keyword != "" {
		keyword = "%" + keyword + "%"
		query = query.Where("title LIKE ? OR content LIKE ?", keyword, keyword)
	}

	var total int64
	query.Count(&total)

	var fragments []model.KnowledgeFragment
	query.Order("sort ASC, id DESC").
		Offset(req.GetOffset()).
		Limit(req.PageSize).
		Find(&fragments)

	RespOK(c, "success", schema.PageResponse{
		Total: total, Page: req.Page, PageSize: req.PageSize, List: fragments,
	})
}

// GetFragmentDetail 获取知识片段详情
func GetFragmentDetail(c *gin.Context) {
	id := c.Param("id")

	var fragment model.KnowledgeFragment
	if err := db.PQ(c).Scopes(db.T(c)).First(&fragment, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "知识片段不存在")
		return
	}

	RespOK(c, "success", fragment)
}

// CreateFragment 创建知识片段
func CreateFragment(c *gin.Context) {
	var req struct {
		Category         string   `json:"category"`
		Title            string   `json:"title" binding:"required"`
		Content          string   `json:"content"`
		Tags             []string `json:"tags"`
		ApplicableModels []string `json:"applicable_models"`
		Status           int      `json:"status"`
		Sort             int      `json:"sort"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误: "+err.Error())
		return
	}

	fragment := &model.KnowledgeFragment{
		Category: req.Category,
		Title:    req.Title,
		Content:  req.Content,
		Status:   req.Status,
		Sort:     req.Sort,
	}
	if fragment.Status == 0 {
		fragment.Status = 1
	}
	fragment.SetTags(req.Tags)
	fragment.SetApplicableModels(req.ApplicableModels)

	if err := db.RQ(c).Create(fragment).Error; err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "创建失败: "+err.Error())
		return
	}

	RespOK(c, "创建成功", fragment)
}

// UpdateFragment 更新知识片段
func UpdateFragment(c *gin.Context) {
	id := c.Param("id")

	var fragment model.KnowledgeFragment
	if err := db.RQ(c).Scopes(db.T(c)).First(&fragment, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "知识片段不存在")
		return
	}

	var req struct {
		Category         string   `json:"category"`
		Title            string   `json:"title"`
		Content          string   `json:"content"`
		Tags             []string `json:"tags"`
		ApplicableModels []string `json:"applicable_models"`
		Status           int      `json:"status"`
		Sort             int      `json:"sort"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误")
		return
	}

	if req.Category != "" {
		fragment.Category = req.Category
	}
	if req.Title != "" {
		fragment.Title = req.Title
	}
	if req.Content != "" {
		fragment.Content = req.Content
	}
	if len(req.Tags) > 0 {
		fragment.SetTags(req.Tags)
	}
	if len(req.ApplicableModels) > 0 {
		fragment.SetApplicableModels(req.ApplicableModels)
	}
	if req.Status != 0 {
		fragment.Status = req.Status
	}
	if req.Sort != 0 {
		fragment.Sort = req.Sort
	}

	db.RQ(c).Save(&fragment)

	RespOK(c, "更新成功", fragment)
}

// DeleteFragment 删除知识片段
// 软删除：status设为0，不是物理删除
func DeleteFragment(c *gin.Context) {
	id := c.Param("id")

	var fragment model.KnowledgeFragment
	if err := db.RQ(c).Scopes(db.T(c)).First(&fragment, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "知识片段不存在")
		return
	}

	fragment.Status = 0
	db.RQ(c).Save(&fragment)

	// 删除后自动热更新缓存
	cache.DefaultKnowledgeCache.Reload()

	RespOK(c, "删除成功", nil)
}

// EnableFragment 启用知识片段
func EnableFragment(c *gin.Context) {
	id := c.Param("id")

	var fragment model.KnowledgeFragment
	if err := db.RQ(c).Scopes(db.T(c)).First(&fragment, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "知识片段不存在")
		return
	}

	fragment.Status = 1
	db.RQ(c).Save(&fragment)

	// 自动热更新缓存
	cache.DefaultKnowledgeCache.Reload()

	RespOK(c, "启用成功", fragment)
}

// DisableFragment 下线知识片段
func DisableFragment(c *gin.Context) {
	id := c.Param("id")

	var fragment model.KnowledgeFragment
	if err := db.RQ(c).Scopes(db.T(c)).First(&fragment, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "知识片段不存在")
		return
	}

	fragment.Status = 0
	db.RQ(c).Save(&fragment)

	// 自动热更新缓存
	cache.DefaultKnowledgeCache.Reload()

	RespOK(c, "下线成功", fragment)
}

// ReloadKnowledgeCache 热更新知识库缓存
func ReloadKnowledgeCache(c *gin.Context) {
	cache.DefaultKnowledgeCache.Reload()

	RespOK(c, "知识库缓存热更新成功", gin.H{
		"version": cache.DefaultKnowledgeCache.GetVersion(),
	})
}

// ============================================================
// 客户端 - 品牌/车型查询
// ============================================================

// GetPublicBrands 获取品牌列表（客户端）
func GetPublicBrands(c *gin.Context) {
	brands := cache.DefaultKnowledgeCache.GetAllBrands(db.EffectiveTenantIDFromGin(c))
	RespOK(c, "success", brands)
}

// GetPublicModels 获取车型列表（客户端）
func GetPublicModels(c *gin.Context) {
	brandIDStr := c.Query("brand_id")
	tid := db.EffectiveTenantIDFromGin(c)

	if brandIDStr != "" {
		brandID, _ := strconv.Atoi(brandIDStr)
		models := cache.DefaultKnowledgeCache.GetModelsByBrandID(tid, uint(brandID))
		RespOK(c, "success", models)
		return
	}

	models := cache.DefaultKnowledgeCache.GetAllModels(tid)
	RespOK(c, "success", models)
}

// GetPublicModelDetail 获取车型详情（含规格参数）
func GetPublicModelDetail(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	tid := db.EffectiveTenantIDFromGin(c)

	carModel := cache.DefaultKnowledgeCache.GetModelByID(tid, uint(id))
	if carModel == nil {
		RespErr(c, http.StatusNotFound, 404, "车型不存在")
		return
	}

	// 获取规格参数
	specs := cache.DefaultKnowledgeCache.GetSpecsByModelID(tid, uint(id))

	// 按分类组织规格参数
	specByCategory := make(map[string][]model.ModelSpec)
	for _, spec := range specs {
		specByCategory[spec.Category] = append(specByCategory[spec.Category], spec)
	}

	RespOK(c, "success", gin.H{
		"model":            carModel,
		"specs":            specs,
		"spec_by_category": specByCategory,
	})
}

// GetPublicCompares 获取竞品对比（客户端）
func GetPublicCompares(c *gin.Context) {
	modelIDStr := c.Query("model_id")
	if modelIDStr == "" {
		RespErr(c, http.StatusBadRequest, 400, "model_id必填")
		return
	}

	modelID, _ := strconv.Atoi(modelIDStr)
	tid := db.EffectiveTenantIDFromGin(c)
	compares := cache.DefaultKnowledgeCache.GetComparesByModelID(tid, uint(modelID))

	RespOK(c, "success", compares)
}

// SearchFragments 搜索知识片段（客户端，租户隔离）
func SearchFragments(c *gin.Context) {
	keyword := c.Query("keyword")
	category := c.Query("category")
	tagsStr := c.Query("tags")

	var tags []string
	if tagsStr != "" {
		tags = strings.Split(tagsStr, ",")
	}

	tid := db.EffectiveTenantIDFromGin(c)
	fragments := cache.DefaultKnowledgeCache.SearchFragments(tid, keyword, category, tags)

	RespOK(c, "success", fragments)
}
