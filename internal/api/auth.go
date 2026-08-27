// 认证接口：登录/注册/当前用户（含防爆破守卫、租户隔离、首登强改密、注销闸门）
package api

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
	"ai-scrm/internal/service"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"strings"
)

// loginRequest 登录请求结构体
type loginRequest struct {
	Username   string `json:"username" binding:"required"`
	Password   string `json:"password" binding:"required"`
	TenantCode string `json:"tenant_code"` // 企业码（统一登录页使用；空=跨租户用户名查找）
}

// registerRequest 注册请求结构体
// M-邮箱验证（2026-08-24）：email_verify_enabled=true 时 email+email_code 必填
type registerRequest struct {
	Username  string `json:"username" binding:"required"`
	Password  string `json:"password" binding:"required"`
	Email     string `json:"email"`      // 绑定邮箱（验证码目的地）
	EmailCode string `json:"email_code"` // 邮箱验证码
}

// resetRequest 重置密码请求结构体
type resetRequest struct {
	Username string `json:"username" binding:"required"`
	Code     string `json:"code" binding:"required"`
}

// newPasswordRequest 设置新密码请求
type newPasswordRequest struct {
	Password string `json:"password" binding:"required"`
}

// Login 登录接口
// 修改原因：原接口仅返回{"status":"login"}，不颁发JWTtoken，导致后续JWT鉴权接口（advisor/admin/chat）无法通过认证
// 现改为：验证凭据 → 生成JWT → 返回token，前端携带Bearer token调用有鉴权的API
func Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"code": 400, "message": "参数错误", "errors": []string{err.Error()}})
		return
	}

	// 0. 防爆破守卫：锁定中直接拒绝（不消耗数据库查询）
	clientIP := c.ClientIP()
	if err := service.CheckLoginAllowed(req.Username, clientIP); err != nil {
		c.JSON(http.StatusTooManyRequests, gin.H{"code": 429, "message": err.Error()})
		return
	}

	// 1. 从数据库查询用户（携带企业码时限定租户，防同名账号串站）
	// M3：同时取 must_change_password，登录响应带首登强改密标记
	var user model.User
	userQuery := "SELECT id, username, password_hash, role, tenant_id, must_change_password FROM tenant_users WHERE username = ?"
	args := []interface{}{req.Username}
	if code := strings.TrimSpace(req.TenantCode); code != "" {
		userQuery += " AND tenant_id IN (SELECT id FROM tenants WHERE code = ?)"
		args = append(args, code)
	}
	result := db.DB.Raw(userQuery, args...).Scan(&user)
	if result.Error != nil {
		c.JSON(500, gin.H{"code": 500, "message": "数据库错误", "data": nil})
		return
	}
	if result.RowsAffected == 0 {
		service.RecordLoginFailure(req.Username, clientIP)
		c.JSON(401, gin.H{"code": 401, "message": "用户名或密码错误", "data": nil})
		return
	}

	// 2. bcrypt CompareHashAndPassword：检查明文密码与哈希是否匹配
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		service.RecordLoginFailure(req.Username, clientIP)
		c.JSON(401, gin.H{"code": 401, "message": "用户名或密码错误", "data": nil})
		return
	}

	// P4 账号注销闸门（2026-08-26）：注销当日仍可登录，次日零点起拒绝登录（数据保留）
	if user.TenantID != nil {
		var ct model.Tenant
		if err := db.DB.Select("id, cancel_at").First(&ct, *user.TenantID).Error; err == nil && ct.CancelAt != nil {
			// 用 Go 参考时间格式 "2006-01-02" 按自然日比较：注销次日零点起才拦截
			if time.Now().Format("2006-01-02") != ct.CancelAt.Format("2006-01-02") {
				service.RecordLoginFailure(req.Username, clientIP)
				c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "账号已注销，不可登录（数据保留期请联系平台处理）", "data": nil})
				return
			}
		}
	}

	// 3. 生成 JWT Token
	tenantID := uint(0) // 默认租户（tenant_id=0），无租户归属时回落平台层
	if user.TenantID != nil {
		tenantID = *user.TenantID
	}
	service.ClearLoginFailures(req.Username)
	token, err := middleware.GenerateToken(user.ID, user.Username, user.Role, tenantID)
	if err != nil {
		c.JSON(500, gin.H{"code": 500, "message": "Token生成失败", "data": nil})
		return
	}

	c.JSON(200, gin.H{
		"code": 0,
		"data": gin.H{
			"token": token,
			// M3：返回用户信息供前端按角色分流 + must_change_password 触发强改密引导
			"user": gin.H{
				"id":                   user.ID,
				"username":             user.Username,
				"role":                 user.Role,
				"tenant_id":            tenantID,
				"must_change_password": user.MustChangePassword,
			},
		},
		"message": "登录成功",
	})
}

