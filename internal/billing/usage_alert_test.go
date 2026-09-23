// 用量预警（D3）单测：纯裁决表驱动 + sweep 生命周期（幂等去重 / 通道缺失分支 / 开关默认关）。
//
// 三条口径决定各自有测：①一轮只发最高档 ②余额桶 0 余额不发 ③平台通道坏了不落库、
// 租户没人可收才落空通道行。外呼一律走 newSilentD3Sink 计数桩——本包单测与开发库共库，
// 真发信等于给库里的过期租户管理员群发。巡检入口一律用 ForTenants 定向版，
// 避免给不在测试范围内的真实租户写"已通知"锚点行（唯一键会让该档本账期永不再发）。
package billing

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/testutil"

	"gorm.io/gorm"
)

// TestMain 复用 testutil 的跳过计数（DB 不可用时本地 SKIP、CI Fatal，不静默绿）
func TestMain(m *testing.M) {
	os.Exit(testutil.RunMain(m))
}

// intSliceJSON 把 Go 切片拼成配置里存的 JSON 字面量（[80,95,100]）
func intSliceJSON(in []int) string {
	parts := make([]string, 0, len(in))
	for _, v := range in {
		parts = append(parts, fmt.Sprint(v))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// useUsageAlertCfg 装一个内存配置桩覆盖预警相关键，返回还原函数。
// 存的形式与 DB 里一致（字符串值带 JSON 引号），否则解析路径与生产不同构。
// thresholds 传 nil 表示不写该键（走 SafeCfgIntSlice 的默认值分支）。
func useUsageAlertCfg(t *testing.T, enabled bool, thresholds []int, balanceBelow int) func() {
	t.Helper()
	cfg := map[string]string{
		"usage_alert_enabled":             fmt.Sprintf("%t", enabled),
		"usage_alert_token_balance_below": fmt.Sprintf("%d", balanceBelow),
		"dunning_enabled":                 "false",
	}
	if thresholds != nil {
		cfg["usage_alert_thresholds"] = intSliceJSON(thresholds)
	}
	restore := runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(cfg, nil))
	return restore
}

// ---------------------------------------------------------------------------
// 纯裁决层（无 DB）：越档 / 未越 / 不限额 / 零余额 / 跨档跳跃
// ---------------------------------------------------------------------------

// TestUsageAlertDecideTable 越档裁决表：未越/越档/跳档/不限额/零余额各给出确定结论。
func TestUsageAlertDecideTable(t *testing.T) {
	base := tenantUsageRow{ID: 1, Name: "甲", Code: "c1"}
	cases := []struct {
		name    string
		row     tenantUsageRow
		balance int64
		want    []usageAlertHit // 期望命中的（metric, threshold, exhausted）三元组按序
	}{
		{
			name: "未到档_不发",
			row:  func() tenantUsageRow { r := base; r.UsedAICalls = 79; r.MaxAICallsMonthly = 100; return r }(),
			want: nil,
		},
		{
			name: "越80_只发一档",
			row:  func() tenantUsageRow { r := base; r.UsedAICalls = 85; r.MaxAICallsMonthly = 100; return r }(),
			want: []usageAlertHit{{TenantID: 1, Metric: model.UsageMetricMonthlyCalls, Threshold: 80, UsagePct: 85, Remaining: 15}},
		},
		{
			name: "一次跨三档_只发最高100且标记耗尽",
			row:  func() tenantUsageRow { r := base; r.UsedAICalls = 100; r.MaxAICallsMonthly = 100; return r }(),
			want: []usageAlertHit{{TenantID: 1, Metric: model.UsageMetricMonthlyCalls, Threshold: 100, UsagePct: 100, Remaining: 0, Exhausted: true}},
		},
		{
			name: "次轨超发_百分比钳100不报130",
			row:  func() tenantUsageRow { r := base; r.UsedAICalls = 130; r.MaxAICallsMonthly = 100; return r }(),
			want: []usageAlertHit{{TenantID: 1, Metric: model.UsageMetricMonthlyCalls, Threshold: 100, UsagePct: 100, Remaining: -30, Exhausted: true}},
		},
		{
			name: "月度配额为0_视为不限额不发",
			row:  func() tenantUsageRow { r := base; r.UsedAICalls = 9999; r.MaxAICallsMonthly = 0; return r }(),
			want: nil,
		},
		{
			name: "月度token越95",
			row: func() tenantUsageRow {
				r := base
				r.MonthlyTokenUsed = 960_000
				r.MonthlyTokenQuota = 1_000_000
				return r
			}(),
			want: []usageAlertHit{{TenantID: 1, Metric: model.UsageMetricMonthlyTokens, Threshold: 95, UsagePct: 96, Remaining: 40_000}},
		},
		{
			name:    "余额桶在阈值下方_提示",
			row:     func() tenantUsageRow { r := base; r.TokenBalance = 150_000; return r }(),
			balance: 200_000,
			want:    []usageAlertHit{{TenantID: 1, Metric: model.UsageMetricTokenBalance, Threshold: 200_000, Remaining: 150_000}},
		},
		{
			name:    "余额为0_本来就没有_不发",
			row:     func() tenantUsageRow { r := base; r.TokenBalance = 0; return r }(),
			balance: 200_000,
			want:    nil,
		},
		{
			name:    "余额只剩阈值一成以内_算耗尽",
			row:     func() tenantUsageRow { r := base; r.TokenBalance = 15_000; return r }(),
			balance: 200_000,
			want:    []usageAlertHit{{TenantID: 1, Metric: model.UsageMetricTokenBalance, Threshold: 200_000, Remaining: 15_000, Exhausted: true}},
		},
		{
			name: "三指标同时越档_各发自己那条",
			row: func() tenantUsageRow {
				r := base
				r.UsedAICalls, r.MaxAICallsMonthly = 100, 100
				r.MonthlyTokenUsed, r.MonthlyTokenQuota = 1_000_000, 1_000_000
				r.TokenBalance = 1_000
				return r
			}(),
			balance: 200_000,
			want: []usageAlertHit{
				{Metric: model.UsageMetricMonthlyCalls, Threshold: 100, Exhausted: true},
				{Metric: model.UsageMetricMonthlyTokens, Threshold: 100, Exhausted: true},
				{Metric: model.UsageMetricTokenBalance, Threshold: 200_000, Exhausted: true},
			},
		},
	}
	thresholds := []int{80, 95, 100}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decideUsageAlerts(tc.row, thresholds, tc.balance)
			if len(got) != len(tc.want) {
				t.Fatalf("命中条数=%d 期望=%d 明细=%+v", len(got), len(tc.want), got)
			}
			for i, w := range tc.want {
				g := got[i]
				if g.Metric != w.Metric || g.Threshold != w.Threshold || g.Exhausted != w.Exhausted {
					t.Fatalf("第%d条 metric/threshold/exhausted=%s/%d/%v 期望=%s/%d/%v",
						i+1, g.Metric, g.Threshold, g.Exhausted, w.Metric, w.Threshold, w.Exhausted)
				}
				if w.UsagePct != 0 && g.UsagePct != w.UsagePct {
					t.Fatalf("第%d条 pct=%d 期望=%d", i+1, g.UsagePct, w.UsagePct)
				}
				if w.Remaining != 0 && g.Remaining != w.Remaining {
					t.Fatalf("第%d条 remaining=%d 期望=%d", i+1, g.Remaining, w.Remaining)
				}
			}
		})
	}
}

