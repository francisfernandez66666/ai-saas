// 触达领域层连库单测：排期前置校验（开关/归属/频控）、静默顺延落库、撤回状态机、
// 跨租户不可见、派发目标解析（无身份/停用通道/窗口判定）。
// 配置替身走真实租户覆盖层（system_configs tenant_id>0 + Reload），不 mock 读层——
// 批六刚收口过"租户覆盖不生效"的读写层错位，这里必须按真实取值链验。
package outreach

import (
	"errors"
	"os"
	"testing"
	"time"

	"ai-scrm/internal/contentsafety"
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/testutil"
)

// TestMain 挂 testutil 出口：DB 不可用时显式打跳过量，防"静默绿"。
func TestMain(m *testing.M) { os.Exit(testutil.RunMain(m)) }

// setTenantCfg 写/改一条租户级热配并 Reload（测后自动删行）。
func setTenantCfg(t *testing.T, tid uint, key, value, valueType string) {
	t.Helper()
	row := model.SystemConfig{
		TenantID: tid, Category: "notify", Key: key, Value: value,
		ValueType: valueType, Description: "单测注入", DefaultValue: value,
	}
	if err := db.DB.Where("tenant_id = ? AND key = ?", tid, key).Delete(&model.SystemConfig{}).Error; err != nil {
		t.Fatalf("清理旧配置失败: %v", err)
	}
	if err := db.DB.Create(&row).Error; err != nil {
		t.Fatalf("写租户配置失败: %v", err)
	}
	if svc := runtimecfg.DefaultSystemConfigService; svc != nil {
		svc.Reload()
	}
	t.Cleanup(func() {
		db.DB.Where("tenant_id = ? AND key = ?", tid, key).Delete(&model.SystemConfig{})
		if svc := runtimecfg.DefaultSystemConfigService; svc != nil {
			svc.Reload()
		}
	})
}

// newCustomer 建一个属于本租户的客户
func newCustomer(t *testing.T, tid uint, name string) uint {
	t.Helper()
	c := model.Customer{TenantID: tid, Name: name}
	if err := db.DB.Create(&c).Error; err != nil {
		t.Fatalf("建客户失败: %v", err)
	}
	return c.ID
}

// enableOutreach 打开该租户的主动触达并把静默段设为"全天不静默"（避免用例被顺延干扰）。
func enableOutreach(t *testing.T, tid uint) {
	t.Helper()
	setTenantCfg(t, tid, "outreach_enabled", "true", "bool")
	setTenantCfg(t, tid, "outreach_quiet_hours", "", "string")
	setTenantCfg(t, tid, "outreach_weekly_limit", "2", "number")
}

// TestCreateRejectsWhenDisabled 出厂默认关：未点头就不能排期（放量纪律）。
func TestCreateRejectsWhenDisabled(t *testing.T) {
	testutil.SetupTestDB(t)
	runtimecfg.InitSystemConfigService()
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	cid := newCustomer(t, tid, "触达-未启用客户")

	if _, err := Create(db.DB, CreateInput{TenantID: tid, CustomerID: cid, Content: "哥，周末有活动"}); err == nil {
		t.Fatal("未开开关时 Create 应被拒")
	} else if RejectReason(err) != model.OutreachReasonDisabled {
		t.Fatalf("拒绝原因应为 disabled，得 %q（err=%v）", RejectReason(err), err)
	}
	if RejectReason(errors.New("别的错误")) != "" {
		t.Fatal("非 RejectError 应返回空原因")
	}
}

// TestCreateDefersQuietHoursAndMasksContent 静默时段顺延与脱敏正文都要在落库前生效。
func TestCreateDefersQuietHoursAndMasksContent(t *testing.T) {
	testutil.SetupTestDB(t)
	runtimecfg.InitSystemConfigService()
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	cid := newCustomer(t, tid, "触达-顺延客户")

	setTenantCfg(t, tid, "outreach_enabled", "true", "bool")
	setTenantCfg(t, tid, "outreach_quiet_hours", "21:00-09:00", "string")
	setTenantCfg(t, tid, "outreach_weekly_limit", "0", "number") // 0=不限次

	oldCheck := CheckFunc
	CheckFunc = func(string) contentsafety.Result {
		return contentsafety.Result{Hit: true, Level: contentsafety.LevelMask, Cleaned: "哥，**周六有空来坐"}
	}
	t.Cleanup(func() { CheckFunc = oldCheck })

	// 计划时间落在静默段（02:00）→ 落库应为当日 09:00
	now := time.Date(2026, 9, 23, 2, 0, 0, 0, time.Local)
	task, err := Create(db.DB, CreateInput{
		TenantID: tid, CustomerID: cid, Content: "哥，脏话周六有空来坐",
		ScheduledAt: now, CreatedBy: 7, Now: now,
	})
	if err != nil {
		t.Fatalf("Create 失败: %v", err)
	}
	t.Cleanup(func() { db.DB.Delete(&model.OutreachTask{}, task.ID) })

	want := time.Date(2026, 9, 23, 9, 0, 0, 0, time.Local)
	if !task.ScheduledAt.Equal(want) {
		t.Fatalf("静默段内应顺延到 09:00，得 %s", task.ScheduledAt.Format(time.RFC3339))
	}
	if task.Content != "哥，**周六有空来坐" {
		t.Fatalf("应落 MASK 脱敏后的正文，得 %q", task.Content)
	}
	if task.Status != model.OutreachStatusPending || task.CreatedBy != 7 {
		t.Fatalf("初始状态/排期人错：%s / %d", task.Status, task.CreatedBy)
	}
}

