// 阶段模型覆盖（stage_models）真实回归（2026-09-24 欠账批一，G-8）
//
// 为什么单独测这条：`stage_models` 是唯一"运营能在后台改、改完直接影响线上走哪条 AI 通道"
// 的配置，而 P1-28（2026-09-09）修的正是一个**静默失效**缺陷——运营给主力模型
// GLM-4-9B 配了阶段覆盖，旧推断逻辑（模型名含 "glm" 就当智谱）把它路由到智谱客户端，
// 拿硅基的模型名调智谱接口 → 401 → 静默回退降级链。表现是"配了没反应、也不报错"，
// 这类缺陷只有断言**实际发出去的请求体里的 model 字段**才测得出来。
//
// 本文件两条腿：
//   - ResolveStageModel：解析层六种配置形态（含脏 JSON / 空 stage / nil 单例）
//   - GenerateTextForStage：接线层——覆盖真的被用出去了（正向），覆盖挂了会回退降级链（反向）
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"ai-scrm/internal/runtimecfg"
)

// withStageModels 把 stage_models 热配临时换成给定 JSON（nil=不装该键），返回恢复函数
func withStageModels(t *testing.T, jsonVal string) func() {
	t.Helper()
	kv := map[string]string{}
	if jsonVal != "" {
		kv["stage_models"] = jsonVal
	}
	return runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(kv, nil))
}

// TestResolveStageModelForms 解析层：哪些配置形态算"生效的覆盖"，哪些必须回退全局链。
// 逐格列表而非只测 happy path——"脏 JSON 视为未配置"是 fail-open 口径，
// 一旦有人改成返回空 model 名，AI 会拿空模型名去请求（400），比回退降级链糟得多。
func TestResolveStageModelForms(t *testing.T) {
	cases := []struct {
		name       string
		stage      string
		json       string // 空串=不装 stage_models 键
		nilService bool   // true=把全局配置单例置 nil（测试启动前的形态）
		wantOK     bool
		wantProv   string
		wantModel  string
	}{
		{
			name: "nil配置单例_回退", stage: "reply", nilService: true, wantOK: false,
		},
		{
			name: "空stage_回退", stage: "", json: `{"reply":{"model":"m1"}}`, wantOK: false,
		},
		{
			name: "键缺失_回退", stage: "reply", json: "", wantOK: false,
		},
		{
			name: "脏JSON_回退不panic", stage: "reply", json: `{"reply":` /* 截断 */, wantOK: false,
		},
		{
			name: "该阶段未配_回退", stage: "intent", json: `{"reply":{"model":"m1"}}`, wantOK: false,
		},
		{
			name: "model留空_回退", stage: "reply", json: `{"reply":{"model":"","provider":"siliconflow"}}`, wantOK: false,
		},
		{
			// P1-28 口径本体：provider 留空一律按主力通道 siliconflow 处理，
			// **不再按模型名里有没有 "glm" 猜智谱**——运营配 "GLM-4-9B-0414" 必须留在硅基。
			name: "只配model_默认硅基", stage: "reply",
			json:   `{"reply":{"model":"THUDM/GLM-4-9B-0414"}}`,
			wantOK: true, wantProv: string(ProviderSiliconFlow), wantModel: "THUDM/GLM-4-9B-0414",
		},
		{
			name: "显式zhipu才走智谱", stage: "intent",
			json:   `{"intent":{"provider":"zhipu","model":"glm-4-air"}}`,
			wantOK: true, wantProv: string(ProviderZhipu), wantModel: "glm-4-air",
		},
		{
			name: "显式gateway", stage: "strategy",
			json:   `{"strategy":{"provider":"gateway","model":"any"}}`,
			wantOK: true, wantProv: string(ProviderGateway), wantModel: "any",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.nilService {
				old := runtimecfg.DefaultSystemConfigService
				runtimecfg.DefaultSystemConfigService = nil
				defer func() { runtimecfg.DefaultSystemConfigService = old }()
			} else {
				defer withStageModels(t, tc.json)()
			}
			gotProv, gotModel, gotOK := ResolveStageModel(tc.stage)
			if gotOK != tc.wantOK {
				t.Fatalf("是否生效应为 %v，实际 %v（provider=%q model=%q）", tc.wantOK, gotOK, gotProv, gotModel)
			}
			if !tc.wantOK {
				if gotModel != "" || gotProv != "" {
					t.Errorf("未生效时不得返回半截配置，实际 provider=%q model=%q", gotProv, gotModel)
				}
				return
			}
			if gotProv != tc.wantProv || gotModel != tc.wantModel {
				t.Errorf("解析结果应为 %s/%s，实际 %s/%s", tc.wantProv, tc.wantModel, gotProv, gotModel)
			}
		})
	}
}