// TestPctHitGuards pctHit 的三类拒绝入参：不限额、负用量、空档位表。
// 负用量在真实库里不该出现，但它是"pct 变负→越不过任何档"之外的另一种污染（配置改成负阈值时
// 也不该反过来给所有人发信）。
func TestPctHitGuards(t *testing.T) {
	if _, ok := pctHit(1, model.UsageMetricMonthlyCalls, 50, 0, []int{80}, "x"); ok {
		t.Fatal("max<=0 应视为不限额，不得命中")
	}
	if _, ok := pctHit(1, model.UsageMetricMonthlyCalls, -1, 100, []int{80}, "x"); ok {
		t.Fatal("used<0 应拒绝")
	}
	if _, ok := pctHit(1, model.UsageMetricMonthlyCalls, 100, 100, nil, "x"); ok {
		t.Fatal("空档位表应不命中")
	}
	if _, ok := pctHit(1, model.UsageMetricMonthlyCalls, 100, 100, []int{0, -5}, "x"); ok {
		t.Fatal("非法档位（<=0）应被跳过")
	}
}

// TestNormalizeInts 配置清洗：钳范围 + 去重 + 升序（运维手写成乱序也不能让低档覆盖高档）
func TestNormalizeInts(t *testing.T) {
	got := normalizeInts([]int{100, 80, 0, -5, 80, 130, 95}, 1, 100)
	want := []int{80, 95, 100}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("清洗结果=%v 期望=%v", got, want)
	}
	if len(normalizeInts(nil, 1, 100)) != 0 {
		t.Fatal("nil 输入应得空切片")
	}
	// dunning 档位用 [0,3650] 范围：0 天档必须保留（到期当天即第一响）
	if fmt.Sprint(normalizeInts([]int{14, 0, 7, 3}, 0, 3650)) != fmt.Sprint([]int{0, 3, 7, 14}) {
		t.Fatal("lo=0 时 0 档不得被丢弃")
	}
}