// TestCreateRejectsForeignCustomer 跨租户客户不可排期（归属校验在应用层，不等 RLS）。
func TestCreateRejectsForeignCustomer(t *testing.T) {
	testutil.SetupTestDB(t)
	runtimecfg.InitSystemConfigService()
	tidA := testutil.CreateTenantCode(t, "out_a")
	tidB := testutil.CreateTenantCode(t, "out_b")
	defer testutil.CleanupTenant(t, tidA)
	defer testutil.CleanupTenant(t, tidB)
	enableOutreach(t, tidA)
	foreign := newCustomer(t, tidB, "触达-别家客户")

	if _, err := Create(db.DB, CreateInput{TenantID: tidA, CustomerID: foreign, Content: "约个时间聊聊"}); err == nil {
		t.Fatal("打别租户客户应被拒")
	} else if RejectReason(err) != model.OutreachReasonNoChannel {
		t.Fatalf("拒绝原因应为 no_channel，得 %q", RejectReason(err))
	}
}

// TestCreateWeeklyLimit 周内额度只数 sent：queued 未确认送达不该永久吃掉客户额度。
func TestCreateWeeklyLimit(t *testing.T) {
	testutil.SetupTestDB(t)
	runtimecfg.InitSystemConfigService()
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	cid := newCustomer(t, tid, "触达-频控客户")
	enableOutreach(t, tid)

	now := time.Now()
	mk := func(status string, sentAt *time.Time) {
		row := model.OutreachTask{
			TenantID: tid, CustomerID: cid, Content: "历史触达", Status: status,
			ScheduledAt: now.Add(-24 * time.Hour), SentAt: sentAt,
		}
		if err := db.DB.Create(&row).Error; err != nil {
			t.Fatalf("造历史任务失败: %v", err)
		}
	}
	sent := now.Add(-2 * 24 * time.Hour)
	// 2 条 sent 达上限；3 条 queued/skipped/failed 不计入
	mk(model.OutreachStatusSent, &sent)
	mk(model.OutreachStatusSent, &sent)
	mk(model.OutreachStatusQueued, nil)
	mk(model.OutreachStatusSkipped, nil)
	mk(model.OutreachStatusFailed, nil)

	if _, err := Create(db.DB, CreateInput{TenantID: tid, CustomerID: cid, Content: "再来一条", Now: now}); err == nil {
		t.Fatal("达周上限时 Create 应被拒")
	} else if RejectReason(err) != model.OutreachReasonOverWeeklyLimit {
		t.Fatalf("拒绝原因应为 over_weekly_limit，得 %q", RejectReason(err))
	}
	// 30 天前（窗口外）的 sent 不占额度
	old := now.Add(-30 * 24 * time.Hour)
	db.DB.Model(&model.OutreachTask{}).Where("tenant_id = ? AND customer_id = ?", tid, cid).
		Update("sent_at", old)
	if _, err := Create(db.DB, CreateInput{TenantID: tid, CustomerID: cid, Content: "一个月后再联系", Now: now}); err != nil {
		t.Fatalf("窗口外历史不应挡今天排期: %v", err)
	}
}

