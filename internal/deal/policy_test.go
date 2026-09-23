// 商机与报价的纯逻辑单测：阶段机（只准往深走 + 终局锁定 + 成交要有钱、流失要有原因）、
// 金额与标题边界、报价明细的服务端重算（含溢出与行数上限）、报价状态机全矩阵、
// 看板过滤谓词与标签的一致性、停滞天数计算与阈值钳位。
//
// 这一层不碰库是有意的：上面每一条判据都是"如果写错了会静默产生错账"的规则，
// 规则必须在没有 DB 的情况下也能被逐格跑到（连库测的是"规则真的落进了 SQL"，见 service_test.go）。
package deal

import (
	"strings"
	"testing"
	"time"

	"ai-scrm/internal/model"
)

// 统一的"现在"，让所有时间判定可复算（不注入的话，跨秒的用例会在 CI 上偶发红）
var fixedNow = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

// 阶段推进裁决全表：只准沿 lead→qualified→quoted→negotiating→won 同序前进，
// 回退、跳步、原地不动各自回稳定原因码——推进不成却报成功，销售看到的就是假成交。
func TestStageMoveForwardOnly(t *testing.T) {
	cases := []struct {
		name string
		in   StageMoveInput
		want string // ""=允许
	}{
		{"未知目标阶段", StageMoveInput{From: model.DealStageLead, To: "someday"}, ReasonStageUnknown},
		{"未知当前阶段", StageMoveInput{From: "???", To: model.DealStageQuoted}, ReasonStageUnknown},
		{"原地不动", StageMoveInput{From: model.DealStageQuoted, To: model.DealStageQuoted}, ReasonSameStage},
		{"往深推进", StageMoveInput{From: model.DealStageLead, To: model.DealStageQualified}, ""},
		{"跨阶段往前", StageMoveInput{From: model.DealStageQualified, To: model.DealStageNegotiating}, ""},
		{"逆向退回", StageMoveInput{From: model.DealStageNegotiating, To: model.DealStageQuoted}, ReasonStageBackward},
		{"退回起点", StageMoveInput{From: model.DealStageQuoted, To: model.DealStageLead}, ReasonStageBackward},
		{"成交但没金额", StageMoveInput{From: model.DealStageNegotiating, To: model.DealStageWon}, ReasonWonAmountRequired},
		{"成交带金额", StageMoveInput{From: model.DealStageNegotiating, To: model.DealStageWon, AmountCents: 1}, ""},
		{"流失不给原因", StageMoveInput{From: model.DealStageQuoted, To: model.DealStageLost}, ReasonLostReasonRequired},
		{"流失原因不认识", StageMoveInput{From: model.DealStageQuoted, To: model.DealStageLost, LostReason: "cheap"}, ReasonLostReasonUnknown},
		{"流失给合法原因", StageMoveInput{From: model.DealStageQuoted, To: model.DealStageLost, LostReason: model.DealLostPrice}, ""},
		// 从最浅阶段直通终局是允许的：小单谈两句就成交，不必为了"走完流程"假装在谈判
		{"线索直通成交", StageMoveInput{From: model.DealStageLead, To: model.DealStageWon, AmountCents: 8800}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.in.Now = fixedNow
			got := DecideMove(tc.in)
			if tc.want == "" {
				if !got.Allow {
					t.Fatalf("应允许，实拒 %q", got.Reason)
				}
				return
			}
			if got.Allow {
				t.Fatalf("应拒为 %q，实得允许", tc.want)
			}
			if got.Reason != tc.want {
				t.Fatalf("原因码应为 %q，实得 %q", tc.want, got.Reason)
			}
		})
	}
}

// 终局锁：won/lost 之后任意目标阶段一律 deal_closed（历史单不允许被后续点击改写）。
func TestTerminalStagesLockTheDeal(t *testing.T) {
	// 终局行是历史事实：任何目标都拒，且拒因统一（前端只按一个码隐藏按钮）
	for _, from := range []string{model.DealStageWon, model.DealStageLost} {
		for _, to := range append(append([]string{}, model.DealStages...), model.DealStageWon, model.DealStageLost) {
			got := DecideMove(StageMoveInput{From: from, To: to, AmountCents: 100, LostReason: model.DealLostOther, Now: fixedNow})
			if got.Allow {
				t.Fatalf("终局 %s 推进到 %s 竟然被允许", from, to)
			}
			if got.Reason != ReasonDealClosed {
				t.Fatalf("%s→%s 拒因应为 %s，实得 %s", from, to, ReasonDealClosed, got.Reason)
			}
		}
	}
}

