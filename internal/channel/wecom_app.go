// 企业微信自建应用适配器（W3，2026-09-12）
// 入站：POST 回调（验签+解密 text/image/混合事件）+ GET echostr 验证；
// 出站：message/send（text/markdown），access_token 走 TokenManager 缓存。
// agentid 复用 Channel.AppID 字段（企微应用 ID 为数字串）。
package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"time"

	"ai-scrm/internal/model"
	"ai-scrm/pkg/wxcrypt"
)

// 加密回调外层信封
type wxCallbackEnvelope struct {
	XMLName    xml.Name `xml:"xml"`
	ToUserName string   `xml:"ToUserName"`
	Encrypt    string   `xml:"Encrypt"`
	AgentID    string   `xml:"AgentID"`
}

// 解密后的明文消息/事件
type wxPlainMessage struct {
	ToUserName     string `xml:"ToUserName"`
	FromUserName   string `xml:"FromUserName"` // 企微：外部/内部 userid
	MsgType        string `xml:"MsgType"`
	Content        string `xml:"Content"`
	Event          string `xml:"Event"`
	EventKey       string `xml:"EventKey"`
	MsgID          string `xml:"MsgId"`
	AgentID        string `xml:"AgentID"`
	ExternalUserID string `xml:"ExternalUserID"` // 客服/外部联系人事件里的外部号
}

// init 初始化当前包的注册表、客户端或默认配置。
func init() { Register(&wecomAppAdapter{}) }

type wecomAppAdapter struct{}

// Type 返回企微自建应用通道适配器类型。
func (wecomAppAdapter) Type() string { return model.ChannelTypeWecomApp }

// newCrypt 构造微信消息加解密器。
func (wecomAppAdapter) newCrypt(cred *Credential) (*wxcrypt.Crypt, error) {
	return wxcrypt.New(cred.Token, cred.Encoding, cred.ReceiveID())
}

// DecryptInbound 验签 + 解密 → 归一化入站消息。
func (wecomAppAdapter) DecryptInbound(cred *Credential, timestamp, nonce, msgSignature string, body []byte) (*InboundMessage, error) {
	c, err := wxcrypt.New(cred.Token, cred.Encoding, cred.ReceiveID())
	if err != nil {
		return nil, err
	}
	var env wxCallbackEnvelope
	if err := xml.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("回调信封解析失败: %w", err)
	}
	if err := c.VerifySignature(timestamp, nonce, env.Encrypt, msgSignature); err != nil {
		return nil, err
	}
	plain, err := c.Decrypt(env.Encrypt)
	if err != nil {
		return nil, err
	}
	var pm wxPlainMessage
	if err := xml.Unmarshal([]byte(plain), &pm); err != nil {
		return nil, fmt.Errorf("明文消息解析失败: %w", err)
	}
	ext := pm.ExternalUserID
	if ext == "" {
		ext = pm.FromUserName
	}
	return &InboundMessage{
		ChannelType: model.ChannelTypeWecomApp,
		ExternalID:  ext,
		StaffID:     pm.FromUserName,
		MsgType:     pm.MsgType,
		Content:     pm.Content,
		IsEvent:     pm.MsgType == "event",
		EventKey:    pm.Event,
		ReceiveID:   cred.ReceiveID(),
	}, nil
}

// VerifyURLEcho 校验回调签名并回显 URL。
func (wecomAppAdapter) VerifyURLEcho(cred *Credential, msgSignature, timestamp, nonce, echostr string) (string, error) {
	c, err := wxcrypt.New(cred.Token, cred.Encoding, cred.ReceiveID())
	if err != nil {
		return "", err
	}
	return c.DecryptURLParam(msgSignature, timestamp, nonce, echostr)
}

// SendText 取 token→message/send。token 失效错误原样返回，由 outbound.sendOne 判断重试。
func (wecomAppAdapter) SendText(ctx context.Context, cred *Credential, externalID, content, msgType string) SendResult {
	if msgType == "" {
		msgType = "text"
	}
	tok, err := wecomToken(ctx, cred)
	if err != nil {
		return SendResult{Err: err}
	}
	base := cred.BaseURL
	if base == "" {
		base = "https://qyapi.weixin.qq.com"
	}
	agentID := atoiOr(cred.AgentID, 0)
	url := fmt.Sprintf("%s/cgi-bin/message/send?access_token=%s", base, tok)
	payload := map[string]interface{}{
		"touser":  externalID,
		"msgtype": msgType,
		"agentid": agentID,
	}
	if msgType == "markdown" {
		payload["markdown"] = map[string]string{"content": content}
	} else {
		payload["text"] = map[string]string{"content": content}
	}
	var out struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
		MsgID   string `json:"msgid"`
	}
	ec, err := postJSON(ctx, url, payload, &out)
	if err != nil {
		return SendResult{Err: err}
	}
	if ec != 0 {
		return SendResult{Err: &ErrTokenFailed{Code: ec, Msg: out.ErrMsg}}
	}
	return SendResult{Sent: true, MsgID: out.MsgID}
}

// wecomToken 换取/复用企微 access_token（corpid+corpsecret，按 channel 缓存）。
func wecomToken(ctx context.Context, cred *Credential) (string, error) {
	return defaultTokenManager.Token(cred.ChannelID, func() (string, int, error) {
		return defaultTokenManager.FetchWecomToken(ctx, cred.BaseURL, cred.CorpID, cred.Secret)
	})
}

// atoiOr 字符串转 int（失败返回 def）。
func atoiOr(s string, def int) int {
	n := 0
	if s == "" {
		return def
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return def
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// httpClient 适配器共用（10s 超时）
var chanHTTP = &http.Client{Timeout: 10 * time.Second}

// postJSON 发 JSON 返回体解析到 out；返回渠道 errcode。
func postJSON(ctx context.Context, url string, payload interface{}, out interface{}) (int, error) {
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return -1, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := chanHTTP.Do(req)
	if err != nil {
		return -1, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if out != nil {
		_ = json.Unmarshal(body, out)
	}
	var ec struct {
		ErrCode int `json:"errcode"`
	}
	_ = json.Unmarshal(body, &ec)
	return ec.ErrCode, nil
}
