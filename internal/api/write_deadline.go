// 同步 AI 入口的连接写截止延长（D4 修复，2026-09-16，AUDIT_DEFECT_VERIFY）
//
// 背景：http.Server WriteTimeout=60s（main.go:678），而 /chat 与 /chat/test 等入口是
// **全同步**链路：合并窗口最坏 25s + 打字延迟 ~15s + AI 降级链总预算 110s（ai_router.go:154），
// 慢模型窗口下最终 c.JSON 落在写截止之后——客户端拿到断连/5xx，但 AI token 成本已烧、
// usage_ledger 已记账、消息已落库（响应与副作用解耦，用户视角"没回"实则"已花钱"）。
// 实测真实 AI 单轮多在 10~40s 掩盖了该问题，属"低概率高损失"。
//
// 方案：不动全局 WriteTimeout（保留其余端点的慢客户端防护），仅对同步 AI 的端点
// 用 http.ResponseController 把本连接写截止单独延后（gin v1.9.1 responseWriter 已实现
// Unwrap，见 gin response_writer.go:54，ResponseController 可达底层 conn）。
// 240s = 25 合并 + 15 延迟 + 110 AI + 2min 硬顶兜底余量，覆盖最坏设计路径。
package api

import (
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// syncAIWriteDeadline 同步 AI 入口的统一写截止（自请求进入起算）
const syncAIWriteDeadline = 240 * time.Second

// extendWriteDeadlineForAI 延长当前连接的响应写截止。
// 失败不致命（如底层不支持）：仅记日志，行为退化为全局 60s——与修复前一致，不引入新故障。
func extendWriteDeadlineForAI(c *gin.Context) {
	rc := http.NewResponseController(c.Writer)
	if err := rc.SetWriteDeadline(time.Now().Add(syncAIWriteDeadline)); err != nil {
		log.Printf("[WARN][D4] 写截止延长失败 path=%s: %v（退化为全局 WriteTimeout）", c.Request.URL.Path, err)
	}
}
