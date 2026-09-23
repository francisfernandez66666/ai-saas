// 获客活码连库单测：建码校验与重名拒绝、公开解析对停用码不可见、扫码事件窗口去重、
// C 端归因的三条硬约束（跨租户不写 / 首触不改写 / 只写两列不覆写整行）、
// 漏斗数字与下钻名单同源、越界页语义，以及派生句柄必须隔离的正反向双测。
//
// 为什么"跨租户不写"要连库测而不是纯函数测：纯函数那条测的是判断本身，
// 这里测的是**判断真的落到了 SQL 条件上**——归因写入的 WHERE 里带着 tenant_id，
// 少写这一个条件，A 渠道的钱就会记到 B 头上，而且线上查不出来。
package acquisition

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"

	"gorm.io/gorm"
)

// TestMain 挂 testutil 出口：DB 不可用时显式打跳过量，防"静默绿"。
func TestMain(m *testing.M) { os.Exit(testutil.RunMain(m)) }

// seedCustomer 造一个本租户客户（显式带 TenantID，过 D6 盖章门禁）
func seedCustomer(t *testing.T, tid uint, name string) uint {
	t.Helper()
	c := model.Customer{TenantID: tid, Name: name, JourneyStage: model.JourneyAIConnected, Source: "外部体验"}
	if err := db.DB.Create(&c).Error; err != nil {
		t.Fatalf("造客户失败: %v", err)
	}
	t.Cleanup(func() { db.DB.Unscoped().Delete(&model.Customer{}, c.ID) })
	return c.ID
}

// seedMessage 造一条该客户的消息（"开过口"的唯一判据）
func seedMessage(t *testing.T, tid, cid uint, senderType string) {
	t.Helper()
	m := model.Message{TenantID: tid, CustomerID: cid, SenderType: senderType, Content: "活码单测消息"}
	if err := db.DB.Create(&m).Error; err != nil {
		t.Fatalf("造消息失败: %v", err)
	}
	t.Cleanup(func() { db.DB.Unscoped().Delete(&model.Message{}, m.ID) })
}

// newTenant 建一个**指定语义码**的单测租户。DB 连接初始化挂在这里：本包每个用例都要落库，
// 漏掉 SetupTestDB 时 db.DB 是 nil，用例会在第一句 GORM 调用上 panic 成一片
// （而不是干净地跳过），所以宁可让取租户这一步自己负责把库准备好。
//
// 为什么必须带语义码而不是直接 CreateTenant：CreateTenant 同 code 复用同一租户
// （testutil 的复用语义），跨租户用例如果两次都拿它，拿回的是**同一个** tid——
// "码属于 A、请求来自 B"当场变成 A/A，那条最重要的主闸会在自己身上假绿。
func newTenant(t *testing.T, semantic string) uint {
	t.Helper()
	testutil.SetupTestDB(t)
	return testutil.CreateTenantCode(t, semantic)
}

func mustCreate(t *testing.T, tid uint, name, channel string) *model.AcquisitionCode {
	t.Helper()
	row, err := CreateCode(db.DB, CreateInput{TenantID: tid, Name: name, Channel: channel})
	if err != nil {
		t.Fatalf("建码失败(%s/%s): %v", name, channel, err)
	}
	t.Cleanup(func() {
		db.DB.Unscoped().Where("code_id = ?", row.ID).Delete(&model.AcquisitionScan{})
		db.DB.Unscoped().Delete(&model.AcquisitionCode{}, row.ID)
	})
	return row
}

