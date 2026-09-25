// AI 回复硬拦截矩阵真实单测（2026-09-24 欠账批一，G-8 残项收口）
//
// `GenerateAIReply` 是"客户收到什么"的唯一出口，但它的分支全靠**提前 return** 实现：
// 跑题拦截、询价拦截、三桶余额、模拟模式、无可用模型、AI 全挂降级、留资反问剥离、
// 引导关闭全面剥离、知识库盲点兜底——每一条都直接影响话术，而此前一条单测都没有
// （TECH_DOC G-8 原话：「llm.GenerateAIReply 硬拦截矩阵」空白）。
//
// 这批用例的价值不在"返回值对不对"，而在钉住三件只有真跑才看得见的事：
//  1. **拦截必须在 AI 之前**：命中硬拦截时厂商端点请求数必须是 0（否则每条跑题消息都照烧 token）；
//     判据用假端点计数，不看日志串。
//  2. **降级出口统一**：余额不足/模拟模式/无模型/AI 失败四条路都落到同一份规则话术
//     （`BuildFallbackReply`），且都受促单锁控制——任何一条漏了 canPromote 就会在降级时促单。
//  3. **引导轮数递减在 defer 里**（P2-60）：所有提前 return 路径都必须消耗一轮，
//     否则配额永不归零、引导式反问永不关闭。这条断言取的是**数据库列值**，不是返回值。
//
// AI 用 httptest 假端点（OpenAI 兼容协议），零真实 Key、零真 token 消耗。
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ai-scrm/config"
	"ai-scrm/internal/ai"
	"ai-scrm/internal/billing"
	"ai-scrm/internal/cache"
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/strategytypes"
	"ai-scrm/internal/testutil"
)

