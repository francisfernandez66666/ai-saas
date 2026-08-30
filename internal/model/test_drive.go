// Package model 业务领域模型与 GORM 表结构定义（SaaS 多租户，tenant_id 隔离，写入经自动盖章回调）。
package model

import (
	"time"
)

// ============================================================
// 试驾预约模型 - 客户到店试驾的预约与结果记录
// 已留资/到店客户由顾问或 AI 协助预约试驾；结果反馈供转化分析
// ============================================================

// TestDrive 试驾预约表
// TenantID 租户归属：SaaS 多租户隔离（fail-closed），读取经 db.RQ(c) 自动带 tenant_id 过滤
type TestDrive struct {
	ID           uint      `gorm:"primaryKey" json:"id"`                      // 主键ID
	TenantID     uint      `gorm:"index;not null;default:0" json:"tenant_id"` // 租户ID（SaaS多租户隔离）
	CustomerID   uint      `gorm:"index;not null" json:"customer_id"`         // 客户ID
	AdvisorID    uint      `gorm:"index;not null" json:"advisor_id"`          // 顾问ID
	Status       string    `gorm:"size:20;default:pending" json:"status"`     // pending/completed/cancelled
	ScheduledAt  time.Time `json:"scheduled_at"`                              // 预约试驾时间
	ModelName    string    `gorm:"size:100" json:"model_name"`                // 试驾车型
	ContactName  string    `gorm:"size:50" json:"contact_name"`               // 联系人
	ContactPhone string    `gorm:"size:20" json:"contact_phone"`              // 联系电话
	Location     string    `gorm:"size:200" json:"location"`                  // 试驾地点/门店
	Note         string    `gorm:"type:text" json:"note"`                     // 备注
	Result       string    `gorm:"type:text" json:"result"`                   // 试驾结果反馈
	CreatedAt    time.Time `json:"created_at"`                                // 创建时间
	UpdatedAt    time.Time `json:"updated_at"`                                // 更新时间
}

// TableName 指定表名
func (TestDrive) TableName() string {
	return "test_drives"
}
