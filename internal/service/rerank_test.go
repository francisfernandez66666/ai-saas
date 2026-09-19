// E9 KB 检索重排单测：fail-open 纪律（未启用/开关关/MockMode/调用失败一律原序）、
// 仅改前 N 候选顺序不改召回集合、远端少返回补漏、HTTP 协议实现（httptest 零真实 Key）。
package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-scrm/config"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
)

// fakeRerankClient 可控重排桩：返回预设 (index,score) 或错误，并记录入参
type fakeRerankClient struct {
	items   []RerankItem
	err     error
	gotQ    string
	gotDocs []string
	calls   int
}

func (f *fakeRerankClient) Rerank(query string, docs []string) ([]RerankItem, error) {
	f.calls++
	f.gotQ = query
	f.gotDocs = docs
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

// withRerankEnv 临时装配全局客户端+热开关，测试结束恢复
func withRerankEnv(t *testing.T, client RerankClient, cfg map[string]string) {
	t.Helper()
	oldClient := DefaultRerankClient
	oldSvc := runtimecfg.DefaultSystemConfigService
	DefaultRerankClient = client
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(cfg, nil)
	t.Cleanup(func() {
		DefaultRerankClient = oldClient
		runtimecfg.DefaultSystemConfigService = oldSvc
	})
}

// fragsOf 按 ID 序列构造片段桩（Title 与 ID 对应便于断言顺序）
func fragsOf(ids ...uint) []model.KnowledgeFragment {
	out := make([]model.KnowledgeFragment, 0, len(ids))
	for _, id := range ids {
		out = append(out, model.KnowledgeFragment{ID: id, Title: fmt.Sprintf("T%d", id)})
	}
	return out
}

// idsOf 提取片段序列的 ID 顺序，用于顺序断言
func idsOf(frags []model.KnowledgeFragment) []uint {
	out := make([]uint, 0, len(frags))
	for _, f := range frags {
		out = append(out, f.ID)
	}
	return out
}

// eqIDs 逐位比较两个 ID 序列（顺序敏感）
func eqIDs(a, b []uint) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// 未启用客户端：原序返回（零行为差）
func TestApplyRerankNilClientKeepsOrder(t *testing.T) {
	withRerankEnv(t, nil, map[string]string{"kb_rerank": "true"})
	in := fragsOf(1, 2, 3)
	got := applyRerank("q", in)
	if !eqIDs(idsOf(got), []uint{1, 2, 3}) {
		t.Fatalf("未启用客户端应原序，得 %v", idsOf(got))
	}
}

// 配了客户端但热开关默认关：原序
func TestApplyRerankSwitchOffKeepsOrder(t *testing.T) {
	fake := &fakeRerankClient{items: []RerankItem{{Index: 2, Score: 1}}}
	withRerankEnv(t, fake, map[string]string{}) // 键缺失=默认关
	got := applyRerank("q", fragsOf(1, 2, 3))
	if !eqIDs(idsOf(got), []uint{1, 2, 3}) || fake.calls != 0 {
		t.Fatalf("开关关应原序且不发请求，得 %v calls=%d", idsOf(got), fake.calls)
	}
}

// MockMode 短路：测试/模拟环境不打真实端点
func TestApplyRerankMockModeShortCircuits(t *testing.T) {
	old := config.GlobalConfig
	config.GlobalConfig = &config.Config{AI: config.AIConfig{MockMode: true}}
	defer func() { config.GlobalConfig = old }()
	fake := &fakeRerankClient{items: []RerankItem{{Index: 2, Score: 1}}}
	withRerankEnv(t, fake, map[string]string{"kb_rerank": "true"})
	got := applyRerank("q", fragsOf(1, 2, 3))
	if !eqIDs(idsOf(got), []uint{1, 2, 3}) || fake.calls != 0 {
		t.Fatalf("MockMode 应短路原序，得 %v calls=%d", idsOf(got), fake.calls)
	}
}

// 正常重排：只改前 N 内部顺序，集合不变；尾部候选数之外不动
func TestApplyRerankReordersCandidatesOnly(t *testing.T) {
	// 候选 2：仅前两条参与重排（2↔1 互换），3/4 保持原位
	fake := &fakeRerankClient{items: []RerankItem{{Index: 1, Score: 0.9}, {Index: 0, Score: 0.1}}}
	withRerankEnv(t, fake, map[string]string{"kb_rerank": "true", "kb_rerank_candidates": "2"})
	got := applyRerank("查询词", fragsOf(1, 2, 3, 4))
	if !eqIDs(idsOf(got), []uint{2, 1, 3, 4}) {
		t.Fatalf("应仅重排前2候选，得 %v", idsOf(got))
	}
	if fake.gotQ != "查询词" || len(fake.gotDocs) != 2 {
		t.Fatalf("入参应为 query+前2条 docs，得 q=%q docs=%d", fake.gotQ, len(fake.gotDocs))
	}
}

// 调用失败：fail-open 原序
func TestApplyRerankErrorFailsOpen(t *testing.T) {
	fake := &fakeRerankClient{err: fmt.Errorf("boom")}
	withRerankEnv(t, fake, map[string]string{"kb_rerank": "true"})
	got := applyRerank("q", fragsOf(1, 2, 3))
	if !eqIDs(idsOf(got), []uint{1, 2, 3}) {
		t.Fatalf("远端失败应回退原序，得 %v", idsOf(got))
	}
}

// 远端少返回（自作主张截断）：漏掉的候选按原序补尾，绝不丢片段
func TestApplyRerankRefillsMissingCandidates(t *testing.T) {
	fake := &fakeRerankClient{items: []RerankItem{{Index: 2, Score: 0.9}}} // 只回 1 条，cand=3
	withRerankEnv(t, fake, map[string]string{"kb_rerank": "true", "kb_rerank_candidates": "3"})
	got := applyRerank("q", fragsOf(1, 2, 3, 4))
	// 期望：3（命中）→ 1,2（漏选按原序补）→ 4（候选外尾部不动）
	if !eqIDs(idsOf(got), []uint{3, 1, 2, 4}) {
		t.Fatalf("少返回应补漏不丢片段，得 %v", idsOf(got))
	}
}

// 候选数大于结果集：按实际条数重排，不越界
func TestApplyRerankCandExceedsLen(t *testing.T) {
	fake := &fakeRerankClient{items: []RerankItem{{Index: 1, Score: 1}, {Index: 0, Score: 0}}}
	withRerankEnv(t, fake, map[string]string{"kb_rerank": "true"})
	got := applyRerank("q", fragsOf(1, 2))
	if !eqIDs(idsOf(got), []uint{2, 1}) {
		t.Fatalf("2 条候选应完整重排，得 %v", idsOf(got))
	}
	if len(fake.gotDocs) != 2 {
		t.Fatalf("docs 应为 2 条，得 %d", len(fake.gotDocs))
	}
}

// 单候选（<2）无意义，直接原序
func TestApplyRerankSingleFragmentSkipped(t *testing.T) {
	fake := &fakeRerankClient{}
	withRerankEnv(t, fake, map[string]string{"kb_rerank": "true"})
	got := applyRerank("q", fragsOf(1))
	if !eqIDs(idsOf(got), []uint{1}) || fake.calls != 0 {
		t.Fatalf("单候选应跳过重排，得 %v calls=%d", idsOf(got), fake.calls)
	}
}

// 候选钳制：过小→2，过大→32
func TestKbRerankCandidatesClamp(t *testing.T) {
	old := runtimecfg.DefaultSystemConfigService
	defer func() { runtimecfg.DefaultSystemConfigService = old }()
	for _, tc := range []struct {
		val  string
		want int
	}{
		{"1", 2}, {"2", 2}, {"8", 8}, {"100", 32}, {"abc", 8},
	} {
		runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(
			map[string]string{"kb_rerank_candidates": tc.val}, nil)
		if got := kbRerankCandidates(); got != tc.want {
			t.Errorf("kb_rerank_candidates=%q 期望 %d 得 %d", tc.val, tc.want, got)
		}
	}
}

// HTTP 客户端正常路径：请求体协议 + Authorization 头 + 响应解析
func TestHttpRerankClientSuccess(t *testing.T) {
	var gotBody rerankRequest
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"results":[{"index":1,"relevance_score":0.9},{"index":0,"relevance_score":0.2}]}`)
	}))
	defer srv.Close()
	c := &httpRerankClient{url: srv.URL, token: "sk-test", model: "test-model", http: srv.Client()}
	items, err := c.Rerank("q", []string{"docA", "docB"})
	if err != nil {
		t.Fatalf("正常调用不应报错: %v", err)
	}
	if len(items) != 2 || items[0].Index != 1 || items[0].Score != 0.9 {
		t.Fatalf("响应解析错误: %+v", items)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("鉴权头错误: %q", gotAuth)
	}
	if gotBody.Model != "test-model" || gotBody.Query != "q" || gotBody.TopN != 2 || len(gotBody.Documents) != 2 {
		t.Errorf("请求体协议错误: %+v", gotBody)
	}
}

// HTTP 客户端异常路径：非200 / 空结果 / 下标越界 均报错（由 applyRerank fail-open）
func TestHttpRerankClientErrors(t *testing.T) {
	t.Run("非200", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":{"message":"boom"}}`)
		}))
		defer srv.Close()
		c := &httpRerankClient{url: srv.URL, model: "m", http: srv.Client()}
		if _, err := c.Rerank("q", []string{"a"}); err == nil {
			t.Fatal("非200应报错")
		}
	})
	t.Run("空结果", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, `{"results":[]}`)
		}))
		defer srv.Close()
		c := &httpRerankClient{url: srv.URL, model: "m", http: srv.Client()}
		if _, err := c.Rerank("q", []string{"a"}); err == nil {
			t.Fatal("空结果应报错")
		}
	})
	t.Run("下标越界", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, `{"results":[{"index":7,"relevance_score":0.5}]}`)
		}))
		defer srv.Close()
		c := &httpRerankClient{url: srv.URL, model: "m", http: srv.Client()}
		if _, err := c.Rerank("q", []string{"a", "b"}); err == nil {
			t.Fatal("越界下标应整批作废报错")
		}
	})
}

