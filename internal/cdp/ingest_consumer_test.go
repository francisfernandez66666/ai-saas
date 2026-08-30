package cdp

import "testing"

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
