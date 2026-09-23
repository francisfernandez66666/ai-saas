// 商机与报价连库单测：建单的入参拒绝与"一客户一在途"（含**绕过应用直插必撞部分唯一索引**的反证）、
// 阶段推进真的按裁决落库（won_at/lost_at/lost_reason 四列字段级核对）、
// 看板六个格子与下钻名单同源（同一谓词、同一份人）、租户与数据范围裁剪在 SQL 条件里生效、
// 报价版本链与状态机落库、过期 sweep 只碰"发出且过期"，以及派生句柄必须隔离的正反向双测。
//
// 纯函数层已把规则逐格跑过（policy_test.go）；这里测的是**规则真的进了 WHERE 条件**。
// 少一个 tenant_id 条件在单测里可能一片绿，线上就是 A 家的单子出现在 B 家看板上。
package deal

import (
	"errors"
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

// newTenant 建一个带语义码的单测租户（复用语义见 testutil；跨租户用例必须各自独立 tid）
func newTenant(t *testing.T, semantic string) uint {
	t.Helper()
	testutil.SetupTestDB(t)
	return testutil.CreateTenantCode(t, semantic)
}

// seedCustomer 造一个本租户客户（显式带 TenantID，过 D6 盖章门禁）
func seedCustomer(t *testing.T, tid uint, name string, ownerUser uint) uint {
	t.Helper()
	c := model.Customer{TenantID: tid, Name: name, Phone: "13800000000", JourneyStage: model.JourneyLeadCaptured, AssignedUserID: ownerUser}
	if err := db.DB.Create(&c).Error; err != nil {
		t.Fatalf("造客户失败: %v", err)
	}
	t.Cleanup(func() { db.DB.Unscoped().Delete(&model.Customer{}, c.ID) })
	return c.ID
}

// mustCreateDeal 建单成功是大多数用例的前置条件，失败直接 fatal
func mustCreateDeal(t *testing.T, tid, cid uint, title, stage string, amount int64) *model.Opportunity {
	t.Helper()
	res, err := CreateDeal(db.DB, CreateInput{TenantID: tid, CustomerID: cid, Title: title, Stage: stage, AmountCents: amount})
	if err != nil || res.Deal == nil {
		t.Fatalf("建单失败: %v", err)
	}
	return res.Deal
}

// seedBackdated 把单子的 stage_entered_at 拨到 N 天前（造"停滞"用；不改代码里的判据，只改数据）
func seedBackdated(t *testing.T, dealID uint, days int) {
	t.Helper()
	err := db.DB.Model(&model.Opportunity{}).Where("id = ?", dealID).
		Update("stage_entered_at", time.Now().Add(-time.Duration(days)*24*time.Hour)).Error
	if err != nil {
		t.Fatalf("回拨阶段时刻失败: %v", err)
	}
}

// 建单入参拒绝逐条：缺租户/缺客户/标题非法/金额非法，且拒绝后必须零落库。
func TestCreateDealRejectsBadInput(t *testing.T) {
	tid := newTenant(t, "deal_a")
	defer testutil.CleanupTenant(t, tid)
	cid := seedCustomer(t, tid, "商机甲", 0)

	cases := []struct {
		name string
		in   CreateInput
		want string
	}{
		{"无租户", CreateInput{CustomerID: cid, Title: "x"}, ReasonTenantRequired},
		{"无客户", CreateInput{TenantID: tid, Title: "x"}, ReasonCustomerNotFound},
		{"假客户（不在本租户）", CreateInput{TenantID: tid, CustomerID: 99999999, Title: "x"}, ReasonCustomerNotFound},
		{"空标题", CreateInput{TenantID: tid, CustomerID: cid, Title: "  "}, ReasonTitleRequired},
		{"负金额", CreateInput{TenantID: tid, CustomerID: cid, Title: "x", AmountCents: -1}, ReasonAmountNegative},
		{"未知阶段", CreateInput{TenantID: tid, CustomerID: cid, Title: "x", Stage: "someday"}, ReasonStageUnknown},
		{"新建即终局", CreateInput{TenantID: tid, CustomerID: cid, Title: "x", Stage: model.DealStageWon, AmountCents: 1}, ReasonStageUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CreateDeal(db.DB, tc.in)
			if RejectReason(err) != tc.want {
				t.Fatalf("应拒为 %s，实得 %v", tc.want, err)
			}
		})
	}
	// 零落库：上面每一条拒绝都必须没留下行（否则"报错了但单子进去了"）
	var n int64
	db.DB.Model(&model.Opportunity{}).Where("tenant_id = ?", tid).Count(&n)
	if n != 0 {
		t.Fatalf("入参全拒的情况下仍落了 %d 张单", n)
	}
}

// 建单默认值与快照：首次建单落 stage_entered_at、来源快照随客户身上的获客码带进来，
// 便于事后回答「这张单当初是谁带来的」。
func TestCreateDealDefaultsAndSourceSnapshot(t *testing.T) {
	tid := newTenant(t, "deal_a")
	defer testutil.CleanupTenant(t, tid)
	cid := seedCustomer(t, tid, "商机乙", 0)

	res, err := CreateDeal(db.DB, CreateInput{TenantID: tid, CustomerID: cid, Title: " 大客户单 ", Source: "wechat_friend", SourceCode: " ABCD2345 "})
	if err != nil {
		t.Fatalf("建单失败: %v", err)
	}
	d := res.Deal
	if d.Stage != model.DealStageQualified {
		t.Fatalf("缺省阶段应为需求确认，实得 %s", d.Stage)
	}
	if d.Source != model.DealSourceManual {
		t.Fatalf("非法来源应归 manual，实得 %s", d.Source)
	}
	if d.Title != "大客户单" || d.SourceCode != "ABCD2345" {
		t.Fatalf("标题/码未清洗：%q / %q", d.Title, d.SourceCode)
	}
	if d.TenantID != tid || d.CustomerID != cid {
		t.Fatalf("租户或客户归属错位: %+v", d)
	}
	if d.StageAt.IsZero() {
		t.Fatal("建单必须写下进入阶段的时刻（停滞计时的起点）")
	}
}

