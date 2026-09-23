// 商机与报价端点测试（商机批 · 批次2，2026-09-23）。
//
// 与 internal/deal 包内测试的分工：包内测裁决与取数（那里"看板与名单同源"是同一次函数调用，
// 看不出接线问题），这里测**端点承诺**——
// 空态是不是 [] 而不是 null、拒绝有没有带稳定 reason 码、跨租户 ID 是不是 404、
// 并发守卫是不是回 409 而不是 200，以及最要紧的一条：
// 从 HTTP 读到的格子数字与从 HTTP 读到的名单 total 必须相等（两侧都走端点才算真验过）。
//
// 依赖本地 PostgreSQL（testutil.SetupTestDB，DB 不可用自动 Skip）；不真实外呼 AI。
package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"ai-scrm/internal/db"
	"ai-scrm/internal/deal"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// newDealAdminRouter 组装管理端最小路由。生产另有 JWTAuth + AdminRequired + OrgResolve 整组闸
// （权限矩阵在 smoke_perm.sh 覆盖），本处只设租户语境，专注测端点自身的契约。
func newDealAdminRouter(tid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	stub := func(c *gin.Context) { c.Set("tenant_id", tid) }
	r.GET("/api/v1/admin/deals/board", stub, DealBoard)
	r.GET("/api/v1/admin/deals", stub, DrillDeals)
	r.POST("/api/v1/admin/deals", stub, CreateDeal)
	r.GET("/api/v1/admin/deals/:id", stub, GetDealDetail)
	r.PUT("/api/v1/admin/deals/:id", stub, EditDeal)
	r.POST("/api/v1/admin/deals/:id/move", stub, MoveDeal)
	r.GET("/api/v1/admin/deals/:id/quotes", stub, ListDealQuotes)
	r.POST("/api/v1/admin/deals/:id/quotes", stub, CreateQuote)
	r.GET("/api/v1/admin/quotes/:id", stub, GetQuoteDetail)
	r.PUT("/api/v1/admin/quotes/:id", stub, EditQuoteDraft)
	r.POST("/api/v1/admin/quotes/:id/send", stub, SendQuoteDraft)
	r.POST("/api/v1/admin/quotes/:id/accept", stub, AcceptQuote)
	r.POST("/api/v1/admin/quotes/:id/decline", stub, DeclineQuote)
	r.POST("/api/v1/admin/quotes/:id/void", stub, VoidQuote)
	return r
}

// newDealAdvisorRouter 顾问端最小路由：租户 + 角色 + 用户三件语境（DataScope 判据全在这里）。
//
// 不挂中间件不等于不测隔离——隔离的判据是"传进 db.DataScope 的 role/user_id 是什么"，
// 这里把它按真值摆出来，跨范围那条用例就会真的看不见。
func newDealAdvisorRouter(tid uint, role string, uid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	stub := func(c *gin.Context) {
		c.Set("tenant_id", tid)
		c.Set("role", role)
		c.Set("user_id", uid)
	}
	r.GET("/api/v1/advisor/customer/:id/deals", stub, AdvisorCustomerDeals)
	r.POST("/api/v1/advisor/customer/:id/deals", stub, AdvisorCreateDeal)
	r.POST("/api/v1/advisor/deals/:id/move", stub, AdvisorMoveDeal)
	r.POST("/api/v1/advisor/deals/:id/quotes", stub, AdvisorCreateQuote)
	return r
}

// dealTenant 建单测租户并返回 ID（语义码必须互不相同：testutil 同码复用同一租户，
// 跨租家用例若撞码就退化成同一家的 A/A，主闸会在自己身上假绿）
func dealTenant(t *testing.T, semantic string) uint {
	t.Helper()
	testutil.SetupTestDB(t)
	return testutil.CreateTenantCode(t, semantic)
}

// dealDo 发一次请求，返回状态码、原始 body 与解析后的 data 段
func dealDo(t *testing.T, r *gin.Engine, method, path, body string) (int, map[string]any, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	raw := rec.Body.String()
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return rec.Code, nil, raw
	}
	if env.Data == nil {
		env.Data = map[string]any{}
	}
	return rec.Code, env.Data, raw
}

// dealMust 断言 200 并回 data 段（失败时把 body 打出来——端点测试最难查的就是"200 判定通过但字段是空的"）
func dealMust(t *testing.T, r *gin.Engine, method, path, body string) map[string]any {
	t.Helper()
	code, data, raw := dealDo(t, r, method, path, body)
	if code != http.StatusOK {
		t.Fatalf("%s %s 应 200，实得 %d body=%s", method, path, code, raw)
	}
	return data
}

// urlQueryEscape 只处理本文件用到的那一个字符：过滤码里的冒号
// （net/url 整串转义会把 "stage:lost" 变成不可读的长串，测试失败信息里就查不动了）
func urlQueryEscape(s string) string { return strings.ReplaceAll(s, ":", "%3A") }

