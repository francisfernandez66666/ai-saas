package configcenter

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/mq"
	"ai-scrm/internal/service"

	"gorm.io/gorm"
)

// ============================================================
// 配置中心（SAAS_PLAN §十九 · 行业包语义）
//
// 在 system_configs 租户覆盖层之上提供生命周期动作：
//   Seed    ：系统默认(0) → 克隆为租户私有行（首次个性化起点）
//   Upgrade ：批量写入/更新租户覆盖值
//   Rollback：删除租户覆盖行 → 回落到系统默认
// 三个动作均发布 tenant_cfg_event，各引擎据此热加载；
// 本包不承载具体业务参数逻辑（行业包内容归配置，不进主干代码）
// ============================================================

// Seed 为租户克隆系统默认配置为可编辑的租户层（已存在的键跳过）
func Seed(tenantID uint) (int, error) {
	if tenantID == 0 {
		return 0, fmt.Errorf("Seed 不适用于系统层(0)")
	}
	var defaults []model.SystemConfig
	if err := db.DB.Where("tenant_id = 0").Find(&defaults).Error; err != nil {
		return 0, err
	}
	inserted := 0
	for _, d := range defaults {
		var cnt int64
		db.DB.Model(&model.SystemConfig{}).
			Where("tenant_id = ? AND \"key\" = ?", tenantID, d.Key).Count(&cnt)
		if cnt > 0 {
			continue
		}
		row := model.SystemConfig{
			TenantID: tenantID, Category: d.Category, Key: d.Key,
			Value: d.DefaultValue, ValueType: d.ValueType,
			Description: d.Description, DefaultValue: d.DefaultValue,
			SortOrder: d.SortOrder,
		}
		if err := db.DB.Create(&row).Error; err != nil {
			log.Printf("[ConfigCenter] Seed 写入失败 key=%s: %v", d.Key, err)
			continue
		}
		inserted++
	}
	publish(tenantID, "seed", "params", inserted)
	log.Printf("[ConfigCenter] 租户%d Seed 完成：%d 项默认配置已克隆", tenantID, inserted)
	return inserted, nil
}

// Upgrade 批量升级租户覆盖值（upsert）
func Upgrade(tenantID uint, items map[string]string) (int, error) {
	if tenantID == 0 || len(items) == 0 {
		return 0, fmt.Errorf("Upgrade 参数为空")
	}
	n := 0
	for k, v := range items {
		if !json.Valid([]byte(v)) {
			continue
		}
		var existing model.SystemConfig
		err := db.DB.Where("tenant_id = ? AND \"key\" = ?", tenantID, k).First(&existing).Error
		if err == nil {
			db.DB.Model(&existing).Update("value", v)
			n++
			continue
		}
		if err != gorm.ErrRecordNotFound {
			return n, err
		}
		var def model.SystemConfig
		db.DB.Where("tenant_id = 0 AND \"key\" = ?", k).First(&def)
		row := model.SystemConfig{
			TenantID: tenantID, Category: def.Category, Key: k, Value: v,
			ValueType: def.ValueType, Description: def.Description,
			DefaultValue: def.DefaultValue, SortOrder: def.SortOrder,
		}
		if row.ValueType == "" {
			row.ValueType = "string"
		}
		if err := db.DB.Create(&row).Error; err != nil {
			return n, err
		}
		n++
	}
	service.DefaultSystemConfigService.Reload()
	publish(tenantID, "upgrade", "params", n)
	log.Printf("[ConfigCenter] 租户%d Upgrade：%d 项覆盖已生效", tenantID, n)
	return n, nil
}

// Rollback 删除指定租户的覆盖层（keys 空=全部），回落系统默认
func Rollback(tenantID uint, keys []string) (int, error) {
	if tenantID == 0 {
		return 0, fmt.Errorf("Rollback 不适用于系统层(0)")
	}
	q := db.DB.Where("tenant_id = ?", tenantID)
	if len(keys) > 0 {
		q = q.Where("\"key\" IN ?", keys)
	}
	res := q.Delete(&model.SystemConfig{})
	if res.Error != nil {
		return 0, res.Error
	}
	service.DefaultSystemConfigService.Reload()
	publish(tenantID, "rollback", "params", int(res.RowsAffected))
	log.Printf("[ConfigCenter] 租户%d Rollback：清除 %d 项覆盖", tenantID, res.RowsAffected)
	return int(res.RowsAffected), nil
}

// publish 发布 tenant_cfg_event（log 模式=进程内总线，kafka 模式=真实主题）
func publish(tenantID uint, action string, scope string, affected int) {
	err := mq.Publish(context.Background(), mq.TopicTenantCfgEvt, tenantID,
		fmt.Sprintf("sys:t%d", tenantID), action, map[string]any{
			"action": action, "scope": scope, "affected": affected,
		})
	if err != nil {
		log.Printf("[ConfigCenter] tenant_cfg_event 发布失败: %v", err)
	}
}
