// 租户入驻与套餐：SaaS 注册闭环、子域名占用检查、套餐公开查询、行业兜底、注册防薅护栏
package api

import "ai-scrm/internal/billing"

import "ai-scrm/internal/pii"

import (
	"ai-scrm/internal/runtimecfg"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
	"ai-scrm/internal/mq"
	"ai-scrm/internal/redisclient"
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

// tenantSignupCtx 租户注册链路在请求生命周期内的共享可变上下文。
// 与 chatSessionCtx 同理：把 TenantSignup() 这个 277 行的 god function 按既有注释段
// "剪切-粘贴"为挂在结构体上的阶段方法，闭包引用的外部变量显式收口为字段。
// 仅做搬家，不改变任何条件顺序 / SQL / Redis 键 / 文案 / 错误码。
type tenantSignupCtx struct {
	c          *gin.Context
	req        signupReq
	svc        *runtimecfg.SystemConfigService // 防薅护栏段一次性取快照，reviewMode 与其同源（与原实现同一对象）
	industry   string                          // 行业兜底后的最终行业码
	refCode    string                          // 归一化后的邀请码（空串=无 ref）
	reviewMode bool                            // 注册审核开关
	hashed     string                          // 管理员密码哈希
	ten        model.Tenant                    // 待创建/已创建的租户行
	adminID    uint                            // 事务内创建的管理员 ID（审计与协议签署用）
}

// TenantSignup POST /api/v1/tenant/signup （免登录）
//
// 行为零变化说明：本函数仅把原体按既有注释段拆为 tenantSignupCtx 上的阶段方法，
// 每个方法返回 (done bool) 表示"已响应客户端、主体可直接 return"。
func TenantSignup(c *gin.Context) {
	s := &tenantSignupCtx{c: c}
	if s.signupValidateBasics() {
		return
	}
	if s.signupCheckAdminEmail() {
		return
	}
	if s.signupCheckUnique() {
		return
	}
	if s.signupRateLimitGuard() {
		return
	}
	if s.signupFinalizeInputs() {
		return
	}
	if s.signupCreateRecords() {
		return
	}
	s.signupPostCreateSideEffects()

	loginURL := fmt.Sprintf("/login?tenant_code=%s", s.req.Code)
	msg := "试用开通成功（7 天）"
	if s.reviewMode {
		msg = "注册成功，账号审核中（通常1个工作日内完成）"
	}
	RespOK(c, msg, gin.H{
		"tenant_id":   s.ten.ID,
		"tenant_code": s.req.Code,
		"status":      s.ten.Status,
		"login_url":   loginURL,
	})
}

// signupValidateBasics 请求体绑定与入参形态校验（格式判据段：失败全是 400，无任何 DB 写副作用，
// 与后面的占用查询/限流段分开，避免把"参数写错"和"标识被抢"混在同一失败路径里）。
func (s *tenantSignupCtx) signupValidateBasics() bool {
	c := s.c
	if err := c.ShouldBindJSON(&s.req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数不完整")
		return true
	}
	s.req.Code = strings.ToLower(strings.TrimSpace(s.req.Code))
	s.req.Username = strings.TrimSpace(s.req.Username)

	if !tenantCodeRe.MatchString(s.req.Code) {
		RespErr(c, http.StatusBadRequest, 400, "标识需为 3-20 位小写字母开头的小写字母/数字/连字符")
		return true
	}
	if strings.TrimSpace(s.req.CompanyName) == "" || len(s.req.Password) < 6 {
		RespErr(c, http.StatusBadRequest, 400, "企业名称必填且密码至少 6 位")
		return true
	}
	return false
}

// signupCheckAdminEmail 管理员邮箱验证分支（M-邮箱接入，2026-08-24）。
// 单独成段是因为它的失败路径依赖热开关 email_verify_enabled——开关关时整段跳过，
// 与后面无条件的重复邮箱校验（UAT定稿②）不是同一判据。
func (s *tenantSignupCtx) signupCheckAdminEmail() bool {
	c := s.c
	// 管理员邮箱验证（M-邮箱接入，2026-08-24）：开关开启时必填且需持有效验证码
	s.req.AdminEmail = service.NormalizeEmail(s.req.AdminEmail)
	if service.EmailVerifyEnabled() {
		if s.req.AdminEmail == "" || s.req.EmailCode == "" {
			RespErr(c, http.StatusBadRequest, 400, "请输入管理员邮箱并获取验证码")
			return true
		}
		var dupMail int64
		db.DB.Model(&model.User{}).Where("email = ?", s.req.AdminEmail).Count(&dupMail)
		if dupMail > 0 {
			RespErr(c, http.StatusConflict, 409, "该邮箱已被绑定，请更换")
			return true
		}
		if err := service.VerifyEmailCode(s.req.AdminEmail, model.EmailPurposeRegister, s.req.EmailCode); err != nil {
			RespErr(c, http.StatusBadRequest, 400, err.Error())
			return true
		}
	}
	return false
}

// signupCheckUnique 子域名/用户名唯一性预检。
// 只挡"一眼可见"的占用，真正的并发闸门在事务提交时由唯一索引兜底（见 signupCreateRecords 的错误映射），
// 所以本段的失败码与那边的失败码是同一套话术、两段防线。
func (s *tenantSignupCtx) signupCheckUnique() bool {
	c := s.c
	var dup int64
	db.DB.Model(&model.Tenant{}).Where("code = ?", s.req.Code).Count(&dup)
	if dup > 0 {
		RespErr(c, http.StatusConflict, 409, "该标识暂不可用，请更换")
		return true
	}
	db.DB.Model(&model.User{}).Where("username = ?", s.req.Username).Count(&dup)
	if dup > 0 {
		RespErr(c, http.StatusConflict, 409, "管理员用户名已存在")
		return true
	}
	return false
}

// signupRateLimitGuard 注册防薅护栏 v2（2026-08-26）：主锚=账号身份(OneID/唯一邮箱)，IP降为内测兜底。
// 单独成段是因为它读系统热配置并可能落 429——失败既不是参数错也不是资源占用，
// 且 Redis 开关决定计数走独立键还是审计 LIKE，与前后段的数据面完全不同。
//
// 生产态(邮箱验证开)：唯一邮箱即身份锚，同邮箱注册提交限流 register_email_daily_limit；
//
//	IP 仅写入审计供事后风控，不做硬拦截——避免同 NAT 多企业误伤（用户定稿语义）
//
// 内测态(邮箱验证关)：无身份锚可用 → 保留原 IP 双闸兜底
func (s *tenantSignupCtx) signupRateLimitGuard() bool {
	c := s.c
	// ---- 注册防薅护栏 v2（2026-08-26）：主锚=账号身份(OneID/唯一邮箱)，IP降为内测兜底 ----
	svc := runtimecfg.DefaultSystemConfigService
	s.svc = svc
	if service.EmailVerifyEnabled() && s.req.AdminEmail != "" {
		daily := svc.GetInt("register_email_daily_limit", 3)
		var cnt int64
		// P2-32 修复：防薅计数迁移到 Redis 独立键（signup:email:{date}:{email}），
		// 不再依赖审计日志 LIKE —— 邮箱含 %/_ 会干扰模糊匹配，且日志按批清理后计数即失效。
		// Redis 关闭时退回审计 LIKE 兜底（escape 通配符）。
		if redisclient.IsEnabled() {
			key := fmt.Sprintf("scrm:signup_email:%s:%s", time.Now().Format("2006-01-02"), s.req.AdminEmail)
			attempts := redisclient.GetInt(key)
			cnt = attempts
		} else {
			escaped := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(s.req.AdminEmail)
			db.DB.Model(&model.TenantAuditLog{}).
				Where("action = ? AND created_at >= CURRENT_DATE AND detail LIKE ? ESCAPE '\\'",
					"tenant_signup", fmt.Sprintf(`%%"email":"%s"%%`, escaped)).
				Count(&cnt)
		}
		if cnt >= int64(daily) {
			log.Printf("[防薅v2] 邮箱=%s 今日注册尝试已达上限(%d)", pii.MaskEmail(s.req.AdminEmail), daily)
			RespErr(c, http.StatusTooManyRequests, 429, "该邮箱今日注册尝试已达上限，请明日再试或联系我们")
			return true
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
				return true
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
				return true
			}
		}
	}
	return false
}

