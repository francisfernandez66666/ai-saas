// testutil 测试基座自身单测（2026-09-08 加固）：
// 验证「进程随机后缀 + 复用语义」——同一进程内多次 CreateTenant 复用同一租户，
// 不同语义码生成不同租户，且 code 均带进程级后缀（供并发 go test 隔离各自数据）。
package testutil

import (
	"testing"
)

func TestCreateTenantReuseSameID(t *testing.T) {
	SetupTestDB(t)
	a := CreateTenant(t)
	b := CreateTenant(t)
	if a != b {
		t.Fatalf("同进程 CreateTenant 应复用同一租户, got %d vs %d", a, b)
	}
	CleanupTenant(t, a)
}

func TestCreateTenantCodeSuffixedAndScoped(t *testing.T) {
	SetupTestDB(t)
	a := CreateTenantCode(t, "iso_a")
	b := CreateTenantCode(t, "iso_b")
	if a == b {
		t.Fatalf("不同语义码应产生不同租户")
	}
	a2 := CreateTenantCode(t, "iso_a")
	if a != a2 {
		t.Fatalf("同语义码应复用同一租户")
	}
	// code 应带进程级后缀（形如 p<pid>x<hex>_iso_a）
	if len(processSuffix) < 8 {
		t.Fatalf("processSuffix 应含 PID+随机段, got %q", processSuffix)
	}
	CleanupTenant(t, a)
	CleanupTenant(t, b)
}
