// Package middleware Gin 中间件链：TenantResolver(fail-closed)→JWTAuth→TenantConsistency→OrgResolve 等安全闸。
package middleware

import (
	"ai-scrm/config"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"ai-scrm/internal/db"
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
	// TV B4 修复(2026-09-14)：签发时的用户 token_version 快照。改密/重置/换绑邮箱
	// 递增库内版本后，旧 token（TV 落后）即时失效——此前无任何吊销机制，
	// 密码泄露后改密并不能踢掉攻击者会话。
	TV uint `json:"tv,omitempty"`
	jwt.RegisteredClaims
}

// GenerateToken 生成JWT Token
// userID: 用户ID
// username: 用户名
// role: 用户角色
// tenantID: 租户ID，0=超级管理员，非0=某租户下用户
// tokenVersion（可选，B4）：签发时用户的 token_version；改密/重置/换绑后旧 token 即失效
func GenerateToken(userID uint, username string, role string, tenantID uint, tokenVersion ...uint) (string, error) {
	expireHours := config.GlobalConfig.JWT.ExpireHours
	var tv uint
	if len(tokenVersion) > 0 {
		tv = tokenVersion[0]
	}
	claims := Claims{
		UserID:   userID,
		Username: username,
		Role:     role,
		TenantID: tenantID,
		TV:       tv,
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
	// 纵深防御(2026-09-15 增强批)：显式算法白名单 HS256——v5 默认虽已拒 none/RSA 混淆，
	// 但 WithValidMethods 把"签名算法不可协商"写死在代码里，防未来换 keyfunc 实现时退化。
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		return []byte(config.GlobalConfig.JWT.Secret), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))

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
		c.Set("token_tv", claims.TV) // B4：供 MustChangePasswordGuard 做吊销版本核对

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
					// P0-6 复核批(2026-09-15)：B4 吊销核对补齐——OptionalJWTAuth 此前
					// 解析成功即注入身份，改密/换绑后被吊销的旧 token 在整个 JWT exp
					// 窗口内仍能经 /chat/history、/client-errors 等路径读租户数据。
					// 核对不过（已吊销/查不到/DB 故障）一律降级为匿名上下文：请求仍放行，
					// 由调用方 visitor_key 兜底——「有则注入」的语义不变，但注入的是可信身份。
					if revoked := TokenRevoked(claims.UserID, claims.TV); !revoked {
						c.Set("user_id", claims.UserID)
						c.Set("username", claims.Username)
						c.Set("role", claims.Role)
						c.Set("tenant_id", claims.TenantID)
					}
				}
				// 坏 token 不拒绝：匿名路径（visitor_key）仍由调用方校验，避免误伤 C 端
			}
		}
		c.Next()
	}
}

// tokenRevoked 核对库内 token_version 是否已高于 token 快照（true=已吊销/不可信）。
// P0-6 复核批(2026-09-15)：与 MustChangePasswordGuard 同口径（row.TV > claims.TV 即失效），
// fail-closed——查不到用户或 DB 故障按不可信处理（调用方决定 401 还是降级匿名）。
// uid==0（平台系统层身份）无 tenant_users 行，恒不吊销（与 guard 的 uid==0 跳过一致）。
func TokenRevoked(uid, claimsTV uint) bool {
	if uid == 0 {
		return false
	}
	if db.DB == nil {
		// DB 未初始化（单元测试/启动早期）无从核对——返回"未吊销"：
		// DB 挂时一切租户数据路径本就 500，此处 fail-open 不产生额外暴露窗口，
		// 且避免 nil *gorm.DB 裸查询 panic 把 401/降级变成 500。
		log.Printf("[auth][WARN] TokenRevoked: DB 未初始化，跳过吊销核对")
		return false
	}
	var row struct {
		TokenVersion uint
	}
	err := db.DB.Table("tenant_users").
		Select("COALESCE(token_version,0) AS token_version").
		Where("id = ?", uid).Scan(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return true
		}
		return true // DB 故障 fail-closed：宁可误拒旧会话，不给吊销留窗口
	}
	return row.TokenVersion > claimsTV
}

// TokenRevocationCheck B4 吊销硬核对中间件（P0-6 复核批，2026-09-15）。
// 供挂在 v1.Use 主链之外的旁挂路由（/auth/email/* 等在 Use 之前注册、拿不到
// MustChangePasswordGuard 的链）——旧 token 被吊销后不得再换绑邮箱/发验证码，
// 否则"改密驱逐攻击者"可被旧 token 反转为账号接管。须挂在 JWTAuth 之后。
func TokenRevocationCheck() gin.HandlerFunc {
	return func(c *gin.Context) {
		claimsTV := uint(0)
		if v, ok := c.Get("token_tv"); ok {
			claimsTV = toUint(v)
		}
		uidV, _ := c.Get("user_id")
		uid := toUint(uidV)
		if uid == 0 {
			c.Next()
			return
		}
		if TokenRevoked(uid, claimsTV) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"code": 401, "error_code": "token_revoked",
				"message": "账号凭据已更新，请重新登录", "data": nil,
			})
			return
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
