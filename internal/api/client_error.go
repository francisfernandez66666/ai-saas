// 前端异常上报（C6）：浏览器/React 崩溃事件复用 feedbacks 表，target_type=client_error。
// 原则：不落原始 PII；message/stack 过手机号掩码；context 只存 route/UA/采样窗口等定位信息。
package api

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
	"ai-scrm/internal/service"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// clientErrorReq 前端异常上报请求体
type clientErrorReq struct {
	Message   string `json:"message" binding:"required"`
	Stack     string `json:"stack"`
	Route     string `json:"route"`
	UserAgent string `json:"user_agent"`
	Page      string `json:"page"`
	App       string `json:"app"`
	Timestamp int64  `json:"timestamp"`
}

// ClientErrorReport POST /api/v1/client-errors
func ClientErrorReport(c *gin.Context) {
	var req clientErrorReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误")
		return
	}
	req.Message = strings.TrimSpace(service.MaskPhoneInText(truncateRunes(req.Message, 500)))
	if req.Message == "" {
		RespErr(c, http.StatusBadRequest, 400, "message 不能为空")
		return
	}
	req.Stack = strings.TrimSpace(service.MaskPhoneInText(truncateRunes(req.Stack, 4000)))
	req.Route = strings.TrimSpace(req.Route)
	req.UserAgent = truncateRunes(req.UserAgent, 300)
	req.Page = truncateRunes(req.Page, 500)
	if len(req.App) > 50 {
		req.App = req.App[:50]
	}

	ctx := map[string]string{}
	if req.Route != "" {
		ctx["route"] = req.Route
	}
	if req.Page != "" {
		ctx["page"] = req.Page
	}
	if req.UserAgent != "" {
		ctx["user_agent"] = req.UserAgent
	}
	if req.App != "" {
		ctx["app"] = req.App
	}
	if req.Timestamp > 0 {
		ctx["client_ts"] = time.UnixMilli(req.Timestamp).Format(time.RFC3339)
	}
	b, _ := json.Marshal(ctx)
	uidV, _ := c.Get("user_id")

	fb := model.Feedback{
		TenantID:   middleware.EffectiveTenantID(c),
		UserID:     toUintSafe(uidV),
		TargetType: "client_error",
		Context:    string(b),
		Content:    req.Message,
	}
	// Content 列保留短摘要；完整 stack 进 Context，避免单列超长且便于统一反馈列表。
	if req.Stack != "" {
		ctx["stack"] = req.Stack
		b2, _ := json.Marshal(ctx)
		fb.Context = string(b2)
	}
	if err := db.DB.Create(&fb).Error; err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "上报失败")
		return
	}
	RespOK(c, "ok", gin.H{"id": fb.ID})
}
