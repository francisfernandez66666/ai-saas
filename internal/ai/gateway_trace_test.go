// E3 网关出站 trace 头单测（2026-09-19）：traceparent 格式/确定性 + 无 trace 零行为差。
package ai

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"ai-scrm/internal/logx"
)

func TestTraceparentFor(t *testing.T) {
	re := regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-01$`)
	h := traceparentFor("a1b2c3d4e5f60718")
	m := re.FindStringSubmatch(h)
	if m == nil {
		t.Fatalf("traceparent 非法 W3C 格式: %q", h)
	}
	sum := sha256.Sum256([]byte("a1b2c3d4e5f60718"))
	want := hexOf(sum[:16])
	if m[1] != want {
		t.Fatalf("trace-id 映射应为 sha256 前 16 字节: got %s want %s", m[1], want)
	}
	// 同 trace 两次调用：trace-id 段稳定，span 段随机
	h2 := traceparentFor("a1b2c3d4e5f60718")
	m2 := re.FindStringSubmatch(h2)
	if m2 == nil || m2[1] != m[1] {
		t.Fatalf("trace-id 段须确定性: %s vs %s", h, h2)
	}
	if m2[2] == m[2] {
		t.Fatalf("span-id 段应随机（连撞两次概率可忽略）: %s", h2)
	}
	if traceparentFor("other")[:35] == h[:35] {
		t.Fatal("不同 trace 应映射不同 trace-id")
	}
}

func TestGatewayClientTraceHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
		})
	}))
	defer srv.Close()
	g := &GatewayClient{BaseURL: srv.URL, http: &http.Client{Timeout: 5 * time.Second}}

	// 无 trace：两个头都不出现（零行为差）
	if _, _, err := g.GenerateTextWithUsage(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}}, 0.7, 0, "reply"); err != nil {
		t.Fatal(err)
	}
	if got.Get("X-Trace-ID") != "" || got.Get("traceparent") != "" {
		t.Fatalf("无 trace 不应出头: %v", got)
	}

	// 有 trace：原值进 X-Trace-ID，映射值进 traceparent
	ctx := logx.ContextWithTrace(context.Background(), "cafe0123456789ab")
	if _, _, err := g.GenerateTextWithUsage(ctx, []ChatMessage{{Role: "user", Content: "hi"}}, 0.7, 0, "reply"); err != nil {
		t.Fatal(err)
	}
	if got.Get("X-Trace-ID") != "cafe0123456789ab" {
		t.Fatalf("X-Trace-ID 应为原值: %q", got.Get("X-Trace-ID"))
	}
	if !regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`).MatchString(got.Get("traceparent")) {
		t.Fatalf("traceparent 非法: %q", got.Get("traceparent"))
	}
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0xf])
	}
	return string(out)
}
