// 租户入驻与套餐：SaaS 注册闭环、子域名占用检查、套餐公开查询、行业兜底、注册防薅护栏
package api

import (
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/mq"
	"ai-scrm/internal/service"
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

// tenantCodeRe 租户子域名标识正则：小写字母开头，后接小写字母/数字/连字符，总长 3-20 位
var tenantCodeRe = regexp.MustCompile(`^[a-z][a-z0-9-]{2,19}$`)

// signupReq 租户注册请求体：企业信息 + 管理员账号 + 可选的邮箱验证码/邀请码/行业
type signupReq struct {
	CompanyName  string `json:"company_name" binding:"required"`
	Code         string `json:"code" binding:"required"` // 子域名标识，注册后不可改
	Username     string `json:"username" binding:"required"`
	Password     string `json:"password" binding:"required"`
	ContactName  string `json:"contact_name"`
	ContactPhone string `json:"contact_phone"`
	AdminEmail   string `json:"admin_email"` // 管理员邮箱（email_verify_enabled 时必填+验证码校验）
	EmailCode    string `json:"email_code"`  // 邮箱验证码
	Ref          string `json:"ref"`         // 邀请码（M-R 邀请推广，选填；首绑唯一）
	Industry     string `json:"industry"`    // 行业（选填；未知行业自动兜底为通用行业 general）
}

// TenantSignup POST /api/v1/tenant/signup （免登录）
func TenantSignup(c *gin.Context) {
	var req signupReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数不完整")
		return
	}
	req.Code = strings.ToLower(strings.TrimSpace(req.Code))
	req.Username = strings.TrimSpace(req.Username)

	if !tenantCodeRe.MatchString(req.Code) {
		RespErr(c, http.StatusBadRequest, 400, "标识需为 3-20 位小写字母开头的小写字母/数字/连字符")
		return
	}
	if strings.TrimSpace(req.CompanyName) == "" || len(req.Password) < 6 {
		RespErr(c, http.StatusBadRequest, 400, "企业名称必填且密码至少 6 位")
		return
	}

	// 管理员邮箱验证（M-邮箱接入，2026-08-24）：开关开启时必填且需持有效验证码
	req.AdminEmail = service.NormalizeEmail(req.AdminEmail)
	if service.EmailVerifyEnabled() {
		if req.AdminEmail == "" || req.EmailCode == "" {
			RespErr(c, http.StatusBadRequest, 400, "请输入管理员邮箱并获取验证码")
			return
		}
		var dupMail int64
		db.DB.Model(&model.User{}).Where("email = ?", req.AdminEmail).Count(&dupMail)
		if dupMail > 0 {
			RespErr(c, http.StatusConflict, 409, "该邮箱已被绑定，请更换")
			return
		}
		if err := service.VerifyEmailCode(req.AdminEmail, model.EmailPurposeRegister, req.EmailCode); err != nil {
			RespErr(c, http.StatusBadRequest, 400, err.Error())
			return
		}
	}

	// 子域名/用户名唯一性预检
	var dup int64
	db.DB.Model(&model.Tenant{}).Where("code = ?", req.Code).Count(&dup)
	if dup > 0 {
		RespErr(c, http.StatusConflict, 409, "该标识暂不可用，请更换")
		return
	}
	db.DB.Model(&model.User{}).Where("username = ?", req.Username).Count(&dup)
	if dup > 0 {
		RespErr(c, http.StatusConflict, 409, "管理员用户名已存在")
		return
	}

	// ---- 注册防薅护栏 v2（2026-08-26）：主锚=账号身份(OneID/唯一邮箱)，IP降为内测兜底 ----
	// 生产态(邮箱验证开)：唯一邮箱即身份锚，同邮箱注册提交限流 register_email_daily_limit；
	//   IP 仅写入审计供事后风控，不做硬拦截——避免同 NAT 多企业误伤（用户定稿语义）
	// 内测态(邮箱验证关)：无身份锚可用 → 保留原 IP 双闸兜底
	svc := service.DefaultSystemConfigService
	if service.EmailVerifyEnabled() && req.AdminEmail != "" {
		daily := svc.GetInt("register_email_daily_limit", 3)
		var cnt int64
		// 防薅按 email 全局计数（跨租户累计）：同邮箱换租户注册也须被闸门拦截（rls_scope_test 已验证）
		db.DB.Model(&model.TenantAuditLog{}).
			Where("action = ? AND created_at >= CURRENT_DATE AND detail LIKE ?",
				"tenant_signup", fmt.Sprintf(`%%"email":"%s"%%`, req.AdminEmail)).
			Count(&cnt)
		if cnt >= int64(daily) {
			log.Printf("[防薅v2] 邮箱=%s 今日注册尝试已达上限(%d)", service.MaskEmail(req.AdminEmail), daily)
			RespErr(c, http.StatusTooManyRequests, 429, "该邮箱今日注册尝试已达上限，请明日再试或联系我们")
			return
		}
	} else {
		ip := c.ClientIP()
		if dailyLimit := svc.GetInt("register_ip_daily_limit", 3); dailyLimit > 0 {
			var cnt int64
			// 防薅按 IP 全局计数（跨租户累计）：同 IP 换租户注册也须被闸门拦截
			db.DB.Model(&model.TenantAuditLog{}).
				Where("action = ? AND ip = ? AND created_at >= CURRENT_DATE", "tenant_signup", ip).
				Count(&cnt)
			if cnt >= int64(dailyLimit) {
				log.Printf("[防薅] IP=%s 今日注册已达上限(%d)", ip, dailyLimit)
				RespErr(c, http.StatusTooManyRequests, 429, "该网络今日注册次数已达上限，请明日再试或联系我们")
				return
			}
		}
		if minGap := svc.GetInt("register_ip_min_interval_sec", 60); minGap > 0 {
			var last time.Time
			// 防薅按 IP 查最近注册时间（跨租户累计）：同 IP 短间隔换租户注册也拦截
			db.DB.Model(&model.TenantAuditLog{}).
				Select("created_at").
				Where("action = ? AND ip = ?", "tenant_signup", ip).
				Order("id DESC").Limit(1).Scan(&last)
			if !last.IsZero() && time.Since(last) < time.Duration(minGap)*time.Second {
				RespErr(c, http.StatusTooManyRequests, 429, "注册过于频繁，请稍后再试")
				return
			}
		}
	}

	// ---- UAT定稿三项（2026-08-26）----

	// ① 弱密码拒绝：与改密共用同一强度基线（≥8位含字母和数字）
	if err := validatePasswordStrength(req.Password); err != nil {
		RespErr(c, http.StatusBadRequest, 400, err.Error())
		return
	}

	// ② 重复邮箱注册拒绝：邮箱为 OneID 外键唯一锚，全局唯一无条件校验
	req.AdminEmail = service.NormalizeEmail(req.AdminEmail)
	if req.AdminEmail != "" {
		var dupMail int64
		db.DB.Model(&model.User{}).Where("email = ?", req.AdminEmail).Count(&dupMail)
		if dupMail > 0 {
			RespErr(c, http.StatusConflict, 409, "该邮箱已被绑定，请更换或直接登录")
			return
		}
	}

	// ③ 行业兜底：未填/未知行业不拒绝，回落通用行业（general）
	industry := strings.ToLower(strings.TrimSpace(req.Industry))

	// 行业兜底（UAT定稿②）：未填或未知行业不拒绝，回落通用行业(general)
	industry = resolveIndustry(industry)

	// 默认套餐：personal（全局目录表无 tenant_id，注册跨租户读取是预期）
	var plan model.SubscriptionPlan
	db.DB.Where("code = ?", "personal").First(&plan)

	hashed, err := utils.HashPassword(req.Password)
	if err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "密码处理失败")
		return
	}

	now := time.Now()
	trialEnd := now.AddDate(0, 0, 7) // 试用 7 天（trial 周期业务固定值）
	// 注册审核开关（M1）：开启时新租户进入 review 状态——不发试用包、业务接口全拦，
	// 超管在平台后台执行「发放试用」后转 trial 放行
	reviewMode := svc.GetBool("registration_review", false)
	status := "trial"
	if reviewMode {
		status = "review"
	}
	ten := model.Tenant{
		Name:           strings.TrimSpace(req.CompanyName),
		Code:           req.Code,
		Tier:           "personal",
		Status:         status,
		ContactName:    req.ContactName,
		ContactPhone:   req.ContactPhone,
		MaxUsers:       plan.MaxUsers,
		MaxCustomers:   plan.MaxCustomers,
		MaxAICalls:     plan.MaxAICalls,
		MaxDepartments: plan.MaxDepartments,
		TrialStartAt:   &now,
		TrialEndAt:     &trialEnd,
		Industry:       industry,
	}
	if plan.ID > 0 {
		ten.PlanID = plan.ID
	}

	// M-R 邀请推广（2026-08-25）：新租户邀请码（8位，冲突概率≈1/31^8）
	if code, err := service.GenerateInviteCode(); err == nil {
		ten.InviteCode = code
	}
	refCode := strings.TrimSpace(strings.ToUpper(req.Ref))

	var adminID uint
	err = db.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ten).Error; err != nil {
			return err
		}
		// M-R 邀请推广：首绑邀请关系 + 邀请人侧奖励
		service.ApplyReferralBinding(tx, &ten, refCode, req.AdminEmail)
		// P1.5 注册赠送免费桶 —— 双发修复(2026-08-26 UAT发现)：
		// 带 ref 时 ApplyReferralBinding 的新客侧发放即注册礼，两条路径叠加曾致60万。
		// 现互斥：仅无 ref 走 GrantTrialBucket；有 ref 的同额由邀请路径承担。
		if refCode == "" {
			service.GrantTrialBucket(tx, ten.ID, req.AdminEmail)
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
			Email:        req.AdminEmail, // 已验证的管理员邮箱
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
		RespErr(c, http.StatusInternalServerError, 500, "开通失败: "+err.Error())
		return
	}

	// P1.5(2026-08-26)：注册赠礼统一收口到 GrantTrialBucket（事务内已发，双唯一防撞库）
	// 移除遗留 grantTrialPackage 双发路径；审核态租户桶已预置、放行前无法消耗

	// 审计（detail 带 email：防薅v2 账号锚限流的计数依据）
	db.DB.Create(&model.TenantAuditLog{
		TenantID: ten.ID, UserID: adminID, Action: "tenant_signup",
		Resource: fmt.Sprintf("tenant:%s", req.Code),
		Detail:   fmt.Sprintf(`{"code":"%s","email":"%s"}`, req.Code, req.AdminEmail),
		IP:       c.ClientIP(), UserAgent: c.Request.UserAgent(),
	})

	// 注册即视为同意《用户协议》《隐私政策》，落签署记录（时间+状态供超管审计）
	RecordAgreementSignatures(ten.ID, adminID)

	// OneID 关联（防薅v2，2026-08-26）：注册成功即以邮箱为身份锚之一写 CDP
	_ = mq.Publish(c.Request.Context(), mq.TopicUserEvent, ten.ID,
		fmt.Sprintf("sys:t%d", ten.ID), "guest_created",
		model.MessageEvent{EventType: "identity", EventName: "guest_created",
			AnchorType: "email",
			Attributes: map[string]any{"email": req.AdminEmail, "tenant_code": req.Code}})

	loginURL := fmt.Sprintf("/login?tenant_code=%s", req.Code)
	msg := "试用开通成功（7 天）"
	if reviewMode {
		msg = "注册成功，账号审核中（通常1个工作日内完成）"
	}
	RespOK(c, msg, gin.H{
		"tenant_id":   ten.ID,
		"tenant_code": req.Code,
		"status":      ten.Status,
		"login_url":   loginURL,
	})
}