// dealSeedCustomer 建一个客户（可选挂归属顾问），返回 ID
func dealSeedCustomer(t *testing.T, tid uint, name string, owner uint) uint {
	t.Helper()
	c := model.Customer{TenantID: tid, Name: name, Phone: "1380000" + name[len(name)-4:], JourneyStage: model.JourneyLeadCaptured}
	c.AssignedUserID = owner
	if err := db.DB.Create(&c).Error; err != nil {
		t.Fatalf("建客户失败: %v", err)
	}
	return c.ID
}

// dealSeedSalesUser 建一个租户内销售账号（DataScope 的 role=user 分支按 assigned_user_id 裁剪）
func dealSeedSalesUser(t *testing.T, tid uint, tag string) uint {
	t.Helper()
	u := model.User{
		Username: fmt.Sprintf("deal_%s_%d", tag, tid),
		// 密码哈希给个占位即可：本文件不经登录链路，密码列只是 NOT NULL 约束要它
		PasswordHash: "!not-used-in-tests",
		RealName:     "顾问" + tag,
		Role:         model.RoleUser,
		Status:       1,
		TenantID:     &tid,
	}
	if err := db.DB.Create(&u).Error; err != nil {
		t.Fatalf("建销售账号失败: %v", err)
	}
	return u.ID
}

// dealCreateViaAPI 经端点建一张单并回其 deal 段
func dealCreateViaAPI(t *testing.T, r *gin.Engine, tid, cid uint, title, stage string, amountCents int64) map[string]any {
	t.Helper()
	_ = tid
	body := fmt.Sprintf(`{"customer_id":%d,"title":%q,"stage":%q,"amount_cents":%d}`, cid, title, stage, amountCents)
	data := dealMust(t, r, http.MethodPost, "/api/v1/admin/deals", body)
	d, ok := data["deal"].(map[string]any)
	if !ok {
		t.Fatalf("建单响应缺 deal 段: %v", data)
	}
	return d
}

// TestDealAdminBoardShapeZeroCellsPresent 空租户看板：六个阶段格子必须在、数字全 0、
// 赢单率 0、config 枚举齐备。**缺格子与格子是 0 在管理会上是两种说法**——
// 前者会被理解成"这块还没做"，所以零命中也必须出现。
func TestDealAdminBoardShapeZeroCellsPresent(t *testing.T) {
	tid := dealTenant(t, "deal_adm_a")
	defer testutil.CleanupTenant(t, tid)
	r := newDealAdminRouter(tid)
	data := dealMust(t, r, http.MethodGet, "/api/v1/admin/deals/board?days=30", "")

	cells, ok := data["stages"].([]any)
	if !ok {
		t.Fatalf("stages 不是数组: %T", data["stages"])
	}
	if len(cells) != len(model.DealStages)+2 {
		t.Fatalf("阶段格子应 %d 个（四过程+两终局），实得 %d", len(model.DealStages)+2, len(cells))
	}
	for _, raw := range cells {
		cell := raw.(map[string]any)
		if cell["count"].(float64) != 0 {
			t.Fatalf("空租户格子应为 0：%v", cell)
		}
		if f, _ := cell["drill_filter"].(string); f == "" {
			t.Fatalf("格子必须自带下钻过滤码（前端不该自己拼字符串）：%v", cell)
		}
		if n, _ := cell["stage_name"].(string); n == "" {
			t.Fatalf("格子必须自带中文名：%v", cell)
		}
	}
	if note, _ := data["note"].(string); note == "" {
		t.Fatalf("看板口径说明必须随响应下发，不能藏在代码注释里")
	}
	cfg, ok := data["config"].(map[string]any)
	if !ok {
		t.Fatalf("config 段缺失")
	}
	for _, k := range []string{"stage_names", "filter_labels", "lost_reasons", "quote_statuses", "filters"} {
		if _, has := cfg[k]; !has {
			t.Fatalf("config 缺键 %s（前端会渲染出 undefined）", k)
		}
	}
	if tr, _ := data["truncated"].(bool); tr {
		t.Fatalf("空数据不该标 truncated")
	}
}

