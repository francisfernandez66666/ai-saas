// 获客活码管理端（获客批 · 批次3，2026-09-23）：码 CRUD、二维码出图、漏斗数字与客户名单下钻。
//
// 只走 admin 闸（AdminRequired 挂在路由组上），读写一律 db.RQ(c) + 行内 tenant_id 双闸：
// 活码是租户私有资产，超管不带 X-Tenant-ID 也不该在别人家建码（CreateCode 里 tenant_required 兜底）。
//
// 三条口径上的选择写在这里，因为它们都会影响"运营看得见什么"：
//  1. **没有删除，只有停用**（见 model/acquisition.go）。停用后对外即不存在、历史统计照读，
//     短码永不回收——回收一个旧码等于把新物料的流量并进旧活动的账本。
//  2. **漏斗数字与下钻名单同源**：两侧都走 acquisition.DrillCustomers/ListWithStats 的同一份
//     归属查询，所以"卡片 3 个、点进去 4 行"在这批是结构上不给机会（D4 立下的同一约束）。
//  3. **扫码次数不可下钻**：它的单位是"次"不是"人"，硬点开就会出现自相矛盾的名单，
//     非客户级指标一律 400（acquisition.ErrBadMetric）。
package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	qrcode "github.com/skip2/go-qrcode"

	"ai-scrm/internal/acquisition"
	"ai-scrm/internal/db"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
)

// acqQueryInt 读整型 query，非法/缺省回默认值（负数同样回默认——窗口/分页没有负数语义）。
func acqQueryInt(c *gin.Context, key string, def int) int {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// acqQueryPageSize 取分页尺寸并钳到 [1,100]。
// 与 D4 同一条口径：下钻是"核对这一格是谁"，不是导数通道（导数走 /admin/export）。
// 本包的命中集在内存里切页，不设上限就等于让一个 query 参数决定响应体大小。
func acqQueryPageSize(c *gin.Context, key string, def int) int {
	n := acqQueryInt(c, key, def)
	if n > acquisition.MaxDrillPageSize {
		return acquisition.MaxDrillPageSize
	}
	return n
}

// acqCustomDomain 取当前租户的白标域名（TenantResolver 已注入，零额外查询）。
// 拿不到（单测桩只设 tenant_id）时返回空串，链接基址自然回落请求 Host——
// 这是"降级但可用"，不是错误，所以不拒请求。
func acqCustomDomain(c *gin.Context) string {
	return middleware.TenantCustomDomain(c)
}

// acquisitionRequestBase 从请求上下文取链接基址（scheme + Host，兼容反代 X-Forwarded-Proto）。
// 与 buildInviteURL 同一口径，但那是邀请码链路的私有实现，此处不复用以免两件事互相绑住。
func acquisitionRequestBase(c *gin.Context) string {
	scheme := "http"
	if c.GetHeader("X-Forwarded-Proto") == "https" || c.Request.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host
}

// acquisitionCodeRow 一个码的完整出参行：配置 + 链接 + 窗口内数字。
// link 随每行下发而不是让前端自己拼：前端不知道（也不该知道）三级基址优先级，
// 让它拼就等于把"印错海报"的风险挪到了一处看不见实现的地方。
func acquisitionCodeRow(s acquisition.CodeWithStats, tenantID uint, customDomain, requestBase string) gin.H {
	return gin.H{
		"id":            s.Code.ID,
		"code":          s.Code.Code,
		"name":          s.Code.Name,
		"channel":       s.Code.Channel,
		"status":        s.Code.Status,
		"remark":        s.Code.Remark,
		"owner_user_id": s.Code.OwnerUserID,
		"created_at":    s.Code.CreatedAt,
		"link":          acquisition.BuildLandingLink(tenantID, customDomain, requestBase, s.Code.Code),
		"scans":         s.Scans,
		"funnel":        s.Funnel,
	}
}

// acquisitionZeroStats 刚建好的码的初始数字（全 0，但**键必须齐**：
// 前端按格子渲染，缺键会让某一格显示 undefined 而不是 0）。
func acquisitionZeroStats() acquisition.CodeWithStats {
	f := make(map[string]int64, len(acquisition.FunnelMetricCodes))
	for _, m := range acquisition.FunnelMetricCodes {
		f[m] = 0
	}
	return acquisition.CodeWithStats{Funnel: f}
}

// ListAcquisitionCodes GET /admin/acquisition/codes?status=&days=
// apidump:ts AcqCodeListResp
// 活码列表 + 每码窗口内漏斗数字，并下发展示口径（渠道枚举、可下钻指标、基址是否配置）。
func ListAcquisitionCodes(c *gin.Context) {
	tid := db.EffectiveTenantIDFromGin(c)
	days := acquisition.ClampDays(acqQueryInt(c, "days", acquisition.DefaultWindowDays))
	rows, err := acquisition.ListWithStats(db.RQ(c), tid, c.Query("status"), days)
	if err != nil {
		RespErrInternal(c, err, "活码列表读取失败")
		return
	}
	customDomain := acqCustomDomain(c)
	base := acquisitionRequestBase(c)
	list := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		list = append(list, acquisitionCodeRow(r, tid, customDomain, base))
	}
	// 空态必须是 [] 不是 null（前端直接 map）
	if list == nil {
		list = []gin.H{}
	}
	RespOK(c, "ok", gin.H{
		"list":  list,
		"total": len(list),
		"days":  days,
		"config": gin.H{
			"channels": acquisition.ChannelCodes,
			"metrics":  acquisition.FunnelMetricCodes,
			"labels":   acquisition.FunnelMetricLabels,
			// 基址没配齐时链接会是相对路径——这种链接印出去就是废码，必须让页面说清楚
			"link_base_configured": acquisition.ResolveLinkBase(tid, customDomain, base) != "",
		},
	})
}