// recordedModels 收集测试服务器收到的请求体 model 字段（这就是"实际发了什么"的证据）。
// 加锁：handler 在独立 goroutine 里写，-race 下主线程读必须有明确的同步关系。
type recordedModels struct {
	mu     sync.Mutex
	models []string
}

func (rec *recordedModels) add(m string) {
	rec.mu.Lock()
	rec.models = append(rec.models, m)
	rec.mu.Unlock()
}

func (rec *recordedModels) all() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]string(nil), rec.models...)
}

// stageTestClient 指向假端点的硅基客户端；call 决定该次请求的响应
func stageTestClient(url string) *SiliconFlowClient {
	return &SiliconFlowClient{
		APIKey: "sk-test", BaseURL: url, ModelName: "链上默认模型",
		MaxTokens: 64, Temperature: 0.5, MaxRetries: 0, Enabled: true,
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
}

// readModelFromRequest 从 OpenAI 兼容请求体里取 model 字段
func readModelFromRequest(r *http.Request) (string, error) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return "", err
	}
	var body struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(b, &body); err != nil {
		return "", err
	}
	return body.Model, nil
}

// TestStageOverrideActuallyUsedOnWire 接线正向腿：stage_models 配了 reply 阶段专属模型时，
// 发往厂商端的**请求体 model 字段必须就是那个模型**，且回复直接来自它（不走降级链）。
//
// 只断 ResolveStageModel 的返回值是不够的：解析对了但 GenerateTextForStage 忘了用它，
// 线上表现同样是"配了没反应"——正是 P1-28 的原始故障形态。
func TestStageOverrideActuallyUsedOnWire(t *testing.T) {
	var rec recordedModels
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m, err := readModelFromRequest(r)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		rec.add(m)
		fmt.Fprint(w, okBody("覆盖模型的回复"))
	}))
	defer srv.Close()

	oldSF := SiliconFlowDefaultClient
	SiliconFlowDefaultClient = stageTestClient(srv.URL)
	defer func() { SiliconFlowDefaultClient = oldSF }()
	defer withStageModels(t, `{"reply":{"model":"覆盖模型"}}`)()

	r := &AIRouter{coolDownSec: 300, budget: 5 * time.Second}
	r.models = []*ModelState{{Provider: ProviderSiliconFlow, ModelName: "链上默认模型", Available: true}}
	// 降级链一次都不该被调用：调了即说明覆盖没生效
	r.callOverride = func(context.Context, ModelProvider, string, []ChatMessage, float64, uint, string) (string, Usage, error) {
		t.Error("覆盖模型已成功，不应再进降级链")
		return "", Usage{}, fmt.Errorf("不该被调用")
	}

	reply, prov, model, usage, err := r.GenerateTextForStage(context.Background(), "reply", 1, routerMessages(), 0.5)
	if err != nil {
		t.Fatalf("覆盖路径应直接成功: %v", err)
	}
	if reply != "覆盖模型的回复" || model != "覆盖模型" || prov != string(ProviderSiliconFlow) {
		t.Errorf("应返回覆盖模型的产出，实际 reply=%q model=%q prov=%q", reply, model, prov)
	}
	if usage.TotalTokens == 0 {
		t.Error("token 用量须透传（usage_ledger 计费口径），实际为 0")
	}
	got := rec.all()
	if len(got) != 1 || got[0] != "覆盖模型" {
		t.Fatalf("厂商端实际收到的 model 必须是[覆盖模型]，实际 %v", got)
	}
}