// 一客一在途单三重封堵：应用层显式拒、EnsureOpenDeal 幂等复用、
// 绕过应用直插必须撞 23505；标丢后可再开新单（终局单是历史，不占坑）。
func TestOneOpenDealPerCustomer(t *testing.T) {
	tid := newTenant(t, "deal_a")
	defer testutil.CleanupTenant(t, tid)
	cid := seedCustomer(t, tid, "商机丙", 0)
	d := mustCreateDeal(t, tid, cid, "第一张", model.DealStageLead, 0)

	// 人工建第二张：明确拒，且告诉调用者为什么
	if _, err := CreateDeal(db.DB, CreateInput{TenantID: tid, CustomerID: cid, Title: "第二张"}); RejectReason(err) != ReasonDealAlreadyOpen {
		t.Fatalf("已有在途单时人工建单应拒为 %s，实得 %v", ReasonDealAlreadyOpen, err)
	}
	// 幂等入口：拿回同一张，不新建
	res, err := EnsureOpenDeal(db.DB, CreateInput{TenantID: tid, CustomerID: cid, Title: "自动建单"})
	if err != nil || !res.Existing || res.Deal.ID != d.ID {
		t.Fatalf("幂等入口应复用既有在途单: existing=%v deal=%+v err=%v", res.Existing, res.Deal, err)
	}
	var n int64
	db.DB.Model(&model.Opportunity{}).Where("tenant_id = ? AND customer_id = ?", tid, cid).Count(&n)
	if n != 1 {
		t.Fatalf("两轮建单后仍应只有 1 张，实得 %d", n)
	}

	// 反证：**绕过应用直插第二张在途单必须被 DB 拒**。
	// 这条不成立就说明 migrations/023 的部分唯一索引没建出来（AutoMigrate 建不出带 WHERE 的索引），
	// 那时"在途商机数"与"有在途单的客户数"两个格子会在多实例下静默不相等。
	err = db.DB.Exec("INSERT INTO opportunities (tenant_id, customer_id, title, stage, stage_entered_at, amount_cents, source, created_at, updated_at) VALUES (?, ?, ?, ?, NOW(), 0, 'manual', NOW(), NOW())",
		tid, cid, "绕过应用直插", model.DealStageQuoted).Error
	if err == nil {
		t.Fatal("部分唯一索引未生效：同一客户出现了两张在途商机")
	}
	if !strings.Contains(err.Error(), "23505") && !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("应撞唯一索引 23505，实得 %v", err)
	}

	// 终局后可以重开：历史那张留原样（重开=新建，不在旧行上翻案）
	if _, err := MoveDeal(db.DB, MoveInput{TenantID: tid, DealID: d.ID, To: model.DealStageLost, LostReason: model.DealLostNoBudget, ChangeAmount: false}); err != nil {
		t.Fatalf("标流失失败: %v", err)
	}
	res2, err := CreateDeal(db.DB, CreateInput{TenantID: tid, CustomerID: cid, Title: "重开一张"})
	if err != nil || res2.Existing {
		t.Fatalf("终局后应能新建: %v", err)
	}
	db.DB.Model(&model.Opportunity{}).Where("tenant_id = ? AND customer_id = ?", tid, cid).Count(&n)
	if n != 2 {
		t.Fatalf("流失单 + 新单应共 2 行（历史不删），实得 %d", n)
	}
}

// 「按裁决落库」逐列断言：裁决给什么就写什么，不该动的列一动不动；
// 并发推进时第二条 must 0 行受影响并报冲突，不能假装成功。
func TestMoveDealWritesExactlyWhatVerdictSays(t *testing.T) {
	tid := newTenant(t, "deal_a")
	defer testutil.CleanupTenant(t, tid)
	cid := seedCustomer(t, tid, "商机丁", 0)
	d := mustCreateDeal(t, tid, cid, "报价推进", model.DealStageQuoted, 0)

	// 报价阶段直接标成交但没金额：拒，且库里一个字都不许变
	if _, err := MoveDeal(db.DB, MoveInput{TenantID: tid, DealID: d.ID, To: model.DealStageWon}); RejectReason(err) != ReasonWonAmountRequired {
		t.Fatalf("无金额成交应拒为 %s，实得 %v", ReasonWonAmountRequired, err)
	}
	var before model.Opportunity
	db.DB.First(&before, d.ID)
	if before.Stage != model.DealStageQuoted || before.WonAt != nil {
		t.Fatalf("被拒的推进仍改动了库: %+v", before)
	}

	// 同一请求里带上金额 → 金额与阶段一起生效（won 判的是改完之后的金额）
	back, err := MoveDeal(db.DB, MoveInput{TenantID: tid, DealID: d.ID, To: model.DealStageWon, AmountCents: 28800000, ChangeAmount: true, LostReason: model.DealLostPrice})
	if err != nil {
		t.Fatalf("带金额成交应成功: %v", err)
	}
	if back.Stage != model.DealStageWon || back.AmountCents != 28800000 {
		t.Fatalf("成交落库形态不符: %+v", back)
	}
	if back.WonAt == nil || back.LostAt != nil {
		t.Fatalf("只应盖成交戳: won=%v lost=%v", back.WonAt, back.LostAt)
	}
	// 裁决里的归零规则必须真的落列：成交单上留着流失原因，赢单分析整张表就废了
	if back.LostReason != "" {
		t.Fatalf("成交单的流失原因应被归零，实得 %q", back.LostReason)
	}

	// 终局锁死：再推一律 deal_closed
	if _, err := MoveDeal(db.DB, MoveInput{TenantID: tid, DealID: d.ID, To: model.DealStageLost, LostReason: model.DealLostOther}); RejectReason(err) != ReasonDealClosed {
		t.Fatalf("终局后再推应拒为 %s", ReasonDealClosed)
	}
	if _, err := EditDeal(db.DB, EditInput{TenantID: tid, DealID: d.ID, AmountCents: 1, ChangeAmount: true}); RejectReason(err) != ReasonDealClosed {
		t.Fatalf("终局单不应允许改金额")
	}
	// 跨租户/不存在一律 ErrNotFound（对外 404，不回显"别家有没有这张单"）
	other := newTenant(t, "deal_b")
	defer testutil.CleanupTenant(t, other)
	if _, err := MoveDeal(db.DB, MoveInput{TenantID: other, DealID: d.ID, To: model.DealStageLost, LostReason: model.DealLostOther}); !isNotFound(err) {
		t.Fatalf("跨租户推进必须 404，实得 %v", err)
	}
}

