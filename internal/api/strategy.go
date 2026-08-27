package api

// 策略中心管理API：话术模板的列表/详情/增删改、卖点列表、锚类型统计与策略测试(StrategyTest)。
// 模板变更后调用strategy.DefaultEngine.ReloadData()热加载；测试接口按租户/部门链召回模板与卖点。

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/engine/strategy"
	"ai-scrm/internal/model"
	"ai-scrm/internal/schema"
	"ai-scrm/internal/service"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 策略中心管理API
// 话术模板管理、策略测试、卖点管理
// ============================================================

// GetTemplateList 获取话术模板列表
func GetTemplateList(c *gin.Context) {
	var req schema.TemplateListRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误", Data: nil})
		return
	}

	query := db.PQ(c).Model(&model.Template{})

	if req.AnchorType != 0 {
		query = query.Where("anchor_type = ?", req.AnchorType)
	}
	if req.Category != "" {
		query = query.Where("category = ?", req.Category)
	}
	if req.Status != 0 {
		query = query.Where("status = ?", req.Status)
	}
	if req.Keyword != "" {
		keyword := "%" + req.Keyword + "%"
		query = query.Where("name LIKE ? OR prompt_template LIKE ?", keyword, keyword)
	}

	var total int64
	query.Count(&total)

	var templates []model.Template
	query.Order("anchor_type ASC, priority DESC, id DESC").
		Offset(req.GetOffset()).
		Limit(req.PageSize).
		Find(&templates)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data: schema.PageResponse{
			Total:    total,
			Page:     req.Page,
			PageSize: req.PageSize,
			List:     templates,
		},
	})
}

// GetTemplate 获取话术模板详情
func GetTemplate(c *gin.Context) {
	id := c.Param("id")

	var template model.Template
	result := db.PQ(c).First(&template, id)
	if result.Error != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "模板不存在", Data: nil})
		return
	}

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data:    template,
	})
}

// CreateTemplate 创建话术模板
func CreateTemplate(c *gin.Context) {
	var req schema.CreateTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误: " + err.Error(), Data: nil})
		return
	}

	template := &model.Template{
		ID:             req.ID,
		AnchorType:     req.AnchorType,
		SubType:        req.SubType,
		Name:           req.Name,
		Category:       req.Category,
		MinIntent:      req.MinIntent,
		MaxIntent:      req.MaxIntent,
		PromptTemplate: req.PromptTemplate,
		HookTemplate:   req.HookTemplate,
		Priority:       req.Priority,
		Status:         req.Status,
	}

	// JSON字段
	if len(req.TriggerTags) > 0 {
		template.TriggerTags = toJSON(req.TriggerTags)
	}
	if len(req.RequiredTags) > 0 {
		template.RequiredTags = toJSON(req.RequiredTags)
	}
	if len(req.ApplicableModels) > 0 {
		template.ApplicableModels = toJSON(req.ApplicableModels)
	}
	if len(req.HookFields) > 0 {
		template.HookFields = toJSON(req.HookFields)
	}
	if len(req.RequiredFeatures) > 0 {
		template.RequiredFeatures = toJSON(req.RequiredFeatures)
	}

	result := db.RQ(c).Create(template)
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, schema.Response{Code: 500, Message: "创建失败", Data: nil})
		return
	}

	// 重新加载策略引擎数据
	strategy.DefaultEngine.ReloadData()

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "创建成功",
		Data:    template,
	})
}

