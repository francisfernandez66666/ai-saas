package middleware

import (
	"ai-scrm/config"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// ============================================================
// JWT认证中间件
// 为什么用JWT？无状态、跨域友好、适合前后端分离架构
// ============================================================

// Claims JWT载荷
type Claims struct {
	UserID   uint   `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	TenantID uint   `json:"tenant_id"` // 租户ID，0=系统管理员，非NULL=某租户下用户
	jwt.RegisteredClaims
}

// GenerateToken 生成JWT Token
// userID: 用户ID
// username: 用户名
// role: 用户角色
// tenantID: 租户ID，0=超级管理员，非0=某租户下用户
func GenerateToken(userID uint, username string, role string, tenantID uint) (string, error) {
	expireHours := config.GlobalConfig.JWT.ExpireHours
	claims := Claims{
		UserID:   userID,
		Username: username,
		Role:     role,
		TenantID: tenantID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Duration(expireHours) * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "ai-scrm",
		},
	}

token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(config.GlobalConfig.JWT.Secret))
}

// ParseToken 解析JWT Token
func ParseToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		return []byte(config.GlobalConfig.JWT.Secret), nil
	})

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims, nil
	}

	return nil, jwt.ErrSignatureInvalid
}

// JWTAuth JWT认证中间件
// 从Authorization头中提取token，验证后将用户信息存入context
func JWTAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 从header中获取token
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"code":    401,
				"message": "未提供认证Token",
				"data":    nil,
			})
			c.Abort()
			return
		}

		// 解析 Bearer token
		parts := strings.SplitN(authHeader, " ", 2)
		if !(len(parts) == 2 && parts[0] == "Bearer") {
			c.JSON(http.StatusUnauthorized, gin.H{
				"code":    401,
				"message": "Token格式错误",
				"data":    nil,
			})
			c.Abort()
			return
		}

		// 验证token
		claims, err := ParseToken(parts[1])
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{
				"code":    401,
				"message": "Token无效或已过期",
				"data":    nil,
			})
			c.Abort()
			return
		}

		// 将用户信息存入上下文
		c.Set("user_id", claims.UserID)
		c.Set("username", claims.Username)
		c.Set("role", claims.Role)
		c.Set("tenant_id", claims.TenantID)

		c.Next()
	}
}

// AdminRequired 管理员权限中间件
// 需要先经过JWTAuth中间件
// 支持：super_admin(超级管理员) / tenant_admin(租户管理员) / admin(传统管理员，兼容旧数据)
func AdminRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		role, exists := c.Get("role")
		if !exists || !(role == "super_admin" || role == "tenant_admin" || role == "admin") {
			c.JSON(http.StatusForbidden, gin.H{
				"code":    403,
				"message": "需要管理员权限",
				"data":    nil,
			})
			c.Abort()
			return
		}
		c.Next()
	}
}

// CurrentUser 获取当前用户ID
func CurrentUser(c *gin.Context) (uint, string, string) {
	userID, _ := c.Get("user_id")
	username, _ := c.Get("username")
	role, _ := c.Get("role")
	return userID.(uint), username.(string), role.(string)
}
