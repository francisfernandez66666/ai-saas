// 流程引擎核心决策逻辑单元测试（2026-09-01 补齐零覆盖）
// 覆盖：条件跳转（findNextNodeByCondition）、首节点（getFirstNextNode）、节点查找（findNodeByID）、
// 默认流程选取（GetDefaultFlow）、节点分发（executeStart/condition/wait/end）
package flow

import (
	"testing"

	"ai-scrm/internal/model"
)

// TestFindNextNodeByCondition 条件跳转：精确匹配 / 无匹配回退首个 / 无条件取首个 / 索引越界回退首个
func TestFindNextNodeByCondition(t *testing.T) {
	node := model.FlowNode{
		ID:        "cond",
		Type:      "condition",
		Config:    map[string]interface{}{"conditions": []interface{}{"ai", "human", "fish"}},
		NextNodes: []string{"node-ai", "node-human", "node-fish"},
	}
	// 精确匹配
	if got := findNextNodeByCondition(node, "human"); got != "node-human" {
		t.Errorf("human 应跳 node-human, got %s", got)
	}
	// 无匹配 → 回退首个
	if got := findNextNodeByCondition(node, "unknown"); got != "node-ai" {
		t.Errorf("未知路由应回退首个, got %s", got)
	}
	// 无条件 → 取首个
	noCond := model.FlowNode{NextNodes: []string{"a", "b"}}
	if got := findNextNodeByCondition(noCond, "x"); got != "a" {
		t.Errorf("无条件应取首个, got %s", got)
	}
	// 匹配索引越界（routing 数据缺失）→ 回退首个
	broken := model.FlowNode{
		Config:    map[string]interface{}{"conditions": []interface{}{"ai", "human"}},
		NextNodes: []string{"only-one"},
	}
	if got := findNextNodeByCondition(broken, "human"); got != "only-one" {
		t.Errorf("索引越界应回退首个, got %s", got)
	}
	// 空节点 → 空
	if got := findNextNodeByCondition(model.FlowNode{}, "x"); got != "" {
		t.Errorf("空节点应返回空串, got %s", got)
	}
}

// TestGetFirstNextNode 首节点读取：空与非空
func TestGetFirstNextNode(t *testing.T) {
	if got := getFirstNextNode(model.FlowNode{NextNodes: []string{"a", "b"}}); got != "a" {
		t.Errorf("非空应返回首个, got %s", got)
	}
	if got := getFirstNextNode(model.FlowNode{}); got != "" {
		t.Errorf("空应返回空串, got %s", got)
	}
}

// TestFindNodeByID 节点查找：命中/未命中/空列表
func TestFindNodeByID(t *testing.T) {
	nodes := []model.FlowNode{{ID: "s", Type: "start"}, {ID: "e", Type: "end"}}
	if got := findNodeByID(nodes, "s"); got == nil || got.Type != "start" {
		t.Errorf("应命中 start 节点, got %+v", got)
	}
	if got := findNodeByID(nodes, "missing"); got != nil {
		t.Errorf("不存在的 id 应返回 nil, got %+v", got)
	}
	if got := findNodeByID(nil, "s"); got != nil {
		t.Errorf("空列表应返回 nil, got %+v", got)
	}
}

// TestGetDefaultFlow 默认流程选取：命中 IsDefault；无默认返回 nil
func TestGetDefaultFlow(t *testing.T) {
	e := &Engine{definitions: []model.FlowDefinition{
		{ID: 1, Name: "非默认", IsDefault: false},
		{ID: 2, Name: "默认", IsDefault: true},
	}}
	if got := e.GetDefaultFlow(); got == nil || got.ID != 2 {
		t.Errorf("应选中 IsDefault 流程, got %+v", got)
	}
	e2 := &Engine{definitions: []model.FlowDefinition{{ID: 1, IsDefault: false}}}
	if got := e2.GetDefaultFlow(); got != nil {
		t.Errorf("无默认流程应返回 nil, got %+v", got)
	}
}

// TestExecutePureNodes 纯节点分发：start 取首个 / condition 依路由 / wait 恒 waiting / end 恒 completed
func TestExecutePureNodes(t *testing.T) {
	e := &Engine{definitions: []model.FlowDefinition{{ID: 1, IsDefault: true}}}

	// start：返回首个后续节点
	startRes := e.executeStartNode(model.FlowNode{NextNodes: []string{"n1", "n2"}}, &FlowContext{})
	if startRes.NextNodeID != "n1" || startRes.Status != "completed" {
		t.Errorf("start 应返回首个+completed, got %+v", startRes)
	}

	// condition：按 RouteResult 跳转
	condNode := model.FlowNode{
		Type:      "condition",
		Config:    map[string]interface{}{"conditions": []interface{}{"ai", "human"}},
		NextNodes: []string{"to-ai", "to-human"},
	}
	condRes := e.executeConditionNode(condNode, &FlowContext{RouteResult: "human"})
	if condRes.NextNodeID != "to-human" {
		t.Errorf("condition 按路由应跳 to-human, got %s", condRes.NextNodeID)
	}

	// wait：恒 waiting
	waitRes := e.executeWaitNode(model.FlowNode{}, &FlowContext{})
	if waitRes.Status != "waiting" {
		t.Errorf("wait 应恒 waiting, got %+v", waitRes)
	}

	// end：恒 completed 且无后续
	endRes := e.executeEndNode(model.FlowNode{}, &FlowContext{})
	if endRes.Status != "completed" || endRes.NextNodeID != "" {
		t.Errorf("end 应 completed+空Next, got %+v", endRes)
	}
}
