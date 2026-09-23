// 获客活码纯函数层单测：短码形态与字符集、落地链接拼接、扫码去重窗口、
// 归因裁决的全分支（含"跨租户一律不写"这条主闸）、漏斗谓词与计数同源、单位纪律。
//
// 为什么这些值得单独测而不等冒烟：归因裁决的每一条分支都是"错了会静默污染数据"的判断，
// 冒烟只能覆盖走得通的那一条；纯函数不碰库，才能把 disabled/mismatch/already
// 这些需要造既有状态的分支一次性摆全。
package acquisition

import (
	"strings"
	"testing"
	"time"

	"ai-scrm/internal/analytics"
)

// TestNewCodeShapeAndCharset 码必须是 8 位、只含安全字符集、且肉眼可抄。
// 钉死"不含 0/O/1/I/L"：这条是产品决定（印在海报上的字不能被念错），
// 不是实现细节，谁将来手滑把字符集换成 base62 就会在这里红。
func TestNewCodeShapeAndCharset(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		c, err := NewCode()
		if err != nil {
			t.Fatalf("生成短码失败: %v", err)
		}
		if len(c) != CodeLength {
			t.Fatalf("短码长度应为 %d，实得 %q", CodeLength, c)
		}
		for _, r := range c {
			if !strings.ContainsRune(codeAlphabet, r) {
				t.Fatalf("短码含字符集外的字符 %q（码=%s）", r, c)
			}
			if strings.ContainsRune("0O1IL", r) {
				t.Fatalf("短码出现易混字符 %q（码=%s）——字符集必须去掉 0/O/1/I/L", r, c)
			}
		}
		if seen[c] {
			t.Fatalf("2000 次生成就撞码了：%s，随机源不可信", c)
		}
		seen[c] = true
	}
}

// TestNormalizeCode 手输/口令传播大小写与空白要容得下，形态非法要在查库之前就被挡掉。
func TestNormalizeCode(t *testing.T) {
	if got := NormalizeCode("  abcdefgh "); got != "ABCDEFGH" {
		t.Fatalf("大小写与空白应被规范化，实得 %q", got)
	}
	cases := map[string]string{
		"":                     "空串",
		"ABC":                  "长度不足",
		strings.Repeat("A", 9): "长度超出",
		"ABCD-EFGH":            "含连字符（遍历/拼接产物）",
		"ABCD0EGH":             "含被剔除的字符 0",
	}
	for raw, why := range cases {
		if got := NormalizeCode(raw); got != "" {
			t.Fatalf("%s 的输入 %q 应判非法（回空串），实得 %q", why, raw, got)
		}
	}
}

// TestBuildLink 基址尾斜杠、空基址两种形态都要稳定（海报上印的是这个字符串）。
func TestBuildLink(t *testing.T) {
	if got := BuildLink("https://go.example.com/", "ABCD2345"); got != "https://go.example.com/client?code=ABCD2345" {
		t.Fatalf("带尾斜杠的基址应被规整，实得 %q", got)
	}
	if got := BuildLink("", "ABCD2345"); got != "/client?code=ABCD2345" {
		t.Fatalf("无基址应回相对路径（同源部署可用），实得 %q", got)
	}
}

// TestShouldCountScan 同一访客窗口内重复打开不重复计；窗口外要计；
// 匿名"纯打开"没有去重依据，只能照记——这条把"这一格天生带噪"钉成代码里的注释。
func TestShouldCountScan(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	in := now.Add(-3 * time.Minute)
	out := now.Add(-11 * time.Minute)
	if ShouldCountScan("vk1", in, now) {
		t.Fatalf("窗口内重复扫码不得再计一次")
	}
	if !ShouldCountScan("vk1", out, now) {
		t.Fatalf("超出 %v 窗口应重新计数", ScanDedupeWindow)
	}
	if !ShouldCountScan("vk1", time.Time{}, now) {
		t.Fatalf("首次扫码必须计数")
	}
	if !ShouldCountScan("", in, now) || !ShouldCountScan("   ", out, now) {
		t.Fatalf("无访客键的打开没有去重依据，只能照计（口径见注释）")
	}
}

// TestDecideApplyBranches 归因裁决全分支。
// 重点是最后两条：**跨租户一律拒**、**无租户语境（tenantID=0）一律拒**——
// 这两条守的是"把 A 渠道的钱记到 B 头上"这种查不出来的事故。
func TestDecideApplyBranches(t *testing.T) {
	cases := []struct {
		name                 string
		raw                  string
		found, active        bool
		codeTenant, tenantID uint
		already              bool
		wantApply            bool
		wantReason           string
	}{
		{"没带码", "", true, true, 7, 7, false, false, ApplyReasonAbsent},
		{"形态非法", "ab", true, true, 7, 7, false, false, ApplyReasonMalformed},
		{"码不存在", "ABCDEFGH", false, false, 0, 7, false, false, ApplyReasonNotFound},
		{"码已停用", "ABCDEFGH", true, false, 7, 7, false, false, ApplyReasonDisabled},
		{"跨租户", "ABCDEFGH", true, true, 8, 7, false, false, ApplyReasonTenantMismatch},
		{"无租户语境", "ABCDEFGH", true, true, 7, 0, false, false, ApplyReasonTenantMismatch},
		{"已有首触码", "ABCDEFGH", true, true, 7, 7, true, false, ApplyReasonAlreadySet},
		{"正常命中", "abcdefgh", true, true, 7, 7, false, true, ApplyOK},
	}
	for _, tc := range cases {
		got := DecideApply(tc.raw, tc.found, tc.active, tc.codeTenant, tc.tenantID, tc.already)
		if got.Apply != tc.wantApply || got.Reason != tc.wantReason {
			t.Fatalf("%s：期望 apply=%v reason=%s，实得 apply=%v reason=%s",
				tc.name, tc.wantApply, tc.wantReason, got.Apply, got.Reason)
		}
	}
}

