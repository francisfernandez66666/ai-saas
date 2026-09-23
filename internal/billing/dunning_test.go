// 到期催缴状态机（D3 dunning）单测：planDunning 纯函数边界 + 一整条生命周期回环。
//
// 生命周期用例刻意写成**一个函数内的顺序回环**（第1档→同日重跑不重复→跳到第8天补发→
// 第15天停用→到账自动解除），因为这套状态机真正的价值与风险都在"档与档之间不重复、
// 封与解之间不误伤"，拆成孤立用例就看不出来了。
//
// 两条红线在用例里显式断言：
//  1. 巡检入口只用 SweepDunningForTenants（定向）——全表版会真改库里其它过期租户的 status；
//  2. 外呼一律用 newSilentD3Sink 计数桩——真 SMTP 凭证与群 webhook 就在 .env 里，
//     跑一次测试等于给真实客户群发催缴信（连只调 suspendTenantByDunning 的用例也要装桩，
//     那个函数成功路径上有 d3.Group 群播报）。
package billing

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/testutil"
)

// useDunningCfg 装催缴配置桩（steps 传 nil 走默认键兜底分支）
func useDunningCfg(t *testing.T, enabled bool, steps []int, suspendAfter int) func() {
	t.Helper()
	cfg := map[string]string{
		"dunning_enabled":            fmt.Sprintf("%t", enabled),
		"dunning_suspend_after_days": fmt.Sprintf("%d", suspendAfter),
		"usage_alert_enabled":        "false",
	}
	if steps != nil {
		cfg["dunning_steps"] = intSliceJSON(steps)
	}
	return runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(cfg, nil))
}

// setTenantExpiredAt 把租户置为 expired 并锚定到期时刻（清掉上一轮宽限期展示值）；
// 同时登记清理：序列行、审计行、租户状态三样都恢复，不留脏数据给下一用例。
func setTenantExpiredAt(t *testing.T, tid uint, expiredAt time.Time) uint {
	t.Helper()
	if err := db.DB.Model(&model.Tenant{}).Where("id = ?", tid).Updates(map[string]any{
		"status": "expired", "expired_at": expiredAt, "grace_period_end_at": nil,
	}).Error; err != nil {
		t.Fatalf("置过期失败 tenant=%d: %v", tid, err)
	}
	t.Cleanup(func() {
		db.DB.Where("tenant_id = ?", tid).Delete(&model.BillingDunning{})
		db.DB.Where("tenant_id = ?", tid).Delete(&model.TenantAuditLog{})
		db.DB.Model(&model.Tenant{}).Where("id = ?", tid).Updates(map[string]any{
			"status": "active", "expired_at": nil, "grace_period_end_at": nil,
		})
	})
	return tid
}

// loadDunning 读回催缴序列行（断言用，缺失即 fatal）
func loadDunning(t *testing.T, tid uint) model.BillingDunning {
	t.Helper()
	var row model.BillingDunning
	if err := db.DB.Where("tenant_id = ?", tid).First(&row).Error; err != nil {
		t.Fatalf("读催缴序列失败 tenant=%d: %v", tid, err)
	}
	return row
}

// tenantStatus 读租户当前状态
func tenantStatus(t *testing.T, tid uint) string {
	t.Helper()
	var s string
	if err := db.DB.Model(&model.Tenant{}).Where("id = ?", tid).Pluck("status", &s).Error; err != nil {
		t.Fatalf("读租户状态失败: %v", err)
	}
	return s
}

// tenantGrace 读宽限期展示列。**必须走结构体整行读**：Pluck 到 **time.Time 时，
// 列为 NULL 不会把已存的 Go 值清零，用 Pluck 判"是否已清空"必然读到上一次的旧值（实测踩到）。
func tenantGrace(t *testing.T, tid uint) *time.Time {
	t.Helper()
	var tt model.Tenant
	if err := db.DB.First(&tt, tid).Error; err != nil {
		t.Fatalf("读租户失败: %v", err)
	}
	return tt.GracePeriodEndAt
}

// countAudit 审计动作计数
func countAudit(tid uint, action string) int64 {
	var n int64
	db.DB.Model(&model.TenantAuditLog{}).Where("tenant_id = ? AND action = ?", tid, action).Count(&n)
	return n
}

// countAuditUser 按"租户+动作+操作人"数审计行（人工动作的归因断言用）
func countAuditUser(tid uint, action string, uid uint) int64 {
	var n int64
	db.DB.Model(&model.TenantAuditLog{}).
		Where("tenant_id = ? AND action = ? AND user_id = ?", tid, action, uid).Count(&n)
	return n
}