// CreateAcquisitionCode POST /admin/acquisition/codes {name,channel,remark}
// apidump:ts AcqCodeRow
// 新建一个活码：形态校验在 acquisition.CreateCode 里做，拒绝回 400 + 稳定原因码。
func CreateAcquisitionCode(c *gin.Context) {
	tid := db.EffectiveTenantIDFromGin(c)
	var req struct {
		Name    string `json:"name"`
		Channel string `json:"channel"`
		Remark  string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErrBind(c, err)
		return
	}
	acq, err := acquisition.CreateCode(db.RQ(c), acquisition.CreateInput{
		TenantID: tid, OwnerUser: currentUserID(c), Name: req.Name, Channel: req.Channel, Remark: req.Remark,
	})
	if err != nil {
		if reason := acquisition.RejectReason(err); reason != "" {
			// 与触达批同一形态：msg 给人读，reason 给前端分支与冒烟断言（文案可改，码不可改）
			c.JSON(http.StatusBadRequest, gin.H{
				"code": 400, "message": err.Error(), "error_code": "acquisition_rejected", "reason": reason,
			})
			return
		}
		RespErrInternal(c, err, "活码创建失败")
		return
	}
	customDomain := acqCustomDomain(c)
	zero := acquisitionZeroStats()
	zero.Code = *acq
	RespOK(c, "ok", acquisitionCodeRow(zero, tid, customDomain, acquisitionRequestBase(c)))
}