// UpdateTemplate 更新话术模板
func UpdateTemplate(c *gin.Context) {
	id := c.Param("id")

	var template model.Template
	result := db.RQ(c).First(&template, id)
	if result.Error != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "模板不存在", Data: nil})
		return
	}

	var req schema.CreateTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误", Data: nil})
		return
	}

	template.AnchorType = req.AnchorType
	template.SubType = req.SubType
	template.Name = req.Name
	template.Category = req.Category
	template.MinIntent = req.MinIntent
	template.MaxIntent = req.MaxIntent
	template.PromptTemplate = req.PromptTemplate
	template.HookTemplate = req.HookTemplate
	template.Priority = req.Priority
	template.Status = req.Status

	if len(req.TriggerTags) > 0 {
		template.TriggerTags = toJSON(req.TriggerTags)
	}
	if len(req.RequiredTags) > 0 {
		template.RequiredTags = toJSON(req.RequiredTags)
	}
	if len(req.ApplicableModels) > 0 {
		template.ApplicableModels = toJSON(req.ApplicableModels)
	}
	if len(req.HookFields) > 0 {
		template.HookFields = toJSON(req.HookFields)
	}
	if len(req.RequiredFeatures) > 0 {
		template.RequiredFeatures = toJSON(req.RequiredFeatures)
	}

	db.RQ(c).Save(&template)

	// 重新加载策略引擎数据
	strategy.DefaultEngine.ReloadData()

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "更新成功",
		Data:    template,
	})
}

// DeleteTemplate 删除话术模板
// 修复（2026-08-25）：原写法 Delete(&model.Template{}, id) 对字符串主键会把 id
// 当裸 SQL 条件拼进语句（生成 `WHERE tenant_id=? AND tpl_xxx` → 42703 列不存在）。
// 字符串主键必须显式 Where 条件删除。org.go 部门删除为数值主键暂不受影响，另行核查。
func DeleteTemplate(c *gin.Context) {
	id := c.Param("id")

	result := db.RQ(c).Where("id = ?", id).Delete(&model.Template{})
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, schema.Response{Code: 500, Message: "删除失败", Data: nil})
		return
	}

	strategy.DefaultEngine.ReloadData()

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "删除成功",
		Data:    nil,
	})
}

// StrategyTest 策略测试接口
// 输入客户信息和会话状态，测试策略决策结果
// 支持传参数覆盖T向量和S状态，方便测试不同场景
func StrategyTest(c *gin.Context) {
	var req schema.StrategyTestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误: " + err.Error(), Data: nil})
		return
	}

	// 获取客户
	var customer model.Customer
	result := db.RQ(c).First(&customer, req.CustomerID)
	if result.Error != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "客户不存在", Data: nil})
		return
	}

	// 获取T向量（从客户数据）
	tVector := customer.GetTVector()

	// ---- 用请求参数覆盖T向量（传了就覆盖，没传用默认）----
	if req.IntentScore != nil {
		tVector[0] = *req.IntentScore // T[0] 意向分
	}
	if req.PriceSensitivity != nil {
		tVector[1] = *req.PriceSensitivity // T[1] 价格敏感度
	}
	if req.TrustLevel != nil {
		tVector[6] = *req.TrustLevel // T[6] 信任度
	}
	// 抗性类型：优先用请求参数，否则从消息文本里识别
	if req.ResistanceType != nil {
		tVector[14] = float64(*req.ResistanceType)
	} else if req.CustomerInput != "" {
		// 从客户消息中识别抗性类型（简单关键词匹配）
		detected := detectResistanceFromText(req.CustomerInput)
		if detected > 0 {
			tVector[14] = float64(detected)
		}
	}

	// 获取或创建会话状态
	var state model.SessionState
	if req.ConversationID > 0 {
		var conv model.Conversation
		db.RQ(c).First(&conv, req.ConversationID)
		state = conv.GetState()
	} else {
		state = model.SessionState{
			Attempts:         1, // 默认1次，避免首次对话永远触发同类锚加成
			HookCount:        0,
			HookRate:         0,
			Emotion:          "neutral",
			HighIntentRounds: 0,
			CurrentStage:     0,
			SilentDuration:   0,
		}
	}

	// ---- 用请求参数覆盖S状态 ----
	if req.Attempts != nil {
		state.Attempts = *req.Attempts
	}
	if req.HookRate != nil {
		state.HookRate = *req.HookRate
		state.HookCount = int(*req.HookRate * float64(state.Attempts))
	}
	if req.CurrentStage != nil {
		state.CurrentStage = *req.CurrentStage
	}
	if req.HighIntentRounds != nil {
		state.HighIntentRounds = *req.HighIntentRounds
	}
	if req.Emotion != nil {
		state.Emotion = *req.Emotion
	}
	if req.SilentDuration != nil {
		state.SilentDuration = *req.SilentDuration
	}

	// 调用策略引擎
	customerTags := customer.GetTags()

	input := strategy.StrategyInput{
		TVector:        tVector,
		State:          state,
		CustomerInput:  req.CustomerInput,
		CustomerTags:   customerTags,
		CustomerID:     customer.ID,
		ConversationID: req.ConversationID,
		TenantID:       customer.TenantID,                          // M1租户隔离修复：模板/卖点召回按此过滤
		DeptIDs:        service.DeptChainForUser(currentUserID(c)), // 三级包：当前用户部门链
	}

	output := strategy.DefaultEngine.Infer(input)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data:    output,
	})
}