// grantTrialPackage 注册联动发放免费试用包（商业化 M2）
// 额度取系统配置 trial_ai_calls（默认500次）；无 free 包时按配置直接加月配额兜底
func grantTrialPackage(tenantID uint) {
	var pkg model.Package
	if err := db.DB.Where("p_type = ? AND enabled = ?", model.PackageTypeFree, true).
		Order("sort_order ASC").First(&pkg).Error; err == nil {
		if err := service.GrantPackage(nil, tenantID, &pkg); err != nil {
			log.Printf("[Signup] 试用包发放失败 tenant=%d: %v", tenantID, err)
		}
		return
	}
	// 兜底：packages 表无 free 包（seed 未跑/被删），按配置值直加月配额
	trialCalls := 500
	if service.DefaultSystemConfigService != nil {
		trialCalls = service.DefaultSystemConfigService.GetInt("trial_ai_calls", 500)
	}
	db.DB.Model(&model.Tenant{}).Where("id = ?", tenantID).
		Update("max_ai_calls_monthly", gorm.Expr("COALESCE(max_ai_calls_monthly,0)+?", trialCalls))
	log.Printf("[Signup] 已发放试用额度 %d 次 → 租户%d（配置兜底）", trialCalls, tenantID)
}

// CheckTenantCode GET /api/v1/tenant/check-code?code=xxx
func CheckTenantCode(c *gin.Context) {
	code := strings.ToLower(strings.TrimSpace(c.Query("code")))
	if !tenantCodeRe.MatchString(code) {
		RespOK(c, "", gin.H{"available": false, "reason": "格式不符"})
		return
	}
	var dup int64
	db.DB.Model(&model.Tenant{}).Where("code = ?", code).Count(&dup)
	RespOK(c, "", gin.H{
		"available": dup == 0,
		"reason":    map[bool]string{true: "可用", false: "该标识暂不可用"}[dup == 0],
	})
}

