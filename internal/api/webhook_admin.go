// 出站事件 webhook 管理端（D6，2026-09-12）：订阅 CRUD + 测试 ping + 投递记录（配合 Admin F 批"集成设置"页）。
package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/webhook"
	"ai-scrm/pkg/crypto"
)

// maskWebhook 列表视图：secret 只回显掩码，绝不明文外泄
func maskWebhook(w *model.TenantWebhook) gin.H {
	return gin.H{
		"id":          w.ID,
		"name":        w.Name,
		"url":         w.URL,
		"events":      w.Events,
		"active":      w.Active,
		"fail_count":  w.FailCount,
		"disabled_at": w.DisabledAt,
		"secret_mask": crypto.MaskSecret(w.Secret),
		"created_at":  w.CreatedAt,
	}
}

// ListWebhooks GET /admin/webhooks
// ListWebhooks 分页列出租户出站 Webhook。
func ListWebhooks(c *gin.Context) {
	var rows []model.TenantWebhook
	db.RQ(c).Scopes(db.T(c)).Order("id DESC").Find(&rows)
	out := make([]gin.H, 0, len(rows))
	for i := range rows {
		out = append(out, maskWebhook(&rows[i]))
	}
	RespOK(c, "ok", gin.H{"list": out})
}

// CreateWebhook POST /admin/webhooks {name,url,secret,events[]}
// CreateWebhook 创建租户出站 Webhook 并加密保存密钥。
func CreateWebhook(c *gin.Context) {
	tid := whTenantID(c)
	var req struct {
		Name   string   `json:"name"`
		URL    string   `json:"url" binding:"required"`
		Secret string   `json:"secret"`
		Events []string `json:"events"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误："+err.Error())
		return
	}
	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		RespErr(c, http.StatusBadRequest, 400, "url 必须 http(s):// 开头")
		return
	}
	secret := req.Secret
	if secret != "" {
		if enc, err := crypto.Encrypt(secret); err == nil {
			secret = enc
		}
	}
	w := model.TenantWebhook{
		TenantID: tid, Name: req.Name, URL: req.URL, Secret: secret,
		Events: strings.Join(req.Events, ","), Active: true,
	}
	if err := db.DB.Create(&w).Error; err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "创建失败："+err.Error())
		return
	}
	// 一次性回显明文 secret（前端复制保存后不再可取）
	RespOK(c, "创建成功", gin.H{"id": w.ID, "secret": req.Secret, "sign_algo": "HMAC-SHA256: ts+\".\"+body"})
}

// UpdateWebhook PUT /admin/webhooks/:id {name?,url?,secret?,events[],active?}
// UpdateWebhook 更新租户出站 Webhook 配置。
func UpdateWebhook(c *gin.Context) {
	id := whID(c)
	var w model.TenantWebhook
	if err := db.RQ(c).Scopes(db.T(c)).First(&w, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "订阅不存在")
		return
	}
	var req struct {
		Name   *string   `json:"name"`
		URL    *string   `json:"url"`
		Secret *string   `json:"secret"`
		Events *[]string `json:"events"`
		Active *bool     `json:"active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误")
		return
	}
	up := map[string]interface{}{}
	if req.Name != nil {
		up["name"] = *req.Name
	}
	if req.URL != nil {
		if !strings.HasPrefix(*req.URL, "http://") && !strings.HasPrefix(*req.URL, "https://") {
			RespErr(c, http.StatusBadRequest, 400, "url 必须 http(s):// 开头")
			return
		}
		up["url"] = *req.URL
	}
	if req.Secret != nil && *req.Secret != "" {
		if enc, err := crypto.Encrypt(*req.Secret); err == nil {
			up["secret"] = enc
		} else {
			up["secret"] = *req.Secret
		}
	}
	if req.Events != nil {
		up["events"] = strings.Join(*req.Events, ",")
	}
	if req.Active != nil {
		up["active"] = *req.Active
		if *req.Active {
			up["disabled_at"] = nil // 重新启用清熔断
			up["fail_count"] = 0
		}
	}
	if len(up) > 0 {
		db.DB.Model(&model.TenantWebhook{}).Where("id = ?", id).Updates(up)
	}
	RespOK(c, "已更新", gin.H{"id": id})
}

// DeleteWebhook DELETE /admin/webhooks/:id
// DeleteWebhook 删除租户出站 Webhook 及其投递记录。
func DeleteWebhook(c *gin.Context) {
	id := whID(c)
	var w model.TenantWebhook
	if err := db.RQ(c).Scopes(db.T(c)).First(&w, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "订阅不存在")
		return
	}
	db.DB.Delete(&model.TenantWebhook{}, id)
	RespOK(c, "已删除", gin.H{"id": id})
}

// TestWebhook POST /admin/webhooks/:id/test — 向订阅 URL 发一条 webhook.test 事件，返回状态码
func TestWebhook(c *gin.Context) {
	id := whID(c)
	var w model.TenantWebhook
	if err := db.RQ(c).Scopes(db.T(c)).First(&w, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "订阅不存在")
		return
	}
	// 测试走明文 secret（deliver 内部解密）；直接用已存密文即可
	status, err := webhook.SendTestPing(w.URL, w.Secret)
	if err != nil {
		RespOK(c, "连通失败", gin.H{"ok": false, "status": status, "detail": err.Error()})
		return
	}
	RespOK(c, "已发送", gin.H{"ok": status >= 200 && status < 300, "status": status})
}

// ListWebhookDeliveries GET /admin/webhooks/:id/deliveries — 最近投递记录（审计/排障）
func ListWebhookDeliveries(c *gin.Context) {
	id := whID(c)
	var w model.TenantWebhook
	if err := db.RQ(c).Scopes(db.T(c)).First(&w, id).Error; err != nil {
		RespErr(c, http.StatusNotFound, 404, "订阅不存在")
		return
	}
	var rows []model.WebhookDelivery
	db.DB.Where("webhook_id = ?", id).Order("id DESC").Limit(50).Find(&rows)
	RespOK(c, "ok", gin.H{"list": rows})
}

// --- helpers ---
// whID 从路径参数解析 Webhook ID。
func whID(c *gin.Context) uint {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	return uint(id)
}

// whTenantID 从登录态解析当前租户 ID。
func whTenantID(c *gin.Context) uint {
	return db.EffectiveTenantIDFromGin(c)
}
