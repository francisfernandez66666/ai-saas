package api

import (
	"net/http"
	"strconv"

	"ai-scrm/internal/cdp"
	"ai-scrm/internal/db"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
	"ai-scrm/internal/schema"
	"ai-scrm/internal/service"

	"github.com/gin-gonic/gin"
)

// ============================================================
// OpenAPI 只读开放接口（商业化第一批 M4，2026-08-23）
//
// 本期开放范围（只读优先，写操作后续评审）：
//   GET /openapi/v1/customers                    customer.read  客户列表
//   GET /openapi/v1/customers/:id/conversations  customer.read  会话与消息
//   GET /openapi/v1/cdp/profiles/:one_id         cdp.read       OneID 画像
//   GET /openapi/v1/usage                        all            用量对账
//
// 硬规则：
//   1. 租户隔离来自 Key 归属（OpenAPIAuth 注入），查询全部走 db.RQ(c)
//   2. CDP 端点复用既有二次校验逻辑：他租户 one_id 一律 404 防枚举
//   3. 脱敏手机号可选（mask=true 默认开），明文不出网关除非显式要求
// ============================================================

// openAPIBalance 开放响应附带的余额可见性（M4，借鉴四期 §一.4）
// 出参：月配额余量/增量余额/计费灰度态——客户侧可自助对账，无需工单询问
func openAPIBalance(c *gin.Context) gin.H {
	used, maxMonthly, balance, err := service.GetTenantQuotaView(middleware.EffectiveTenantID(c))
	if err != nil {
		return nil
	}
	enforced := true
	if service.DefaultSystemConfigService != nil {
		enforced = service.DefaultSystemConfigService.GetBool("billing_enforced", false)
	}
	return gin.H{
		"quota_remaining":  maxMonthly - used, // 三桶之月配额桶剩余（次），客户侧自助对账
		"ai_call_balance":  balance,           // 三桶之增量买断余额桶（次），不随月重置
		"billing_enforced": enforced,
	}
}

// OpenAPICustomers GET /openapi/v1/customers?page=&page_size=&keyword=&mask=
func OpenAPICustomers(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	query := db.RQ(c).Model(&model.Customer{})
	if kw := c.Query("keyword"); kw != "" {
		like := "%" + kw + "%"
		query = query.Where("name LIKE ? OR phone LIKE ?", like, like)
	}
	if stage := c.Query("journey_stage"); stage != "" {
		query = query.Where("journey_stage = ?", stage)
	}

	var total int64
	query.Count(&total)

	var customers []model.Customer
	query.Select("id, name, phone, wechat_id, gender, journey_stage, interest_model, source, assigned_user_id, created_at").
		Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&customers)

	// 手机号默认脱敏（mask=false 显式关闭；开放渠道默认最小暴露）
	mask := c.DefaultQuery("mask", "true") == "true"
	type item struct {
		ID             uint   `json:"id"`
		Name           string `json:"name"`
		Phone          string `json:"phone"`
		JourneyStage   string `json:"journey_stage"`
		InterestModel  string `json:"interest_model"`
		Source         string `json:"source"`
		AssignedUserID uint   `json:"assigned_user_id"`
		CreatedAt      string `json:"created_at"`
	}
	list := make([]item, 0, len(customers))
	for _, cu := range customers {
		p := cu.Phone
		if mask && p != "" && len(p) == 11 {
			p = p[:3] + "****" + p[7:]
		}
		list = append(list, item{
			ID: cu.ID, Name: cu.Name, Phone: p,
			JourneyStage: cu.JourneyStage, InterestModel: cu.InterestModel,
			Source: cu.Source, AssignedUserID: cu.AssignedUserID,
			CreatedAt: cu.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "success", Data: gin.H{
		"total": total, "page": page, "page_size": pageSize, "list": list,
		"balance": openAPIBalance(c), // M4 计费可见性
	}})
}

