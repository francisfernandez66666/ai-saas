// 企微侧边栏 JS-SDK 配置 + 客户上下文（W7，2026-09-12）——顾问登录态可访问。
// jsconfig：给企微工作台/侧边栏 H5 注入 wx.config（corp 级签名）；context：拉渠道客户画像/最近会话。
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"ai-scrm/internal/channel"
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
)

// checkChannelTenantOwnership P0-4 复核批(2026-09-15)：FindActiveByCallback 按 corpid
// **全局**查通道（回调端点语义），侧边栏接口若直接信任 ch.TenantID，等于 corpid 成了
// 跨租户读键——任意登录用户拿他司 corpid+external_userid 即可读他司客户画像与聊天原文。
// 鉴态侧边栏接口必须核对 通道归属租户 == 调用者租户。
func checkChannelTenantOwnership(c *gin.Context, ch *model.Channel) bool {
	tid := db.EffectiveTenantIDFromGin(c)
	if tid == 0 || ch.TenantID != tid {
		RespErr(c, http.StatusForbidden, 403, "通道不属于当前租户")
		return false
	}
	return true
}

// ChannelWecomJSConfig GET /channel/wecom/jsconfig?url=&corpid=
// ChannelWecomJSConfig 返回企微侧边栏 JS-SDK 配置。
func ChannelWecomJSConfig(c *gin.Context) {
	target := c.Query("url")
	corpid := c.Query("corpid")
	if target == "" || corpid == "" {
		RespErr(c, http.StatusBadRequest, 400, "url/corpid 必填")
		return
	}
	ch, err := channel.FindActiveByCallback(model.ChannelTypeWecomApp, corpid)
	if err != nil {
		RespErr(c, http.StatusNotFound, 404, "通道不存在或未启用")
		return
	}
	if !checkChannelTenantOwnership(c, ch) {
		return
	}
	cred, err := channel.DecryptCredential(ch)
	if err != nil {
		RespErr(c, http.StatusBadRequest, 400, "凭据需重录")
		return
	}
	res, err := channel.BuildJSConfig(c.Request.Context(), cred, target)
	if err != nil {
		RespErr(c, http.StatusBadGateway, 502, "生成 JS 配置失败："+err.Error())
		return
	}
	RespOK(c, "ok", res)
}

// ChannelWecomContext GET /channel/wecom/context?corpid=&external_userid=
// 返回该渠道客户画像 + 最近会话（供侧边栏展示；仅本租户，数据范围沿用通道归属）。
func ChannelWecomContext(c *gin.Context) {
	corpid := c.Query("corpid")
	ext := c.Query("external_userid")
	if corpid == "" || ext == "" {
		RespErr(c, http.StatusBadRequest, 400, "corpid/external_userid 必填")
		return
	}
	ch, err := channel.FindActiveByCallback(model.ChannelTypeWecomApp, corpid)
	if err != nil {
		RespErr(c, http.StatusNotFound, 404, "通道不存在或未启用")
		return
	}
	// P0-4 复核批(2026-09-15)：原实现按 ch.TenantID（通道归属方）过滤客户/消息——
	// 调用者租户与通道归属不一致时即跨租户读他司客户画像+最近20条聊天原文。
	if !checkChannelTenantOwnership(c, ch) {
		return
	}
	var ident model.ChannelIdentity
	if err := db.DB.Where("channel_id = ? AND external_id = ?", ch.ID, ext).First(&ident).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "该渠道客户尚未建立会话")
		return
	}
	var cust model.Customer
	if err := db.DB.Where("id = ? AND tenant_id = ?", ident.CustomerID, ch.TenantID).First(&cust).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "客户不存在")
		return
	}
	var msgs []model.Message
	db.DB.Where("customer_id = ?", cust.ID).Order("id DESC").Limit(20).Find(&msgs)
	RespOK(c, "ok", gin.H{
		"customer_id":     cust.ID,
		"name":            cust.Name,
		"journey_stage":   cust.JourneyStage,
		"interest_model":  cust.InterestProduct,
		"tags":            cust.Tags,
		"staff_id":        ident.StaffID,
		"recent_messages": msgs,
	})
}