// TestUsageAlertPeriodKey 账期锚必须是月粒度：跨月同档要能再发一次
func TestUsageAlertPeriodKey(t *testing.T) {
	a := usageAlertPeriodKey(time.Date(2026, 9, 30, 23, 0, 0, 0, time.Local))
	b := usageAlertPeriodKey(time.Date(2026, 10, 1, 0, 30, 0, 0, time.Local))
	if a != "2026-09" || b != "2026-10" {
		t.Fatalf("账期锚=%s/%s，期望 2026-09/2026-10", a, b)
	}
}

// ---------------------------------------------------------------------------
// sweep 生命周期（DB）
// ---------------------------------------------------------------------------

// setupUsageAlertTenant 造一家"配额已知"的在用租户，返回租户 ID 与清理
func setupUsageAlertTenant(t *testing.T, code string, used, max int) uint {
	t.Helper()
	tid := testutil.CreateTenantCode(t, code)
	if err := db.DB.Model(&model.Tenant{}).Where("id = ?", tid).Updates(map[string]any{
		"status": "active", "used_ai_calls": used, "max_ai_calls_monthly": max,
		"monthly_token_used": 0, "monthly_token_quota": 0, "token_balance": 0,
	}).Error; err != nil {
		t.Fatalf("配额预置: %v", err)
	}
	// 只清自己家的留痕，别碰别人的账本
	t.Cleanup(func() {
		db.DB.Where("tenant_id = ?", tid).Delete(&model.UsageAlert{})
	})
	return tid
}

func countUsageAlerts(tenantID uint) int64 {
	var n int64
	db.DB.Model(&model.UsageAlert{}).Where("tenant_id = ?", tenantID).Count(&n)
	return n
}

// TestUsageAlertSweepIdempotent 触发→落锚→重跑不重发；且外呼参数正确（档=100、耗尽惊动群）
func TestUsageAlertSweepIdempotent(t *testing.T) {
	testutil.SetupTestDB(t)
	restore := useUsageAlertCfg(t, true, []int{80, 95, 100}, 200000)
	defer restore()

	var log d3CallLog
	log.smtpReady, log.groupReady = true, true
	log.admins = []string{"boss@example.com"}
	defer withD3Sink(newSilentD3Sink(&log))()

	tid := setupUsageAlertTenant(t, "ua_full", 100, 100)
	period := usageAlertPeriodKey(time.Now())

	if n := SweepUsageAlertsForTenants([]uint{tid}); n != 1 {
		t.Fatalf("首轮应新落 1 条，实际 %d", n)
	}
	if len(log.usage) != 1 {
		t.Fatalf("应外呼 1 封，实际 %d", len(log.usage))
	}
	call := log.usage[0]
	if call.Pct != 100 || !call.Exhausted || call.Remaining != 0 {
		t.Fatalf("外呼入参 pct/exhausted/remaining=%d/%v/%d，期望 100/true/0", call.Pct, call.Exhausted, call.Remaining)
	}
	if len(log.groups) != 1 || !strings.Contains(log.groups[0], "用量耗尽") {
		t.Fatalf("耗尽档应惊动平台群一次，实际=%v", log.groups)
	}
	var row model.UsageAlert
	if err := db.DB.Where("tenant_id = ? AND metric = ? AND period_key = ?", tid, model.UsageMetricMonthlyCalls, period).
		First(&row).Error; err != nil {
		t.Fatalf("留痕行未落库: %v", err)
	}
	if row.Threshold != 100 || row.Channels != "email,group" {
		t.Fatalf("留痕 threshold/channels=%d/%s，期望 100/email,group", row.Threshold, row.Channels)
	}

	// 幂等：同月重跑不再外呼、不再落锚（唯一键 + 先查后插双保险）
	if n := SweepUsageAlertsForTenants([]uint{tid}); n != 0 {
		t.Fatalf("重跑应 0 条，实际 %d", n)
	}
	if len(log.usage) != 1 {
		t.Fatalf("重跑不得再外呼，累计 %d 封", len(log.usage))
	}
	if countUsageAlerts(tid) != 1 {
		t.Fatalf("留痕应恰好 1 条，实际 %d", countUsageAlerts(tid))
	}
}

