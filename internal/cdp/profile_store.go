package cdp

import (
	"encoding/json"
	"log"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"

	"gorm.io/gorm"
)

// gdb 会话获取（消费者运行于后台 goroutine，无 gin 上下文；命名避开 db 包名）
func gdb() *gorm.DB { return db.DB }

// UpsertAnchor 身份锚点归并：锚点值 → OneID 映射（已存在则不覆盖）
// 锚点类型：phone / wechat_openid / device_id 等
func UpsertAnchor(tenantID uint, anchorType, anchorValue, oneID string) {
	if anchorValue == "" || oneID == "" {
		return
	}
	var cnt int64
	gdb().Model(&model.IdMapping{}).
		Where("tenant_id = ? AND internal_type = ? AND cdp_entity_id = ?",
			tenantID, "anchor:"+anchorType+":"+anchorValue, oneID).
		Count(&cnt)
	if cnt > 0 {
		return
	}
	gdb().Create(&model.IdMapping{
		TenantID:     tenantID,
		InternalType: "anchor:" + anchorType + ":" + anchorValue,
		CdpEntityId:  oneID,
		MappingType:  "one2one",
		Source:       "ingest",
	})
}

// EnsureProfile 确保画像主体存在（按 OneID 幂等），返回 nil 表示失败
func EnsureProfile(tenantID uint, oneID string, customerID uint) *model.CdpProfile {
	var p model.CdpProfile
	err := gdb().Where("tenant_id = ? AND cdp_id = ?", tenantID, oneID).First(&p).Error
	if err == nil {
		return &p
	}
	fresh := model.CdpProfile{
		TenantID: tenantID, CustomerID: customerID, CdpId: oneID,
		ProfileName: oneID, Status: 1, ProfileData: "{}",
	}
	if err := gdb().Create(&fresh).Error; err != nil {
		log.Printf("[CDP] 画像创建失败 one=%s: %v", oneID, err)
		return nil
	}
	return &fresh
}

// ApplyTag 原子标签赋值（upsert：同画像同标签更新时间戳与值）
// 仅写行为/身份维；态度维数据须来自零方渠道，禁止在此由行为推导
func ApplyTag(tenantID uint, profileID uint, code string, value string) {
	var def model.CdpTagDefinition
	if err := gdb().Where("code = ?", code).First(&def).Error; err != nil {
		log.Printf("[CDP] 未定义标签 %s，跳过（扩展请先登记字典）", code)
		return
	}
	var exist model.CdpTagAssignment
	err := gdb().Where("cdp_profile_id = ? AND definition_id = ?", profileID, def.ID).First(&exist).Error
	if err == nil {
		gdb().Model(&exist).Updates(map[string]interface{}{"tag_value": value})
		return
	}
	gdb().Create(&model.CdpTagAssignment{
		TenantID: tenantID, CdpProfileID: profileID,
		DefinitionID: def.ID, TagValue: value,
	})
}

// ---- ProfileStore：同步读 API（仅流程引擎/业务系统可调；超时降级由调用方控制）----

type ProfileView struct {
	OneID  string            `json:"one_id"`
	Name   string            `json:"name"`
	Status int               `json:"status"`
	Tags   map[string]string `json:"tags"`
	Events int64             `json:"event_count"`
}

// GetProfile 360° 视图：画像基础 + 标签字典联查 + 事件计数
func GetProfile(tenantID uint, oneID string) *ProfileView {
	var p model.CdpProfile
	if err := gdb().Where("tenant_id = ? AND cdp_id = ?", tenantID, oneID).First(&p).Error; err != nil {
		return nil
	}
	view := &ProfileView{OneID: oneID, Name: p.ProfileName, Status: p.Status, Tags: map[string]string{}}
	rows := []struct {
		Code  string
		Value string
	}{}
	gdb().Table("cdp_tag_assignments a").
		Select("df.code as code, a.tag_value as value").
		Joins("LEFT JOIN cdp_tag_definitions df ON a.definition_id = df.id").
		Where("a.cdp_profile_id = ?", p.ID).Scan(&rows)
	for _, r := range rows {
		view.Tags[r.Code] = r.Value
	}
	gdb().Model(&model.EventLog{}).Where("tenant_id = ? AND customer_id = ?", tenantID, p.CustomerID).
		Count(&view.Events)
	return view
}

var _ = json.Marshal
