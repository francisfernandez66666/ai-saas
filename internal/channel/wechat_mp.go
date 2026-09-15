// 微信公众号适配器（W5，2026-09-12）
// 入站：GET URL 校验（signature/timestamp/nonce/echostr，明文/安全模式均支持）+ POST 消息回调（安全模式加密 XML）。
// 出站：`/cgi-bin/message/custom/send?access_token=`；48h 客服窗口内可发，超窗直接进死信（不空转）。
// access_token：走公众号端点（appid+secret 换 token）。
package channel

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"ai-scrm/internal/model"
	"ai-scrm/pkg/wxcrypt"
)

// init 初始化当前包的注册表、客户端或默认配置。
func init() { Register(&wechatMPAdapter{}) }

type wechatMPAdapter struct{}

// Type 返回微信公众号通道适配器类型。
func (wechatMPAdapter) Type() string { return model.ChannelTypeWechatMP }

// mpPlainMessage 公众号明文消息（openid 承载于 FromUserName）
type mpPlainMessage struct {
	ToUserName   string `xml:"ToUserName"`   // 公众号 appid
	FromUserName string `xml:"FromUserName"` // 用户 openid
	MsgType      string `xml:"MsgType"`
	Content      string `xml:"Content"`
	Event        string `xml:"Event"`
	MsgID        string `xml:"MsgId"` // D4：入站幂等锚
}

// DecryptInbound 与企微共用 wxcrypt 信封，仅 receive_id=appid；公众号事件类也照此解析。
func (wechatMPAdapter) DecryptInbound(cred *Credential, ts, nonce, msgSig string, body []byte) (*InboundMessage, error) {
	c, err := wxcrypt.New(cred.Token, cred.Encoding, cred.ReceiveID())
	if err != nil {
		return nil, err
	}
	var env wxCallbackEnvelope
	if err := xml.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("公众号回调信封解析失败: %w", err)
	}
	if err := c.VerifySignature(ts, nonce, env.Encrypt, msgSig); err != nil {
		return nil, err
	}
	plain, err := c.Decrypt(env.Encrypt)
	if err != nil {
		return nil, err
	}
	var pm mpPlainMessage
	if err := xml.Unmarshal([]byte(plain), &pm); err != nil {
		return nil, err
	}
	return &InboundMessage{
		ChannelType: model.ChannelTypeWechatMP,
		ExternalID:  pm.FromUserName,
		MsgType:     pm.MsgType,
		Content:     pm.Content,
		MsgID:       pm.MsgID, // D4：入站幂等锚
		IsEvent:     pm.MsgType == "event",
		EventKey:    pm.Event,
		ReceiveID:   cred.ReceiveID(),
	}, nil
}

// VerifyURLEcho 校验回调签名并回显 URL。
func (wechatMPAdapter) VerifyURLEcho(cred *Credential, msgSig, ts, nonce, echo string) (string, error) {
	// 明文模式：msgSig 为空 → 直接回显 echo（配置层未加密时的兼容通道）
	if msgSig == "" {
		return echo, nil
	}
	c, err := wxcrypt.New(cred.Token, cred.Encoding, cred.ReceiveID())
	if err != nil {
		return "", err
	}
	return c.DecryptURLParam(msgSig, ts, nonce, echo)
}

// SendText 取 mp token→custom/send。errcode 45015（超 48h 窗口）转 Fatal 免退避。
func (wechatMPAdapter) SendText(ctx context.Context, cred *Credential, externalID, content, msgType string) SendResult {
	tok, err := mpToken(ctx, cred)
	if err != nil {
		return SendResult{Err: err}
	}
	base := cred.BaseURL
	if base == "" {
		base = "https://api.weixin.qq.com"
	}
	u := fmt.Sprintf("%s/cgi-bin/message/custom/send?access_token=%s", base, url.QueryEscape(tok))
	payload := map[string]interface{}{
		"touser":  externalID,
		"msgtype": "text",
	}
	payload["text"] = map[string]string{"content": content}
	var out struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
		MsgID   string `json:"msgid"`
	}
	ec, err := postJSON(ctx, u, payload, &out)
	if err != nil {
		return SendResult{Err: err}
	}
	if ec != 0 {
		fatal := ec == 45015 || ec == 40003 // 超窗/非法 openid 不重试
		return SendResult{Err: &ErrTokenFailed{Code: ec, Msg: out.ErrMsg}, Fatal: fatal}
	}
	return SendResult{Sent: true, MsgID: out.MsgID}
}