// signupFinalizeInputs UAT定稿三项（2026-08-26）收尾校验 + 建店材料备齐。
// 单独成段是因为这里是"只读校验 + 材料装配"的最后一步：①②的失败仍是 400/409，
// 而密码哈希失败是 500——失败量纲从此往后就变成"落库段"的了。
func (s *tenantSignupCtx) signupFinalizeInputs() bool {
	c := s.c
	// ---- UAT定稿三项（2026-08-26）----

	// ① 弱密码拒绝：与改密共用同一强度基线（≥8位含字母和数字）
	if err := validatePasswordStrength(s.req.Password); err != nil {
		RespErr(c, http.StatusBadRequest, 400, err.Error())
		return true
	}

	// ② 重复邮箱注册拒绝：邮箱为 OneID 外键唯一锚，全局唯一无条件校验
	s.req.AdminEmail = service.NormalizeEmail(s.req.AdminEmail)
	if s.req.AdminEmail != "" {
		var dupMail int64
		db.DB.Model(&model.User{}).Where("email = ?", s.req.AdminEmail).Count(&dupMail)
		if dupMail > 0 {
			RespErr(c, http.StatusConflict, 409, "该邮箱已被绑定，请更换或直接登录")
			return true
		}
	}

	// ③ 行业兜底：未填/未知行业不拒绝，回落通用行业（general）
	industry := strings.ToLower(strings.TrimSpace(s.req.Industry))

	// 行业兜底（UAT定稿②）：未填或未知行业不拒绝，回落通用行业(general)
	s.industry = resolveIndustry(industry)

	// 默认套餐：personal（全局目录表无 tenant_id，注册跨租户读取是预期）
	var plan model.SubscriptionPlan
	db.DB.Where("code = ?", "personal").First(&plan)

	hashed, err := utils.HashPassword(s.req.Password)
	if err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "密码处理失败")
		return true
	}
	s.hashed = hashed
	// plan 只在这一步用（配额快照写进租户行），故随装配段就地展开，不提为字段
	now := time.Now()
	trialEnd := now.AddDate(0, 0, 7) // 试用 7 天（trial 周期业务固定值）
	// 注册审核开关（M1）：开启时新租户进入 review 状态——不发试用包、业务接口全拦，
	// 超管在平台后台执行「发放试用」后转 trial 放行
	s.reviewMode = s.svc.GetBool("registration_review", false)
	status := "trial"
	if s.reviewMode {
		status = "review"
	}
	s.ten = model.Tenant{
		Name:           strings.TrimSpace(s.req.CompanyName),
		Code:           s.req.Code,
		Tier:           "personal",
		Status:         status,
		ContactName:    s.req.ContactName,
		ContactPhone:   s.req.ContactPhone,
		MaxUsers:       plan.MaxUsers,
		MaxCustomers:   plan.MaxCustomers,
		MaxAICalls:     plan.MaxAICalls,
		MaxDepartments: plan.MaxDepartments,
		TrialStartAt:   &now,
		TrialEndAt:     &trialEnd,
		Industry:       s.industry,
	}
	if plan.ID > 0 {
		s.ten.PlanID = plan.ID
	}

	// M-R 邀请推广（2026-08-25）：新租户邀请码（8位，冲突概率≈1/31^8）
	if code, err := billing.GenerateInviteCode(); err == nil {
		s.ten.InviteCode = code
	}
	s.refCode = strings.TrimSpace(strings.ToUpper(s.req.Ref))
	return false
}

