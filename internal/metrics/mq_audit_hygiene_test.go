// 消息中心死信观测位单测（G-15③，2026-09-24）
//
// 钉四件真会出错的事：
//  1. 判级方向——死信>0 必须转 warn，且永不转 crit（单条死信的正确动作是查台账重放，不是半夜刷群）；
//  2. 未接库不得报 0——把"没数据可读"显示成"一条死信都没有"是本观测位最坏的失效形态，
//     而 G-15 之前的整个消息中心观测面就是这种失效（台账只记发布阶段，消费结果从不回写）；
//  3. 取数失败沿用旧值并标 stale；
//  4. TTL 内不重复查库（探针被 /status/detail 与告警 tick 反复调用）。
//
// 反证用例（缺它护栏会空转）：把计数函数换成恒定报错的实现，观测位必须报"沿用"而不是绿。
package metrics

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// withMQAuditSeams 注入死信计数与虚拟时钟，返回推进时钟的函数；用例结束自动复位。
// 另外两项卫生探针一并接成固定值：本包多数用例不连库，让它们走真实实现会在 nil db.DB 上炸
// （hygieneChecks 一次组装四项，任一项现算都会摸到库）。
func withMQAuditSeams(t *testing.T, fn func() (int64, error)) func(time.Duration) {
	t.Helper()
	oldFn, oldNow, oldTTL := deadLetterCountFn, hygieneNow, mqAuditProbeTTL
	oldOrphan, oldBacklog := orphanCountFn, archiveBacklogFn
	t.Cleanup(func() {
		deadLetterCountFn, hygieneNow, mqAuditProbeTTL = oldFn, oldNow, oldTTL
		orphanCountFn, archiveBacklogFn = oldOrphan, oldBacklog
		ResetHygieneCache()
	})
	deadLetterCountFn = fn
	orphanCountFn = func() (int64, error) { return 0, nil }
	archiveBacklogFn = func(int) (int64, bool, error) { return 0, false, nil }
	cur := time.Unix(1700000000, 0)
	hygieneNow = func() time.Time { return cur }
	mqAuditProbeTTL = 10 * time.Minute
	ResetMQAuditCache()
	return func(d time.Duration) { cur = cur.Add(d) }
}

// TestMQDeadLetterGrading 死信判级：0=ok，≥warn 阈值=warn，任何量都不 crit
func TestMQDeadLetterGrading(t *testing.T) {
	cases := []struct {
		name string
		val  int64
		want HealthStatus
	}{
		{"零死信", 0, StatusOK},
		{"一条即 warn（默认阈值 1）", 1, StatusWarn},
		{"大规模丢失也不 crit", 9_999_999, StatusWarn},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := tc.val
			withMQAuditSeams(t, func() (int64, error) { return v, nil })
			got := findHygiene(t, hygieneChecks(), "mq_dead_letters")
			if got.Status != tc.want {
				t.Errorf("value=%d 判级 got=%s want=%s", v, got.Status, tc.want)
			}
			if got.CritAt != "-" {
				t.Errorf("死信观测位不得设 crit（单条丢事件不该刷群），实际 %q", got.CritAt)
			}
		})
	}
}

// TestMQDeadLettersPresentInHygieneChecks 观测项必须真的挂在清单里——
// hygieneChecks 是 /status/detail 唯一的卫生项来源，漏挂等于这一整块白做。
func TestMQDeadLettersPresentInHygieneChecks(t *testing.T) {
	withMQAuditSeams(t, func() (int64, error) { return 3, nil })
	var names []string
	for _, c := range hygieneChecks() {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "orphan_messages,message_archive_backlog,chat_archive,mq_dead_letters" {
		t.Fatalf("卫生观测项清单漂移（顺序与成员都要稳定，冒烟按名取值）：%v", names)
	}
}

// TestMQAuditNotWiredIsNotGreen 未接库/取数报错时不得报"零死信"：
// 首探无历史值可沿用 → 值必须带"沿用/查询失败"字样，让看的人知道这条绿是虚的。
func TestMQAuditNotWiredIsNotGreen(t *testing.T) {
	var fail bool
	advance := withMQAuditSeams(t, func() (int64, error) {
		if fail {
			return 0, errors.New("模拟：库抖动")
		}
		return 5, nil
	})
	first := findHygiene(t, hygieneChecks(), "mq_dead_letters")
	if first.Value != "5" {
		t.Fatalf("首探应报真实值 5，实际 %q", first.Value)
	}
	fail = true
	advance(11 * time.Minute) // 走过 TTL 触发重探（勿用 Reset 模拟过期，那会把缓存清空测不到沿用上值）
	second := findHygiene(t, hygieneChecks(), "mq_dead_letters")
	if second.Value == "0" {
		t.Fatalf("取数失败被洗成 0 条死信（故障读成健康）")
	}
	if !strings.Contains(second.Value, "5") || !strings.Contains(second.Value, "沿用") {
		t.Errorf("应沿用旧值 5 并明示 stale，实际 %q", second.Value)
	}
}

// TestMQAuditProbeCachedWithinTTL TTL 内不重复查库
func TestMQAuditProbeCachedWithinTTL(t *testing.T) {
	var calls int
	withMQAuditSeams(t, func() (int64, error) { calls++; return 2, nil })
	for i := 0; i < 3; i++ {
		hygieneChecks()
	}
	if calls != 1 {
		t.Errorf("TTL 内三次探测应只查库 1 次，实际 %d 次", calls)
	}
}

// TestMQDeadLettersAccessor MQDeadLetters() 与观测项必须同源（冒烟直出字段读的是这个入口，
// 若它另起一条查询口径，字段与 checks 就会各说各话）
func TestMQDeadLettersAccessor(t *testing.T) {
	withMQAuditSeams(t, func() (int64, error) { return 7, nil })
	if got := MQDeadLetters(); got != 7 {
		t.Errorf("MQDeadLetters=%d want 7", got)
	}
	if v := findHygiene(t, hygieneChecks(), "mq_dead_letters").Value; v != "7" {
		t.Errorf("观测项 value=%q 与 MQDeadLetters 不同源", v)
	}
}