// TestCreateCodeRejectsBadInput 建码的入参拒绝必须回稳定码，且拒了就不能留行。
func TestCreateCodeRejectsBadInput(t *testing.T) {
	tid := newTenant(t, "acq_a")
	defer testutil.CleanupTenant(t, tid)

	cases := []struct {
		name, channel, want string
	}{
		{"", ChannelStore, "name_required"},
		{"门店立牌", "视频号", "channel_unknown"},
	}
	for _, tc := range cases {
		_, err := CreateCode(db.DB, CreateInput{TenantID: tid, Name: tc.name, Channel: tc.channel})
		if RejectReason(err) != tc.want {
			t.Fatalf("名称=%q 渠道=%q 期望拒绝码 %q，实得 %q（err=%v）", tc.name, tc.channel, tc.want, RejectReason(err), err)
		}
	}
	var n int64
	db.DB.Model(&model.AcquisitionCode{}).Where("tenant_id = ?", tid).Count(&n)
	if n != 0 {
		t.Fatalf("被拒的建码不得留行，实得 %d 行", n)
	}
	// 无租户语境（tid=0）必须拒：公开链路绝不允许凭空造出"平台级活码"
	if _, err := CreateCode(db.DB, CreateInput{TenantID: 0, Name: "越权", Channel: ChannelStore}); RejectReason(err) != "tenant_required" {
		t.Fatalf("tid=0 建码应被拒为 tenant_required")
	}
}

// TestCreateCodeRejectsDuplicateName 同名同渠道的启用码判重（防手抖连点出三个一样的码）。
// 同名**不同渠道**必须放行——门店立牌和朋友圈本来就常起同一个名字。
func TestCreateCodeRejectsDuplicateName(t *testing.T) {
	tid := newTenant(t, "acq_a")
	defer testutil.CleanupTenant(t, tid)

	mustCreate(t, tid, "春季车展", ChannelDouyin)
	if _, err := CreateCode(db.DB, CreateInput{TenantID: tid, Name: "春季车展", Channel: ChannelDouyin}); RejectReason(err) != "duplicate_code_name" {
		t.Fatalf("同渠道重名应判 duplicate_code_name，实得 %q", RejectReason(err))
	}
	if _, err := CreateCode(db.DB, CreateInput{TenantID: tid, Name: "春季车展", Channel: ChannelStore}); err != nil {
		t.Fatalf("同名不同渠道应可建（%v）", err)
	}
}

// TestResolveIgnoresDisabledAndForeign 公开解析：形态非法/不存在/停用一律同一种"不可用"，
// 不给外部试探"哪些码存在但被停了"的探测面。
func TestResolveIgnoresDisabledAndForeign(t *testing.T) {
	tid := newTenant(t, "acq_a")
	defer testutil.CleanupTenant(t, tid)
	row := mustCreate(t, tid, "门店前台立牌", ChannelStore)

	if _, err := Resolve(db.DB, "ABCDEFGH"); err != ErrNotFound {
		t.Fatalf("不存在的码应回 ErrNotFound，实得 %v", err)
	}
	if _, err := Resolve(db.DB, "AB"); err != ErrNotFound {
		t.Fatalf("形态非法的码应在查库前就被挡掉（不变成一次 DB 查询）")
	}
	got, err := Resolve(db.DB, strings.ToLower(row.Code))
	if err != nil || got.ID != row.ID {
		t.Fatalf("小写传入也应解析到同一个码: %+v %v", got, err)
	}
	if _, err := SetCodeStatus(db.DB, tid, row.ID, false); err != nil {
		t.Fatalf("停用失败: %v", err)
	}
	if _, err := Resolve(db.DB, row.Code); err != ErrNotFound {
		t.Fatalf("停用码对外必须表现为不可用（与不存在同形态），实得 %v", err)
	}
	// 停用不影响管理端读取历史（列表仍带它，只是不再产生新归因）
	stats, err := ListWithStats(db.DB, tid, "", DefaultWindowDays)
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	if len(stats) != 1 || stats[0].Code.Status != model.AcquisitionStatusDisabled {
		t.Fatalf("停用码应在管理端可见（status=disabled），实得 %+v", stats)
	}
}

