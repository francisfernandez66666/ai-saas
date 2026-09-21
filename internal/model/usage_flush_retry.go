// UsageFlushRetry 计量扣减挂账表（2026-09-20 审计批 P0-1）：
// UsageSink flush 采用"写前落表→同事务核销扣减"，任何一环失败/进程崩溃，
// 欠账都以行形态存活，由启动即扫 + 周期 sweep 原子补扣——弃批从"永久漏账"降级为"延后扣"。
package model

import "time"

// UsageFlushRetry 一行 = 一个租户一笔待核销的 token 扣减欠账。
type UsageFlushRetry struct {
	ID        uint      `gorm:"primaryKey" json:"id"`    // 主键ID
	TenantID  uint      `gorm:"index" json:"tenant_id"`  // 租户ID（显式落列，后台链路无请求 ctx）
	Tokens    int64     `json:"tokens"`                  // 待扣 token 数（>0）
	CreatedAt time.Time `gorm:"index" json:"created_at"` // 挂账时间（sweep 按龄告警用）
}

// TableName 指定表名
func (UsageFlushRetry) TableName() string {
	return "usage_flush_retry"
}