// 编辑不改阶段、清空日期要真写成 NULL（Updates 传 nil 指针不会写 NULL，必须 gorm.Expr）。
func TestEditDealKeepsStageAndClearsDate(t *testing.T) {
	tid := newTenant(t, "deal_a")
	defer testutil.CleanupTenant(t, tid)
	cid := seedCustomer(t, tid, "商机戊", 0)
	close := time.Now().Add(30 * 24 * time.Hour)
	res, err := CreateDeal(db.DB, CreateInput{TenantID: tid, CustomerID: cid, Title: "带预期日", ExpectedCloseAt: &close})
	if err != nil {
		t.Fatalf("建单失败: %v", err)
	}
	d := res.Deal

	// 只改标题：阶段与阶段时刻不得动（EditDeal 里没有 stage 这一列，但守卫写错就会覆盖）
	oldStageAt := d.StageAt
	if _, err := EditDeal(db.DB, EditInput{TenantID: tid, DealID: d.ID, Title: "改过名字"}); err != nil {
		t.Fatalf("改标题失败: %v", err)
	}
	var after model.Opportunity
	db.DB.First(&after, d.ID)
	if after.Title != "改过名字" || after.Stage != d.Stage || !after.StageAt.Equal(oldStageAt) {
		t.Fatalf("编辑标题影响了阶段/计时: %+v", after)
	}
	// 显式清空预期成交日：不承诺比乱承诺诚实，但清空必须是显式的
	if _, err := EditDeal(db.DB, EditInput{TenantID: tid, DealID: d.ID, SetCloseDate: true}); err != nil {
		t.Fatalf("清空日期失败: %v", err)
	}
	var cleared model.Opportunity // 必须是全新变量：复用 after 会把上一次读到的日期留在指针字段里（NULL 不覆写目标）
	db.DB.First(&cleared, d.ID)
	if cleared.ExpectedCloseAt != nil {
		t.Fatalf("清空后应无预期成交日，实得 %v", *cleared.ExpectedCloseAt)
	}
	// 什么都没要改：原样返回、不报错也不写库（前端"只开了弹窗没改任何东西"是常态）
	got, err := EditDeal(db.DB, EditInput{TenantID: tid, DealID: d.ID})
	if err != nil || got.ID != d.ID {
		t.Fatalf("空编辑应原样返回: %v", err)
	}
}

