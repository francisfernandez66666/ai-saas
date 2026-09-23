// E1 批（2026-09-24）企微应用级签名单测：wx.agentConfig 的贴纸来源、agentid 判据、
// URL 去片段，以及**两张票据缓存互不污染**——这是本文件存在的头号理由。
// corp 票与应用票内容不同，一旦共用一个 map，表现是"签名算得出来、企微侧恒 40102"，
// 且只在侧边栏被点过一次之后复现；单测必须先把这条错位钉死。
package channel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// agentMock 起一个同时提供 corp 票与应用票的模拟企微端点，并记录两端命中次数。
//
// 两票字面量刻意不同（CORP_TICKET / AGENT_TICKET）：签名由 ticket 直接决定，
// 于是"缓存串味"会立刻表现为 signature 用错贴纸，而不是一个看不见的 40102。
type agentMock struct {
	srv       *httptest.Server
	corpHits  int
	agentHits int
	agentType string // 最近一次 /cgi-bin/ticket/get 的 type 参数
	failAgent bool   // true 时应用票端点回企微错误（模拟换票失败）
	mu        sync.Mutex
}

func (m *agentMock) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/cgi-bin/gettoken", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "access_token": "TK", "expires_in": 7200})
	})
	mux.HandleFunc("/cgi-bin/get_jsapi_ticket", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.corpHits++
		m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "ticket": "CORP_TICKET", "expires_in": 7200})
	})
	mux.HandleFunc("/cgi-bin/ticket/get", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.agentHits++
		m.agentType = r.URL.Query().Get("type")
		fail := m.failAgent
		m.mu.Unlock()
		if fail {
			_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 40013, "errmsg": "invalid app secret"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "ticket": "AGENT_TICKET", "expires_in": 7200})
	})
	return mux
}

func newAgentMock(t *testing.T) *agentMock {
	t.Helper()
	m := &agentMock{}
	m.srv = httptest.NewServer(m.handler())
	t.Cleanup(m.srv.Close)
	return m
}

// TestResolvedAgentID agentid 判据：显式字段优先，appid 仅在纯数字时回落。
//
// 反证用例（wx 开头的 appid 不得当 agentid 用）是这条回落唯一的护栏——
// 公众号 appid 混进企微出站会把一个明确的 0 换成一个更没道理的数，
// 而企微回的是"agentid 不合法"，排查方向指向网络层。
func TestResolvedAgentID(t *testing.T) {
	cases := []struct {
		name string
		cred Credential
		want string
	}{
		{"config_json 显式 agentid 优先", Credential{AgentID: "1000002", AppID: "9999999"}, "1000002"},
		{"空 agentid + 纯数字 appid 回落", Credential{AppID: "1000003"}, "1000003"},
		{"空 agentid + 公众号 wx appid 不回落", Credential{AppID: "wx0a1b2c3d4e5f"}, ""},
		{"两侧皆空", Credential{}, ""},
		{"空白串视同空", Credential{AgentID: "  ", AppID: " 1000004 "}, "1000004"},
	}
	for _, tc := range cases {
		got := tc.cred.ResolvedAgentID()
		if got != tc.want {
			t.Fatalf("%s: 期望 %q 实际 %q", tc.name, tc.want, got)
		}
	}
}

// TestStripURLFragment 签名 URL 必须去掉 # 片段（企微按不含 hash 的地址算签名）。
func TestStripURLFragment(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://a.com/x?id=1#/sidebar", "https://a.com/x?id=1"},
		{"https://a.com/x", "https://a.com/x"},
		{"  https://a.com/x#/y  ", "https://a.com/x"},
		{"https://a.com/#", "https://a.com/"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := StripURLFragment(tc.in); got != tc.want {
			t.Fatalf("StripURLFragment(%q) 期望 %q 实际 %q", tc.in, tc.want, got)
		}
	}
}

