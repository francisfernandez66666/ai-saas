// 客户管理 CRUD：分页筛选、SaaS 配额上限、T 向量初始化、软删除
package api

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/schema"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ============================================================
// 客户管理API
// 客户的CRUD操作，供B端销售/管理员使用
// ============================================================

// GetCustomerList 获取客户列表
// 支持分页、关键词搜索、条件筛选
// apidump:ts Paginated<Customer>
// 客户分页列表，list 元素为 model.Customer（tenant_id/visitor_key 为 json:"-" 不下发）。
func GetCustomerList(c *gin.Context) {
	var req schema.CustomerListRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误")
		return
	}

	query := db.RQ(c).Model(&model.Customer{})
	// P1-7 修复(2026-09-09)：接入四级数据范围（销售只见名下/部门子树客户），
	// 对齐 advisor 组 customerInDataScope 语义，杜绝普通销售翻全租户客户（含未分配访客手机号）
	query = query.Scopes(db.DataScope(c))

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

	RespOK(c, "success", schema.PageResponse{
		Total:    total,
		Page:     req.Page,
		PageSize: req.PageSize,
		List:     customers,
	})
}

// GetCustomer 获取客户详情
// apidump:ts Customer
// 单条客户档案。
func GetCustomer(c *gin.Context) {
	// P2-28 修复：非数字 id 直打 PG 触发 22P02→500。统一 PathUintID 收口（非法→400）
	id, ok := PathUintID(c)
	if !ok {
		RespErr(c, http.StatusBadRequest, 400, "客户ID非法")
		return
	}

	var customer model.Customer
	result := db.RQ(c).First(&customer, id)
	if result.Error != nil {
		RespErr(c, http.StatusNotFound, 404, "客户不存在")
		return
	}
	// P1-7 修复：详情接入四级数据范围（销售仅可见本人名下客户）
	if !customerInDataScope(c, customer.AssignedUserID) {
		RespErr(c, http.StatusNotFound, 404, "客户不存在")
		return
	}

	RespOK(c, "success", customer)
}

// CreateCustomer 创建客户
// apidump:ts Customer
// 创建成功回整行客户。
func CreateCustomer(c *gin.Context) {
	var req schema.CreateCustomerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErrBind(c, err)
		return
	}

	// 构建客户对象
	customer := &model.Customer{
		Name:            req.Name,
		Phone:           req.Phone,
		WechatID:        req.WechatID,
		Gender:          req.Gender,
		Age:             req.Age,
		Region:          req.Region,
		City:            req.City,
		Career:          req.Career,
		CustomerType:    req.CustomerType,
		InterestProduct: req.InterestProduct,
		CurrentProduct:  req.CurrentProduct,
		ProductAge:      req.ProductAge,
		Source:          req.Source,
		Budget:          req.Budget,
		DecisionCycle:   req.DecisionCycle,
		AssignedUserID:  req.AssignedUserID,
		Remark:          req.Remark,
		VisitorKey:      model.GenerateVisitorKey(), // C3：创建即下发访客密钥
		Status:          1,
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

	// 初始化T向量（置于配额检查前：C7 事务分支的 tx.Create 也须带 t_vector_json）
	tVector := customer.GetTVector()
	customer.SaveTVector(tVector)

	// 配额检查（SaaS）：租户客户数上限（MaxCustomers=0 视为不限）
	// C7 修复(2026-09-14)：count-then-insert 非原子，并发建客可超上限（配额=钱的边界）。
	// 用租户级 pg_advisory_xact_lock 把"复查计数+插入"串行化在单事务内。
	if tid := db.EffectiveTenantIDFromGin(c); tid > 0 {
		var tenant model.Tenant
		if err := db.DB.Select("max_customers").First(&tenant, tid).Error; err == nil && tenant.MaxCustomers > 0 {
			quotaErr := errors.New("quota_exceeded")
			// C7 回归修复(2026-09-15)：原用裸 db.DB.Transaction，其 *gorm.DB 会话不带请求 context，
			// 写入盖章回调 TenantFromContext(stmt.Context)=0 → 客户被盖成 tenant_id=0（跨租户越权读+
			// 归属错乱，quota'd 租户建客后 /chat 找不到自己客户）。改走 db.RQ(c).Transaction 让 tx
			// 继承租户 context；并显式 customer.TenantID=tid 双保险（回调对非零值不覆写）。
			customer.TenantID = tid
			txErr := db.RQ(c).Transaction(func(tx *gorm.DB) error {
				if r := tx.Exec("SELECT pg_advisory_xact_lock(?)", int64(tid)); r.Error != nil {
					return r.Error
				}
				var cnt int64
				if r := tx.Model(&model.Customer{}).Where("tenant_id = ?", tid).Count(&cnt); r.Error != nil {
					return r.Error
				}
				if cnt >= int64(tenant.MaxCustomers) {
					return quotaErr
				}
				return tx.Create(customer).Error
			})
			switch {
			case errors.Is(txErr, quotaErr):
				RespErr(c, http.StatusForbidden, 403, "客户数已达套餐上限，请升级套餐")
				return
			case txErr != nil:
				RespErrInternal(c, txErr, "创建失败")
				return
			}
			RespOK(c, "创建成功", customer)
			return
		}
	}

	// 无配额上限路径：直接创建（t_vector 已在配额检查前初始化）
	result := db.RQ(c).Create(customer)
	if result.Error != nil {
		RespErrInternal(c, result.Error, "创建失败")
		return
	}

	RespOK(c, "创建成功", customer)
}

