// collector 幂等认领单测（P2-6，2026-09-20 批三）：Redis 未启用（测试环境默认）走内存兜底轨——
// 同 ID 首认领成功、重发拒收；空 map 容量自愈不报错。
package api

import (
	"testing"
)

// TestClaimCollectorEventMemory 覆盖内存降级轨的去重语义。
func TestClaimCollectorEventMemory(t *testing.T) {
	id := "evt-unit-p26-" + t.Name()
	if !claimCollectorEvent(id) {
		t.Fatal("首次认领应成功")
	}
	if claimCollectorEvent(id) {
		t.Fatal("同 ID 重发应被去重拒收")
	}
	if !claimCollectorEvent(id + "_other") {
		t.Fatal("不同 ID 互不影响")
	}
}