// TestAgentConfigTicketNotSharedWithCorpTicket 两张票各自缓存、各自取用。
//
// 断言三件事：① 应用票端点带的是 type=agent_config（拿 corp 票的 URL 换不到它）；
// ② 同一 channelID 下 corp 签名用 CORP_TICKET、应用签名用 AGENT_TICKET；
// ③ 各调两次每端只命中一次（缓存真生效，没在侧边栏每次点击都烧企微配额）。
func TestAgentConfigTicketNotSharedWithCorpTicket(t *testing.T) {
	m := newAgentMock(t)
	cred := &Credential{ChannelID: 9201, CorpID: "wx_corp_9201", Secret: "s", AgentID: "1000002", BaseURL: m.srv.URL}
	ctx := context.Background()
	const target = "https://scrm.example.com/advisor?cid=7#chat"

	corp, err := BuildJSConfig(ctx, cred, target)
	if err != nil {
		t.Fatalf("corp jsconfig 失败: %v", err)
	}
	agent, err := BuildAgentConfig(ctx, cred, target)
	if err != nil {
		t.Fatalf("agentConfig 失败: %v", err)
	}
	if m.agentType != "agent_config" {
		t.Fatalf("应用票未带 type=agent_config，实际 type=%q", m.agentType)
	}
	// 两条签名必须各自配自己的贴纸：串味时这里必错（而不是线上偶发 40102）。
	if got := SignJSConfig("CORP_TICKET", corp.NonceStr, corp.Timestamp, "https://scrm.example.com/advisor?cid=7"); got != corp.Signature {
		t.Fatalf("corp 签名与 CORP_TICKET 重算不符: %+v", corp)
	}
	if got := SignJSConfig("AGENT_TICKET", agent.NonceStr, agent.Timestamp, "https://scrm.example.com/advisor?cid=7"); got != agent.Signature {
		t.Fatalf("应用签名与 AGENT_TICKET 重算不符（两票缓存串味）: %+v", agent)
	}
	if agent.Signature == corp.Signature {
		t.Fatalf("两条签名相同，说明其中一张票用错: %s", agent.Signature)
	}
	if agent.AgentID != "1000002" || agent.CorpID != "wx_corp_9201" {
		t.Fatalf("agentConfig 出参错: %+v", agent)
	}

	// 二次调用走缓存：两端点各只应被命中一次。
	for i := 0; i < 2; i++ {
		if _, err := BuildJSConfig(ctx, cred, target); err != nil {
			t.Fatalf("第 %d 次 corp jsconfig 失败: %v", i+2, err)
		}
		if _, err := BuildAgentConfig(ctx, cred, target); err != nil {
			t.Fatalf("第 %d 次 agentConfig 失败: %v", i+2, err)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.corpHits != 1 || m.agentHits != 1 {
		t.Fatalf("票据缓存未生效: corp 命中 %d 应用命中 %d（期望各 1）", m.corpHits, m.agentHits)
	}
}

// TestResetAgentConfigTicketCache 换凭据后要能显式清掉应用票，否则旧票在 TTL 内继续签。
func TestResetAgentConfigTicketCache(t *testing.T) {
	m := newAgentMock(t)
	cred := &Credential{ChannelID: 9202, CorpID: "wx_corp_9202", Secret: "s", AgentID: "1000002", BaseURL: m.srv.URL}
	if _, err := BuildAgentConfig(context.Background(), cred, "https://a.com/x"); err != nil {
		t.Fatalf("agentConfig 失败: %v", err)
	}
	ResetAgentConfigTicketCache(cred.ChannelID)
	if _, err := BuildAgentConfig(context.Background(), cred, "https://a.com/x"); err != nil {
		t.Fatalf("清缓存后重建失败: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.agentHits != 2 {
		t.Fatalf("ResetAgentConfigTicketCache 未生效，命中 %d 次（期望 2）", m.agentHits)
	}
}

// TestBuildAgentConfigMissingAgentID 未录入 agentid 必须是可分支的稳定错误，而非签出一个 0。
func TestBuildAgentConfigMissingAgentID(t *testing.T) {
	m := newAgentMock(t)
	cred := &Credential{ChannelID: 9203, CorpID: "wx_corp_9203", Secret: "s", AppID: "wx_not_an_agentid", BaseURL: m.srv.URL}
	_, err := BuildAgentConfig(context.Background(), cred, "https://a.com/x")
	if !errors.Is(err, ErrAgentIDMissing) {
		t.Fatalf("期望 ErrAgentIDMissing，实际 %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.agentHits != 0 || m.corpHits != 0 {
		t.Fatalf("缺 agentid 时不该先烧企微配额: corp=%d agent=%d", m.corpHits, m.agentHits)
	}
}

// TestBuildAgentConfigEmptyURL 去片段后为空要直接拒（侧边栏传 location.hash 时会命中）。
func TestBuildAgentConfigEmptyURL(t *testing.T) {
	cred := &Credential{ChannelID: 9204, AgentID: "1"}
	if _, err := BuildAgentConfig(context.Background(), cred, "  "); err == nil {
		t.Fatal("空 url 期望报错，实际 nil")
	}
	if _, err := BuildAgentConfig(context.Background(), cred, "#only-hash"); err == nil {
		t.Fatal("只有 hash 的 url 期望报错，实际 nil")
	}
}

// TestFetchAgentConfigTicketFailureNotCached 换票失败不得写缓存——否则这一次企微抖动
// 会在整个 TTL 里把侧边栏钉死在"配置失败"上。
func TestFetchAgentConfigTicketFailureNotCached(t *testing.T) {
	m := newAgentMock(t)
	m.failAgent = true
	cred := &Credential{ChannelID: 9205, CorpID: "wx_corp_9205", Secret: "s", AgentID: "1000002", BaseURL: m.srv.URL}
	if _, err := BuildAgentConfig(context.Background(), cred, "https://a.com/x"); err == nil {
		t.Fatal("换票失败期望报错，实际 nil")
	} else if !strings.Contains(err.Error(), "agent_config") {
		t.Fatalf("错误应指明是 agent_config 贴纸拉取失败，实际: %v", err)
	}
	m.mu.Lock()
	m.failAgent = false
	m.mu.Unlock()
	if _, err := BuildAgentConfig(context.Background(), cred, "https://a.com/x"); err != nil {
		t.Fatalf("企微恢复后仍失败（失败被缓存了）: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.agentHits != 2 {
		t.Fatalf("失败后应重新拉票，命中 %d 次（期望 2）", m.agentHits)
	}
}
