// 数据层卫生观测位单测（批六，2026-09-23）
// 钉三件真会出错的事：
//  1. 判级方向——孤儿行数越大越糟，超 warn 阈值必须转 warn，且永不转 crit（不刷群）；
//  2. 取数失败不得"假装健康"——计数报错时必须沿用上一次的值并标记 stale，
//     而不是归零显示一片绿（观测位最坏的失效形态就是把故障读成健康）；
//  3. TTL 缓存必须真的生效——两次探测只查一次库（探针会被 /status/detail 与告警 tick 反复调，
//     底层是 messages 这种全站最大的表，逐次现算等于给健康检查加了全表扫描）。
package metrics

import (
	"errors"
	"testing"
	"time"
)

// withHygieneSeams 注入计数实现与虚拟时钟，返回推进时钟的函数（推进超过 TTL 即触发重探），
// 用例结束自动复位
func withHygieneSeams(t *testing.T, orphan func() (int64, error), backlog func(int) (int64, bool, error)) func(time.Duration) {
	t.Helper()
	o, b, clk, ttl := orphanCountFn, archiveBacklogFn, hygieneNow, hygieneProbeTTL
	t.Cleanup(func() {
		orphanCountFn, archiveBacklogFn, hygieneNow, hygieneProbeTTL = o, b, clk, ttl
		ResetHygieneCache()
	})
	if orphan != nil {
		orphanCountFn = orphan
	}
	if backlog != nil {
		archiveBacklogFn = backlog
	}
	cur := time.Unix(1700000000, 0)
	hygieneNow = func() time.Time { return cur }
	hygieneProbeTTL = 10 * time.Minute
	ResetHygieneCache()
	return func(d time.Duration) { cur = cur.Add(d) }
}

// findHygiene 从检查结果里按名取项
func findHygiene(t *testing.T, checks []HealthCheck, name string) HealthCheck {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("缺观测位 %s（实际 %v）", name, checks)
	return HealthCheck{}
}

// TestHygieneOrphanGrading 孤儿行数判级：0/低于阈值=ok，超 warn=warn，任何量都不 crit
func TestHygieneOrphanGrading(t *testing.T) {
	cases := []struct {
		name string
		val  int64
		want HealthStatus
	}{
		{"零孤儿", 0, StatusOK},
		{"阈值内", 99, StatusOK},
		{"达 warn 阈值", 100, StatusWarn},
		{"极端放大也不 crit", 9_999_999, StatusWarn},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := tc.val
			withHygieneSeams(t, func() (int64, error) { return v, nil },
				func(int) (int64, bool, error) { return 0, false, nil })
			got := findHygiene(t, hygieneChecks(), "orphan_messages")
			if got.Status != tc.want {
				t.Errorf("value=%d 判级 got=%s want=%s", v, got.Status, tc.want)
			}
			if got.CritAt != "-" {
				t.Errorf("孤儿观测位不应设 crit 阈值（不影响客户，只会刷群），实际 %q", got.CritAt)
			}
		})
	}
}

// TestHygieneStaleKeepsLastValue 取数失败必须沿用上值并标记 stale（不得归零装健康）
func TestHygieneStaleKeepsLastValue(t *testing.T) {
	var fail bool
	advance := withHygieneSeams(t, func() (int64, error) {
		if fail {
			return 0, errors.New("模拟：库抖动")
		}
		return 42, nil
	}, func(int) (int64, bool, error) { return 0, false, nil })

	first := findHygiene(t, hygieneChecks(), "orphan_messages")
	if first.Value != "42" {
		t.Fatalf("首探应报真实值 42，实际 %q", first.Value)
	}
	// 时钟走过 TTL（触发重探）+ 这次取数失败：值必须仍是 42，且明示沿用了旧值
	// （勿用 ResetHygieneCache 模拟过期——那会把缓存本身清空，测不到"故障沿用上值"这条路径）
	fail = true
	advance(11 * time.Minute)
	second := findHygiene(t, hygieneChecks(), "orphan_messages")
	if second.Value == "0" || second.Value == "" {
		t.Fatalf("取数失败被当成 0 孤儿（故障读成健康）：%q", second.Value)
	}
	if !contains(second.Value, "42") {
		t.Fatalf("应沿用上一次值 42，实际 %q", second.Value)
	}
	if !contains(second.Value, "沿用") {
		t.Errorf("stale 必须在 value 里可见（不能悄悄端出旧数），实际 %q", second.Value)
	}
}

// TestHygieneProbeCachedWithinTTL TTL 内只查一次库
func TestHygieneProbeCachedWithinTTL(t *testing.T) {
	var calls int
	withHygieneSeams(t, func() (int64, error) { calls++; return 7, nil },
		func(int) (int64, bool, error) { calls++; return 0, false, nil })
	hygieneChecks()
	hygieneChecks()
	hygieneChecks()
	if calls != 2 {
		t.Errorf("TTL 内三次探测应只查库 2 次（孤儿+归档各一），实际 %d 次", calls)
	}
}

// TestHygieneArchiveBacklogDisabled 归档默认关闭：报 disabled 且绝不查库
func TestHygieneArchiveBacklogDisabled(t *testing.T) {
	var backlogQueried bool
	withHygieneSeams(t, func() (int64, error) { return 0, nil },
		func(days int) (int64, bool, error) {
			// days<=0 时 archiveBacklogFn 仍被调用，但实现必须自行短路：
			// 这里模拟"真库实现"——只在 days>0 时才认为发生了查询
			if days > 0 {
				backlogQueried = true
			}
			return 0, days > 0, nil
		})
	got := findHygiene(t, hygieneChecks(), "message_archive_backlog")
	if got.Value != "disabled" {
		t.Errorf("message_archive_days=0 应报 disabled，实际 %q", got.Value)
	}
	if backlogQueried {
		t.Errorf("归档关闭时不应触发按年龄的计数查询（全表扫描代价）")
	}
	if got.Status != StatusOK {
		t.Errorf("未启用归档属设计内选择，不得判红，实际 %s", got.Status)
	}
}

// contains 小工具（避免为一处断言引入 strings 依赖造成误解：本包其它文件已用 strings，
// 这里显式局部实现只为让用例意图一目了然）
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
