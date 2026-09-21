// P2-12 复核批（2026-09-15）回归：出站回调 URL 的 SSRF 闸门行为锁定。
// 语义：debug/测试环境允许回环（httptest 假接收端、smoke 的 127.0.0.1:9 死信用例）；
// release 必须拒内网/环回/链路本地(云元数据 169.254.169.254)/非法 scheme——方向不可反。
package webhook

import "testing"

// TestValidateCallbackURLDebugAllowsLoopback 验证 debug 环境放行环回地址回调，保持本地联调路径零扰动。
func TestValidateCallbackURLDebugAllowsLoopback(t *testing.T) {
	t.Setenv("GIN_MODE", "debug")
	for _, u := range []string{
		"http://127.0.0.1:9/webhook", "http://localhost/hook",
		"https://api.example.com/wh", "http://10.0.0.8/x",
	} {
		if err := ValidateCallbackURL(u); err != nil {
			t.Errorf("debug 环境 %s 应放行（测试基建依赖）: %v", u, err)
		}
	}
}

// TestValidateCallbackURLReleaseBlocksInternal 验证 release 环境拦截内网/环回/云元数据地址回调，防 SSRF。
func TestValidateCallbackURLReleaseBlocksInternal(t *testing.T) {
	t.Setenv("GIN_MODE", "release")
	blocked := []string{
		"http://127.0.0.1:9/webhook",               // 环回
		"http://localhost/hook",                    // 环回域名（解析到 127.x）
		"http://169.254.169.254/latest/meta-data/", // 云厂商凭证靶点
		"http://10.1.2.3/internal",                 // 私网段
		"http://192.168.0.1/admin",                 // 私网段
		"http://[::1]/x",                           // IPv6 环回
		"http://0.0.0.0/x",                         // 未指定地址
		"ftp://example.com/wh",                     // 非法 scheme
		"://bad",                                   // 解析失败
		"https:///nohost",                          // 缺主机名
	}
	for _, u := range blocked {
		if err := ValidateCallbackURL(u); err == nil {
			t.Errorf("release 环境 %s 必须拒绝（SSRF 面）", u)
		}
	}
	// 公网字面 IP 放行（不依赖 DNS，离线可测）
	if err := ValidateCallbackURL("https://8.8.8.8/hook"); err != nil {
		t.Errorf("release 环境公网 IP 回调应放行: %v", err)
	}
}
