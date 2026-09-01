// Package cdp：internal/cdp 模块（自动补包注释）。
package cdp

import "testing"

// TestIsPriceInquiry 验证价格询问意图识别的典型用例（正向与负向）
func TestIsPriceInquiry(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"多少钱", true},
		{"首付多少", true},
		{"优惠点吗", true},
		{"我想看红色那款", false},
	}
	for _, c := range cases {
		if got := isPriceInquiry(c.text); got != c.want {
			t.Errorf("isPriceInquiry(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

// TestBuiltinTagDefsCount P1-1：原子标签补齐至 30+（四维体系完备度门槛）
func TestBuiltinTagDefsCount(t *testing.T) {
	if len(builtinTagDefs) < 30 {
		t.Errorf("原子标签数 = %d, want >= 30", len(builtinTagDefs))
	}
}
