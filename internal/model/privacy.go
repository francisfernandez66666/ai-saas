// PIPL 删除权数据模型（C2，2026-09-12）
// 区分两条轨：商家侧"注销后数据保留"（法务资产，见 admin/account/cancel）与客户侧"个人删除请求"（本表）。
// 请求受理后进入 pending，到期（默认 +15d）由日批匿名化；商家也可在 Admin"隐私合规"页手动立即执行。
package model

import "time"

// 删除请求作用域
const (
	DeletionScopeCustomer = "customer" // 终端客户（visitor_key 自证）
	DeletionScopeUser     = "user"     // 平台登录用户（本人员工账号）
)

// 删除请求状态
const (
	DeletionStatusPending    = "pending"    // 已受理待到期执行
	DeletionStatusAnonymized = "anonymized" // 已匿名化（内容列断链，统计列保留）
	DeletionStatusFailed     = "failed"     // 执行失败（可重试/人工介入）
)

// DeletionRequest 个人数据删除请求（PIPL 删除权）
type DeletionRequest struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	TenantID    uint       `gorm:"index;not null" json:"tenant_id"`
	Scope       string     `gorm:"size:20;not null;index" json:"scope"` // customer|user
	CustomerID  uint       `gorm:"index;default:0" json:"customer_id"`  // scope=customer 时非 0
	UserID      uint       `gorm:"index;default:0" json:"user_id"`      // scope=user 时非 0
	Status      string     `gorm:"size:20;not null;default:pending;index" json:"status"`
	RequestedAt time.Time  `json:"requested_at"`
	Deadline    time.Time  `gorm:"index" json:"deadline"` // requested_at + 15d，到期由日批处理
	ProcessedAt *time.Time `json:"processed_at"`          // 实际执行完成时间
	Error       string     `gorm:"size:500" json:"error"` // 失败原因
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// TableName 指定表名
func (DeletionRequest) TableName() string { return "deletion_requests" }
