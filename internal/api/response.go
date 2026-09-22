// 统一API响应助手：RespOK/RespErr 封装成功与错误响应结构，全接口共用。
package api

import (
	"errors"
	"net/http"
	"reflect"
	"strings"

	"ai-scrm/internal/logx"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
)

// 包初始化即接管校验器的字段命名，使全仓参数错误文案统一用 JSON 契约名。
func init() { registerJSONTagName() }

/*
RespOK 统一成功响应

格式：HTTP 200 + {"code":0,"message":msg,"data":data}

参数：
  - c: Gin 上下文
  - msg: 成功提示信息
  - data: 响应数据（任意类型）

设计说明：code=0 是前端判断成功的唯一标识，语义不可变更。
*/
// RespOK 返回统一成功响应。
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
// RespErr 返回统一错误响应。
func RespErr(c *gin.Context, httpStatus, code int, msg string) {
	// P2-3 错误码全量迁移：存量 handler 的魔数（400/404/409/429/500…）在此统一推导 error_code，
	// 使前端 toastError 能按语义码精细化提示，无需逐个 handler 迁移。
	errorCode := codeNameFromCode(code)
	resp := gin.H{"code": code, "message": msg}
	if errorCode != "" {
		resp["error_code"] = errorCode
	}
	c.JSON(httpStatus, resp)
}

/*
RespErrInternal 5xx 统一脱敏出口

格式：HTTP 500 + {"code":500,"message":safeMsg,"error_code":"internal_error"}

参数：
  - c: Gin 上下文
  - err: 真实错误（只落日志，绝不回传）
  - safeMsg: 对外安全文案（如"创建失败"，不含任何内部结构信息）

设计说明（2026-09-22 复核 P1-1）：
数据库原生错误含 SQLSTATE、表名、列名、列宽等内部结构，直接拼进响应体等于
对外开放一套探测面——实测超长入参会回显
"value too long for type character varying(50) (SQLSTATE 22001)"，
调用方据此可枚举表结构。故约定：handler 内所有 5xx 一律走本函数，
禁止把 err.Error() 拼接进响应体；真实错误经 logx 脱敏并带 trace 落日志，供排障。
*/
// RespErrInternal 返回 5xx 统一响应，真实错误只落日志不回传（防内部结构泄漏）。
func RespErrInternal(c *gin.Context, err error, safeMsg string) {
	if err != nil {
		logx.WithTrace(c.Request.Context()).Error("internal_error",
			"detail", logx.Safe(err.Error(), 500), "path", c.FullPath())
	}
	RespErr(c, http.StatusInternalServerError, 500, safeMsg)
}

// registerJSONTagName 让校验错误里的字段名为 JSON 契约名（snake_case），而非 Go 字段名。
//
// 不做这步时超长入参的提示是"字段 Name 超出长度上限 50"，而调用方在报文里写的键是 name——
// 用 Go 内部命名回敬调用方既难读，也等于承认了服务端的内部命名。
// 该函数只在包初始化时执行一次，影响的是 gin 默认校验器实例。
func registerJSONTagName() {
	if v, ok := binding.Validator.Engine().(*validator.Validate); ok {
		v.RegisterTagNameFunc(func(fld reflect.StructField) string {
			name := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
			if name == "" || name == "-" {
				name = strings.SplitN(fld.Tag.Get("form"), ",", 2)[0]
			}
			if name == "" || name == "-" {
				name = fld.Name
			}
			return name
		})
	}
}

/*
SanitizeBindErr 把参数绑定错误转成对外安全且可读的短语

背景（2026-09-22 复核 P1-1）：go-playground/validator 的原始错误形如
"Key: 'CreateCustomerRequest.Name' Error:Field validation for 'Name' failed on the 'max' tag"，
直接回传等于把 Go 结构体名、字段名、标签名一整套内部命名外泄，且对终端用户是英文噪音。

设计：只回传"哪个字段、为什么"这一层信息（字段名本身在 JSON 契约里已公开，不构成泄漏），
未知类型的错误一律归为"请求体格式不正确"，详情留在服务端日志。
*/
// SanitizeBindErr 将参数绑定错误转换为对外安全且可读的中文短语。
func SanitizeBindErr(err error) string {
	if err == nil {
		return ""
	}
	var ve validator.ValidationErrors
	if errors.As(err, &ve) && len(ve) > 0 {
		e := ve[0]
		field := e.Field()
		switch e.Tag() {
		case "required":
			return "缺少必填字段 " + field
		case "max":
			return "字段 " + field + " 超出长度上限 " + e.Param()
		case "min":
			return "字段 " + field + " 低于长度下限 " + e.Param()
		case "oneof":
			return "字段 " + field + " 取值不在允许范围内"
		case "email":
			return "字段 " + field + " 不是合法邮箱"
		case "gte", "gt", "lte", "lt":
			return "字段 " + field + " 超出取值范围 " + e.Param()
		case "numeric", "number":
			return "字段 " + field + " 必须是数字"
		default:
			return "字段 " + field + " 取值不合法"
		}
	}
	return "请求体格式不正确"
}

// RespErrBind 参数绑定失败统一出口：对外只给安全短语，原始错误落日志便于排障。
func RespErrBind(c *gin.Context, err error) {
	if err != nil {
		logx.WithTrace(c.Request.Context()).Warn("bind_error",
			"detail", logx.Safe(err.Error(), 300), "path", c.FullPath())
	}
	RespErr(c, http.StatusBadRequest, 400, "参数错误: "+SanitizeBindErr(err))
}
