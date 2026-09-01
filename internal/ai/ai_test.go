// AI 调度层纯逻辑单元测试（2026-09-01 补齐）
// 覆盖：兜底话术构建（BuildFallbackReply 含促单锁）、策略指令构建（BuildStrategyPrompt）、
// 历史截断（BuildConversationHistory）、模型冷却（markFailure/markSuccess/RecoverCoolingModels）、
// 大小写不敏感匹配（containsFold）
package ai

import (
	"strings"
	"testing"
	"time"

	"ai-scrm/internal/model"
	"ai-scrm/internal/strategytypes"
)

// TestBuildFallbackReply PromtText+Hook 拼接；条件交换受 canPromote 锁（促单红线）
func TestBuildFallbackReply(t *testing.T) {
	// 基础拼接
	reply := BuildFallbackReply(&strategytypes.StrategyOutput{
		PromptText:   "这款车有四驱。",
		HookText:     "周六有现车试驾。",
		ExchangeFlag: true, ExchangeType: "price",
	}, true)
	if !strings.Contains(reply, "这款车有四驱") || !strings.Contains(reply, "周六有现车试驾") {
		t.Errorf("应拼接 prompt+hook, got %q", reply)
	}
	if !strings.Contains(reply, "优惠") {
		t.Errorf("canPromote=true 且price 交换应带优惠话术, got %q", reply)
	}
	// 促单锁：canPromote=false 禁止优惠
	replyLocked := BuildFallbackReply(&strategytypes.StrategyOutput{
		PromptText: "到店前不建议谈价。", ExchangeFlag: true, ExchangeType: "price",
	}, false)
	if strings.Contains(replyLocked, "优惠") {
		t.Errorf("canPromote=false 不应带优惠, got %q", replyLocked)
	}
	// 引擎空回退
	if got := BuildFallbackReply(&strategytypes.StrategyOutput{}, true); got == "" {
		t.Error("空输出应有兜底话术")
	}
}

// TestBuildStrategyPrompt 策略指令：不抛锚注入倾听探索；拆解/对比锚专属指令；条件交换受促单锁
func TestBuildStrategyPrompt(t *testing.T) {
	// 不抛锚 + 未留资 → 倾听探索指令
	p := BuildStrategyPrompt(&strategytypes.StrategyOutput{FinalAnchor: strategytypes.AnchorNoThrow},
		nil, 0, false, true, false)
	if !strings.Contains(p, "倾听探索") {
		t.Errorf("未留资不抛锚应注入倾听探索, got %q", p)
	}
	// 不抛锚 + 已留资 → 跳过倾听探索（不再问开放问题）
	pCaptured := BuildStrategyPrompt(&strategytypes.StrategyOutput{FinalAnchor: strategytypes.AnchorNoThrow},
		nil, 0, false, true, true)
	if strings.Contains(pCaptured, "倾听探索") {
		t.Errorf("已留资不抛锚不应注入倾听探索, got %q", pCaptured)
	}
	// 拆解锚 → 兴趣爆发指令
	pDis := BuildStrategyPrompt(&strategytypes.StrategyOutput{FinalAnchor: strategytypes.AnchorDisassemble},
		nil, 0, false, true, false)
	if !strings.Contains(pDis, "兴趣爆发") {
		t.Errorf("拆解锚应注入兴趣爆发指令, got %q", pDis)
	}
	// 条件交换受促单锁：canPromote=false 不出现交换指令
	pSwap := BuildStrategyPrompt(&strategytypes.StrategyOutput{
		FinalAnchor: strategytypes.AnchorDisassemble, ExchangeFlag: true, ExchangeType: "payment",
	}, nil, 0, false, false, false)
	if strings.Contains(pSwap, "交换") {
		t.Errorf("canPromote=false 不应有交换指令, got %q", pSwap)
	}
	// 客户标签拼接
	pTag := BuildStrategyPrompt(&strategytypes.StrategyOutput{FinalAnchor: strategytypes.AnchorCompare},
		[]string{"关注四驱", "预算30万"}, 0, false, true, false)
	if !strings.Contains(pTag, "关注四驱") || !strings.Contains(pTag, "预算30万") {
		t.Errorf("应拼接客户标签, got %q", pTag)
	}
}

// TestBuildConversationHistory 历史截断：取最近 maxRounds 轮、角色映射
func TestBuildConversationHistory(t *testing.T) {
	msgs := []model.Message{
		{SenderType: "customer", Content: "在吗"},
		{SenderType: "ai", Content: "在的，有什么想看"},
		{SenderType: "customer", Content: "越野车"},
		{SenderType: "human", Content: "您好，我帮您安排"},
	}
	got := BuildConversationHistory(msgs, 1) // 保留最近1轮=2条
	if len(got) != 2 {
		t.Fatalf("maxRounds=1 应保留2条, got %d", len(got))
	}
	if got[0].Role != "user" || got[1].Role != "assistant" {
		t.Errorf("角色映射错误, got %+v", got)
	}
	if got[0].Content != "越野车" {
		t.Errorf("应保留最近的客户消息, got %q", got[0].Content)
	}
	if got := BuildConversationHistory(nil, 3); got != nil {
		t.Errorf("空消息应返回 nil, got %+v", got)
	}
}

// TestMarkFailureCooldown 冷却规则：连续失败≥5置不可用；成功复位 0
func TestMarkFailureCooldown(t *testing.T) {
	r := &AIRouter{coolDownSec: 5}
	m := &ModelState{Provider: ProviderSiliconFlow, ModelName: "m1", Available: true}
	for i := 0; i < 4; i++ {
		r.markFailure(m)
	}
	if !m.Available {
		t.Error("失败4次仍应可用")
	}
	r.markFailure(m)
	if m.Available {
		t.Errorf("连续失败5次应置不可用（冷却）")
	}
	r.markSuccess(m)
	if !m.Available || m.ConsecutiveFails != 0 {
		t.Errorf("成功应复位可用且计数清零, got avail=%v fails=%d", m.Available, m.ConsecutiveFails)
	}
}

// TestRecoverCoolingModels 冷却到期自动恢复：超过 coolDownSec 恢复可用，未到期维持不可用
func TestRecoverCoolingModels(t *testing.T) {
	r := &AIRouter{coolDownSec: 5}
	expired := &ModelState{Provider: ProviderSiliconFlow, ModelName: "m1", Available: false, LastFailTime: time.Now().Add(-10 * time.Second)}
	notYet := &ModelState{Provider: ProviderSiliconFlow, ModelName: "m2", Available: false, LastFailTime: time.Now().Add(-1 * time.Second)}
	r.models = []*ModelState{expired, notYet}
	r.RecoverCoolingModels()
	if !expired.Available {
		t.Error("超期模型应恢复可用")
	}
	if notYet.Available {
		t.Error("未到期模型不应恢复")
	}
}

// TestContainsFold 大小写不敏感子串匹配（provider 推断用）
func TestContainsFold(t *testing.T) {
	if !containsFold("THUDM/GLM-4-9B-0414", "glm") {
		t.Error("应匹配小写 glm")
	}
	if !containsFold("abcDEF", "def") {
		t.Error("应匹配混合大小写")
	}
	if containsFold("abc", "xyz") {
		t.Error("不应误匹配")
	}
	if containsFold("a", "ab") {
		t.Error("子串长于文本不应匹配")
	}
}
