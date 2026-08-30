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
	BrandName       string `json:"brand_name"`       // 品牌名称
	BrandLink       string `json:"brand_link"`       // 品牌链接
	LogoURL         string `json:"logo_url"`         // Logo 图片地址
	FaviconURL      string `json:"favicon_url"`      // Favicon 图标地址
	PrimaryColor    string `json:"primary_color"`    // 主题色
	SecondaryColor  string `json:"secondary_color"`  // 辅助色
	CustomDomain    string `json:"custom_domain"`    // 自定义域名
	CustomCSS       string `json:"custom_css"`       // 自定义样式
	CustomJS        string `json:"custom_js"`        // 自定义脚本
}

// GetPublicBranding 获取公开白标配置
// GET /api/v1/public/branding
// 按当前访问 Host 解析租户白标；平台默认域返回跨山 LexCross 平台品牌
// 用于前端初始化时加载对应租户的品牌样式
func GetPublicBranding(c *gin.Context) {
	// 根据请求 Host 自动解析所属租户（支持自定义域名）
	t := middleware.ResolveTenantFromHost(c)
	if t == nil {
		// 未匹配到租户时返回平台默认品牌
		c.JSON(http.StatusOK, BrandingResp{PlatformDefault: true, BrandName: defaultBrandName})
		return
	}
	c.JSON(http.StatusOK, brandingData(t))
}

// brandingUpdateReq 白标更新请求（超管 / 租户管理员共用）
type brandingUpdateReq struct {
	BrandName    string  `json:"brand_name"`    // 品牌名称
	BrandLink    string  `json:"brand_link"`    // 品牌链接
	LogoURL      string  `json:"logo_url"`      // Logo 地址
	FaviconURL   string  `json:"favicon_url"`   // Favicon 地址
	PrimaryColor string  `json:"primary_color"` // 主题色
	CustomDomain *string `json:"custom_domain"` // 指针：可清空（传 null 或空串）
	CustomCSS    string  `json:"custom_css"`    // 自定义 CSS
	CustomJS     string  `json:"custom_js"`     // 自定义 JS
}

// applyBrandingUpdate 落库更新白标配置并失效租户解析缓存
// 通用方法：超管和租户管理员共用，写入后必须清除缓存否则自定义域名不即时生效
// 返回更新后的租户模型，自定义域名冲突时返回错误
func applyBrandingUpdate(tenantID uint, req brandingUpdateReq) (*model.Tenant, error) {
	// 先查出目标租户
	var t model.Tenant
	if err := db.DB.First(&t, tenantID).Error; err != nil {
		return nil, err
	}
	// 构建更新字段映射
	updates := map[string]interface{}{
		"brand_name":    req.BrandName,
		"brand_link":    req.BrandLink,
		"logo_url":      req.LogoURL,
		"favicon_url":   req.FaviconURL,
		"primary_color": req.PrimaryColor,
		"custom_css":    req.CustomCSS,
		"custom_js":     req.CustomJS,
	}
	// 自定义域名特殊处理：指针 nil 表示不修改，空串表示清空
	if req.CustomDomain != nil {
		if *req.CustomDomain == "" {
			updates["custom_domain"] = nil
		} else {
			updates["custom_domain"] = *req.CustomDomain
		}
	}
	// 执行数据库更新
	if err := db.DB.Model(&t).Updates(updates).Error; err != nil {
		return nil, err
	}
	// 重新加载租户数据（含新值）并清除内存缓存
	db.DB.First(&t, tenantID)
	middleware.InvalidateTenantCache()
	return &t, nil
}

// AdminUpdateBranding 租户管理员设置自身租户白标
// PUT /api/v1/admin/tenant/branding
// 仅限租户管理员操作本租户的白标配置
func AdminUpdateBranding(c *gin.Context) {
	var req brandingUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误: "+err.Error())
		return
	}
	// 从中间件获取当前租户ID
	tid := middleware.EffectiveTenantID(c)
	if tid == 0 {
		RespErr(c, http.StatusForbidden, 403, "无法识别租户")
		return
	}
	t, err := applyBrandingUpdate(tid, req)
	if err != nil {
		RespErr(c, http.StatusConflict, 409, "更新失败（自定义域名可能已被占用）: "+err.Error())
		return
	}
	RespOK(c, "品牌配置已更新", brandingData(t))
}

// SuperGetBranding 超管读取任意租户白标配置
// GET /api/v1/super/tenants/:id/branding
// 超管可查看所有租户的白标设置，用于平台运营管理
func SuperGetBranding(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		RespErr(c, http.StatusBadRequest, 400, "租户ID非法")
		return
	}
	var t model.Tenant
	if err := db.DB.First(&t, uint(id)).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "租户不存在")
		return
	}
	RespOK(c, "ok", brandingData(&t))
}

// SuperUpdateBranding 超管设置任意租户白标
// PUT /api/v1/super/tenants/:id/branding
// 超管可强制修改任意租户的白标配置，自定义域名唯一性冲突时返回409
func SuperUpdateBranding(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		RespErr(c, http.StatusBadRequest, 400, "租户ID非法")
		return
	}
	var req brandingUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误: "+err.Error())
		return
	}
	t, err := applyBrandingUpdate(uint(id), req)
	if err != nil {
		RespErr(c, http.StatusConflict, 409, "更新失败（自定义域名可能已被占用）: "+err.Error())
		return
	}
	RespOK(c, "品牌配置已更新", brandingData(t))
}

// brandingData 将租户模型转换为对外品牌响应结构
// 品牌名为空时回退到平台默认品牌名（跨山 LexCross）
// 自定义域名从指针解包为空字符串
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
