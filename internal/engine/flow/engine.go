package flow

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	statemachine "ai-scrm/internal/state_machine"
	"errors"
	"fmt"
	"log"
	"time"
)

// ============================================================
// 流程引擎 - 执行器
// 负责启动流程、推进流程、管理流程实例
//
// 设计思想：
//   - 流程引擎只看route字段跳节点，不关心决策细节
//   - 决策细节由策略中心负责，流程引擎只管"走流程"
//   - 预设默认流程：开始→策略中心→(AI对话/转人工/养鱼)→客户回复→标签更新→回到策略中心→...→结束
// ============================================================

// InitEngine 初始化流程引擎
func InitEngine() {
	DefaultEngine = &Engine{}
	DefaultEngine.LoadDefinitions()
	log.Println("流程引擎初始化完成")
}

// LoadDefinitions 加载流程定义
func (e *Engine) LoadDefinitions() {
	var defs []model.FlowDefinition
	db.DB.Where("status = ?", 1).Find(&defs)
	e.definitions = defs
	log.Printf("已加载 %d 个流程定义", len(defs))
}

// ReloadDefinitions 重新加载流程定义
func (e *Engine) ReloadDefinitions() {
	e.LoadDefinitions()
}

// StartFlow 启动一个流程
// 创建流程实例，从开始节点执行
func (e *Engine) StartFlow(flowCode string, ctx *FlowContext) (*model.FlowInstance, error) {
	// 1. 找到流程定义
	var flowDef model.FlowDefinition
	// P2 租户化：流程定义预置(0)+租户私有联合可见，租户自有优先
	result := db.DB.Where("code = ? AND status = ? AND tenant_id IN ?", flowCode, 1, []uint{ctx.TenantID, 0}).
		Order("tenant_id DESC").First(&flowDef)
	if result.Error != nil {
		log.Printf("[流程引擎] 未找到流程定义: %s, 错误: %v", flowCode, result.Error)
		return nil, result.Error
	}

	// 2. 创建流程实例
	instance := &model.FlowInstance{
		TenantID:       ctx.TenantID,
		FlowDefID:      flowDef.ID,
		CustomerID:     ctx.CustomerID,
		ConversationID: ctx.ConversationID,
		CurrentNodeID:  flowDef.StartNodeID,
		Status:         "running",
	}
	db.DB.Create(instance)

	log.Printf("[流程引擎] 启动流程: %s, 实例ID: %d, 开始节点: %s",
		flowCode, instance.ID, flowDef.StartNodeID)

	// 3. 从开始节点执行
	e.executeToWait(instance, &flowDef, ctx)

	// 4. 同步状态机
	e.syncStateMachine(instance)

	return instance, nil
}

// syncStateMachine 同步流程状态机（SAAS_PLAN §十七：状态机只被流程引擎读写）
// one_id 分片键暂用客户占位（c:{customerID}），CDP OneID 落地后切换
func (e *Engine) syncStateMachine(instance *model.FlowInstance) {
	if instance.TenantID == 0 {
		return // 无租户语境的引擎直启不记状态机
	}
	shardKey := fmt.Sprintf("c:%d", instance.CustomerID)
	sm, err := statemachine.Get(instance.TenantID, instance.ID)
	if err != nil {
		log.Printf("[流程引擎] 状态机读取失败: %v", err)
		return
	}
	if sm == nil {
		_, _ = statemachine.Init(instance.TenantID, shardKey, instance.ID, instance.CurrentNodeID)
		return
	}
	if instance.Status == "completed" {
		_ = statemachine.Complete(instance.TenantID, instance.ID)
		return
	}
	// 乐观锁推进，冲突重取重试一次
	for i := 0; i < 2; i++ {
		err := statemachine.Advance(instance.TenantID, instance.ID, instance.CurrentNodeID, sm.Version, "{}")
		if err == nil {
			return
		}
		if errors.Is(err, statemachine.ErrVersionConflict) {
			if sm, err = statemachine.Get(instance.TenantID, instance.ID); err != nil || sm == nil {
				return
			}
			continue
		}
		log.Printf("[流程引擎] 状态机推进失败: %v", err)
		return
	}
}