// 裁决输出的时间戳与流失原因归零规则：进 won 才落 won_at、进 lost 才落 lost_at，
// 非流失态必须清空 lost_reason，否则一张单子上会同时挂着「成交」和「价格太高」。
func TestMoveResultNormalizesStampsAndLostReason(t *testing.T) {
	// 成交：盖 won_at、清 lost_reason（哪怕前端把上一次填的流失原因一起传上来）
	got := DecideMove(StageMoveInput{From: model.DealStageNegotiating, To: model.DealStageWon,
		AmountCents: 100, LostReason: model.DealLostPrice, Now: fixedNow})
	if !got.Allow {
		t.Fatalf("应允许: %s", got.Reason)
	}
	if got.WonAt == nil || !got.WonAt.Equal(fixedNow) {
		t.Fatalf("成交必须盖 won_at 且等于注入时刻，实得 %v", got.WonAt)
	}
	if got.LostAt != nil {
		t.Fatal("成交单不得有流失时间")
	}
	if got.LostReason != "" {
		t.Fatalf("成交单的流失原因必须归零，实得 %q", got.LostReason)
	}
	if !got.Terminal {
		t.Fatal("成交后应标为终局")
	}
	// 流失：盖 lost_at、留原因、不盖 won_at
	lost := DecideMove(StageMoveInput{From: model.DealStageQualified, To: model.DealStageLost,
		AmountCents: 100, LostReason: model.DealLostCompetitor, Now: fixedNow})
	if lost.LostAt == nil || lost.WonAt != nil || lost.LostReason != model.DealLostCompetitor {
		t.Fatalf("流失落库形态不符：%+v", lost)
	}
	// 中间阶段：两个终局戳都不许有，且停滞计时从今天重新开始
	mid := DecideMove(StageMoveInput{From: model.DealStageQualified, To: model.DealStageQuoted, Now: fixedNow})
	if mid.WonAt != nil || mid.LostAt != nil || !mid.StageAt.Equal(fixedNow) || mid.Terminal {
		t.Fatalf("在途推进形态不符：%+v", mid)
	}
}

// 金额与标题边界：按字素数长度、金额上下限（负数与超上限都拒），
// 报价单开着开着变成负数或天文数字都是数据事故，不是显示问题。
func TestValidateAmountAndTitleBounds(t *testing.T) {
	if r := ValidateAmount(-1); r != ReasonAmountNegative {
		t.Fatalf("负数金额应拒为 %s，实得 %s", ReasonAmountNegative, r)
	}
	if r := ValidateAmount(MaxAmountCents + 1); r != ReasonAmountTooLarge {
		t.Fatalf("超限金额应拒为 %s，实得 %s", ReasonAmountTooLarge, r)
	}
	if r := ValidateAmount(0); r != "" {
		t.Fatalf("0 分是合法值（还没问到价），实拒 %s", r)
	}
	if r := ValidateTitle("   "); r != ReasonTitleRequired {
		t.Fatalf("空标题应拒为 %s，实得 %s", ReasonTitleRequired, r)
	}
	long := strings.Repeat("车", MaxTitleRunes+1)
	if r := ValidateTitle(long); r != ReasonTitleTooLong {
		t.Fatalf("中文按字素计长，超 %d 字应拒为 %s，实得 %s", MaxTitleRunes, ReasonTitleTooLong, r)
	}
	// 恰好上限必须通过：按字节算的话这条会在中文标题上把正常单子拒掉
	if r := ValidateTitle(strings.Repeat("车", MaxTitleRunes)); r != "" {
		t.Fatalf("恰好 %d 个中文字应通过，实拒 %s", MaxTitleRunes, r)
	}
}

