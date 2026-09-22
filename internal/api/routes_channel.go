// 通道回调公开路由注册（W3-5，2026-09-12）——必须在 v1.Use(JWTAuth) 之前挂到 v1 上。
package api

import (
	"time"

	"github.com/gin-gonic/gin"

	"ai-scrm/internal/middleware"
)

// registerChannelCallbacks 渠道回调入口（公开，靠签名验证）：GET=URL验证，POST=消息/事件。
// P1-7 修复(2026-09-15)：挂 IP 限流——旧实现无频控，匿名者可对自增 :id 枚举通道
// （404 不存在/403 验签失败/200 通过 三态即存在性 oracle），且每请求强制
// base64+AES+XML 解析，可匿名施加 CPU 成本。600/min 远高于微信/企微正常推送峰值
// （高活跃租户突发也有量级），保留止重推语义。
func registerChannelCallbacks(v1 *gin.RouterGroup) {
	cb := v1.Group("/channel/callback", middleware.IPRateLimit("channel_callback", 600, time.Minute))
	cb.GET("/:id", ChannelCallbackVerify)
	cb.POST("/:id", ChannelCallbackReceive)
	// 注册期鉴权记录：渠道回调公开，靠签名验证 + IP 限流（整组共享）。
	RecordAuth("*", "/api/v1/channel/callback", "channel_signature", "ip_limit")
}

// registerChannelAuthed 通道鉴态路由（W7，须在 v1.Use(JWTAuth) 之后注册）：侧边栏 JS 配置 + 客户上下文。
func registerChannelAuthed(v1 *gin.RouterGroup) {
	g := v1.Group("/channel/wecom")
	g.GET("/jsconfig", ChannelWecomJSConfig)
	g.GET("/context", ChannelWecomContext)
	// 注册期鉴权记录：通道鉴态需登录。
	RecordAuth("*", "/api/v1/channel/wecom", "jwt")
}
