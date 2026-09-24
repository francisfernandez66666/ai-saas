// 会话存档观测位单测（E8-4，2026-09-24）
//
// 盯的是"分级判据本身"：这一项存在的意义是把三种在后台页面长得一模一样的状态分开——
// 没装配、没开、开了但取数器没接。所以每一档都要正向断（该 warn 的 warn），
// 还要配反向用例（不该 warn 的不许 warn），否则"永远 warn"和"永远 ok"都能让测试全绿。
//
// 与冒烟的分工：这里测判级与缓存（不碰库），真库形态/接口契约在 smoke §三十七。
package metrics

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// withArchiveProbe 临时装配探针取数与时钟，测完复位（含 TTL 缓存，防串到别的用例）
func withArchiveProbe(t *testing.T, fn func() (ArchiveProbe, error), now func() time.Time) {
	t.Helper()
	prevFn, prevNow, prevTTL := archiveProbeFn, archiveProbeNow, archiveProbeTTL
	SetArchiveProbe(fn) // 装配即清缓存
	archiveProbeNow = now
	archiveProbeTTL = 5 * time.Minute
	t.Cleanup(func() {
		SetArchiveProbe(prevFn)
		archiveProbeNow, archiveProbeTTL = prevNow, prevTTL
	})
}

// fixedClock 返回一个可推进的假时钟（测 TTL 复用，不起真等待）
func fixedClock(start time.Time) (func() time.Time, func(time.Duration)) {
	now := start
	return func() time.Time { return now }, func(d time.Duration) { now = now.Add(d) }
}

// TestArchiveCheckNotWired 未装配（gateway / 单测进程）判 ok 而不是故障。
// 这一档如果误判 warn，每个不跑存档的进程都会给运维群加一条假告警。
func TestArchiveCheckNotWired(t *testing.T) {
	ResetArchiveProbe()
	t.Cleanup(ResetArchiveProbe)
	ch := archiveCheck()
	if ch.Name != "chat_archive" {
		t.Fatalf("观测项名应为 chat_archive，实得 %s", ch.Name)
	}
	if ch.Status != StatusOK || ch.Value != "not_wired" {
		t.Fatalf("未装配应 ok/not_wired，实得 %s %s", ch.Status, ch.Value)
	}
}

// TestArchiveCheckGrades 五档分级逐格钉死（含"不该报警"的两格）
func TestArchiveCheckGrades(t *testing.T) {
	cases := []struct {
		name       string
		probe      ArchiveProbe
		wantStatus HealthStatus
		wantIn     string // value 必含的稳定片段
	}{
		{
			name:       "没开存档——出厂默认，不报警",
			probe:      ArchiveProbe{FetcherReady: false},
			wantStatus: StatusOK,
			wantIn:     "disabled",
		},
		{
			name:       "开了两个通道但取数器没接——warn 且说清 sdk_not_built",
			probe:      ArchiveProbe{EnabledChannels: 2, FetcherReady: false, SyncRan: true},
			wantStatus: StatusWarn,
			wantIn:     "sdk_not_built",
		},
		{
			name:       "接了取数器但上一轮有失败码——warn 并带码",
			probe:      ArchiveProbe{EnabledChannels: 1, FetcherReady: true, SyncRan: true, SyncErrors: "archive_key_missing=1"},
			wantStatus: StatusWarn,
			wantIn:     "archive_key_missing=1",
		},
		{
			name:       "接了取数器但本进程没跑过轮——warn（ticker 没装配是另一件事）",
			probe:      ArchiveProbe{EnabledChannels: 1, FetcherReady: true, SyncRan: false},
			wantStatus: StatusWarn,
			wantIn:     "ticker_not_run",
		},
		{
			name:       "解密失败占比过半——warn",
			probe:      ArchiveProbe{EnabledChannels: 1, FetcherReady: true, SyncRan: true, Stored: 100, Failed: 60},
			wantStatus: StatusWarn,
			wantIn:     "解密失败",
		},
		{
			name:       "一切正常——ok，且数字逐个回显",
			probe:      ArchiveProbe{EnabledChannels: 1, FetcherReady: true, SyncRan: true, Stored: 100, Failed: 1},
			wantStatus: StatusOK,
			wantIn:     "1 通道 / 落库 100 行 / 解密失败 1 行",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := tc.probe
			withArchiveProbe(t, func() (ArchiveProbe, error) { return probe, nil }, time.Now)
			ch := archiveCheck()
			if ch.Status != tc.wantStatus {
				t.Fatalf("分级应为 %s，实得 %s（value=%s desc=%s）", tc.wantStatus, ch.Status, ch.Value, ch.Desc)
			}
			if !strings.Contains(ch.Value, tc.wantIn) && !strings.Contains(ch.Desc, tc.wantIn) {
				t.Fatalf("value/desc 应含 %q，实得 value=%q desc=%q", tc.wantIn, ch.Value, ch.Desc)
			}
			// 存档缺数据不刷群：任何一档都不许判 crit
			if ch.Status == StatusCrit {
				t.Fatalf("会话存档观测位不得判 crit（合规留痕不是线上故障）")
			}
		})
	}
}