// startFakeAI 起一个 OpenAI 兼容假端点：每次请求计数 +1，并按 replyOf 生成回复内容
func startFakeAI(t *testing.T, replyOf func(body map[string]any) string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		content := replyOf(body)
		resp := map[string]any{
			"id":    "cmpl-guard",
			"model": "guard-model",
			"choices": []map[string]any{
				{"index": 0, "finish_reason": "stop", "message": map[string]string{"role": "assistant", "content": content}},
			},
			"usage": map[string]int{"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// startCounting500 假端点固定 500：模拟"全模型调用失败"
func startCounting500(t *testing.T, hits *int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"上游 500"}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// installGuardEnv 把降级链收成本进程**唯一一条**假端点路，并补齐测试二进制缺失的全局装配。
//
// 三件事都是"不补就会假绿/假红/真烧钱"的：
//  1. **知识库缓存单例**：生产由 main.go（与 cmd/replayeval）按启动顺序装配，llm 包此前
//     没有测试二进制所以从未被满足——GenerateAIReply 在非拦截路径会经
//     `getCustomerModelID → DefaultKnowledgeCache.GetDefaultModel` nil 解引用（本文件首跑即 panic）。
//     这里注入**空缓存**而不是 `InitKnowledgeCache()`：后者会 Reload 扫全表并起跨实例轮询协程，
//     用例既不需要也不该留下常驻 goroutine。
//  2. **网关/智谱/DeepSeek 三路全部清空**：本机 .env 真的带着 DEEPSEEK_API_KEY。
//     留着它，降级链就有第二条路——「AI 全挂降级」用例里假端点回 500 后，链路会**去打真实
//     DeepSeek 端点**（烧真 token、且回复内容不可控），而用例还以为自己在测降级。
//     故除清配置外还自检「降级链候选数 ≤1」（0 是"无可用模型"用例的合法形态）。
//  3. **GatewayURL 清空**：非空即 gatewayMode，本地计量分支整段被跳过，
//     三桶/可用性相关断言会走错分支（`requireNoGatewayClient` 兜底复核）。
func installGuardEnv(t *testing.T, baseURL string, withKey bool) {
	t.Helper()

	oldCache := cache.DefaultKnowledgeCache
	if oldCache == nil {
		cache.DefaultKnowledgeCache = &cache.KnowledgeCacheManager{}
	}

	// AIConfig 是值字段，按值快照即可完整还原（子配置也都是值类型）
	oldAI := config.GlobalConfig.AI
	oldSF, oldGLM, oldGW, oldDS, oldRouter :=
		ai.SiliconFlowDefaultClient, ai.DefaultClient, ai.DefaultGatewayClient, ai.DeepSeekDefaultClient, ai.Router

	key := ""
	if withKey {
		key = "sk-guard-test"
	}
	config.GlobalConfig.AI = config.AIConfig{
		SiliconFlow: config.SiliconFlowConfig{
			APIKey: key, BaseURL: baseURL, Model: "guard-model", MaxTokens: 128, Temperature: 0.5,
		},
	}
	ai.InitSiliconFlowClient()
	ai.InitClient() // 让 ai.DefaultClient 非空（GenerateAIReply 要读它的 APIKey 判 hasAnyAI）
	ai.InitDeepSeekClient()
	ai.InitRouter()
	requireNoGatewayClient(t)
	if n := len(ai.Router.GetModels()); n > 1 {
		t.Fatalf("降级链候选数=%d，期望 ≤1（多于一条即含真实厂商端点，「AI 全挂」用例会去打真接口烧 token）", n)
	}
	t.Cleanup(func() {
		config.GlobalConfig.AI = oldAI
		ai.SiliconFlowDefaultClient, ai.DefaultClient, ai.DefaultGatewayClient,
			ai.DeepSeekDefaultClient, ai.Router = oldSF, oldGLM, oldGW, oldDS, oldRouter
		cache.DefaultKnowledgeCache = oldCache
	})
}

// requireNoGatewayClient 钉住"本用例不在网关模式"：
// 网关模式下本地跳过计量（gatewayMode），若环境里残留了网关客户端，三桶/计量相关断言会走错分支。
func requireNoGatewayClient(t *testing.T) {
	t.Helper()
	if ai.DefaultGatewayClient != nil {
		t.Fatal("测试环境不应存在 AI 网关客户端（会切到 gatewayMode 跳过本地计量分支）")
	}
}

// guardConfig 装热配：行业语义键全部显式给死，避免跟着种子值漂移
func guardConfig(t *testing.T, extra map[string]string) {
	t.Helper()
	old := runtimecfg.DefaultSystemConfigService
	kv := map[string]string{
		"mock_mode":                            "false",
		"token_billing_enabled":                "false",
		"billing_enforced":                     "false",
		"knowledge_blindspot_fallback_enabled": "true",
		"guided_dialog_max_rounds":             "5",
		"repeat_question_max_times":            "3",
		"offtopic_repeat_max_times":            "3",
		"chat_history_rounds":                  "3",
		"industry.topic_keywords":              `["试驾"]`,
		"industry.offtopic_keywords":           `["作业"]`,
		"industry.offtopic_replies":            `["跑题兜底句，咱们聊回正事"]`,
		"industry.price_keywords":              `["多少钱"]`,
		"industry.price_reply_lead":            `["询价已留资句"]`,
		"industry.price_reply_nolead":          `["询价未留资句"]`,
	}
	for k, v := range extra {
		kv[k] = v
	}
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(kv, nil)
	t.Cleanup(func() { runtimecfg.DefaultSystemConfigService = old })
}

// newGuardCustomer 建一个测试客户（不落库也行：GenerateAIReply 只读画像字段，
// 唯二的写库分支是"胡搅蛮缠降权/恢复"，本文件不触发）
func newGuardCustomer(tid uint, journey string) *model.Customer {
	return &model.Customer{
		ID:           990801,
		TenantID:     tid,
		JourneyStage: journey,
		IntentScore:  0.5,
	}
}

// newGuardConversation 建会话并落库（引导轮数/关闭位是本文件的被测列，必须回读）
func newGuardConversation(t *testing.T, tid, custID uint, guidedRounds int, guidedDisabled bool) *model.Conversation {
	t.Helper()
	conv := model.Conversation{
		TenantID: tid, CustomerID: custID, Status: "active",
		GuidedRemainingRounds: guidedRounds, GuidedDisabled: guidedDisabled,
	}
	if err := db.DB.Create(&conv).Error; err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	t.Cleanup(func() { db.DB.Unscoped().Delete(&model.Conversation{}, conv.ID) })
	return &conv
}

// readConvGuided 字段级回读引导轮数与关闭位（P2-60 的判据在库里，不在返回值里）
func readConvGuided(t *testing.T, id uint) (int, bool) {
	t.Helper()
	var row model.Conversation
	if err := db.DB.Select("guided_remaining_rounds", "guided_disabled").First(&row, id).Error; err != nil {
		t.Fatalf("回读会话失败: %v", err)
	}
	return row.GuidedRemainingRounds, row.GuidedDisabled
}

func strategyOut() *strategytypes.StrategyOutput {
	return &strategytypes.StrategyOutput{FinalAnchor: strategytypes.AnchorNoThrow}
}

// TestHardInterceptsNeverReachAI 六条"必须在 AI 之前 return"的路径：
// 假端点请求数必须为 0——这是"硬拦截不进大模型"这条产品承诺在单测层的唯一证据。
func TestHardInterceptsNeverReachAI(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	cases := []struct {
		name    string
		kv      map[string]string
		journey string
		input   string
		want    string // 空=用 BuildFallbackReply 现算期望值
	}{
		{
			name: "跑题硬拦截", kv: nil, journey: "", input: "帮我写一下作业呗",
			want: "跑题兜底句，咱们聊回正事",
		},
		{
			name: "询价硬拦截·未留资", kv: nil, journey: model.JourneyAIConnected, input: "这个多少钱",
			want: "询价未留资句",
		},
		{
			name: "询价硬拦截·已留资", kv: nil, journey: model.JourneyLeadCaptured, input: "到底多少钱",
			want: "询价已留资句",
		},
		{
			name: "模拟模式", kv: map[string]string{"mock_mode": "true"},
			journey: model.JourneyAIConnected, input: "续航实际能跑多少", want: "",
		},
		{
			name: "三桶皆空降级", kv: map[string]string{"token_billing_enabled": "true", "billing_enforced": "true"},
			journey: model.JourneyAIConnected, input: "续航实际能跑多少", want: "",
		},
		{
			name: "无可用模型降级", kv: nil, journey: model.JourneyAIConnected,
			input: "续航实际能跑多少", want: "", // 无 Key 由下面 withKey=false 单独跑
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt64(&hits, 1)
				fmt.Fprint(w, `{"choices":[{"message":{"content":"不该被调用"}}]}`)
			}))
			defer srv.Close()

			guardConfig(t, tc.kv)
			withKey := tc.name != "无可用模型降级"
			installGuardEnv(t, srv.URL, withKey)
			if tc.name == "三桶皆空降级" {
				zeroAllBuckets(t, tid)
			}

			customer := newGuardCustomer(tid, tc.journey)
			conv := newGuardConversation(t, tid, customer.ID, 0, false)

			got := GenerateAIReply(context.Background(), customer, conv.ID, tc.input, strategyOut(), nil)
			if n := atomic.LoadInt64(&hits); n != 0 {
				t.Errorf("命中硬拦截却请求了 AI 端点 %d 次（每条这类消息都在白烧 token）", n)
			}
			if got == "" {
				t.Fatal("硬拦截路径必须给出可读话术，实际空串")
			}
			if tc.want != "" {
				if got != tc.want {
					t.Errorf("话术=%q，期望行业键配置的 %q（说明话术没走租户配置）", got, tc.want)
				}
			} else {
				want := ai.BuildFallbackReply(strategyOut(), customer.CanPromote())
				if got != want {
					t.Errorf("降级出口应统一到规则话术，got=%q want=%q", got, want)
				}
			}
			// P2-60：提前 return 也必须消耗引导轮数——这里轮数已是 0，不得变负
			if rounds, _ := readConvGuided(t, conv.ID); rounds != 0 {
				t.Errorf("guided_remaining_rounds 应为 0，实际 %d", rounds)
			}
		})
	}
}

