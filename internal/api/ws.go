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

// notifyWS 向实时 hub 推送一条新消息事件（best-effort，hub 未初始化亦安全）
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

// WSAdvisor 顾问端 WS（JWT 经 query 传递，因浏览器 WS 难附加 header）
func WSAdvisor(c *gin.Context) {
	token := c.Query("token")
	if token == "" {
		respFailStatus(c, http.StatusUnauthorized, CodeUnauthorized, "缺少 token")
		return
	}
	claims, err := middleware.ParseToken(token)
	if err != nil {
		respFailStatus(c, http.StatusUnauthorized, CodeUnauthorized, "token 无效")
		return
	}
	handler := websocket.Handler(func(ws *websocket.Conn) {
		cl := realtime.NewClient(claims.TenantID, claims.UserID, 0)
		realtime.DefaultHub.Register(cl)
		defer realtime.DefaultHub.Unregister(cl)
		// 写泵
		done := make(chan struct{})
		go func() {
			for msg := range cl.SendQueue() {
				if err := websocket.Message.Send(ws, msg); err != nil {
					close(done)
					return
				}
			}
		}()
		// 读泵（丢弃客户端消息，仅保活）
		buf := ""
		for {
			if err := websocket.Message.Receive(ws, &buf); err != nil {
				return
			}
		}
	})
	handler.ServeHTTP(c.Writer, c.Request)
}

// WSClient C 端 WS（visitor_key + customer_id 双重校验，防越权）
func WSClient(c *gin.Context) {
	cid, _ := strconv.ParseUint(c.Query("customer_id"), 10, 64)
	vk := c.Query("visitor_key")
	if cid == 0 || vk == "" {
		respFailStatus(c, http.StatusBadRequest, CodeParamErr, "缺少 customer_id/visitor_key")
		return
	}
	var cust model.Customer
	if err := db.RQ(c).First(&cust, uint(cid)).Error; err != nil || cust.VisitorKey != vk {
		respFailStatus(c, http.StatusForbidden, CodeForbidden, "客户身份校验失败")
		return
	}
	handler := websocket.Handler(func(ws *websocket.Conn) {
		cl := realtime.NewClient(cust.TenantID, 0, uint(cid))
		realtime.DefaultHub.Register(cl)
		defer realtime.DefaultHub.Unregister(cl)
		go func() {
			for msg := range cl.SendQueue() {
				if err := websocket.Message.Send(ws, msg); err != nil {
					return
				}
			}
		}()
		buf := ""
		for {
			if err := websocket.Message.Receive(ws, &buf); err != nil {
				return
			}
		}
	})
	handler.ServeHTTP(c.Writer, c.Request)
}
