// Package api 测试：P2-5 内容安全 LLM 机审（fail-open + JSON 解析）。
package api

import (
	"fmt"
	"testing"
)

// TestLLMReviewerReview 覆盖：命中违规、未命中、空文本、调用异常 fail-open、解析失败 fail-open。
func TestLLMReviewerReview(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		call    func(string) (string, error)
		wantVio bool
		wantErr bool
	}{
		{
			name:    "机审判违规",
			text:    "含违规内容的回复",
			call:    func(string) (string, error) { return `{"violation": true, "reason": "涉政"}`, nil },
			wantVio: true,
		},
		{
			name:    "机审未违规",
			text:    "正常回复",
			call:    func(string) (string, error) { return `{"violation": false, "reason": ""}`, nil },
			wantVio: false,
		},
		{
			name:    "空文本直接放行不调用",
			text:    "   ",
			call:    func(string) (string, error) { t.Fatal("空文本不应调用 LLM"); return "", nil },
			wantVio: false,
		},
		{
			name:    "调用异常 fail-open 放行",
			text:    "某回复",
			call:    func(string) (string, error) { return "", fmt.Errorf("网关超时") },
			wantVio: false,
			wantErr: true,
		},
		{
			name: "模型夹带文字仍能解析",
			text: "某回复",
			call: func(string) (string, error) {
				return "好的，结果如下：{\"violation\": true, \"reason\": \"虚假宣传\"}", nil
			},
			wantVio: true,
		},
		{
			name:    "无 JSON 解析失败 fail-open",
			text:    "某回复",
			call:    func(string) (string, error) { return "我无法判断", nil },
			wantVio: false,
			wantErr: true,
		},
	}
	for _, c := range cases {
		r := &LLMReviewer{call: c.call}
		vio, _, err := r.Review(c.text)
		if vio != c.wantVio {
			t.Errorf("%s: violation=%v, want %v", c.name, vio, c.wantVio)
		}
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err=%v, wantErr=%v", c.name, err, c.wantErr)
		}
	}
}