// signupCreateRecords 事务落库：租户 + 邀请绑定/试用桶 + 根部门 + 管理员，并把 DB 错误映射回业务码。
// 事务边界不跨函数拆散（C7 红线：事务内写租户表必须显式 TenantID——root/admin 均已显式带上，
// 租户行本体是新建主体）；错误映射段留在同函数，是因为"哪些索引冲突对应哪句 409 话术"
// 只有握着事务返回值的人能判定，拆出去只会多传一个 error 参数。
func (s *tenantSignupCtx) signupCreateRecords() bool {
	c := s.c
	var err error
	err = db.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&s.ten).Error; err != nil {
			return err
		}
		// M-R 邀请推广：首绑邀请关系 + 邀请人侧奖励
		billing.ApplyReferralBinding(tx, &s.ten, s.refCode, s.req.AdminEmail)
		// P1.5 注册赠送免费桶 —— 双发修复(2026-08-26 UAT发现)：
		// 带 ref 时 ApplyReferralBinding 的新客侧发放即注册礼，两条路径叠加曾致60万。
		// 现互斥：仅无 ref 走 GrantTrialBucket；有 ref 的同额由邀请路径承担。
		if s.refCode == "" {
			billing.GrantTrialBucket(tx, s.ten.ID, s.req.AdminEmail)
		}
		// 根部门
		root := model.Department{TenantID: s.ten.ID, Name: "销售部", Depth: 1, Status: 1}
		root.Path = "/"
		if err := tx.Create(&root).Error; err != nil {
			return err
		}
		if err := tx.Model(&root).Update("path", fmt.Sprintf("/%d/", root.ID)).Error; err != nil {
			return err
		}
		// 租户管理员
		admin := model.User{
			Username:     s.req.Username,
			PasswordHash: s.hashed,
			RealName:     s.req.ContactName,
			Phone:        s.req.ContactPhone,
			Email:        s.req.AdminEmail, // 已验证的管理员邮箱
			Role:         model.RoleTenantAdmin,
			Status:       1,
			TenantID:     &s.ten.ID,
		}
		if err := tx.Create(&admin).Error; err != nil {
			return err
		}
		s.adminID = admin.ID
		return nil
	})
	if err != nil {
		// R15 修复(2026-09-11)：唯一索引是并发注册的最终闸门——撞库返回业务 409，
		// 不再把裸 DB 错误 500 给前端（且 500 会让用户以为可重试，实际必然再撞）
		msg := err.Error()
		switch {
		case strings.Contains(msg, "ux_tenant_users_email_nonempty"):
			RespErr(c, http.StatusConflict, 409, "该邮箱刚被并发注册占用，请直接登录或更换")
		case strings.Contains(msg, "idx_tenants_code"):
			RespErr(c, http.StatusConflict, 409, "该标识暂不可用，请更换")
		case strings.Contains(msg, "idx_tenant_users_username"):
			RespErr(c, http.StatusConflict, 409, "管理员用户名已存在")
		default:
			// 2026-09-22 P1-1：默认分支曾原样回传 DB 错误（含索引名/列宽/SQLSTATE），
			// 等同于对外提供结构探测面；改为脱敏出口，真实错误只落日志。
			RespErrInternal(c, err, "开通失败")
		}
		return true
	}
	return false
}

