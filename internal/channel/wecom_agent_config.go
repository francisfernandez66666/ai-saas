// 企微「应用级」JS-SDK 配置（E1 批，2026-09-24）：给侧边栏 H5 的 wx.agentConfig 出签名。
//
// 为什么单开一个文件、且不复用 corp jsapi_ticket 的缓存：
//
//	wx.config 用的是 corp 级 ticket（/cgi-bin/get_jsapi_ticket），
//	wx.agentConfig 用的是另一张 —— /cgi-bin/ticket/get?type=agent_config。
//	两张贴纸内容不同、到期不同，混进同一个 map 的后果是"签名算得出来、微信侧一律 40102"，
//	而且只在用户先点过一次侧边栏之后才复现 —— 最难查的那类错位。所以各自一份缓存、各自判临期。
//
// 签名公式两边共用 channel.SignJSConfig（sha1，参数按字典序拼），
// 公式只允许有一处实现：两处各抄一遍，改一处漏一处，表现就是侧边栏偶发"无权限"。
package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ErrAgentIDMissing 通道没录入企微应用 agentid：此时任何"应用级"接口都无从签名。
// 不回落到 0 —— agentid=0 发出去微信必拒，而错误里写清"去通道表单补 AgentID"才有出路。
var ErrAgentIDMissing = errors.New("channel: agentid 未录入")

// agentConfigTicket 进程内缓存（按 channelID 分桶，提前 5 分钟刷新，口径同 D12 corp ticket）。
var (
	agentConfigTicketMu  sync.Mutex
	agentConfigTicketMap = map[uint]struct {
		ticket   string
		expireAt time.Time
	}{}
)

// ResetAgentConfigTicketCache 清空 agent_config 票据缓存。
//
// 为什么要暴露：换凭据（改 secret/重建通道）后旧票据还能用一段时间，
// 而票据是按 channelID 缓存的，测试与"改了配不等重启"都需要一个显式清位。
func ResetAgentConfigTicketCache(channelID uint) {
	agentConfigTicketMu.Lock()
	delete(agentConfigTicketMap, channelID)
	agentConfigTicketMu.Unlock()
}

// ResolvedAgentID 取本通道的企微应用 agentid（出站与签名的唯一判据）。
//
// config_json.agentid 是录入面的正式字段（Admin 通道表单写它）；早期约定"应用 ID 直接填在
// appid 列"，所以留一条回落 —— 但**只在 appid 是纯数字时**回落：公众号 appid 是 wx 开头
// 的字符串，拿它当 agentid 只是把 0 换成一个更没道理的数。
func (c *Credential) ResolvedAgentID() string {
	if s := strings.TrimSpace(c.AgentID); s != "" {
		return s
	}
	if s := strings.TrimSpace(c.AppID); s != "" && isAllDigits(s) {
		return s
	}
	return ""
}

// isAllDigits 纯数字判定（agentid 是数字串）。空串不算数字，交给上面的非空分支。
func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// StripURLFragment 去掉 URL 的 #片段。
//
// 企微按"不含 hash 的完整地址"算签名，而侧边栏多半直接传 location.href ——
// hash 一带上就是稳定签名不匹配（页面看起来一切正常，只是 agentConfig 一直报 invalid signature）。
// corp 级与 agent 级两条签名都走这里，避免"同一个 url 两种签法"。
func StripURLFragment(raw string) string {
	s := strings.TrimSpace(raw)
	if i := strings.IndexByte(s, '#'); i >= 0 {
		s = s[:i]
	}
	return s
}

// FetchAgentConfigTicket 拉 type=agent_config 票据（命中缓存直接复用，不烧企微接口配额）。
func FetchAgentConfigTicket(ctx context.Context, cred *Credential) (string, error) {
	agentConfigTicketMu.Lock()
	if e, ok := agentConfigTicketMap[cred.ChannelID]; ok && time.Now().Before(e.expireAt) {
		tk := e.ticket
		agentConfigTicketMu.Unlock()
		return tk, nil
	}
	agentConfigTicketMu.Unlock()

	tok, err := wecomToken(ctx, cred)
	if err != nil {
		return "", err
	}
	base := cred.BaseURL
	if base == "" {
		base = "https://qyapi.weixin.qq.com"
	}
	u := fmt.Sprintf("%s/cgi-bin/ticket/get?access_token=%s&type=agent_config", base, url.QueryEscape(tok))
	body, err := getJSON(ctx, u)
	if err != nil {
		return "", err
	}
	var r struct {
		ErrCode   int    `json:"errcode"`
		ErrMsg    string `json:"errmsg"`
		Ticket    string `json:"ticket"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.ErrCode != 0 || r.Ticket == "" {
		return "", fmt.Errorf("拉取 agent_config ticket 失败: %s", string(body))
	}
	ttl := time.Duration(r.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	ttl -= 5 * time.Minute // 提前 5 分钟过期，避开"卡在边界上拿到刚失效的票"
	agentConfigTicketMu.Lock()
	agentConfigTicketMap[cred.ChannelID] = struct {
		ticket   string
		expireAt time.Time
	}{r.Ticket, time.Now().Add(ttl)}
	agentConfigTicketMu.Unlock()
	return r.Ticket, nil
}

// AgentCfgResult wx.agentConfig 需要的字段，前端直接摊进 wx.agentConfig({...})。
// 注意 agentid 下发的是字符串：JS 侧数字超过安全整数会精度丢失，签名又必须与它配套。
type AgentCfgResult struct {
	CorpID    string `json:"corpid"`
	AgentID   string `json:"agentid"`
	Timestamp string `json:"timestamp"`
	NonceStr  string `json:"noncestr"`
	Signature string `json:"signature"`
}

// BuildAgentConfig 一步生成 wx.agentConfig 参数（通道需 active、凭据可解密、agentid 已录入）。
func BuildAgentConfig(ctx context.Context, cred *Credential, rawURL string) (*AgentCfgResult, error) {
	target := StripURLFragment(rawURL)
	if target == "" {
		return nil, errors.New("url 必填")
	}
	agentID := cred.ResolvedAgentID()
	if agentID == "" {
		return nil, ErrAgentIDMissing
	}
	ticket, err := FetchAgentConfigTicket(ctx, cred)
	if err != nil {
		return nil, err
	}
	ts := fmt.Sprint(time.Now().Unix())
	nonce := fmt.Sprintf("n%d", time.Now().UnixNano()%100000)
	return &AgentCfgResult{
		CorpID:    cred.CorpID,
		AgentID:   agentID,
		Timestamp: ts,
		NonceStr:  nonce,
		Signature: SignJSConfig(ticket, nonce, ts, target),
	}, nil
}

// getJSON 发一个 GET 并原样回响应体（企微 ticket 类端点的统一取法）。
//
// BaseURL 只在 mock/自测注入，release 侧由 P2-12 的 SSRF 闸校验（见 channel_admin.go），
// 与 wecomToken/FetchCorpJSAPITicket 同一口径，不在这里另设第二道判断。
func getJSON(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := chanHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
