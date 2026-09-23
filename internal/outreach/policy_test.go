// 触达策略纯函数单测：静默时段顺延（含跨午夜/同日/非法配置）、通道窗口判定、文案闸门。
// 全部不连库、不依赖词库加载——判定钩子用注入替身，CI 与本地同口径。
package outreach

import (
	"strings"
	"testing"
	"time"

	"ai-scrm/internal/contentsafety"
	"ai-scrm/internal/model"
)

// utcPlus8 固定时区：顺延结果必须按"运营所在时区"判，不能随 CI 机器 TZ 漂移。
var utcPlus8 = time.FixedZone("UTC+8", 8*3600)

// at 构造 UTC+8 的某个本地时刻
func at(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, utcPlus8)
}

// TestDeferPastQuiet 静默时段顺延的四类边界：跨午夜前后半段、同日段、窗口外不动。
func TestDeferPastQuiet(t *testing.T) {
	cases := []struct {
		name string
		spec string
		in   time.Time
		want time.Time
	}{
		{"跨午夜段前半_顺延到次日段末", "21:00-09:00", at(2026, 9, 23, 22, 30), at(2026, 9, 24, 9, 0)},
		{"跨午夜段后半_顺延到当日段末", "21:00-09:00", at(2026, 9, 23, 3, 5), at(2026, 9, 23, 9, 0)},
		{"恰在段末不再顺延", "21:00-09:00", at(2026, 9, 23, 9, 0), at(2026, 9, 23, 9, 0)},
		{"段起始时刻算静默", "21:00-09:00", at(2026, 9, 23, 21, 0), at(2026, 9, 24, 9, 0)},
		{"同日段内顺延到当日段末", "09:00-17:00", at(2026, 9, 23, 12, 0), at(2026, 9, 23, 17, 0)},
		{"窗口外原样返回", "21:00-09:00", at(2026, 9, 23, 15, 20), at(2026, 9, 23, 15, 20)},
		{"空配置不顺延", "", at(2026, 9, 23, 22, 30), at(2026, 9, 23, 22, 30)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DeferPastQuiet(c.in, c.spec, utcPlus8)
			if !got.Equal(c.want) {
				t.Fatalf("顺延结果错：得 %s 期望 %s", got.In(utcPlus8).Format(time.RFC3339), c.want.Format(time.RFC3339))
			}
		})
	}
}

// TestDeferPastQuietFailOpen 非法配置一律原样返回：配置写错不能让排期能力整体失效。
func TestDeferPastQuietFailOpen(t *testing.T) {
	base := at(2026, 9, 23, 22, 30)
	for _, spec := range []string{"abc", "25:00-09:00", "21:60-09:00", "21:00", "09:00-09:00", "  ", "21:00-x"} {
		if got := DeferPastQuiet(base, spec, utcPlus8); !got.Equal(base) {
			t.Fatalf("非法配置 %q 应原样返回，得 %s", spec, got)
		}
	}
}

// TestWindowAllowsPerChannelType 通道分档：企微应用可主动推；微信侧两通道受 48h 窗口约束。
func TestWindowAllowsPerChannelType(t *testing.T) {
	now := at(2026, 9, 23, 12, 0)
	recent := now.Add(-10 * time.Hour)
	stale := now.Add(-60 * time.Hour)

	cases := []struct {
		name        string
		chType      string
		last        time.Time
		hours       int
		wantAllowed bool
		wantReason  string
	}{
		{"企微应用无窗口限制", "wecom_app", stale, 48, true, ""},
		{"企微应用连来句都没有也放行", "wecom_app", time.Time{}, 48, true, ""},
		{"微信客服窗口内放行", "wecom_kf", recent, 48, true, ""},
		{"微信客服超窗拒绝", "wecom_kf", stale, 48, false, model.OutreachReasonOutOfWindow},
		{"公众号超窗拒绝", "wechat_mp", stale, 48, false, model.OutreachReasonOutOfWindow},
		{"未知通道按最严窗口", "mystery", stale, 48, false, model.OutreachReasonOutOfWindow},
		{"窗口参数非法退回48h", "wecom_kf", now.Add(-50 * time.Hour), 0, false, model.OutreachReasonOutOfWindow},
		{"窗口参数非法退回48h_窗内放行", "wecom_kf", now.Add(-20 * time.Hour), 0, true, ""},
		{"无通道类型即不可达", "", recent, 48, false, model.OutreachReasonNoChannel},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, reason := WindowAllows(c.chType, c.last, now, c.hours)
			if ok != c.wantAllowed || reason != c.wantReason {
				t.Fatalf("判定错：得 (%v,%q) 期望 (%v,%q)", ok, reason, c.wantAllowed, c.wantReason)
			}
		})
	}
}