// TestUsageAlertLowTierCovered 首轮即 100% 时，只发最高档一封，且低档不再被后续巡检补发
// （这是"一轮三封邮件"的历史断点，回归锚点）
func TestUsageAlertLowTierCovered(t *testing.T) {
	testutil.SetupTestDB(t)
	restore := useUsageAlertCfg(t, true, []int{80, 95, 100}, 200000)
	defer restore()
	var log d3CallLog
	log.smtpReady, log.groupReady = true, false
	log.admins = []string{"boss@example.com"}
	defer withD3Sink(newSilentD3Sink(&log))()

	tid := setupUsageAlertTenant(t, "ua_jump", 100, 100)
	if n := SweepUsageAlertsForTenants([]uint{tid}); n != 1 {
		t.Fatalf("跨档首轮应只落 1 条，实际 %d", n)
	}
	// 用量回落到 85%（月中人工调高配额）：80 档此时才"首次越过"，应补发 80 档
	db.DB.Model(&model.Tenant{}).Where("id = ?", tid).Update("used_ai_calls", 85)
	if n := SweepUsageAlertsForTenants([]uint{tid}); n != 1 {
		t.Fatalf("回落到 85%% 应补发 80 档 1 条，实际 %d", n)
	}
	if len(log.usage) != 2 {
		t.Fatalf("累计外呼应 2 封，实际 %d", len(log.usage))
	}
	var ths []int
	db.DB.Model(&model.UsageAlert{}).Where("tenant_id = ?", tid).Order("threshold ASC").Pluck("threshold", &ths)
	if fmt.Sprint(ths) != fmt.Sprint([]int{80, 100}) {
		t.Fatalf("留痕档位=%v，期望 [80 100]", ths)
	}
}

// TestUsageAlertSwitchOff 开关关闭（出厂默认）必须零开销：不外呼、不落库
func TestUsageAlertSwitchOff(t *testing.T) {
	testutil.SetupTestDB(t)
	restore := useUsageAlertCfg(t, false, []int{80, 95, 100}, 200000)
	defer restore()
	var log d3CallLog
	log.smtpReady, log.groupReady = true, true
	log.admins = []string{"boss@example.com"}
	defer withD3Sink(newSilentD3Sink(&log))()

	tid := setupUsageAlertTenant(t, "ua_off", 100, 100)
	if n := SweepUsageAlertsForTenants([]uint{tid}); n != 0 {
		t.Fatalf("开关关闭应 0 条，实际 %d", n)
	}
	if len(log.usage) != 0 || len(log.groups) != 0 {
		t.Fatalf("开关关闭不得外呼，usage=%v groups=%v", log.usage, log.groups)
	}
	if countUsageAlerts(tid) != 0 {
		t.Fatal("开关关闭不得留痕")
	}
}

