// CDP 开放接口API：OneID 合并与事件摄入的对外写入端点。
package api

import (
	"net/http"
	"regexp"

	"ai-scrm/internal/cdp"
	"ai-scrm/internal/db"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/schema"

	"github.com/gin-gonic/gin"
)

// ============================================================
// CDP OpenAPI（只读出口，2026-08-22 遗留项 Phase A；CDP 摄入写收口在 cdp.IngestConsumer）
//
// 架构定位：数据层对业务层的唯一读通道（SAAS_PLAN §16.4 / 目标架构·数据层）
// 硬规则：
//   1. 服务端二次校验租户归属——one_id 只在本租户画像内解析，查不到即404（不泄露存在性）
//   2. 字段脱敏——手机号等敏感锚点不出网关
//   3. 只查不写——本文件禁止出现任何 Create/Update/Delete
// 调用方：B端（advisor/admin）经 JWTAuth+TenantConsistency 后进入
// ============================================================

// phoneMaskRe 手机号脱敏正则（11位大陆手机号）
var phoneMaskRe = regexp.MustCompile(`1[3-9]\d{9}`)

// maskSensitiveFields 递归脱敏：手机号保留前3后4
func maskSensitiveFields(s string) string {
	return phoneMaskRe.ReplaceAllStringFunc(s, func(p string) string {
		if len(p) != 11 {
			return p
		}
		return p[:3] + "****" + p[7:]
	})
}

// GetCDPProfile GET /api/v1/cdp/profiles/:one_id
// 360°用户视图：画像基础 + 四维标签 + 事件计数（脱敏后返回）
func GetCDPProfile(c *gin.Context) {
	tenantID := middleware.EffectiveTenantID(c)
	oneID := c.Param("one_id")
	if oneID == "" {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "one_id 不能为空"})
		return
	}

	view := cdp.GetProfile(tenantID, oneID)
	if view == nil {
		// 二次校验失败：不区分"不存在"与"他租户"，统一404防枚举
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "画像不存在", Data: nil})
		return
	}

	// 脱敏：名称与标签值统一过一遍手机号掩码
	view.Name = maskSensitiveFields(view.Name)
	for k, v := range view.Tags {
		view.Tags[k] = maskSensitiveFields(v)
	}

	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "success", Data: view})
}

// GetCDPSegment GET /api/v1/cdp/segments?tag=beh_lead_captured
// 分群圈选：按原子标签圈选当前租户 OneID 列表（分群=查询结果，不落静态标签）
func GetCDPSegment(c *gin.Context) {
	tenantID := middleware.EffectiveTenantID(c)
	tagCode := c.Query("tag")
	if tagCode == "" {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "tag 参数不能为空"})
		return
	}

	oneIDs, err := cdp.NewSegmentEngine().SegmentByTag(tenantID, tagCode)
	if err != nil {
		c.JSON(http.StatusInternalServerError, schema.Response{Code: 500, Message: "分群查询失败: " + err.Error()})
		return
	}
	// OneID 本身是内部标识（c:{id}），非敏感；列表规模保护上限
	if len(oneIDs) > 1000 {
		oneIDs = oneIDs[:1000]
	}
	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "success", Data: gin.H{
		"tag":     tagCode,
		"total":   len(oneIDs),
		"one_ids": oneIDs,
	}})
}

// ListCDPTagDefs GET /api/v1/cdp/tag-defs
// 标签字典查询（四维分类可见，供 B 端理解分群语义）
func ListCDPTagDefs(c *gin.Context) {
	tenantID := middleware.EffectiveTenantID(c)
	defs, err := cdp.ListTagDefinitions(tenantID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, schema.Response{Code: 500, Message: "字典查询失败"})
		return
	}
	c.JSON(http.StatusOK, schema.Response{Code: 0, Message: "success", Data: defs})
}

var _ = db.DB // 保持 db 引用对齐（本包其他文件已使用）
