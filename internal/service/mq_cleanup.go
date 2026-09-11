// Package service 业务服务层：消息中心审计/收件箱数据治理（P1-37）
package service

import (
	"log"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
)

// CleanupMQTables 清理消息中心留存数据（P1-37，2026-09-09）
// message_event_records：审计默认保留 30 天；inbox_events：收件箱默认保留 90 天。
// 保留天数可用 system_configs 覆盖：mq_audit_retention_days / mq_inbox_retention_days。
// 条件 DELETE 幂等，多实例由调用方 Redis 选主兜底。
func CleanupMQTables() int {
	auditDays := DefaultSystemConfigService.GetInt("mq_audit_retention_days", 30)
	inboxDays := DefaultSystemConfigService.GetInt("mq_inbox_retention_days", 90)

	total := 0

	auditCut := time.Now().AddDate(0, 0, -auditDays)
	ra := db.DB.Unscoped().Where("created_at < ?", auditCut).
		Delete(&model.MessageEventRecord{}).RowsAffected
	if ra > 0 {
		log.Printf("[MQ清理] message_event_records 删除 %d 条（<%d天）", ra, auditDays)
	}
	total += int(ra)

	inboxCut := time.Now().AddDate(0, 0, -inboxDays)
	ri := db.DB.Unscoped().Where("created_at < ?", inboxCut).
		Delete(&model.InboxEvent{}).RowsAffected
	if ri > 0 {
		log.Printf("[MQ清理] inbox_events 删除 %d 条（<%d天）", ri, inboxDays)
	}
	total += int(ri)

	return total
}
