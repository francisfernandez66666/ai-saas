// 统一API响应助手：RespOK/RespErr 封装成功与错误响应结构，全接口共用。
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

/*
RespOK 统一成功响应

格式：HTTP 200 + {"code":0,"message":msg,"data":data}

参数：
  - c: Gin 上下文
  - msg: 成功提示信息
  - data: 响应数据（任意类型）

设计说明：code=0 是前端判断成功的唯一标识，语义不可变更。
*/
func RespOK(c *gin.Context, msg string, data interface{}) {
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": msg, "data": data})
}

/*
RespErr 统一错误响应

格式：HTTP {httpStatus} + {"code":code,"message":msg}

参数：
  - c: Gin 上下文
  - httpStatus: HTTP 状态码（如 400/401/500）
  - code: 业务错误码（便于前端精细化处理）
  - msg: 错误描述信息

设计说明：HTTP 状态码与业务码解耦，支持前端按 code 分类 toast。
*/
func RespErr(c *gin.Context, httpStatus, code int, msg string) {
	c.JSON(httpStatus, gin.H{"code": code, "message": msg})
}
