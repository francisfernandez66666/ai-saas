// 统一业务错误码与响应助手：RespCode/RespFail 等响应封装。
package api

// ============================================================
// 统一业务错误码与响应助手（P2 错误码规范化，2026-08-29）
//
// 背景：此前各 handler 直接用魔法数字（0/400/500/401…），语义不统一。
// 约定：
//   - code == 0 表示成功（前端仅判断 j.code === 0，故语义码改动不影响前端逻辑）
//   - 非 0 为具体业务错误码，便于日志/告警/前端精细化提示
//   - HTTP 状态码随错误性质返回（401/403/429/400/500），业务 code 与之解耦
// 新接口统一走本文件助手；存量 handler 随改动逐步迁移。
// ============================================================

import (
	"ai-scrm/internal/schema"
	"github.com/gin-gonic/gin"
)

// RespCode 业务响应码
type RespCode int

const (
	CodeOK           RespCode = 0
	CodeParamErr     RespCode = 40001 // 参数校验失败
	CodeUnauthorized RespCode = 40101 // 未认证/ token 无效
	CodeForbidden    RespCode = 40301 // 无权限/身份校验失败
	CodeNotFound     RespCode = 40401 // 资源不存在
	CodeRateLimited  RespCode = 42901 // 触发限流/防薅
	CodeBizErr       RespCode = 42001 // 业务规则拒绝（如余额不足/线索已存在）
	CodeInternal     RespCode = 50001 // 服务内部错误
)

// codeName 机器可读错误码（与 schema.Response.Error_code 对应，前端按此 toast）
var codeName = map[RespCode]string{
	CodeOK:           "ok",
	CodeParamErr:     "param_error",
	CodeUnauthorized: "unauthorized",
	CodeForbidden:    "forbidden",
	CodeNotFound:     "not_found",
	CodeRateLimited:  "rate_limited",
	CodeBizErr:       "biz_error",
	CodeInternal:     "internal_error",
}

// RespFail 业务错误响应（P1-4：统一带 error_code，前端据此精细化 toast）
// httpStatus 随错误性质返回（401/403/429/400/500），业务 code 与之解耦。
func RespFail(c *gin.Context, httpStatus int, code RespCode, msg string) {
	c.JSON(httpStatus, schema.Response{
		Code:       int(code),
		Error_code: codeName[code],
		Message:    msg,
	})
}

// respOK 成功响应
func respOK(c *gin.Context, data any) {
	RespErr(c, 200, int(CodeOK), "ok")
}

// respFail 业务错误（默认 200 HTTP，由业务 code 区分；保持与前端既有约定一致）
func respFail(c *gin.Context, code RespCode, msg string) {
	RespErr(c, 200, int(code), msg)
}

// respFailStatus 业务错误（携带准确 HTTP 状态码，用于新增鉴权/限流端点）
func respFailStatus(c *gin.Context, httpStatus int, code RespCode, msg string) {
	RespErr(c, httpStatus, int(code), msg)
}
