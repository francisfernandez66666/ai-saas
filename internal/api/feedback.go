package api

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

type feedbackReq struct {
	TargetType  string `json:"target_type"` // ai_reply|feature|other（缺省 ai_reply）
	RefID       uint   `json:"ref_id"`      // 关联消息ID
	Content     string `json:"content" binding:"required"`
	WithContext bool   `json:"with_context"`
}

// CreateFeedback POST /api/v1/feedback
func CreateFeedback(c *gin.Context) {
	var req feedbackReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "意见内容不能为空"})
		return
	}
	if len([]rune(req.Content)) > 1000 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "意见最多1000字"})
		return
	}
	if req.TargetType == "" {
		req.TargetType = "ai_reply"
	}

	uid, username, _ := middleware.CurrentUser(c)
	tid := tenantIDOf(c)

	// 限流：同用户每日 20 条（查表实现，免新表）
	var today int64
	db.DB.Model(&model.Feedback{}).
		Where("user_id = ? AND created_at >= CURRENT_DATE", uid).
		Count(&today)
	if today >= feedbackDailyLimit {
		c.JSON(http.StatusTooManyRequests, gin.H{"code": 429, "message": "今日反馈已达上限（20条），请明日再试"})
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
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "提交失败"})
		return
	}

	service.NotifyWecom(fmt.Sprintf("【用户反馈】%s：%s「%s」",
		username, targetTypeLabel(req.TargetType), truncateRunes(req.Content, 60)))
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "反馈已提交，感谢你的意见", "data": fb})
}

// SuperFeedbackList GET /api/v1/super/feedbacks?status=&page=
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
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{
		"total": total, "page": page, "page_size": pageSize, "list": rows,
	}})
}

// SuperResolveFeedback POST /api/v1/super/feedbacks/resolve {id, note}
func SuperResolveFeedback(c *gin.Context) {
	var req struct {
		ID   uint   `json:"id" binding:"required"`
		Note string `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误：id 必填"})
		return
	}
	now := time.Now()
	uidV, _ := c.Get("user_id")
	res := db.DB.Model(&model.Feedback{}).
		Where("id = ? AND status = 'open'", req.ID).
		Updates(map[string]interface{}{
			"status": "resolved", "handle_note": req.Note,
			"handled_by": toUintSafe(uidV), "handled_at": now,
		})
	if res.Error != nil || res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "反馈不存在或已处理"})
		return
	}
	writeAuditSimple(c, 0, "feedback_resolve", fmt.Sprintf("feedback:%d", req.ID))
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "已标记处理完成"})
}

// buildFeedbackContext 反馈上下文采集（脱敏）：AI回复摘要 + 客户掩码手机
// 明文手机号不出库；查不到关联消息时返回空（不阻断提交）
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
		phone := cust.Phone
		if len(phone) == 11 {
			phone = phone[:3] + "****" + phone[7:]
		}
		ctx["customer_masked"] = cust.Name + "/" + phone
		ctx["journey_stage"] = cust.JourneyStage
	}
	b, _ := json.Marshal(ctx)
	return string(b)
}

// targetTypeLabel 反馈类型中文标签（企微推送/列表展示用）
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

// truncateRunes 按字符截断（rune安全，中文不切半），超长追加省略号
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