// TestDealAdminCreateRejectsReasonCodes 建单/推进/报价的入参拒绝一律带稳定 reason 码：
// 前端与冒烟按 reason 分支，中文提示可改、码不可改。
func TestDealAdminCreateRejectsReasonCodes(t *testing.T) {
	tid := dealTenant(t, "deal_adm_b")
	defer testutil.CleanupTenant(t, tid)
	r := newDealAdminRouter(tid)
	cid := dealSeedCustomer(t, tid, "陈老板0001", 0)

	cases := []struct {
		name, body, wantReason string
	}{
		{"缺客户", `{"customer_id":0,"title":"XT5 意向"}`, deal.ReasonCustomerNotFound},
		{"客户不存在", dealJSON2(cid+99999, "不存在"), deal.ReasonCustomerNotFound},
		{"空标题", `{"customer_id":` + fmt.Sprint(cid) + `,"title":"  "}`, deal.ReasonTitleRequired},
		{"负金额", `{"customer_id":` + fmt.Sprint(cid) + `,"title":"A","amount_cents":-1}`, deal.ReasonAmountNegative},
		{"非法阶段", `{"customer_id":` + fmt.Sprint(cid) + `,"title":"A","stage":"exploded"}`, deal.ReasonStageUnknown},
		{"建单即终局", `{"customer_id":` + fmt.Sprint(cid) + `,"title":"A","stage":"won"}`, deal.ReasonStageUnknown},
	}
	for _, tc := range cases {
		code, _, raw := dealDo(t, r, http.MethodPost, "/api/v1/admin/deals", tc.body)
		if code != http.StatusBadRequest {
			t.Fatalf("%s：应 400，实得 %d body=%s", tc.name, code, raw)
		}
		if reason := jsonStr(t, raw, "reason"); reason != tc.wantReason {
			t.Fatalf("%s：reason 应为 %q，实得 %q（body=%s）", tc.name, tc.wantReason, reason, raw)
		}
	}
	// 日期形态非法必须在查库之前拒：静默当成"没传"就会把用户写的日期丢掉
	code, _, raw := dealDo(t, r, http.MethodPost, "/api/v1/admin/deals",
		`{"customer_id":`+fmt.Sprint(cid)+`,"title":"A","expected_close_at":"2026/13/45"}`)
	if code != http.StatusBadRequest || !bytes.Contains([]byte(raw), []byte("expected_close_at")) {
		t.Fatalf("非法日期应 400 并点名该字段，实得 %d body=%s", code, raw)
	}
}

// dealJSON2 手工拼一条"客户不存在"的建单请求体（避开格式化串里塞引号的可读性灾难）
func dealJSON2(cid uint, title string) string {
	return fmt.Sprintf(`{"customer_id":%d,"title":%q}`, cid, title)
}

// jsonStr 从 JSON 响应里取一个字符串字段
func jsonStr(t *testing.T, raw, key string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("解析响应失败: %v body=%s", err, raw)
	}
	s, _ := m[key].(string)
	return s
}

// TestDealAdminOneOpenPerCustomer 同一客户第二张在途单必须 400 + deal_already_open，
// 而且**不是静默复用**：点"新建"的人拿到一张别人正在谈的旧单，就会在上面改金额。
func TestDealAdminOneOpenPerCustomer(t *testing.T) {
	tid := dealTenant(t, "deal_adm_c")
	defer testutil.CleanupTenant(t, tid)
	r := newDealAdminRouter(tid)
	cid := dealSeedCustomer(t, tid, "李经理0002", 0)

	first := dealCreateViaAPI(t, r, tid, cid, "首单", model.DealStageQualified, 28000000)
	if first["stage"] != model.DealStageQualified {
		t.Fatalf("建单默认阶段应回显：实得 %v", first["stage"])
	}
	code, _, raw := dealDo(t, r, http.MethodPost, "/api/v1/admin/deals",
		fmt.Sprintf(`{"customer_id":%d,"title":"第二单"}`, cid))
	if code != http.StatusBadRequest || jsonStr(t, raw, "reason") != deal.ReasonDealAlreadyOpen {
		t.Fatalf("重复在途单应 400 deal_already_open，实得 %d body=%s", code, raw)
	}
	// 空态名单必须是 [] 不是 null（前端直接 map，null 会整页炸）
	data := dealMust(t, r, http.MethodGet, "/api/v1/admin/deals?filter=lost", "")
	if list, ok := data["list"].([]any); !ok || len(list) != 0 {
		t.Fatalf("空名单应是 []，实得 %#v", data["list"])
	}
	// 缺 filter 不默认回某一份名单：默认视图与"我点了哪一格"混在一起就没法核对
	if code, _, _ := dealDo(t, r, http.MethodGet, "/api/v1/admin/deals", ""); code != http.StatusBadRequest {
		t.Fatalf("缺 filter 应 400，实得 %d", code)
	}
	if code, _, _ := dealDo(t, r, http.MethodGet, "/api/v1/admin/deals?filter=whatever", ""); code != http.StatusBadRequest {
		t.Fatalf("未知 filter 应 400，实得 %d", code)
	}
}

