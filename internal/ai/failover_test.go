// AI 供应商降级链故障注入演练（2026-09-05 补齐测试欠账 P0）
// 覆盖三分支：429 限流 / 超时 / 余额耗尽(4xx不重试)，以及路由器级降级语义：
// 主挂自动降备、全挂返回错误、冷却模型跳过、成功重置失败计数。
// 全部走 httptest / callOverride 注入，不依赖真实厂商 Key。
package ai

import (
	"ai-scrm/config"
	"context"
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
	_, _, err := c.GenerateTextWithUsage(context.Background(), []ChatMessage{{Role: "user", Content: "你好"}}, 0.5)
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
	_, _, err := c.GenerateTextWithUsage(context.Background(), []ChatMessage{{Role: "user", Content: "你好"}}, 0.5)
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
	_, _, err := c.GenerateTextWithUsage(context.Background(), []ChatMessage{{Role: "user", Content: "你好"}}, 0.5)
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
	reply, usage, err := c.GenerateTextWithUsage(context.Background(), []ChatMessage{{Role: "user", Content: "有四驱吗"}}, 0.5)
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
	r.callOverride = func(_ context.Context, provider ModelProvider, model string, _ []ChatMessage, _ float64, _ uint, _ string) (string, Usage, error) {
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
	reply, _, model, usage, err := r.GenerateTextForStage(context.Background(), "reply", 1, routerMessages(), 0.5)
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

// D4 用例一：首个模型永不返回（在 callOverride 里死等 ctx）→ 应在剩余预算内被取消并降级到第二个模型，
// 总耗时受 budget 约束而非单模型 client 超时。验收"单模型挂死不吃满全链"。
func TestRouterBudgetCancelsHangingModel(t *testing.T) {
	var calls []string
	r := &AIRouter{coolDownSec: 300, budget: 1200 * time.Millisecond}
	r.models = []*ModelState{
		{Provider: ProviderSiliconFlow, ModelName: "挂死模型", Available: true},
		{Provider: ProviderSiliconFlow, ModelName: "健康模型", Available: true},
	}
	r.callOverride = func(ctx context.Context, _ ModelProvider, model string, _ []ChatMessage, _ float64, _ uint, _ string) (string, Usage, error) {
		calls = append(calls, model)
		if model == "挂死模型" {
			<-ctx.Done() // 永不主动返回，靠上游预算取消
			return "", Usage{}, ctx.Err()
		}
		return "预算内被顶上", Usage{}, nil
	}
	start := time.Now()
	reply, _, model, _, err := r.GenerateTextWithUsage(routerMessages(), 0.5, 1, "reply")
	elapsed := time.Since(start)
	if err != nil || reply != "预算内被顶上" || model != "健康模型" {
		t.Fatalf("挂死后应降级到健康模型，reply=%q model=%q err=%v", reply, model, err)
	}
	if len(calls) != 2 || calls[0] != "挂死模型" || calls[1] != "健康模型" {
		t.Fatalf("应先挂死模型再健康模型，实际 %v", calls)
	}
	// 首个模型用光 ~1.2s 预算被取消后第二个立即成功；应远小于"两个模型各 120s"级别
	if elapsed > 3*time.Second {
		t.Fatalf("总耗时应≈budget量级(≤3s)，实际 %v", elapsed)
	}
}

// D4 用例二：HTTP 层 ctx 取消——单模型 client.Timeout 故意设很长（10s），但上游只给 200ms 预算，
// 挂死的 httptest handler 应在 200ms 内被 NewRequestWithContext 取消返回，而非卡满 10s。
func TestSFContextCancelsHangingHTTPRequest(t *testing.T) {
	srvDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 永不写响应；测试结束 close(srvDone) 放行，避免 srv.Close() 死等
		select {
		case <-r.Context().Done():
		case <-srvDone:
		}
	}))
	defer func() { close(srvDone); srv.Close() }()

	c := newSFTestClient(srv.URL, 0, 10*time.Second) // client 超时远超 ctx 预算
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := c.GenerateTextWithUsage(ctx, []ChatMessage{{Role: "user", Content: "你好"}}, 0.5)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("ctx 到期应返回错误")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("应在 ctx 预算内中断（证明 ctx 生效），实际耗时 %v", elapsed)
	}
}

