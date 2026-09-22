// Package runtimecfg 租户覆盖探测（AnyTenantFlagOn）的单测：后台任务入口闸的"租户开没开"判定。
//
// 为什么单独测这个谓词：它是"系统层关着、但租户自己开了"唯一能被后台任务看见的信号。
// 写错成"读系统层"或"漏判带引号的 true"，症状都是租户在后台开了开关而作业永不执行——
// 静默失效，没有任何报错，只能靠断言钉。
package runtimecfg

import "testing"

// TestAnyTenantFlagOnOnlySeesTenantLayer 系统层 true 不算数（那是全局读的事），只有租户覆盖层置真才返回真。
func TestAnyTenantFlagOnOnlySeesTenantLayer(t *testing.T) {
	s := NewStaticService(map[string]string{"job_enabled": "true"}, map[uint]map[string]string{
		7: {"other_key": "true"},
	})
	if s.AnyTenantFlagOn("job_enabled") {
		t.Error("系统层的 true 不应被 AnyTenantFlagOn 判为「有租户开启」")
	}
	s2 := NewStaticService(nil, map[uint]map[string]string{
		7: {"job_enabled": "true"},
	})
	if !s2.AnyTenantFlagOn("job_enabled") {
		t.Error("租户覆盖层 true 必须被探测到（否则该租户的作业永不进入）")
	}
}

// TestAnyTenantFlagOnAcceptsBothValueForms 存量值两种形态都要认：裸 true 与带引号 "true"。
// 历史写法不统一（见 normalizeConfigValue 的 P2-55 说明），只认一种就会漏判。
func TestAnyTenantFlagOnAcceptsBothValueForms(t *testing.T) {
	for _, v := range []string{"true", `"true"`, " true ", `"TRUE"`} {
		s := NewStaticService(nil, map[uint]map[string]string{3: {"k": v}})
		if !s.AnyTenantFlagOn("k") {
			t.Errorf("值 %q 应判为已开启", v)
		}
	}
	for _, v := range []string{"false", `"false"`, "0", ""} {
		s := NewStaticService(nil, map[uint]map[string]string{3: {"k": v}})
		if s.AnyTenantFlagOn("k") {
			t.Errorf("值 %q 不应判为已开启", v)
		}
	}
}

// TestAnyTenantFlagOnNilService 单例未初始化时返回 false 而非 panic（冷路径/单测环境）。
func TestAnyTenantFlagOnNilService(t *testing.T) {
	old := DefaultSystemConfigService
	DefaultSystemConfigService = nil
	t.Cleanup(func() { DefaultSystemConfigService = old })
	if SafeAnyTenantFlagOn("talkmining_draft_enabled") {
		t.Error("服务未初始化时不得判为已开启")
	}
}
