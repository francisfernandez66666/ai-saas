// 渠道标识 ↔ 站内客户 映射与入站身份解析（W2 OneID 桥，2026-09-12）
package channel

import (
	"errors"
	"fmt"
	"strings"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"

	"gorm.io/gorm"
)

// ResolveOrCreateCustomer 按 (channel_id, external_id) 找站内客户；无则建渠道潜客并绑定。
// 返回 customerID。新建客户：Source=渠道类型，JourneyStage=ai_connected，Name 缺省用外部号尾号占位。
func ResolveOrCreateCustomer(tenantID, channelID uint, externalID, staffID, name string) (uint, error) {
	if externalID == "" {
		return 0, errors.New("external_id 为空，无法解析渠道客户")
	}
	var ident model.ChannelIdentity
	err := db.DB.Where("channel_id = ? AND external_id = ?", channelID, externalID).First(&ident).Error
	if err == nil {
		// 命中：补记 staff（换接待）
		if staffID != "" && ident.StaffID != staffID {
			db.DB.Model(&ident).Update("staff_id", staffID)
		}
		return ident.CustomerID, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, err
	}
	// 未命中：建新客户 + 绑定
	displayName := name
	if displayName == "" {
		displayName = "微信用户" + tailN(externalID, 4)
	}
	cust := model.Customer{
		TenantID:       tenantID,
		Name:           displayName,
		Source:         fmt.Sprintf("channel:%d", channelID),
		JourneyStage:   model.JourneyAIConnected,
		ExternalUserID: externalID,
		Status:         1,
	}
	if err := db.DB.Create(&cust).Error; err != nil {
		return 0, fmt.Errorf("创建渠道客户失败: %w", err)
	}
	ni := model.ChannelIdentity{
		TenantID:   tenantID,
		ChannelID:  channelID,
		CustomerID: cust.ID,
		ExternalID: externalID,
		StaffID:    staffID,
	}
	if err := db.DB.Create(&ni).Error; err != nil {
		return cust.ID, err
	}
	return cust.ID, nil
}

// externalIDFor 出站反查：按 (channel_id, customer_id) 取回渠道外部号。
func externalIDFor(channelID, customerID uint) (string, error) {
	var ident model.ChannelIdentity
	if err := db.DB.Where("channel_id = ? AND customer_id = ?", channelID, customerID).First(&ident).Error; err != nil {
		return "", fmt.Errorf("未找到渠道客户映射 channel=%d customer=%d", channelID, customerID)
	}
	return ident.ExternalID, nil
}

// UpsertIdentity 显式绑定/更新（顾问手动关联已有客户到渠道号时用）。
func UpsertIdentity(tenantID, channelID, customerID uint, externalID, staffID string) error {
	var ident model.ChannelIdentity
	err := db.DB.Where("channel_id = ? AND external_id = ?", channelID, externalID).First(&ident).Error
	if err == nil {
		return db.DB.Model(&ident).Updates(map[string]interface{}{"customer_id": customerID, "staff_id": staffID}).Error
	}
	return db.DB.Create(&model.ChannelIdentity{
		TenantID: tenantID, ChannelID: channelID, CustomerID: customerID, ExternalID: externalID, StaffID: staffID,
	}).Error
}

// tailN 返回字符串末尾 n 个字符，用于身份尾号展示。
func tailN(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// gormErrNoRows 复用 gorm 的 ErrRecordNotFound
// isTokenInvalidErr 判定渠道返回是否"access_token 失效"（-1 或含 invalid token）。
func isTokenInvalidErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "code=-1") || strings.Contains(s, "40001") || strings.Contains(s, "42001") ||
		strings.Contains(strings.ToLower(s), "invalid token") || strings.Contains(strings.ToLower(s), "expired")
}
