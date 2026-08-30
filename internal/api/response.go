// 统一API响应助手：RespOK/RespErr 封装成功与错误响应结构，全接口共用。
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// RespOK 统一成功响应：200 + {"code":0,"message":msg,"data":data}
func RespOK(c *gin.Context, msg string, data interface{}) {
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": msg, "data": data})
}

// RespErr 统一错误响应：httpStatus + {"code":code,"message":msg}
func RespErr(c *gin.Context, httpStatus, code int, msg string) {
	c.JSON(httpStatus, gin.H{"code": code, "message": msg})
}
