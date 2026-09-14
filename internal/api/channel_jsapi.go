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