// zeroAllBuckets 把租户三桶清零（"三桶皆空"用例的前置，不依赖 CreateTenant 默认值）
func zeroAllBuckets(t *testing.T, tid uint) {
	t.Helper()
	if err := db.DB.Model(&model.Tenant{}).Where("id = ?", tid).Updates(map[string]any{
		"free_token_balance": 0, "monthly_token_quota": 0, "monthly_token_used": 0, "token_balance": 0,
	}).Error; err != nil {
		t.Fatalf("清三桶失败: %v", err)
	}
	// 前置自检：确认真的取不到可用性，否则"降级"断言会在 CheckTokenAvailability=true 上假绿
	if billing.CheckTokenAvailability(tid) {
		t.Fatal("前置破坏：三桶已清零但可用性检查仍放行，降级分支不会被触发")
	}
}

// TestAIFailureDegradesToTemplate 反向腿：前序用例的 hits==0 之所以有意义，
// 是因为这里"未命中硬拦截时端点确实会被请求"。AI 全挂必须落到规则话术而不是 500/空串。
func TestAIFailureDegradesToTemplate(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	var hits int64
	srv := startCounting500(t, &hits)
	guardConfig(t, nil)
	installGuardEnv(t, srv.URL, true)

	customer := newGuardCustomer(tid, model.JourneyAIConnected)
	conv := newGuardConversation(t, tid, customer.ID, 0, false)

	got := GenerateAIReply(context.Background(), customer, conv.ID, "续航实际能跑多少公里", strategyOut(), nil)
	if atomic.LoadInt64(&hits) == 0 {
		t.Fatal("未命中硬拦截时应当真调 AI（否则上一组的 hits==0 断言是空转）")
	}
	if want := ai.BuildFallbackReply(strategyOut(), customer.CanPromote()); got != want {
		t.Errorf("AI 全挂应降级到规则话术，got=%q want=%q", got, want)
	}
}