// TestDealAdminBoardCellsEqualDrillTotalsOverHTTP 从 HTTP 两侧读，格子数字 == 名单 total。
//
// 包内测试证明的是"同一次调用两侧一致"，那件事本来就不可能不一致；
// 这里证明的是**接线**：看板端点与名单端点真的走了同一套判据、同一份窗口参数。
// 前置自检（先证明数据真在库里）不可省——合成数据没落库时整段等式会在 0==0 上假绿。
func TestDealAdminBoardCellsEqualDrillTotalsOverHTTP(t *testing.T) {
	tid := dealTenant(t, "deal_adm_d")
	defer testutil.CleanupTenant(t, tid)
	r := newDealAdminRouter(tid)

	mk := func(name, stage string, amount int64) (uint, uint) {
		cid := dealSeedCustomer(t, tid, name, 0)
		d := dealCreateViaAPI(t, r, tid, cid, "单-"+name, stage, amount)
		return uint(d["id"].(float64)), cid
	}
	_, _ = mk("甲0001", model.DealStageLead, 0)
	dQuoted, _ := mk("乙0002", model.DealStageQuoted, 15000000)
	dNego, _ := mk("丙0003", model.DealStageNegotiating, 26000000)
	dWon, _ := mk("丁0004", model.DealStageQualified, 30000000)
	dLost, _ := mk("戊0005", model.DealStageQualified, 0)
	// 两张 qualified 分别推到成交与流失（终局单不再占用"一客一在途"的坑）
	code, _, raw := dealDo(t, r, http.MethodPost, fmt.Sprintf("/api/v1/admin/deals/%d/move", dWon),
		`{"to":"won","amount_cents":30000000,"change_amount":true}`)
	if code != http.StatusOK {
		t.Fatalf("推进成交应 200，实得 %d body=%s", code, raw)
	}
	if code, _, raw = dealDo(t, r, http.MethodPost, fmt.Sprintf("/api/v1/admin/deals/%d/move", dLost),
		`{"to":"lost","lost_reason":"price"}`); code != http.StatusOK {
		t.Fatalf("推进流失应 200，实得 %d body=%s", code, raw)
	}

	// 前置自检：五张单确实在库里（缺它整段等式在 0==0 上假绿）
	board := dealMust(t, r, http.MethodGet, "/api/v1/admin/deals/board?days=90", "")
	if got := board["total_count"].(float64); got != 5 {
		t.Fatalf("前置条件被破坏：看板应见 5 张单，实得 %v", got)
	}
	cells := map[string]float64{}
	for _, raw := range board["stages"].([]any) {
		c := raw.(map[string]any)
		cells[c["drill_filter"].(string)] = c["count"].(float64)
	}
	// 三张在途、一张成交、一张流失——若阶段推进没落库，这组期望值第一个就会红
	if cells["stage:quoted"] != 1 || cells["stage:negotiating"] != 1 || cells["stage:lead"] != 1 {
		t.Fatalf("阶段格子分布不符预期：%v", cells)
	}
	if cells["stage:won"] != 1 || cells["stage:lost"] != 1 {
		t.Fatalf("终局格子不符预期：%v", cells)
	}

	check := func(filter string) {
		t.Helper()
		data := dealMust(t, r, http.MethodGet, "/api/v1/admin/deals?filter="+filter+"&days=90&page_size=100", "")
		total := data["total"].(float64)
		list := data["list"].([]any)
		want, has := cells[filter]
		if !has {
			// 分组码（open/stuck/won/lost）不在阶段格子里，另行对看板汇总位
			switch filter {
			case deal.FilterOpen:
				want = board["open_count"].(float64)
			case deal.FilterStuck:
				want = board["stuck_count"].(float64)
			case deal.FilterWon:
				want = board["won_count"].(float64)
			case deal.FilterLost:
				want = board["lost_count"].(float64)
			default:
				t.Fatalf("未知过滤码 %q", filter)
			}
		}
		if total != want || float64(len(list)) != want {
			t.Fatalf("过滤 %s：看板 %v / 名单 total %v / 名单行数 %v 三者必须相等", filter, want, total, len(list))
		}
	}
	for _, f := range append(append([]string{}, deal.FilterCodes...), deal.StageFilterCodes()...) {
		check(f)
	}
	// 格子不只对数字：点进去必须**就是那一张**（数字相等但名单是别人，同样是缺陷）
	cellOf := func(filter string) []any {
		t.Helper()
		list := dealMust(t, r, http.MethodGet, "/api/v1/admin/deals?filter="+urlQueryEscape(filter), "")["list"].([]any)
		if len(list) != 1 {
			t.Fatalf("格子 %s 应恰好 1 行，实得 %d", filter, len(list))
		}
		return list
	}
	row := cellOf("stage:negotiating")[0].(map[string]any)
	if got := uint(row["id"].(float64)); got != dNego {
		t.Fatalf("谈判格应列出 %d，实得 %d（同源判据被改坏了）", dNego, got)
	}
	if _, has := row["open_quote"]; !has {
		t.Fatalf("名单行必须带 open_quote 键（无报价时是 null，不是缺键）")
	}
	// 成交格同理：列的必须是那张真被推进成交的单，而不是"数字对上了但对象是别人"
	if got := uint(cellOf("stage:won")[0].(map[string]any)["id"].(float64)); got != dWon {
		t.Fatalf("成交格应列出 %d，实得 %d", dWon, got)
	}
	if got := uint(cellOf("stage:quoted")[0].(map[string]any)["id"].(float64)); got != dQuoted {
		t.Fatalf("已报价格应列出 %d，实得 %d", dQuoted, got)
	}
}

