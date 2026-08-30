// 租户白标品牌：按 Host 解析公开白标、超管/租户管理员更新（自定义域名、解析缓存失效）
package api

import (
	"net/http"
	"strconv"

	"ai-scrm/internal/db"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"

	"github.com/gin-gonic/gin"
)

// defaultBrandName 平台默认品牌（跨山 LexCross），租户未设置自定义品牌名时回退
const defaultBrandName = "跨山 LexCross"

// BrandingResp 对外品牌配置（租户级白标）
type BrandingResp struct {
	PlatformDefault bool   `json:"platform_default"` // true=平台默认品牌（无租户/未定制）
	BrandName       string `json:"brand_name"`
	BrandLink       string `json:"brand_link"`
	LogoURL         string `json:"logo_url"`
	FaviconURL      string `json:"favicon_url"`
	PrimaryColor    string `json:"primary_color"`
	SecondaryColor  string `json:"secondary_color"`
	CustomDomain    string `json:"custom_domain"`
	CustomCSS       string `json:"custom_css"`
	CustomJS        string `json:"custom_js"`
}

// GetPublicBranding GET /api/v1/public/branding
// 按当前访问 Host 解析租户白标；平台默认域返回跨山 LexCross 平台品牌
func GetPublicBranding(c *gin.Context) {
	t := middleware.ResolveTenantFromHost(c)
	if t == nil {
		c.JSON(http.StatusOK, BrandingResp{PlatformDefault: true, BrandName: defaultBrandName})
		return
	}
	c.JSON(http.StatusOK, brandingData(t))
}

// brandingUpdateReq 白标更新请求（超管 / 租户管理员共用）
type brandingUpdateReq struct {
	BrandName    string  `json:"brand_name"`
	BrandLink    string  `json:"brand_link"`
	LogoURL      string  `json:"logo_url"`
	FaviconURL   string  `json:"favicon_url"`
	PrimaryColor string  `json:"primary_color"`
	CustomDomain *string `json:"custom_domain"` // 指针：可清空（传 null 或空串）
	CustomCSS    string  `json:"custom_css"`
	CustomJS     string  `json:"custom_js"`
}

// applyBrandingUpdate 落库更新并失效租户解析缓存
func applyBrandingUpdate(tenantID uint, req brandingUpdateReq) (*model.Tenant, error) {
	var t model.Tenant
	if err := db.DB.First(&t, tenantID).Error; err != nil {
		return nil, err
	}
	updates := map[string]interface{}{
		"brand_name":    req.BrandName,
		"brand_link":    req.BrandLink,
		"logo_url":      req.LogoURL,
		"favicon_url":   req.FaviconURL,
		"primary_color": req.PrimaryColor,
		"custom_css":    req.CustomCSS,
		"custom_js":     req.CustomJS,
	}
	if req.CustomDomain != nil {
		if *req.CustomDomain == "" {
			updates["custom_domain"] = nil
		} else {
			updates["custom_domain"] = *req.CustomDomain
		}
	}
	if err := db.DB.Model(&t).Updates(updates).Error; err != nil {
		return nil, err
	}
	db.DB.First(&t, tenantID)
	middleware.InvalidateTenantCache()
	return &t, nil
}

// AdminUpdateBranding PUT /api/v1/admin/tenant/branding 租户管理员设置自身租户白标
func AdminUpdateBranding(c *gin.Context) {
	var req brandingUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误: " + err.Error()})
		return
	}
	tid := middleware.EffectiveTenantID(c)
	if tid == 0 {
		c.JSON(http.StatusForbidden, gin.H{"code": 403, "message": "无法识别租户"})
		return
	}
	t, err := applyBrandingUpdate(tid, req)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"code": 409, "message": "更新失败（自定义域名可能已被占用）: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "品牌配置已更新", "data": brandingData(t)})
}

// SuperGetBranding GET /api/v1/super/tenants/:id/branding 超管读取任意租户白标
func SuperGetBranding(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "租户ID非法"})
		return
	}
	var t model.Tenant
	if err := db.DB.First(&t, uint(id)).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "租户不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "ok", "data": brandingData(&t)})
}

// SuperUpdateBranding PUT /api/v1/super/tenants/:id/branding 超管设置任意租户白标
func SuperUpdateBranding(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "租户ID非法"})
		return
	}
	var req brandingUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "参数错误: " + err.Error()})
		return
	}
	t, err := applyBrandingUpdate(uint(id), req)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"code": 409, "message": "更新失败（自定义域名可能已被占用）: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "品牌配置已更新", "data": brandingData(t)})
}

// brandingData 租户 → 对外品牌结构（缺省品牌名回退跨山 LexCross）
func brandingData(t *model.Tenant) BrandingResp {
	name := t.BrandName
	if name == "" {
		name = defaultBrandName
	}
	cd := ""
	if t.CustomDomain != nil {
		cd = *t.CustomDomain
	}
	return BrandingResp{
		BrandName:      name,
		BrandLink:      t.BrandLink,
		LogoURL:        t.LogoURL,
		FaviconURL:     t.FaviconURL,
		PrimaryColor:   t.PrimaryColor,
		SecondaryColor: t.SecondaryColor,
		CustomDomain:   cd,
		CustomCSS:      t.CustomCSS,
		CustomJS:       t.CustomJS,
	}
}
