// Package service 业务服务层：消息中心审计/收件箱数据治理（P1-37）
package service

import (
	"log"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/mq"
	"ai-scrm/internal/runtimecfg"
)

// CleanupMQTables 清理消息中心留存数据（P1-37，2026-09-09）
// message_event_records：审计默认保留 30 天；inbox_events：收件箱默认保留 90 天。
// 保留天数可用 system_configs 覆盖：mq_audit_retention_days / mq_inbox_retention_days。
// 条件 DELETE 幂等，多实例由调用方 Redis 选主兜底。
//
// G-15②：dead_letter 行**不走这条 30 天清理**。重试耗尽后 Kafka offset 已提交、进程内总线
// 也无重投，这一行就是那条事件的**唯一副本**（payload + 失败原因都在里面）——按普通审计一起
// 删掉，等于把"我们丢过什么"这条证据按保留期准时销毁，而丢事件恰恰是最需要事后追查的事。
// 死信单独有 `mq_dead_letter_retention_days` 控制：**默认 0 = 永不删**，需要清的时候显式配。
// 死信存量由 /status/detail 的 mq_event_audit 观测位暴露，不会无声堆积。
func CleanupMQTables() int {
	auditDays := runtimecfg.DefaultSystemConfigService.GetInt("mq_audit_retention_days", 30)
	inboxDays := runtimecfg.DefaultSystemConfigService.GetInt("mq_inbox_retention_days", 90)
	dlDays := runtimecfg.DefaultSystemConfigService.GetInt("mq_dead_letter_retention_days", 0)

	total := 0

	auditCut := time.Now().AddDate(0, 0, -auditDays)
	ra := db.DB.Unscoped().Where("created_at < ? AND status <> ?", auditCut, mq.StatusDeadLetter).
		Delete(&model.MessageEventRecord{}).RowsAffected
	if ra > 0 {
		log.Printf("[MQ清理] message_event_records 删除 %d 条（<%d天，不含死信）", ra, auditDays)
	}
	total += int(ra)

	if dlDays > 0 {
		dlCut := time.Now().AddDate(0, 0, -dlDays)
		rd := db.DB.Unscoped().Where("created_at < ? AND status = ?", dlCut, mq.StatusDeadLetter).
			Delete(&model.MessageEventRecord{}).RowsAffected
		if rd > 0 {
			log.Printf("[MQ清理] message_event_records 死信删除 %d 条（<%d天，按显式配置）", rd, dlDays)
		}
		total += int(rd)
	} else if n := countDeadLetters(); n > 0 {
		// 只提示不删：让人知道有多少条事件的唯一副本还留着待处理
		log.Printf("[MQ清理] 死信 %d 条已跳过（mq_dead_letter_retention_days=0 表示永不删）", n)
	}

	inboxCut := time.Now().AddDate(0, 0, -inboxDays)
	ri := db.DB.Unscoped().Where("created_at < ?", inboxCut).
		Delete(&model.InboxEvent{}).RowsAffected
	if ri > 0 {
		log.Printf("[MQ清理] inbox_events 删除 %d 条（<%d天）", ri, inboxDays)
	}
	total += int(ri)

	return total
}

// countDeadLetters 数当前在册死信（供跳过日志与观测位复用；查询失败回 0 不阻断清理）
func countDeadLetters() int64 {
	var n int64
	if err := db.DB.Model(&model.MessageEventRecord{}). // g12:platform 平台级数据治理任务，跨租户统计
								Where("status = ?", mq.StatusDeadLetter).Count(&n).Error; err != nil {
		log.Printf("[MQ清理] 死信计数查询失败: %v", err)
		return 0
	}
	return n
}