// fakeCheck 用固定规则替身替换词库判定（BLOCK 词=x，MASK 词=脏）。
func fakeCheck(res contentsafety.Result) func(string) contentsafety.Result {
	return func(text string) contentsafety.Result { return res }
}

// TestValidateContent 文案闸门：空/超长/BLOCK 拒，MASK 用脱敏后的文本落库。
func TestValidateContent(t *testing.T) {
	old := CheckFunc
	t.Cleanup(func() { CheckFunc = old })

	if _, reason := ValidateContent("   "); reason != model.OutreachReasonContentFlagged {
		t.Fatalf("空白文案应被拒，得 reason=%q", reason)
	}
	if _, reason := ValidateContent(strings.Repeat("车", maxContentRunes+1)); reason != model.OutreachReasonContentFlagged {
		t.Fatal("超长文案应被拒")
	}
	// 恰好等于上限必须放行（边界别写成 > 与 >= 之争的暗坑）
	if cleaned, reason := ValidateContent(strings.Repeat("车", maxContentRunes)); reason != "" || cleaned == "" {
		t.Fatalf("等于上限应放行，得 reason=%q", reason)
	}

	CheckFunc = fakeCheck(contentsafety.Result{Hit: true, Level: contentsafety.LevelBlock, Cleaned: ""})
	if _, reason := ValidateContent("涉敏内容"); reason != model.OutreachReasonContentFlagged {
		t.Fatalf("BLOCK 命中应被拒，得 reason=%q", reason)
	}

	CheckFunc = fakeCheck(contentsafety.Result{Hit: true, Level: contentsafety.LevelMask, Cleaned: "哥，**店里有活动"})
	cleaned, reason := ValidateContent("哥，脏话店里有活动")
	if reason != "" || cleaned != "哥，**店里有活动" {
		t.Fatalf("MASK 命中应落脱敏文本，得 (%q,%q)", cleaned, reason)
	}

	CheckFunc = fakeCheck(contentsafety.Result{Cleaned: "原样"})
	if cleaned, reason := ValidateContent("  周末来试驾  "); reason != "" || cleaned != "原样" {
		t.Fatalf("未命中应走 Cleaned，得 (%q,%q)", cleaned, reason)
	}
	// 钩子返回空 Cleaned 时退回 trim 后原文（防词库降级把正文丢了）
	CheckFunc = fakeCheck(contentsafety.Result{})
	if cleaned, _ := ValidateContent("  周末来试驾  "); cleaned != "周末来试驾" {
		t.Fatalf("Cleaned 为空应退回原文，得 %q", cleaned)
	}
}

// TestClockMinutesAndSpan 解析与区间判定的自证（含跨午夜"或"语义）。
func TestClockMinutesAndSpan(t *testing.T) {
	if m, ok := clockMinutes(" 09:30 "); !ok || m != 570 {
		t.Fatalf("09:30 应解析为 570，得 %d ok=%v", m, ok)
	}
	if _, ok := clockMinutes("9:70"); ok {
		t.Fatal("分钟越界应解析失败")
	}
	if !inQuietSpan(22*60, 21*60, 9*60) || !inQuietSpan(3*60, 21*60, 9*60) {
		t.Fatal("跨午夜段两侧都应判为静默")
	}
	if inQuietSpan(12*60, 21*60, 9*60) {
		t.Fatal("12:00 不在 21:00-09:00 静默段内")
	}
	if !inQuietSpan(12*60, 9*60, 17*60) || inQuietSpan(18*60, 9*60, 17*60) {
		t.Fatal("同日段区间语义判定错")
	}
	if inQuietSpan(12*60, 9*60, 9*60) {
		t.Fatal("start==end 视为不设静默段")
	}
}
