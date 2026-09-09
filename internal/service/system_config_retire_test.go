// Package service 系统配置停用键清理逻辑单元测试（2026-09-09）。
// 背景：CalcHumanlikeDelay 改为固定 5~15s 后，旧"打字+线下偏移"相关配置键不再被读取，
// 若仍留在 DefaultConfigs / DB 存量中会在后台配置中心展示死配置误导运营。
// 本测试锁定三类不变式：
//   1. DefaultConfigs 不再包含任何已停用键（新装环境不会写入死配置）；
//   2. retiredConfigKeys 清单与 DefaultConfigs 无交集（不误删仍在用的键）；
//   3. 仍有意义的回复速度键（合并窗口/简单延迟/到店两段式/处理锁超时/延迟模式）必须保留。
package service

import (
	"testing"
)

// TestRetiredConfigKeysExcludedFromDefaults 已停用键绝不能再出现在 DefaultConfigs 中
func TestRetiredConfigKeysExcludedFromDefaults(t *testing.T) {
	for _, cfg := range DefaultConfigs {
		if retiredConfigKeys[cfg.Key] {
			t.Errorf("DefaultConfigs 仍包含已停用键 %q，新装环境会再次写入死配置", cfg.Key)
		}
	}
}

// TestRetiredConfigKeysAllListed 所有曾被写入、现已停用的键都应登记进 retiredConfigKeys
func TestRetiredConfigKeysAllListed(t *testing.T) {
	// 回归保护：一旦 CalcHumanlikeDelay 等实现改动使新键失效，但忘记登记清理清单，
	// 后台会重新出现死配置。此处列出已知应列为停用的键，缺失即报错。
	expected := []string{
		// 延迟公式废弃（打字+线下偏移 → 固定 5~15s）
		"reply_min_delay", "max_reply_delay",
		"offline_offset_work_simple", "offline_offset_work_medium", "offline_offset_work_complex",
		"offline_offset_offwork_simple", "offline_offset_offwork_medium", "offline_offset_offwork_complex",
		// 三层分流之前的保留档位（无读取方）
		"l1_simple_delay", "l2_simple_delay", "l3_simple_delay", "l1_complex_delay",
		"off_work_multiplier", "weekend_multiplier",
	}
	for _, k := range expected {
		if !retiredConfigKeys[k] {
			t.Errorf("已停用键 %q 未登记进 retiredConfigKeys", k)
		}
	}
}

// TestActiveReplySpeedKeysPreserved 回复速度分类中仍生效的键必须保留在 DefaultConfigs
func TestActiveReplySpeedKeysPreserved(t *testing.T) {
	mustKeep := []string{
		"merge_window_seconds", // 合并窗口（二分消息合并）
		"simple_msg_delay",     // 简单消息快速通道
		"store_visit_first_delay",   // 到店第一段
		"store_visit_second_delay",  // 到店第二段
		"processing_lock_timeout",   // 队列自愈
		"max_merge_messages",        // 合并条数上限
		"reply_delay_mode",          // normal/instant
	}
	found := map[string]bool{}
	for _, cfg := range DefaultConfigs {
		if cfg.Category == "reply_speed" {
			found[cfg.Key] = true
		}
	}
	for _, k := range mustKeep {
		if !found[k] {
			t.Errorf("仍生效的回复速度键 %q 不应被移除，DefaultConfigs 缺失", k)
		}
	}
	// 提示：若未来确有需要重新引入可调延迟区间，优先替换为新键名（如 ai_reply_delay_range [5,15]），
	// 而不是复用已停用键——历史键名语义已与当前实现无关。
}