// TestDealAdminMoveValidationAndTerminalLock 推进校验：won 无金额拒、lost 无原因拒、
// 回退拒、终局后再动一律 deal_closed；**0 行受影响的并发场景回 409 不回 200**。
func TestDealAdminMoveValidationAndTerminalLock(t *testing.T) {
	tid := dealTenant(t, "deal_adm_e")
	defer testutil.CleanupTenant(t, tid)
	r := newDealAdminRouter(tid)
	cid := dealSeedCustomer(t, tid, "王主管0003", 0)
	d := dealCreateViaAPI(t, r, tid, cid, "报价意向", model.DealStageQualified, 0)
	id := uint(d["id"].(float64))

	if code, _, raw := dealDo(t, r, http.MethodPost, fmt.Sprintf("/api/v1/admin/deals/%d/move", id),
		`{"to":"won","amount_cents":0}`); code != http.StatusBadRequest ||
		jsonStr(t, raw, "reason") != deal.ReasonWonAmountRequired {
		t.Fatalf("无金额成交应 400 won_amount_required，实得 %d body=%s", code, raw)
	}
	if code, _, raw := dealDo(t, r, http.MethodPost, fmt.Sprintf("/api/v1/admin/deals/%d/move", id),
		`{"to":"lost"}`); code != http.StatusBadRequest ||
		jsonStr(t, raw, "reason") != deal.ReasonLostReasonRequired {
		t.Fatalf("无原因流失应 400 lost_reason_required，实得 %d body=%s", code, raw)
	}
	if code, _, raw := dealDo(t, r, http.MethodPost, fmt.Sprintf("/api/v1/admin/deals/%d/move", id),
		`{"to":"lead"}`); code != http.StatusBadRequest ||
		jsonStr(t, raw, "reason") != deal.ReasonStageBackward {
		t.Fatalf("阶段回退应 400 stage_backward，实得 %d body=%s", code, raw)
	}
	// 正常推进到已报价
	if code, _, raw := dealDo(t, r, http.MethodPost, fmt.Sprintf("/api/v1/admin/deals/%d/move", id),
		`{"to":"quoted"}`); code != http.StatusOK {
		t.Fatalf("推进已报价应 200，实得 %d body=%s", code, raw)
	}
	// 成交并顺手改金额（won 判定用的是改完之后的金额）
	data := dealMust(t, r, http.MethodPost, fmt.Sprintf("/api/v1/admin/deals/%d/move", id),
		`{"to":"won","amount_cents":32000000,"change_amount":true}`)
	deal2 := data["deal"].(map[string]any)
	if deal2["stage"] != model.DealStageWon || deal2["amount_cents"].(float64) != 32000000 {
		t.Fatalf("成交落库不符：%v", deal2)
	}
	if s, _ := deal2["won_at"].(string); s == "" {
		t.Fatalf("成交时刻必须落库（报表按它算周期），实得空值")
	}
	// 终局锁：编辑与再推进都拒
	if code, _, raw := dealDo(t, r, http.MethodPut, fmt.Sprintf("/api/v1/admin/deals/%d", id),
		`{"title":"改成别的","change_amount":true,"amount_cents":1}`); code != http.StatusBadRequest ||
		jsonStr(t, raw, "reason") != deal.ReasonDealClosed {
		t.Fatalf("终局单编辑应 400 deal_closed，实得 %d body=%s", code, raw)
	}
	if code, _, raw := dealDo(t, r, http.MethodPost, fmt.Sprintf("/api/v1/admin/deals/%d/move", id),
		`{"to":"lost","lost_reason":"price"}`); code != http.StatusBadRequest ||
		jsonStr(t, raw, "reason") != deal.ReasonDealClosed {
		t.Fatalf("终局单再推进应 400 deal_closed，实得 %d body=%s", code, raw)
	}
	// 拿一个不存在的单子推进 → 404，不回显"存在但不可见"
	if code, _, _ := dealDo(t, r, http.MethodPost, "/api/v1/admin/deals/99999999/move",
		`{"to":"quoted"}`); code != http.StatusNotFound {
		t.Fatalf("不存在的单子应 404，实得 %d", code)
	}
}

