// 通道管理端接口（W2/F11，2026-09-12）：CRUD + 凭据一次性回显/掩码 + 连通性测试 + 出站死信。
package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"ai-scrm/internal/channel"
	"ai-scrm/internal/model"
	"ai-scrm/pkg/crypto"
)

// ---- channel_admin 局部小助手（避免依赖不存在的通用工具）----

func chanID(c *gin.Context) uint {
	n, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	return uint(n)
}

// strPtrIfNotEmpty 把非空字符串转换为指针，便于 JSON 省略空字段。
func strPtrIfNotEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// errString 将 error 安全转换为字符串。
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// callbackURLHint 通道专属回调地址（含 :id，多通道无歧义；前端拼域名后填到渠道后台）。
func callbackURLHint(ch *model.Channel) string {
	return "/api/v1/channel/callback/" + strconv.FormatUint(uint64(ch.ID), 10)
}

// channelCreateReq 通道接入凭据创建/更新请求体：type 为适配器类型（wecom_app/wecom_kf/wechat_mp），
// 凭据明文字段服务端经 pkg/crypto AES-GCM 加密后落密文列，响应永不回显明文。
type channelCreateReq struct {
	Type         string `json:"type" binding:"required"`
	Name         string `json:"name" binding:"required"`
	CorpID       string `json:"corpid"`
	AppID        string `json:"appid"`
	AgentID      string `json:"agentid"`
	Secret       string `json:"secret"`
	Token        string `json:"token"`
	Encoding     string `json:"encoding_aes_key"`
	DepartmentID uint   `json:"department_id"`
	ConfigJSON   string `json:"config_json"`
}

// channelView 通道出接口视图（凭据恒为掩码，明文只在 create 一次性回显）。
type channelView struct {
	ID           uint   `json:"id"`
	Type         string `json:"type"`
	Name         string `json:"name"`
	CorpID       string `json:"corpid"`
	AppID        string `json:"appid"`
	Status       string `json:"status"`
	DepartmentID uint   `json:"department_id"`
	SecretMask   string `json:"secret_mask"`
	TokenMask    string `json:"token_mask"`
	AesKeyMask   string `json:"aeskey_mask"`
	ConfigJSON   string `json:"config_json"`
	CreatedAt    string `json:"created_at"`
}

// maskOf 返回通道凭据字段掩码。
func maskOf(cipherStr string) string {
	// 解出明文再掩码（仅本租户管理员可见掩码）；解不开→"****"
	if plain, err := crypto.Decrypt(cipherStr); err == nil {
		return crypto.MaskSecret(plain)
	}
	return "****"
}

// toView 将通道模型转换为管理端响应视图。
func toView(ch model.Channel) channelView {
	return channelView{
		ID: ch.ID, Type: ch.Type, Name: ch.Name, CorpID: ch.CorpID, AppID: ch.AppID,
		Status: ch.Status, DepartmentID: ch.DepartmentID,
		SecretMask: maskOf(ch.SecretCipher), TokenMask: maskOf(ch.TokenCipher), AesKeyMask: maskOf(ch.AesKeyCipher),
		ConfigJSON: ch.ConfigJSON, CreatedAt: ch.CreatedAt.Format("2006-01-02 15:04:05"),
	}
}

// injectAgentID 把 agentid 合入 config_json（企微 message/send 需要）。
func injectAgentID(cfgJSON, agentID string) string {
	if agentID == "" {
		return cfgJSON
	}
	if cfgJSON == "" || cfgJSON == "{}" {
		return `{"agentid":"` + agentID + `"}`
	}
	// 简易合并：在首个 } 前插入（config_json 为受控扁平对象）
	for i := len(cfgJSON) - 1; i >= 0; i-- {
		if cfgJSON[i] == '}' {
			sep := ","
			if cfgJSON[:i] == "{" {
				sep = ""
			}
			return cfgJSON[:i] + sep + `"agentid":"` + agentID + `"}`
		}
	}
	return cfgJSON
}

// ListChannels GET /admin/channels
// apidump:ts ChannelListResp
// ListChannels 返回租户接入通道列表。
func ListChannels(c *gin.Context) {
	list, err := channel.List(tenantIDOf(c))
	if err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "查询通道失败")
		return
	}
	views := make([]channelView, 0, len(list))
	for _, ch := range list {
		views = append(views, toView(ch))
	}
	RespOK(c, "ok", gin.H{"list": views})
}