// UpdateCustomer 更新客户信息
// apidump:ts Customer
// 更新成功回整行客户。
func UpdateCustomer(c *gin.Context) {
	// P2-28 修复：非数字 id 直打 PG 22P02→500。统一 PathUintID
	id, ok := PathUintID(c)
	if !ok {
		RespErr(c, http.StatusBadRequest, 400, "客户ID非法")
		return
	}

	var customer model.Customer
	result := db.RQ(c).First(&customer, id)
	if result.Error != nil {
		RespErr(c, http.StatusNotFound, 404, "客户不存在")
		return
	}
	// P1-7 修复(2026-09-09)：写操作接入四级数据范围（销售仅可改本人名下客户）
	if !canOperateCustomer(c, customer.AssignedUserID) {
		RespErr(c, http.StatusNotFound, 404, "客户不存在")
		return
	}

	var req schema.UpdateCustomerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误")
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
	if req.InterestProduct != "" {
		customer.InterestProduct = req.InterestProduct
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

	// 字段级更新，不用整行 Save（2026-09-23 批六收口，与 chat_human/advisor 那批 9 处同修法）：
	// 编辑表单打开时的 customer 行是"快照"，Save 会把快照整行写回，覆盖编辑窗口内
	// 聊天链路对该行的实时改动——最典型的是 intent_score / journey_stage /
	// assignment_reason（AI 判意向、留资分配、接管转回都在写它们），
	// 运营在后台改个备注就把客户刚推进的旅程阶段打回原形，且不会再有任何日志提示。
	// 只写本表单真正拥有的列，未列出的列一律不碰（含 tenant_id/visitor_key/
	// external_user_id/created_at/updated_at 由 GORM 自管）。
	if err := db.RQ(c).Model(&model.Customer{}).Where("id = ?", id).Updates(map[string]any{
		"name":             customer.Name,
		"phone":            customer.Phone,
		"gender":           customer.Gender,
		"age":              customer.Age,
		"region":           customer.Region,
		"career":           customer.Career,
		"interest_model":   customer.InterestProduct, // 泛行业化后 Go 字段改名，列名沿用旧名
		"budget":           customer.Budget,
		"decision_cycle":   customer.DecisionCycle,
		"intent_score":     customer.IntentScore,
		"trust_level":      customer.TrustLevel,
		"resistance_type":  customer.ResistanceType,
		"remark":           customer.Remark,
		"assigned_user_id": customer.AssignedUserID,
		"status":           customer.Status,
		"tags":             customer.Tags,
		"t_vector":         customer.TVectorJSON,
	}).Error; err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "更新失败")
		return
	}

	RespOK(c, "更新成功", customer)
}

// DeleteCustomer 删除客户
func DeleteCustomer(c *gin.Context) {
	// P2-28 修复：非数字 id 直打 PG 22P02→500。统一 PathUintID
	id, ok := PathUintID(c)
	if !ok {
		RespErr(c, http.StatusBadRequest, 400, "客户ID非法")
		return
	}

	var customer model.Customer
	result := db.RQ(c).First(&customer, id)
	if result.Error != nil {
		RespErr(c, http.StatusNotFound, 404, "客户不存在")
		return
	}
	// P1-7 修复：删除接入四级数据范围（销售仅可删本人名下客户）
	if !canOperateCustomer(c, customer.AssignedUserID) {
		RespErr(c, http.StatusNotFound, 404, "客户不存在")
		return
	}

	// 软删除：只写 status 一列（2026-09-23 批六收口）。
	// 原先读整行再 Save，会把上面 First 那一刻的快照整行写回——聊天链路正在改的
	// intent_score/journey_stage/assigned_user_id 会被一并覆掉，删除动作不该有这种副作用。
	if err := db.RQ(c).Model(&model.Customer{}).Where("id = ?", id).Update("status", 0).Error; err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "删除失败")
		return
	}

	RespOK(c, "删除成功", nil)
}

// GetCustomerConversations 获取客户的会话列表
// apidump:ts Conversation[]
// 该客户的会话列表（created_at 倒序，上限 50 条）。
func GetCustomerConversations(c *gin.Context) {
	customerID, _ := strconv.Atoi(c.Param("id"))

	// B1 修复(2026-09-14)：先校验客户在本人数据范围内，再放行其会话（旧实现跨销售越权读）
	var cust model.Customer
	if err := db.RQ(c).First(&cust, customerID).Error; err != nil ||
		!customerInDataScope(c, cust.AssignedUserID) {
		RespErr(c, http.StatusNotFound, 404, "客户不存在")
		return
	}
	var conversations []model.Conversation
	db.RQ(c).Where("customer_id = ?", customerID).
		Order("created_at DESC").
		Limit(50).
		Find(&conversations)

	RespOK(c, "success", conversations)
}
