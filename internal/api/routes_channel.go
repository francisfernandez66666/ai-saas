// 通道回调公开路由注册（W3-5，2026-09-12）——必须在 v1.Use(JWTAuth) 之前挂到 v1 上。
package api

import "github.com/gin-gonic/gin"

// registerChannelCallbacks 渠道回调入口（公开，靠签名验证）：GET=URL验证，POST=消息/事件。
func registerChannelCallbacks(v1 *gin.RouterGroup) {
	cb := v1.Group("/channel/callback")
	cb.GET("/:id", ChannelCallbackVerify)
	cb.POST("/:id", ChannelCallbackReceive)
}

// registerChannelAuthed 通道鉴态路由（W7，须在 v1.Use(JWTAuth) 之后注册）：侧边栏 JS 配置 + 客户上下文。
func registerChannelAuthed(v1 *gin.RouterGroup) {
	g := v1.Group("/channel/wecom")
	g.GET("/jsconfig", ChannelWecomJSConfig)
	g.GET("/context", ChannelWecomContext)
}
