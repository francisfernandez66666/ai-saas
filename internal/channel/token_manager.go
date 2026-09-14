// access_token 管理器（W3/W5 共用，2026-09-12）
// 企微 corpid+secret 与 公众号 appid+secret 均走"换 token→缓存→临期刷新"同一模式。
// 可靠性：进程内按 channelID 缓存，提前 5 分钟刷新；命中 -1（token 失效）时强制刷新并重试一次。
// 可测性：BaseURL 可注入（config_json.mock_base_url 或测试 httptest），无真实凭证也能跑通。
package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// ErrTokenFailed 换取 access_token 失败（携带渠道返回码便于诊断）
type ErrTokenFailed struct {
	Code int
	Msg  string
}

// Error 返回 access_token 获取失败的稳定错误文本。
func (e *ErrTokenFailed) Error() string {
	return fmt.Sprintf("换取 access_token 失败: code=%d msg=%s", e.Code, e.Msg)
}

type cachedToken struct {
	token    string
	expireAt time.Time
}

// TokenManager 进程内 access_token 缓存（按 channelID 分桶，多租户多应用互不串）。
type TokenManager struct {
	mu  sync.Mutex
	cm  map[uint]*cachedToken
	hc  *http.Client
	now func() time.Time
}

// NewTokenManager 构造（now 注入便于单测计时）。
func NewTokenManager() *TokenManager {
	return &TokenManager{
		cm:  map[uint]*cachedToken{},
		hc:  &http.Client{Timeout: 10 * time.Second},
		now: time.Now,
	}
}

// defaultTokenManager 全局实例（出站/入站适配器共用）。
var defaultTokenManager = NewTokenManager()

// DefaultTokenManager 暴露全局 token 管理器（连通性测试/装配用）。
func DefaultTokenManager() *TokenManager { return defaultTokenManager }

// tokenResp 微信/企微统一返回结构（errcode=0 时 token 有效）。
type tokenResp struct {
	ErrCode     int    `json:"errcode"`
	ErrMsg      string `json:"errmsg"`
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// Token 返回缓存或新换取的 access_token。baseURL 为渠道 API 根（mock 注入点）。
// endpoint 由调用方给完整 URL（含 query），本函数只管缓存与刷新时机。
func (tm *TokenManager) Token(channelID uint, fetch func() (string, int, error)) (string, error) {
	tm.mu.Lock()
	c := tm.cm[channelID]
	if c != nil && tm.now().Before(c.expireAt) {
		tok := c.token
		tm.mu.Unlock()
		return tok, nil
	}
	tm.mu.Unlock()

	tok, expiresIn, err := fetch()
	if err != nil {
		return "", err
	}
	tm.mu.Lock()
	// 提前 5 分钟到期；下限保护避免 expiresIn 异常导致频繁刷新
	expire := tm.now().Add(time.Duration(expiresIn)*time.Second - 5*time.Minute)
	if expiresIn <= 0 {
		expire = tm.now().Add(60 * time.Second)
	}
	tm.cm[channelID] = &cachedToken{token: tok, expireAt: expire}
	tm.mu.Unlock()
	return tok, nil
}

// Invalidate 强制失效某通道 token（收到 -1 时调用，下次重新换取）。
func (tm *TokenManager) Invalidate(channelID uint) {
	tm.mu.Lock()
	delete(tm.cm, channelID)
	tm.mu.Unlock()
}

// FetchWecomToken 企微自建应用/客服：corpid+corpsecret 换 token。
func (tm *TokenManager) FetchWecomToken(ctx context.Context, baseURL, corpid, secret string) (string, int, error) {
	if baseURL == "" {
		baseURL = "https://qyapi.weixin.qq.com"
	}
	u := fmt.Sprintf("%s/cgi-bin/gettoken?corpid=%s&corpsecret=%s", baseURL, url.QueryEscape(corpid), url.QueryEscape(secret))
	return tm.doToken(ctx, u)
}

// FetchMPWechatToken 公众号：appid+secret 换 token。
func (tm *TokenManager) FetchMPWechatToken(ctx context.Context, baseURL, appid, secret string) (string, int, error) {
	if baseURL == "" {
		baseURL = "https://api.weixin.qq.com"
	}
	u := fmt.Sprintf("%s/cgi-bin/token?grant_type=client_credential&appid=%s&secret=%s", baseURL, url.QueryEscape(appid), url.QueryEscape(secret))
	return tm.doToken(ctx, u)
}

// doToken 请求通道 access_token 并刷新缓存。
func (tm *TokenManager) doToken(ctx context.Context, u string) (string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", 0, err
	}
	resp, err := tm.hc.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var tr tokenResp
	if json.Unmarshal(b, &tr) != nil {
		return "", 0, fmt.Errorf("token 响应解析失败: %s", string(b))
	}
	if tr.ErrCode != 0 {
		return "", tr.ErrCode, &ErrTokenFailed{Code: tr.ErrCode, Msg: tr.ErrMsg}
	}
	return tr.AccessToken, tr.ExpiresIn, nil
}

// resolveBaseURL 从通道 config_json 读取 mock/自定义 API 根（无则空=用官方默认）。
func resolveBaseURL(configJSON string) string {
	return cfgStr(configJSON, "mock_base_url")
}

// cfgStr 从 config_json 读取字符串字段（缺失/非法返回空）。
func cfgStr(configJSON, key string) string {
	if configJSON == "" {
		return ""
	}
	var m map[string]interface{}
	if json.Unmarshal([]byte(configJSON), &m) != nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}