// TestDealWriteErrMapsDomainErrorsToHTTP 领域错误 → HTTP 的映射表（端点侧唯一新增的判断）。
//
// 并发冲突（条件守卫 0 行受影响）必须在**这里**被判成 409：包内测的是"MoveDeal 报冲突"，
// 而"报冲突"落到响应上是 409 还是 200，只有这一层说了算——写错的就是
// "两个人同时点成交、后一个看到成功但单子没动"这个最难查的事故形态。
func TestDealWriteErrMapsDomainErrorsToHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name   string
		err    error
		want   int
		reason string
	}{
		{"冲突报 409", fmt.Errorf("deal: 商机状态%w", deal.ErrConflict), http.StatusConflict, ""},
		{"不存在报 404", deal.ErrNotFound, http.StatusNotFound, ""},
		{"坏过滤码报 400", deal.ErrBadFilter, http.StatusBadRequest, ""},
		{"拒绝带 reason", &deal.ErrReject{Reason: deal.ReasonDealClosed, Msg: "已终局"}, http.StatusBadRequest, deal.ReasonDealClosed},
		{"其他错误不得泄露内部信息", fmt.Errorf("boom: 连接串 user=postgres"), http.StatusInternalServerError, ""},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		// RespErrInternal 会用 request context 落日志（不带 Request 直接 nil 指针）
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/deals/1/move", nil)
		dealWriteErr(c, tc.err, "操作失败")
		if rec.Code != tc.want {
			t.Fatalf("%s：应 %d，实得 %d body=%s", tc.name, tc.want, rec.Code, rec.Body.String())
		}
		if tc.reason != "" {
			if got := jsonStr(t, rec.Body.String(), "reason"); got != tc.reason {
				t.Fatalf("%s：reason 应为 %q，实得 %q", tc.name, tc.reason, got)
			}
		}
		if tc.want == http.StatusInternalServerError &&
			bytes.Contains(rec.Body.Bytes(), []byte("postgres")) {
			t.Fatalf("内部错误不得把原始错误写进响应（泄露连接信息）：%s", rec.Body.String())
		}
	}
}

// TestDealQuoteVersionChainOverHTTP 报价版本链与状态锁（走端点）：
// 建版→发出→内容锁定（改拒 quote_locked）→再建版把旧版标 superseded→接受不回写商机阶段。
func TestDealQuoteVersionChainOverHTTP(t *testing.T) {
	tid := dealTenant(t, "deal_adm_f")
	defer testutil.CleanupTenant(t, tid)
	r := newDealAdminRouter(tid)
	cid := dealSeedCustomer(t, tid, "赵总0004", 0)
	d := dealCreateViaAPI(t, r, tid, cid, "整车报价", model.DealStageQuoted, 0)
	dealID := uint(d["id"].(float64))

	lines := `[{"name":"XT5 豪华版","qty":1,"unit_cents":26980000},{"name":"延保套餐","qty":1,"unit_cents":6800000}]`
	// 明细为空必须拒（合计为 0 的报价单发出去就是事故）
	if code, _, raw := dealDo(t, r, http.MethodPost, fmt.Sprintf("/api/v1/admin/deals/%d/quotes", dealID),
		`{"lines":[]}`); code != http.StatusBadRequest ||
		jsonStr(t, raw, "reason") != deal.ReasonQuoteLinesRequired {
		t.Fatalf("空明细应 400，实得 %d body=%s", code, raw)
	}
	q1 := dealMust(t, r, http.MethodPost, fmt.Sprintf("/api/v1/admin/deals/%d/quotes", dealID),
		`{"lines":`+lines+`,"note":"含上牌服务"}`)["quote"].(map[string]any)
	if v := q1["version"].(float64); v != 1 {
		t.Fatalf("首版应为 v1，实得 %v", v)
	}
	if tot := q1["total_cents"].(float64); tot != 33780000 {
		t.Fatalf("合计应由服务端重算=33780000，实得 %v", tot)
	}
	q1ID := uint(q1["id"].(float64))
	// 草稿可改
	if _, data, _ := dealDo(t, r, http.MethodPut, fmt.Sprintf("/api/v1/admin/quotes/%d", q1ID),
		`{"lines":[{"name":"XT5 豪华版","qty":2,"unit_cents":26980000}],"note":"改数量"}`); data["quote"] == nil {
		t.Fatalf("草稿编辑未回报价段")
	}
	// 发出
	SENT := dealMust(t, r, http.MethodPost, fmt.Sprintf("/api/v1/admin/quotes/%d/send", q1ID), "")
	if st := SENT["quote"].(map[string]any)["status"]; st != model.QuoteStatusSent {
		t.Fatalf("发出后状态应 sent，实得 %v", st)
	}
	// 发出后锁定
	if code, _, raw := dealDo(t, r, http.MethodPut, fmt.Sprintf("/api/v1/admin/quotes/%d", q1ID),
		`{"lines":`+lines+`}`); code != http.StatusBadRequest ||
		jsonStr(t, raw, "reason") != deal.ReasonQuoteLocked {
		t.Fatalf("已发出报价改内容应 400 quote_locked，实得 %d body=%s", code, raw)
	}
	// 再建一版：v1 自动被取代，version=2
	q2 := dealMust(t, r, http.MethodPost, fmt.Sprintf("/api/v1/admin/deals/%d/quotes", dealID),
		`{"lines":`+lines+`,"send":true}`)["quote"].(map[string]any)
	if v := q2["version"].(float64); v != 2 {
		t.Fatalf("第二版应为 v2，实得 %v", v)
	}
	if st := q2["status"]; st != model.QuoteStatusSent {
		t.Fatalf("send=true 应经同一个状态机落到 sent，实得 %v", st)
	}
	list := dealMust(t, r, http.MethodGet, fmt.Sprintf("/api/v1/admin/deals/%d/quotes", dealID), "")["list"].([]any)
	if len(list) != 2 {
		t.Fatalf("版本序列应有两行（历史不覆盖），实得 %d", len(list))
	}
	var superseded int
	for _, raw := range list {
		if q := raw.(map[string]any); q["status"] == model.QuoteStatusSuperseded {
			superseded++
		}
	}
	if superseded != 1 {
		t.Fatalf("旧在途版应恰有 1 张被取代，实得 %d", superseded)
	}
	// 接受：报价终局，但**商机阶段不得被顺手改掉**（成交是人的判断）
	q2ID := uint(q2["id"].(float64))
	acc := dealMust(t, r, http.MethodPost, fmt.Sprintf("/api/v1/admin/quotes/%d/accept", q2ID), "")
	if acc["quote"].(map[string]any)["status"] != model.QuoteStatusAccepted {
		t.Fatalf("接受后应 accepted，实得 %v", acc["quote"])
	}
	back := dealMust(t, r, http.MethodGet, fmt.Sprintf("/api/v1/admin/deals/%d", dealID), "")["deal"].(map[string]any)
	if back["stage"] != model.DealStageQuoted {
		t.Fatalf("报价接受不得自动推进商机阶段，实得 %v", back["stage"])
	}
	// 已答复的报价再动作一律拒
	if code, _, raw := dealDo(t, r, http.MethodPost, fmt.Sprintf("/api/v1/admin/quotes/%d/void", q2ID), ""); code != http.StatusBadRequest ||
		jsonStr(t, raw, "reason") != deal.ReasonQuoteFinal {
		t.Fatalf("终局报价作废应 400 quote_final，实得 %d body=%s", code, raw)
	}
	// 详情里明细行齐备（客户手里那一版必须可复现）
	detail := dealMust(t, r, http.MethodGet, fmt.Sprintf("/api/v1/admin/quotes/%d", q2ID), "")["quote"].(map[string]any)
	ls, ok := detail["lines"].([]any)
	if !ok || len(ls) != 2 {
		t.Fatalf("报价明细应回 2 行，实得 %#v", detail["lines"])
	}
	if first := ls[0].(map[string]any); first["total_cents"].(float64) != 26980000 {
		t.Fatalf("行小计应由后端回填，实得 %v", first["total_cents"])
	}
}

