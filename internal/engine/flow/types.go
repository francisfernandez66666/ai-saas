// Package flow 流程引擎：类型定义（Engine/FlowContext/NodeResult 与节点类型常量）。
package flow

// ============================================================
// 流程引擎 - 类型定义
// 流程引擎只看route字段跳节点，不关心决策细节
// 为什么需要流程引擎？策略中心只管单轮决策，流程引擎管整体会话生命周期
// ============================================================

import "ai-scrm/internal/model"

// Engine 流程引擎结构体
// SaaS 化改造：支持多租户下的数据隔离
// TenantID=0 表示查询所有租户数据（启动时默认），>0 则严格隔离
type Engine struct {
	TenantID uint // 当前租户ID
	// 可以在这里缓存流程定义（LoadDefinitions 从库加载启用中的流程）
	definitions []model.FlowDefinition
}

// DefaultEngine 默认流程引擎实例
var DefaultEngine *Engine

// FlowContext 流程执行上下文
// 在节点之间传递数据
type FlowContext struct {
	TenantID       uint                   // 租户ID（P2：实例盖章+状态机隔离）
	CustomerID     uint                   // 客户ID
	ConversationID uint                   // 会话ID
	UserID         uint                   // 操作人ID
	RouteResult    string                 // 路由结果（用于条件判断节点）
	Data           map[string]interface{} // 通用数据容器
}

// NodeResult 节点执行结果
type NodeResult struct {
	NextNodeID string                 // 下一个节点ID
	Output     map[string]interface{} // 节点输出数据
	Status     string                 // 节点状态: completed/waiting/error
}

// ============================================================
// 节点类型常量（与model中保持一致）
// ============================================================

// 流程引擎节点类型常量（与 model 中的 FlowNodeType 保持一致）
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
