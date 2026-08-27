package api

// 后台系统配置管理API：查询/批量更新/恢复默认/强制初始化/可用模型列表/租户配置回滚。
// 平台级键(pay_mode/billing_enforced等)仅super_admin可改且强制写系统默认层，其余写租户覆盖层。

import (
	"ai-scrm/internal/ai"
	"ai-scrm/internal/config_center"
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/schema"
	"ai-scrm/internal/service"
	"fmt"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 系统配置管理API（Admin后台）
// 路由前缀：/api/v1/admin/config
// 功能：查询配置、批量更新、恢复默认、获取可用模型列表
// 暂不鉴权，后续再加
// ============================================================

// GetSystemConfigs 查询系统配置
// GET /api/v1/admin/config
// 支持按分类筛选：?category=reply_speed
// 不传category则返回所有配置
func GetSystemConfigs(c *gin.Context) {
	category := c.Query("category") // 可选：按分类筛选

	var configs []model.SystemConfig
	if category != "" {
		configs = service.DefaultSystemConfigService.GetByCategory(category)
	} else {
		configs = service.DefaultSystemConfigService.GetAll()
	}

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data:    configs,
	})
}

// BatchUpdateSystemConfig 批量更新系统配置
// PUT /api/v1/admin/config
// Body: [{key, value}, ...]
// 更新后自动热加载到内存，无需重启服务
func BatchUpdateSystemConfig(c *gin.Context) {
	var items []service.ConfigUpdateItem
	if err := c.ShouldBindJSON(&items); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{
			Code:    400,
			Message: "参数错误: " + err.Error(),
			Data:    nil,
		})
		return
	}

	if len(items) == 0 {
		c.JSON(http.StatusBadRequest, schema.Response{
			Code:    400,
			Message: "更新列表不能为空",
			Data:    nil,
		})
		return
	}

	// 平台级键（pay_mode/billing_enforced等）安全闸门（商业化 M1/M5）：
	// 仅 super_admin 可改，且强制写系统默认层。
	// 修复（2026-08-23）：非超管调用改为"静默剥离平台键"而非整单403——
	// admin.html 的 saveAll 会全量提交所有分类配置，租户管理员改任意参数
	// 都会夹带平台键，直接拒绝会破坏既有保存流程；剥离+留痕即可
	roleV, _ := c.Get("role")
	roleStr, _ := roleV.(string)
	isSuper := roleStr == model.RoleSuperAdmin
	platform := []service.ConfigUpdateItem{}
	tenant := []service.ConfigUpdateItem{}
	for _, it := range items {
		if service.PlatformLevelKeys[it.Key] {
			platform = append(platform, it)
		} else {
			tenant = append(tenant, it)
		}
	}
	if len(platform) > 0 && !isSuper {
		keys := make([]string, 0, len(platform))
		for _, it := range platform {
			keys = append(keys, it.Key)
		}
		log.Printf("[安全] 非超管(role=%s)尝试修改平台级键 %v，已剥离忽略", roleStr, keys)
		platform = nil // 剥离：租户层其余配置照常生效
	}

	// 批量更新DB + 热加载内存
	// P2 租户化：租户管理员改参数写入 (tenant_id,key) 覆盖层，不污染系统默认(0)
	// super_admin 未显式指定租户时 tid=默认租户（中间件已裁决），显式指定则写对应租户
	if err := service.DefaultSystemConfigService.BatchUpdateForTenant(db.EffectiveTenantIDFromGin(c), tenant); err != nil {
		c.JSON(http.StatusInternalServerError, schema.Response{
			Code:    500,
			Message: "更新失败: " + err.Error(),
			Data:    nil,
		})
		return
	}
	// 平台键走系统默认层（BatchUpdate 内部含 Reload 热加载）
	if len(platform) > 0 {
		if err := service.DefaultSystemConfigService.BatchUpdate(platform); err != nil {
			c.JSON(http.StatusInternalServerError, schema.Response{
				Code:    500,
				Message: "平台参数更新失败: " + err.Error(),
				Data:    nil,
			})
			return
		}
	}

	// K7修复(2026-08-27)：跨实例广播配置变更，其他实例收到后本地重载
	configcenter.BroadcastReload(db.EffectiveTenantIDFromGin(c))

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "更新成功，已热加载到内存",
		Data:    nil,
	})
}

// ResetSystemConfig 恢复所有配置为默认值
// POST /api/v1/admin/config/reset
// 会将所有配置项的Value恢复为DefaultValue
func ResetSystemConfig(c *gin.Context) {
	if err := service.DefaultSystemConfigService.ResetAll(); err != nil {
		c.JSON(http.StatusInternalServerError, schema.Response{
			Code:    500,
			Message: "重置失败: " + err.Error(),
			Data:    nil,
		})
		return
	}

	// K7修复(2026-08-27)：跨实例广播配置变更
	configcenter.BroadcastReload(db.EffectiveTenantIDFromGin(c))

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "已恢复所有默认值",
		Data:    nil,
	})
}