// TestApplyToGuestCrossTenantRefuses 主闸：码属于 A 租户，B 租户的请求打不上归因。
func TestApplyToGuestCrossTenantRefuses(t *testing.T) {
	tidA := newTenant(t, "acq_a")
	defer testutil.CleanupTenant(t, tidA)
	tidB := newTenant(t, "acq_b")
	defer testutil.CleanupTenant(t, tidB)

	code := mustCreate(t, tidA, "甲店抖音口播", ChannelDouyin)
	cidB := seedCustomer(t, tidB, "乙租户客户")

	res, err := ApplyToGuest(db.DB, tidB, cidB, code.Code, "vk-cross-1")
	if err != nil {
		t.Fatalf("归因不应报错（报错会让调用方放弃建客）: %v", err)
	}
	if res.Applied || res.Reason != ApplyReasonTenantMismatch {
		t.Fatalf("跨租户必须拒写并说明原因，实得 %+v", res)
	}
	var c model.Customer
	if err := db.DB.First(&c, cidB).Error; err != nil {
		t.Fatalf("读客户失败: %v", err)
	}
	if c.AcquisitionCode != "" || c.Source != "外部体验" {
		t.Fatalf("被拒的归因必须一个字段都不动，实得 code=%q source=%q", c.AcquisitionCode, c.Source)
	}
	var scans int64
	db.DB.Model(&model.AcquisitionScan{}).Where("code_id = ?", code.ID).Count(&scans)
	if scans != 0 {
		t.Fatalf("跨租户不得替别人家记扫码事件（会把 A 的渠道数据灌水），实得 %d 行", scans)
	}
}

// TestApplyToGuestWritesBothColumnsAndKeepsRest 命中归因：写 acquisition_code + source，
// 且**不得覆写客户的其它列**（接管态/归属销售可能在同一瞬间被别的链路改掉，
// 整行 Save 会把它们打回旧值——本项目反复踩过，故字段级 Updates 要有机验证）。
func TestApplyToGuestWritesBothColumnsAndKeepsRest(t *testing.T) {
	tid := newTenant(t, "acq_a")
	defer testutil.CleanupTenant(t, tid)
	code := mustCreate(t, tid, "朋友圈海报", ChannelWechat)
	cid := seedCustomer(t, tid, "首触客户")

	// 先写入"别的链路正在改"的两列
	if err := db.DB.Model(&model.Customer{}).Where("id = ?", cid).
		Updates(map[string]any{"assigned_user_id": 8888, "journey_stage": model.JourneyLeadCaptured}).Error; err != nil {
		t.Fatalf("预置列失败: %v", err)
	}
	res, err := ApplyToGuest(db.DB, tid, cid, strings.ToLower(code.Code), "vk-ok-1")
	if err != nil || !res.Applied || res.Reason != ApplyOK {
		t.Fatalf("归因应成功: %+v %v", res, err)
	}
	var c model.Customer
	if err := db.DB.First(&c, cid).Error; err != nil {
		t.Fatalf("读客户失败: %v", err)
	}
	if c.AcquisitionCode != code.Code {
		t.Fatalf("归因码应写进去（大小写归一），实得 %q", c.AcquisitionCode)
	}
	if c.Source != ChannelWechat {
		t.Fatalf("来源列应同步成渠道位，实得 %q", c.Source)
	}
	if c.AssignedUserID != 8888 || c.JourneyStage != model.JourneyLeadCaptured {
		t.Fatalf("归因不得覆写其它列：归属=%d 阶段=%q", c.AssignedUserID, c.JourneyStage)
	}
	if res.Scans != 1 {
		t.Fatalf("首次归因应新记一条扫码事件，实得 %+v", res)
	}
}

