// WebSocket 实时推送端点：顾问端与C端的新消息实时通知。
package api

// ============================================================
// WebSocket 实时推送端点（P1-2，2026-08-29）
//
// GET /api/v1/ws/advisor?token=<JWT>  顾问端：接收本租户全部客户新消息通知
// GET /api/v1/ws/client?customer_id=&visitor_key=  C端：接收自身新消息通知（C3 越权防线）
//
// 推送仅作体验增强（触发前端即时拉取），既有 5s 轮询保留为兜底，不影响正确性。
// 使用 golang.org/x/net/websocket（已在依赖），避免引入新联网依赖。
// ============================================================

import (
	"encoding/json"
	"net/http"
	"strconv"

	"ai-scrm/internal/db"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
	"ai-scrm/internal/realtime"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/websocket"
)

// notifyWS 向实时 hub 推送一条新消息事件（仅信号）
// best-effort 设计：hub 未初始化时静默跳过，不阻塞主流程
// 在发消息、收消息等场景调用，触发 WebSocket 推送到前端
func notifyWS(tenantID, customerID, conversationID uint, senderType string) {
	if realtime.DefaultHub == nil {
		return
	}
	ev := realtime.RealtimeEvent{
		Type:           "new_message",
		CustomerID:     customerID,
		ConversationID: conversationID,
		SenderType:     senderType,
		TenantID:       tenantID,
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	realtime.DefaultHub.Publish(ev, b)
}

// notifyWSWithContent 向实时 hub 推送消息内容（P1-1 增强版）
// 与 notifyWS 类似，但推送完整消息体，前端收到即更新本地状态
func notifyWSWithContent(tenantID, customerID, conversationID uint, senderType string,
	messageID uint, content, senderName, createdAt string) {
	if realtime.DefaultHub == nil {
		return
	}
	ev := realtime.RealtimeMessage{
		Type:           "message_content",
		CustomerID:     customerID,
		ConversationID: conversationID,
		SenderType:     senderType,
		TenantID:       tenantID,
		MessageID:      messageID,
		Content:        content,
		SenderName:     senderName,
		CreatedAt:      createdAt,
	}
	realtime.DefaultHub.PublishWithContent(ev)
}

// notifyWSTyping 向实时 hub 推送"正在输入"状态（P1-1）
func notifyWSTyping(tenantID, customerID, conversationID uint, isTyping bool) {
	if realtime.DefaultHub == nil {
		return
	}
	realtime.DefaultHub.PublishTyping(tenantID, customerID, conversationID, isTyping)
}

// WSAdvisor 顾问端 WebSocket 连接
// GET /api/v1/ws/advisor?token=<JWT>
// 顾问登录后建立 WS 长连接，接收本租户所有客户的新消息通知
// JWT 通过 query 参数传递（浏览器 WebSocket API 不支持自定义 header）
func WSAdvisor(c *gin.Context) {
	// 从 query 参数获取 JWT token
	token := c.Query("token")
	if token == "" {
		respFailStatus(c, http.StatusUnauthorized, CodeUnauthorized, "缺少 token")
		return
	}
	// 解析 JWT 获取用户身份和租户信息
	claims, err := middleware.ParseToken(token)
	if err != nil {
		respFailStatus(c, http.StatusUnauthorized, CodeUnauthorized, "token 无效")
		return
	}
	// 升级 HTTP 连接为 WebSocket，注册客户端到 Hub
	handler := websocket.Handler(func(ws *websocket.Conn) {
		cl := realtime.NewClient(claims.TenantID, claims.UserID, 0)
		realtime.DefaultHub.Register(cl)
		defer realtime.DefaultHub.Unregister(cl)
		// 写泵：从客户端发送队列读取消息并推送到 WebSocket
		done := make(chan struct{})
		go func() {
			for msg := range cl.SendQueue() {
				if err := websocket.Message.Send(ws, msg); err != nil {
					close(done)
					return
				}
			}
		}()
		// 读泵：持续读取客户端消息（丢弃内容），仅用于检测连接断开
		buf := ""
		for {
			if err := websocket.Message.Receive(ws, &buf); err != nil {
				return
			}
		}
	})
	handler.ServeHTTP(c.Writer, c.Request)
}

// WSClient C端（客户）WebSocket 连接
// GET /api/v1/ws/client?customer_id=&visitor_key=
// 客户建立 WS 连接后接收自身的新消息通知
// 采用 visitor_key + customer_id 双重校验防止越权访问
func WSClient(c *gin.Context) {
	cid, _ := strconv.ParseUint(c.Query("customer_id"), 10, 64)
	vk := c.Query("visitor_key")
	if cid == 0 || vk == "" {
		respFailStatus(c, http.StatusBadRequest, CodeParamErr, "缺少 customer_id/visitor_key")
		return
	}
	// 校验客户身份：visitor_key 是客户身份凭证，必须匹配
	var cust model.Customer
	if err := db.RQ(c).First(&cust, uint(cid)).Error; err != nil || cust.VisitorKey != vk {
		respFailStatus(c, http.StatusForbidden, CodeForbidden, "客户身份校验失败")
		return
	}
	// 升级 HTTP 连接为 WebSocket，注册到 Hub（仅监听该客户的消息）
	handler := websocket.Handler(func(ws *websocket.Conn) {
		cl := realtime.NewClient(cust.TenantID, 0, uint(cid))
		realtime.DefaultHub.Register(cl)
		defer realtime.DefaultHub.Unregister(cl)
		// 写泵：推送消息到客户端
		go func() {
			for msg := range cl.SendQueue() {
				if err := websocket.Message.Send(ws, msg); err != nil {
					return
				}
			}
		}()
		// 读泵：检测连接断开
		buf := ""
		for {
			if err := websocket.Message.Receive(ws, &buf); err != nil {
				return
			}
		}
	})
	handler.ServeHTTP(c.Writer, c.Request)
}
