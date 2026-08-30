package service

import "testing"

func TestMaskPhone(t *testing.T) {
	cases := map[string]string{
		"13800001111": "138****1111",
		"123":         "123", // 非11位原样
		"":            "",
	}
	for in, want := range cases {
		if got := MaskPhone(in); got != want {
			t.Errorf("MaskPhone(%q)=%q want %q", in, got, want)
		}
	}
}

func TestMaskEmail(t *testing.T) {
	cases := map[string]string{
		"alice@example.com": "a***e@example.com",
		"bob@x.io":          "b***b@x.io",
		"a@b.com":           "a***@b.com",
		"notanemail":        "notanemail",
	}
	for in, want := range cases {
		if got := MaskEmail(in); got != want {
			t.Errorf("MaskEmail(%q)=%q want %q", in, got, want)
		}
	}
}

func TestMaskName(t *testing.T) {
	cases := map[string]string{
		"张三":  "*三",
		"李四王": "李**王",
		"王":   "王",
		"":    "",
	}
	for in, want := range cases {
		if got := MaskName(in); got != want {
			t.Errorf("MaskName(%q)=%q want %q", in, got, want)
		}
	}
}

func TestMaskPhoneInText(t *testing.T) {
	got := MaskPhoneInText("联系 13800001111 或 13912345678 谢谢")
	want := "联系 1380***1111 或 1391***5678 谢谢"
	if got != want {
		t.Errorf("MaskPhoneInText=%q want %q", got, want)
	}
}