// 看板与名单同源的正面实证：合成 5 张跨阶段单（先做前置自检证明数据真在库里，
// 否则整段等式在 0==0 上假绿），逐筛选断「看板格子数字 == 名单 total == 名单行数」，
// 并锁窗口——days=90 时窗外那张单两侧同时不计，放宽到 365 两侧一起 +1，
// 证明 days 真进了查询而不是只改了样式。
func TestBoardNumbersEqualDrillTotals(t *testing.T) {
	tid := newTenant(t, "deal_a")
	defer testutil.CleanupTenant(t, tid)

	// 六格互不相同的合成人：lead 新鲜 / quoted 停滞 / negotiating 新鲜 / won / lost，
	// 再加一张窗外建的老单（同阶段但 created_at 在窗口外，用来证明 days 真的进了查询）
	cA := seedCustomer(t, tid, "看板甲", 0)
	cB := seedCustomer(t, tid, "看板乙", 0)
	cC := seedCustomer(t, tid, "看板丙", 0)
	cD := seedCustomer(t, tid, "看板丁", 0)
	dLead := mustCreateDeal(t, tid, cA, "线索单", model.DealStageLead, 0)
	dQuoted := mustCreateDeal(t, tid, cB, "停滞报价单", model.DealStageQuoted, 100)
	dNego := mustCreateDeal(t, tid, cC, "谈判单", model.DealStageNegotiating, 200)
	dWon := mustCreateDeal(t, tid, cD, "成交单", model.DealStageQualified, 300)
	if _, err := MoveDeal(db.DB, MoveInput{TenantID: tid, DealID: dWon.ID, To: model.DealStageWon, AmountCents: 300, ChangeAmount: true}); err != nil {
		t.Fatalf("成交失败: %v", err)
	}
	cE := seedCustomer(t, tid, "看板戊", 0)
	dLost := mustCreateDeal(t, tid, cE, "流失单", model.DealStageLead, 0)
	// 流失只能靠推进落终局（建单即终局被拒），所以这里多跑一次 MoveDeal 而不是直改数据
	if _, err := MoveDeal(db.DB, MoveInput{TenantID: tid, DealID: dLost.ID, To: model.DealStageLost, LostReason: model.DealLostTimeout}); err != nil {
		t.Fatalf("流失失败: %v", err)
	}
	seedBackdated(t, dQuoted.ID, 20) // 停滞 20 天
	seedBackdated(t, dLead.ID, 2)    // 在途但没停滞

	board, err := Board(db.DB, Scope{TenantID: tid}, 90, 7)
	if err != nil {
		t.Fatalf("看板失败: %v", err)
	}
	// 落库前置自检：合成数据真的在库里。缺了它，后面每一句"两侧相等"都会在 0==0 上假绿。
	var openCnt int64
	db.DB.Model(&model.Opportunity{}).Where("tenant_id = ? AND stage NOT IN ?", tid,
		[]string{model.DealStageWon, model.DealStageLost}).Count(&openCnt)
	if openCnt != 3 {
		t.Fatalf("合成数据未落库：在途应为 3 张，实得 %d", openCnt)
	}

	cells := map[string]StageCell{}
	for _, c := range board.Stages {
		cells[c.Stage] = c
	}
	if len(board.Stages) != 6 {
		t.Fatalf("六个阶段格子必须全在（零命中也要显示 0），实得 %d", len(board.Stages))
	}
	if cells[model.DealStageLead].Count != 1 || cells[model.DealStageQuoted].Count != 1 || cells[model.DealStageWon].Count != 1 {
		t.Fatalf("阶段计数错位: %+v", board.Stages)
	}
	if cells[model.DealStageWon].AmountCents != 300 {
		t.Fatalf("成交金额应合计 300 分，实得 %d", cells[model.DealStageWon].AmountCents)
	}
	if board.OpenCount != 3 || board.OpenAmountCents != 300 {
		t.Fatalf("在途应为 3 张/300 分，实得 %d/%d", board.OpenCount, board.OpenAmountCents)
	}
	if board.StuckCount != 1 {
		t.Fatalf("停滞应恰好 1 张（只有那张 20 天的），实得 %d", board.StuckCount)
	}
	// 赢单率分母只算终局：1 赢 1 输 = 50%，把 3 张在途算进去会得 20%
	if board.WinRatePct < 49.9 || board.WinRatePct > 50.1 {
		t.Fatalf("赢单率应为 50%%，实得 %v", board.WinRatePct)
	}
	if board.TotalCount != 5 {
		t.Fatalf("总数应为 5，实得 %d", board.TotalCount)
	}

	// **看板每一格与下钻 total 逐格相等**（同源谓词的全部意义就在这条等式上）
	for _, f := range append(append([]string{}, FilterCodes...), StageFilterCodes()...) {
		dr, err := DrillDeals(db.DB, Scope{TenantID: tid}, f, 90, 7, 1, 50)
		if err != nil {
			t.Fatalf("下钻 %s 失败: %v", f, err)
		}
		want := int64(0)
		switch f {
		case FilterOpen:
			want = board.OpenCount
		case FilterStuck:
			want = board.StuckCount
		case FilterWon:
			want = board.WonCount
		case FilterLost:
			want = board.LostCount
		default:
			if s, ok := strings.CutPrefix(f, FilterStagePrefix); ok {
				want = cells[s].Count
			}
		}
		if dr.Total != want {
			t.Fatalf("格子 %s：看板 %d、名单 %d，两侧不同源", f, want, dr.Total)
		}
		if int64(len(dr.List)) != want { // 都在第一页内，长度也必须等
			t.Fatalf("格子 %s：名单长度 %d ≠ total %d", f, len(dr.List), want)
		}
	}

	// 谈判那一格点开的必须恰好是那张谈判单：只等"数量相等"的话，
	// 名单取错人（比如按阶段拼错了 where 却恰好条数一样）也能绿。
	nego, err := DrillDeals(db.DB, Scope{TenantID: tid}, FilterStagePrefix+model.DealStageNegotiating, 90, 7, 1, 50)
	if err != nil {
		t.Fatalf("谈判格下钻失败: %v", err)
	}
	if len(nego.List) != 1 || nego.List[0].ID != dNego.ID {
		t.Fatalf("谈判格名单应恰好是那张谈判单(%d)，实得 %+v", dNego.ID, nego.List)
	}
	if nego.List[0].StalledDays < 0 || nego.List[0].StageName != model.DealStageNames[model.DealStageNegotiating] {
		t.Fatalf("名单行的停滞天数/阶段名未随下发：%+v", nego.List[0])
	}

	// 窗口真的进了查询：造一张"开在 200 天前、今天才成交"的单，收窄 days 时它必须整张消失。
	// 没有这一条，days 可以被实现成"只改样式不改进查询"，而看板与名单会同时错得很整齐。
	cOld := seedCustomer(t, tid, "窗外客户", 0)
	dOld := mustCreateDeal(t, tid, cOld, "窗外老单", model.DealStageQualified, 0)
	if _, err := MoveDeal(db.DB, MoveInput{TenantID: tid, DealID: dOld.ID, To: model.DealStageWon, AmountCents: 999, ChangeAmount: true}); err != nil {
		t.Fatalf("窗外单标成交失败: %v", err)
	}
	if err := db.DB.Model(&model.Opportunity{}).Where("id = ?", dOld.ID).
		Update("created_at", time.Now().Add(-200*24*time.Hour)).Error; err != nil {
		t.Fatalf("回拨建单时刻失败: %v", err)
	}
	narrow, err := Board(db.DB, Scope{TenantID: tid}, 90, 7)
	if err != nil {
		t.Fatalf("二次看板失败: %v", err)
	}
	if narrow.TotalCount != 5 || narrow.WonCount != 1 {
		t.Fatalf("90 天窗口应只见 5 张/1 张成交（窗外那张必须不计），实得 %d/%d", narrow.TotalCount, narrow.WonCount)
	}
	wideBoard, err := Board(db.DB, Scope{TenantID: tid}, 365, 7)
	if err != nil {
		t.Fatalf("365 天看板失败: %v", err)
	}
	if wideBoard.TotalCount != 6 || wideBoard.WonCount != 2 {
		t.Fatalf("放宽到 365 天应见 6 张/2 张成交（与名单同涨才说明两侧同窗），实得 %d/%d", wideBoard.TotalCount, wideBoard.WonCount)
	}
	dr, err := DrillDeals(db.DB, Scope{TenantID: tid}, FilterWon, 30, 7, 1, 50)
	if err != nil {
		t.Fatalf("30 天下钻失败: %v", err)
	}
	if dr.Total != 1 {
		t.Fatalf("30 天窗口内成交应只剩 1 张（窗外那张必须不计），实得 %d", dr.Total)
	}
	放宽, _ := DrillDeals(db.DB, Scope{TenantID: tid}, FilterWon, 365, 7, 1, 50)
	if 放宽.Total != 2 {
		t.Fatalf("放宽到 365 天后成交名单应 +1（证明 days 真进了下钻查询），实得 %d", 放宽.Total)
	}
}