// TestArchiveProbeCacheAndStale TTL 内只取数一次；取数失败沿用上次数值并标 stale。
//
// 反向自证的那一半：如果不缓存，/status/detail 每被 scrape 一次就为展示扫一遍存档表；
// 如果失败时清零，一次库抖动会把"开了 3 个通道"显示成"没开"，比不报更糟。
func TestArchiveProbeCacheAndStale(t *testing.T) {
	calls := 0
	ok := ArchiveProbe{EnabledChannels: 3, FetcherReady: true, SyncRan: true, Stored: 42}
	withArchiveProbe(t, func() (ArchiveProbe, error) {
		calls++
		if calls > 1 {
			return ArchiveProbe{}, errors.New("db down") // 第二次起模拟取数失败
		}
		return ok, nil
	}, time.Now)

	if ch := archiveCheck(); !strings.Contains(ch.Value, "落库 42") {
		t.Fatalf("首轮应显示真实数字，实得 %q", ch.Value)
	}
	if calls != 1 {
		t.Fatalf("TTL 内不得重复取数，实得调用 %d 次", calls)
	}

	// 推进过 TTL：第二次调用失败 → 沿用旧值 + stale 说明
	archiveProbeNow = func() time.Time { return time.Now().Add(time.Hour) }
	ch := archiveCheck()
	if calls != 2 {
		t.Fatalf("过期后应重新取数，实得调用 %d 次", calls)
	}
	if !strings.Contains(ch.Value, "落库 42") {
		t.Fatalf("取数失败必须沿用上一次数字（不得假装没开存档），实得 %q", ch.Value)
	}
	if !strings.Contains(ch.Desc, "本轮查询失败") {
		t.Fatalf("取数失败必须标注 stale，实得 %q", ch.Desc)
	}

	// 复位后再取：装配为 nil 即回到 not_wired，不残留旧缓存
	ResetArchiveProbe()
	if ch := archiveCheck(); ch.Value != "not_wired" {
		t.Fatalf("卸载探针后应回 not_wired，实得 %q", ch.Value)
	}
}

// TestArchiveProbeZeroEnabledNotCached "没开存档"那一次取数不得进 TTL 缓存。
//
// 双向都要钉：零通道时昂贵查询根本不存在（只是一次走索引的 Pluck），缓存省不下什么；
// 而管理员刚在后台把存档打开后 5 分钟内探针继续报 "disabled"，
// 就是"探针说没这回事、数据其实在库里"——冒烟 §三十七 前一次 /status/detail 会先取到零通道态，
// 若缓存生效，本节后面那两条断言会稳定读到老状态。
func TestArchiveProbeZeroEnabledNotCached(t *testing.T) {
	enabled := false
	calls := 0
	withArchiveProbe(t, func() (ArchiveProbe, error) {
		calls++
		if enabled {
			return ArchiveProbe{EnabledChannels: 1, FetcherReady: false}, nil
		}
		return ArchiveProbe{}, nil
	}, time.Now)

	if ch := archiveCheck(); ch.Value != "disabled" {
		t.Fatalf("首轮应报 disabled，实得 %q", ch.Value)
	}
	enabled = true // 模拟管理员刚开了存档（同一分钟内）
	ch := archiveCheck()
	if calls != 2 {
		t.Fatalf("零通道结果不得占用 TTL 缓存，实得调用 %d 次", calls)
	}
	if !strings.Contains(ch.Value, "sdk_not_built") {
		t.Fatalf("开启后当场就该报 sdk_not_built，实得 %q", ch.Value)
	}
	// 反向自证：一旦真开了通道，第二次探测就必须回到"缓存生效"（不为展示扫存档表）
	if ch2 := archiveCheck(); calls != 2 || !strings.Contains(ch2.Value, "sdk_not_built") {
		t.Fatalf("非零通道结果必须走 TTL 缓存，实得调用 %d 次 value=%q", calls, ch2.Value)
	}
}

// TestArchiveCheckInHealthList 观测项必须真的出现在健康清单里。
// 判级函数单独测全绿、却忘了挂进 hygieneChecks，是这类探针最常见的"写了等于没写"。
func TestArchiveCheckInHealthList(t *testing.T) {
	// 另两项卫生探针一并桩掉：本包不连库，跑真查询会在 nil 句柄上 panic
	withHygieneSeams(t, func() (int64, error) { return 0, nil }, func(int) (int64, bool, error) { return 0, false, nil })
	withArchiveProbe(t, func() (ArchiveProbe, error) {
		return ArchiveProbe{EnabledChannels: 1, FetcherReady: false}, nil
	}, time.Now)
	checks := hygieneChecks()
	var found *HealthCheck
	for i := range checks {
		if checks[i].Name == "chat_archive" {
			found = &checks[i]
		}
	}
	if found == nil {
		t.Fatalf("hygieneChecks 未包含 chat_archive 观测项")
	}
	if found.Status != StatusWarn {
		t.Fatalf("清单里的 chat_archive 应继承判级，实得 %s", found.Status)
	}
}
