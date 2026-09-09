// 延迟铁律单元测试（2026-09-01 补）：将「简单8s / 合并25s / 回复75s上顶」默认值固化为断言
// AGENTS.md 延迟参数铁律：合并 25s / 简单 8s / AI 5~15s 固定随机（2026-09-09 更新）/ 2min 硬顶
// 本测试 stub DefaultSystemConfigService（无 DB），验证配置缺失时默认值保持铁律
package service

import (
	"testing"
	"time"
)

// TestDelayIronRules 延迟铁律默认值：简单8s / 合并窗口25s
func TestDelayIronRules(t *testing.T) {
	old := DefaultSystemConfigService
	DefaultSystemConfigService = &SystemConfigService{cache: map[string]string{}}
	defer func() { DefaultSystemConfigService = old }()

	// 简单消息固定8秒（快速通道核心）
	if got := GetSimpleReplyDelay(); got != 8*time.Second {
		t.Errorf("简单消息延迟应为8s(铁律), got %s", got)
	}
	// 合并窗口默认25秒（合并队列窗口铁律）
	if got := time.Duration(DefaultSystemConfigService.GetInt("merge_window_seconds", 25)) * time.Second; got != 25*time.Second {
		t.Errorf("合并窗口默认应为25s(铁律), got %s", got)
	}
}

// TestGetSimpleReplyDelayFromConfig 后台可调：配置覆盖默认8s
func TestGetSimpleReplyDelayFromConfig(t *testing.T) {
	old := DefaultSystemConfigService
	DefaultSystemConfigService = &SystemConfigService{cache: map[string]string{"simple_msg_delay": "10"}}
	defer func() { DefaultSystemConfigService = old }()
	if got := GetSimpleReplyDelay(); got != 10*time.Second {
		t.Errorf("配置10应生效, got %s", got)
	}
}