// SetAcquisitionCodeStatus POST /admin/acquisition/codes/:id/status {active}
// apidump:ts AcqStatusResp
// 启用/停用。停用是"对外不再认这个码"，不是删除：历史数字照读，短码永不回收。
func SetAcquisitionCodeStatus(c *gin.Context) {
	tid := db.EffectiveTenantIDFromGin(c)
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		RespErr(c, http.StatusBadRequest, 400, "ID 非法")
		return
	}
	var req struct {
		Active *bool `json:"active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Active == nil {
		// 缺 active 不猜默认值：这是个会改变对外可见性的动作，含糊的请求应当被拒而不是"顺手启用"
		RespErr(c, http.StatusBadRequest, 400, "缺少参数 active")
		return
	}
	n, serr := acquisition.SetCodeStatus(db.RQ(c), tid, uint(id), *req.Active)
	if serr != nil {
		RespErrInternal(c, serr, "活码状态更新失败")
		return
	}
	if n == 0 {
		// 不存在 / 跨租户两种情况同等回 404，不回显差别（否则可用于探测谁的 ID 存在）
		RespErr(c, http.StatusNotFound, 404, "活码不存在")
		return
	}
	RespOK(c, "ok", gin.H{"id": id, "status": acquisitionStatusOf(*req.Active)})
}

// acquisitionStatusOf 布尔 → 状态字面量（与 model 常量同源，不在此处另写一遍字符串）
func acquisitionStatusOf(active bool) string {
	if active {
		return model.AcquisitionStatusActive
	}
	return model.AcquisitionStatusDisabled
}

// GetAcquisitionCodeQR GET /admin/acquisition/codes/:id/qr.png?size=
// 出二维码 PNG（物料用）。归属校验与列表同闸：跨租户 ID 一律 404，不返回别人的码。
//
// 出图在服务端而不是前端：这张图要被拖进 Illustrator/打印店，产物必须与页面解耦，
// 且链接内容（三级基址拼出来的那条）只有后端算得准。
func GetAcquisitionCodeQR(c *gin.Context) {
	tid := db.EffectiveTenantIDFromGin(c)
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		RespErr(c, http.StatusBadRequest, 400, "ID 非法")
		return
	}
	var acq model.AcquisitionCode
	// 停用中的码照样出图（运营可能只是先停归因、物料还在用），但必须是自己的码
	if err := db.RQ(c).Where("id = ?", uint(id)).First(&acq).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "活码不存在")
		return
	}
	size := 320
	if s := c.Query("size"); s != "" {
		if n := acqQueryInt(c, "size", 320); n >= 100 && n <= 1000 {
			size = n
		}
	}
	customDomain := acqCustomDomain(c)
	link := acquisition.BuildLandingLink(tid, customDomain, acquisitionRequestBase(c), acq.Code)
	png, err := qrcode.Encode(link, qrcode.Medium, size)
	if err != nil {
		RespErrInternal(c, err, "二维码生成失败")
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.Data(http.StatusOK, "image/png", png)
}

// DrillAcquisitionCustomers GET /admin/acquisition/codes/:id/customers?metric=&days=&page=&page_size=
// apidump:ts AcqDrillResp
// 漏斗某一格点开就是这些人：名单 total 恒等于卡片数字（两侧同源，见文件头第 2 条）。
func DrillAcquisitionCustomers(c *gin.Context) {
	tid := db.EffectiveTenantIDFromGin(c)
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		RespErr(c, http.StatusBadRequest, 400, "ID 非法")
		return
	}
	res, derr := acquisition.DrillCustomers(db.RQ(c), tid, uint(id),
		strings.TrimSpace(c.Query("metric")),
		acqQueryInt(c, "days", acquisition.DefaultWindowDays),
		acqQueryInt(c, "page", 1),
		acqQueryPageSize(c, "page_size", 20))
	if derr != nil {
		switch {
		case errors.Is(derr, acquisition.ErrBadMetric):
			RespErr(c, http.StatusBadRequest, 400, derr.Error())
		case errors.Is(derr, acquisition.ErrNotFound):
			RespErr(c, http.StatusNotFound, 404, "活码不存在")
		default:
			RespErrInternal(c, derr, "名单读取失败")
		}
		return
	}
	RespOK(c, "ok", gin.H{
		"metric": res.Metric, "label": res.Label, "total": res.Total,
		"list": res.List, "note": res.Note,
		// page/page_size 回显钳制后的值：前端据此算总页数，回原值会让 1000 这种请求
		// 在页面上算出"共 1 页"而实际每页只有 100 行。
		"page": acqQueryInt(c, "page", 1), "page_size": acqQueryPageSize(c, "page_size", 20),
	})
}