// ListPlans GET /api/v1/plans （定价页公开查询）
// 商业化 M2 扩展：data 保持 legacy subscription_plans（老定价页兼容），
// packages 返回新商业包体系（试用/包月/增量），两者并存过渡
func ListPlans(c *gin.Context) {
	var plans []model.SubscriptionPlan
	// 定价目录全局共享（无 tenant_id），所有会话可见是预期（rls_scope_test 已验证）
	if err := db.DB.Where("is_active = ?", true).Order("sort_order ASC, id ASC").Find(&plans).Error; err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "查询失败")
		return
	}
	var pkgs []model.Package
	db.DB.Where("enabled = ?", true).Order("sort_order ASC, id ASC").Find(&pkgs)
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": plans, "packages": pkgs})
}

// resolveIndustry 行业解析与兜底（UAT定稿②，2026-08-26）
// 规则：入参为空 → 直接通用行业；非空时校验是否为已上架的行业级包 code，
// 命中返回原值，未命中不拒绝、回落通用行业(general)。通用行业无需包文件，
// 仅作为租户身份标签存在（后续可挂载通用内容包）。
func resolveIndustry(industry string) string {
	industry = strings.ToLower(strings.TrimSpace(industry))
	if industry == "" {
		return "general"
	}
	var cnt int64
	db.DB.Model(&model.IndustryPack{}).
		Where("pack_level = ? AND code = ?", "industry", industry).Count(&cnt)
	if cnt > 0 {
		return industry
	}
	log.Printf("[Signup] 行业[%s]暂无对应行业包，回落通用行业general", industry)
	return "general"
}
