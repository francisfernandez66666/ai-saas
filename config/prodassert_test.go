// Package config 全局配置中心（见 config.go）。本文件为 P1-4 启动断言的单元测试。
package config

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestAssertProdMode 覆盖三态：prod+debug 拒绝 / prod+release 放行 / dev 不拦。
func TestAssertProdMode(t *testing.T) {
	cases := []struct {
		name    string
		appEnv  string
		ginMode string
		wantErr bool
	}{
		{"prod+debug 拒绝", "prod", gin.DebugMode, true},
		{"prod+test 拒绝", "prod", gin.TestMode, true},
		{"prod+release 放行", "prod", gin.ReleaseMode, false},
		{"dev+debug 放行", "dev", gin.DebugMode, false},
		{"dev+release 放行", "dev", gin.ReleaseMode, false},
		{"空环境+debug 放行", "", gin.DebugMode, false},
		{"非prod大写+debug 放行", "staging", gin.DebugMode, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := assertProdMode(tc.appEnv, tc.ginMode)
			if (err != nil) != tc.wantErr {
				t.Fatalf("assertProdMode(%q,%q) err=%v, wantErr=%v", tc.appEnv, tc.ginMode, err, tc.wantErr)
			}
		})
	}
}
