// 微信客服（wecom_kf，企微生态下外部微信用户，W4，2026-09-12）
// 入站双通道：①事件回调（kf_msg_or_event 携带 token，指示有增量消息）→ 走 SyncPull；
//
//	②SyncPull：`/cgi-bin/kf/sync_msg?access_token&cursor&open_kfid` 拉取实际消息（含 next_cursor）。
//
// 出站：`/cgi-bin/kf/send_msg`。48h 会话窗口内可自由发；超窗仅可发系统消息（本期仅拦普通文本，日志告警）。
package channel

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/pkg/wxcrypt"
)

// init 初始化当前包的注册表、客户端或默认配置。
func init() { Register(&wecomKfAdapter{}) }

type wecomKfAdapter struct{}

// Type 返回微信客服通道适配器类型。
func (wecomKfAdapter) Type() string { return model.ChannelTypeWecomKf }

// DecryptInbound 与 wecom_app 共用信封（msg_signature + 加密 XML）。事件类走 IsEvent。
func (a wecomKfAdapter) DecryptInbound(cred *Credential, ts, nonce, sig string, body []byte) (*InboundMessage, error) {
	c, err := wxcrypt.New(cred.Token, cred.Encoding, cred.ReceiveID())
	if err != nil {
		return nil, err
	}
	var env wxCallbackEnvelope
	if err := xml.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	if err := c.VerifySignature(ts, nonce, env.Encrypt, sig); err != nil {
		return nil, err
	}
	plain, err := c.Decrypt(env.Encrypt)
	if err != nil {
		return nil, err
	}
	var pm wxPlainMessage
	if err := xml.Unmarshal([]byte(plain), &pm); err != nil {
		return nil, err
	}
	ext := pm.ExternalUserID
	if ext == "" {
		ext = pm.FromUserName
	}
	return &InboundMessage{
		ChannelType: model.ChannelTypeWecomKf,
		ExternalID:  ext,
		MsgType:     pm.MsgType,
		Content:     pm.Content,
		IsEvent:     pm.MsgType == "event",
		EventKey:    pm.Event,
		ReceiveID:   cred.ReceiveID(),
	}, nil
}

// VerifyURLEcho 校验回调签名并回显 URL。
func (a wecomKfAdapter) VerifyURLEcho(cred *Credential, msgSig, ts, nonce, echo string) (string, error) {
	c, err := wxcrypt.New(cred.Token, cred.Encoding, cred.ReceiveID())
	if err != nil {
		return "", err
	}
	return c.DecryptURLParam(msgSig, ts, nonce, echo)
}

// SendText POST /cgi-bin/kf/send_msg（open_kfid 即 external_id 承载）。
// 48h 窗口外发送由上层策略决定（这里只如实返回渠道错误码）。
func (a wecomKfAdapter) SendText(ctx context.Context, cred *Credential, externalID, content, msgType string) SendResult {
	tok, err := wecomToken(ctx, cred)
	if err != nil {
		return SendResult{Err: err}
	}
	base := cred.BaseURL
	if base == "" {
		base = "https://qyapi.weixin.qq.com"
	}
	u := fmt.Sprintf("%s/cgi-bin/kf/send_msg?access_token=%s", base, url.QueryEscape(tok))
	msg := map[string]interface{}{
		"touser":    externalID,
		"msgtype":   msgType,
		"open_kfid": externalID,
	}
	if msgType == "markdown" {
		msg["markdown"] = map[string]string{"content": content}
	} else {
		msg["text"] = map[string]string{"content": content}
	}
	var out struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
		MsgID   string `json:"msgid"`
	}
	ec, err := postJSON(ctx, u, msg, &out)
	if err != nil {
		return SendResult{Err: err}
	}
	if ec != 0 {
		// 22 秒窗口错误（如 9500 用户未主动咨询）转 Fatal 免空转退避
		fatal := ec == 93000 || ec == 95001 || ec == 95000
		return SendResult{Err: &ErrTokenFailed{Code: ec, Msg: out.ErrMsg}, Fatal: fatal}
	}
	return SendResult{Sent: true, MsgID: out.MsgID}
}

