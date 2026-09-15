// P2-12 复核批（2026-09-15）回归：通道 config_json.mock_base_url 的 SSRF 闸门。
// mock_base_url 是"把收发生效流量指到任意主机"的后门键，须与 webhook 回调同闸口径：
// debug 放行（mockwx 本地联调链路），release 拒内网/环回；JSON 形态错误不在此裁决（交下层）。
package api

import "testing"

func TestValidateChannelMockBaseURL(t *testing.T) {
	// debug：空配置/无键/非法 JSON/空值均不得拦截（保持既有联调路径零扰动）
	t.Setenv("GIN_MODE", "debug")
	for _, cfg := range []string{"", "{}", `{"corp":"x"}`, `not-json`, `{"mock_base_url":""}`, `{"mock_base_url":"  "}`} {
		if err := validateChannelMockBaseURL(cfg); err != nil {
			t.Errorf("debug 环境 %q 应放行: %v", cfg, err)
		}
	}
	if err := validateChannelMockBaseURL(`{"mock_base_url":"http://127.0.0.1:9999"}`); err != nil {
		t.Errorf("debug 环境本地 mock 端点应放行: %v", err)
	}

	// release：内网/环回靶点必须拒，公网放行
	t.Setenv("GIN_MODE", "release")
	if err := validateChannelMockBaseURL(`{"mock_base_url":"http://127.0.0.1:9999"}`); err == nil {
		t.Error("release 环境环回 base_url 必须拒绝")
	}
	if err := validateChannelMockBaseURL(`{"mock_base_url":"http://169.254.169.254/"}`); err == nil {
		t.Error("release 环境云元数据靶点必须拒绝")
	}
	if err := validateChannelMockBaseURL(`{"mock_base_url":"ftp://10.0.0.1"}`); err == nil {
		t.Error("非 http(s) scheme 必须拒绝")
	}
	if err := validateChannelMockBaseURL(`{"mock_base_url":"https://qyapi.weixin.qq.com"}`); err != nil {
		t.Errorf("公网官方端点应放行: %v", err)
	}
}