// sameTime 时刻比对（DB 往返有微秒/时区精度差，容忍 1 分钟）
func sameTime(a, b time.Time) bool {
	d := a.Sub(b)
	if d < 0 {
		d = -d
	}
	return d < time.Minute
}

// ---------------------------------------------------------------------------
// 纯裁决层：档位边界
// ---------------------------------------------------------------------------

// TestPlanDunningTable 催缴档位裁决表：跳档只补发最高档、序列走完即 exhausted、只催不封时永不停用。
func TestPlanDunningTable(t *testing.T) {
	steps := []int{0, 3, 7, 14}
	cases := []struct {
		name         string
		stage        int
		dayPast      int
		steps        []int
		suspendAfter int
		want         dunningAction
	}{
		{
			name: "还没到期_什么都不做", stage: 0, dayPast: -1, steps: steps, suspendAfter: 14,
			want: dunningAction{Stage: 0, DayPast: -1},
		},
		{
			name: "到期当天_发第1档并预告将停用", stage: 0, dayPast: 0, steps: steps, suspendAfter: 14,
			want: dunningAction{Notify: true, Stage: 1, DayPast: 0, WillSuspend: true},
		},
		{
			name: "同档重跑_不再发", stage: 1, dayPast: 1, steps: steps, suspendAfter: 14,
			want: dunningAction{Stage: 1, DayPast: 1, WillSuspend: true},
		},
		{
			name: "正常推进到第2档", stage: 1, dayPast: 3, steps: steps, suspendAfter: 14,
			want: dunningAction{Notify: true, Stage: 2, DayPast: 3, WillSuspend: true},
		},
		{
			name: "巡检停两天_一次跨两档只补最高", stage: 1, dayPast: 8, steps: steps, suspendAfter: 14,
			want: dunningAction{Notify: true, Stage: 3, DayPast: 8, WillSuspend: true},
		},
		{
			name: "档位发完但未到停用阈值_安静等封禁", stage: 4, dayPast: 10, steps: steps, suspendAfter: 14,
			want: dunningAction{Stage: 4, DayPast: 10, WillSuspend: true},
		},
		{
			name: "宽限期满_停用且记为末档", stage: 3, dayPast: 14, steps: steps, suspendAfter: 14,
			want: dunningAction{Notify: true, Stage: 4, DayPast: 14, Suspend: true},
		},
		{
			name: "封禁阈值0_只催不封", stage: 0, dayPast: 99, steps: steps, suspendAfter: 0,
			want: dunningAction{Notify: true, Stage: 4, DayPast: 99},
		},
		{
			name: "空档位表_只靠停用兜底", stage: 0, dayPast: 5, steps: nil, suspendAfter: 14,
			want: dunningAction{Stage: 0, DayPast: 5, WillSuspend: true},
		},
		{
			name: "未经清洗的乱序配置_仍按越过最高档下标算", stage: 1, dayPast: 7, steps: []int{7, 0, 14}, suspendAfter: 14,
			// 命中下标 0（7）与 1（0）→ target=2；14 未越过。sweep 传进来的是 normalize 后的升序，
			// 这条只钉住"planDunning 按下标给档位号"这一实现口径，不代表生产会拿到乱序表。
			want: dunningAction{Notify: true, Stage: 2, DayPast: 7, WillSuspend: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := planDunning(tc.stage, tc.dayPast, tc.steps, tc.suspendAfter)
			if got != tc.want {
				t.Fatalf("动作=%+v 期望=%+v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 生命周期回环
// ---------------------------------------------------------------------------

// TestDunningLifecycle 第1档 → 同日重跑不重复 → 跳到第8天补发 → 第15天停用 → 到账自动解除
func TestDunningLifecycle(t *testing.T) {
	testutil.SetupTestDB(t)
	defer useDunningCfg(t, true, []int{0, 3, 7, 14}, 14)()

	var log d3CallLog
	log.smtpReady, log.groupReady = true, true
	log.admins = []string{"boss@example.com"}
	defer withD3Sink(newSilentD3Sink(&log))()

	dueAt := time.Now()
	tid := setTenantExpiredAt(t, testutil.CreateTenantCode(t, "dun_life"), dueAt)

	// ① 到期当天：新建序列 + 发第 1 档 + 宽限期展示值落列
	if n := SweepDunningForTenants([]uint{tid}); n != 1 {
		t.Fatalf("首轮应 1 次动作，实际 %d", n)
	}
	row := loadDunning(t, tid)
	if row.Status != model.DunningStatusRunning || row.Stage != 1 {
		t.Fatalf("序列=%s stage=%d，期望 running/1", row.Status, row.Stage)
	}
	if row.DueAt == nil || !sameTime(*row.DueAt, dueAt) {
		t.Fatalf("due_at 应锚定本轮到期时刻，实际 %v", row.DueAt)
	}
	if row.NextNotifyAt == nil {
		t.Fatal("第1档后应有下一档预定时刻")
	}
	if len(log.dunning) != 1 || log.dunning[0].Stage != 1 || log.dunning[0].Suspended {
		t.Fatalf("首轮外呼=%+v，期望 1 封第 1 档且未停用", log.dunning)
	}
	if !log.dunning[0].WillSuspend {
		t.Fatal("宽限期未到，文案必须预告\"到期后第 N 天停止登录\"")
	}
	if row.SentTo == "" {
		t.Fatal("应留收件人脱敏痕迹")
	}
	if strings.Contains(row.SentTo, "boss@example.com") {
		t.Fatalf("sent_to 不得存明文邮箱，实际 %q", row.SentTo)
	}
	if g := tenantGrace(t, tid); g == nil || !sameTime(*g, dueAt.AddDate(0, 0, 14)) {
		t.Fatalf("grace_period_end_at=%v，期望到期+14天", g)
	}
	// 租户状态不该被第 1 档改动（摘除归 ExpireCheck，本序列到宽限期才动 status）
	if s := tenantStatus(t, tid); s != "expired" {
		t.Fatalf("催缴档不得改租户状态，实际 %s", s)
	}

	// ② 同日重跑：档位未前进 → 零动作、零外呼
	if n := SweepDunningForTenants([]uint{tid}); n != 0 {
		t.Fatalf("同档重跑应 0 次动作，实际 %d", n)
	}
	if len(log.dunning) != 1 {
		t.Fatalf("同档重跑不得再发信，累计 %d 封", len(log.dunning))
	}

	// ③ 跳到逾期第 8 天：到期日被改（人工延期后再次到期同形态）→ 序列重开并补发到第 3 档
	setTenantExpiredAt(t, tid, time.Now().Add(-8*24*time.Hour))
	if n := SweepDunningForTenants([]uint{tid}); n != 1 {
		t.Fatalf("跳档应 1 次动作，实际 %d", n)
	}
	row = loadDunning(t, tid)
	if row.Stage != 3 {
		t.Fatalf("跳到第8天应补发到 stage=3（0/3/7 三档中最高），实际 %d", row.Stage)
	}
	// 中间档（第 3 天那封）不得追发——客户收到"第3天该续费了"时已第8天，纯属骚扰
	if len(log.dunning) != 2 {
		t.Fatalf("不得逐档追发，累计 %d 封：%+v", len(log.dunning), log.dunning)
	}
	if log.dunning[1].Stage != 3 || log.dunning[1].DayPast != 8 {
		t.Fatalf("第2封=%+v，期望 stage=3 dayPast=8", log.dunning[1])
	}

	// ④ 逾期第 15 天（>14 宽限）：自动停用 + 审计 + 群消息 + 序列收尾
	setTenantExpiredAt(t, tid, time.Now().Add(-15*24*time.Hour))
	if n := SweepDunningForTenants([]uint{tid}); n != 1 {
		t.Fatalf("停用轮应 1 次动作，实际 %d", n)
	}
	row = loadDunning(t, tid)
	if row.Status != model.DunningStatusExhausted {
		t.Fatalf("停用后序列应 exhausted，实际 %s", row.Status)
	}
	if row.SuspendedAt == nil {
		t.Fatal("suspended_at 必须记录归因时刻（解除封禁的唯一凭据）")
	}
	if row.NextNotifyAt != nil {
		t.Fatal("序列结束后不得再有预定发信时刻")
	}
	if s := tenantStatus(t, tid); s != "suspended" {
		t.Fatalf("宽限期满应停用，实际 %s", s)
	}
	if countAudit(tid, dunningAuditSuspendAction) != 1 {
		t.Fatalf("自动停用审计应 1 条，实际 %d", countAudit(tid, dunningAuditSuspendAction))
	}
	last := log.dunning[len(log.dunning)-1]
	if !last.Suspended || last.WillSuspend {
		t.Fatalf("停用那封文案必须走\"已停止登录\"态，got suspended=%v will=%v", last.Suspended, last.WillSuspend)
	}
	if !hasContains(log.groups, "欠费停用") {
		t.Fatalf("停用应惊动平台群，实际 %v", log.groups)
	}
	// 已封禁不再推第二档（对账也不能把自家封的解回去）
	before := len(log.dunning)
	if n := SweepDunningForTenants([]uint{tid}); n != 0 {
		t.Fatalf("停用后不该再有动作，实际 %d", n)
	}
	if len(log.dunning) != before {
		t.Fatal("停用后不得再发第二封")
	}
	if s := tenantStatus(t, tid); s != "suspended" {
		t.Fatalf("对账不得把本序列的封禁撤销成 expired（那等于封了个寂寞），实际 %s", s)
	}

	// ⑤ 续费到账：序列 resolved + 解除本序列施加的停用 + 宽限期展示值清零
	DunningOnPaid(tid, "TEST_DUNNING_PAID")
	row = loadDunning(t, tid)
	if row.Status != model.DunningStatusResolved {
		t.Fatalf("到账后序列应 resolved，实际 %s", row.Status)
	}
	if row.SuspendedAt != nil {
		t.Fatal("本序列施加的停用应随到账解除")
	}
	if s := tenantStatus(t, tid); s != "active" {
		t.Fatalf("到账应恢复登录，实际 %s", s)
	}
	if g := tenantGrace(t, tid); g != nil {
		t.Fatalf("不欠费的租户不得还挂着宽限期，实际 %v", g)
	}
	if !hasContains(log.groups, "欠费恢复") {
		t.Fatalf("恢复应知会平台群，实际 %v", log.groups)
	}
	// resolved 序列再被巡检捞到也不动作
	if n := SweepDunningForTenants([]uint{tid}); n != 0 {
		t.Fatalf("已结序列不得复燃，实际 %d", n)
	}
}

// TestDunningNeverUndoesManualSuspend 超管在自动停用之后又人工封了一次：
// 续费只结序列，绝不替客户决定解除封禁（否则欠费户点一下按钮就绕过运营封禁）。
func TestDunningNeverUndoesManualSuspend(t *testing.T) {
	testutil.SetupTestDB(t)
	defer useDunningCfg(t, true, []int{0, 3, 7, 14}, 14)()
	var log d3CallLog
	log.smtpReady, log.groupReady = true, false
	log.admins = []string{"boss@example.com"}
	defer withD3Sink(newSilentD3Sink(&log))()

	tid := setTenantExpiredAt(t, testutil.CreateTenantCode(t, "dun_manual"), time.Now().Add(-15*24*time.Hour))
	if n := SweepDunningForTenants([]uint{tid}); n != 1 {
		t.Fatalf("应停用，实际动作 %d", n)
	}
	row := loadDunning(t, tid)
	if row.SuspendedAt == nil {
		t.Fatal("前置条件：本序列已施加停用")
	}
	// 超管人工封禁审计（detail 形态与 super.go:SuperTenantStatus 真写法一致：{"to":"suspended"}）
	manualAt := row.SuspendedAt.Add(time.Hour)
	db.DB.Create(&model.TenantAuditLog{
		TenantID: tid, Action: dunningManualSuspendAction, Resource: fmt.Sprintf("tenant:%d", tid),
		Detail: `{"to":"suspended"}`, CreatedAt: manualAt,
	})
	if !manualSuspendAfter(tid, *row.SuspendedAt) {
		t.Fatal("人工封禁审计应被归因函数识别（LIKE 模式必须与 super.go 实际 detail 形态对齐，否则归因永远判不出）")
	}

	DunningOnPaid(tid, "TEST_DUNNING_MANUAL")
	if s := tenantStatus(t, tid); s != "suspended" {
		t.Fatalf("存在更晚的人工封禁时续费不得自动解封，实际 %s", s)
	}
	row = loadDunning(t, tid)
	if row.Status != model.DunningStatusResolved {
		t.Fatalf("序列本身仍应结掉（催缴已完成它的职责），实际 %s", row.Status)
	}
	if row.SuspendedAt == nil {
		t.Fatal("未解除封禁时 suspended_at 必须保留，否则下一次封禁失去归因")
	}

	// 对照：人工封禁发生在自动停用**之前**的，续费照常解封（它不是"更新的心意"）
	tid2 := setTenantExpiredAt(t, testutil.CreateTenantCode(t, "dun_manual_before"), time.Now().Add(-15*24*time.Hour))
	if n := SweepDunningForTenants([]uint{tid2}); n != 1 {
		t.Fatalf("对照组应停用，实际 %d", n)
	}
	row2 := loadDunning(t, tid2)
	db.DB.Create(&model.TenantAuditLog{
		TenantID: tid2, Action: dunningManualSuspendAction, Resource: fmt.Sprintf("tenant:%d", tid2),
		Detail: `{"to":"suspended"}`, CreatedAt: row2.SuspendedAt.Add(-time.Hour),
	})
	DunningOnPaid(tid2, "TEST_DUNNING_OLD_MANUAL")
	if s := tenantStatus(t, tid2); s != "active" {
		t.Fatalf("更早的人工封禁不该阻止解封，实际 %s", s)
	}
}

// TestDunningPaidWithoutSequence 从未欠过费的租户到账：DunningOnPaid 必须静默无操作
// （这是它最高频的路径——每一笔正常付费订单都会调它，绝不能因"没有催缴行"而报错或改状态）
func TestDunningPaidWithoutSequence(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenantCode(t, "dun_noseq")
	db.DB.Model(&model.Tenant{}).Where("id = ?", tid).Update("status", "active")
	t.Cleanup(func() { db.DB.Where("tenant_id = ?", tid).Delete(&model.BillingDunning{}) })

	DunningOnPaid(tid, "TEST_DUNNING_NOOP")
	var n int64
	db.DB.Model(&model.BillingDunning{}).Where("tenant_id = ?", tid).Count(&n)
	if n != 0 {
		t.Fatalf("无欠费史不得凭空建催缴行，实际 %d 行", n)
	}
	if s := tenantStatus(t, tid); s != "active" {
		t.Fatalf("状态不得被动过，实际 %s", s)
	}
	DunningOnPaid(0, "TEST_DUNNING_NOOP") // 无租户语境（平台内部单）也必须安静返回
}

// TestDunningSwitchOffAndScope 开关关闭零动作；定向巡检绝不波及其它过期租户
func TestDunningSwitchOffAndScope(t *testing.T) {
	testutil.SetupTestDB(t)
	var log d3CallLog
	log.smtpReady, log.groupReady = true, true
	log.admins = []string{"boss@example.com"}
	defer withD3Sink(newSilentD3Sink(&log))()

	inScope := setTenantExpiredAt(t, testutil.CreateTenantCode(t, "dun_in"), time.Now().Add(-20*24*time.Hour))
	outScope := setTenantExpiredAt(t, testutil.CreateTenantCode(t, "dun_out"), time.Now().Add(-20*24*time.Hour))

	// ① 开关关闭：什么都不发生（出厂默认即此态，防"忘了关就群发催缴"）
	defer useDunningCfg(t, false, []int{0, 3, 7, 14}, 14)()
	if n := SweepDunningForTenants([]uint{inScope}); n != 0 {
		t.Fatalf("开关关闭应 0 动作，实际 %d", n)
	}
	var rows int64
	db.DB.Model(&model.BillingDunning{}).Where("tenant_id IN ?", []uint{inScope, outScope}).Count(&rows)
	if rows != 0 {
		t.Fatal("开关关闭不得建催缴序列")
	}
	if len(log.dunning) != 0 || len(log.groups) != 0 {
		t.Fatal("开关关闭不得外呼")
	}

	// ② 开关打开但只巡检 inScope：outScope 不得被建序列、更不得被封
	defer useDunningCfg(t, true, []int{0, 3, 7, 14}, 14)()
	if n := SweepDunningForTenants([]uint{inScope}); n != 1 {
		t.Fatalf("在册租户应 1 次动作，实际 %d", n)
	}
	if s := tenantStatus(t, outScope); s != "expired" {
		t.Fatalf("不在 scope 的过期租户被动了状态：%s（全表巡检的权力不得交给测试）", s)
	}
	db.DB.Model(&model.BillingDunning{}).Where("tenant_id = ?", outScope).Count(&rows)
	if rows != 0 {
		t.Fatal("不在 scope 的租户被建了催缴序列")
	}
	if s := tenantStatus(t, inScope); s != "suspended" {
		t.Fatalf("逾期 20 天>宽限 14 天应停用，实际 %s", s)
	}
}

// TestDunningReconcileClosesRecovered 超管手工把租户改回 active（或人工推后到期日）而没走账：
// 对账必须把序列收掉，不能让超管队列永远挂着已结的一家。
func TestDunningReconcileClosesRecovered(t *testing.T) {
	testutil.SetupTestDB(t)
	defer useDunningCfg(t, true, []int{0, 3, 7, 14}, 14)()
	var log d3CallLog
	log.smtpReady, log.groupReady = true, false
	log.admins = []string{"boss@example.com"}
	defer withD3Sink(newSilentD3Sink(&log))()

	// A：已因欠费停用（序列停在 exhausted），随后超管手工改回 active（未走账）
	//    → 序列收口 + 封禁归因清零（留着会让下次 DunningOnPaid 去解一个并不存在的封禁）
	tid := setTenantExpiredAt(t, testutil.CreateTenantCode(t, "dun_recon_a"), time.Now().Add(-15*24*time.Hour))
	if n := SweepDunningForTenants([]uint{tid}); n != 1 {
		t.Fatalf("前置：应停用，实际 %d", n)
	}
	db.DB.Model(&model.Tenant{}).Where("id = ?", tid).Update("status", "active")
	if n := SweepDunningForTenants([]uint{tid}); n != 0 {
		t.Fatalf("对账轮不应再产生催缴动作，实际 %d", n)
	}
	row := loadDunning(t, tid)
	if row.Status != model.DunningStatusResolved {
		t.Fatalf("不再欠费的序列应收口（含 exhausted 态），实际 %s", row.Status)
	}
	if row.SuspendedAt != nil {
		t.Fatal("封禁已不存在时归因必须一起清掉")
	}

	// B：到期日被人工推到未来（赠送期）→ 序列该收，且不得继续催
	tidB := setTenantExpiredAt(t, testutil.CreateTenantCode(t, "dun_recon_b"), time.Now().Add(-20*24*time.Hour))
	if n := SweepDunningForTenants([]uint{tidB}); n != 1 {
		t.Fatalf("前置 B：应有动作，实际 %d", n)
	}
	db.DB.Model(&model.Tenant{}).Where("id = ?", tidB).Updates(map[string]any{
		"status": "expired", "expired_at": time.Now().AddDate(0, 0, 30),
	})
	if n := SweepDunningForTenants([]uint{tidB}); n != 0 {
		t.Fatalf("到期日推到未来后不得再催，实际 %d", n)
	}
	if st := loadDunning(t, tidB); st.Status != model.DunningStatusResolved {
		t.Fatalf("到期日推后应随对账收口，实际 %s", st.Status)
	}
	if n := SweepDunningForTenants([]uint{tidB}); n != 0 {
		t.Fatalf("已收口且未过期不得复燃，实际 %d", n)
	}
}

// TestDunningOnlyExpiredTenantsAreTouched 非 expired 状态一律不进序列（摘除归 ExpireCheck）
func TestDunningOnlyExpiredTenantsAreTouched(t *testing.T) {
	testutil.SetupTestDB(t)
	defer useDunningCfg(t, true, []int{0, 3, 7, 14}, 14)()
	var log d3CallLog
	log.smtpReady, log.groupReady = true, false
	log.admins = []string{"boss@example.com"}
	defer withD3Sink(newSilentD3Sink(&log))()

	tid := testutil.CreateTenantCode(t, "dun_active")
	t.Cleanup(func() { db.DB.Where("tenant_id = ?", tid).Delete(&model.BillingDunning{}) })
	// 过期日已过但状态仍是 active（ExpireCheck 还没跑）：本序列不抢它的活
	db.DB.Model(&model.Tenant{}).Where("id = ?", tid).Updates(map[string]any{
		"status": "active", "expired_at": time.Now().AddDate(0, 0, -30),
	})
	if n := SweepDunningForTenants([]uint{tid}); n != 0 {
		t.Fatalf("active 租户不得被催缴，实际 %d", n)
	}
	if len(log.dunning) != 0 {
		t.Fatal("active 租户不得收到催缴信")
	}
}

// ---------------------------------------------------------------------------
// 超管侧动作：人工重发 / 清序列 / 队列视图
// ---------------------------------------------------------------------------

// TestNudgeDunningNow 人工重发：不改档位、留可归因审计行，缺操作人身份必须被拒。
func TestNudgeDunningNow(t *testing.T) {
	testutil.SetupTestDB(t)
	defer useDunningCfg(t, true, []int{0, 3, 7, 14}, 14)()
	var log d3CallLog
	log.smtpReady, log.groupReady = true, false
	log.admins = []string{"boss@example.com"}
	defer withD3Sink(newSilentD3Sink(&log))()

	// 没有序列 → 报错而不是凭空发信
	if err := NudgeDunningNow(testutil.CreateTenantCode(t, "dun_nudge_none"), 1, "127.0.0.1"); err == nil {
		t.Fatal("无催缴序列时人工重发必须报错")
	}

	tid := setTenantExpiredAt(t, testutil.CreateTenantCode(t, "dun_nudge"), time.Now().Add(-5*24*time.Hour))
	if n := SweepDunningForTenants([]uint{tid}); n != 1 {
		t.Fatalf("前置：应发第 2 档，实际 %d", n)
	}
	before := loadDunning(t, tid)
	sentBefore := len(log.dunning)
	// 匿名调用一律拒：人工催缴是"以平台名义再开口"，无人可归因就不该成立
	if err := NudgeDunningNow(tid, 0, "127.0.0.1"); err == nil {
		t.Fatal("缺操作人身份时人工重发必须报错")
	}
	if len(log.dunning) != sentBefore {
		t.Fatal("被拒的重发不得真发信")
	}
	if err := NudgeDunningNow(tid, 1, "127.0.0.1"); err != nil {
		t.Fatalf("人工重发失败: %v", err)
	}
	after := loadDunning(t, tid)
	if len(log.dunning) != sentBefore+1 {
		t.Fatalf("应多发 1 封，实际累计 %d", len(log.dunning))
	}
	if after.Stage != before.Stage {
		t.Fatalf("人工重发绝不推进档位（那是序列的节奏），%d→%d", before.Stage, after.Stage)
	}
	if after.LastNotifiedAt == nil || !after.LastNotifiedAt.Add(time.Second).After(*before.LastNotifiedAt) {
		t.Fatal("last_notified_at 应刷新")
	}
	if countAudit(tid, "dunning_manual_nudge") != 1 {
		t.Fatal("人工重发必须留审计（谁在替客户决定再催一次）")
	}
	if n := countAuditUser(tid, "dunning_manual_nudge", 1); n != 1 {
		t.Fatalf("审计行必须带操作人，否则事后无法归因，实际 %d", n)
	}

	// 无收件人 → 动作未成立：不报错就等于超管以为"已经催过了"
	log.admins = nil
	if err := NudgeDunningNow(tid, 1, "127.0.0.1"); err == nil {
		t.Fatal("无可用收件人时必须报错")
	}
	// 没有到期时间的租户无从算 dayPast
	tid2 := testutil.CreateTenantCode(t, "dun_nudge_nodate")
	db.DB.Model(&model.Tenant{}).Where("id = ?", tid2).Updates(map[string]any{"status": "expired", "expired_at": nil})
	db.DB.Create(&model.BillingDunning{TenantID: tid2, Status: model.DunningStatusRunning})
	t.Cleanup(func() { db.DB.Where("tenant_id = ?", tid2).Delete(&model.BillingDunning{}) })
	if err := NudgeDunningNow(tid2, 1, "127.0.0.1"); err == nil {
		t.Fatal("无到期时间应拒绝（否则邮件里会出现 1970 年）")
	}
}

// TestResetDunning 人工清序列：只停止催缴，绝不解封、绝不改到期时间，被拒时不得改动状态。
func TestResetDunning(t *testing.T) {
	testutil.SetupTestDB(t)
	defer useDunningCfg(t, true, []int{0, 3, 7, 14}, 14)()
	var log d3CallLog
	log.smtpReady, log.groupReady = true, false
	log.admins = []string{"boss@example.com"}
	defer withD3Sink(newSilentD3Sink(&log))()

	if err := ResetDunning(testutil.CreateTenantCode(t, "dun_reset_none"), 1, "127.0.0.1"); err == nil {
		t.Fatal("无待处理序列时 reset 应报错（前端据此提示而不是假成功）")
	}

	tid := setTenantExpiredAt(t, testutil.CreateTenantCode(t, "dun_reset"), time.Now().Add(-15*24*time.Hour))
	if n := SweepDunningForTenants([]uint{tid}); n != 1 {
		t.Fatalf("前置：应停用，实际 %d", n)
	}
	before := loadDunning(t, tid) // 第 15 天这一轮已经封过，序列是 exhausted
	if err := ResetDunning(tid, 0, "127.0.0.1"); err == nil {
		t.Fatal("缺操作人身份时 reset 必须报错")
	}
	if row := loadDunning(t, tid); row.Status != before.Status {
		t.Fatalf("被拒的 reset 不得改动序列状态，%s→%s", before.Status, row.Status)
	}
	if err := ResetDunning(tid, 1, "127.0.0.1"); err != nil {
		t.Fatalf("清序列失败: %v", err)
	}
	row := loadDunning(t, tid)
	if row.Status != model.DunningStatusResolved {
		t.Fatalf("序列应 resolved，实际 %s", row.Status)
	}
	if countAuditUser(tid, "dunning_manual_reset", 1) != 1 {
		t.Fatal("人工清序列必须留可归因的审计行")
	}
	// 资金红线：reset 只停催缴，绝不把欠费户放回货架
	if s := tenantStatus(t, tid); s != "suspended" {
		t.Fatalf("ResetDunning 不得解封（解封只认可到账），实际 %s", s)
	}
	if err := ResetDunning(tid, 1, "127.0.0.1"); err == nil {
		t.Fatal("已结序列再 reset 应报错（防重复操作被当成成功）")
	}
}

// TestDunningQueueAndTenantView 超管队列与租户自查视图：跨租户不外泄，租户侧只拿到自己的进度。
func TestDunningQueueAndTenantView(t *testing.T) {
	testutil.SetupTestDB(t)
	defer useDunningCfg(t, true, []int{0, 3, 7, 14}, 14)()
	var log d3CallLog
	log.smtpReady, log.groupReady = true, false
	log.admins = []string{"boss@example.com"}
	defer withD3Sink(newSilentD3Sink(&log))()

	// 超管队列按档位倒序，且带租户名/状态/宽限终点
	tidLow := setTenantExpiredAt(t, testutil.CreateTenantCode(t, "dun_view_low"), time.Now().Add(-1*24*time.Hour))
	tidHigh := setTenantExpiredAt(t, testutil.CreateTenantCode(t, "dun_view_high"), time.Now().Add(-15*24*time.Hour))
	for _, id := range []uint{tidLow, tidHigh} {
		if n := SweepDunningForTenants([]uint{id}); n != 1 {
			t.Fatalf("前置动作失败 tenant=%d", id)
		}
	}
	open := ListDunningQueue(true, 50)
	if len(open) < 2 {
		t.Fatalf("未结队列应含本轮两家，实际 %d", len(open))
	}
	byID := map[uint]DunningRowView{}
	for _, r := range open {
		byID[r.TenantID] = r
	}
	high, okHigh := byID[tidHigh]
	if !okHigh {
		t.Fatal("高档位租户未出现在队列里（超管看不到最该跟进的一家）")
	}
	if !high.Suspended || high.TenantStatus != "suspended" {
		t.Fatalf("队列回显=%s suspended=%v，期望 exhausted/suspended", high.Status, high.Suspended)
	}
	if high.TenantName == "" || high.Code == "" {
		t.Fatal("队列必须带租户名与企业码，否则运营无从下手")
	}
	if high.DayPast < 14 || high.GraceEnd == nil {
		t.Fatalf("day_past=%d grace_end=%v，期望已过宽限且有宽限终点", high.DayPast, high.GraceEnd)
	}
	// 结掉一家后 onlyOpen 不再回显，全量仍可见（审计留痕不删行）
	DunningOnPaid(tidLow, "TEST_DUNNING_VIEW")
	for _, r := range ListDunningQueue(true, 50) {
		if r.TenantID == tidLow {
			t.Fatal("已结序列仍出现在\"未结\"队列")
		}
	}
	var found bool
	for _, r := range ListDunningQueue(false, 200) {
		if r.TenantID == tidLow && r.Status == model.DunningStatusResolved {
			found = true
		}
	}
	if !found {
		t.Fatal("全量队列应能看到已解决序列（复盘用）")
	}
	// limit 非法值不得放大成全表
	if got := ListDunningQueue(false, 0); len(got) == 0 {
		t.Fatal("limit=0 应回退默认 50 而非返回空")
	}

	// 租户侧看得见的自己的进度
	v := GetTenantDunning(tidHigh)
	if !v.Exists || v.Stage != 4 || v.TotalStages != 4 || !v.Suspended {
		t.Fatalf("租户侧进度=%+v，期望 4/4 且已停用", v)
	}
	if v.DayPast < 14 || v.GraceEnd == nil {
		t.Fatalf("进度视图 day_past=%d grace=%v", v.DayPast, v.GraceEnd)
	}
	// 无序列租户：Exists=false 而不是报错
	if gv := GetTenantDunning(testutil.CreateTenantCode(t, "dun_view_none")); gv.Exists {
		t.Fatal("未欠过费的租户不该有催缴进度")
	}
}

// TestDunningSuspendYieldsToConcurrentRenewal 封禁走条件 UPDATE：
// 判定与写入之间状态被并发改走（刚好到账）时必须让路，不留封禁审计。
// 这是"刚付完款就被停用"资金事故的第二道闸（第一道是与续费共用同一把锁）。
func TestDunningSuspendYieldsToConcurrentRenewal(t *testing.T) {
	testutil.SetupTestDB(t)
	var log d3CallLog
	log.smtpReady, log.groupReady = true, true
	log.admins = []string{"boss@example.com"}
	defer withD3Sink(newSilentD3Sink(&log))()

	tid := setTenantExpiredAt(t, testutil.CreateTenantCode(t, "dun_yield"), time.Now().Add(-20*24*time.Hour))
	db.DB.Model(&model.Tenant{}).Where("id = ?", tid).Update("status", "active") // 模拟并发到账
	row := model.BillingDunning{TenantID: tid, Status: model.DunningStatusExhausted, Stage: 4}
	if err := db.DB.Create(&row).Error; err != nil {
		t.Fatalf("建序列失败: %v", err)
	}
	var t0 model.Tenant
	if err := db.DB.First(&t0, tid).Error; err != nil {
		t.Fatalf("读租户失败: %v", err)
	}
	suspendTenantByDunning(t0, row, time.Now())
	if s := tenantStatus(t, tid); s != "active" {
		t.Fatalf("状态被并发抢改后仍强行停用，实际 %s", s)
	}
	if n := countAudit(tid, dunningAuditSuspendAction); n != 0 {
		t.Fatalf("让路时不得留封禁审计，实际 %d 条", n)
	}
	if hasContains(log.groups, "欠费停用") {
		t.Fatal("让路时不得往群里报停用")
	}
}

// hasContains 字符串切片里是否有包含关键词的一条
func hasContains(list []string, kw string) bool {
	for _, s := range list {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}