// 下钻分页与数据范围语义：越界页空列表但 total 如实、翻页零重叠、
// 顾问数据范围（CustomerScope）同时收窄看板、名单与详情三处。
func TestDrillPagingAndScopeSemantics(t *testing.T) {
	tid := newTenant(t, "deal_a")
	defer testutil.CleanupTenant(t, tid)
	cids := []uint{}
	for i := 0; i < 3; i++ {
		c := seedCustomer(t, tid, fmt.Sprintf("分页客户%d", i), 0)
		cids = append(cids, c)
		mustCreateDeal(t, tid, c, fmt.Sprintf("分页单%d", i), model.DealStageQualified, int64(i+1)*100)
	}
	dr, err := DrillDeals(db.DB, Scope{TenantID: tid}, FilterOpen, 90, 7, 1, 2)
	if err != nil {
		t.Fatalf("下钻失败: %v", err)
	}
	if dr.Total != 3 || len(dr.List) != 2 {
		t.Fatalf("第一页应 2 行/total 3，实得 %d/%d", len(dr.List), dr.Total)
	}
	// 越界页：list 空、total 如实（前端据此收回页码）
	page9, err := DrillDeals(db.DB, Scope{TenantID: tid}, FilterOpen, 90, 7, 9, 2)
	if err != nil || len(page9.List) != 0 || page9.Total != 3 {
		t.Fatalf("越界页语义不符: total=%d len=%d err=%v", page9.Total, len(page9.List), err)
	}
	// 翻页零重叠 + 不重不漏
	seen := map[uint]bool{}
	for _, p := range []int{1, 2} {
		r, err := DrillDeals(db.DB, Scope{TenantID: tid}, FilterOpen, 90, 7, p, 2)
		if err != nil {
			t.Fatalf("第 %d 页失败: %v", p, err)
		}
		for _, it := range r.List {
			if seen[it.ID] {
				t.Fatalf("分页出现重叠: %d", it.ID)
			}
			seen[it.ID] = true
		}
	}
	if len(seen) != 3 {
		t.Fatalf("两页应覆盖 3 张，实得 %d", len(seen))
	}
	// page_size 硬顶：传 9999 不得决定响应体大小
	if ClampPageSize(9999) != MaxPageSize {
		t.Fatal("page_size 未被钳住")
	}
	// 数据范围裁剪：只允许看 cids[0] 名下客户的单
	scopeSub := db.DB.Model(&model.Customer{}).Select("id").Where("tenant_id = ? AND id = ?", tid, cids[0])
	scoped, err := Board(db.DB, Scope{TenantID: tid, CustomerScope: scopeSub}, 90, 7)
	if err != nil {
		t.Fatalf("裁剪看板失败: %v", err)
	}
	if scoped.TotalCount != 1 || scoped.OpenCount != 1 {
		t.Fatalf("裁剪后应只见 1 张，实得 total=%d open=%d", scoped.TotalCount, scoped.OpenCount)
	}
	dr2, err := DrillDeals(db.DB, Scope{TenantID: tid, CustomerScope: db.DB.Model(&model.Customer{}).Select("id").Where("tenant_id = ? AND id = ?", tid, cids[0])}, FilterOpen, 90, 7, 1, 20)
	if err != nil || dr2.Total != 1 {
		t.Fatalf("裁剪名单应 1 张: total=%d err=%v", dr2.Total, err)
	}
	// 裁剪下打不开别人的单（列表看不见的单换个 ID 就能看 = 数据范围白做）
	var foreign model.Opportunity
	if err := db.DB.Where("tenant_id = ? AND customer_id = ?", tid, cids[1]).First(&foreign).Error; err != nil {
		t.Fatalf("前置数据缺失: %v", err)
	}
	if _, _, err := GetDeal(db.DB, Scope{TenantID: tid, CustomerScope: db.DB.Model(&model.Customer{}).Select("id").Where("tenant_id = ? AND id = ?", tid, cids[0])}, foreign.ID); !isNotFound(err) {
		t.Fatalf("越权详情必须 404，实得 %v", err)
	}
}

