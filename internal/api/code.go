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
	"net/http"
	"strconv"

	"ai-scrm/internal/schema"
	"github.com/gin-gonic/gin"
)

// RespCode 业务响应码类型（用于类型安全的错误码定义）
type RespCode int

// 业务错误码常量定义
const (
	CodeOK           RespCode = 0     // 成功
	CodeParamErr     RespCode = 40001 // 参数校验失败
	CodeUnauthorized RespCode = 40101 // 未认证/ token 无效
	CodeForbidden    RespCode = 40301 // 无权限/身份校验失败
	CodeNotFound     RespCode = 40401 // 资源不存在
	CodeRateLimited  RespCode = 42901 // 触发限流/防薅
	CodeBizErr       RespCode = 42001 // 业务规则拒绝（如余额不足/线索已存在）
	CodeInternal     RespCode = 50001 // 服务内部错误
)

// codeName 机器可读错误码映射（与 schema.Response.Error_code 对应，前端按此 toast）
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

// codeNameFromCode 由业务码（含存量魔数）推导机器可读错误码
// P2-3 错误码全量迁移（2026-08-30）：
//   - 语义码（40001/40101/…/50001）→ 直接映射
//   - 存量魔数（400/401/403/404/409/429/500/502）→ 按 HTTP 语义归类
//
// 返回空串表示不输出 error_code 字段（如 code=0 成功）。
func codeNameFromCode(code int) string {
	if code == 0 {
		return ""
	}
	// 先按语义码精确映射（兜底双码并存：语义码与魔数同义）
	switch RespCode(code) {
	case CodeParamErr:
		return codeName[CodeParamErr]
	case CodeUnauthorized:
		return codeName[CodeUnauthorized]
	case CodeForbidden:
		return codeName[CodeForbidden]
	case CodeNotFound:
		return codeName[CodeNotFound]
	case CodeRateLimited:
		return codeName[CodeRateLimited]
	case CodeBizErr:
		return codeName[CodeBizErr]
	case CodeInternal:
		return codeName[CodeInternal]
	}
	// 存量魔数归类（HTTP 语义与业务码解耦、按场景归类）
	switch code {
	case http.StatusBadRequest: // 400
		return codeName[CodeParamErr]
	case http.StatusUnauthorized: // 401
		return codeName[CodeUnauthorized]
	case http.StatusForbidden: // 403
		return codeName[CodeForbidden]
	case http.StatusNotFound: // 404
		return codeName[CodeNotFound]
	case http.StatusConflict: // 409 业务规则冲突（如幂等拦截）
		return codeName[CodeBizErr]
	case http.StatusTooManyRequests: // 429
		return codeName[CodeRateLimited]
	case http.StatusBadGateway: // 502 第三方通信失败→内部错误
		return codeName[CodeInternal]
	}
	if code >= 500 {
		return codeName[CodeInternal]
	}
	return codeName[CodeBizErr]
}

/*
RespFail 业务错误响应

用途：返回带业务错误码的错误响应，支持前端精细化 toast 提示。

参数：
  - c: Gin 上下文
  - httpStatus: HTTP 状态码（401/403/429/400/500）
  - code: 业务错误码（RespCode 类型）
  - msg: 错误描述信息

设计说明：
  - HTTP 状态码随错误性质返回，业务 code 与之解耦
  - 前端通过 error_code 字段进行精细化提示
*/
func RespFail(c *gin.Context, httpStatus int, code RespCode, msg string) {
	c.JSON(httpStatus, schema.Response{
		Code:       int(code),
		Error_code: codeName[code],
		Message:    msg,
	})
}

/*
respOK 成功响应（内部使用）

用途：返回成功响应，HTTP 200 + code=0。

设计说明：复用 RespErr 函数，保持响应结构一致性。
*/
func respOK(c *gin.Context, data any) {
	RespErr(c, 200, int(CodeOK), "ok")
}

/*
respFail 业务错误响应（HTTP 200）

用途：返回业务错误响应，HTTP 200 + 业务 code。

设计说明：保持与前端既有约定一致，前端通过 code 判断业务错误。
*/
func respFail(c *gin.Context, code RespCode, msg string) {
	RespErr(c, 200, int(code), msg)
}

/*
respFailStatus 业务错误响应（携带准确 HTTP 状态码）

用途：返回带准确 HTTP 状态码的业务错误响应，用于新增鉴权/限流端点。

参数：
  - c: Gin 上下文
  - httpStatus: HTTP 状态码
  - code: 业务错误码
  - msg: 错误描述信息
*/
func respFailStatus(c *gin.Context, httpStatus int, code RespCode, msg string) {
	RespErr(c, httpStatus, int(code), msg)
}

// PathUintID 解析路径参数 :id 为正整数（健壮性收口 2026-09-05）。
// 修改原因：此前各处直接把 c.Param("id") 字符串拼进 SQL（如 super.go 空 ID 打到 PG
// 报 22P02 invalid input syntax for bigint），既产生无效 SQL 噪音又使错误口径 404/500 漂移。
// 现统一在入口校验：非法（空/非数字/0）直接 400，不触 DB。
// 返回 ok=false 时已写好响应，调用方直接 return 即可。
func PathUintID(c *gin.Context) (uint, bool) {
	n, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || n == 0 {
		RespErr(c, http.StatusBadRequest, int(CodeParamErr), "ID 非法")
		return 0, false
	}
	return uint(n), true
}