// TestUsageAlertInfraDownNoAnchor 平台通道全未配 = 我方问题：命中不落库，
// 配好后下一轮自愈补发（宁可晚说，不可假装说过）。
func TestUsageAlertInfraDownNoAnchor(t *testing.T) {
	testutil.SetupTestDB(t)
	restore := useUsageAlertCfg(t, true, []int{80, 95, 100}, 200000)
	defer restore()
	var log d3CallLog // smtpReady/groupReady 均 false
	defer withD3Sink(newSilentD3Sink(&log))()

	tid := setupUsageAlertTenant(t, "ua_infra", 100, 100)
	if n := SweepUsageAlertsForTenants([]uint{tid}); n != 0 {
		t.Fatalf("通道未就绪应 0 条，实际 %d", n)
	}
	if countUsageAlerts(tid) != 0 {
		t.Fatal("通道未就绪不得落锚——否则配好后本账期永远不再提醒")
	}
	// 自愈：配置就绪后同一轮立即补发
	log.smtpReady, log.admins = true, []string{"boss@example.com"}
	if n := SweepUsageAlertsForTenants([]uint{tid}); n != 1 {
		t.Fatalf("通道就绪后应自愈补发 1 条，实际 %d", n)
	}
	if countUsageAlerts(tid) != 1 {
		t.Fatalf("自愈后应留痕 1 条，实际 %d", countUsageAlerts(tid))
	}
}

// TestUsageAlertNoAdminRecordsEmptyChannel 租户没有绑定邮箱的管理员 = 它的常态：
// 落一条 channels 为空的行，停止每小时重复"命中→发不出→写日志"。
func TestUsageAlertNoAdminRecordsEmptyChannel(t *testing.T) {
	testutil.SetupTestDB(t)
	restore := useUsageAlertCfg(t, true, []int{80, 95, 100}, 200000)
	defer restore()
	var log d3CallLog
	log.smtpReady, log.groupReady = true, false
	log.admins = nil // 没人可收
	defer withD3Sink(newSilentD3Sink(&log))()

	tid := setupUsageAlertTenant(t, "ua_nobody", 100, 100)
	// 群通道也没配，但 SMTP 就绪 → infraReady=true，走"没人可收→落空通道行"分支
	if n := SweepUsageAlertsForTenants([]uint{tid}); n != 0 {
		t.Fatalf("没发出去就不该计入 sent，实际 %d", n)
	}
	var row model.UsageAlert
	if err := db.DB.Where("tenant_id = ?", tid).First(&row).Error; err != nil {
		t.Fatalf("应落空通道留痕行: %v", err)
	}
	if row.Channels != "" {
		t.Fatalf("channels 应为空串，实际 %q", row.Channels)
	}
	if len(log.usage) != 0 {
		t.Fatal("无收件人不得外呼")
	}
}

// TestUsageAlertTenantScope 定向巡检不得波及其它租户（这是 ForTenants 入口存在的全部理由）
func TestUsageAlertTenantScope(t *testing.T) {
	testutil.SetupTestDB(t)
	restore := useUsageAlertCfg(t, true, []int{80, 95, 100}, 200000)
	defer restore()
	var log d3CallLog
	log.smtpReady, log.groupReady = true, false
	log.admins = []string{"boss@example.com"}
	defer withD3Sink(newSilentD3Sink(&log))()

	inScope := setupUsageAlertTenant(t, "ua_in", 100, 100)
	outScope := setupUsageAlertTenant(t, "ua_out", 100, 100)
	if n := SweepUsageAlertsForTenants([]uint{inScope}); n != 1 {
		t.Fatalf("在册租户应落 1 条，实际 %d", n)
	}
	if countUsageAlerts(outScope) != 0 {
		t.Fatal("不在本轮 scope 的租户被写了留痕——污染真实租户账本")
	}
	// 列表接口按租户取，跨租户不可见
	if rows := ListTenantUsageAlerts(outScope, 10); len(rows) != 0 {
		t.Fatalf("别家留痕不得出现在本家列表，实际 %d 条", len(rows))
	}
	rows := ListTenantUsageAlerts(inScope, 10)
	if len(rows) != 1 || rows[0].Metric != model.UsageMetricMonthlyCalls || rows[0].Threshold != 100 {
		t.Fatalf("本家留痕=%+v，期望 1 条 monthly_calls/100", rows)
	}
}