// TestLeadCapturedReplyStripsGuidedQuestion 留资硬拦截（分支7）：已留资 + 引导轮数已耗尽时，
// AI 回复里的反问句必须被剥掉——留资后禁止再问，这是线索两分支的硬约定。
func TestLeadCapturedReplyStripsGuidedQuestion(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	const raw = "这款车实际续航能跑四百多。你是想周末来店里试驾吗？"
	srv := startFakeAI(t, func(map[string]any) string { return raw })
	guardConfig(t, nil)
	installGuardEnv(t, srv.URL, true)

	customer := newGuardCustomer(tid, model.JourneyLeadCaptured)
	conv := newGuardConversation(t, tid, customer.ID, 0, false) // 轮数 0 = 非"留资后第一条"豁免窗口

	got := GenerateAIReply(context.Background(), customer, conv.ID, "续航实际能跑多少公里", strategyOut(), nil)
	if strings.ContainsAny(got, "吗呢吧？?") {
		t.Errorf("已留资客户的回复仍含反问句：%q（应为剥离后的陈述部分）", got)
	}
	if !strings.HasPrefix(raw, got) || got == raw {
		t.Errorf("剥离结果=%q 不是原句的陈述前缀（原句 %q）", got, raw)
	}
}

// TestGuidedDisabledStripsAllQuestions 分支7d：引导已关闭的会话，任何反问都要剥掉；
// 反向腿=同一句话在未关闭时原样返回（否则"剥离"可能只是无条件截断）。
func TestGuidedDisabledStripsAllQuestions(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	const raw = "续航四百多公里，你平时跑高速多不多？"

	srv := startFakeAI(t, func(map[string]any) string { return raw })
	guardConfig(t, nil)
	installGuardEnv(t, srv.URL, true)

	customer := newGuardCustomer(tid, model.JourneyAIConnected) // 未留资：走 7d 而非分支7

	// 关闭态：反问必须被剥
	convOff := newGuardConversation(t, tid, customer.ID, 0, true)
	gotOff := GenerateAIReply(context.Background(), customer, convOff.ID, "续航跑高速能有多少", strategyOut(), nil)
	if strings.ContainsAny(gotOff, "？?") {
		t.Errorf("引导关闭后回复仍带问句：%q", gotOff)
	}

	// 未关闭态：同一句必须原样出来（证明剥离是被 GuidedDisabled 触发的，不是恒截断）
	customer2 := newGuardCustomer(tid, model.JourneyAIConnected)
	customer2.ID = 990802
	convOn := newGuardConversation(t, tid, customer2.ID, 0, false)
	gotOn := GenerateAIReply(context.Background(), customer2, convOn.ID, "续航跑高速能有多少", strategyOut(), nil)
	if strings.TrimSpace(gotOn) != raw {
		t.Errorf("引导未关闭时不应改动 AI 回复，got=%q want=%q", gotOn, raw)
	}
}

