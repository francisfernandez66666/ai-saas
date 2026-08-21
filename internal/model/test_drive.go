package model

import (
	"time"
)

type TestDrive struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	TenantID     uint      `gorm:"index;not null;default:0" json:"tenant_id"` // 租户ID（SaaS多租户隔离）
	CustomerID   uint      `gorm:"index;not null" json:"customer_id"`
	AdvisorID    uint      `gorm:"index;not null" json:"advisor_id"`
	Status       string    `gorm:"size:20;default:pending" json:"status"` // pending/completed/cancelled
	ScheduledAt  time.Time `json:"scheduled_at"`                          // 预约试驾时间
	ModelName    string    `gorm:"size:100" json:"model_name"`            // 试驾车型
	ContactName  string    `gorm:"size:50" json:"contact_name"`           // 联系人
	ContactPhone string    `gorm:"size:20" json:"contact_phone"`          // 联系电话
	Location     string    `gorm:"size:200" json:"location"`              // 试驾地点/门店
	Note         string    `gorm:"type:text" json:"note"`                 // 备注
	Result       string    `gorm:"type:text" json:"result"`               // 试驾结果反馈
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (TestDrive) TableName() string {
	return "test_drives"
}