// 商机来源枚举归一：未知来源回落到手动，非法值不得静默入库（来源列是渠道归因口径的一部分）。
func TestNormalizeAndValidateSource(t *testing.T) {
	for _, s := range []string{model.DealSourceManual, model.DealSourceAcquisition, model.DealSourceAI} {
		if NormalizeSource(s) != s {
			t.Fatalf("合法来源 %s 被改写为 %s", s, NormalizeSource(s))
		}
		if !IsValidSource(s) {
			t.Fatalf("严格校验应认得 %s", s)
		}
	}
	if NormalizeSource("wechat_friend") != model.DealSourceManual {
		t.Fatal("非法来源应归 manual（旁路信息不拒单）")
	}
	if IsValidSource("wechat_friend") {
		t.Fatal("接口入参的严格校验必须拒掉未知来源，静默归一会让前端 bug 永久隐身")
	}
}

// 报价单状态机全矩阵（7 状态 × 6 动作）：期望表硬编码，
// 已终态（accepted/declined/void/superseded/expired）任何动作都必须被拒——商务证据不可篡改。
func TestQuoteActionMatrix(t *testing.T) {
	statuses := []string{
		model.QuoteStatusDraft, model.QuoteStatusSent, model.QuoteStatusAccepted,
		model.QuoteStatusDeclined, model.QuoteStatusSuperseded, model.QuoteStatusVoid,
		model.QuoteStatusExpired,
	}
	// 期望表逐格写死：这张矩阵就是报价单的全部生命周期，
	// 任何一格被"顺手放宽"（比如让已发出的能被编辑）都会在客户手上留下对不上的钱数。
	want := map[string]map[string]string{
		model.QuoteStatusDraft: {
			ActionEdit: "", ActionSend: "", ActionAccept: ReasonQuoteNotSent,
			ActionDecline: ReasonQuoteNotSent, ActionVoid: "", ActionSupersede: "",
		},
		model.QuoteStatusSent: {
			ActionEdit: ReasonQuoteLocked, ActionSend: ReasonQuoteNotDraft, ActionAccept: "",
			ActionDecline: "", ActionVoid: "", ActionSupersede: "",
		},
		model.QuoteStatusAccepted:   finalRow(),
		model.QuoteStatusDeclined:   finalRow(),
		model.QuoteStatusSuperseded: finalRow(),
		model.QuoteStatusVoid:       finalRow(),
		model.QuoteStatusExpired:    finalRow(),
	}
	for _, st := range statuses {
		for act, exp := range want[st] {
			if got := DecideQuoteAction(st, act); got != exp {
				t.Fatalf("状态 %s 的动作 %s 应判 %q，实得 %q", st, act, exp, got)
			}
		}
	}
	if DecideQuoteAction(model.QuoteStatusDraft, "teleport") != ReasonQuoteFinal {
		t.Fatal("未知动作必须拒，不能默认允许")
	}
	for _, st := range statuses {
		final := st != model.QuoteStatusDraft && st != model.QuoteStatusSent
		if IsQuoteFinal(st) != final {
			t.Fatalf("%s 的终局判定错位", st)
		}
	}
}

// finalRow 终局态一律拒绝所有动作
func finalRow() map[string]string {
	return map[string]string{
		ActionEdit: ReasonQuoteFinal, ActionSend: ReasonQuoteFinal, ActionAccept: ReasonQuoteFinal,
		ActionDecline: ReasonQuoteFinal, ActionVoid: ReasonQuoteFinal, ActionSupersede: ReasonQuoteFinal,
	}
}

// 明细小计与总额由服务端重算：前端传来的行金额一律不信，行合计与单头合计必须自洽。
func TestBuildQuoteLinesRecomputesTotals(t *testing.T) {
	in := []QuoteLineInput{
		{Name: " XT5 豪华版 ", Qty: 2, UnitCents: 26990000},
		{Name: "延保套餐", Qty: 1, UnitCents: 480000},
	}
	lines, total, r := BuildQuoteLines(in)
	if r != "" {
		t.Fatalf("合法明细被拒: %s", r)
	}
	if total != 2*26990000+480000 {
		t.Fatalf("合计应由服务端算出，实得 %d", total)
	}
	if lines[0].Name != "XT5 豪华版" {
		t.Fatalf("项目名应去空白，实得 %q", lines[0].Name)
	}
	if lines[0].TotalCents != 2*26990000 || lines[1].TotalCents != 480000 {
		t.Fatalf("行小计未回填：%+v", lines)
	}
}

