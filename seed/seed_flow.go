// Package seed 种子数据填充：本文件为流程域——预置默认对话流程定义 default_chat_flow。
package seed

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"encoding/json"
	"log"
)

// ============================================================
// 5. 种子流程定义（默认对话流程）
// ============================================================

// seedFlowDefinition 写入 flow_definitions 表（幂等：已有流程定义则跳过）。
// 作用：预置默认对话流程 default_chat_flow（策略中心决策→AI对话/人工介入/养鱼模式→循环打标）；
// 流程节点与连线以 JSON 存储于 NodesJSON/EdgesJSON，作为会话编排引擎的默认蓝图。
func seedFlowDefinition() {
	var count int64
	db.DB.Model(&model.FlowDefinition{}).Count(&count)
	if count > 0 {
		log.Println("流程定义数据已存在，跳过")
		return
	}

	// 构建节点
	nodes := []model.FlowNode{
		{
			ID:        "start",
			Type:      "start",
			Name:      "开始",
			Config:    map[string]interface{}{},
			NextNodes: []string{"strategy_1"},
		},
		{
			ID:   "strategy_1",
			Type: "strategy",
			Name: "策略中心决策",
			Config: map[string]interface{}{
				"conditions": []string{"ai", "human", "fish"},
			},
			NextNodes: []string{"ai_chat", "human_intervene", "fish_mode"},
		},
		{
			ID:        "ai_chat",
			Type:      "ai",
			Name:      "AI对话",
			Config:    map[string]interface{}{},
			NextNodes: []string{"tag_update_1"},
		},
		{
			ID:        "human_intervene",
			Type:      "human",
			Name:      "人工介入",
			Config:    map[string]interface{}{},
			NextNodes: []string{"tag_update_1"},
		},
		{
			ID:        "fish_mode",
			Type:      "wait",
			Name:      "养鱼模式",
			Config:    map[string]interface{}{},
			NextNodes: []string{"strategy_1"},
		},
		{
			ID:   "tag_update_1",
			Type: "tag_update",
			Name: "更新标签",
			Config: map[string]interface{}{
				"add_tags": []string{},
			},
			NextNodes: []string{"strategy_1"},
		},
		{
			ID:        "end",
			Type:      "end",
			Name:      "结束",
			Config:    map[string]interface{}{},
			NextNodes: []string{},
		},
	}

	// 构建连线
	edges := []model.FlowEdge{
		{From: "start", To: "strategy_1", Condition: ""},
		{From: "strategy_1", To: "ai_chat", Condition: "ai"},
		{From: "strategy_1", To: "human_intervene", Condition: "human"},
		{From: "strategy_1", To: "fish_mode", Condition: "fish"},
		{From: "ai_chat", To: "tag_update_1", Condition: ""},
		{From: "human_intervene", To: "tag_update_1", Condition: ""},
		{From: "fish_mode", To: "strategy_1", Condition: ""},
		{From: "tag_update_1", To: "strategy_1", Condition: ""},
	}

	nodesJSON, _ := json.Marshal(nodes)
	edgesJSON, _ := json.Marshal(edges)

	flowDef := &model.FlowDefinition{
		Name:        "默认对话流程",
		Code:        "default_chat_flow",
		Description: "标准AI对话流程：策略中心决策→AI对话/人工/养鱼→循环",
		NodesJSON:   string(nodesJSON),
		EdgesJSON:   string(edgesJSON),
		StartNodeID: "start",
		IsDefault:   true,
		Status:      1,
		Version:     "1.0",
	}

	db.DB.Create(flowDef)

	log.Println("已创建默认对话流程")
}

// seedWelcomePaidFlow 预置「付费成功→开通欢迎流程」定义（幂等：按 code 唯一）。
// 由流程引擎 payment 消费者（StartPaymentConsumer）在确认到账时启动；
// 欢迎话术/触达由运营基于该流程实例或 payment_welcome 飞轮事件驱动。
func seedWelcomePaidFlow() {
	var cnt int64
	db.DB.Model(&model.FlowDefinition{}).Where("code = ?", "welcome_paid").Count(&cnt)
	if cnt > 0 {
		return
	}

	nodes := []model.FlowNode{
		{ID: "start", Type: "start", Name: "开始", Config: map[string]interface{}{}, NextNodes: []string{"welcome_tag"}},
		{
			ID:   "welcome_tag",
			Type: "tag_update",
			Name: "标记已付费待欢迎",
			Config: map[string]interface{}{
				"add_tags": []string{"mkt_welcome_sent"},
			},
			NextNodes: []string{"end"},
		},
		{ID: "end", Type: "end", Name: "结束", Config: map[string]interface{}{}, NextNodes: []string{}},
	}
	edges := []model.FlowEdge{
		{From: "start", To: "welcome_tag", Condition: ""},
		{From: "welcome_tag", To: "end", Condition: ""},
	}
	nodesJSON, _ := json.Marshal(nodes)
	edgesJSON, _ := json.Marshal(edges)

	db.DB.Create(&model.FlowDefinition{
		Name:        "开通欢迎流程",
		Code:        "welcome_paid",
		Description: "付费成功触发：标记已付费待欢迎，供运营触达",
		NodesJSON:   string(nodesJSON),
		EdgesJSON:   string(edgesJSON),
		StartNodeID: "start",
		IsDefault:   false,
		Status:      1,
		Version:     "1.0",
	})
	log.Println("已创建开通欢迎流程 welcome_paid")
}