// CreateChannel POST /admin/channels —— 凭据加密落库，明文一次性回显。
// apidump:ts CreateChannelResp
// CreateChannel 创建通道并加密保存凭据。
func CreateChannel(c *gin.Context) {
	var req channelCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误：type/name 必填")
		return
	}
	cfg := injectAgentID(req.ConfigJSON, req.AgentID)
	ch, err := channel.Create(channel.CreateInput{
		TenantID: tenantIDOf(c), Type: req.Type, Name: req.Name, CorpID: req.CorpID, AppID: req.AppID,
		Secret: req.Secret, Token: req.Token, Encoding: req.Encoding, DepartmentID: req.DepartmentID, ConfigJSON: cfg,
	})
	if err != nil {
		RespErr(c, http.StatusBadRequest, 400, "创建失败："+err.Error())
		return
	}
	writeAuditSimple(c, tenantIDOf(c), "channel_create", "channel:"+ch.Type)
	// 一次性回显明文（仅此响应，列表/详情均为掩码）
	RespOK(c, "通道已创建（凭据明文仅此次可见，请妥善保管）", gin.H{
		"channel":      toView(*ch),
		"plaintext":    gin.H{"secret": req.Secret, "token": req.Token, "encoding_aes_key": req.Encoding},
		"callback_url": callbackURLHint(ch),
	})
}

// UpdateChannel PUT /admin/channels/:id —— 凭据字段留空=不改动。
func UpdateChannel(c *gin.Context) {
	var req channelCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErr(c, http.StatusBadRequest, 400, "参数错误")
		return
	}
	id := chanID(c)
	cfg := injectAgentID(req.ConfigJSON, req.AgentID)
	_, err := channel.Update(tenantIDOf(c), id, channel.UpdateInput{
		Name: req.Name, CorpID: req.CorpID, AppID: req.AppID, Secret: req.Secret, Token: req.Token,
		Encoding: req.Encoding, Status: "", ConfigJSON: strPtrIfNotEmpty(cfg),
	})
	if err != nil {
		RespErr(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	writeAuditSimple(c, tenantIDOf(c), "channel_update", "channel")
	RespOK(c, "已更新", nil)
}

// DeleteChannel DELETE /admin/channels/:id （软删）
func DeleteChannel(c *gin.Context) {
	id := chanID(c)
	if err := channel.Delete(tenantIDOf(c), id); err != nil {
		RespErr(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	writeAuditSimple(c, tenantIDOf(c), "channel_delete", "channel")
	RespOK(c, "已删除", nil)
}

// SetChannelStatus PUT /admin/channels/:id/status?status=active|disabled —— 连通后启用/停用。
func SetChannelStatus(c *gin.Context) {
	id := chanID(c)
	status := c.Query("status")
	if status != model.ChannelStatusActive && status != model.ChannelStatusDisabled {
		RespErr(c, http.StatusBadRequest, 400, "status 仅 active|disabled")
		return
	}
	_, err := channel.Update(tenantIDOf(c), id, channel.UpdateInput{Status: status})
	if err != nil {
		RespErr(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	writeAuditSimple(c, tenantIDOf(c), "channel_status", "channel:"+status)
	RespOK(c, "已更新状态", nil)
}

// VerifyChannel POST /admin/channels/:id/verify —— 拉 access_token 冒烟（成功则自动置 active）。
func VerifyChannel(c *gin.Context) {
	id := chanID(c)
	ch, err := channel.Get(tenantIDOf(c), id)
	if err != nil {
		RespErr(c, http.StatusNotFound, 404, "通道不存在")
		return
	}
	cred, err := channel.DecryptCredential(ch)
	if err != nil {
		RespErr(c, http.StatusBadRequest, 400, "凭据需重录（密钥轮换）")
		return
	}
	ctx := c.Request.Context()
	var tok string
	switch ch.Type {
	case model.ChannelTypeWechatMP:
		tok, _, err = channel.DefaultTokenManager().FetchMPWechatToken(ctx, cred.BaseURL, cred.AppID, cred.Secret)
	default:
		tok, _, err = channel.DefaultTokenManager().FetchWecomToken(ctx, cred.BaseURL, cred.CorpID, cred.Secret)
	}
	if err != nil || tok == "" {
		RespOK(c, "连通失败：token 换取未通过", gin.H{"ok": false, "detail": errString(err)})
		return
	}
	channel.Update(tenantIDOf(c), id, channel.UpdateInput{Status: model.ChannelStatusActive})
	RespOK(c, "连通成功，已启用", gin.H{"ok": true})
}

// ListChannelDeadLetters GET /admin/channels/dead-letters （W6 死信可见）
// apidump:ts OutboundListResp
// ListChannelDeadLetters 返回出站死信队列。
func ListChannelDeadLetters(c *gin.Context) {
	list, err := channel.ListDeadLetters(tenantIDOf(c))
	if err != nil {
		RespErr(c, http.StatusInternalServerError, 500, "查询失败")
		return
	}
	RespOK(c, "ok", gin.H{"list": list})
}

// RetryChannelDeadLetter POST /admin/channels/dead-letters/:id/retry
// RetryChannelDeadLetter 手动重发一条出站死信。
func RetryChannelDeadLetter(c *gin.Context) {
	id := chanID(c)
	if err := channel.RetryDeadLetter(tenantIDOf(c), id); err != nil {
		RespErr(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	writeAuditSimple(c, tenantIDOf(c), "channel_dlq_retry", "outbound")
	RespOK(c, "已重发", nil)
}