// signupPostCreateSideEffects 建店成功后的旁路动作：行业包落绑、审计、Redis 计数、协议签署、OneID 发布。
// 单独成段是因为这一段全部是"尽力而为"的旁路（都不回滚主事务、失败不改变响应），
// 与前面"任何一步失败即拒绝开通"的判据段性质相反，混在一起会掩盖哪些失败是无感的。
func (s *tenantSignupCtx) signupPostCreateSideEffects() {
	c := s.c
	// 泛行业化 P4：注册即按所选行业落行业包（realty/b2b/...），
	// 库存不存在或 general 时跳过，由启动钩子 AutoApplyDefaultIndustryPack 的默认包兜底
	BindTenantToIndustryPack(s.ten.ID, s.industry)

	// P1.5(2026-08-26)：注册赠礼统一收口到 GrantTrialBucket（事务内已发，双唯一防撞库）
	// 移除遗留 grantTrialPackage 双发路径；审核态租户桶已预置、放行前无法消耗

	// 审计（detail 带 email：防薅v2 账号锚限流的计数依据）
	db.DB.Create(&model.TenantAuditLog{
		TenantID: s.ten.ID, UserID: s.adminID, Action: "tenant_signup",
		Resource: fmt.Sprintf("tenant:%s", s.req.Code),
		Detail:   fmt.Sprintf(`{"code":"%s","email":"%s"}`, s.req.Code, s.req.AdminEmail),
		IP:       c.ClientIP(), UserAgent: c.Request.UserAgent(),
	})

	// P2-32 修复：注册成功同时 INCR Redis 每日键（与上方防薅检查同源），
	// 审计 LIKE 仅作 Redis 关闭时兜底；键带 TTL 次日自动回收。
	if redisclient.IsEnabled() && s.req.AdminEmail != "" {
		redisclient.IncrWithTTL(
			fmt.Sprintf("scrm:signup_email:%s:%s", time.Now().Format("2006-01-02"), s.req.AdminEmail),
			48*time.Hour)
	}

	// 注册即视为同意《用户协议》《隐私政策》，落签署记录（时间+状态供超管审计）
	RecordAgreementSignatures(s.ten.ID, s.adminID)

	// OneID 关联（防薅v2，2026-08-26）：注册成功即以邮箱为身份锚之一写 CDP
	_ = mq.Publish(middleware.CtxWithTrace(c), mq.TopicUserEvent, s.ten.ID,
		fmt.Sprintf("sys:t%d", s.ten.ID), "guest_created",
		model.MessageEvent{EventType: "identity", EventName: "guest_created",
			AnchorType: "email",
			Attributes: map[string]any{"email": s.req.AdminEmail, "tenant_code": s.req.Code}})
}