// detectResistanceFromText 从客户消息文本中识别抗性类型
// 简单关键词匹配，后续可升级为AI分类
// 返回：0=无 1=价格 2=规格 3=服务 4=品牌
func detectResistanceFromText(text string) int {
	text = strings.ToLower(text)

	// 价格抗性关键词
	priceKeywords := []string{"贵", "太贵", "价格高", "便宜点", "优惠", "降价", "贵了", "价格", "不值", "性价比", "贵了点", "超出预算", "预算不够", "price", "expensive", "too much"}
	for _, kw := range priceKeywords {
		if strings.Contains(text, kw) {
			return 1
		}
	}

	// 规格抗性关键词
	specKeywords := []string{"动力不够", "配置低", "配置不行", "马力", "续航", "空间小", "太小", "动力弱", "参数", "配置"}
	for _, kw := range specKeywords {
		if strings.Contains(text, kw) {
			return 2
		}
	}

	// 服务抗性关键词
	serviceKeywords := []string{"售后", "服务差", "保养", "维修", "取送车", "服务不好", "质保"}
	for _, kw := range serviceKeywords {
		if strings.Contains(text, kw) {
			return 3
		}
	}

	// 品牌抗性关键词
	brandKeywords := []string{"品牌", "没听过", "杂牌", "不如", "品牌力", "知名度"}
	for _, kw := range brandKeywords {
		if strings.Contains(text, kw) {
			return 4
		}
	}

	return 0
}

// GetFeatureList 获取卖点列表
func GetFeatureList(c *gin.Context) {
	var features []model.Feature
	db.PQ(c).Order("category, priority DESC").Find(&features)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data:    features,
	})
}

// GetAnchorStats 获取锚类型统计
func GetAnchorStats(c *gin.Context) {
	// 从消息表统计各锚类型使用情况
	type anchorStat struct {
		AnchorType int   `json:"anchor_type"`
		Count      int64 `json:"count"`
	}

	var stats []anchorStat
	db.PQ(c).Model(&model.Message{}).
		Where("sender_type = ? AND anchor_type > 0", "ai").
		Select("anchor_type, count(*) as count").
		Group("anchor_type").
		Scan(&stats)

	// 计算hook率（简化版）
	result := make([]schema.AnchorStats, 0)
	for _, s := range stats {
		result = append(result, schema.AnchorStats{
			AnchorType: s.AnchorType,
			AnchorName: strategy.GetAnchorName(s.AnchorType),
			UsageCount: s.Count,
			HookRate:   0.5, // 简化，实际需关联计算
		})
	}

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data:    result,
	})
}

// toJSON 将数组转为JSON字符串
func toJSON(arr []string) string {
	// 手动拼接简单JSON数组
	if len(arr) == 0 {
		return "[]"
	}
	result := "["
	for i, s := range arr {
		if i > 0 {
			result += ","
		}
		result += "\"" + s + "\""
	}
	result += "]"
	return result
}
