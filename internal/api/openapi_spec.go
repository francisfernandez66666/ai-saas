// E10 租户 OpenAPI 文档站（2026-09-19）：规格构建 + 公开只读端点。
//
// 设计红线：本规格**只覆盖租户开放面 /openapi/v1/* 五端点**（sk_ Key 鉴权），
// 绝不渲染 /admin、/super 等内部面——公开文档站即攻击面清单，泄露内部路由等于送地图。
// 规格由代码内联构建（非手写 JSON 文件），cmd/apidump -format openapi 导出 golden
// 防漂移：路由清单与 spec paths 不一致即契约 FAIL（新增 /openapi 端点忘了补文档当场红）。
// 端点 GET /api/v1/openapi/spec 公开挂载（注册在 JWTAuth 之前），前端 /docs/api 页自渲染。
package api

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
)

// OpenAPIGetSpec GET /api/v1/openapi/spec —— 公开返回 OpenAPI 3.0 规格（仅租户开放面子集）。
// 原样 JSON 输出（不套 RespOK 信封）：保持 OpenAPI 生态通用性，导入 Postman/Swagger 工具链可直接消费。
func OpenAPIGetSpec(c *gin.Context) {
	spec, err := BuildOpenAPISpecJSON()
	if err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "规格生成失败")
		return
	}
	c.Header("Cache-Control", "public, max-age=60")
	c.Data(http.StatusOK, "application/json; charset=utf-8", spec)
}

// BuildOpenAPISpecJSON 序列化规格（两空格缩进、不转义 HTML、尾随换行）——
// 端点响应与 cmd/apidump golden 共用同一函数，保证字节级一致。
func BuildOpenAPISpecJSON() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(BuildOpenAPISpec()); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// openapiEnvelopeRef 统一响应信封说明（读端点共用；chat/completions 走 OpenAI 原生结构）
func openapiEnvelopeRef(desc string) map[string]any {
	return map[string]any{
		"type":        "object",
		"description": desc,
		"properties": map[string]any{
			"code":    map[string]any{"type": "integer", "description": "0=成功，非0=业务错误"},
			"message": map[string]any{"type": "string"},
			"data":    map[string]any{"description": "业务数据，形态见各端点"},
		},
	}
}

// intParam 数值查询参数快捷构造
func intParam(name, desc string, required bool, def string) map[string]any {
	p := map[string]any{
		"name":        name,
		"in":          "query",
		"required":    required,
		"description": desc,
		"schema":      map[string]any{"type": "integer"},
	}
	if def != "" {
		p["schema"].(map[string]any)["default"] = def
	}
	return p
}

// strParam 字符串查询参数快捷构造
func strParam(name, desc string, required bool) map[string]any {
	return map[string]any{
		"name":        name,
		"in":          "query",
		"required":    required,
		"description": desc,
		"schema":      map[string]any{"type": "string"},
	}
}