// TestApplyToGuestFirstTouchSticky 首触粘性：已有码的客户再扫别的码也不改写。
// 理由：按最近触达归因，所有渠道的钱最后都会流向最后一个触点（门店的钱记到个人头上）。
func TestApplyToGuestFirstTouchSticky(t *testing.T) {
	tid := newTenant(t, "acq_a")
	defer testutil.CleanupTenant(t, tid)
	first := mustCreate(t, tid, "门店立牌", ChannelStore)
	second := mustCreate(t, tid, "销售个人码", ChannelWechat)
	cid := seedCustomer(t, tid, "两码客户")

	if res, err := ApplyToGuest(db.DB, tid, cid, first.Code, "vk-sticky"); err != nil || !res.Applied {
		t.Fatalf("首触归因应成功: %+v %v", res, err)
	}
	res, err := ApplyToGuest(db.DB, tid, cid, second.Code, "vk-sticky")
	if err != nil {
		t.Fatalf("第二次不应报错: %v", err)
	}
	if res.Applied || res.Reason != ApplyReasonAlreadySet {
		t.Fatalf("二次扫码不得改写首触归因，实得 %+v", res)
	}
	var c model.Customer
	if err := db.DB.First(&c, cid).Error; err != nil {
		t.Fatalf("读客户失败: %v", err)
	}
	if c.AcquisitionCode != first.Code {
		t.Fatalf("首触码必须留在 %q，实得 %q", first.Code, c.AcquisitionCode)
	}
	// 未命中的扫码不记事件：漏斗"这个码带来多少人"必须是真被归因的人
	var n int64
	db.DB.Model(&model.AcquisitionScan{}).Where("code_id = ? AND customer_id = ?", second.ID, cid).Count(&n)
	if n != 0 {
		t.Fatalf("未归因成功不得记扫码事件，实得 %d 行", n)
	}
}

// TestApplyToGuestMalformedAndMissing 没带码/形态非法：不打扰建客、不写任何行。
func TestApplyToGuestMalformedAndMissing(t *testing.T) {
	tid := newTenant(t, "acq_a")
	defer testutil.CleanupTenant(t, tid)
	cid := seedCustomer(t, tid, "自然流量客户")

	for raw, want := range map[string]string{"": ApplyReasonAbsent, "!!": ApplyReasonMalformed, "ZZZZZZZZ": ApplyReasonNotFound} {
		res, err := ApplyToGuest(db.DB, tid, cid, raw, "vk-none")
		if err != nil {
			t.Fatalf("raw=%q 不应报错: %v", raw, err)
		}
		if res.Applied || res.Reason != want {
			t.Fatalf("raw=%q 期望 %s，实得 %+v", raw, want, res)
		}
	}
	var c model.Customer
	db.DB.First(&c, cid)
	if c.AcquisitionCode != "" || c.Source != "外部体验" {
		t.Fatalf("未命中归因不得动客户列，实得 code=%q source=%q", c.AcquisitionCode, c.Source)
	}
}

// TestRecordScanDedupeWindow 同访客窗口内只一行；窗口外再一行；无访客键各算一次。
// 这条测的是"渠道预算按扫码数分配"时数字会不会被一个人刷出来。
func TestRecordScanDedupeWindow(t *testing.T) {
	tid := newTenant(t, "acq_a")
	defer testutil.CleanupTenant(t, tid)
	code := mustCreate(t, tid, "小红书笔记", ChannelXiaohong)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	first, err := RecordScan(db.DB, ScanInput{TenantID: tid, CodeID: code.ID, Code: code.Code, VisitorKey: "vk-d", Now: now})
	if err != nil || !first {
		t.Fatalf("首次扫码应记一行: %v %v", first, err)
	}
	again, err := RecordScan(db.DB, ScanInput{TenantID: tid, CodeID: code.ID, Code: code.Code, VisitorKey: "vk-d", Now: now.Add(2 * time.Minute)})
	if err != nil {
		t.Fatalf("窗口内重复扫码不应报错（它是常态）: %v", err)
	}
	if again {
		t.Fatalf("窗口内重复扫码不得再记一行")
	}
	later, err := RecordScan(db.DB, ScanInput{TenantID: tid, CodeID: code.ID, Code: code.Code, VisitorKey: "vk-d", Now: now.Add(ScanDedupeWindow + time.Minute)})
	if err != nil || !later {
		t.Fatalf("超出窗口应重新计数: %v %v", later, err)
	}
	anon1, _ := RecordScan(db.DB, ScanInput{TenantID: tid, CodeID: code.ID, Code: code.Code, Now: now})
	anon2, _ := RecordScan(db.DB, ScanInput{TenantID: tid, CodeID: code.ID, Code: code.Code, Now: now})
	if !anon1 || !anon2 {
		t.Fatalf("匿名打开没有去重依据，两次都要记（口径已在响应 note 里说明）")
	}
	var n int64
	db.DB.Model(&model.AcquisitionScan{}).Where("code_id = ?", code.ID).Count(&n)
	if want := int64(4); n != want {
		t.Fatalf("扫码事件行数应为 %d（窗口外1+窗口外2+匿名2），实得 %d", want, n)
	}
}