// 跨租户不可见：别家的单在看板数字、名单、详情三个面上都不出现，也不回显差别。
func TestBoardAndDrillAreTenantScoped(t *testing.T) {
	tidA := newTenant(t, "deal_a")
	defer testutil.CleanupTenant(t, tidA)
	tidB := newTenant(t, "deal_b")
	defer testutil.CleanupTenant(t, tidB)
	cA := seedCustomer(t, tidA, "甲家客户", 0)
	cB := seedCustomer(t, tidB, "乙家客户", 0)
	d := mustCreateDeal(t, tidA, cA, "甲的单", model.DealStageQualified, 500)
	mustCreateDeal(t, tidB, cB, "乙的单", model.DealStageLead, 0)

	bA, _ := Board(db.DB, Scope{TenantID: tidA}, 90, 7)
	bB, _ := Board(db.DB, Scope{TenantID: tidB}, 90, 7)
	if bA.TotalCount != 1 || bB.TotalCount != 1 {
		t.Fatalf("两家各应只见自己 1 张: A=%d B=%d", bA.TotalCount, bB.TotalCount)
	}
	if bA.OpenAmountCents != 500 || bB.OpenAmountCents != 0 {
		t.Fatalf("金额跨租户泄露: A=%d B=%d", bA.OpenAmountCents, bB.OpenAmountCents)
	}
	if _, _, err := GetDeal(db.DB, Scope{TenantID: tidB}, d.ID); !isNotFound(err) {
		t.Fatalf("跨租户详情必须 404，实得 %v", err)
	}
	// 推进也不能碰别人家的单
	if _, err := MoveDeal(db.DB, MoveInput{TenantID: tidB, DealID: d.ID, To: model.DealStageQuoted}); !isNotFound(err) {
		t.Fatalf("跨租户推进必须 404，实得 %v", err)
	}
}

// 报价单版本链与在途唯一：新草拟自动把旧 draft/sent 置 superseded、version 递增，
// 绕过应用直插第二张在途单必须撞 23505；报价接受不得顺手把商机推成成交（那是人的决定）。
func TestQuoteVersionChainAndStateLock(t *testing.T) {
	tid := newTenant(t, "deal_a")
	defer testutil.CleanupTenant(t, tid)
	cid := seedCustomer(t, tid, "报价客户", 0)
	d := mustCreateDeal(t, tid, cid, "报价链", model.DealStageQuoted, 0)

	lines := []QuoteLineInput{{Name: "XT5", Qty: 1, UnitCents: 26990000}, {Name: "延保", Qty: 2, UnitCents: 480000}}
	v1, err := CreateQuote(db.DB, CreateQuoteInput{TenantID: tid, DealID: d.ID, CreatedBy: 7, Lines: lines, Note: "首版"})
	if err != nil {
		t.Fatalf("建报价失败: %v", err)
	}
	if v1.Version != 1 || v1.Status != model.QuoteStatusDraft || v1.TotalCents != 26990000+960000 {
		t.Fatalf("首版形态不符: %+v", v1)
	}
	// 前端传来的合计不作数：明细里没有 total 字段可传，后端按行算（此处断"合计与行一致"）
	var raw model.Quote
	db.DB.First(&raw, v1.ID)
	if !strings.Contains(raw.Lines, "XT5") || !strings.Contains(raw.Lines, `"total_cents":26990000`) {
		t.Fatalf("明细未按行回填小计: %s", raw.Lines)
	}

	// 未发出就改：允许；改完合计必须重算
	if _, err := EditQuote(db.DB, EditQuoteInput{TenantID: tid, QuoteID: v1.ID, Lines: []QuoteLineInput{{Name: "XT5", Qty: 2, UnitCents: 100}}}); err != nil {
		t.Fatalf("草稿应可编辑: %v", err)
	}
	chk, _ := GetQuote(db.DB, tid, v1.ID)
	if chk.TotalCents != 200 || len(chk.Lines) != 1 {
		t.Fatalf("编辑后合计未重算: %d", chk.TotalCents)
	}

	// 发出 → 锁定，编辑必须被拒
	sent, err := SendQuote(db.DB, ActInput{TenantID: tid, QuoteID: v1.ID})
	if err != nil || sent.Status != model.QuoteStatusSent || sent.SentAt == "" {
		t.Fatalf("发出失败: %+v %v", sent, err)
	}
	if _, err := EditQuote(db.DB, EditQuoteInput{TenantID: tid, QuoteID: v1.ID, Lines: lines}); RejectReason(err) != ReasonQuoteLocked {
		t.Fatalf("已发出的报价应拒为 %s，实得 %v", ReasonQuoteLocked, err)
	}
	// 重复发出也拒（状态机不是幂等装饰：sent_at 被刷第二次就等于"我们改了发出去的那张"）
	if _, err := SendQuote(db.DB, ActInput{TenantID: tid, QuoteID: v1.ID}); err == nil {
		t.Fatal("重复发出必须报错")
	}

	// 出新版本：v1 被取代、v2 是草稿，版本号继续往前（绝不因为"没有活口了"退回 v1）
	v2, err := CreateQuote(db.DB, CreateQuoteInput{TenantID: tid, DealID: d.ID, Lines: lines, SendNow: true})
	if err != nil {
		t.Fatalf("建 v2 失败: %v", err)
	}
	if v2.Version != 2 || v2.Status != model.QuoteStatusSent {
		t.Fatalf("v2 形态不符: %+v", v2)
	}
	old, _ := GetQuote(db.DB, tid, v1.ID)
	if old.Status != model.QuoteStatusSuperseded {
		t.Fatalf("v1 应被新版取代，实得 %s", old.Status)
	}
	// 同一商机同时至多一张活口：反证——绕过应用直插第二张 draft 必撞 023 的部分唯一索引
	err = db.DB.Exec("INSERT INTO quotes (tenant_id, opportunity_id, version, lines, total_cents, status, created_at, updated_at) VALUES (?, ?, 99, '[]', 0, 'draft', NOW(), NOW())", tid, d.ID).Error
	if err == nil {
		t.Fatal("部分唯一索引未生效：同一商机出现两张在途报价")
	}
	if !strings.Contains(err.Error(), "23505") && !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("应撞唯一索引，实得 %v", err)
	}

	// 答复：只有已发出的能被接受/拒绝；接受后不得再拒绝，也不自动改商机阶段
	if _, err := DecideQuote(db.DB, ActInput{TenantID: tid, QuoteID: v2.ID}, true); err != nil {
		t.Fatalf("接受失败: %v", err)
	}
	if _, err := DecideQuote(db.DB, ActInput{TenantID: tid, QuoteID: v2.ID}, false); RejectReason(err) != ReasonQuoteFinal {
		t.Fatalf("已接受的再拒绝应拒为 %s，实得 %v", ReasonQuoteFinal, err)
	}
	var afterDecide model.Opportunity
	db.DB.First(&afterDecide, d.ID)
	if afterDecide.Stage != model.DealStageQuoted {
		t.Fatalf("接受报价不得自动改商机阶段（成交是管理判断，不是报价副作用），实得 %s", afterDecide.Stage)
	}
	if afterDecide.UpdatedAt.IsZero() {
		t.Fatal("前置数据异常")
	}
	// 报价列表按版本倒序，且已接受的历史仍在（改版不覆盖）
	list, err := ListQuotes(db.DB, tid, d.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("版本序列应 2 条，实得 %d (%v)", len(list), err)
	}
	if list[0].Version != 2 || list[1].Version != 1 {
		t.Fatalf("版本应倒序: %+v", list)
	}
	// 终局单的报价一律拒建
	if _, err := MoveDeal(db.DB, MoveInput{TenantID: tid, DealID: d.ID, To: model.DealStageLost, LostReason: model.DealLostPrice}); err != nil {
		t.Fatalf("标流失失败: %v", err)
	}
	if _, err := CreateQuote(db.DB, CreateQuoteInput{TenantID: tid, DealID: d.ID, Lines: lines}); RejectReason(err) != ReasonDealClosed {
		t.Fatalf("终局单出报价应拒为 %s，实得 %v", ReasonDealClosed, err)
	}
}

