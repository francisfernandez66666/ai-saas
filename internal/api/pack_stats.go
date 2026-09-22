// D9 包效果归因接口（Admin 本租户 / Super 跨租户）+ 批五 C·L1 行动建议卡片。
package api

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"ai-scrm/internal/attribution"
	"ai-scrm/internal/db"
	"ai-scrm/internal/engine/strategy"
	"ai-scrm/internal/model"
)

// parseStatFilter 解析包质量统计查询条件。
func parseStatFilter(c *gin.Context, tid *uint) attribution.StatFilter {
	days, _ := strconv.Atoi(c.DefaultQuery("days", "30"))
	if days < 0 {
		days = 0
	}
	if days > 365 {
		days = 365
	}
	return attribution.StatFilter{
		TenantID:    tid,
		PackCode:    c.Query("pack_code"),
		PackVersion: c.Query("pack_version"),
		TemplateID:  c.Query("template_id"),
		AbGroup:     c.Query("ab_group"), // E4：话术实验组对照视图
		Days:        days,
	}
}

// AdminPackStats GET /api/v1/admin/packs/stats — 本租户包/模板效果。
// apidump:ts PackStatsResp
// AdminPackStats 返回本租户行业包质量统计（附批五 C·L1 判优建议卡片）。
func AdminPackStats(c *gin.Context) {
	tid := db.EffectiveTenantIDFromGin(c)
	rows, err := attribution.Stats(parseStatFilter(c, &tid))
	if err != nil {
		RespErrInternal(c, err, "包效果统计失败")
		return
	}
	RespOK(c, "ok", gin.H{
		"list":        rows,
		"sample_min":  50,
		"suggestions": buildPackSuggestions(parseStatFilter(c, &tid)),
	})
}

// SuperPackStats GET /api/v1/super/packs/stats — 跨租户包质量视图。
// apidump:ts PackStatsResp
// SuperPackStats 返回跨租户行业包质量统计（附批五 C·L1 判优建议卡片，按租户分组互不混判）。
func SuperPackStats(c *gin.Context) {
	var tid *uint
	if raw := c.Query("tenant_id"); raw != "" {
		if v, err := strconv.ParseUint(raw, 10, 64); err == nil && v > 0 {
			u := uint(v)
			tid = &u
		}
	}
	rows, err := attribution.Stats(parseStatFilter(c, tid))
	if err != nil {
		RespErrInternal(c, err, "包效果统计失败")
		return
	}
	var total int64
	for _, r := range rows {
		total += r.SampleCount
	}
	RespOK(c, "ok", gin.H{
		"list":          rows,
		"sample_min":    50,
		"total_samples": total,
		"suggestions":   buildPackSuggestions(parseStatFilter(c, tid)),
	})
}

// ============================================================
// 批五 C·L1：行动建议卡片（pack_stats 快照 → strategy.BuildTemplateSuggestions 纯函数）
// ============================================================

// buildPackSuggestions 装配判优输入并产出建议卡片；任何一步失败返回空数组——
// 建议是旁路能力，绝不把 /packs/stats 主视图带崩（fail-open 同择臂层纪律）。
func buildPackSuggestions(f attribution.StatFilter) []strategy.TemplateSuggestion {
	snaps, err := loadPackStatSnapshots(f)
	if err != nil || len(snaps) == 0 {
		return []strategy.TemplateSuggestion{}
	}
	templateIDs := make([]string, 0, len(snaps))
	tenantIDs := make([]uint, 0, 8)
	seenTenant := map[uint]bool{}
	for _, s := range snaps {
		templateIDs = append(templateIDs, s.TemplateID)
		if !seenTenant[s.TenantID] {
			seenTenant[s.TenantID] = true
			tenantIDs = append(tenantIDs, s.TenantID)
		}
	}
	anchors, err := loadTemplateAnchorMap(templateIDs, tenantIDs)
	if err != nil {
		return []strategy.TemplateSuggestion{}
	}
	inputs := make([]strategy.TemplateStatInput, 0, len(snaps))
	for _, s := range snaps {
		anchor, ok := anchors[s.TemplateID]
		if !ok {
			continue // 模板已删除/不可见：没有可"下线/改稿"的对象，跳过不出卡
		}
		inputs = append(inputs, strategy.TemplateStatInput{
			TenantID: s.TenantID, PackCode: s.PackCode, PackVersion: s.PackVersion,
			TemplateID: s.TemplateID, AnchorType: anchor,
			SampleCount: s.SampleCount,
			HookRate:    s.HookRate, LeadRate: s.LeadRate,
			ArriveRate: s.ArriveRate, DealRate: s.DealRate,
		})
	}
	// 按租户分组判优：experiment_*（奖励指标/门槛/置信度）是租户自配键，
	// Super 跨租户视图里绝不能共用一份参数（见 strategy.BuildTemplateSuggestionsByTenant）。
	return strategy.BuildTemplateSuggestionsByTenant(inputs)
}

// loadPackStatSnapshots 读取 pack_stats 物化快照（批五 A 产出，本接口只读不写）。
// Super 无 tenant 过滤时显式排除 tenant_id=0 平台层行（快照只应存在于真实租户）。
func loadPackStatSnapshots(f attribution.StatFilter) ([]model.PackStatSnapshot, error) {
	q := db.DB.Model(&model.PackStatSnapshot{}).Where("tenant_id <> 0")
	if f.TenantID != nil {
		q = q.Where("tenant_id = ?", *f.TenantID)
	}
	if f.PackCode != "" {
		q = q.Where("pack_code = ?", f.PackCode)
	}
	if f.PackVersion != "" {
		q = q.Where("pack_version = ?", f.PackVersion)
	}
	if f.TemplateID != "" {
		q = q.Where("template_id = ?", f.TemplateID)
	}
	if f.Days > 0 {
		q = q.Where("computed_at >= ?", time.Now().AddDate(0, 0, -f.Days))
	}
	var rows []model.PackStatSnapshot
	err := q.Order("tenant_id ASC, pack_code ASC, pack_version ASC, template_id ASC").Find(&rows).Error
	return rows, err
}

// loadTemplateAnchorMap 按模板 ID 集合读锚类型映射（判优分组维度），
// 模板可能属于快照租户或系统预置（tenant_id=0），两者都纳入可见范围。
func loadTemplateAnchorMap(templateIDs []string, tenantIDs []uint) (map[string]int, error) {
	out := make(map[string]int, len(templateIDs))
	if len(templateIDs) == 0 {
		return out, nil
	}
	ids := append(tenantIDs, 0) // 预置话术锚信息
	var rows []model.Template
	if err := db.DB.Select("id, tenant_id, anchor_type").
		Where("id IN ? AND tenant_id IN ?", templateIDs, ids).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = r.AnchorType
	}
	return out, nil
}
