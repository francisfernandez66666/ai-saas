// 获客活码公开链路（获客批 · 批次2，2026-09-23）：落地页读码、扫码计数、访客建档归因。
//
// 本文件三个端点全部**免登录、且不走 Host 租户解析**（middleware.skipTenantPrefixes 已放行
// `/api/v1/acquisition/`）。原因：活码是印在物料上的固定链接，客户扫码时落在哪个域名下
// 由物料决定，不能要求来路 Host 恰好能解析出租户——那会让"没有独立域名的租户发不了活码"。
// 租户身份反过来从**码自己**身上拿（acquisition.Resolve 全局单键查询），
// 因此"谁的码"这件事只有一个事实源，不存在按 Host 猜错家的可能。
//
// 与之配套的一条红线在 internal/acquisition.ApplyToGuest：建档请求解析出的租户
// 与码所属租户不一致时**只记不上归因、不换租户**（public 侧绝不借一个码把自己写进别人家）。
package api

import (
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"ai-scrm/internal/acquisition"
	"ai-scrm/internal/db"
)

// acquisitionCodeParam 取路径上的码并做形态规范化。
// 规范化必须在任何 DB 查询之前完成：非法形态连表都不该碰（省一次查询是次要的，
// 主要是让"探测"在成本上等同于打一个静态资源）。
func acquisitionCodeParam(c *gin.Context) string {
	return acquisition.NormalizeCode(c.Param("code"))
}

// ResolveAcquisitionCode GET /api/v1/acquisition/:code
// apidump:ts AcqResolveResp
// 落地页自检码是否还活着：启用中回 200 + 渠道名，停用/不存在/形态非法一律同一个 404。
// 三种失败不回显差别——它是公开面上唯一"猜码"的地方，回显"存在但已停用"就等于告诉试探者
// 这个码真存在，剩下的只是时间问题。
func ResolveAcquisitionCode(c *gin.Context) {
	code := acquisitionCodeParam(c)
	if code == "" {
		RespErr(c, http.StatusNotFound, 404, "活动已结束")
		return
	}
	acq, err := acquisition.Resolve(db.DB, code)
	if err != nil {
		RespErr(c, http.StatusNotFound, 404, "活动已结束")
		return
	}
	// 只回前端渲染"欢迎来到「XX」"需要的最小集合：不回 tenant_id、不回 owner、不回扫码数。
	RespOK(c, "ok", gin.H{
		"code":         acq.Code,
		"channel":      acq.Channel,
		"landing_path": acquisition.LandingPath,
	})
}

// RecordAcquisitionScan POST /api/v1/acquisition/:code/scan
// apidump:ts AcqScanResp
// 记一次"打开链接"事件（还没有客户身份，所以只进扫码表，不建客户）。
//
// 为什么值得单独一个端点：建档要过 Turnstile、可能失败，也可能客户扫完就走、一句话没说。
// 少了这一发，"扫码数"就只能等于"建档数"，渠道质量里最有信息量的那一格
// ——「投出去 100 次点击、只进来 3 个客户」——就永远看不见。
//
// counted=false 不是错误：同一访客在去重窗口内重复打开是常态，回 200 让前端安静继续。
func RecordAcquisitionScan(c *gin.Context) {
	code := acquisitionCodeParam(c)
	if code == "" {
		RespErr(c, http.StatusNotFound, 404, "活动已结束")
		return
	}
	acq, err := acquisition.Resolve(db.DB, code)
	if err != nil {
		RespErr(c, http.StatusNotFound, 404, "活动已结束")
		return
	}
	// 访客键只作去重依据，绝不回显、也不落进任何业务表（VisitorKey 在模型上是 json:"-"）
	var req struct {
		VisitorKey string `json:"visitor_key"`
	}
	_ = c.ShouldBindJSON(&req) // 允许空体：链接被爬虫/预览机器人打开时没有键，也要计数
	vk := strings.TrimSpace(req.VisitorKey)
	counted, err := acquisition.RecordScan(db.DB, acquisition.ScanInput{
		TenantID: acq.TenantID, CodeID: acq.ID, Code: acq.Code, VisitorKey: vk,
	})
	if err != nil {
		// 计数失败不打断落地页（前端不重试、不提示），但要在日志留痕：
		// 这一格数字以后是用来分渠道预算的，静默漏账比少记一条更糟
		log.Printf("[获客活码] 扫码计数失败 code=%s err=%v", acq.Code, err)
		counted = false
	}
	RespOK(c, "ok", gin.H{"counted": counted})
}

// acquisitionForGuest 给刚建好的匿名访客打活码，返回 nil 表示"这次请求没带码"（响应里不出现该键）。
//
// 归因结果必须回给前端而不是只写库：建客是扫码后唯一一次"确定性动作"，
// 归因没写上（码是别人的 / 已被别的码首触占过）在这一步是可见的，
// 否则一个渠道全部漏记要到后台看数字才发现，那时物料早发完了。
//
// Applied 为真时**调用方要把内存中的 customer 一起改掉**（Source/AcquisitionCode），
// 否则紧跟其后的 guest_created 事件会带着建档时的旧来源发出去，CDP 渠道标签永久错位。
func acquisitionForGuest(c *gin.Context, tenantID, customerID uint, rawCode, visitorKey string) *acquisition.AttributionResult {
	code := acquisition.NormalizeCode(rawCode)
	if code == "" {
		return nil
	}
	res, err := acquisition.ApplyToGuest(db.DB, tenantID, customerID, code, visitorKey)
	if err != nil {
		// 异常绝不上抛：客户进不来比归因没记上严重得多（同 internal/acquisition 那条铁律）。
		// 但要回一个可见的 reason，前端与冒烟才知道"这次是存储故障"而不是"码无效"。
		log.Printf("[获客活码] 归因写入异常 code=%s customer=%d err=%v", code, customerID, err)
		return &acquisition.AttributionResult{Reason: acquisition.ApplyReasonStorageError}
	}
	return &res
}