// Register 注册接口
// 修改原因：原系统只有写死账号，缺乏自助注册能力，SaaS 多租户必须支持新账号创建
// 流程：检查用名是否存在 → bcrypt哈希密码 → 写入 tenant_users → 生成JWT → 返回token
func Register(c *gin.Context) {
	var req registerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"code": 400, "message": "参数错误", "errors": []string{err.Error()}})
		return
	}

	// 0.5 邮箱验证（2026-08-24）：开关开启时必须持有效验证码；邮箱全局唯一
	req.Email = service.NormalizeEmail(req.Email)
	if service.EmailVerifyEnabled() {
		if req.Email == "" || req.EmailCode == "" {
			c.JSON(400, gin.H{"code": 400, "message": "请输入邮箱并获取验证码"})
			return
		}
		var dup int64
		db.DB.Model(&model.User{}).Where("email = ?", req.Email).Count(&dup)
		if dup > 0 {
			c.JSON(409, gin.H{"code": 409, "message": "该邮箱已被绑定，请更换或直接登录"})
			return
		}
		if err := service.VerifyEmailCode(req.Email, model.EmailPurposeRegister, req.EmailCode); err != nil {
			c.JSON(400, gin.H{"code": 400, "message": err.Error()})
			return
		}
	}

	// 1. 检查用户名是否已存在
	var count int64
	db.DB.Raw("SELECT count(*) FROM tenant_users WHERE username = ?", req.Username).Scan(&count)
	if count > 0 {
		c.JSON(400, gin.H{"code": 400, "message": "用户名已存在", "data": nil})
		return
	}

	// 2. bcrypt 哈希密码 (Cost 12，耗时约 100ms，生产环境可根据服务器性能调整)
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		c.JSON(500, gin.H{"code": 500, "message": "密码加密失败", "data": nil})
		return
	}

	// 3. 写入数据库（默认 role=sales，tenant_id=0，即默认租户）
	newUser := model.User{
		Username:     req.Username,
		PasswordHash: string(hashedPassword),
		Role:         "sales",   // 新账号默认为销售权限
		TenantID:     new(uint), // 默认租户，可后台分配不同tid
		Email:        req.Email, // 绑定邮箱（已验证）
	}
	result := db.DB.Create(&newUser)
	if result.Error != nil {
		c.JSON(500, gin.H{"code": 500, "message": "注册失败: " + result.Error.Error(), "data": nil})
		return
	}

	// 注册即视为同意《用户协议》《隐私政策》，落签署记录（时间+状态供超管审计）
	RecordAgreementSignatures(*newUser.TenantID, newUser.ID)

	// 4. 生成 JWT Token
	token, err := middleware.GenerateToken(newUser.ID, newUser.Username, newUser.Role, *newUser.TenantID)
	if err != nil {
		c.JSON(500, gin.H{"code": 500, "message": "Token生成失败", "data": nil})
		return
	}

	c.JSON(200, gin.H{
		"code":    0,
		"data":    gin.H{"token": token},
		"message": "注册成功，请使用新账号登录",
	})
}

// ResetPassword / VerifyResetCodeAndReset 已重构至 auth_password.go（M3 去演示化）：
// 固定验证码 123456 删除，改为随机码 + password_resets 哈希存储 + 一次性 + 限频

// GetCurrentUser GET /api/v1/auth/me —— 当前登录用户信息
// 修复：原实现硬编码返回 {"user":"admin"}，改为读真实身份（含强改密标记+绑定邮箱回显）
func GetCurrentUser(c *gin.Context) {
	userID, username, role := middleware.CurrentUser(c)
	tenantID := middleware.EffectiveTenantID(c)
	var row struct {
		MustChangePassword bool
		Email              string
	}
	db.DB.Model(&model.User{}).Select("COALESCE(must_change_password,false) AS must_change_password, COALESCE(email,'') AS email").
		Where("id = ?", userID).Scan(&row)
	c.JSON(200, gin.H{
		"code": 0,
		"data": gin.H{
			"id":                   userID,
			"username":             username,
			"role":                 role,
			"tenant_id":            tenantID,
			"email":                row.Email,
			"must_change_password": row.MustChangePassword,
		},
	})
}
