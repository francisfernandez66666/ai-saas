package model

import (
	"encoding/json"
	"time"
)

// ============================================================
// 流程引擎模型 - 流程定义 + 流程实例
// 流程引擎只看route字段跳节点，不关心决策细节
// 为什么分离定义和实例？一个流程定义可以有多个运行中的实例
// ============================================================

// ---- 节点类型常量 ----
const (
	NodeTypeStart     = "start"      // 开始节点
	NodeTypeAI        = "ai"         // AI对话节点
	NodeTypeStrategy  = "strategy"   // 策略中心调用节点
	NodeTypeHuman     = "human"      // 人工介入节点
	NodeTypeCondition = "condition"  // 条件判断节点
	NodeTypeTagUpdate = "tag_update" // 标签更新节点
	NodeTypeWait      = "wait"       // 等待节点
	NodeTypeEnd       = "end"        // 结束节点
)

// FlowDefinition 流程定义表
type FlowDefinition struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	TenantID    uint   `gorm:"index;not null;default:0" json:"tenant_id"` // 租户ID（0=系统预置流程；>0=租户私有）SaaS多租户隔离
	Name        string `gorm:"size:100;not null" json:"name"`             // 流程名称
	Code        string `gorm:"size:50;uniqueIndex" json:"code"`           // 流程编码
	Description string `gorm:"size:255" json:"description"`               // 描述
	// NodesJSON 节点定义JSON数组
	// 每个节点: {id, type, name, config, next_nodes}
	NodesJSON string `gorm:"column:nodes;type:text" json:"nodes_json"`
	// EdgesJSON 连线定义JSON数组
	// 每条边: {from, to, condition}
	EdgesJSON   string    `gorm:"column:edges;type:text" json:"edges_json"`
	StartNodeID string    `gorm:"size:50" json:"start_node_id"`    // 开始节点ID
	IsDefault   bool      `gorm:"default:false" json:"is_default"` // 是否默认流程
	Status      int       `gorm:"default:1" json:"status"`         // 状态
	Version     string    `gorm:"size:20" json:"version"`          // 版本
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TableName 指定表名
func (FlowDefinition) TableName() string {
	return "flow_definitions"
}

// FlowNode 流程节点结构
type FlowNode struct {
	ID        string                 `json:"id"`         // 节点ID
	Type      string                 `json:"type"`       // 节点类型
	Name      string                 `json:"name"`       // 节点名称
	Config    map[string]interface{} `json:"config"`     // 节点配置
	NextNodes []string               `json:"next_nodes"` // 后续节点ID列表
}

// FlowEdge 流程连线结构
type FlowEdge struct {
	From      string `json:"from"`      // 源节点ID
	To        string `json:"to"`        // 目标节点ID
	Condition string `json:"condition"` // 条件(route值)
}

// GetNodes 获取节点列表
func (f *FlowDefinition) GetNodes() []FlowNode {
	if f.NodesJSON == "" {
		return []FlowNode{}
	}
	var nodes []FlowNode
	json.Unmarshal([]byte(f.NodesJSON), &nodes)
	return nodes
}

// GetEdges 获取连线列表
func (f *FlowDefinition) GetEdges() []FlowEdge {
	if f.EdgesJSON == "" {
		return []FlowEdge{}
	}
	var edges []FlowEdge
	json.Unmarshal([]byte(f.EdgesJSON), &edges)
	return edges
}

// FlowInstance 流程实例表
type FlowInstance struct {
	ID             uint   `gorm:"primaryKey" json:"id"`
	TenantID       uint   `gorm:"index;not null;default:0" json:"tenant_id"` // 租户ID（SaaS多租户隔离）
	FlowDefID      uint   `gorm:"index;not null" json:"flow_def_id"`         // 流程定义ID
	ConversationID uint   `gorm:"index" json:"conversation_id"`              // 关联会话ID
	CustomerID     uint   `gorm:"index" json:"customer_id"`                  // 客户ID
	CurrentNodeID  string `gorm:"size:50" json:"current_node_id"`            // 当前节点ID
	Status         string `gorm:"size:20;default:running" json:"status"`     // 状态: running/completed/suspended
	// StateJSON 流程实例状态JSON
	// 保存上下文变量、已执行节点历史等
	StateJSON string     `gorm:"type:text" json:"state_json"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// TableName 指定表名
func (FlowInstance) TableName() string {
	return "flow_instances"
}

// FlowInstanceState 流程实例状态
type FlowInstanceState struct {
	RouteResult   string                 `json:"route_result"`   // 当前路由结果
	Context       map[string]interface{} `json:"context"`        // 上下文变量
	ExecutedNodes []string               `json:"executed_nodes"` // 已执行节点列表
	LastNodeID    string                 `json:"last_node_id"`   // 上一个节点
}

// GetState 获取实例状态
func (fi *FlowInstance) GetState() FlowInstanceState {
	state := FlowInstanceState{
		Context:       map[string]interface{}{},
		ExecutedNodes: []string{},
	}
	if fi.StateJSON != "" {
		json.Unmarshal([]byte(fi.StateJSON), &state)
	}
	return state
}

// SaveState 保存实例状态
func (fi *FlowInstance) SaveState(state FlowInstanceState) {
	data, _ := json.Marshal(state)
	fi.StateJSON = string(data)
}
