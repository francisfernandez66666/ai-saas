// Package service 业务服务层测试：数据飞轮批量上报接收与 PII 递归脱敏。
package service

import "testing"

// TestAnonymizeText 验证文本匿名化：手机号/邮箱被掩码替换
func TestAnonymizeText(t *testing.T) {
	got := AnonymizeText("手机13800001111 邮箱a@b.com")
	want := "手机1380***1111 邮箱a***@b.com"
	if got != want {
		t.Errorf("AnonymizeText=%q want %q", got, want)
	}
}

// TestAnonymizePayload 验证嵌套 payload 递归脱敏：map/list 内敏感信息都被掩码，且不改动原数据
func TestAnonymizePayload(t *testing.T) {
	in := map[string]any{
		"text":   "联系13800001111",
		"nested": map[string]any{"memo": "邮件 x@y.com"},
		"list":   []any{"call13800001111", 123},
	}
	out := AnonymizePayload(in)
	if out["text"] != "联系1380***1111" {
		t.Errorf("text 未脱敏: %v", out["text"])
	}
	nested := out["nested"].(map[string]any)
	if nested["memo"] != "邮件 x***@y.com" {
		t.Errorf("nested 未脱敏: %v", nested["memo"])
	}
	list := out["list"].([]any)
	if list[0] != "call1380***1111" {
		t.Errorf("list 未脱敏: %v", list[0])
	}
	if list[1] != 123 {
		t.Errorf("非字符串应原样: %v", list[1])
	}
	// 原始 map 不应被修改（脱敏应为副本）
	if in["text"] != "联系13800001111" {
		t.Errorf("原始 payload 不应被改动")
	}
}
