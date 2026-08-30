// Package model 业务领域模型与 GORM 表结构定义（SaaS 多租户，tenant_id 隔离，写入经自动盖章回调）。
package model

import (
	"time"
)

// ============================================================
// 流程状态机（SAAS_PLAN §十七）
// 记录跨业务系统的流程实例进度：只被流程引擎读写，不对外暴露
// 不派发任务、不调业务系统、不决策下一步
// 并发控制：按 one_id 分片（单用户串行、跨用户并行）+ version 乐观锁
// ============================================================

// FlowStateMachine 流程状态机表
type FlowStateMachine struct {
	ID             uint      `gorm:"primaryKey" json:"id"`                         // 主键ID
	TenantID       uint      `gorm:"index;not null;default:0" json:"tenant_id"`    // 租户ID
	OneID          string    `gorm:"size:64;index" json:"one_id"`                  // OneID（并发分片键）
	FlowInstanceID uint      `gorm:"uniqueIndex;not null" json:"flow_instance_id"` // 流程实例ID（一实例一行）
	CurrentNode    string    `gorm:"size:50" json:"current_node"`                  // 当前节点
	LastEventID    string    `gorm:"size:64" json:"last_event_id"`                 // 最近消费事件ID（幂等）
	HeartbeatTS    time.Time `json:"heartbeat_ts"`                                 // 心跳时间（巡检用）
	Status         string    `gorm:"size:20;default:running;index" json:"status"`  // running/paused/completed
	Version        int64     `gorm:"not null;default:1" json:"version"`            // 乐观锁版本号
	StateJSON      string    `gorm:"type:text" json:"state_json"`                  // 场景流状态快照
	CreatedAt      time.Time `json:"created_at"`                                   // 创建时间
	UpdatedAt      time.Time `json:"updated_at"`                                   // 更新时间
}

// TableName 指定表名
func (FlowStateMachine) TableName() string {
	return "flow_state_machines"
}

// 状态常量
const (
	SMStatusRunning   = "running"
	SMStatusPaused    = "paused"
	SMStatusCompleted = "completed"
)
