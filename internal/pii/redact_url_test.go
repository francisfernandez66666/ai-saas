// 凭据型 URL 脱敏的单测（FIX-2，2026-09-26）：正向证"星号替身"，反向证"排障线索不丢"。
package pii

import (
	"strings"
	"testing"
)

// TestRedactSecretURL 逐参验证：值被替成 ***，scheme/host/path 与其它参数原样保留。
func TestRedactSecretURL(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		mustHas []string // 脱敏后仍必须存在（排障线索）
	}{
		{
			name: "企微取token的真实错误形态",
			in:   `Get "https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid=ww1234567&corpsecret=SECRETABCDEF123456": dial tcp 1.2.3.4:443: connect: network is unreachable`,
			want: `Get "https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid=ww1234567&corpsecret=***": dial tcp 1.2.3.4:443: connect: network is unreachable`,
			mustHas: []string{
				"qyapi.weixin.qq.com",    // 域名要在，否则无法判断打到哪个网关
				"gettoken",               // 路径要在
				"corpid=ww1234567",       // corpid 不是密钥（企微侧公开标识），保留便于定位通道
				"network is unreachable", // 真因要在
			},
		},
		{
			name: "公众号 secret",
			in:   `Get "https://api.weixin.qq.com/cgi-bin/token?grant_type=client_credential&appid=wx9&secret=ABCDEFGH": context deadline exceeded`,
			want: `Get "https://api.weixin.qq.com/cgi-bin/token?grant_type=client_credential&appid=wx9&secret=***": context deadline exceeded`,
		},
		{
			name: "access_token 在中间参数位",
			in:   `Post "https://qyapi.weixin.qq.com/cgi-bin/message/send?access_token=TokValue123": EOF`,
			want: `Post "https://qyapi.weixin.qq.com/cgi-bin/message/send?access_token=***": EOF`,
		},
		{
			name: "大小写不敏感",
			in:   "http://h/p?CorpSecret=abc&x=1",
			want: "http://h/p?CorpSecret=***&x=1",
		},
		{
			name: "无凭据参数原样返回",
			in:   `Get "https://qyapi.weixin.qq.com/cgi-bin/get_menu?corpid=ww1": dial tcp`,
			want: `Get "https://qyapi.weixin.qq.com/cgi-bin/get_menu?corpid=ww1": dial tcp`,
		},
	}
	for _, c := range cases {
		got := RedactSecretURL(c.in)
		if got != c.want {
			t.Errorf("%s: RedactSecretURL=%q want %q", c.name, got, c.want)
		}
		if strings.Contains(got, "SECRETABCDEF") || strings.Contains(got, "TOK") {
			t.Errorf("%s: 密钥明文未被替换: %q", c.name, got)
		}
		for _, mh := range c.mustHas {
			if !strings.Contains(got, mh) {
				t.Errorf("%s: 脱敏把排障线索一起吃掉了，缺 %q: %q", c.name, mh, got)
			}
		}
	}
}

// TestRedactSecretURLIdempotent 二次脱敏结果不变 —— 落库侧与出接口侧各脱一次才不会叠加星号。
func TestRedactSecretURLIdempotent(t *testing.T) {
	in := `Get "https://h/gettoken?corpid=ww1&corpsecret=ABC": x`
	once := RedactSecretURL(in)
	twice := RedactSecretURL(once)
	if once != twice {
		t.Errorf("脱敏不幂等: once=%q twice=%q", once, twice)
	}
	if !strings.Contains(once, "corpsecret=***") {
		t.Errorf("期望恰好替成 ***，实际 %q", once)
	}
}

// TestContainsSecretParam 供清洗脚本与冒烟自检使用：
// 未脱敏为 true、已脱敏为 false、空值（secret=）不算泄露。
func TestContainsSecretParam(t *testing.T) {
	if !ContainsSecretParam("https://h/t?corpsecret=ABC") {
		t.Error("含明文密钥却判 false，护栏会空转")
	}
	if ContainsSecretParam("https://h/t?corpsecret=***") {
		t.Error("已脱敏仍判 true，清洗自检无法收敛")
	}
	if ContainsSecretParam("https://h/t?corpsecret=") {
		t.Error("空值不构成泄露，不该判 true")
	}
	if ContainsSecretParam("") {
		t.Error("空串不该判 true")
	}
}