// BuildOpenAPISpec 构建租户开放面 OpenAPI 3.0 规格（map 形态，供 golden 与端点共用）。
// paths 必须与 routes_openapi.go 注册清单一一对应——cmd/apidump -format openapi 会双向核对。
func BuildOpenAPISpec() map[string]any {
	return map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":   "AI-SCRM 开放 API（租户面）",
			"version": "v1",
			"description": "AI-SCRM 对外数据与对话能力。鉴权：`Authorization: Bearer sk_...`（后台「API Key」签发，最小权限，按 Key 限流 60 次/分钟）。\n\n" +
				"读端点统一返回 `{code, message, data}` 信封（code=0 成功）；`/chat/completions` 为 OpenAI chat/completions 兼容结构。\n\n" +
				"错误码：400 参数非法 / 401 Key 无效或吊销 / 403 权限不足或租户不可用 / 404 资源不存在或跨租户 / 429 触发限流。",
		},
		"servers":  []any{map[string]any{"url": "/openapi/v1", "description": "当前实例"}},
		"security": []any{map[string]any{"BearerKey": []any{}}},
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"BearerKey": map[string]any{
					"type":         "http",
					"scheme":       "bearer",
					"bearerFormat": "sk_",
					"description":  "API Key（后台「系统管理 → API Key」签发，明文仅返回一次）。权限位：customer.read / cdp.read / chat.write / all",
				},
			},
		},
		"paths": map[string]any{
			"/customers": map[string]any{
				"get": map[string]any{
					"summary":     "客户列表",
					"description": "权限 customer.read。手机号默认脱敏（mask=false 显式关闭），跨租户数据天然不可见。附 balance 余额对账视图。",
					"parameters": []any{
						intParam("page", "页码，默认 1", false, "1"),
						intParam("page_size", "每页条数 1~100，默认 20", false, "20"),
						strParam("keyword", "姓名/手机号模糊搜索", false),
						strParam("journey_stage", "旅程阶段过滤（如 ai_connected/lead_captured）", false),
						strParam("mask", "手机号脱敏，默认 true；传 false 出明文", false),
					},
					"responses": map[string]any{
						"200": map[string]any{
							"description": "total/page/page_size/list[]（id,name,phone,journey_stage,interest_model,source,assigned_user_id,created_at）+ balance（quota_remaining/ai_call_balance/billing_enforced）",
							"content":     map[string]any{"application/json": map[string]any{"schema": openapiEnvelopeRef("统一信封")}},
						},
					},
				},
			},
			"/customers/{id}/conversations": map[string]any{
				"get": map[string]any{
					"summary":     "客户会话与消息",
					"description": "权限 customer.read。返回该客户最近 20 条会话、每条最多 200 条消息（sender_type/content/created_at）。他租户客户 ID 一律 404。",
					"parameters": []any{
						map[string]any{"name": "id", "in": "path", "required": true, "description": "客户 ID", "schema": map[string]any{"type": "integer"}},
					},
					"responses": map[string]any{
						"200": map[string]any{
							"description": "customer_id/name/journey_stage/conversations[]（conversation_id,status,message_count,messages[]）+ balance",
							"content":     map[string]any{"application/json": map[string]any{"schema": openapiEnvelopeRef("统一信封")}},
						},
						"404": map[string]any{"description": "客户不存在或跨租户"},
					},
				},
			},
			"/cdp/profiles/{one_id}": map[string]any{
				"get": map[string]any{
					"summary":     "OneID 客户画像",
					"description": "权限 cdp.read。返回 CDP 画像视图（姓名/标签已脱敏）；不存在与他租户统一 404 防枚举。",
					"parameters": []any{
						map[string]any{"name": "one_id", "in": "path", "required": true, "description": "OneID 标识", "schema": map[string]any{"type": "string"}},
					},
					"responses": map[string]any{
						"200": map[string]any{"description": "画像视图", "content": map[string]any{"application/json": map[string]any{"schema": openapiEnvelopeRef("统一信封")}}},
						"404": map[string]any{"description": "画像不存在（含跨租户）"},
					},
				},
			},
			"/usage": map[string]any{
				"get": map[string]any{
					"summary":     "用量对账",
					"description": "权限 all。按日返回 usage_records 汇总（date/metric/value），用于客户侧自助对账。",
					"parameters": []any{
						intParam("days", "回看天数 1~365，默认 30", false, "30"),
					},
					"responses": map[string]any{
						"200": map[string]any{"description": "data 为 [{date,metric,value}] 数组", "content": map[string]any{"application/json": map[string]any{"schema": openapiEnvelopeRef("统一信封")}}},
					},
				},
			},
			"/chat/completions": map[string]any{
				"post": map[string]any{
					"summary":     "AI 销售对话（OpenAI 兼容）",
					"description": "权限 chat.write。复用站内全链路（策略推理/硬边界/留资捕获/内容安全/计费同池）。会话归属由 external_user_id(+session_id) 服务端映射；stream=true 走 SSE 逐帧，末尾 [DONE]。到店倾向+手机号会触发留资并分配顾问。",
					"requestBody": map[string]any{
						"required": true,
						"content": map[string]any{"application/json": map[string]any{
							"schema": map[string]any{
								"type":     "object",
								"required": []string{"messages", "external_user_id"},
								"properties": map[string]any{
									"model":            map[string]any{"type": "string", "description": "模型名（透传回显，默认 ai-scrm）"},
									"messages":         map[string]any{"type": "array", "description": "OpenAI 消息数组，取最后一条 role=user 作为本轮输入", "items": map[string]any{"type": "object", "properties": map[string]any{"role": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}}},
									"external_user_id": map[string]any{"type": "string", "description": "渠道侧用户 ID（必填，租户内映射为客户）"},
									"session_id":       map[string]any{"type": "string", "description": "渠道会话 ID（可选，隔离同一用户多轮会话）"},
									"channel":          map[string]any{"type": "string", "description": "渠道标识 douyin/tiktok/taobao...，默认 openapi"},
									"stream":           map[string]any{"type": "boolean", "description": "true=SSE 流式，false=全量 JSON"},
								},
							},
							"example": map[string]any{
								"model":            "ai-scrm",
								"messages":         []any{map[string]any{"role": "user", "content": "你们越野版落地价多少？"}},
								"external_user_id": "douyin_8823",
								"session_id":       "s-2026-001",
								"channel":          "douyin",
							},
						}},
					},
					"responses": map[string]any{
						"200": map[string]any{
							"description": "OpenAI chat.completion（choices[0].message.content + usage 估算 token 数）；stream=true 为 chat.completion.chunk SSE 帧序列",
							"content": map[string]any{
								"application/json": map[string]any{"example": map[string]any{
									"id": "chatcmpl-20260919120000.000000000", "object": "chat.completion", "model": "ai-scrm",
									"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "越野版现在有置换补贴，方便留个电话帮您锁优惠吗？"}, "finish_reason": "stop"}},
									"usage":   map[string]any{"prompt_tokens": 12, "completion_tokens": 24, "total_tokens": 36},
								}},
								"text/event-stream": map[string]any{"description": "SSE 逐帧，末帧 data: [DONE]"},
							},
						},
						"400": map[string]any{"description": "external_user_id 缺失或 messages 无用户内容"},
						"429": map[string]any{"description": "Key 维度限流 60 次/分钟"},
					},
				},
			},
		},
	}
}