// TestFunnelNumbersEqualDrillTotals 本批的主断言：**漏斗数字与名单条数同源相等**。
// 两侧都从 funnelCustomers 取同一份人，所以不等就是 bug，而不是"哪边写错了"。
func TestFunnelNumbersEqualDrillTotals(t *testing.T) {
	tid := newTenant(t, "acq_a")
	defer testutil.CleanupTenant(t, tid)
	code := mustCreate(t, tid, "百度投放", ChannelBaidu)

	mk := func(name, stage string, spoke bool) uint {
		cid := seedCustomer(t, tid, name)
		if spoke {
			seedMessage(t, tid, cid, "customer")
		}
		if _, err := ApplyToGuest(db.DB, tid, cid, code.Code, "vk-"+name); err != nil {
			t.Fatalf("归因失败: %v", err)
		}
		if stage != "" {
			if err := db.DB.Model(&model.Customer{}).Where("id = ?", cid).Update("journey_stage", stage).Error; err != nil {
				t.Fatalf("改阶段失败: %v", err)
			}
		}
		return cid
	}
	// 四个人把五格数字摆开：新4 / 开口2 / 留资3 / 到店2 / 成交1。
	// 后三格**递减但不互不相同**——阶段集合是"及之后"的包含关系（ordered 也算到店），
	// 摆成互不相同反而会诱导写出一套"每格只数一个阶段"的错谓词。
	// 开口与到店同为 2 的歧义由段末那组逐行断言消掉：错接的话，未开口的到店客户会混进开口名单。
	mk("甲开口留资", model.JourneyLeadCaptured, true)
	mk("乙到店", model.JourneyArrived, false)
	mk("丙成交", model.JourneyOrdered, true)
	mk("丁只建联", "", false)

	stats, err := ListWithStats(db.DB, tid, "active", DefaultWindowDays)
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("应只回到本租户这一个码，实得 %d", len(stats))
	}
	got := stats[0].Funnel
	want := map[string]int64{MetricNew: 4, MetricSpoke: 2, MetricLead: 3, MetricArrived: 2, MetricOrdered: 1}
	for m, w := range want {
		if got[m] != w {
			t.Fatalf("指标 %s 期望 %d，实得 %d（说明谓词或取数口被动过）", m, w, got[m])
		}
	}
	// 包含关系必须体现在数字上：成交的人一定被算进到店与留资，否则阶段集合被换成了互斥口径
	if got[MetricOrdered] > got[MetricArrived] || got[MetricArrived] > got[MetricLead] || got[MetricLead] > got[MetricNew] {
		t.Fatalf("漏斗不递减，阶段集合的「及之后」口径已破：%v", got)
	}
	if stats[0].Scans != 4 {
		t.Fatalf("四次归因各记一条扫码事件，实得 %d", stats[0].Scans)
	}
	// 逐指标比"名单 total == 卡片数字"
	for _, m := range FunnelMetricCodes {
		dr, err := DrillCustomers(db.DB, tid, code.ID, m, DefaultWindowDays, 1, 20)
		if err != nil {
			t.Fatalf("指标 %s 下钻失败: %v", m, err)
		}
		if dr.Total != got[m] {
			t.Fatalf("指标 %s：名单 %d 条 / 卡片 %d 个，两侧不同源了", m, dr.Total, got[m])
		}
		if dr.Label == "" {
			t.Fatalf("指标 %s 未随响应下发口径名", m)
		}
	}
	// 扫码次数不可下钻（单位纪律）
	if _, err := DrillCustomers(db.DB, tid, code.ID, MetricScans, DefaultWindowDays, 1, 20); err != ErrBadMetric {
		t.Fatalf("扫码次数应被判不可下钻，实得 %v", err)
	}
	// 名单字段够前端渲染，且开过口的判据跟着人走
	dr, err := DrillCustomers(db.DB, tid, code.ID, MetricSpoke, DefaultWindowDays, 1, 20)
	if err != nil {
		t.Fatalf("开口下钻失败: %v", err)
	}
	if len(dr.List) != 2 || dr.List[0].Name == "" || dr.List[0].CreatedAt == "" {
		t.Fatalf("开口名单应有 2 行且字段可渲染，实得 %+v", dr.List)
	}
	for _, r := range dr.List {
		if !r.Spoke {
			t.Fatalf("下钻到开口指标却返回未开口客户：%+v", r)
		}
	}
}

