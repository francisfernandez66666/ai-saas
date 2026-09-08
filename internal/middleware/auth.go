// Package middleware Gin 中间件链：TenantResolver(fail-closed)→JWTAuth→TenantConsistency→OrgResolve 等安全闸。
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

// OptionalJWTAuth 可选鉴权中间件（2026-09-08 修复 /chat/history 越权/403 双端契约）
//
// 背景：/api/v1/chat/history 同时服务匿名 C 端（visitor_key 鉴权）与登录 B 端（JWT）。
// 原实现该路由注册在 v1.Use(JWTAuth...) 之前，永远不跑 JWTAuth → CheckVisitorKey 的
// 登录态分支（依赖 c.Get("user_id")）恒不命中 → 顾问/管理员拉聊天记录一律 403。
//
// 语义：携带合法 Bearer token 时注入 claims 上下文（user_id/role/tenant_id），
// 匿名/无 token/坏 token 一律放行（交由调用方 visitor_key 校验兜底），
// 不拒绝任何请求——它只是「有则注入」，不是鉴权闸。
func OptionalJWTAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader != "" {
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) == 2 && parts[0] == "Bearer" {
				if claims, err := ParseToken(parts[1]); err == nil {
					c.Set("user_id", claims.UserID)
					c.Set("username", claims.Username)
					c.Set("role", claims.Role)
					c.Set("tenant_id", claims.TenantID)
				}
				// 坏 token 不拒绝：匿名路径（visitor_key）仍由调用方校验，避免误伤 C 端
			}
		}
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