// 装配逻辑：URL 空=不启用；Key 空=复用 EmbeddingKey
func TestInitRerankClientWiring(t *testing.T) {
	old := config.GlobalConfig
	defer func() { config.GlobalConfig = old }()

	config.GlobalConfig = &config.Config{}
	InitRerankClient()
	if DefaultRerankClient != nil {
		t.Fatal("RERANK_API_URL 为空应不启用")
	}

	config.GlobalConfig = &config.Config{AI: config.AIConfig{
		EmbeddingKey: "shared-key",
		RerankURL:    "https://example.invalid/v1/rerank",
		RerankModel:  "m",
	}}
	InitRerankClient()
	c, ok := DefaultRerankClient.(*httpRerankClient)
	if !ok {
		t.Fatalf("配置 URL 后应装配 HTTP 客户端，得 %T", DefaultRerankClient)
	}
	if c.token != "shared-key" {
		t.Errorf("RERANK_API_KEY 为空应复用 EmbeddingKey，得 %q", c.token)
	}

	config.GlobalConfig = &config.Config{AI: config.AIConfig{
		EmbeddingKey: "shared-key",
		RerankKey:    "own-key",
		RerankURL:    "https://example.invalid/v1/rerank",
	}}
	InitRerankClient()
	c, _ = DefaultRerankClient.(*httpRerankClient)
	if c.token != "own-key" {
		t.Errorf("显式 RERANK_API_KEY 应优先，得 %q", c.token)
	}
	DefaultRerankClient = nil
}