// 明细入参拒绝：空名/数量为 0/超行数上限/总额溢出，逐项回稳定原因码。
func TestBuildQuoteLinesRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		in   []QuoteLineInput
		want string
	}{
		{"空明细", nil, ReasonQuoteLinesRequired},
		{"项目名为空", []QuoteLineInput{{Name: "  ", Qty: 1, UnitCents: 1}}, ReasonQuoteLineName},
		{"数量为 0", []QuoteLineInput{{Name: "车", Qty: 0, UnitCents: 1}}, ReasonQuoteLineQty},
		{"数量为负", []QuoteLineInput{{Name: "车", Qty: -3, UnitCents: 1}}, ReasonQuoteLineQty},
		{"单价为负", []QuoteLineInput{{Name: "车", Qty: 1, UnitCents: -1}}, ReasonQuoteLineUnit},
		{"行小计溢出", []QuoteLineInput{{Name: "车", Qty: MaxQtyPerLine, UnitCents: MaxAmountCents}}, ReasonQuoteTotalTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines, total, r := BuildQuoteLines(tc.in)
			if r != tc.want {
				t.Fatalf("应拒为 %s，实得 %q（lines=%d total=%d）", tc.want, r, len(lines), total)
			}
			if lines != nil || total != 0 {
				t.Fatal("拒绝时不得返回半成品明细或金额")
			}
		})
	}
	tooMany := make([]QuoteLineInput, MaxQuoteLines+1)
	for i := range tooMany {
		tooMany[i] = QuoteLineInput{Name: "行", Qty: 1, UnitCents: 1}
	}
	if _, _, r := BuildQuoteLines(tooMany); r != ReasonQuoteTooManyLines {
		t.Fatalf("超过 %d 行应拒为 %s，实得 %s", MaxQuoteLines, ReasonQuoteTooManyLines, r)
	}
	// 合计超上限：每行都合法，加起来爆表——只判行不判合计的话，total 会变成负数
	var sum []QuoteLineInput
	for i := 0; i < MaxQuoteLines; i++ {
		sum = append(sum, QuoteLineInput{Name: "车", Qty: MaxQtyPerLine, UnitCents: MaxAmountCents / MaxQtyPerLine})
	}
	if _, _, r := BuildQuoteLines(sum); r != ReasonQuoteTotalTooLarge {
		t.Fatalf("合计超上限必须拒（否则溢出成负数），实得 %s", r)
	}
}

// 筛选码与看板分组同源：每个码都有中文名，且 IsValidFilter 与 FilterCodes 恒一致。
func TestFilterCodesAreValidAndLabeled(t *testing.T) {
	all := append(append([]string{}, FilterCodes...), StageFilterCodes()...)
	seen := map[string]bool{}
	for _, f := range all {
		if seen[f] {
			t.Fatalf("过滤码 %s 重复出现，前端会渲染两个同名格子", f)
		}
		seen[f] = true
		if !IsValidFilter(f) {
			t.Fatalf("内置过滤码 %s 自检不通过", f)
		}
		if strings.TrimSpace(FilterLabel(f)) == "" {
			t.Fatalf("过滤码 %s 没有中文名", f)
		}
	}
	for _, bad := range []string{"", "stages", "stage:teleport", "customer"} {
		if IsValidFilter(bad) {
			t.Fatalf("非法过滤码 %q 被判合法（不认识的必须拒，不能默认回某一份名单）", bad)
		}
	}
}

