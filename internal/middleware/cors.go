// Package middleware Gin 中间件链：TenantResolver(fail-closed)→JWTAuth→TenantConsistency→OrgResolve 等安全闸。
package middleware

import (
	"os"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// ============================================================
// CORS跨域中间件（H5 安全修复）
// 原实现反射任意 Origin 且允许凭证，构成 CSRF 向量；
// 现改为白名单：仅 CORS_ALLOWED_ORIGINS 列出的来源可跨域且带凭证，
// 其余来源不返回 ACAO，浏览器直接拒绝（安全默认）。
// 开发态默认放行 localhost:5173 / 127.0.0.1:5173（与 vite 代理一致）。
// ============================================================

// corsOrigins 跨域白名单集合（CORS_ALLOWED_ORIGINS 解析结果）；corsOriginsOnce 保证只初始化一次
var (
	corsOrigins     map[string]bool
	corsOriginsOnce sync.Once
)

// initCORS 从环境变量解析跨域白名单，并默认放行本地开发来源（localhost:5173），幂等单次执行
func initCORS() {
	corsOriginsOnce.Do(func() {
		corsOrigins = map[string]bool{}
		raw := os.Getenv("CORS_ALLOWED_ORIGINS")
		for _, o := range strings.Split(raw, ",") {
			o = strings.TrimSpace(o)
			if o != "" {
				corsOrigins[o] = true
			}
		}
		// 开发态常见来源默认放行（生产应通过 CORS_ALLOWED_ORIGINS 显式配置）
		for _, o := range []string{"http://localhost:5173", "http://127.0.0.1:5173"} {
			corsOrigins[o] = true
		}
	})
}

// CORS 跨域处理中间件
func CORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		initCORS()

		// 允许的来源（仅白名单内的具体来源，禁止反射任意 Origin）
		origin := c.Request.Header.Get("Origin")
		if origin != "" && corsOrigins[origin] {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
		} else {
			// 不在白名单：不返回 ACAO，浏览器将拒绝跨域读取响应（安全默认）
			c.Header("Access-Control-Allow-Origin", "")
		}

		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, PATCH")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, X-Requested-With")
		c.Header("Access-Control-Max-Age", "86400") // 预检请求缓存24小时

		// 处理OPTIONS预检请求
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}
