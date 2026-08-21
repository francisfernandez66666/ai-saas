package flow

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"log"
)

// ============================================================
// 流程引擎 - 节点执行器
// 每种节点类型对应一个执行函数
// 设计原则：每个节点只做自己的事，不关心其他节点
// ============================================================

// ExecuteNode 执行节点
// 根据节点类型分发到对应的执行函数
func (e *Engine) ExecuteNode(node model.FlowNode, ctx *FlowContext) NodeResult {
	switch node.Type {
	case NodeTypeStart:
		return e.executeStartNode(node, ctx)
	case NodeTypeStrategy:
		return e.executeStrategyNode(node, ctx)
	case NodeTypeAI:
		return e.executeAINode(node, ctx)
	case NodeTypeHuman:
		return e.executeHumanNode(node, ctx)
	case NodeTypeCondition:
		return e.executeConditionNode(node, ctx)
	case NodeTypeTagUpdate:
		return e.executeTagUpdateNode(node, ctx)
	case NodeTypeWait:
		return e.executeWaitNode(node, ctx)
	case NodeTypeEnd:
		return e.executeEndNode(node, ctx)
	default:
		log.Printf("[流程引擎] 未知节点类型: %s", node.Type)
		return NodeResult{
			Status: "error",
		}
	}
}

// executeStartNode 开始节点
// 作用：流程入口，初始化上下文
func (e *Engine) executeStartNode(node model.FlowNode, ctx *FlowContext) NodeResult {
	log.Printf("[流程引擎] 执行开始节点: %s", node.Name)

	// 开始节点直接进入下一个节点
	return NodeResult{
		NextNodeID: getFirstNextNode(node),
		Status:     "completed",
	}
}

// executeStrategyNode 策略中心调用节点
// 作用：调用策略中心引擎，获取路由结果
// 策略中心决定下一步怎么走
func (e *Engine) executeStrategyNode(node model.FlowNode, ctx *FlowContext) NodeResult {
	log.Printf("[流程引擎] 执行策略中心节点: %s", node.Name)

	// 策略中心的实际调用在chat handler中完成
	// 这里只负责流程流转
	// 路由结果已经存在ctx.RouteResult中

	// 根据路由结果选择下一个节点
	nextNode := findNextNodeByCondition(node, ctx.RouteResult)

	return NodeResult{
		NextNodeID: nextNode,
		Output: map[string]interface{}{
			"route_result": ctx.RouteResult,
		},
		Status: "completed",
	}
}

// executeAINode AI对话节点
// 作用：AI回复客户，等待客户回复
// 这是一个"等待节点"，执行后暂停，等客户回复后再推进
func (e *Engine) executeAINode(node model.FlowNode, ctx *FlowContext) NodeResult {
	log.Printf("[流程引擎] 执行AI对话节点: %s", node.Name)

	// AI对话节点是等待型节点
	// 流程在这里暂停，等客户回复后再回到策略中心节点
	return NodeResult{
		NextNodeID: "", // 不自动推进，等客户消息触发
		Output: map[string]interface{}{
			"mode": "ai",
		},
		Status: "waiting",
	}
}

// executeHumanNode 人工介入节点
// 作用：转人工，通知销售
// 这也是一个"等待节点"，等人工处理完后再决定下一步
func (e *Engine) executeHumanNode(node model.FlowNode, ctx *FlowContext) NodeResult {
	log.Printf("[流程引擎] 执行人工介入节点: %s", node.Name)

	// 更新会话模式为人工
	if ctx.ConversationID > 0 {
		db.DB.Model(&model.Conversation{}).
			Where("id = ?", ctx.ConversationID).
			Update("mode", "human")
	}

	return NodeResult{
		NextNodeID: "", // 等待人工处理
		Output: map[string]interface{}{
			"mode": "human",
		},
		Status: "waiting",
	}
}

// executeConditionNode 条件判断节点
// 作用：根据条件选择下一个节点
// 条件来自上下文中的RouteResult
func (e *Engine) executeConditionNode(node model.FlowNode, ctx *FlowContext) NodeResult {
	log.Printf("[流程引擎] 执行条件判断节点: %s, route=%s", node.Name, ctx.RouteResult)

	// 根据路由结果选择下一个节点
	nextNode := findNextNodeByCondition(node, ctx.RouteResult)

	return NodeResult{
		NextNodeID: nextNode,
		Status:     "completed",
	}
}

// executeTagUpdateNode 标签更新节点
// 作用：更新客户标签
func (e *Engine) executeTagUpdateNode(node model.FlowNode, ctx *FlowContext) NodeResult {
	log.Printf("[流程引擎] 执行标签更新节点: %s", node.Name)

	// 从节点配置中获取要添加/移除的标签
	// config: {add_tags: [], remove_tags: []}
	if addTags, ok := node.Config["add_tags"].([]interface{}); ok {
		for _, tag := range addTags {
			log.Printf("  添加标签: %v", tag)
		}
	}

	return NodeResult{
		NextNodeID: getFirstNextNode(node),
		Status:     "completed",
	}
}

// executeWaitNode 等待节点
// 作用：等待一段时间或某个事件
func (e *Engine) executeWaitNode(node model.FlowNode, ctx *FlowContext) NodeResult {
	log.Printf("[流程引擎] 执行等待节点: %s", node.Name)

	// 等待节点是暂停型节点
	return NodeResult{
		NextNodeID: "",
		Status:     "waiting",
	}
}

// executeEndNode 结束节点
// 作用：流程结束
func (e *Engine) executeEndNode(node model.FlowNode, ctx *FlowContext) NodeResult {
	log.Printf("[流程引擎] 执行结束节点: %s", node.Name)

	return NodeResult{
		NextNodeID: "",
		Status:     "completed",
	}
}

// ============================================================
// 辅助函数
// ============================================================

// getFirstNextNode 获取第一个后续节点
func getFirstNextNode(node model.FlowNode) string {
	if len(node.NextNodes) > 0 {
		return node.NextNodes[0]
	}
	return ""
}

// findNextNodeByCondition 根据条件找后续节点
// 节点的NextNodes按顺序存储，条件在config的conditions字段
// 简化实现：NextNodes的顺序对应条件顺序
// conditions: ["ai", "human", "fish"]
func findNextNodeByCondition(node model.FlowNode, routeResult string) string {
	// 从config中获取条件列表
	conditions := []string{}
	if conds, ok := node.Config["conditions"].([]interface{}); ok {
		for _, c := range conds {
			conditions = append(conditions, c.(string))
		}
	}

	// 如果没有配置条件，默认第一个
	if len(conditions) == 0 {
		return getFirstNextNode(node)
	}

	// 找匹配的条件索引
	for i, cond := range conditions {
		if cond == routeResult {
			if i < len(node.NextNodes) {
				return node.NextNodes[i]
			}
			break
		}
	}

	// 没找到匹配的，返回第一个
	return getFirstNextNode(node)
}
