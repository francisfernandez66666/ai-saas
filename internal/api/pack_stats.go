// D9 包效果归因接口（Admin 本租户 / Super 跨租户）。
package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"ai-scrm/internal/attribution"
	"ai-scrm/internal/db"
)

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
		Days:        days,
	}
}

// AdminPackStats GET /api/v1/admin/packs/stats — 本租户包/模板效果。
// apidump:ts PackStatsResp
func AdminPackStats(c *gin.Context) {
	tid := db.EffectiveTenantIDFromGin(c)
	rows, err := attribution.Stats(parseStatFilter(c, &tid))
	if err != nil {
		RespErr(c, http.StatusInternalServerError, 500, attribution.StatsMessage(err))
		return
	}
	RespOK(c, "ok", gin.H{"list": rows, "sample_min": 50})
}

// SuperPackStats GET /api/v1/super/packs/stats — 跨租户包质量视图。
// apidump:ts PackStatsResp
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
		RespErr(c, http.StatusInternalServerError, 500, attribution.StatsMessage(err))
		return
	}
	var total int64
	for _, r := range rows {
		total += r.SampleCount
	}
	RespOK(c, "ok", gin.H{"list": rows, "sample_min": 50, "total_samples": total})
}