// 过期巡检只碰「已发出且真过期」：草稿、未到期、已终态一律不动，且重复跑幂等。
func TestSweepExpiredOnlyTouchesSentAndOverdue(t *testing.T) {
	tid := newTenant(t, "deal_a")
	defer testutil.CleanupTenant(t, tid)
	lines := []QuoteLineInput{{Name: "车", Qty: 1, UnitCents: 100}}

	past := time.Now().Add(-24 * time.Hour)
	future := time.Now().Add(24 * time.Hour)

	c1 := seedCustomer(t, tid, "过期甲", 0)
	d1 := mustCreateDeal(t, tid, c1, "过期单", model.DealStageQuoted, 0)
	qSent, err := CreateQuote(db.DB, CreateQuoteInput{TenantID: tid, DealID: d1.ID, Lines: lines, ValidUntil: &past, SendNow: true})
	if err != nil {
		t.Fatalf("建过期报价失败: %v", err)
	}
	c2 := seedCustomer(t, tid, "未到期乙", 0)
	d2 := mustCreateDeal(t, tid, c2, "未到期单", model.DealStageQuoted, 0)
	qFuture, err := CreateQuote(db.DB, CreateQuoteInput{TenantID: tid, DealID: d2.ID, Lines: lines, ValidUntil: &future, SendNow: true})
	if err != nil {
		t.Fatalf("建未到期报价失败: %v", err)
	}
	c3 := seedCustomer(t, tid, "无期限丙", 0)
	d3 := mustCreateDeal(t, tid, c3, "无期限单", model.DealStageQuoted, 0)
	qNoUntil, err := CreateQuote(db.DB, CreateQuoteInput{TenantID: tid, DealID: d3.ID, Lines: lines, SendNow: true})
	if err != nil {
		t.Fatalf("建无期限报价失败: %v", err)
	}
	// 已接受的那张即便过期也不动：客户已经点头了，系统不去改判定结果
	c4 := seedCustomer(t, tid, "已接受丁", 0)
	d4 := mustCreateDeal(t, tid, c4, "已接受单", model.DealStageQuoted, 0)
	qAccepted, err := CreateQuote(db.DB, CreateQuoteInput{TenantID: tid, DealID: d4.ID, Lines: lines, ValidUntil: &past, SendNow: true})
	if err != nil {
		t.Fatalf("建已接受报价失败: %v", err)
	}
	if _, err := DecideQuote(db.DB, ActInput{TenantID: tid, QuoteID: qAccepted.ID}, true); err != nil {
		t.Fatalf("接受失败: %v", err)
	}

	n, err := SweepExpiredQuotes(db.DB, time.Now())
	if err != nil {
		t.Fatalf("sweep 失败: %v", err)
	}
	if n == 0 {
		t.Fatal("sweep 命中 0 行：过期判据没进 SQL（用例将在 0==0 上空转）")
	}
	assertStatus(t, qSent.ID, model.QuoteStatusExpired)
	assertStatus(t, qFuture.ID, model.QuoteStatusSent)
	assertStatus(t, qNoUntil.ID, model.QuoteStatusSent)
	assertStatus(t, qAccepted.ID, model.QuoteStatusAccepted)

	// 幂等：再跑一遍不多不少（sweep 每小时跑，重复处理会把状态刷成流水账）
	n2, err := SweepExpiredQuotes(db.DB, time.Now())
	if err != nil || n2 != 0 {
		t.Fatalf("二次 sweep 应 0 行，实得 %d (%v)", n2, err)
	}
}