// TestStageOverrideFailureFallsBackToChain 接线反向腿：覆盖模型调用失败（这里是 500）时，
// 必须回退全局降级链并把请求发出去——覆盖配置不能变成"单点故障"。
// 同时钉住"覆盖那一次确实发生过"（否则回退成功只是因为根本没试）。
func TestStageOverrideFailureFallsBackToChain(t *testing.T) {
	var rec recordedModels
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m, _ := readModelFromRequest(r)
		rec.add(m)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"上游 500"}}`)
	}))
	defer srv.Close()

	oldSF := SiliconFlowDefaultClient
	SiliconFlowDefaultClient = stageTestClient(srv.URL)
	defer func() { SiliconFlowDefaultClient = oldSF }()
	defer withStageModels(t, `{"reply":{"model":"覆盖模型"}}`)()

	r := &AIRouter{coolDownSec: 300, budget: 5 * time.Second}
	r.models = []*ModelState{{Provider: ProviderSiliconFlow, ModelName: "链上默认模型", Available: true}}
	r.callOverride = func(_ context.Context, _ ModelProvider, model string, _ []ChatMessage, _ float64, _ uint, _ string) (string, Usage, error) {
		return "降级链顶上", Usage{TotalTokens: 3}, nil
	}

	reply, _, model, usage, err := r.GenerateTextForStage(context.Background(), "reply", 1, routerMessages(), 0.5)
	if err != nil || reply != "降级链顶上" || model != "链上默认模型" {
		t.Fatalf("覆盖失败应回退降级链成功，reply=%q model=%q err=%v", reply, model, err)
	}
	if usage.TotalTokens != 3 {
		t.Errorf("应透传降级链的用量，实际 %+v", usage)
	}
	got := rec.all()
	if len(got) != 1 || got[0] != "覆盖模型" {
		t.Fatalf("覆盖模型应被真实尝试一次（证明失败是真失败），实际请求 %v", got)
	}
}

// TestStageOverrideUnknownProviderReturnsError provider 配了链外值（如手滑写 "openai"）时，
// callProvider 必须报错并让降级链兜住，而不是 panic 或返回空回复当成成功。
func TestStageOverrideUnknownProviderReturnsError(t *testing.T) {
	oldSF := SiliconFlowDefaultClient
	SiliconFlowDefaultClient = stageTestClient("http://127.0.0.1:1") // 不该被调用
	defer func() { SiliconFlowDefaultClient = oldSF }()
	defer withStageModels(t, `{"reply":{"provider":"openai","model":"gpt-x"}}`)()

	r := &AIRouter{coolDownSec: 300, budget: 5 * time.Second}
	r.models = []*ModelState{{Provider: ProviderSiliconFlow, ModelName: "链上默认模型", Available: true}}
	var chainCalled bool
	r.callOverride = func(_ context.Context, _ ModelProvider, _ string, _ []ChatMessage, _ float64, _ uint, _ string) (string, Usage, error) {
		chainCalled = true
		return "未知provider时由链兜底", Usage{}, nil
	}
	reply, _, model, _, err := r.GenerateTextForStage(context.Background(), "reply", 1, routerMessages(), 0.5)
	if err != nil || reply != "未知provider时由链兜底" {
		t.Fatalf("未知 provider 应回退降级链，reply=%q err=%v", reply, err)
	}
	if !chainCalled || model != "链上默认模型" {
		t.Fatalf("降级链未被真实调用（chainCalled=%v model=%q）", chainCalled, model)
	}
}
