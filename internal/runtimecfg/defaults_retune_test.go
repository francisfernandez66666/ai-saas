// Package runtimecfg 出厂值改版纠偏表（retunedConfigValues）的一致性断言。
//
// 为什么要钉住这张表而不是它的执行结果：纠偏本身是"改一次就永远不再命中"的存量迁移，
// 单测里造 DB 状态去跑它，测的是 GORM 而不是我们的约定；真正会出错的是**表写歪**——
// 比如把 To 改成了 100 却忘了同步 DefaultConfigs（仍是 10），于是每次启动都把运营值
// 或新出厂值又改回另一个数，配置中心出现"改不回去的键"。这类错只有交叉断言能抓。
package runtimecfg

import "testing"

// TestRetuneTargetsMatchFactoryDefaults 纠偏目标值必须等于当前出厂值，且纠偏来源必须是旧值。
func TestRetuneTargetsMatchFactoryDefaults(t *testing.T) {
	if len(retunedConfigValues) == 0 {
		t.Fatal("纠偏表为空：量纲改版的存量键将永远读到旧值")
	}
	for _, r := range retunedConfigValues {
		found := false
		for _, c := range DefaultConfigs {
			if c.Key != r.Key {
				continue
			}
			found = true
			if c.Value != r.To {
				t.Errorf("键%s 纠偏目标 %q 与出厂值 %q 不一致：启动后会反复改写", r.Key, r.To, c.Value)
			}
			if c.DefaultValue != r.To {
				t.Errorf("键%s 纠偏目标 %q 与 DefaultValue %q 不一致：ResetAll 会把值打回旧量纲", r.Key, r.To, c.DefaultValue)
			}
			if r.From == r.To {
				t.Errorf("键%s From==To，该条纠偏是空操作，应删除", r.Key)
			}
			break
		}
		if !found {
			t.Errorf("DefaultConfigs 里找不到键 %q：键已改名或删除，本条纠偏失去意义", r.Key)
		}
	}
}

// TestRetuneKeysAreNotPlatformLevel 纠偏表只作用于系统层行，键若是平台级则与"运营统一定价节奏"冲突。
// 平台级键的写入只走 super 通道且强制 tenant_id=0，若混进纠偏表，改一次量纲就会静默改写平台策略。
func TestRetuneKeysAreNotPlatformLevel(t *testing.T) {
	for _, r := range retunedConfigValues {
		if PlatformLevelKeys[r.Key] {
			t.Errorf("键%s 是平台级键，不应出现在出厂值纠偏表中", r.Key)
		}
	}
}