// TestDrillPagingSemantics 分页语义（同 D4）：total 不随分页变、越界页只回空列表不改 total、
// 未知指标/跨租户码都判错而不是默认回某一份名单。
func TestDrillPagingSemantics(t *testing.T) {
	tid := newTenant(t, "acq_a")
	defer testutil.CleanupTenant(t, tid)
	other := newTenant(t, "acq_b")
	defer testutil.CleanupTenant(t, other)
	code := mustCreate(t, tid, "分页测试码", ChannelStore)
	for i := 0; i < 3; i++ {
		cid := seedCustomer(t, tid, fmt.Sprintf("分页客户%d", i))
		if _, err := ApplyToGuest(db.DB, tid, cid, code.Code, fmt.Sprintf("vk-page-%d", i)); err != nil {
			t.Fatalf("归因失败: %v", err)
		}
	}
	p1, err := DrillCustomers(db.DB, tid, code.ID, MetricNew, DefaultWindowDays, 1, 2)
	if err != nil {
		t.Fatalf("第一页失败: %v", err)
	}
	if p1.Total != 3 || len(p1.List) != 2 {
		t.Fatalf("第一页应 total=3 且 2 行，实得 total=%d len=%d", p1.Total, len(p1.List))
	}
	p2, _ := DrillCustomers(db.DB, tid, code.ID, MetricNew, DefaultWindowDays, 2, 2)
	if p2.Total != 3 || len(p2.List) != 1 {
		t.Fatalf("第二页 total 不得随分页变，实得 total=%d len=%d", p2.Total, len(p2.List))
	}
	seen := map[uint]bool{}
	for _, r := range append(p1.List, p2.List...) {
		if seen[r.ID] {
			t.Fatalf("翻页出现重复客户 %d", r.ID)
		}
		seen[r.ID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("两页合起来应覆盖 3 个客户，实得 %d", len(seen))
	}
	p9, _ := DrillCustomers(db.DB, tid, code.ID, MetricNew, DefaultWindowDays, 9, 2)
	if p9.Total != 3 || len(p9.List) != 0 {
		t.Fatalf("越界页必须 list 空、total 如实，实得 total=%d len=%d", p9.Total, len(p9.List))
	}
	// 别人的码在本租户语境下查不到（不回显"别家有这个码"）
	if _, err := DrillCustomers(db.DB, other, code.ID, MetricNew, DefaultWindowDays, 1, 20); err != ErrNotFound {
		t.Fatalf("跨租户下钻应判 ErrNotFound，实得 %v", err)
	}
	if _, err := DrillCustomers(db.DB, tid, code.ID, "", DefaultWindowDays, 1, 20); err != ErrBadMetric {
		t.Fatalf("缺指标应判 ErrBadMetric（不默认回一份名单），实得 %v", err)
	}
}

// TestListStatsTenantScoped 列表只回本租户的码（跨租户不可见，含停用码也不串家）。
func TestListStatsTenantScoped(t *testing.T) {
	tidA := newTenant(t, "acq_a")
	defer testutil.CleanupTenant(t, tidA)
	tidB := newTenant(t, "acq_b")
	defer testutil.CleanupTenant(t, tidB)
	mustCreate(t, tidA, "甲店A", ChannelStore)
	mustCreate(t, tidB, "乙店B", ChannelStore)

	a, err := ListWithStats(db.DB, tidA, "", DefaultWindowDays)
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	if len(a) != 1 || a[0].Code.TenantID != tidA {
		t.Fatalf("A 只该看到自己的码，实得 %+v", a)
	}
	b, _ := ListWithStats(db.DB, tidB, "active", DefaultWindowDays)
	if len(b) != 1 || b[0].Code.Name != "乙店B" {
		t.Fatalf("B 只该看到自己的码，实得 %+v", b)
	}
}

// TestDerivedHandleIsolationRequired 反向用例：**不隔离必须真的报错**。
// 反证不成立就说明护栏是空转的（触达批立下的写法）。
// api 层传进来的是 db.RQ(c)——clone=0，条件就地累加；这里手工复现同样的脏句柄。
func TestDerivedHandleIsolationRequired(t *testing.T) {
	tid := newTenant(t, "acq_a")
	defer testutil.CleanupTenant(t, tid)
	dirty := db.DB.Model(&model.AcquisitionCode{}).Where("tenant_id = ?", tid)
	// 第一条链跑完，句柄已带上 Model=acquisition_codes 与 tenant 条件
	var n int64
	if err := dirty.Count(&n).Error; err != nil {
		t.Fatalf("前置条件被破坏：第一条查询不该报错: %v", err)
	}
	// 第二条查询故意**不经 isolate**，在同一个脏句柄上换成 customers
	var m int64
	err := dirty.Model(&model.Customer{}).Where("acquisition_code = ?", "ZZZZZZZZ").Count(&m).Error
	if err == nil {
		t.Fatalf("脏句柄复用必须真的报错，否则 isolate 这层护栏是空转的（实得 count=%d）", m)
	}
	t.Logf("脏句柄复用如期报错：%v", err)
	// 同一条 SQL 走 isolate 则干净可跑
	ok := db.DB.Session(&gorm.Session{}).Model(&model.Customer{}).Where("acquisition_code = ?", "ZZZZZZZZ")
	var k int64
	if err := ok.Count(&k).Error; err != nil {
		t.Fatalf("隔离句柄应可正常执行: %v", err)
	}
}

// TestSetCodeStatusTenantScoped 启停只认自己家的码：跨租户改不动、也不回显差别。
func TestSetCodeStatusTenantScoped(t *testing.T) {
	tidA := newTenant(t, "acq_a")
	defer testutil.CleanupTenant(t, tidA)
	tidB := newTenant(t, "acq_b")
	defer testutil.CleanupTenant(t, tidB)
	code := mustCreate(t, tidA, "封禁测试码", ChannelStore)

	// 注意：真实调用方传的是 db.RQ(ctx)，跨租户时 where 会多一道租户条件；
	// 这里用平台句柄手工带上 tid，验的就是本函数自己那条 where 有没有漏。
	n, err := SetCodeStatus(db.DB, tidB, code.ID, false)
	if err != nil {
		t.Fatalf("停用调用失败: %v", err)
	}
	if n != 0 {
		t.Fatalf("跨租户停用不得生效")
	}
	var c model.AcquisitionCode
	db.DB.First(&c, code.ID)
	if c.Status != model.AcquisitionStatusActive {
		t.Fatalf("别人的停用不得改我的码状态，实得 %q", c.Status)
	}
	if n, err := SetCodeStatus(db.DB, tidA, code.ID, false); err != nil || n != 1 {
		t.Fatalf("本租户停用应生效: rows=%d err=%v", n, err)
	}
	db.DB.First(&c, code.ID)
	if c.Status != model.AcquisitionStatusDisabled {
		t.Fatalf("停用未落库，实得 %q", c.Status)
	}
}