// SyncPullOnce 拉取指定 kf 通道的增量消息（cursor 存 config_json.kf_cursor）。
// 由 main.go 后台 ticker（channel:kf_sync，5s）调用；返回处理条数。
func SyncPullOnce(ctx context.Context, ch *model.Channel) int {
	cred, err := DecryptCredential(ch)
	if err != nil {
		return 0
	}
	tok, err := wecomToken(ctx, cred)
	if err != nil {
		return 0
	}
	base := cred.BaseURL
	if base == "" {
		base = "https://qyapi.weixin.qq.com"
	}
	cursor := ch.KfCursor // D14：优先读独立列
	if cursor == "" {
		cursor = cfgStr(ch.ConfigJSON, "kf_cursor") // 存量数据回落（列上线前的旧游标存在 config_json）
	}
	u := fmt.Sprintf("%s/cgi-bin/kf/sync_msg?access_token=%s&limit=50", base, url.QueryEscape(tok))
	if cursor != "" {
		u += "&cursor=" + url.QueryEscape(cursor)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0
	}
	resp, err := chanHTTP.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var r struct {
		ErrCode    int    `json:"errcode"`
		ErrMsg     string `json:"errmsg"`
		NextCursor string `json:"next_cursor"`
		MsgList    []struct {
			MsgID          string `json:"msgid"`
			OpenKfid       string `json:"open_kfid"`
			ExternalUserid string `json:"external_userid"`
			MsgType        string `json:"msg_type"`
			Content        string `json:"content"`
			Origin         int    `json:"origin"` // 3=客户发出
		} `json:"msglist"`
	}
	if json.Unmarshal(body, &r) != nil || r.ErrCode != 0 {
		return 0
	}
	n := 0
	failed := false // D2 修复(2026-09-14)：任一条入站处理失败即不推进游标
	for _, m := range r.MsgList {
		if m.Origin != 3 || m.Content == "" {
			continue // 只处理客户发出（3）的文本消息
		}
		in := &InboundMessage{
			ChannelType: model.ChannelTypeWecomKf,
			ExternalID:  m.ExternalUserid,
			MsgType:     m.MsgType,
			Content:     m.Content,
			MsgID:       m.MsgID, // D4：轮询重拉同批经入站幂等去重，不产生双回复
			ReceiveID:   cred.ReceiveID(),
		}
		if err := ProcessInbound(ch, in); err != nil {
			failed = true
			continue
		}
		n++
	}
	// D2 修复(2026-09-14)：旧实现无条件推进 next_cursor——ProcessInbound 报错的客户消息被永久跳过（丢消息）。
	// 现：整批全成功才推进；有失败保留旧游标，下轮重拉同批，已成功的靠 channel_inbound_msgs 幂等跳过。
	if r.NextCursor != "" && !failed {
		updateKfCursor(ch.ID, r.NextCursor)
	}
	return n
}

// updateKfCursor 将 next_cursor 写入 config_json（简单文本替换：无则加，有则换）。
func updateKfCursor(channelID uint, cursor string) {
	// D14 修复(2026-09-14)：游标独立列原子写，不再读改写 config_json（防与凭据编辑并发丢键）
	db.DB.Model(&model.Channel{}).Where("id = ?", channelID).Update("kf_cursor", cursor)
}

// setCfgStr 在 config_json 中 set 一个字符串键（简易：读入 map→写回，避免依赖大 jsonb 表达式）。
func setCfgStr(configJSON, key, val string) string {
	m := map[string]interface{}{}
	if configJSON != "" {
		_ = json.Unmarshal([]byte(configJSON), &m)
	}
	if val == "" {
		delete(m, key)
	} else {
		m[key] = val
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// SyncAllActiveKfChannels 由 main.go 后台 ticker（channel:kf_sync，5s）调用：轮询所有启用中的 kf 通道拉增量。
// 无 token 时 SyncPullOnce 直接返回 0，不做无谓重试。
func SyncAllActiveKfChannels(ctx context.Context) {
	var list []model.Channel
	if err := db.DB.Where("type = ? AND status = ? AND deleted_at IS NULL", model.ChannelTypeWecomKf, model.ChannelStatusActive).Find(&list).Error; err != nil {
		return
	}
	for i := range list {
		ch := list[i]
		_ = SyncPullOnce(ctx, &ch)
	}
}
