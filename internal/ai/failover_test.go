// AI 供应商降级链故障注入演练（2026-09-05 补齐测试欠账 P0）
// 覆盖三分支：429 限流 / 超时 / 余额耗尽(4xx不重试)，以及路由器级降级语义：
// 主挂自动降备、全挂返回错误、冷却模型跳过、成功重置失败计数。
// 全部走 httptest / callOverride 注入，不依赖真实厂商 Key。
package ai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newSFTestClient 构造指向测试服务器的硅基流动客户端（同包可注入非导出字段）
func newSFTestClient(url string, retries int, timeout time.Duration) *SiliconFlowClient {
	return &SiliconFlowClient{
		APIKey:      "sk-test",
		BaseURL:     url,
		ModelName:   "test-model",
		MaxTokens:   100,
		Temperature: 0.5,
		MaxRetries:  retries,
		Enabled:     true,
		httpClient:  &http.Client{Timeout: timeout},
	}
}

// okBody 构造 OpenAI 兼容成功响应
func okBody(content string) string {
	resp := map[string]interface{}{
		"id":    "cmpl-1",
		"model": "test-model",
		"choices": []map[string]interface{}{
			{"index": 0, "finish_reason": "stop", "message": map[string]string{"role": "assistant", "content": content}},
		},
		"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

// 分支一：429 限流 → 快速失败并带可识别错误（供路由器降级判定）
func TestSFFailover429FastFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"TPM limit reached"}}`)
	}))
	defer srv.Close()

	c := newSFTestClient(srv.URL, 0, 5*time.Second)
	_, _, err := c.GenerateTextWithUsage([]ChatMessage{{Role: "user", Content: "你好"}}, 0.5)
	if err == nil {
		t.Fatal("429 应返回错误")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Fatalf("错误应含状态码429，实际: %v", err)
	}
	if !isSiliconFlowRateLimitError(err) {
		t.Fatalf("429 应被识别为限流错误(退避翻倍依据)，实际: %v", err)
	}
}

// 分支二：超时 → 客户端超时中断，错误可识别（供路由器降级判定）
func TestSFFailoverTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		fmt.Fprint(w, okBody("迟到的回复"))
	}))
	defer srv.Close()

	c := newSFTestClient(srv.URL, 0, 50*time.Millisecond)
	start := time.Now()
	_, _, err := c.GenerateTextWithUsage([]ChatMessage{{Role: "user", Content: "你好"}}, 0.5)
	if err == nil {
		t.Fatal("超时应返回错误")
	}
	if !strings.Contains(err.Error(), "请求API失败") {
		t.Fatalf("错误应为请求失败口径，实际: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("应在客户端超时窗内中断，实际耗时 %v", time.Since(start))
	}
}

// 分支三：余额耗尽(4xx 参数/账户类) → 不重试直接降级（重试浪费时间且无意义）
func TestSFFailoverBalanceExhaustedNoRetry(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"message":"余额不足，请充值"}}`)
	}))
	defer srv.Close()

	// MaxRetries=1：若是 4xx 账户错误应立即跳出，只发 1 次请求
	c := newSFTestClient(srv.URL, 1, 5*time.Second)
	_, _, err := c.GenerateTextWithUsage([]ChatMessage{{Role: "user", Content: "你好"}}, 0.5)
	if err == nil {
		t.Fatal("余额耗尽应返回错误")
	}
	if !isSiliconFlowClientError(err) {
		t.Fatalf("4xx 应被识别为参数/账户类错误(不重试)，实际: %v", err)
	}
	if hits != 1 {
		t.Fatalf("4xx 应零重试（服务器应只收到1次请求），实际 %d 次", hits)
	}
}

// 正常路径：200 → 回复与 token 用量正确解析（usage_ledger 口径的前提）
func TestSFSuccessParsesUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, okBody("这款车有四驱。"))
	}))
	defer srv.Close()

	c := newSFTestClient(srv.URL, 0, 5*time.Second)
	reply, usage, err := c.GenerateTextWithUsage([]ChatMessage{{Role: "user", Content: "有四驱吗"}}, 0.5)
	if err != nil || reply != "这款车有四驱。" {
		t.Fatalf("应成功返回话术，reply=%q err=%v", reply, err)
	}
	if usage.TotalTokens != 15 {
		t.Fatalf("token 用量应解析为15，实际 %+v", usage)
	}
}

// ============================================================
// 路由器级：降级 / 冷却 / 重置 / 全链失败
// ============================================================

