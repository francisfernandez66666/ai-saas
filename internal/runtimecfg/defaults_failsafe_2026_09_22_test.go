// Package runtimecfg 2026-09-22 复核 P0-1/P0-2 的回归断言：出厂默认值必须站在安全侧
//
// 为什么要为"配置默认值"写测试：默认值是唯一一处"改一行代码就能让整个商业模式失效"
// 的地方，而且它不参与编译、不参与业务测试——改回去不会有任何红灯，只会悄悄回到
// "能演示、不能收款、也不扣费"的状态。
package runtimecfg

import "testing"

// findDefault 在 DefaultConfigs 里取键，返回是否找到与出厂值。
func findDefault(category, key string) (bool, string) {
	for _, c := range DefaultConfigs {
		if c.Category == category && c.Key == key {
			return true, c.Value
		}
	}
	return false, ""
}

// TestDefaultPayModeNotMock 出厂收款模式不得是 mock。
func TestDefaultPayModeNotMock(t *testing.T) {
	found, v := findDefault("billing", "pay_mode")
	if !found {
		// 找不到就放行是"空过"——键被删/改名时必须红，不能绿
		t.Fatal("DefaultConfigs 里找不到 billing.pay_mode：键被改名或删除，本断言失去意义")
	}
	if v == "\"mock\"" || v == "mock" {
		t.Errorf("出厂 pay_mode 不得为 mock（实测：默认 mock 下调一次 mock-pay 即 0 元拿到全部权益），实际=%s", v)
	}
}

// TestDefaultTokenBillingEnabled 扣减引擎总闸出厂应为开。
func TestDefaultTokenBillingEnabled(t *testing.T) {
	found, v := findDefault("billing", "token_billing_enabled")
	if !found {
		t.Fatal("DefaultConfigs 里找不到 billing.token_billing_enabled")
	}
	if v != "true" {
		t.Errorf("出厂 token_billing_enabled 应为 true（否则卖 token 不成立：只落账不扣费），实际=%s", v)
	}
}

// TestDefaultBillingEnforcedSoftly 硬停服闸门出厂仍为关——这是与方案文档的有意偏差。
//
// 方案建议 token_billing_enabled 与 billing_enforced 同时置 true；这里保持 false，
// 理由见 config_defaults.go 中该键的注释：新装环境尚无真实补给路径，硬停会打断整站 AI。
// 本断言的作用是把这个"决定"钉住，防止有人在不知情时把它拨成 true 而无人知晓。
func TestDefaultBillingEnforcedSoftly(t *testing.T) {
	found, v := findDefault("billing", "billing_enforced")
	if !found {
		t.Fatal("DefaultConfigs 里找不到 billing.billing_enforced")
	}
	if v != "false" {
		t.Errorf("billing_enforced 出厂应保持 false（有意偏差，改动前请先看 config_defaults.go 注释），实际=%s", v)
	}
}