// TestRouterDeepSeekThirdProviderTakeover P1-5→批三(2026-09-20)验收：
// 硅基流动主+备源同平台全故障（500/429）时，DeepSeek 官网直连第三路必须接管——
// 旧降级链两环同挂单一平台，平台整体故障=全链同灭只剩模板兜底。
func TestRouterDeepSeekThirdProviderTakeover(t *testing.T) {
	r := &AIRouter{coolDownSec: 300}
	r.models = []*ModelState{
		{Provider: ProviderSiliconFlow, ModelName: "sf_main", Available: true},
		{Provider: ProviderSiliconFlow, ModelName: "sf_backup", Available: true},
		{Provider: ProviderDeepSeek, ModelName: "deepseek-chat", Available: true},
	}
	var calls []string
	r.callOverride = func(_ context.Context, p ModelProvider, model string, _ []ChatMessage, _ float64, _ uint, _ string) (string, Usage, error) {
		calls = append(calls, string(p)+":"+model)
		if p == ProviderDeepSeek {
			return "第三路顶上", Usage{TotalTokens: 9}, nil
		}
		return "", Usage{}, fmt.Errorf("API返回错误: status=500")
	}
	reply, prov, model, usage, err := r.GenerateTextForStage(context.Background(), "reply", 1, routerMessages(), 0.5)
	if err != nil || reply != "第三路顶上" || model != "deepseek-chat" {
		t.Fatalf("双硅基挂后第三路应接管成功，reply=%q model=%q err=%v", reply, model, err)
	}
	if prov != string(ProviderDeepSeek) {
		t.Fatalf("返回 provider 应为 deepseek，实际 %q", prov)
	}
	if len(calls) != 3 || usage.TotalTokens != 9 {
		t.Fatalf("应依次试遍 sf主/sf备/deepseek 且透传用量，calls=%v usage=%+v", calls, usage)
	}
}

// TestInitRouterAssemblesDeepSeek InitRouter 装配门：配 DEEPSEEK_API_KEY 才挂第三路，
// 未配置维持原两环链（零行为变更）。
func TestInitRouterAssemblesDeepSeek(t *testing.T) {
	origCfg := config.GlobalConfig
	origSF, origDS, origRouter := SiliconFlowDefaultClient, DeepSeekDefaultClient, Router
	// GlobalConfig 是指针：测试期整只换新实例（浅拷贝原值），结束后还原指针，避免污染其它用例
	base := config.Config{}
	if origCfg != nil {
		base = *origCfg
	}
	cfg := base
	config.GlobalConfig = &cfg
	defer func() {
		config.GlobalConfig = origCfg
		SiliconFlowDefaultClient, DeepSeekDefaultClient, Router = origSF, origDS, origRouter
	}()

	config.GlobalConfig.AI.SiliconFlow = config.SiliconFlowConfig{APIKey: "sk-sf", BaseURL: "https://sf.test/v1", Model: "glm", ModelBackup: "ds-flash", MaxTokens: 512, Temperature: 0.7}
	config.GlobalConfig.AI.DeepSeek = config.DeepSeekConfig{APIKey: "sk-ds", BaseURL: "https://ds.test", Model: "deepseek-chat", MaxTokens: 512, Temperature: 0.7}
	InitSiliconFlowClient()
	InitDeepSeekClient()
	InitRouter()
	var sawDS bool
	for i, m := range Router.models {
		if m.Provider == ProviderDeepSeek {
			sawDS = true
			if m.ModelName != "deepseek-chat" {
				t.Fatalf("第三路模型名应为配置值，实际 %q", m.ModelName)
			}
			if i != len(Router.models)-1 {
				t.Fatal("第三路应排在降级链末端（前序供应商优先）")
			}
		}
	}
	if !sawDS {
		t.Fatalf("配置 DEEPSEEK_API_KEY 后应装配第三路，实际链：%+v", Router.models)
	}

	// 无 Key → 不装配（第三路对存量部署零影响）
	config.GlobalConfig.AI.DeepSeek.APIKey = ""
	InitDeepSeekClient()
	InitRouter()
	for _, m := range Router.models {
		if m.Provider == ProviderDeepSeek {
			t.Fatal("未配 DEEPSEEK_API_KEY 不得装配第三路")
		}
	}
}
