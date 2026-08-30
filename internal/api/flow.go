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

// GetFlowList 获取流程定义列表
func GetFlowList(c *gin.Context) {
	var flows []model.FlowDefinition
	db.PQ(c).Order("id DESC").Find(&flows)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data:    flows,
	})
}

// GetFlow 获取流程定义详情
func GetFlow(c *gin.Context) {
	id := c.Param("id")

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

// StartFlow 启动流程
func StartFlow(c *gin.Context) {
	var req schema.FlowStartRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, schema.Response{Code: 400, Message: "参数错误", Data: nil})
		return
	}

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

// AdvanceFlow 推进流程
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

// GetFlowInstance 获取流程实例
func GetFlowInstance(c *gin.Context) {
	id := c.Param("id")

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

// GetFlowInstanceList 获取流程实例列表
func GetFlowInstanceList(c *gin.Context) {
	var instances []model.FlowInstance
	db.PQ(c).Order("id DESC").Limit(100).Find(&instances)

	c.JSON(http.StatusOK, schema.Response{
		Code:    0,
		Message: "success",
		Data:    instances,
	})
}
