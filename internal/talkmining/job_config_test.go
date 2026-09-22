// Package talkmining 作业层配置的租户生效断言（2026-09-23 批六，零 DB）。
//
// 为什么只测配置而不测整轮作业：RunDraftSweep 要连库枚举租户（放它的是
// smoke_channel/test_all 的 E2E 层），而本批真正的缺陷点在"读哪一层的值"——
// 出稿开关与窗口/上限/门槛都是租户后台可改的键，读错层就是"改了不生效"的静默失效。
// 配置优先级用纯函数就能钉死，不必为此拖一个 DB 依赖进来。
package talkmining

import (
	"testing"

	"ai-scrm/internal/runtimecfg"
)

// TestDraftGateIsTenantScoped 出稿闸按租户裁决：租户开=开，未覆盖=沿用系统层。
// 出稿会烧真实 token，判错的两个方向都贵——误开=白花钱，误关=租户以为在跑。
func TestDraftGateIsTenantScoped(t *testing.T) {
	restore := runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(
		map[string]string{"talkmining_draft_enabled": "false"},
		map[uint]map[string]string{9: {"talkmining_draft_enabled": "true"}},
	))
	defer restore()

	if !draftEnabledFor(9) {
		t.Error("租户9 自己开了开关，必须出稿（系统层 false 不得压住租户覆盖）")
	}
	if draftEnabledFor(10) {
		t.Error("未覆盖的租户10 必须沿用系统层 false——默认关是本作业的投产纪律")
	}
}

// TestTenantKnobsOverrideCallerValues 窗口/上限/门槛的优先级：租户覆盖 > 入参(=系统默认)。
// 入参当默认值传进来正是为了这个语义：调用方给的是全局读到的值，租户没配就照旧。
func TestTenantKnobsOverrideCallerValues(t *testing.T) {
	restore := runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(nil, map[uint]map[string]string{
		4: {"talkmining_window_days": "7", "talkmining_min_samples": "40"},
	}))
	defer restore()

	if got := cfgIntForTenant(4, "talkmining_window_days", 30); got != 7 {
		t.Errorf("租户4 窗口应取覆盖值 7，实际=%d", got)
	}
	if got := cfgIntForTenant(4, "talkmining_max_drafts_per_run", 20); got != 20 {
		t.Errorf("未覆盖的键必须沿用入参默认 20，实际=%d", got)
	}
	if got := cfgIntForTenant(5, "talkmining_window_days", 30); got != 30 {
		t.Errorf("无覆盖租户应得入参默认 30，实际=%d", got)
	}
}

// TestConfigNilServiceSafe 配置单例未初始化（单测冷路径/启动早期）时回落入参默认，不 panic。
func TestConfigNilServiceSafe(t *testing.T) {
	restore := runtimecfg.SetDefaultForTest(nil)
	defer restore()

	if !cfgBoolForTenant(1, "talkmining_draft_enabled", true) {
		t.Error("nil 服务下布尔须回落到传入默认值 true")
	}
	if got := cfgIntForTenant(1, "talkmining_window_days", 30); got != 30 {
		t.Errorf("nil 服务下整数须回落到传入默认值 30，实际=%d", got)
	}
}