// TestCancelOnlyPending 撤回状态机：pending 可撤、queued 不可撤（已进通道队列不假承诺）。
func TestCancelOnlyPending(t *testing.T) {
	testutil.SetupTestDB(t)
	runtimecfg.InitSystemConfigService()
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	now := time.Now()
	pending := model.OutreachTask{TenantID: tid, CustomerID: 900001, Content: "待撤", Status: model.OutreachStatusPending, ScheduledAt: now}
	queued := model.OutreachTask{TenantID: tid, CustomerID: 900002, Content: "已投", Status: model.OutreachStatusQueued, ScheduledAt: now}
	for _, row := range []*model.OutreachTask{&pending, &queued} {
		if err := db.DB.Create(row).Error; err != nil {
			t.Fatalf("造任务失败: %v", err)
		}
	}

	if n, err := Cancel(db.DB, tid, pending.ID); err != nil || n != 1 {
		t.Fatalf("pending 应撤成功，得 n=%d err=%v", n, err)
	}
	if n, err := Cancel(db.DB, tid, queued.ID); err != nil || n != 0 {
		t.Fatalf("queued 不可撤（应影响 0 行），得 n=%d err=%v", n, err)
	}
	// 他租户同名 id 撤不动（tenant_id 条件在 WHERE 里，不是靠缓存）
	other := testutil.CreateTenantCode(t, "out_cancel")
	defer testutil.CleanupTenant(t, other)
	if n, err := Cancel(db.DB, other, pending.ID); err != nil || n != 0 {
		t.Fatalf("跨租户撤回必须 0 行，得 n=%d err=%v", n, err)
	}
}

// TestListScopedAndFiltered 列表按租户隔离 + 状态筛选。
func TestListScopedAndFiltered(t *testing.T) {
	testutil.SetupTestDB(t)
	runtimecfg.InitSystemConfigService()
	tid := testutil.CreateTenant(t)
	other := testutil.CreateTenantCode(t, "out_list")
	defer testutil.CleanupTenant(t, tid)
	defer testutil.CleanupTenant(t, other)
	now := time.Now()
	db.DB.Create(&model.OutreachTask{TenantID: tid, CustomerID: 900101, Content: "本租户待办", Status: model.OutreachStatusPending, ScheduledAt: now})
	db.DB.Create(&model.OutreachTask{TenantID: other, CustomerID: 900102, Content: "别家任务", Status: model.OutreachStatusPending, ScheduledAt: now})

	rows, total, err := List(db.DB, tid, "", 50, 0)
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if total < 1 || len(rows) == 0 {
		t.Fatalf("本租户任务应可见，得 total=%d rows=%d", total, len(rows))
	}
	for _, r := range rows {
		if r.TenantID != tid {
			t.Fatalf("列表串租户：任务 %d 属于 %d", r.ID, r.TenantID)
		}
	}
	if _, sentTotal, _ := List(db.DB, tid, model.OutreachStatusSent, 50, 0); sentTotal != 0 {
		t.Fatalf("按 sent 筛选应为空，得 %d", sentTotal)
	}
}

// TestResolveTarget 派发目标解析：无身份/停用通道/窗口外来句 各给可解释结论。
func TestResolveTarget(t *testing.T) {
	testutil.SetupTestDB(t)
	runtimecfg.InitSystemConfigService()
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	// ① 客户没有任何渠道身份 → 不可达（nil,nil，非错误）
	plain := newCustomer(t, tid, "触达-无身份")
	tgt, err := ResolveTarget(db.DB, tid, plain)
	if err != nil || tgt != nil {
		t.Fatalf("无身份应返回 (nil,nil)，得 (%v,%v)", tgt, err)
	}

	// ② 有身份但通道停用 → ErrChannelInactive（在入队前拒，不往死信里堆）
	cust := newCustomer(t, tid, "触达-停用通道")
	ch := model.Channel{TenantID: tid, Name: "触达测试通道", Type: "wecom_kf", Status: "disabled"}
	if err := db.DB.Create(&ch).Error; err != nil {
		t.Fatalf("建通道失败: %v", err)
	}
	if err := db.DB.Create(&model.ChannelIdentity{TenantID: tid, ChannelID: ch.ID, CustomerID: cust, ExternalID: "wm_ext_1"}).Error; err != nil {
		t.Fatalf("建渠道身份失败: %v", err)
	}
	tgt, err = ResolveTarget(db.DB, tid, cust)
	if !errors.Is(err, ErrChannelInactive) {
		t.Fatalf("停用通道应报 ErrChannelInactive，得 %v", err)
	}
	if tgt == nil || tgt.ChannelID != ch.ID {
		t.Fatalf("停用通道也应回目标供留痕，得 %+v", tgt)
	}

	// ③ 通道启用 → 带最近来句时间；无来句时 LastInbound 为零值，窗口判定据实拒
	if err := db.DB.Model(&model.Channel{}).Where("id = ?", ch.ID).Update("status", "active").Error; err != nil {
		t.Fatalf("改通道状态失败: %v", err)
	}
	tgt, err = ResolveTarget(db.DB, tid, cust)
	if err != nil || tgt == nil {
		t.Fatalf("启用通道应解析成功: %+v %v", tgt, err)
	}
	if !tgt.LastInbound.IsZero() {
		t.Fatalf("该客户尚无来句，LastInbound 应为零值，得 %s", tgt.LastInbound)
	}
	if ok, reason := WindowAllows(tgt.ChannelType, tgt.LastInbound, time.Now(), 48); ok || reason != model.OutreachReasonOutOfWindow {
		t.Fatalf("从未沟通过的微信侧客户不应可主动触达，得 (%v,%q)", ok, reason)
	}

	// ④ 补一条窗口内来句 → 放行
	db.DB.Create(&model.Message{TenantID: tid, CustomerID: cust, ConversationID: 900900, SenderType: "customer", Content: "周末看看", CreatedAt: time.Now().Add(-time.Hour)})
	tgt, err = ResolveTarget(db.DB, tid, cust)
	if err != nil || tgt == nil || tgt.LastInbound.IsZero() {
		t.Fatalf("应取到最近来句时间: %+v %v", tgt, err)
	}
	if ok, reason := WindowAllows(tgt.ChannelType, tgt.LastInbound, time.Now(), 48); !ok {
		t.Fatalf("窗口内应放行，得 reason=%q", reason)
	}
	// ⑤ 别的租户查不到这个目标
	if got, err := ResolveTarget(db.DB, testutil.CreateTenantCode(t, "out_ro"), cust); err != nil || got != nil {
		t.Fatalf("跨租户不应解析到他家通道，得 %+v %v", got, err)
	}
}

