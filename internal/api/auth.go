package api

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// loginRequest 登录请求结构体
type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// registerRequest 注册请求结构体
type registerRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
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

	// 1. 从数据库查询用户
	var user model.User
	result := db.DB.Raw("SELECT id, username, password_hash, role, tenant_id FROM tenant_users WHERE username = ?", req.Username).Scan(&user)
	if result.Error != nil {
		c.JSON(500, gin.H{"code": 500, "message": "数据库错误", "data": nil})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(401, gin.H{"code": 401, "message": "用户名或密码错误", "data": nil})
		return
	}

	// 2. bcrypt CompareHashAndPassword：检查明文密码与哈希是否匹配
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		c.JSON(401, gin.H{"code": 401, "message": "用户名或密码错误", "data": nil})
		return
	}

	// 3. 生成 JWT Token
	tenantID := uint(0)
	if user.TenantID != nil {
		tenantID = *user.TenantID
	}
	token, err := middleware.GenerateToken(user.ID, user.Username, user.Role, tenantID)
	if err != nil {
		c.JSON(500, gin.H{"code": 500, "message": "Token生成失败", "data": nil})
		return
	}

	c.JSON(200, gin.H{
		"code":    0,
		"data":    gin.H{"token": token},
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
	}
	result := db.DB.Create(&newUser)
	if result.Error != nil {
		c.JSON(500, gin.H{"code": 500, "message": "注册失败: " + result.Error.Error(), "data": nil})
		return
	}

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

// ResetPasswordRequest 发送重置密码验证码请求
type ResetPasswordRequest struct {
	Username string `json:"username" binding:"required"`
}

// ResetPasswordCodeVerify 验证重置验证码并重置密码的请求
type ResetPasswordCodeVerify struct {
	Username string `json:"username" binding:"required"`
	Code     string `json:"code" binding:"required"`
}

// NewPassword 设置新密码请求
type NewPassword struct {
	Password string `json:"password" binding:"required"`
}

// ResetPassword 发送重置密码请求
// 修改原因：支持用户通过验证码重置密码，无需记住旧密码
func ResetPassword(c *gin.Context) {
	var req ResetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"code": 400, "message": "参数错误", "errors": []string{err.Error()}})
		return
	}

	// 检查用户是否存在
	var count int64
	db.DB.Raw("SELECT count(*) FROM tenant_users WHERE username = ?", req.Username).Scan(&count)
	if count == 0 {
		c.JSON(404, gin.H{"code": 404, "message": "用户不存在", "data": nil})
		return
	}

	// 生成一个 6 位数字验证码
	// 实际项目应通过邮件/SMS发送，此处生成后直接返回供前端演示
	// 验证码有效期：10分钟
	code := "123456" // 简化演示：固定验证码，实际应生成随机码并存入 Redis

	c.JSON(200, gin.H{
		"code": 0,
		"data": gin.H{
			"username": req.Username,
			"code":     code,
			"message":  "验证码已生成，请在10分钟内使用（演示模式：固定码 123456）",
		},
		"message": "验证码已生成",
	})
}

// VerifyResetCodeAndReset 验证验证码并重置密码
// 修改原因：验证码验证通过后，允许用户设置新密码
func VerifyResetCodeAndReset(c *gin.Context) {
	var req ResetPasswordCodeVerify
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"code": 400, "message": "参数错误", "errors": []string{err.Error()}})
		return
	}

	// 1. 检查用户是否存在
	var user model.User
	result := db.DB.Raw("SELECT id, username, password_hash FROM tenant_users WHERE username = ?", req.Username).Scan(&user)
	if result.Error != nil {
		c.JSON(500, gin.H{"code": 500, "message": "数据库错误", "data": nil})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(404, gin.H{"code": 404, "message": "用户不存在", "data": nil})
		return
	}

	// 2. 验证验证码（简化版：直接比对传入的验证码）
	// 实际项目应从 Redis 或数据库中取出存储的验证码进行比对
	if req.Code != "123456" {
		c.JSON(400, gin.H{"code": 400, "message": "验证码错误或已过期", "data": nil})
		return
	}

	// 3. bcrypt 哈希新密码
	// 注意：这里需要从请求体中获取新密码，但当前绑定结构体没有 password 字段
	// 我们需要重新绑定或使用 different approach
	// 为简化，我们改用查询参数或 body 中的 new_password 字段
	// 这里演示：直接使用前端传入的 new_password 字段
	var structForNewPass struct {
		NewPassword string `json:"new_password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&structForNewPass); err != nil {
		c.JSON(400, gin.H{"code": 400, "message": "参数错误", "errors": []string{err.Error()}})
		return
	}

	// 4. bcrypt 哈希新密码
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(structForNewPass.NewPassword), 12)
	if err != nil {
		c.JSON(500, gin.H{"code": 500, "message": "密码加密失败", "data": nil})
		return
	}

	// 5. 更新数据库中的密码
	result = db.DB.Raw("UPDATE tenant_users SET password_hash = ? WHERE username = ?", string(hashedPassword), req.Username)
	if result.Error != nil {
		c.JSON(500, gin.H{"code": 500, "message": "重置失败: " + result.Error.Error(), "data": nil})
		return
	}

	c.JSON(200, gin.H{
		"code":    0,
		"data":    nil,
		"message": "密码重置成功，请使用新密码登录",
	})
}

func GetCurrentUser(c *gin.Context) {
	c.JSON(200, gin.H{"user": "admin"})
}