// OpenAPICustomerConversations GET /openapi/v1/customers/:id/conversations
func OpenAPICustomerConversations(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Error_code: "bad_request", Message: "客户ID非法"})
		return
	}

	// RQ 已限定本租户：跨租户 ID 查询天然 404
	var customer model.Customer
	if err := db.RQ(c).Select("id, name, journey_stage").First(&customer, id).Error; err != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Error_code: "not_found", Message: "客户不存在"})
		return
	}

	var conversations []model.Conversation
	db.RQ(c).Where("customer_id = ?", id).Order("id DESC").Limit(20).Find(&conversations)

	result := make([]gin.H, 0, len(conversations))
	for _, conv := range conversations {
		var messages []model.Message
		db.RQ(c).Select("sender_type, content, created_at").
			Where("conversation_id = ?", conv.ID).Order("id ASC").Limit(200).Find(&messages)
		result = append(result, gin.H{
			"conversation_id": conv.ID,
			"status":          conv.Status,
			"message_count":   len(messages),
			"messages":        messages,
		})
	}
	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "success", Data: gin.H{
		"customer_id":   customer.ID,
		"name":          customer.Name,
		"journey_stage": customer.JourneyStage,
		"conversations": result,
		"balance":       openAPIBalance(c), // M4 计费可见性
	}})
}

// OpenAPICDPProfile GET /openapi/v1/cdp/profiles/:one_id —— 复用 Phase A 内部函数
func OpenAPICDPProfile(c *gin.Context) {
	tenantID := middleware.EffectiveTenantID(c)
	oneID := c.Param("one_id")

	view := cdp.GetProfile(tenantID, oneID)
	if view == nil {
		// 与 B 端同款二次校验：不区分不存在/他租户，统一404防枚举
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Error_code: "not_found", Message: "画像不存在", Data: nil})
		return
	}
	view.Name = maskSensitiveFields(view.Name)
	for k, v := range view.Tags {
		view.Tags[k] = maskSensitiveFields(v)
	}
	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "success", Data: view})
}

// OpenAPIUsage GET /openapi/v1/usage?days=30 —— 用量对账（usage_records 汇总）
func OpenAPIUsage(c *gin.Context) {
	days, _ := strconv.Atoi(c.DefaultQuery("days", "30"))
	if days < 1 || days > 365 {
		days = 30
	}
	tid := middleware.EffectiveTenantID(c)

	type row struct {
		Date   string `json:"date"`
		Metric string `json:"metric"`
		Value  int64  `json:"value"`
	}
	rows := []row{}
	err := db.DB.Table("usage_records").
		Select("date, metric, value").
		Where("tenant_id = ? AND date >= TO_CHAR(CURRENT_DATE - ?, 'YYYY-MM-DD')", tid, days).
		Order("date DESC").Scan(&rows).Error
	if err != nil {
		c.JSON(http.StatusInternalServerError, schema.Response{Code: 500, Message: "查询失败"})
		return
	}
	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "success", Data: rows})
}

// ============================================================
// API Key 自助管理 API（B端，挂 admin 组）
//   POST   /api/v1/admin/apikeys            {name, perms} → 明文仅此一次
//   GET    /api/v1/admin/apikeys            列表（哈希不出网）
//   POST   /api/v1/admin/apikeys/:id/disable|enable
//   DELETE /api/v1/admin/apikeys/:id
// ============================================================

// validPerms API Key 权限白名单；create 时据此校验入参合法性（默认最小权限，拒绝越权 perm）
var validPerms = map[string]bool{
	middleware.PermChatRead: true, middleware.PermChatWrite: true,
	middleware.PermCustomerRead: true, middleware.PermCDPRead: true, middleware.PermAll: true,
}

