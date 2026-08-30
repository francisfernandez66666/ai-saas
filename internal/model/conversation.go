// Package model 业务领域模型与 GORM 表结构定义（SaaS 多租户，tenant_id 隔离，写入经自动盖章回调）。
package model

import (
	"encoding/json"
	"time"
)

// ============================================================
// 会话模型 - 一次完整的客户对话
// 核心是会话状态S，记录策略引擎运行时的状态
// 为什么单独存状态？策略中心是无状态的，状态必须持久化
// ============================================================

// Conversation 会话表
type Conversation struct {
	ID             uint   `gorm:"primaryKey" json:"id"` // 注释：主键ID
	TenantID       uint   `gorm:"index;not null;default:0" json:"tenant_id"` // 租户ID（SaaS多租户隔离）
	CustomerID     uint   `gorm:"index;not null" json:"customer_id"`         // 客户ID
	AssignedUserID uint   `json:"assigned_user_id"`                          // 归属销售ID
	FlowInstanceID uint   `json:"flow_instance_id"`                          // 流程实例ID
	Status         string `gorm:"size:20;default:active" json:"status"`      // 状态: active/closed/transferred
	Channel        string `gorm:"size:20;default:web" json:"channel"`        // 渠道: web/wechat/phone
	SessionID      string `gorm:"size:64;index" json:"session_id"`            // 外部会话ID（OpenAPI对话端点：同一external_user_id下多轮会话隔离）
	Mode           string `gorm:"size:20;default:ai" json:"mode"`            // 当前模式: ai/human/fish(养鱼)

	// ---- 会话状态S ----
	Attempts              int        `gorm:"default:0" json:"attempts"`                // 已抛锚次数
	HookCount             int        `gorm:"default:0" json:"hook_count"`              // 成功接钩次数
	LastTid               string     `gorm:"size:50" json:"last_tid"`                  // 上一条话术模板ID
	LastAnchorType        int        `json:"last_anchor_type"`                         // 上一次锚类型
	Emotion               string     `gorm:"size:20;default:neutral" json:"emotion"`   // 情绪: positive/neutral/negative
	HighIntentRounds      int        `gorm:"default:0" json:"high_intent_rounds"`      // 高意向持续轮数
	CurrentStage          int        `gorm:"default:0" json:"current_stage"`           // 当前心智阶段(0-5六段)
	SilentDuration        int        `gorm:"default:0" json:"silent_duration"`         // 沉默时长(秒)
	LastMessageAt         *time.Time `json:"last_message_at"`                          // 最后消息时间
	LastHumanReplyAt      *time.Time `json:"last_human_reply_at"`                      // 最后人工回复时间
	IsHumanLocked         bool       `gorm:"default:false" json:"is_human_locked"`     // 是否人工锁定模式
	IsAiReplyEnabled      bool       `gorm:"default:true" json:"is_ai_reply_enabled"`  // AI回复开关（顾问可手动切换）
	GuidedDisabled        bool       `gorm:"default:false" json:"guided_disabled"`     // 引导式反问已关闭
	GuidedRemainingRounds int        `gorm:"default:0" json:"guided_remaining_rounds"` // 剩余引导式反问轮数(0=不限/已关闭)

	// ---- 软接管相关 ----
	PendingHandoff    bool       `gorm:"default:false" json:"pending_handoff"` // 是否待人工接管（软接管状态）
	HandoffNotifiedAt *time.Time `json:"handoff_notified_at"`                  // 通知销售的时间（用于超时兜底判断）

	// ---- 状态S的JSON存储（扩展用）----
	StateJSON string `gorm:"type:text" json:"state_json"` // 完整会话状态JSON

	CreatedAt time.Time `json:"created_at"` // 注释：创建时间
	UpdatedAt time.Time `json:"updated_at"` // 注释：更新时间
}

// TableName 指定表名
func (Conversation) TableName() string {
	return "conversations"
}

// SessionState 会话状态S的完整结构
// 对应文档中的"会话状态S字段"
type SessionState struct {
	Attempts         int     `json:"attempts"`           // 已抛锚次数
	HookCount        int     `json:"hook_count"`         // 成功接钩次数
	HookRate         float64 `json:"hook_rate"`          // 接钩率 = hook_count / attempts
	LastTid          string  `json:"last_tid"`           // 上一条话术模板ID
	LastAnchorType   int     `json:"last_anchor_type"`   // 上一次锚类型
	Emotion          string  `json:"emotion"`            // 情绪标签
	HighIntentRounds int     `json:"high_intent_rounds"` // 高意向持续轮数
	SilentDuration   int     `json:"silent_duration"`    // 沉默时长（秒）
	CurrentStage     int     `json:"current_stage"`      // 当前心智阶段(0-5)
}

// GetState 获取会话状态S
func (c *Conversation) GetState() SessionState {
	state := SessionState{
		Attempts:         c.Attempts,
		HookCount:        c.HookCount,
		LastTid:          c.LastTid,
		LastAnchorType:   c.LastAnchorType,
		Emotion:          c.Emotion,
		HighIntentRounds: c.HighIntentRounds,
		SilentDuration:   c.SilentDuration,
		CurrentStage:     c.CurrentStage,
	}
	// 计算接钩率
	if c.Attempts > 0 {
		state.HookRate = float64(c.HookCount) / float64(c.Attempts)
	} else {
		state.HookRate = 0
	}
	// 如果有JSON状态，合并
	if c.StateJSON != "" {
		var jsonState SessionState
		if err := json.Unmarshal([]byte(c.StateJSON), &jsonState); err == nil {
			// 用JSON中的值覆盖（如果有）
			if jsonState.HookRate > 0 {
				state.HookRate = jsonState.HookRate
			}
		}
	}
	return state
}

// SaveState 保存会话状态
func (c *Conversation) SaveState(state SessionState) {
	c.Attempts = state.Attempts
	c.HookCount = state.HookCount
	c.LastTid = state.LastTid
	c.LastAnchorType = state.LastAnchorType
	c.Emotion = state.Emotion
	c.HighIntentRounds = state.HighIntentRounds
	c.SilentDuration = state.SilentDuration
	c.CurrentStage = state.CurrentStage
	// 存JSON备份
	data, _ := json.Marshal(state)
	c.StateJSON = string(data)
}

// IncrementAttempts 增加抛锚次数
func (c *Conversation) IncrementAttempts() {
	c.Attempts++
	// 同步更新状态
	state := c.GetState()
	state.Attempts = c.Attempts
	c.SaveState(state)
}

// RecordHook 记录一次接钩成功
func (c *Conversation) RecordHook() {
	c.HookCount++
	// 同步更新状态
	state := c.GetState()
	state.HookCount = c.HookCount
	c.SaveState(state)
}

// UpdateSilentDuration 更新沉默时长
func (c *Conversation) UpdateSilentDuration() {
	if c.LastMessageAt != nil {
		c.SilentDuration = int(time.Since(*c.LastMessageAt).Seconds())
	}
}
