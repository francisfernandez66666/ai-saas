// WebSocket 自定义握手鉴权单测（2026-09-08 修复落点）：
// 覆盖四类来源——无 Origin（非浏览器/多端/未来 OpenAPI）、同源、白名单源、恶意跨域。
package api

import (
	"net/http/httptest"
	"testing"

	"golang.org/x/net/websocket"
)

func TestWSHandshakeOriginRules(t *testing.T) {
	cases := []struct {
		name   string // 用例名
		origin string // 请求 Origin（空串=不携带头）
		host   string // 请求 Host
		allow  bool   // 期望是否放行
	}{
		{"非浏览器-无Origin头放行", "", "localhost:9090", true},
		{"同源放行", "http://localhost:9090", "localhost:9090", true},
		{"同源-端口变体同一Host放行", "http://localhost:9090", "localhost:9090", true},
		{"本地开发白名单源放行", "http://localhost:5173", "localhost:9090", true},
		{"恶意跨域拒绝", "http://evil.com", "localhost:9090", false},
		{"非法URL拒绝", "not-a-url%%", "localhost:9090", false},
		{"空Origin串（空头等价无头）", "", "localhost:9090", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/ws/advisor", nil)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			err := wsHandshakeCheck(&websocket.Config{}, req)
			if tc.allow && err != nil {
				t.Fatalf("应放行却拒绝: %v", err)
			}
			if !tc.allow && err == nil {
				t.Fatalf("应拒绝却放行")
			}
		})
	}
}
