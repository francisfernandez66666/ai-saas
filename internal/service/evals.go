// 话术离线评分：零成本护栏，纯函数预筛 + 回归基线 + 批量评估。
package service

import (
	"context"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
)

// EvalResult 离线评分结果（与 LLM evals 阶段互补，零成本护栏）
// 用于存储销售话术离线评分的最终结果，包含0-5分的评分和扣分/加分理由
type EvalResult struct {
	Score   float64  // 0~5 综合评分，5分满分
	Reasons []string // 扣分/加分理由（短句），用于可解释性
}

// ScoreReplyOffline 销售话术离线评分（数据飞轮 evals 阶段）
// anchors：期望出现的关键词（需求针对性）；forbidden：禁止出现的泄露/违规词
// 纯函数、无外部依赖，可作为 LLM evals 的廉价预筛与回归基线
func ScoreReplyOffline(reply string, anchors, forbidden []string) EvalResult {
	r := EvalResult{Score: 5, Reasons: nil}
	text := strings.TrimSpace(reply)
	n := utf8.RuneCountInString(text)

	// 1) 长度：过短直接低分，过长温和扣
	if n == 0 {
		return EvalResult{Score: 0, Reasons: []string{"空回复"}}
	}
	if n < 10 {
		r.Score -= 2.5
		r.Reasons = append(r.Reasons, "回复过短")
	} else if n > 400 {
		r.Score -= 0.5
		r.Reasons = append(r.Reasons, "偏长")
	}

	// 2) 锚词命中：每命中一个 +0.4（上限 +1）
	hits := 0
	for _, a := range anchors {
		if a != "" && strings.Contains(text, a) {
			hits++
		}
	}
	if len(anchors) > 0 {
		add := float64(hits) * 0.4
		if add > 1 {
			add = 1
		}
		r.Score += add
		if hits == 0 {
			r.Reasons = append(r.Reasons, "未命中任何需求锚词")
		}
	}

	// 3) 违规词：命中即重扣
	for _, f := range forbidden {
		if f != "" && strings.Contains(text, f) {
			r.Score -= 2
			r.Reasons = append(r.Reasons, "含违规词:"+f)
		}
	}

	if r.Score < 0 {
		r.Score = 0
	}
	if r.Score > 5 {
		r.Score = 5
	}
	return r
}

// ============================================================
// 批量评估服务（P0-1 数据飞轮，2026-08-30）
//
// 目标：素材池 → 批量AI评估 → 人工审核 → 行业包迭代
// 数据流：kb_materials(待审) → BatchEvaluate → 更新score/status
// ============================================================

// BatchEvaluateResult 单条素材评估结果
type BatchEvaluateResult struct {
	MaterialID uint     `json:"material_id"`     // 素材ID
	Score      float64  `json:"score"`           // 评分（0-5）
	Reasons    []string `json:"reasons"`         // 评分理由
	Status     string   `json:"status"`          // 评估后状态：pending_review/approved/rejected
	Error      string   `json:"error,omitempty"` // 评估错误（如有）
}

// BatchEvaluate 批量离线评估素材池（零成本护栏 + LLM深度评估）
// 从 kb_feedback_materials 表读取待审素材，逐条评估，更新评分和状态
// 评估策略：
//  1. 纯函数预筛（ScoreReplyOffline）：空回复/过短/违规词 直接拒绝
//  2. LLM深度评估（可选）：stage_models 新增 evals 阶段模型
//  3. 人工审核兜底：score ≥ 3.5 进入待审，< 3.5 自动拒绝
func BatchEvaluate(tenantID uint, limit int) ([]BatchEvaluateResult, error) {
	if limit <= 0 {
		limit = 50
	}

	// 1. 读取待审素材
	var materials []model.KbFeedbackMaterial
	err := db.DB.Where("tenant_id = ? AND status = ?", tenantID, "pending").
		Order("id ASC").Limit(limit).Find(&materials).Error
	if err != nil {
		return nil, err
	}

	if len(materials) == 0 {
		return nil, nil
	}

	results := make([]BatchEvaluateResult, 0, len(materials))

	// 2. 逐条评估
	for _, m := range materials {
		result := evaluateSingleMaterial(tenantID, &m)
		results = append(results, result)

		// 3. 更新素材状态
		newStatus := "pending_review"
		if result.Score < 3.5 {
			newStatus = "rejected"
		}
		db.DB.Model(&model.KbFeedbackMaterial{}).Where("id = ?", m.ID).
			Updates(map[string]interface{}{
				"score":     result.Score,
				"status":    newStatus,
				"evaluated": true,
			})
	}

	log.Printf("[DataFlywheel] 批量评估完成 tenant=%d materials=%d", tenantID, len(results))
	return results, nil
}