// newTestRouter 构造带注入调用的路由器（callOverride 模拟各厂商成败）
func newTestRouter(names []string, call func(attempt int, provider ModelProvider, model string) (string, Usage, error)) (*AIRouter, *[]string) {
	var calls []string
	r := &AIRouter{coolDownSec: 300}
	for _, n := range names {
		r.models = append(r.models, &ModelState{Provider: ProviderSiliconFlow, ModelName: n, Available: true})
	}
	r.callOverride = func(provider ModelProvider, model string, _ []ChatMessage, _ float64, _ uint, _ string) (string, Usage, error) {
		calls = append(calls, model)
		return call(len(calls), provider, model)
	}
	return r, &calls
}

// routerMessages 构造测试用消息样本（角色=user，固定中文问候，供路由器降级用例复用）
func routerMessages() []ChatMessage {
	return []ChatMessage{{Role: "user", Content: "你好"}}
}

// 主模型 429 → 自动降级到备用模型成功，且主模型记录一次失败
func TestRouterDegradesOnPrimary429(t *testing.T) {
	r, calls := newTestRouter([]string{"主模型", "备用模型"}, func(_ int, _ ModelProvider, model string) (string, Usage, error) {
		if model == "主模型" {
			return "", Usage{}, fmt.Errorf("API返回错误: status=429, body=rate limit")
		}
		return "备用答上了", Usage{TotalTokens: 7}, nil
	})
	reply, _, model, usage, err := r.GenerateTextForStage("reply", 1, routerMessages(), 0.5)
	if err != nil || reply != "备用答上了" || model != "备用模型" {
		t.Fatalf("应降级到备用模型成功，reply=%q model=%q err=%v", reply, model, err)
	}
	if len(*calls) != 2 {
		t.Fatalf("应尝试主+备两次，实际 %v", *calls)
	}
	if usage.TotalTokens != 7 {
		t.Fatalf("应透传备用的 token 用量，实际 %+v", usage)
	}
	if r.models[0].ConsecutiveFails != 1 {
		t.Fatalf("主模型应记 1 次连续失败，实际 %d", r.models[0].ConsecutiveFails)
	}
	if r.models[1].ConsecutiveFails != 0 || !r.models[1].Available {
		t.Fatal("备用模型成功后应保持可用且计数归零")
	}
}

// 全链失败 → 返回最后一个错误（上层据此走模板兜底话术）
func TestRouterAllFailReturnsError(t *testing.T) {
	r, calls := newTestRouter([]string{"主模型", "备用模型"}, func(_ int, _ ModelProvider, model string) (string, Usage, error) {
		return "", Usage{}, fmt.Errorf("API返回错误: status=429")
	})
	_, _, model, _, err := r.GenerateTextWithUsage(routerMessages(), 0.5, 1, "reply")
	if err == nil || model != "" {
		t.Fatalf("全挂应返回错误且无模型名，model=%q err=%v", model, err)
	}
	if len(*calls) != 2 {
		t.Fatalf("应把链上模型全部试一遍，实际 %v", *calls)
	}
}

// 连续失败≥3 且冷却中 → 该模型被跳过，不再浪费时间（熔断生效）
func TestRouterSkipsCoolingModel(t *testing.T) {
	r, calls := newTestRouter([]string{"主模型", "备用模型"}, func(_ int, _ ModelProvider, model string) (string, Usage, error) {
		if model != "备用模型" {
			t.Fatalf("冷却中的主模型不应被调用")
		}
		return "冷却期由备用顶上", Usage{}, nil
	})
	// 预置主模型熔断状态：连续失败3次 + 刚失败（冷却窗内）
	r.models[0].ConsecutiveFails = 3
	r.models[0].LastFailTime = time.Now()

	if _, _, _, _, err := r.GenerateTextWithUsage(routerMessages(), 0.5, 1, "reply"); err != nil {
		t.Fatalf("generate err: %v", err)
	}
	if len(*calls) != 1 || (*calls)[0] != "备用模型" {
		t.Fatalf("应只调用备用模型，实际 %v", *calls)
	}
}

// 失败后恢复成功 → 计数重置、冷却清除（"每次都从主模型再试"的设计前提）
func TestRouterSuccessResetsFailCounter(t *testing.T) {
	r, _ := newTestRouter([]string{"主模型"}, func(_ int, _ ModelProvider, _ string) (string, Usage, error) {
		return "恢复了", Usage{}, nil
	})
	r.models[0].ConsecutiveFails = 2
	r.models[0].LastFailTime = time.Now().Add(-10 * time.Minute) // 冷却已过

	if _, _, _, _, err := r.GenerateTextWithUsage(routerMessages(), 0.5, 1, "reply"); err != nil {
		t.Fatal(err.Error())
	}
	if r.models[0].ConsecutiveFails != 0 || !r.models[0].LastFailTime.IsZero() {
		t.Fatalf("成功后应重置计数与冷却，实际 fails=%d", r.models[0].ConsecutiveFails)
	}
}
