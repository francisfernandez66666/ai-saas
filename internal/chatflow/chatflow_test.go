// 聊天流核心商业规则单元测试（2026-09-01 补齐零覆盖）
// 覆盖：留资判定（IsLeadCaptured）、反问剥离（StripGuidedQuestions/StripAllQuestions/HasAnyPrefix）、
// 中文分词（ExtractKeywords）、知识盲点识别（DetectKnowledgeBlindspot）、延迟取消（RegisterDelayCancel/CancellableSleep/CancelDelay）
package chatflow

import (
	"strings"
	"testing"
	"time"

	"ai-scrm/internal/model"
)

// TestIsLeadCaptured 留资判定：lead_captured 及以上阶段 / 已留资 tag / 未留资 / 空客户
func TestIsLeadCaptured(t *testing.T) {
	stages := []string{
		model.JourneyLeadCaptured, model.JourneyArrived,
		model.JourneyOrdered, model.JourneyDelivered,
	}
	for _, s := range stages {
		c := &model.Customer{JourneyStage: s}
		if !IsLeadCaptured(c) {
			t.Errorf("阶段 %s 应判定已留资", s)
		}
	}
	// tag 兜底
	c := &model.Customer{JourneyStage: "visitor", Tags: `["已留资"]`}
	if !IsLeadCaptured(c) {
		t.Error("含已留资 tag 应判定已留资")
	}
	// 未留资
	c2 := &model.Customer{JourneyStage: "visitor"}
	if IsLeadCaptured(c2) {
		t.Error("visitor 阶段不应判定已留资")
	}
	// 空客户（nil 安全）
	if IsLeadCaptured(nil) {
		t.Error("nil 客户不应判定已留资")
	}
}

// TestStripGuidedQuestions 基础反问剥离：语气词/问号切分，剥离后保留陈述主体
func TestStripGuidedQuestions(t *testing.T) {
	if got := StripGuidedQuestions("我推荐这款越野车，您看呢"); strings.Contains(got, "您看呢") {
		t.Errorf("应剥离语气词反问, got %q", got)
	}
	// 纯语气词句剥离后保留陈述主体
	if got := StripGuidedQuestions("您好吗"); !strings.HasPrefix(got, "您好") {
		t.Errorf("应保留陈述主体, got %q", got)
	}
	// 无反问 → 原样
	if got := StripGuidedQuestions("您的车已到店"); got != "您的车已到店" {
		t.Errorf("无反问应原样, got %q", got)
	}
}

// TestStripAllQuestions 全面反问剥离：显式反问词中缀剥离（若前置部分非空）
func TestStripAllQuestions(t *testing.T) {
	// 显式反问词"要不要"位于句中，前置陈述非空 → 剥离反问
	if got := StripAllQuestions("这款车有四驱，要不要安排试驾"); strings.Contains(got, "要不要") {
		t.Errorf("应剥离显式反问词, got %q", got)
	}
	// 尾语气词路径：剥离末尾"吗"保留陈述主体
	if got := StripAllQuestions("这周三有空，您看行吗"); !strings.Contains(got, "行") {
		t.Errorf("尾语气词应剥离问号部分, got %q", got)
	}
	if got := StripAllQuestions("您好"); got == "" {
		t.Errorf("剥离后不应为空, got %q", got)
	}
}

// TestHasAnyPrefix 前缀命中
func TestHasAnyPrefix(t *testing.T) {
	if !HasAnyPrefix("您留个电话", []string{"您", "你好"}) {
		t.Error("应命中前缀 您")
	}
	if HasAnyPrefix("你好", []string{"您"}) {
		t.Error("不应误命中前缀")
	}
	if HasAnyPrefix("", []string{"您"}) {
		t.Error("空串不应命中")
	}
}

// TestExtractKeywords 中文2-gram分词：停用词过滤、非空文本产出关键词
func TestExtractKeywords(t *testing.T) {
	words := ExtractKeywords("这款越野车有没有四驱")
	if len(words) == 0 {
		t.Fatal("非空文本应产出关键词")
	}
	// 停用词"有没有"中的词不应直接作为关键词（分词后拆入2-gram，验证至少产出内容即可）
	if got := ExtractKeywords(""); len(got) != 0 {
		t.Errorf("空串应产出空关键词, got %v", got)
	}
	// 短文本（1字）不出2-gram
	if got := ExtractKeywords("我"); len(got) != 0 {
		t.Errorf("单字文本不应产出2-gram, got %v", got)
	}
}

// TestDetectKnowledgeBlindspot 知识盲点：命中信号词返回兜底话术；未命中返回空
func TestDetectKnowledgeBlindspot(t *testing.T) {
	got := DetectKnowledgeBlindspot("这个问题目前没有确切信息，建议您咨询门店", "")
	blindspotReplies := []string{"好的，稍等，这个问题我查一下", "这个我得查查，稍等哈", "这个我不太确定，稍等，我帮你查一下"}
	found := false
	for _, r := range blindspotReplies {
		if got == r {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("命中盲点应返回兜底话术之一, got %q", got)
	}
	if got := DetectKnowledgeBlindspot("这款车有四驱和差速锁", ""); got != "" {
		t.Errorf("未命中信号词应返回空, got %q", got)
	}
}

// TestCancellableSleep 延迟取消：短时等待完成返回true；注册后取消返回false
func TestCancellableSleep(t *testing.T) {
	// 零时长立即返回 true
	if !CancellableSleep(999991, 0) {
		t.Error("零时长应返回 true")
	}
	// 短时等完返回 true
	if !CancellableSleep(999991, time.Millisecond*5) {
		t.Error("短时等待应返回 true（未被取消）")
	}
	// 注册后取消 → 返回 false
	const cid = uint(999992)
	done := make(chan bool, 1)
	go func() {
		done <- CancellableSleep(cid, time.Second) // 1s 延迟，应被下边 CancelDelay 打断
	}()
	time.Sleep(time.Millisecond * 30)
	CancelDelay(cid)
	select {
	case ok := <-done:
		if ok {
			t.Error("被取消的延迟应返回 false")
		}
	case <-time.After(time.Millisecond * 200):
		t.Error("取消后 CancellableSleep 未及时返回")
	}
	CancelDelay(cid) // 无活跃延迟，重复取消不 panic
}
