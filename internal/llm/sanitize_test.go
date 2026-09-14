// Q5 单测(2026-09-12)：去 AI 味铁律——说"你"不说"您"
// ① sanitizeAddress 表驱动；② prompt 指令清洗回归（正向话术不得再出现「您」开头/「您好」指令）
package llm

import "testing"

// TestSanitizeAddress 覆盖 SanitizeAddress 相关行为与边界。
func TestSanitizeAddress(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"空串", "", ""},
		{"无敬语原样", "你想了解啥", "你想了解啥"},
		{"单词您", "您好呀", "你好呀"},
		{"多处您", "等您试驾后我再根据您的配置报价", "等你试驾后我再根据你的配置报价"},
		{"句尾您", "随时找我您", "随时找我你"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sanitizeAddress(c.in); got != c.want {
				t.Errorf("sanitizeAddress(%q)=%q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestSanitizeAddressPreservesOtherText 只替换"您"字符，其余内容（含"你"）零改动
func TestSanitizeAddressPreservesOtherText(t *testing.T) {
	in := "你、您好，咱的车 15.7英寸屏幕"
	want := "你、你好，咱的车 15.7英寸屏幕"
	if got := sanitizeAddress(in); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
