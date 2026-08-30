// 用户反馈API：登录用户提交反馈、超管列表与标记处理。
package api

// 用户反馈API（商业化第二批M2）：登录用户提交反馈(20条/天限流)、超管分页列表与标记处理。
// 支持脱敏上下文采集(掩码手机号不出库)，新反馈推企微群催办。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
	"ai-scrm/internal/service"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 用户反馈 API（商业化第二批 M2，借鉴翻译助手四期 §五）
//
// POST /api/v1/feedback                登录用户提交（20条/天限流）
// GET  /api/v1/super/feedbacks         超管分页列表（含租户/用户名）
// POST /api/v1/super/feedbacks/resolve 超管标记已处理
//
// 上下文采集：with_context=true 时后端自动抓取关联 AI 回复摘要+客户掩码手机，
// 明文手机号不出库；新反馈推企微群催办
// ============================================================

const feedbackDailyLimit = 20 // 单用户每日上限

// feedbackReq 反馈提交请求的结构体定义
type feedbackReq struct {
	TargetType  string `json:"target_type"` // 反馈类型：ai_reply（AI话术）、feature（功能建议）、other（其他）
	RefID       uint   `json:"ref_id"`      // 关联的消息ID，用于获取反馈上下文
	Content     string `json:"content" binding:"required"` // 反馈内容，必填
	WithContext bool   `json:"with_context"` // 是否需要自动采集反馈上下文（AI回复摘要+客户掩码手机）
}

/*
CreateFeedback 处理 POST /api/v1/feedback 请求，允许登录用户提交反馈。
反馈内容限制在1000字以内，支持可选的上下文采集功能。
系统会执行每日20条的限流策略，并在提交后通过企微群推送催办通知。
参数：c - Gin请求上下文，包含用户认证信息
返回：反馈提交结果，成功后返回反馈对象
*/
func CreateFeedback(c *gin.Context) {
	var req feedbackReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "意见内容不能为空")
		return
	}
	if len([]rune(req.Content)) > 1000 {
		RespErr(c, http.StatusBadRequest, 400, "意见最多1000字")
		return
	}
	if req.TargetType == "" {
		req.TargetType = "ai_reply"
	}

	uid, username, _ := middleware.CurrentUser(c)
	tid := tenantIDOf(c)

	// 限流：同用户每日 20 条（查表实现，免新表）
	var today int64
	db.RQ(c).Model(&model.Feedback{}).
		Where("user_id = ? AND created_at >= CURRENT_DATE", uid).
		Count(&today)
	if today >= feedbackDailyLimit {
		RespErr(c, http.StatusTooManyRequests, 429, "今日反馈已达上限（20条），请明日再试")
		return
	}

	fb := model.Feedback{
		TenantID:   tid,
		UserID:     uid,
		TargetType: req.TargetType,
		RefID:      req.RefID,
		Content:    req.Content,
		Status:     "open",
	}
	if req.WithContext {
		fb.Context = buildFeedbackContext(req.RefID)
	}
	if err := db.DB.Create(&fb).Error; err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "提交失败")
		return
	}

	service.NotifyWecom(fmt.Sprintf("【用户反馈】%s：%s「%s」",
		username, targetTypeLabel(req.TargetType), truncateRunes(req.Content, 60)))
	RespOK(c, "反馈已提交，感谢你的意见", fb)
}

/*
SuperFeedbackList 处理 GET /api/v1/super/feedbacks?status=&page= 请求，返回反馈分页列表。
供超级管理员查看所有租户的用户反馈，支持按状态筛选。
参数：c - Gin请求上下文，通过query参数传递筛选条件和分页信息
返回：分页后的反馈列表，包含租户名称、用户名等关联信息
*/
func SuperFeedbackList(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	q := db.DB.Table("feedbacks f").
		Select(`f.id, COALESCE(f.tenant_id,0) AS tenant_id, COALESCE(t.name,'') AS tenant_name,
			COALESCE(u.username,'') AS username, f.target_type, f.ref_id,
			COALESCE(f.context,'') AS context, f.content, f.status,
			COALESCE(f.handle_note,'') AS handle_note, f.created_at, f.handled_at`).
		Joins("LEFT JOIN tenants t ON f.tenant_id = t.id").
		Joins("LEFT JOIN tenant_users u ON f.user_id = u.id")

	if status := c.Query("status"); status != "" {
		q = q.Where("f.status = ?", status)
	}

	var total int64
	q.Count(&total)

	rows := []map[string]interface{}{}
	if err := q.Order("f.id DESC").Offset((page - 1) * pageSize).Limit(pageSize).
		Scan(&rows).Error; err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "查询失败")
		return
	}
	RespOK(c, "", gin.H{
		"total": total, "page": page, "page_size": pageSize, "list": rows,
	})
}

/*
SuperResolveFeedback 处理 POST /api/v1/super/feedbacks/resolve 请求，标记反馈为已处理。
仅允许处理状态为"open"的反馈，处理后记录处理人、处理时间和备注信息。
参数：c - Gin请求上下文，包含请求体{id, note}
返回：处理结果，成功后记录审计日志
*/
func SuperResolveFeedback(c *gin.Context) {
	var req struct {
		ID   uint   `json:"id" binding:"required"`
		Note string `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误：id 必填")
		return
	}
	now := time.Now()
	uidV, _ := c.Get("user_id")
	// TODO-RLS: verify tenant scope (super/platform cross-tenant path)
	res := db.DB.Model(&model.Feedback{}).
		Where("id = ? AND status = 'open'", req.ID).
		Updates(map[string]interface{}{
			"status": "resolved", "handle_note": req.Note,
			"handled_by": toUintSafe(uidV), "handled_at": now,
		})
	if res.Error != nil || res.RowsAffected == 0 {
		RespErr(c, http.StatusNotFound, 404, "反馈不存在或已处理")
		return
	}
	writeAuditSimple(c, 0, "feedback_resolve", fmt.Sprintf("feedback:%d", req.ID))
	RespOK(c, "已标记处理完成", nil)
}

/*
buildFeedbackContext 构建反馈上下文信息，用于辅助问题定位和复现。
当messageID不为0时，会查询关联的AI回复内容和客户信息。
客户手机号会进行脱敏处理，明文手机号不会出现在上下文中。
参数：messageID - 关联的消息ID，为0时返回空字符串
返回：JSON格式的上下文信息字符串，包含AI回复摘要、客户掩码手机和旅程阶段
*/
func buildFeedbackContext(messageID uint) string {
	if messageID == 0 {
		return ""
	}
	var msg model.Message
	if err := db.DB.Select("customer_id, content").First(&msg, messageID).Error; err != nil {
		return ""
	}
	ctx := map[string]string{"ai_reply_excerpt": truncateRunes(msg.Content, 120)}
	var cust model.Customer
	if err := db.DB.Select("name, phone, journey_stage").First(&cust, msg.CustomerID).Error; err == nil {
		phone := service.MaskPhone(cust.Phone)
		ctx["customer_masked"] = cust.Name + "/" + phone
		ctx["journey_stage"] = cust.JourneyStage
	}
	b, _ := json.Marshal(ctx)
	return string(b)
}

// targetTypeLabel 将反馈类型代码转换为中文标签，用于企微推送和列表展示
func targetTypeLabel(t string) string {
	switch t {
	case "ai_reply":
		return "AI话术"
	case "feature":
		return "功能建议"
	default:
		return "其他"
	}
}

// truncateRunes 按字符截断字符串，确保中文字符不会被切断
// 当字符串长度超过指定限制时，在末尾添加省略号
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
