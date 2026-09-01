// 无关话题硬边界校验（2026-09-01 补）
// 覆盖：白名单优先（含车相关词放行）、黑名单拦截、白黑同现白名单优先、空串放行
package service

import "testing"

// TestIsOffTopic 无关话题硬边界：白名单优先于黑名单
func TestIsOffTopic(t *testing.T) {
	// 车相关 → 放行
	if IsOffTopic("这款越野车四驱怎么样") {
		t.Error("车相关消息不应被拦截")
	}
	// 黑名单无关话题 → 拦截
	if !IsOffTopic("帮我解一下这道高数题") {
		t.Error("纯无关话题应被拦截")
	}
	if !IsOffTopic("你能帮我写个python脚本吗") {
		t.Error("编程类无关话题应被拦截")
	}
	// 白黑同现 → 白名单优先，放行（客户说"帮我写个试驾报告"）
	if IsOffTopic("帮我写个试驾报告") {
		t.Error("含试驾白名单词不应被拦截")
	}
	// 空串/纯空格 → 放行
	if IsOffTopic("") || IsOffTopic("   ") {
		t.Error("空串应放行")
	}
	// 非车非无关 → 放行
	if IsOffTopic("你好") {
		t.Error("普通寒暄应放行")
	}
}

// TestGetOffTopicReply 无关话题兜底话术：确定性（按长度散列）且非空
func TestGetOffTopicReply(t *testing.T) {
	replies := map[string]bool{
		"哈哈这块我确实不太行，咱们还是聊车吧？你平时用车都干啥？": true,
		"这个我还真不懂，不过你平时有没有自驾出行的需求？":     true,
		"哈哈我是卖车的，专业对口才靠谱，你想了解哪款？":      true,
		"这块超纲了哈，我对车倒是门儿清，有啥想了解的？":      true,
	}
	got := GetOffTopicReply("abc")
	if !replies[got] {
		t.Errorf("返回话术应属于预设集合, got %q", got)
	}
	// 同长度内容应返回同一条（确定性）
	if GetOffTopicReply("abcd") != GetOffTopicReply("abcd") {
		t.Error("同内容应返回同一话术")
	}
}
