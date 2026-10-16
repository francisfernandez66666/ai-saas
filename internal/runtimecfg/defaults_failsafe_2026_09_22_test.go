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

// TestDefaultBillingEnforcedOn 硬停服闸门出厂为 true（P1-3 收口，2026-10-14）。
//
// 历史：方案初期因新装环境无收款路径，刻意保持 false（"扣减先开、硬停缓开"的防误伤爬坡），
// 并由旧 TestDefaultBillingEnforcedSoftly 把这个"缓开"决定钉住。2026-10-14 用户确认收款渠道
// 已就绪，billing_enforced 翻 true（与 token_billing_enabled 同开，符合"token 唯一硬通货"模型）。
// 本断言现在钉住相反的决定：出厂必须为 true，防止不知情时被拨回 false 导致"卖出去不硬停"。
// 改动前请先看 config_defaults.go 该键注释；存量库需超管平台级同步（seed 仅对缺失键生效）。
func TestDefaultBillingEnforcedOn(t *testing.T) {
	found, v := findDefault("billing", "billing_enforced")
	if !found {
		t.Fatal("DefaultConfigs 里找不到 billing.billing_enforced")
	}
	if v != "true" {
		t.Errorf("billing_enforced 出厂应为 true（收款渠道已就绪，应急才平台级改回 false），实际=%s", v)
	}
}

// TestDefaultContentsafetyEnforce 内容安全闸门出厂为 enforce（P2-5 收口，2026-10-16）。
//
// 历史：C1 闸门 2026-09-12 上线时出厂 shadow（"首周演练误杀率后切 enforce"的爬坡口径），
// readiness 探针对 shadow 恒 Warn 提醒收口。2026-10-16 用户拍板切 enforce：LLM 机审装配
// （contentsafety_llm_reviewer.go，fail-open 有 6 例单测背书）已就位，演练期目的达成，
// 出站违规回复必须真拦（MASK 改写/BLOCK 转人工），不再"只观察不拦截"。
// 本断言钉住该决定：防止不知情时被拨回 shadow，让 PIPL/广告法风险重新退回只计数态。
// 回退 shadow 属应急动作，需平台级显式改并记录原因（见 config_defaults.go 同键注释）。
func TestDefaultContentsafetyEnforce(t *testing.T) {
	found, v := findDefault("notify", "contentsafety_mode")
	if !found {
		t.Fatal("DefaultConfigs 里找不到 notify.contentsafety_mode：键被改名或删除，本断言失去意义")
	}
	if v != "\"enforce\"" && v != "enforce" {
		t.Errorf("contentsafety_mode 出厂应为 enforce（2026-10-16 用户拍板，shadow 演练期已过；回退属需记录原因的应急），实际=%s", v)
	}
}
