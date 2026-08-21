package mq

import (
	"context"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"

	"gorm.io/gorm"
)

// ============================================================
// Inbox 幂等收件箱（SAAS_PLAN §2.5）
// event_id 唯一：消费前抢占，防重复消费；失败可标记重试
// ============================================================

// EnsureProcessed 幂等抢占：首次返回 false（调用方执行处理），已存在返回 true（跳过）
func EnsureProcessed(eventID string, tenantID uint, oneID string, topic string) (bool, error) {
	var existing model.InboxEvent
	err := db.DB.Where("event_id = ?", eventID).First(&existing).Error
	if err == nil {
		return existing.Status == "done", nil
	}
	if err != gorm.ErrRecordNotFound {
		return false, err
	}
	row := model.InboxEvent{
		EventID:  eventID,
		TenantID: tenantID,
		OneID:    oneID,
		Topic:    topic,
		Status:   "pending",
	}
	if err := db.DB.Create(&row).Error; err != nil {
		// 并发竞争：别人先插了 → 视为已处理
		var again model.InboxEvent
		if e2 := db.DB.Where("event_id = ?", eventID).First(&again).Error; e2 == nil {
			return again.Status == "done", nil
		}
		return false, err
	}
	return false, nil
}

// MarkInboxDone 处理成功落标记
func MarkInboxDone(eventID string) error {
	return db.DB.Model(&model.InboxEvent{}).
		Where("event_id = ?", eventID).
		Updates(map[string]interface{}{"status": "done", "processed_at": time.Now()}).Error
}

// MarkInboxFailed 处理失败标记（审计表回放时重试 pending/failed）
func MarkInboxFailed(eventID string) error {
	return db.DB.Model(&model.InboxEvent{}).
		Where("event_id = ?", eventID).
		Update("status", "failed").Error
}

// 便捷组合：处理成功后自动打 done 标记的包装器
func WithInbox(ctx context.Context, env Envelope, handler EventHandler) error {
	processed, err := EnsureProcessed(env.Header.EventID, env.Header.TenantID, env.Header.OneID, env.Topic)
	if err != nil {
		return err
	}
	if processed {
		return nil
	}
	if err := handler(ctx, env); err != nil {
		_ = MarkInboxFailed(env.Header.EventID)
		return err
	}
	return MarkInboxDone(env.Header.EventID)
}