// 谓词与阶段集合互证：open/stuck/won/lost 四组的成员必须与 IsTerminal、阶段表口径吻合，
// 看板格子和点开名单答的不是同一个问题时，数字就失去了可核对性。
func TestMatchFilterGroupsAgreeWithStageSets(t *testing.T) {
	stale := fixedNow.Add(-30 * 24 * time.Hour)
	fresh := fixedNow.Add(-1 * time.Hour)
	stages := append(append([]string{}, model.DealStages...), model.DealStageWon, model.DealStageLost)

	// open 与"四个在途阶段格子"的并集必须严格相等——这是"卡片数 == 名单长度"的结构前提
	var openUnion int
	for _, s := range stages {
		if MatchFilter(FilterOpen, s, stale, fixedNow, DefaultStuckDays) {
			openUnion++
		}
		if MatchFilter(FilterStagePrefix+s, s, stale, fixedNow, DefaultStuckDays) != true {
			t.Fatalf("stage:%s 格子对自身阶段必须命中", s)
		}
		if MatchFilter(FilterStagePrefix+s, model.DealStageLead, stale, fixedNow, DefaultStuckDays) && s != model.DealStageLead {
			t.Fatalf("stage:%s 不该命中别的阶段", s)
		}
	}
	if openUnion != len(model.DealStages) {
		t.Fatalf("open 应命中全部 %d 个在途阶段，实得 %d", len(model.DealStages), openUnion)
	}
	// 停滞：只有"在途 + 超阈"命中；阈值刚好边界算命中（>=），否则同一时刻两侧判不一致
	if !MatchFilter(FilterStuck, model.DealStageQuoted, fixedNow.Add(-7*24*time.Hour), fixedNow, 7) {
		t.Fatal("恰好 7 天应计入停滞（判据是 >=）")
	}
	if MatchFilter(FilterStuck, model.DealStageQuoted, fresh, fixedNow, 7) {
		t.Fatal("1 小时前的推进不该算停滞")
	}
	for _, term := range []string{model.DealStageWon, model.DealStageLost} {
		if MatchFilter(FilterStuck, term, stale, fixedNow, 7) {
			t.Fatalf("终局单 %s 不进停滞榜：它已经不用跟了", term)
		}
	}
}

// 停滞天数不得为负、零时间不炸（停滞榜排序靠它，算错就永远空或永远满）。
func TestStalledDaysIsSafe(t *testing.T) {
	if d := StalledDays(time.Time{}, fixedNow); d != 0 {
		t.Fatalf("零值阶段时刻应算 0 天（宁可不上停滞榜，也不把缺字段当成卡了 2 万天），实得 %v", d)
	}
	if d := StalledDays(fixedNow.Add(48*time.Hour), fixedNow); d != 0 {
		t.Fatalf("时钟回拨/未来时刻应归零，实得 %v", d)
	}
	if d := StalledDays(fixedNow.Add(-72*time.Hour), fixedNow); d < 2.99 || d > 3.01 {
		t.Fatalf("3 天应算约 3.0，实得 %v", d)
	}
}

// 窗口与分页钳制：days/page_size 越界一律钳进合法区间，不由 query 参数决定响应体大小。
func TestClamps(t *testing.T) {
	if ClampDays(0) != DefaultWindowDays || ClampDays(-5) != DefaultWindowDays {
		t.Fatal("非法 days 应回默认窗口")
	}
	if ClampDays(MaxWindowDays+999) != MaxWindowDays {
		t.Fatal("days 必须被钳到硬上限")
	}
	if ClampPageSize(0) != 20 || ClampPageSize(9999) != MaxPageSize {
		t.Fatal("page_size 必须被钳到硬上限，不该让 query 参数决定响应体大小")
	}
	// 只有"没传"(0) 回默认；传了负数是有人明确要求"立刻算停滞"，钳到下限而不是偷偷回 7 天
	if ClampStuckDays(0) != DefaultStuckDays {
		t.Fatal("未传停滞阈值应回默认")
	}
	if ClampStuckDays(9999) != MaxStuckDays || ClampStuckDays(-9) != MinStuckDays {
		t.Fatal("停滞阈值必须落在区间内（下限可达）")
	}
}

// 阶段码白名单：只有枚举内的值算合法，未知值不得放行（防前端自造阶段绕过状态机）。
func TestIsValidStageCoversOnlyKnownCodes(t *testing.T) {
	for _, s := range append(append([]string{}, model.DealStages...), model.DealStageWon, model.DealStageLost) {
		if !IsValidStage(s) {
			t.Fatalf("已定义阶段 %s 被判非法（枚举与校验迟早对不上）", s)
		}
	}
	for _, bad := range []string{"", "WON", "deal_won", "  "} {
		if IsValidStage(bad) {
			t.Fatalf("非法阶段 %q 被判合法", bad)
		}
	}
}