// 报价详情租户作用域：跨租户拿别人 ID 一律 404，不回显「存在但不可见」。
func TestQuoteDetailTenantScopedAndCrossTenantInvisible(t *testing.T) {
	tidA := newTenant(t, "deal_a")
	defer testutil.CleanupTenant(t, tidA)
	tidB := newTenant(t, "deal_b")
	defer testutil.CleanupTenant(t, tidB)
	cA := seedCustomer(t, tidA, "报价甲家", 0)
	d := mustCreateDeal(t, tidA, cA, "甲家报价单", model.DealStageQuoted, 0)
	q, err := CreateQuote(db.DB, CreateQuoteInput{TenantID: tidA, DealID: d.ID, CreatedBy: 9, Lines: []QuoteLineInput{{Name: "车", Qty: 1, UnitCents: 100}}})
	if err != nil {
		t.Fatalf("建报价失败: %v", err)
	}
	if _, err := GetQuote(db.DB, tidB, q.ID); !isNotFound(err) {
		t.Fatalf("跨租户看报价必须 404，实得 %v", err)
	}
	if _, err := SendQuote(db.DB, ActInput{TenantID: tidB, QuoteID: q.ID}); !isNotFound(err) {
		t.Fatalf("跨租户发报价必须 404，实得 %v", err)
	}
	// 报价详情里的明细必须带小计（前端只读，不再自己乘一遍）
	detail, err := GetQuote(db.DB, tidA, q.ID)
	if err != nil {
		t.Fatalf("本租户取详情失败: %v", err)
	}
	if len(detail.Lines) != 1 || detail.Lines[0].TotalCents != 100 {
		t.Fatalf("明细小计缺失: %+v", detail.Lines)
	}
	if detail.CreatedBy != 9 {
		t.Fatalf("建单人应透传（作废/争议时要能找到是谁开的），实得 %d", detail.CreatedBy)
	}
}

// TestDerivedHandleIsolationRequired 反向用例：**不隔离必须真的报错**。
// 反证不成立就说明护栏是空转的（触达批立下的写法）。
// api 层传进来的是 db.RQ(c)——clone=0，条件就地累加；这里手工复现同样的脏句柄。
//
// 判据选的是"第一条查询的列条件被带进第二张表"：opportunities 有 stage、customers 有
// journey_stage，把 customers 的条件漏进 opportunities 就是 42703。
// （第一版这里用的是"换 Model 后两个租户条件共存"，那条在两张表都有 tenant_id 时
// 根本不会报错，等于用一条永真的反证冒充护栏——首跑即被自己抓到。）
func TestDerivedHandleIsolationRequired(t *testing.T) {
	tid := newTenant(t, "deal_a")
	defer testutil.CleanupTenant(t, tid)
	dirty := db.DB.Model(&model.Customer{}).Where("tenant_id = ? AND journey_stage = ?", tid, model.JourneyOrdered)
	var n int64
	if err := dirty.Count(&n).Error; err != nil {
		t.Fatalf("前置条件被破坏：第一条查询不该报错: %v", err)
	}
	// 第二条查询故意**不经 isolate**，在同一个脏句柄上换成商机表
	var m int64
	err := dirty.Model(&model.Opportunity{}).Where("stage = ?", model.DealStageWon).Count(&m).Error
	if err == nil {
		t.Fatalf("脏句柄复用必须真的报错，否则 isolate 这层护栏是空转的（实得 count=%d）", m)
	}
	if !strings.Contains(err.Error(), "42703") {
		t.Fatalf("应撞「列不存在 42703」（证明第一条件的列被带进了第二张表），实得 %v", err)
	}
	t.Logf("脏句柄复用如期报错：%v", err)
	// 同一条 SQL 走 isolate 则干净可跑
	var k int64
	if err := db.DB.Session(&gorm.Session{}).Model(&model.Opportunity{}).
		Where("tenant_id = ? AND stage = ?", tid, model.DealStageWon).Count(&k).Error; err != nil {
		t.Fatalf("隔离句柄应可正常执行: %v", err)
	}
}

// assertStatus 读库断言报价单状态（字段级，不看返回值的内存副本）
func assertStatus(t *testing.T, id uint, want string) {
	t.Helper()
	var q model.Quote
	if err := db.DB.First(&q, id).Error; err != nil {
		t.Fatalf("读报价失败: %v", err)
	}
	if q.Status != want {
		t.Fatalf("报价 %d 状态应为 %s，实得 %s", id, want, q.Status)
	}
}

// isNotFound 断"对外一律 404"的那条线：只有 ErrNotFound 才算，别的错误不得冒充
func isNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
