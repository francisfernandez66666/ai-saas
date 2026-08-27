package api

// 数据飞轮素材池管理API（P3）：素材列表、人工评审(approved/rejected/pending)、AI evals自动评分。
// 两层评分互不干扰(human_score人工/ai_score自动)，入库以人工审核为准，approved且pack_code非空进入行业包。

// ============================================================
// 数据飞轮：素材池管理 + AI evals 评分（P3，2026-08-26）
//
// GET  /api/v1/super/materials?status=&source=&page=   素材池列表（超管）
// POST /api/v1/super/materials/:id/review             人工评审 {status,human_score?,pack_code?}
// POST /api/v1/super/materials/:id/evals              触发 AI evals 自动评分（stage=evals）
//
// 两层评分互不干扰：human_score（结果导向，人工）/ ai_score（自动评估）各自独立；
// 初期入库以人工审核为准——approved 且 pack_code 非空 = 进入行业包迭代素材库。
// ============================================================

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"ai-scrm/internal/ai"
	"ai-scrm/internal/db"
	"ai-scrm/internal/llm"
	"ai-scrm/internal/model"

	"github.com/gin-gonic/gin"
)

// SuperMaterialList GET /api/v1/super/materials
func SuperMaterialList(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	q := db.DB.Model(&model.KbFeedbackMaterial{})
	if st := c.Query("status"); st != "" {
		q = q.Where("status = ?", st)
	}
	if src := c.Query("source"); src != "" {
		q = q.Where("source = ?", src)
	}
	var total int64
	q.Count(&total)
	var rows []model.KbFeedbackMaterial
	q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows)
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{
		"list": rows, "total": total, "page": page, "page_size": pageSize,
	}})
}

// SuperMaterialReview POST /api/v1/super/materials/:id/review
func SuperMaterialReview(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		Status     string `json:"status" binding:"required"` // approved/rejected/pending
		HumanScore *int   `json:"human_score"`
		PackCode   string `json:"pack_code"`
		DealClosed *bool  `json:"deal_closed"`
	}
	if err := c.ShouldBindJSON(&req); err != nil ||
		(req.Status != "approved" && req.Status != "rejected" && req.Status != "pending") {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "status 必须为 approved/rejected/pending"})
		return
	}
	if req.HumanScore != nil && (*req.HumanScore < 1 || *req.HumanScore > 5) {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "human_score 取值 1-5"})
		return
	}
	updates := map[string]interface{}{"status": req.Status}
	if req.HumanScore != nil {
		updates["human_score"] = *req.HumanScore
	}
	if req.DealClosed != nil {
		updates["deal_closed"] = *req.DealClosed
	}
	if req.PackCode != "" {
		updates["pack_code"] = req.PackCode
	}
	res := db.DB.Model(&model.KbFeedbackMaterial{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil || res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "素材不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "已更新"})
}

// SuperMaterialEvals POST /api/v1/super/materials/:id/evals
// AI evals：走 stage_models 的 evals 阶段专属模型（后台可配仅超管），与对话链路隔离
func SuperMaterialEvals(c *gin.Context) {
	id := c.Param("id")
	var m model.KbFeedbackMaterial
	if err := db.DB.First(&m, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "素材不存在"})
		return
	}
	prompt := "你是销售话术质量评估器。对下面这条汽车销售回复打分(0-5，支持一位小数)，并给出不超过50字的理由。" +
		"评估维度：口语自然度、需求针对性、推进有效性。只输出 JSON：{\"score\":数字,\"note\":\"理由\"}\n\n回复内容：\n" + m.Content
	messages := []ai.ChatMessage{{Role: "user", Content: prompt}}
	// H5修复(2026-08-27)：归位 llm 层唯一入口，按素材所属租户落 usage 计量，不计次配额
	reply, err := llm.GenerateEvalsText(m.TenantID, messages, 0.2)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "evals 模型调用失败: " + err.Error()})
		return
	}
	// 宽松解析 JSON（截取首个 { 到末个 }）
	var out struct {
		Score float64 `json:"score"`
		Note  string  `json:"note"`
	}
	start := -1
	for i := 0; i < len(reply); i++ {
		if reply[i] == '{' {
			start = i
			break
		}
	}
	if start >= 0 {
		end := strings.LastIndex(reply, "}")
		if end > start {
			_ = json.Unmarshal([]byte(reply[start:end+1]), &out)
		}
	}
	if out.Score < 0 || out.Score > 5 {
		out.Score = 0
		out.Note = "解析失败原始输出: " + reply
	}
	db.DB.Model(&m).Updates(map[string]interface{}{
		"ai_score": out.Score, "ai_eval_note": out.Note,
	})
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "AI 评分完成",
		"data": gin.H{"score": out.Score, "note": out.Note}})
}
