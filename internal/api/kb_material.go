// 数据飞轮素材池API：素材评审与 AI evals 自动评分管理。
package api

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
	"ai-scrm/internal/engine/strategy"
	"ai-scrm/internal/model"
	"ai-scrm/internal/service"

	"github.com/gin-gonic/gin"
)

/*
SuperMaterialList 处理 GET /api/v1/super/materials 请求，返回素材池分页列表。
支持按状态(status)和来源(source)筛选，供超级管理员查看所有素材的评审状态。
参数：c - Gin请求上下文，通过query参数传递筛选条件和分页信息
返回：分页后的素材列表，包含total、page、page_size等分页元数据
*/
func SuperMaterialList(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	// 超管专属(SuperRequired 守卫)跨租户素材池列表：全平台语义（rls_scope_test 已验证）
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
	RespOK(c, "", gin.H{
		"list": rows, "total": total, "page": page, "page_size": pageSize,
	})
}

// SuperMaterialReview 处理 POST /api/v1/super/materials/:id/review 请求，执行人工评审操作。
// 评审状态只能为 approved（通过）、rejected（拒绝）或 pending（待处理）。
// 支持可选的人工评分(human_score)、行业包代码(pack_code)和成交标记(deal_closed)。
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
		RespErr(c, http.StatusBadRequest, 400, "status 必须为 approved/rejected/pending")
		return
	}
	if req.HumanScore != nil && (*req.HumanScore < 1 || *req.HumanScore > 5) {
		RespErr(c, http.StatusBadRequest, 400, "human_score 取值 1-5")
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
		RespErr(c, http.StatusNotFound, 404, "素材不存在")
		return
	}
	RespOK(c, "已更新", nil)
}

// SuperMaterialEvals 处理 POST /api/v1/super/materials/:id/evals 请求，触发AI自动评分。
// 该函数通过策略引擎调用evals阶段专属模型，与对话链路完全隔离。
// 评分维度包括：口语自然度、需求针对性、推进有效性，输出0-5分及简短理由。
// 同时执行离线评分作为零成本护栏，与LLM评估结果互补验证。
func SuperMaterialEvals(c *gin.Context) {
	id := c.Param("id")
	var m model.KbFeedbackMaterial
	if err := db.DB.First(&m, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "素材不存在")
		return
	}
	prompt := "你是销售话术质量评估器。对下面这条汽车销售回复打分(0-5，支持一位小数)，并给出不超过50字的理由。" +
		"评估维度：口语自然度、需求针对性、推进有效性。只输出 JSON：{\"score\":数字,\"note\":\"理由\"}\n\n回复内容：\n" + m.Content
	messages := []ai.ChatMessage{{Role: "user", Content: prompt}}
	// 架构红线修复(2026-08-30)：经策略引擎桥接 llm，业务层不再直连 internal/llm
	reply, err := strategy.GenerateEvals(m.TenantID, messages, 0.2)
	if err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "evals 模型调用失败: "+err.Error())
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
	// 离线评分（零成本护栏，与 LLM evals 阶段互补）
	auto := service.ScoreReplyOffline(m.Content, nil, nil)
	RespOK(c, "AI 评分完成", gin.H{"score": out.Score, "note": out.Note, "auto_score": auto.Score, "auto_reasons": auto.Reasons})
}