// TestKnowledgeBlindspotFallbackSwitch 知识库盲点兜底（分支8）：模型输出含不确定信号时
// 换成固定兜底句；总开关关掉必须原样返回（该开关是热配，关掉即回退旧行为）。
func TestKnowledgeBlindspotFallbackSwitch(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	const raw = "这个参数我不太确定，你到店看一下实车就行。"

	t.Run("开关开_换兜底句", func(t *testing.T) {
		srv := startFakeAI(t, func(map[string]any) string { return raw })
		guardConfig(t, nil)
		installGuardEnv(t, srv.URL, true)
		customer := newGuardCustomer(tid, model.JourneyAIConnected)
		conv := newGuardConversation(t, tid, customer.ID, 0, false)
		got := GenerateAIReply(context.Background(), customer, conv.ID, "轴距到底多少", strategyOut(), nil)
		if got == raw || !strings.Contains(got, "查") {
			t.Errorf("盲点信号未被兜底替换，got=%q", got)
		}
	})

	t.Run("开关关_原样返回", func(t *testing.T) {
		srv := startFakeAI(t, func(map[string]any) string { return raw })
		guardConfig(t, map[string]string{"knowledge_blindspot_fallback_enabled": "false"})
		installGuardEnv(t, srv.URL, true)
		customer := newGuardCustomer(tid, model.JourneyAIConnected)
		conv := newGuardConversation(t, tid, customer.ID, 0, false)
		got := GenerateAIReply(context.Background(), customer, conv.ID, "轴距到底多少", strategyOut(), nil)
		if strings.TrimSpace(got) != raw {
			t.Errorf("开关关掉后不得改写回复，got=%q want=%q", got, raw)
		}
	})
}

// TestGuidedRoundDecrementsOnEveryExit P2-60 回归锁：递减挂在 defer 统一出口上，
// **提前 return 的硬拦截路径也必须消耗一轮**。旧实现只在真 AI 成功路径末尾递减，
// 配额耗尽/降级/硬拦截永不递减 → 租户永久卡在 GuidedRemainingRounds=1、反问永不关闭。
func TestGuidedRoundDecrementsOnEveryExit(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"不该被调用"}}]}`)
	}))
	defer srv.Close()
	guardConfig(t, nil)
	installGuardEnv(t, srv.URL, true)

	// 2 → 1：仍在引导期，不得提前置关闭位
	customer := newGuardCustomer(tid, model.JourneyAIConnected)
	conv := newGuardConversation(t, tid, customer.ID, 2, false)
	GenerateAIReply(context.Background(), customer, conv.ID, "帮我写一下作业呗", strategyOut(), nil)
	if rounds, disabled := readConvGuided(t, conv.ID); rounds != 1 || disabled {
		t.Errorf("硬拦截路径应把引导轮数 2 递减为 1 且不关闭引导，实际 rounds=%d disabled=%v", rounds, disabled)
	}

	// 1 → 0 且置 guided_disabled：配额耗尽当轮就要关反问
	customer2 := newGuardCustomer(tid, model.JourneyAIConnected)
	customer2.ID = 990803
	conv2 := newGuardConversation(t, tid, customer2.ID, 1, false)
	GenerateAIReply(context.Background(), customer2, conv2.ID, "帮我写一下作业呗", strategyOut(), nil)
	if rounds, disabled := readConvGuided(t, conv2.ID); rounds != 0 || !disabled {
		t.Errorf("引导轮数耗尽应置 guided_disabled=true，实际 rounds=%d disabled=%v", rounds, disabled)
	}

	if atomic.LoadInt64(&hits) != 0 {
		t.Errorf("跑题用例不应请求 AI，实际 %d 次", atomic.LoadInt64(&hits))
	}
}

// TestOutbound敬语清洗 sanitizeAddress 挂在唯一出口：模型输出「您」必须被换成「你」，
// 且这条替换发生在所有拦截之后（否则剥离/兜底句里残留敬语没人管）。
func TestOutbound敬语清洗(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	const raw = "建议您到店体验一下实车。" // 故意不含盲点信号词，也不含反问标记词
	srv := startFakeAI(t, func(map[string]any) string { return raw })
	guardConfig(t, nil)
	installGuardEnv(t, srv.URL, true)

	customer := newGuardCustomer(tid, model.JourneyAIConnected)
	conv := newGuardConversation(t, tid, customer.ID, 0, false)
	got := GenerateAIReply(context.Background(), customer, conv.ID, "续航实际能跑多少公里", strategyOut(), nil)
	// 等值断言（不是"不含您"的单向断言）：单向断言允许实现顺手改写整句，
	// 而这条清洗的契约恰恰是"只换人称、不动内容"。
	if want := "建议你到店体验一下实车。"; got != want {
		t.Errorf("出口敬语清洗结果=%q，期望 %q（只准换「您」为「你」，其余逐字不变）", got, want)
	}
}