// mpToken 公众号 access_token 换取（按 channel 缓存）
func mpToken(ctx context.Context, cred *Credential) (string, error) {
	return defaultTokenManager.Token(cred.ChannelID, func() (string, int, error) {
		return defaultTokenManager.FetchMPWechatToken(ctx, cred.BaseURL, cred.AppID, cred.Secret)
	})
}

// ============================================================
// 侧边栏 JS-SDK 签名（W7，企微 JS-SDK corp 级签名）
// ============================================================

// D12 修复(2026-09-14)：corp jsapi_ticket 进程内缓存（按 channelID 分桶，提前 5 分钟刷新）。
// 旧实现注释"缓存 30 分钟"但每次现拉——jsconfig 调用密集时烧微信接口配额且拖慢侧边栏首屏。
var (
	jsapiTicketMu  sync.Mutex
	jsapiTicketMap = map[uint]struct {
		ticket   string
		expireAt time.Time
	}{}
)

// FetchCorpJSAPITicket 拉取企微 corp jsapi_ticket（进程内缓存，临期前复用）。
func FetchCorpJSAPITicket(ctx context.Context, cred *Credential) (string, error) {
	// D12 命中缓存直接返回（cred.ChannelID 入站/侧边栏装配均已回填）
	jsapiTicketMu.Lock()
	if e, ok := jsapiTicketMap[cred.ChannelID]; ok && time.Now().Before(e.expireAt) {
		tk := e.ticket
		jsapiTicketMu.Unlock()
		return tk, nil
	}
	jsapiTicketMu.Unlock()

	tok, err := wecomToken(ctx, cred)
	if err != nil {
		return "", err
	}
	base := cred.BaseURL
	if base == "" {
		base = "https://qyapi.weixin.qq.com"
	}
	u := fmt.Sprintf("%s/cgi-bin/get_jsapi_ticket?access_token=%s", base, url.QueryEscape(tok))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := chanHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var r struct {
		ErrCode   int    `json:"errcode"`
		ErrMsg    string `json:"errmsg"`
		Ticket    string `json:"ticket"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.ErrCode != 0 {
		return "", fmt.Errorf("拉取 corp jsapi_ticket 失败: %s", string(body))
	}
	// D12：写入缓存，提前 5 分钟到期（默认 7200s 生命周期）
	ttl := time.Duration(r.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	ttl -= 5 * time.Minute
	jsapiTicketMu.Lock()
	jsapiTicketMap[cred.ChannelID] = struct {
		ticket   string
		expireAt time.Time
	}{r.Ticket, time.Now().Add(ttl)}
	jsapiTicketMu.Unlock()
	return r.Ticket, nil
}

// SignJSConfig 计算 wx.config 的 corp 级签名：
//
//	sha1("jsapi_ticket="+ticket+"&noncestr="+nonce+"&timestamp="+ts+"&url="+url)
//
// SignJSConfig 生成公众号 JS-SDK 配置签名。
func SignJSConfig(ticket, nonce, ts, targetURL string) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("jsapi_ticket=%s&noncestr=%s&timestamp=%s&url=%s", ticket, nonce, ts, targetURL)))
	return hex.EncodeToString(sum[:])
}

// JSCfgResult JS-SDK 配置签名返回值。
type JSCfgResult struct {
	CorpID    string `json:"corpid"`
	Timestamp string `json:"timestamp"`
	NonceStr  string `json:"noncestr"`
	Signature string `json:"signature"`
}

// BuildJSConfig 一步生成 wx.config 参数（channel 需 active + 凭据可解密）。
func BuildJSConfig(ctx context.Context, cred *Credential, targetURL string) (*JSCfgResult, error) {
	if targetURL == "" {
		return nil, errors.New("url 必填")
	}
	ticket, err := FetchCorpJSAPITicket(ctx, cred)
	if err != nil {
		return nil, err
	}
	ts := fmt.Sprint(time.Now().Unix())
	nonce := fmt.Sprintf("n%d", time.Now().UnixNano()%100000)
	return &JSCfgResult{
		CorpID:    cred.CorpID,
		Timestamp: ts,
		NonceStr:  nonce,
		Signature: SignJSConfig(ticket, nonce, ts, targetURL),
	}, nil
}
