// Package model 数据模型定义。
// 本文件：请求级审计日志模型（P1-2，2026-10-13）。
//
// 与 TenantAuditLog（业务语义层、手工埋点、管理端审计视图数据源）分离——
// RequestAuditLog 是传输层全量审计：记录每一个经鉴权链的请求的
// method/path/tenant/user/ip/ua/status/latency，填补"审计靠记得埋点"的缺口。
// 建表真源见 migrations/032_request_audit.up.sql；AutoMigrate 幂等共存。
package model

import "time"

// RequestAuditLog 请求级审计日志（传输层）。
// 不持久化 query 串，避免把 URL 中的 token / 验证码等敏感参数写入审计库。
type RequestAuditLog struct {
	ID        uint      `gorm:"primaryKey" json:"id"`                           // 主键
	TenantID  uint      `gorm:"index;not null;default:0" json:"tenant_id"`      // 租户ID（未解析到为 0）
	UserID    uint      `gorm:"index;not null;default:0" json:"user_id"`        // 操作用户ID（未认证为 0）
	Method    string    `gorm:"size:10;not null" json:"method"`                 // HTTP 方法
	Path      string    `gorm:"size:512;not null" json:"path"`                  // 请求路径
	Status    int       `gorm:"not null;default:0" json:"status"`               // HTTP 状态码
	LatencyMs int       `gorm:"not null;default:0" json:"latency_ms"`           // 处理耗时（毫秒）
	IP        string    `gorm:"size:45;not null;default:''" json:"ip"`          // 客户端IP（兼容 IPv6）
	UserAgent string    `gorm:"size:512;not null;default:''" json:"user_agent"` // 用户代理
	CreatedAt time.Time `json:"created_at"`                                     // 创建时间
}

// TableName 指定表名：request_audit_logs
func (RequestAuditLog) TableName() string {
	return "request_audit_logs"
}