// ============================================================
// 强制初始化默认配置
// POST /api/v1/admin/config/init
// 修复：后台配置为空时，强制删除旧数据+重新写入默认配置
// 场景：用户反复反馈"后台什么参数都没有"，ensureDefaults幂等逻辑在脏DB下可能失效
// 修复方案：不管有没有旧数据，一律删除重建，确保配置一定存在
// ============================================================
func ForceInitSystemConfig(c *gin.Context) {
	// 调用ForceResetDefaults：删除旧数据 + 重新写入默认配置 + 热加载
	if err := service.DefaultSystemConfigService.ForceResetDefaults(); err != nil {
		c.JSON(http.StatusInternalServerError, schema.Response{
			Code:    500,
			Message: "强制初始化失败: " + err.Error(),
			Data:    nil,
		})
		return
	}

	// K7修复(2026-08-27)：跨实例广播配置变更
	configcenter.BroadcastReload(db.EffectiveTenantIDFromGin(c))

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "默认配置强制初始化成功",
		Data:    len(service.DefaultConfigs),
	})
}

// ============================================================
// 可用模型列表API
// ============================================================

// modelInfo 单个模型的信息（返回给前端）
type modelInfo struct {
	Provider    string `json:"provider"`     // 提供商标识：siliconflow / zhipu / fallback
	ModelName   string `json:"model_name"`   // 模型全名（API调用时的参数）
	Identifier  string `json:"identifier"`   // 标识符（与model_priority配置中的值对应）
	Available   bool   `json:"available"`    // 当前是否可用
	DisplayName string `json:"display_name"` // 中文显示名（前端展示用）
}

// modelsResponse 可用模型列表响应
type modelsResponse struct {
	Models      []modelInfo `json:"models"`      // 模型详情列表
	Identifiers []string    `json:"identifiers"` // 所有标识符列表
}

// GetAvailableModels 获取可用模型列表
// GET /api/v1/admin/models
// 返回AI路由器中当前配置的所有模型，用于模型优先级选择
func GetAvailableModels(c *gin.Context) {
	// 从AI路由器获取当前模型列表
	routerModels := ai.Router.GetModels()
	result := make([]modelInfo, 0, len(routerModels))

	// 模型名→标识符的映射（标识符用于model_priority配置）
	nameToIdentifier := map[string]string{
		"deepseek-ai/DeepSeek-V4-Flash": "siliconflow_deepseek_v4_flash",
		"THUDM/GLM-4-9B-0414":           "siliconflow_glm4_9b",
		"glm-4-flash":                   "zhipu_glm4_flash",
	}

	// 模型名→中文显示名映射
	nameToDisplay := map[string]string{
		"deepseek-ai/DeepSeek-V4-Flash": "DeepSeek V4 Flash (硅基流动)",
		"THUDM/GLM-4-9B-0414":           "GLM-4-9B (硅基流动免费)",
		"glm-4-flash":                   "GLM-4-Flash (智谱)",
	}

	for _, m := range routerModels {
		identifier := nameToIdentifier[m.ModelName]
		displayName := nameToDisplay[m.ModelName]
		if displayName == "" {
			displayName = m.ModelName
		}

		result = append(result, modelInfo{
			Provider:    string(m.Provider),
			ModelName:   m.ModelName,
			Identifier:  identifier,
			Available:   m.Available,
			DisplayName: displayName,
		})
	}

	// 添加模板兜底（不在路由器模型列表中，但是model_priority的可选项）
	result = append(result, modelInfo{
		Provider:    "fallback",
		ModelName:   "template_fallback",
		Identifier:  "template_fallback",
		Available:   true,
		DisplayName: "模板兜底（不调用AI）",
	})

	// 收集所有标识符
	identifiers := make([]string, 0, len(result))
	for _, m := range result {
		if m.Identifier != "" {
			identifiers = append(identifiers, m.Identifier)
		}
	}

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data: modelsResponse{
			Models:      result,
			Identifiers: identifiers,
		},
	})
}

// RollbackTenantConfig POST /api/v1/admin/config/rollback {keys?:[]}
// 清除本租户覆盖层回落系统默认（config_center.Rollback）
func RollbackTenantConfig(c *gin.Context) {
	var req struct {
		Keys []string `json:"keys"`
	}
	_ = c.ShouldBindJSON(&req)
	n, err := configcenter.Rollback(db.EffectiveTenantIDFromGin(c), req.Keys)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "回滚失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": fmt.Sprintf("已回滚 %d 项配置至系统默认", n)})
}
