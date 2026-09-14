// D2b 分包物理防回潮测试：配置/脱敏/RLS/通知/指标/计费已离开 internal/service，新增领域代码不得继续塞回。
package service

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestD2BServiceLayout 断言 internal/service 不再包含已物理拆出的领域文件。
// 背景：service 包曾长期堆积计费、配置中心、通知、脱敏、监控等巨型能力；D2b 已将其拆到独立包，
// 此测试用于防止“同包同名文件回流”导致分包红线重新失效。
func TestD2BServiceLayout(t *testing.T) {
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 无法定位当前测试文件")
	}
	dir := filepath.Dir(self)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取 internal/service 目录失败: %v", err)
	}
	present := map[string]bool{}
	for _, e := range entries {
		present[e.Name()] = true
	}

	removed := []string{
		"system_config_service.go",
		"config_defaults.go",
		"system_config_retire_test.go",
		"mask.go",
		"rls.go",
		"notifier.go",
		"metrics.go",
		"metrics_disk_unix.go",
		"metrics_disk_other.go",
		"monitoring.go",
		"billing_service.go",
		"billing_order.go",
		"billing_grant.go",
		"billing_refund.go",
		"token_billing.go",
		"usage_sink.go",
		"usage_service.go",
		"package_service.go",
		"referral.go",
	}
	for _, name := range removed {
		if present[name] {
			t.Errorf("D2b 分包已废弃的文件重新出现在 internal/service: %s", name)
		}
	}
}
