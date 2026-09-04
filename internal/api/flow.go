// 流程引擎API：流程定义查询与流程实例的启动/推进/查询。
package api

// 流程引擎API：流程定义列表/详情查询，以及流程实例的启动(StartFlow)、推进(AdvanceFlow)与查询。
// 所有查询走db.PQ(c)确保租户隔离。

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/engine/flow"
	"ai-scrm/internal/model"
	"ai-scrm/internal/schema"
	"net/http"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 流程引擎API
// 流程定义管理、流程实例管理、流程推进
// ============================================================

// GetFlowList 获取当前租户的流程定义列表
// 按ID降序返回，包含所有状态的流程定义（草稿/已发布/已归档）
func GetFlowList(c *gin.Context) {
	var flows []model.FlowDefinition
	db.PQ(c).Order("id DESC").Find(&flows)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data:    flows,
	})
}

// GetFlow 获取单个流程定义详情
// 根据URL参数ID查询，不存在时返回404
func GetFlow(c *gin.Context) {
	// 健壮性收口(2026-09-05)：FlowDefinition 为 uint 主键，非法 ID 直接 400，不再打进 PG(22P02)
	id, ok := PathUintID(c)
	if !ok {
		return
	}
	var flowDef model.FlowDefinition
	result := db.PQ(c).First(&flowDef, id)
	if result.Error != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "流程不存在", Data: nil})
		return
	}

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data:    flowDef,
	})
}

// StartFlow 启动一个新流程实例
// 请求体包含流程编码、客户ID、会话ID
// 流程引擎根据流程编码找到定义，创建实例并执行第一个节点
func StartFlow(c *gin.Context) {
	var req schema.FlowStartRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误", Data: nil})
		return
	}

	// 构建流程上下文，携带租户/客户/会话等运行时信息
	flowCtx := &flow.FlowContext{
		TenantID:       db.EffectiveTenantIDFromGin(c),
		CustomerID:     req.CustomerID,
		ConversationID: req.ConversationID,
		RouteResult:    "ai",
	}

	instance, err := flow.DefaultEngine.StartFlow(req.FlowCode, flowCtx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, schema.Response{
			Code:    500,
			Message: "启动流程失败: " + err.Error(),
			Data:    nil,
		})
		return
	}

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "流程已启动",
		Data:    instance,
	})
}

// AdvanceFlow 推进流程到下一个节点
// 请求体包含实例ID和路由选择（决定走向哪个分支）
// 路由结果会影响流程引擎的节点跳转逻辑
func AdvanceFlow(c *gin.Context) {
	var req schema.FlowAdvanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误", Data: nil})
		return
	}

	flowCtx := &flow.FlowContext{
		TenantID:    db.EffectiveTenantIDFromGin(c),
		RouteResult: req.Route,
	}

	instance, err := flow.DefaultEngine.AdvanceFlow(req.InstanceID, req.Route, flowCtx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, schema.Response{
			Code:    500,
			Message: "推进流程失败: " + err.Error(),
			Data:    nil,
		})
		return
	}

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "流程已推进",
		Data:    instance,
	})
}

// GetFlowInstance 获取单个流程实例详情
// 根据实例ID查询，用于查看流程执行状态和历史
func GetFlowInstance(c *gin.Context) {
	// 健壮性收口(2026-09-05)：uint 主键入口校验，非法不再触 DB
	id, ok := PathUintID(c)
	if !ok {
		return
	}
	var instance model.FlowInstance
	result := db.PQ(c).First(&instance, id)
	if result.Error != nil {
		c.JSON(http.StatusNotFound, schema.Response{Code: 404, Message: "流程实例不存在", Data: nil})
		return
	}

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data:    instance,
	})
}

// GetFlowInstanceList 获取当前租户的流程实例列表
// 最多返回最近100条记录，按ID降序排列
func GetFlowInstanceList(c *gin.Context) {
	var instances []model.FlowInstance
	db.PQ(c).Order("id DESC").Limit(100).Find(&instances)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data:    instances,
	})
}