// evaluateSingleMaterial 单条素材评估
func evaluateSingleMaterial(tenantID uint, m *model.KbFeedbackMaterial) BatchEvaluateResult {
	result := BatchEvaluateResult{
		MaterialID: m.ID,
		Status:     "pending_review",
	}

	// 1. 内容检查
	content := strings.TrimSpace(m.Content)
	if content == "" {
		result.Score = 0
		result.Reasons = []string{"空内容"}
		result.Status = "rejected"
		return result
	}

	// 2. 纯函数预筛
	anchors := []string{"车型", "配置", "价格", "试驾", "优惠"}
	forbidden := []string{"绝密", "内部", "禁止"}
	offline := ScoreReplyOffline(content, anchors, forbidden)
	result.Score = offline.Score
	result.Reasons = offline.Reasons

	// 3. LLM深度评估（可选，stage_models 新增 evals 阶段）
	// 当配置了 evals 阶段模型时，调用LLM进行深度评估
	llmScore, llmReasons := evaluateWithLLM(tenantID, content)
	if llmScore > 0 {
		// LLM评估有效时，取纯函数和LLM的加权平均
		result.Score = (offline.Score*0.4 + llmScore*0.6)
		result.Reasons = append(result.Reasons, llmReasons...)
	}

	// 4. 评分裁剪
	if result.Score < 0 {
		result.Score = 0
	}
	if result.Score > 5 {
		result.Score = 5
	}

	return result
}

// EvalLLMFunc 由 main.go 组合根注入：调用 LLM 对素材内容做深度评估。
// 返回评分（0-5）和评估理由；未注入/nil 时 evaluateWithLLM 直接跳过（纯函数预筛兜底）。
// 用函数变量而非直接 import llm/strategy 的原因：
//   - llm→service 已成环（llm 依赖 service 落账），service 反向 import 必成环；
//   - 业务层禁止直连 internal/llm 红线。
//
// main.go 注入适配器指向 strategy.GenerateEvals → llm.GenerateEvalsText（与 kb_material 同桥）。
var EvalLLMFunc func(tenantID uint, content string) (float64, []string)

// evaluateWithLLM 调用LLM进行深度评估（可选）
// 返回评分（0-5）和评估理由；未注入钩子/调用失败时返回0（跳过LLM评估，纯函数预筛兜底）
func evaluateWithLLM(tenantID uint, content string) (float64, []string) {
	if EvalLLMFunc == nil {
		return 0, nil
	}
	score, reasons := EvalLLMFunc(tenantID, content)
	if score <= 0 {
		return 0, nil
	}
	return score, reasons
}

// StartBatchEvaluator 启动批量评估定时任务（main.go 调用）
// 每小时评估一批待审素材，加速素材池流转
func StartBatchEvaluator() {
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			// 遍历所有租户，批量评估
			var tenants []model.Tenant
			db.DB.Where("status IN ?", []string{"active", "trial"}).Find(&tenants)
			for _, t := range tenants {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				_ = ctx
				_, err := BatchEvaluate(t.ID, 100)
				cancel()
				if err != nil {
					log.Printf("[DataFlywheel] 批量评估失败 tenant=%d: %v", t.ID, err)
				}
			}
		}
	}()
	log.Println("[DataFlywheel] 批量评估定时任务已启动（每小时）")
}
