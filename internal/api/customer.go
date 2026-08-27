// 客户管理 CRUD：分页筛选、SaaS 配额上限、T 向量初始化、软删除
package api

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/schema"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 客户管理API
// 客户的CRUD操作，供B端销售/管理员使用
// ============================================================

// GetCustomerList 获取客户列表
// 支持分页、关键词搜索、条件筛选
func GetCustomerList(c *gin.Context) {
	var req schema.CustomerListRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误", Data: nil})
		return
	}

	query := db.RQ(c).Model(&model.Customer{})

	// 关键词搜索
	if req.Keyword != "" {
		keyword := "%" + req.Keyword + "%"
		query = query.Where("name LIKE ? OR phone LIKE ? OR wechat_id LIKE ?", keyword, keyword, keyword)
	}

	// 状态筛选
	if req.Status != 0 {
		query = query.Where("status = ?", req.Status)
	}

	// 来源筛选
	if req.Source != "" {
		query = query.Where("source = ?", req.Source)
	}

	// 类型筛选
	if req.CustomerType != "" {
		query = query.Where("customer_type = ?", req.CustomerType)
	}

	// 统计总数
	var total int64
	query.Count(&total)

	// 分页查询
	var customers []model.Customer
	query.Order("id DESC").
		Offset(req.GetOffset()).
		Limit(req.PageSize).
		Find(&customers)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data: schema.PageResponse{
			Total:    total,
			Page:     req.Page,
			PageSize: req.PageSize,
			List:     customers,
		},
	})
}

// GetCustomer 获取客户详情
func GetCustomer(c *gin.Context) {
	id := c.Param("id")

	var customer model.Customer
	result := db.RQ(c).First(&customer, id)
	if result.Error != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "客户不存在", Data: nil})
		return
	}

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data:    customer,
	})
}

// CreateCustomer 创建客户
func CreateCustomer(c *gin.Context) {
	var req schema.CreateCustomerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误: " + err.Error(), Data: nil})
		return
	}

	// 构建客户对象
	customer := &model.Customer{
		Name:           req.Name,
		Phone:          req.Phone,
		WechatID:       req.WechatID,
		Gender:         req.Gender,
		Age:            req.Age,
		Region:         req.Region,
		City:           req.City,
		Career:         req.Career,
		CustomerType:   req.CustomerType,
		InterestModel:  req.InterestModel,
		CurrentCar:     req.CurrentCar,
		CarAge:         req.CarAge,
		Source:         req.Source,
		Budget:         req.Budget,
		DecisionCycle:  req.DecisionCycle,
		AssignedUserID: req.AssignedUserID,
		Remark:         req.Remark,
		Status:         1,
	}

	// 设置标签
	if len(req.Tags) > 0 {
		customer.SetTags(req.Tags)
	}

	// 设置默认初始值
	// 新客默认值：意向分 0.2 / 信任度 0.3 / 价格敏感度 0.5（模型初始基线）
	if customer.IntentScore == 0 {
		customer.IntentScore = 0.2
	}
	if customer.TrustLevel == 0 {
		customer.TrustLevel = 0.3
	}
	if customer.PriceSensitivity == 0 {
		customer.PriceSensitivity = 0.5
	}

	// 配额检查（SaaS）：租户客户数上限（MaxCustomers=0 视为不限）
	if tid := db.EffectiveTenantIDFromGin(c); tid > 0 {
		var tenant model.Tenant
		if err := db.DB.Select("max_customers").First(&tenant, tid).Error; err == nil && tenant.MaxCustomers > 0 {
			var cnt int64
			db.RQ(c).Model(&model.Customer{}).Count(&cnt)
			if cnt >= int64(tenant.MaxCustomers) {
				c.JSON(http.StatusForbidden, schema.Response{
					Code:    403,
					Message: "客户数已达套餐上限，请升级套餐",
					Data:    nil,
				})
				return
			}
		}
	}

	// 初始化T向量
	tVector := customer.GetTVector()
	customer.SaveTVector(tVector)

	result := db.RQ(c).Create(customer)
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, schema.Response{Code: 500, Message: "创建失败: " + result.Error.Error(), Data: nil})
		return
	}

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "创建成功",
		Data:    customer,
	})
}

// UpdateCustomer 更新客户信息
func UpdateCustomer(c *gin.Context) {
	id := c.Param("id")

	var customer model.Customer
	result := db.RQ(c).First(&customer, id)
	if result.Error != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "客户不存在", Data: nil})
		return
	}

	var req schema.UpdateCustomerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误", Data: nil})
		return
	}

	// 更新字段
	if req.Name != "" {
		customer.Name = req.Name
	}
	if req.Phone != "" {
		customer.Phone = req.Phone
	}
	if req.Gender != 0 {
		customer.Gender = req.Gender
	}
	if req.Age != 0 {
		customer.Age = req.Age
	}
	if req.Region != "" {
		customer.Region = req.Region
	}
	if req.Career != "" {
		customer.Career = req.Career
	}
	if req.InterestModel != "" {
		customer.InterestModel = req.InterestModel
	}
	if req.Budget != 0 {
		customer.Budget = req.Budget
	}
	if req.DecisionCycle != 0 {
		customer.DecisionCycle = req.DecisionCycle
	}
	if req.IntentScore != 0 {
		customer.IntentScore = req.IntentScore
	}
	if req.TrustLevel != 0 {
		customer.TrustLevel = req.TrustLevel
	}
	if req.ResistanceType != "" {
		customer.ResistanceType = req.ResistanceType
	}
	if req.Remark != "" {
		customer.Remark = req.Remark
	}
	if req.AssignedUserID != 0 {
		customer.AssignedUserID = req.AssignedUserID
	}
	if req.Status != 0 {
		customer.Status = req.Status
	}
	if len(req.Tags) > 0 {
		customer.SetTags(req.Tags)
	}

	// 更新T向量
	tVector := customer.GetTVector()
	customer.SaveTVector(tVector)

	db.RQ(c).Save(&customer)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "更新成功",
		Data:    customer,
	})
}

// DeleteCustomer 删除客户
func DeleteCustomer(c *gin.Context) {
	id := c.Param("id")

	var customer model.Customer
	result := db.RQ(c).First(&customer, id)
	if result.Error != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "客户不存在", Data: nil})
		return
	}

	// 软删除：状态设为0
	customer.Status = 0
	db.RQ(c).Save(&customer)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "删除成功",
		Data:    nil,
	})
}

// GetCustomerConversations 获取客户的会话列表
func GetCustomerConversations(c *gin.Context) {
	customerID, _ := strconv.Atoi(c.Param("id"))

	var conversations []model.Conversation
	db.RQ(c).Where("customer_id = ?", customerID).
		Order("created_at DESC").
		Limit(50).
		Find(&conversations)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data:    conversations,
	})
}
