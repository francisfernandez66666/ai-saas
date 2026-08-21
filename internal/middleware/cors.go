package middleware

import (
	"github.com/gin-gonic/gin"
)

// ============================================================
// CORS跨域中间件
// 为什么需要？前端开发时和后端不在同一端口，需要跨域支持
// ============================================================

// CORS 跨域处理中间件
// 允许所有来源，方便开发调试
// 生产环境建议限制具体域名
func CORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 允许的来源
		origin := c.Request.Header.Get("Origin")
		if origin == "" {
			origin = "*"
		}

		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, PATCH")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, X-Requested-With")
		c.Header("Access-Control-Allow-Credentials", "true")
		c.Header("Access-Control-Max-Age", "86400") // 预检请求缓存24小时

		// 处理OPTIONS预检请求
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}