// CheckTenantCode GET /api/v1/tenant/check-code?code=xxx
// CheckTenantCode 校验企业码是否可用于注册或登录。
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
// P2-90 修复(2026-09-09)：packages 并入 data 信封（data.packages），
// 恢复 ApiResp<T> 统一契约——原"顶层并行 packages 键"破坏类型契约。
func ListPlans(c *gin.Context) {
	var plans []model.SubscriptionPlan
	// 定价目录全局共享（无 tenant_id），所有会话可见是预期（rls_scope_test 已验证）
	if err := db.DB.Where("is_active = ?", true).Order("sort_order ASC, id ASC").Find(&plans).Error; err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "查询失败")
		return
	}
	var pkgs []model.Package
	db.DB.Where("enabled = ?", true).Order("sort_order ASC, id ASC").Find(&pkgs)
	RespOK(c, "", gin.H{"plans": plans, "packages": pkgs})
}

// resolveIndustry 行业解析与兜底（UAT定稿②，2026-08-26）
// 规则：入参为空 → 直接通用行业；非空时校验是否为已上架的行业级包 code，
// 命中返回原值，未命中不拒绝、回落通用行业(general)。
// G-22b（2026-09-24）：general 从"纯身份标签"升级为带内容的中立兜底包
// （packs-src/general 六件套 + data/packs/general_*.aipack），
// 回落后的租户注册时会被 BindTenantToIndustryPack 落上这套中立话术，
// 而不是继续吃汽车基包的人设与询价话术。
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
