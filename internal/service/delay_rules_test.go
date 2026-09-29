// 延迟铁律单元测试（2026-09-01 补）：将「简单8s / 合并25s / 回复75s上顶」默认值固化为断言
// AGENTS.md 延迟参数铁律：合并 25s / 简单 8s / AI 5~15s 固定随机（2026-09-09 更新）/ 2min 硬顶
// 本测试 stub runtimecfg.DefaultSystemConfigService（无 DB），验证配置缺失时默认值保持铁律
package service

import (
	"ai-scrm/internal/runtimecfg"
	"testing"
	"time"
)

// TestDelayIronRules 延迟铁律默认值：简单8s / 合并窗口25s
func TestDelayIronRules(t *testing.T) {
	old := runtimecfg.DefaultSystemConfigService
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(map[string]string{}, nil)
	defer func() { runtimecfg.DefaultSystemConfigService = old }()

	// 简单消息固定8秒（快速通道核心）；tenantID=0 走"无租户覆盖→系统层→出厂默认"链
	if got := GetSimpleReplyDelay(0); got != 8*time.Second {
		t.Errorf("简单消息延迟应为8s(铁律), got %s", got)
	}
	// 合并窗口默认25秒（合并队列窗口铁律）
	if got := time.Duration(runtimecfg.DefaultSystemConfigService.GetInt("merge_window_seconds", 25)) * time.Second; got != 25*time.Second {
		t.Errorf("合并窗口默认应为25s(铁律), got %s", got)
	}
}

// TestGetSimpleReplyDelayFromConfig 后台可调：配置覆盖默认8s
func TestGetSimpleReplyDelayFromConfig(t *testing.T) {
	old := runtimecfg.DefaultSystemConfigService
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(map[string]string{"simple_msg_delay": "10"}, nil)
	defer func() { runtimecfg.DefaultSystemConfigService = old }()
	if got := GetSimpleReplyDelay(0); got != 10*time.Second {
		t.Errorf("配置10应生效, got %s", got)
	}
}

// TestSimpleMsgDelayTenantOverride FIX-3（2026-09-29 审计批二）：钉三层取值链——
// ① 租户覆盖层被真读到；② 未覆盖的租户回落系统层；③ 改回裸 GetInt 只查系统层时第①条必红。
// 现场：写侧 admin/config 把这些键存进租户覆盖层，旧读法只查系统缓存，"租户写了读不到"。
func TestSimpleMsgDelayTenantOverride(t *testing.T) {
	old := runtimecfg.DefaultSystemConfigService
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(
		map[string]string{"simple_msg_delay": "12"},
		map[uint]map[string]string{7: {"simple_msg_delay": "5"}},
	)
	defer func() { runtimecfg.DefaultSystemConfigService = old }()

	if got := GetSimpleReplyDelay(7); got != 5*time.Second {
		t.Errorf("租户7应读覆盖层5s, got %s", got)
	}
	if got := GetSimpleReplyDelay(8); got != 12*time.Second {
		t.Errorf("未覆盖租户应回落系统层12s, got %s", got)
	}
}
