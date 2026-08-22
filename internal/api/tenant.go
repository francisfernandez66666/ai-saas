package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/pkg/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ============================================================
// 租户入驻（SaaS 注册闭环）
//
// 流程：填写企业信息+子域名标识+管理员账号 → 创建租户(trial 7天) →
//       建根部门「销售部」→ 建租户管理员账号 → 跳转统一登录
// 配套：子域名占用检查、套餐公开查询（定价页用）
// ============================================================

var tenantCodeRe = regexp.MustCompile(`^[a-z][a-z0-9-]{2,19}$`)

type signupReq struct {
	CompanyName  string `json:"company_name" binding:"required"`
	Code         string `json:"code" binding:"required"` // 子域名标识，注册后不可改
	Username     string `json:"username" binding:"required"`
	Password     string `json:"password" binding:"required"`
	ContactName  string `json:"contact_name"`
	ContactPhone string `json:"contact_phone"`
}

// TenantSignup POST /api/v1/tenant/signup （免登录）
func TenantSignup(c *gin.Context) {
	var req signupReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数不完整"})
		return
	}
	req.Code = strings.ToLower(strings.TrimSpace(req.Code))
	req.Username = strings.TrimSpace(req.Username)

	if !tenantCodeRe.MatchString(req.Code) {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "标识需为 3-20 位小写字母开头的小写字母/数字/连字符"})
		return
	}
	if strings.TrimSpace(req.CompanyName) == "" || len(req.Password) < 6 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "企业名称必填且密码至少 6 位"})
		return
	}

	// 子域名/用户名唯一性预检
	var dup int64
	db.DB.Model(&model.Tenant{}).Where("code = ?", req.Code).Count(&dup)
	if dup > 0 {
		c.JSON(http.StatusConflict, gin.H{"code": 409, "message": "该标识已被使用，请更换"})
		return
	}
	db.DB.Model(&model.User{}).Where("username = ?", req.Username).Count(&dup)
	if dup > 0 {
		c.JSON(http.StatusConflict, gin.H{"code": 409, "message": "管理员用户名已存在"})
		return
	}

	// 默认套餐：personal（不存在则置空由超管补配）
	var plan model.SubscriptionPlan
	db.DB.Where("code = ?", "personal").First(&plan)

	hashed, err := utils.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "密码处理失败"})
		return
	}

	now := time.Now()
	trialEnd := now.AddDate(0, 0, 7)
	ten := model.Tenant{
		Name:           strings.TrimSpace(req.CompanyName),
		Code:           req.Code,
		Tier:           "personal",
		Status:         "trial",
		ContactName:    req.ContactName,
		ContactPhone:   req.ContactPhone,
		MaxUsers:       plan.MaxUsers,
		MaxCustomers:   plan.MaxCustomers,
		MaxAICTalls:    plan.MaxAICTalls,
		MaxDepartments: plan.MaxDepartments,
		TrialStartAt:   &now,
		TrialEndAt:     &trialEnd,
	}
	if plan.ID > 0 {
		ten.PlanID = plan.ID
	}

	var adminID uint
	err = db.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ten).Error; err != nil {
			return err
		}
		// 根部门
		root := model.Department{TenantID: ten.ID, Name: "销售部", Depth: 1, Status: 1}
		root.Path = "/"
		if err := tx.Create(&root).Error; err != nil {
			return err
		}
		if err := tx.Model(&root).Update("path", fmt.Sprintf("/%d/", root.ID)).Error; err != nil {
			return err
		}
		// 租户管理员
		admin := model.User{
			Username:     req.Username,
			PasswordHash: hashed,
			RealName:     req.ContactName,
			Phone:        req.ContactPhone,
			Role:         model.RoleTenantAdmin,
			Status:       1,
			TenantID:     &ten.ID,
		}
		if err := tx.Create(&admin).Error; err != nil {
			return err
		}
		adminID = admin.ID
		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "开通失败: " + err.Error()})
		return
	}

	// 审计
	db.DB.Create(&model.TenantAuditLog{
		TenantID: ten.ID, UserID: adminID, Action: "tenant_signup",
		Resource: fmt.Sprintf("tenant:%s", req.Code),
		IP:       c.ClientIP(), UserAgent: c.Request.UserAgent(),
	})

	loginURL := fmt.Sprintf("/login?tenant_code=%s", req.Code)
	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "试用开通成功（7 天）",
		"data": gin.H{
			"tenant_id":   ten.ID,
			"tenant_code": req.Code,
			"login_url":   loginURL,
		},
	})
}

// CheckTenantCode GET /api/v1/tenant/check-code?code=xxx
func CheckTenantCode(c *gin.Context) {
	code := strings.ToLower(strings.TrimSpace(c.Query("code")))
	if !tenantCodeRe.MatchString(code) {
		c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{"available": false, "reason": "格式不符"}})
		return
	}
	var dup int64
	db.DB.Model(&model.Tenant{}).Where("code = ?", code).Count(&dup)
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{
		"available": dup == 0,
		"reason":    map[bool]string{true: "可用", false: "已被使用"}[dup == 0],
	}})
}

// ListPlans GET /api/v1/plans （定价页公开查询）
func ListPlans(c *gin.Context) {
	var plans []model.SubscriptionPlan
	if err := db.DB.Where("is_active = ?", true).Order("sort_order ASC, id ASC").Find(&plans).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": plans})
}
