// 连接写截止延长（D4 修复，2026-09-16；2026-09-24 泛化收口残项 2「WriteTimeout 链路预算」）
//
// 背景：http.Server WriteTimeout=60s（main.go 的 http.Server 装配），而有些端点的响应
// 天生就比 60s 长：
//   - /chat 与 /chat/unauthorized 等入口是**全同步**链路：合并窗口最坏 25s + 打字延迟
//     ~15s + AI 降级链总预算 110s（ai_router.go），慢模型窗口下最终 c.JSON 落在写截止之后
//     ——客户端拿到断连/5xx，但 AI token 成本已烧、usage_ledger 已记账、消息已落库
//     （响应与副作用解耦，用户视角"没回"实则"已花钱"）。实测真实 AI 单轮多在 10~40s
//     掩盖了该问题，属"低概率高损失"。
//   - /admin/export/*.csv 是**逐行 Flush 的流式响应**：硬顶 5 万行、每行一次 Flush，
//     慢客户端（移动网络/代理回传）下写满 10MB 级正文可以远超 60s。这里断连的后果不是
//     报错而是**静默截断**——状态码 200、文件已开写，用户拿到一个"看起来正常"的半截表。
//
// 方案：不动全局 WriteTimeout（保留其余端点的慢客户端防护），仅对上述端点用
// http.ResponseController 把本连接写截止单独延后（gin v1.9.1 responseWriter 已实现
// Unwrap，见 gin response_writer.go:54，ResponseController 可达底层 conn）。
// 240s = 25 合并 + 15 延迟 + 110 AI + 2min 硬顶兜底余量，覆盖最坏设计路径。
// 180s = 5 万行 × ~200B ≈ 10MB，按 1Mbps 上行（~125KB/s）约 80s，留一倍余量。
package api

import (
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// syncAIWriteDeadline 同步 AI 入口的统一写截止（自请求进入起算）
const syncAIWriteDeadline = 240 * time.Second

// csvExportWriteDeadline 流式 CSV 导出的写截止：5 万行逐行 Flush，慢客户端下 10MB 级
// 正文可以在 60s 内写不完；断连不报错、只把文件截断成半截，所以宁可对这一个连接放宽。
const csvExportWriteDeadline = 180 * time.Second

// extendWriteDeadline 把当前连接的响应写截止延后 d（自此刻起算）。
// 失败不致命（如底层不支持）：仅记日志，行为退化为全局 WriteTimeout——与修复前一致，不引入新故障。
// why 只进日志，用于日后从日志判断"这条连接是被哪个端点延长的"。
func extendWriteDeadline(c *gin.Context, d time.Duration, why string) {
	rc := http.NewResponseController(c.Writer)
	if err := rc.SetWriteDeadline(time.Now().Add(d)); err != nil {
		log.Printf("[WARN][D4] 写截止延长失败 path=%s reason=%s: %v（退化为全局 WriteTimeout）", c.Request.URL.Path, why, err)
	}
}

// extendWriteDeadlineForAI 延长当前连接的响应写截止（同步 AI 链路）。
func extendWriteDeadlineForAI(c *gin.Context) {
	extendWriteDeadline(c, syncAIWriteDeadline, "sync-ai")
}

// extendWriteDeadlineForExport 延长当前连接的响应写截止（CSV 流式导出）。
// 唯一调用点是 export.go 的 csvWriter——它必须在写第一行（含响应头）之前被调到，
// 头一旦发出就改不了，截断也只可能发生在那之后。
func extendWriteDeadlineForExport(c *gin.Context) {
	extendWriteDeadline(c, csvExportWriteDeadline, "csv-export")
}