// TestListTenantUsageAlertsLimit 非法 limit 回退默认值，不放大查询
func TestListTenantUsageAlertsLimit(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := setupUsageAlertTenant(t, "ua_limit", 0, 0)
	period := usageAlertPeriodKey(time.Now())
	for _, th := range []int{80, 95, 100} {
		if !insertUsageAlert(usageAlertHit{TenantID: tid, Metric: model.UsageMetricMonthlyCalls, Threshold: th, UsagePct: th}, period, "email") {
			t.Fatalf("直插档位%d留痕失败", th)
		}
	}
	if got := ListTenantUsageAlerts(tid, 0); len(got) != 3 {
		t.Fatalf("limit=0 应回退默认 20 并返回全部 3 条，实际 %d", len(got))
	}
	if got := ListTenantUsageAlerts(tid, 99999); len(got) != 3 {
		t.Fatalf("limit 超上限应钳到 20 且不影响结果，实际 %d", len(got))
	}
	// 倒序：先高后低
	if got := ListTenantUsageAlerts(tid, 2); len(got) != 2 || got[0].Threshold != 100 || got[1].Threshold != 95 {
		t.Fatalf("排序应 threshold 倒序，实际 %+v", got)
	}
	if usageAlertExists(tid, model.UsageMetricMonthlyCalls, 80, period) != true {
		t.Fatal("唯一键预检应命中已落档位")
	}
	if usageAlertExists(tid, model.UsageMetricMonthlyCalls, 80, period+"-x") {
		t.Fatal("不同账期不得视为已发")
	}
}

// TestUsageAlertConfigView 生效配置回读：阈值清洗后升序、通道就绪态如实反映
func TestUsageAlertConfigView(t *testing.T) {
	testutil.SetupTestDB(t)
	restore := useUsageAlertCfg(t, true, []int{100, 80, 95}, 500000)
	defer restore()
	var log d3CallLog
	log.smtpReady, log.groupReady = false, true
	defer withD3Sink(newSilentD3Sink(&log))()

	v := CurrentUsageAlertConfig()
	if !v.Enabled || v.TokenBalanceBelow != 500000 {
		t.Fatalf("配置回读=%+v", v)
	}
	if fmt.Sprint(v.Thresholds) != fmt.Sprint([]int{80, 95, 100}) {
		t.Fatalf("阈值应清洗为升序，实际 %v", v.Thresholds)
	}
	if v.EmailReady || !v.GroupReady {
		t.Fatalf("通道就绪态=%+v，期望 email=false group=true", v)
	}
}

// TestUsageAlertInsertConflict 唯一键冲突时 insertUsageAlert 必须返回 false（多实例并发落锚
// 只有一个人算"说过话"，另一条按已发处理）
func TestUsageAlertInsertConflict(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := setupUsageAlertTenant(t, "ua_conflict", 0, 0)
	period := usageAlertPeriodKey(time.Now())
	hit := usageAlertHit{TenantID: tid, Metric: model.UsageMetricTokenBalance, Threshold: 200000, Remaining: 1000}
	if !insertUsageAlert(hit, period, "email") {
		t.Fatal("首次落锚应成功")
	}
	if insertUsageAlert(hit, period, "email") {
		t.Fatal("重复落锚应失败（唯一键 ux_usage_alert_once）")
	}
	var n int64
	db.DB.Model(&model.UsageAlert{}).Where("tenant_id = ? AND metric = ?", tid, model.UsageMetricTokenBalance).Count(&n)
	if n != 1 {
		t.Fatalf("冲突后应只有 1 行，实际 %d", n)
	}
	// 空 channels 也允许落锚（"没人可收"留痕），与冲突分支不混淆
	if !insertUsageAlert(usageAlertHit{TenantID: tid, Metric: model.UsageMetricMonthlyCalls, Threshold: 80}, period, "") {
		t.Fatal("空通道留痕应可落库")
	}
	var ch string
	db.DB.Model(&model.UsageAlert{}).Where("tenant_id = ? AND metric = ?", tid, model.UsageMetricMonthlyCalls).
		Pluck("channels", &ch)
	if ch != "" {
		t.Fatalf("空通道应存空串，实际 %q", ch)
	}
	// 清理：断言 gorm 不吞错、删干净后确实查不到
	if err := db.DB.Where("tenant_id = ?", tid).Delete(&model.UsageAlert{}).Error; err != nil {
		t.Fatalf("清理留痕失败: %v", err)
	}
	var after model.UsageAlert
	if err := db.DB.Where("tenant_id = ?", tid).First(&after).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("清理后应查不到，实际 err=%v", err)
	}
}