// AdminCreateAPIKey POST /api/v1/admin/apikeys
func AdminCreateAPIKey(c *gin.Context) {
	var req struct {
		Name  string   `json:"name" binding:"required"`
		Perms []string `json:"perms" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误：name/perms 必填"})
		return
	}
	for _, p := range req.Perms {
		if !validPerms[p] {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "非法权限：" + p})
			return
		}
	}

	raw, prefix, hash := middleware.GenerateAPIKey()
	permsJSON := "[" // 最小权限序列化（白名单校验已过，直接拼装安全）
	for i, p := range req.Perms {
		if i > 0 {
			permsJSON += ","
		}
		permsJSON += "\"" + p + "\""
	}
	permsJSON += "]"

	key := model.ApiKey{
		TenantID:    ptrUint(tenantIDOf(c)),
		Name:        req.Name,
		KeyPrefix:   prefix,
		KeyHash:     hash,
		Permissions: permsJSON,
		IsActive:    true,
	}
	if err := db.DB.Create(&key).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "签发失败"})
		return
	}
	writeAuditSimple(c, tenantIDOf(c), "apikey_create", "apikey:"+prefix)

	// 明文仅出现在本次响应中，之后任何接口只回前缀
	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "API Key 已创建，明文仅显示这一次，请妥善保存",
		"data":    gin.H{"id": key.ID, "name": key.Name, "key": raw, "perms": req.Perms},
	})
}

// AdminListAPIKeys GET /api/v1/admin/apikeys
func AdminListAPIKeys(c *gin.Context) {
	var keys []model.ApiKey
	db.RQ(c).Select("id, name, key_prefix, permissions, last_used_at, call_count, is_active, created_at").
		Order("id DESC").Find(&keys)
	// call_count / last_used_at 为 Key 计量（api_calls）的对外展示字段；鉴权+perm+计量统一在 middleware.OpenAPIAuth 裁决
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": keys})
}

// AdminToggleAPIKey POST /api/v1/admin/apikeys/:id/disable | enable
func AdminDisableAPIKey(c *gin.Context) { toggleAPIKey(c, false) }

// AdminEnableAPIKey POST /api/v1/admin/apikeys/:id/enable 启用Key
func AdminEnableAPIKey(c *gin.Context) { toggleAPIKey(c, true) }

// toggleAPIKey 启停实现：db.RQ 租户过滤只能操作本租户Key；停用即时生效（鉴权每请求查库无缓存窗口）
func toggleAPIKey(c *gin.Context, active bool) {
	// RQ 租户过滤：只能操作本租户的 Key
	res := db.RQ(c).Model(&model.ApiKey{}).Where("id = ?", c.Param("id")).
		Update("is_active", active)
	if res.Error != nil || res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "API Key 不存在"})
		return
	}
	action := "apikey_enable"
	if !active {
		action = "apikey_disable" // 停用即时生效（每请求查库无缓存窗口）
	}
	writeAuditSimple(c, tenantIDOf(c), action, "apikey:"+c.Param("id"))
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": map[bool]string{true: "已启用", false: "已停用"}[active]})
}

// AdminDeleteAPIKey DELETE /api/v1/admin/apikeys/:id
func AdminDeleteAPIKey(c *gin.Context) {
	res := db.RQ(c).Where("id = ?", c.Param("id")).Delete(&model.ApiKey{})
	if res.Error != nil || res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "API Key 不存在"})
		return
	}
	writeAuditSimple(c, tenantIDOf(c), "apikey_delete", "apikey:"+c.Param("id"))
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "已删除"})
}

// writeAuditSimple 轻量审计写入（异步，失败不影响主流程）
func writeAuditSimple(c *gin.Context, tenantID uint, action, resource string) {
	uidV, _ := c.Get("user_id")
	go func() {
		defer func() { _ = recover() }()
		db.DB.Create(&model.TenantAuditLog{
			TenantID: tenantID, UserID: toUintSafe(uidV), Action: action, Resource: resource,
			IP: c.ClientIP(), UserAgent: c.Request.UserAgent(),
		})
	}()
}

// ptrUint 取指针工具
func ptrUint(v uint) *uint { return &v }