// TestDealAdminCrossTenantInvisible 换一家租户读同一批 ID：一律 404 / 空集，不回显差别。
// （否则端点本身就是一台"别人家有没有这张单"的探针。）
func TestDealAdminCrossTenantInvisible(t *testing.T) {
	tidA := dealTenant(t, "deal_adm_g")
	defer testutil.CleanupTenant(t, tidA)
	tidB := dealTenant(t, "deal_adm_h")
	defer testutil.CleanupTenant(t, tidB)
	rA := newDealAdminRouter(tidA)
	rB := newDealAdminRouter(tidB)

	cidA := dealSeedCustomer(t, tidA, "甲家客户0005", 0)
	d := dealCreateViaAPI(t, rA, tidA, cidA, "甲家单", model.DealStageQualified, 100)
	dealID := uint(d["id"].(float64))
	q := dealMust(t, rA, http.MethodPost, fmt.Sprintf("/api/v1/admin/deals/%d/quotes", dealID),
		`{"lines":[{"name":"A","qty":1,"unit_cents":100}]}`)["quote"].(map[string]any)
	quoteID := uint(q["id"].(float64))

	if code, _, _ := dealDo(t, rB, http.MethodGet, fmt.Sprintf("/api/v1/admin/deals/%d", dealID), ""); code != http.StatusNotFound {
		t.Fatalf("跨租户读单子应 404，实得 %d", code)
	}
	if code, _, _ := dealDo(t, rB, http.MethodGet, fmt.Sprintf("/api/v1/admin/quotes/%d", quoteID), ""); code != http.StatusNotFound {
		t.Fatalf("跨租户读报价应 404，实得 %d", code)
	}
	if code, _, _ := dealDo(t, rB, http.MethodPost, fmt.Sprintf("/api/v1/admin/deals/%d/move", dealID),
		`{"to":"quoted"}`); code != http.StatusNotFound {
		t.Fatalf("跨租户推进应 404，实得 %d", code)
	}
	// 别人家的客户也不能被自己开单（建单里那条"客户必须在本租户下"的判断）
	if code, _, raw := dealDo(t, rB, http.MethodPost, "/api/v1/admin/deals",
		fmt.Sprintf(`{"customer_id":%d,"title":"串家单"}`, cidA)); code != http.StatusBadRequest ||
		jsonStr(t, raw, "reason") != deal.ReasonCustomerNotFound {
		t.Fatalf("跨租户建单应 400 customer_not_found，实得 %d body=%s", code, raw)
	}
	// B 家看板必须是空的（A 家的单一张都不该进来）
	board := dealMust(t, rB, http.MethodGet, "/api/v1/admin/deals/board", "")
	if board["total_count"].(float64) != 0 {
		t.Fatalf("B 家看板应 0 张，实得 %v", board["total_count"])
	}
}