// TestDerivedScopedHandleDoesNotLeak 锁住"派生句柄条件累加"这一整类缺陷（2026-09-23 实锤）。
//
// 真实请求链路里 api 层传的是 db.RQ(c)——一个 clone=0、Statement 里已经挂着
// tenant_id 条件的派生会话；在它上面继续链式 .Model/.Where 会**就地累加**条件。
// 于是 Create 的第二条查询（周内频次 count）带上了第一条（客户存在性 First）的
// Model 与条件，SQL 退化成 SELECT count(*) FROM "customers" ... AND customer_id = ?
// → 42703 column "customer_id" does not exist，接口 500（smoke §三十二 首跑抓到）。
// 干净 db.DB 的 clone=1 会每次新建 Statement，单测天然撞不到，所以这里必须
// 显式构造派生句柄形态，把四个多查询入口一起过一遍。
func TestDerivedScopedHandleDoesNotLeak(t *testing.T) {
	testutil.SetupTestDB(t)
	runtimecfg.InitSystemConfigService()
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	enableOutreach(t, tid)
	cid := newCustomer(t, tid, "触达-派生句柄客户")
	other := newCustomer(t, tid, "触达-派生句柄客户2")

	derived := db.DB.Where("tenant_id = ?", tid) // 与 RQ(c) 同形态：已带条件 + clone=0

	task, err := Create(derived, CreateInput{TenantID: tid, CustomerID: cid, Content: "周末到店有礼"})
	if err != nil {
		t.Fatalf("派生句柄下排期应成功（旧写法在这里 500）: %v", err)
	}
	rows, total, err := List(derived, tid, "", 10, 0)
	if err != nil {
		t.Fatalf("派生句柄下列表应成功: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("列表应只见刚排的 1 条，得 total=%d rows=%d", total, len(rows))
	}
	// 无通道身份 → (nil, nil)：三次查询（身份/通道/来句）不得互相污染
	if tgt, err := ResolveTarget(derived, tid, other); err != nil || tgt != nil {
		t.Fatalf("无身份客户应解析为不可达，得 (%+v,%v)", tgt, err)
	}
	if n, err := Cancel(derived, tid, task.ID); err != nil || n != 1 {
		t.Fatalf("派生句柄下撤回应生效 1 行，得 (%d,%v)", n, err)
	}
}

// TestDerivedScopedHandleLeaksWithoutIsolate 上一条用例的反证（防空转）：
// 刻意按旧写法在派生句柄上连查两次，必须**真的**复现条件累加，
// 否则说明本包用例没构造出泄漏形态（或 GORM 语义变了），那条 isolate 就成了无的之矢。
func TestDerivedScopedHandleLeaksWithoutIsolate(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	cid := newCustomer(t, tid, "触达-泄漏反证客户")

	q := db.DB.Where("tenant_id = ?", tid)
	var cust model.Customer
	if err := q.Where("id = ? AND tenant_id = ?", cid, tid).First(&cust).Error; err != nil {
		t.Fatalf("前置查询失败，反证无从谈起: %v", err)
	}
	var n int64
	err := q.Model(&model.OutreachTask{}).
		Where("tenant_id = ? AND customer_id = ? AND status = ?", tid, cid, model.OutreachStatusSent).
		Count(&n).Error
	if err == nil {
		t.Fatal("反证失败：派生句柄未发生条件累加，TestDerivedScopedHandleDoesNotLeak 与 isolate 都锁不住任何东西")
	}
	t.Logf("已复现旧写法的泄漏错误（预期非空）: %v", err)
}