// TestFunnelStagesComeFromAnalytics "什么算已留资/到店/成交"全项目只准有一个答案。
// 这条测的不是活码，是**两个看板不得各说一遍**：贡献度看板改阶段集合，
// 活码漏斗必须跟着动，否则就是给老板摆两张互相打脸的表。
func TestFunnelStagesComeFromAnalytics(t *testing.T) {
	deep := FunnelCustomer{CustomerID: 1, Spoke: true, Stage: "delivered"}
	for _, m := range []string{MetricLead, MetricArrived, MetricOrdered} {
		if !MatchFunnelMetric(m, deep) {
			t.Fatalf("delivered 客户应命中 %s（阶段集合来自 analytics：%v）", m, analytics.OrderedStages)
		}
	}
	fresh := FunnelCustomer{CustomerID: 2, Spoke: false, Stage: "ai_connected"}
	for _, m := range []string{MetricLead, MetricArrived, MetricOrdered} {
		if MatchFunnelMetric(m, fresh) {
			t.Fatalf("刚建联客户不得命中 %s", m)
		}
	}
	if !MatchFunnelMetric(MetricNew, fresh) {
		t.Fatalf("归因到本码的客户本身就是新增客户")
	}
	if MatchFunnelMetric(MetricSpoke, fresh) || !MatchFunnelMetric(MetricSpoke, deep) {
		t.Fatalf("开过口判据错位")
	}
	if MatchFunnelMetric("made_up_metric", deep) {
		t.Fatalf("未知指标必须判不命中（不许默认回某一份名单）")
	}
}

// TestCountsAndFilterUseOnePredicate 计数与逐个筛选必须同源（D4 立下的结构约束）。
// 写法：同一批人，一边累加、一边逐个谓词筛，两侧数字必须恒等。
func TestCountsAndFilterUseOnePredicate(t *testing.T) {
	cs := []FunnelCustomer{
		{CustomerID: 1, Spoke: true, Stage: "lead_captured"},
		{CustomerID: 2, Spoke: false, Stage: "arrived"},
		{CustomerID: 3, Spoke: true, Stage: "ordered"},
		{CustomerID: 4, Spoke: false, Stage: "ai_connected"},
	}
	counts := ClassifyFunnelCounts(cs)
	for _, m := range FunnelMetricCodes {
		manual := int64(0)
		for _, c := range cs {
			if MatchFunnelMetric(m, c) {
				manual++
			}
		}
		if counts[m] != manual {
			t.Fatalf("指标 %s 计数与谓词逐条判定不等：%d vs %d", m, counts[m], manual)
		}
	}
	// 期望值本身也钉一次：改阶段集合或谓词时，这里会指出"哪个数变了"
	want := map[string]int64{MetricNew: 4, MetricSpoke: 2, MetricLead: 3, MetricArrived: 2, MetricOrdered: 1}
	for m, w := range want {
		if counts[m] != w {
			t.Fatalf("指标 %s 期望 %d，实得 %d", m, w, counts[m])
		}
	}
}

// TestScansAreNotDrillable 单位纪律：扫码次数单位是"次"，不是"人"，点开会自相矛盾。
func TestScansAreNotDrillable(t *testing.T) {
	if IsDrillableMetric(MetricScans) {
		t.Fatalf("扫码次数是事件级指标，不得进客户级下钻白名单")
	}
	for _, m := range FunnelMetricCodes {
		if !IsDrillableMetric(m) {
			t.Fatalf("客户级指标 %s 应在可下钻白名单内", m)
		}
	}
	if len(FunnelMetricCodes) != len(FunnelMetricLabels)-1 {
		t.Fatalf("可下钻白名单 %d 个、口径文案 %d 个，二者应只差扫码次数那一格",
			len(FunnelMetricCodes), len(FunnelMetricLabels))
	}
}

// TestValidateCreateReasons 入参拒绝要回稳定码（前端与冒烟按码分支，不解析中文）。
func TestValidateCreateReasons(t *testing.T) {
	cases := []struct {
		name, channel, want string
	}{
		{"", ChannelDouyin, "name_required"},
		{"   ", ChannelDouyin, "name_required"},
		{strings.Repeat("门", MaxNameRunes+1), ChannelDouyin, "name_too_long"},
		{"门店立牌", "", "channel_required"},
		{"门店立牌", "视频号", "channel_unknown"},
		{"门店立牌", ChannelStore, ""},
	}
	for _, tc := range cases {
		if got := ValidateCreate(tc.name, tc.channel); got != tc.want {
			t.Fatalf("名称=%q 渠道=%q 期望原因码 %q，实得 %q", tc.name, tc.channel, tc.want, got)
		}
	}
}

// TestClampDays 窗口钳位（外部传 0/负数/一年以上的都不得原样进查询）。
func TestClampDays(t *testing.T) {
	for raw, want := range map[int]int{0: DefaultWindowDays, -5: DefaultWindowDays, 1: 1, 9999: MaxWindowDays} {
		if got := ClampDays(raw); got != want {
			t.Fatalf("days=%d 期望钳成 %d，实得 %d", raw, want, got)
		}
	}
}