// TestDealAdvisorScopeOnlyOwnCustomers 顾问端数据范围：自己的客户看得见、别人的看不见。
//
// 这一条是商机批新增面里唯一**扩大可读集合**的地方（opportunities 没有归属列，
// 裁剪只能落在客户上），所以必须正面断一次：
// 换个人登录就看不到，才是"单子跟着客户走"的正确实现。
func TestDealAdvisorScopeOnlyOwnCustomers(t *testing.T) {
	tid := dealTenant(t, "deal_adv_a")
	defer testutil.CleanupTenant(t, tid)
	salesA := dealSeedSalesUser(t, tid, "a")
	salesB := dealSeedSalesUser(t, tid, "b")

	rAdmin := newDealAdminRouter(tid)
	cidA := dealSeedCustomer(t, tid, "甲客户0006", salesA)
	cidB := dealSeedCustomer(t, tid, "乙客户0007", salesB)
	dealCreateViaAPI(t, rAdmin, tid, cidA, "甲的单", model.DealStageQuoted, 100)
	dealCreateViaAPI(t, rAdmin, tid, cidB, "乙的单", model.DealStageQuoted, 200)

	rA := newDealAdvisorRouter(tid, model.RoleUser, salesA)
	rB := newDealAdvisorRouter(tid, model.RoleUser, salesB)

	data := dealMust(t, rA, http.MethodGet, fmt.Sprintf("/api/v1/advisor/customer/%d/deals", cidA), "")
	if list := data["list"].([]any); len(list) != 1 {
		t.Fatalf("甲应看到自己客户的 1 张单，实得 %d", len(list))
	}
	// 乙的客户在甲的范围内根本不存在：回**空列表**而不是 403
	// （列表接口已经裁过，这里给空集既不外泄"这个 ID 存在"，也不把前端页面炸在错误分支上）
	data = dealMust(t, rA, http.MethodGet, fmt.Sprintf("/api/v1/advisor/customer/%d/deals", cidB), "")
	if list, ok := data["list"].([]any); !ok || len(list) != 0 {
		t.Fatalf("甲看乙的客户应回空列表，实得 %#v", data["list"])
	}
	// 反向：乙看得见自己的、看不见甲的
	if got := len(dealMust(t, rB, http.MethodGet, fmt.Sprintf("/api/v1/advisor/customer/%d/deals", cidB), "")["list"].([]any)); got != 1 {
		t.Fatalf("乙应看到自己客户的 1 张单，实得 %d", got)
	}
	// 写侧同样受范围闸：甲推不动乙名下的单（经 deal.GetDeal 复用同一套裁剪判据）
	var bDeal model.Opportunity
	if err := db.DB.Where("tenant_id = ? AND customer_id = ?", tid, cidB).First(&bDeal).Error; err != nil {
		t.Fatalf("取乙的单子失败: %v", err)
	}
	if code, _, _ := dealDo(t, rA, http.MethodPost, fmt.Sprintf("/api/v1/advisor/deals/%d/move", bDeal.ID),
		`{"to":"negotiating"}`); code != http.StatusNotFound {
		t.Fatalf("甲推进乙的单子应 404，实得 %d", code)
	}
	// 甲给自己的客户开单：来源由后端按活码判定（无码=manual），不给前端传 source 的口子
	if code, _, _ := dealDo(t, rA, http.MethodPost, fmt.Sprintf("/api/v1/advisor/customer/%d/deals", cidA),
		`{"title":"第二张","source":"ai"}`); code != http.StatusBadRequest {
		t.Fatalf("甲重复开单应 400 already_open，实得 %d", code)
	}
	// 管理员视角（role=tenant_admin）不加范围条件：两个客户都可见
	rAdminScope := newDealAdvisorRouter(tid, model.RoleTenantAdmin, salesA)
	if got := len(dealMust(t, rAdminScope, http.MethodGet, fmt.Sprintf("/api/v1/advisor/customer/%d/deals", cidB), "")["list"].([]any)); got != 1 {
		t.Fatalf("管理员应看到乙客户的单，实得 %d 行", got)
	}
}
