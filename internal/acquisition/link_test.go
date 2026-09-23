// 活码落地链接单测（获客批 · 批次3，2026-09-23）：三级基址优先级与形态规范化。
//
// 这里测的不是字符串拼接，而是"海报会不会印错"：链接一旦印出去就改不动，
// 基址取错（取了生成者当时所在的域名）意味着这一批物料全部指向错误的落地点，
// 而归因闸会把流量记成"没写上"——现场看起来只是"效果不好"，最难查。
package acquisition

import (
	"testing"
)

// TestNormalizeBaseViaBuildLandingLink 覆盖落地链接的规范化：缺协议补 https、去尾部斜杠、
// 三级基址全空时回相对路径（物料印相对路径等于印废码，故空态也必须可解释）。
func TestNormalizeBaseViaBuildLandingLink(t *testing.T) {
	// 运营常漏协议：填 "go.acme.com" 拼进二维码会生成纯文本，微信里点不开——必须补 https
	cases := []struct {
		name         string
		customDomain string
		requestBase  string
		want         string
	}{
		{"自定义域名优先于请求域", "acme.example.com", "http://localhost:9090", "https://acme.example.com/client?code=ABCD2345"},
		{"已带 https 不重复补", "https://go.acme.com/", "http://localhost:9090", "https://go.acme.com/client?code=ABCD2345"},
		{"无域名时回落请求域", "", "http://127.0.0.1:9090", "http://127.0.0.1:9090/client?code=ABCD2345"},
		{"三级全空退回相对路径（不成链但可排查）", "", "", "/client?code=ABCD2345"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// tid 用一个不存在的租户：本包单测环境里配置服务未装配，
			// 第一级（租户热配）必然取空，正好把二、三级单独钉住。
			got := BuildLandingLink(999999, tc.customDomain, tc.requestBase, "ABCD2345")
			if got != tc.want {
				t.Fatalf("链接拼错：\n got=%s\nwant=%s", got, tc.want)
			}
		})
	}
}

// TestTenantLinkBaseNilServiceSafe 配置中心未装配（启动早期/单测）时读基址必须回空不 panic。
// 与 outreach 的 nil 守卫同一红线：这一崩是崩在"运营点生成二维码"这个动作上。
func TestTenantLinkBaseNilServiceSafe(t *testing.T) {
	if got := TenantLinkBase(1); got != "" {
		t.Fatalf("未装配配置服务时应回空串，实得 %q（若配置服务已被其他用例装配，本断言需改为断具体值）", got)
	}
}

// TestResolveLinkBaseEmptyMeansNoAbsoluteBase 空串是"没配到可用域名"的明确信号，
// 管理端据此在页面上提示"当前未配置落地域名"，而不是默默出一张印了相对路径的废码。
func TestResolveLinkBaseEmptyMeansNoAbsoluteBase(t *testing.T) {
	if got := ResolveLinkBase(999999, "", ""); got != "" {
		t.Fatalf("三级全空必须解析出空基址，实得 %q", got)
	}
	if got := ResolveLinkBase(999999, "  ", "  "); got != "" {
		t.Fatalf("纯空白域名等同未配（拼接会得到 https:///client），实得 %q", got)
	}
}
