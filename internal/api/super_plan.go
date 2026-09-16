// super_plan.go 超管租户套餐调整（商业缺口批 2026-09-16）。
// 背景：signup 时 plan 配额快照到 tenants（席位/客户/部门/AI 次数），但此后**无任何接口改套餐**——
// 销售谈成升级/降级只能人肉改库。本文件补一键换套餐：按 plan 快照同步四项配额 + tier，
// 席位执行点（org.go 加成员按 max_users）自动随之生效；变更写 tenant_audit_logs 留痕。
package api

import (
	"fmt"
	"log"
	"net/http"

	"ai-scrm/internal/db"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"

	"github.com/gin-gonic/gin"
)

// SuperPlans GET /api/v1/super/plans —— 可售套餐列表（换套餐下拉数据源）
func SuperPlans(c *gin.Context) {
	var plans []model.SubscriptionPlan
	db.DB.Where("is_active = ?", true).Order("price_monthly_cents ASC").Find(&plans)
	RespOK(c, "", gin.H{"items": plans})
}

// SuperUpdateTenantPlan PUT /api/v1/super/tenants/:id/plan —— 换套餐（配额快照同步）
// 只改 tenants 上的配额列（与 signup 同一份来源），不动用户/客户/部门存量数据：
// 降级不删超额存量（org/customer 新增时按新配额拦截），语义与到期宽限一致。
func SuperUpdateTenantPlan(c *gin.Context) {
	tid, ok := PathUintID(c)
	if !ok {
		return
	}
	var req struct {
		PlanID uint `json:"plan_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.PlanID == 0 {
		RespErr(c, http.StatusBadRequest, 400, "plan_id 必填")
		return
	}
	var t model.Tenant
	if err := db.DB.First(&t, tid).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "租户不存在")
		return
	}
	var plan model.SubscriptionPlan
	if err := db.DB.First(&plan, req.PlanID).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "套餐不存在")
		return
	}
	if !plan.IsActive {
		RespErr(c, http.StatusBadRequest, 400, "该套餐已下架，不可指派")
		return
	}
	if t.PlanID == plan.ID {
		RespOK(c, "套餐未变化", gin.H{"plan_id": plan.ID, "plan_name": plan.Name})
		return
	}
	oldPlanID := t.PlanID
	if err := db.DB.Model(&model.Tenant{}).Where("id = ?", t.ID).Updates(map[string]interface{}{
		"plan_id":              plan.ID,
		"tier":                 plan.Tier,
		"max_users":            plan.MaxUsers,
		"max_customers":        plan.MaxCustomers,
		"max_departments":      plan.MaxDepartments,
		"max_ai_calls_monthly": plan.MaxAICalls,
		"max_storage_mb":       plan.MaxStorageMB,
		"max_knowledge_brands": plan.MaxKnowledgeBrands,
		"max_knowledge_models": plan.MaxKnowledgeModels,
	}).Error; err != nil {
		log.Printf("[超管换套餐] 租户%d 更新配额失败: %v", t.ID, err)
		RespErr(c, http.StatusInternalServerError, 500, "更新失败")
		return
	}
	db.DB.Create(&model.TenantAuditLog{
		TenantID: t.ID, Action: "plan_change", Resource: fmt.Sprintf("tenant:%d", t.ID),
		Detail: fmt.Sprintf(`{"from_plan_id":%d,"to_plan_id":%d,"to_plan":"%s","max_users":%d}`, oldPlanID, plan.ID, plan.Name, plan.MaxUsers),
	})
	_, actor, _ := middleware.CurrentUser(c)
	log.Printf("[超管换套餐] 租户%d(%s) %d→%d(%s) 席位=%d 操作人=%s", t.ID, t.Code, oldPlanID, plan.ID, plan.Name, plan.MaxUsers, actor)
	RespOK(c, "套餐已更新", gin.H{"plan_id": plan.ID, "plan_name": plan.Name, "max_users": plan.MaxUsers})
}