// AdvanceFlow 推进流程
// 根据路由结果，从当前节点继续执行
func (e *Engine) AdvanceFlow(instanceID uint, routeResult string, ctx *FlowContext) (*model.FlowInstance, error) {
	// 1. 获取流程实例
	var instance model.FlowInstance
	result := db.DB.First(&instance, instanceID)
	if result.Error != nil {
		return nil, result.Error
	}

	// 2. 获取流程定义
	var flowDef model.FlowDefinition
	result = db.DB.First(&flowDef, instance.FlowDefID)
	if result.Error != nil {
		return nil, result.Error
	}

	// 3. 设置路由结果到上下文
	ctx.RouteResult = routeResult

	log.Printf("[流程引擎] 推进流程实例: %d, 当前节点: %s, 路由: %s",
		instanceID, instance.CurrentNodeID, routeResult)

	// 4. 从当前节点继续执行
	e.executeToWait(&instance, &flowDef, ctx)

	// 5. 同步状态机
	e.syncStateMachine(&instance)

	return &instance, nil
}

// executeToWait 执行节点直到遇到等待节点或结束
// 这是流程引擎的核心循环
func (e *Engine) executeToWait(instance *model.FlowInstance, flowDef *model.FlowDefinition, ctx *FlowContext) {
	nodes := flowDef.GetNodes()

	// 循环执行节点，直到遇到等待型节点或结束
	maxSteps := 50 // 防止死循环
	step := 0

	for step < maxSteps {
		step++

		// 找到当前节点
		currentNode := findNodeByID(nodes, instance.CurrentNodeID)
		if currentNode == nil {
			log.Printf("[流程引擎] 未找到节点: %s", instance.CurrentNodeID)
			break
		}

		// 执行节点
		nodeResult := e.ExecuteNode(*currentNode, ctx)

		// 更新实例状态
		state := instance.GetState()
		state.ExecutedNodes = append(state.ExecutedNodes, instance.CurrentNodeID)
		state.LastNodeID = instance.CurrentNodeID
		if nodeResult.Output != nil {
			if state.Context == nil {
				state.Context = map[string]interface{}{}
			}
			for k, v := range nodeResult.Output {
				state.Context[k] = v
			}
		}
		state.RouteResult = ctx.RouteResult
		instance.SaveState(state)

		// 如果是等待节点，暂停
		if nodeResult.Status == "waiting" {
			log.Printf("[流程引擎] 流程暂停在节点: %s (%s)", currentNode.Name, currentNode.Type)
			break
		}

		// 如果是结束节点，完成
		if currentNode.Type == NodeTypeEnd {
			instance.Status = "completed"
			now := time.Now()
			instance.EndedAt = &now
			log.Printf("[流程引擎] 流程完成: %d", instance.ID)
			break
		}

		// 移动到下一个节点
		if nodeResult.NextNodeID != "" {
			instance.CurrentNodeID = nodeResult.NextNodeID
		} else {
			// 没有下一个节点了
			log.Printf("[流程引擎] 无后续节点，流程暂停")
			break
		}
	}

	// 保存实例状态
	db.DB.Save(instance)
}

// findNodeByID 根据ID找节点
func findNodeByID(nodes []model.FlowNode, nodeID string) *model.FlowNode {
	for i := range nodes {
		if nodes[i].ID == nodeID {
			return &nodes[i]
		}
	}
	return nil
}

// GetDefaultFlow 获取默认流程
func (e *Engine) GetDefaultFlow() *model.FlowDefinition {
	for i := range e.definitions {
		if e.definitions[i].IsDefault {
			return &e.definitions[i]
		}
	}
	return nil
}
