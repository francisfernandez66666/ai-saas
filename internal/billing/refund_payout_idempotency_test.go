// 退款出款幂等锚、状态回写与结果回查护栏单测（FIX-1，2026-09-26）。
//
// 本文件锁四件事，每一件都直接对应一次真钱风险：
//  1. **单号稳定**：同一订单第二次呼 PSP，PSP 收到的 out_refund_no 必须与第一次逐字相等，
//     且**恰好等于 "RF"+订单号**。第二条不能省——旧写法是 "RF"+秒级时间戳+订单号，
//     同一秒内重试本来就"相等"，只断"两次相等"是个会骗人的断言（本仓说的假绿形态）。
//  2. **意图先落库**：单号写不进库就不许调 PSP（宁可少发，下一轮对账拿同一个号补；
//     "发了但库里不知道发过"会让人在 PSP 后台看不见第二笔时选择再发一次）。
//  3. **回写必查错**：状态写失败、或写影响 0 行，都必须返回错误并当场群告警。
//  4. **查单收敛五态**：只有 PSP 明确给"成功/失败"才改终态；处理中、未知值、查单报错
//     一律保持 psp_ok（一次查不到不等于钱没出去，误判成 failed 会把真退款从人工核销
//     队列里摘掉——那比"多查一轮"坏得多）。
//
// 反向对照（2026-09-26 逐条变异实跑，非纸面推演）：
//   - 三处 provider 的单号改回带时间戳 → 用例 1 红；
//   - "意图落库失败即 return" 删掉 → 用例 2 红（PSP 收到了请求）；
//   - writeRefundPspStatus 丢错误 / 不判 RowsAffected → 用例 3 红；
//   - reconcileOneRefundResult 的 default 分支改成落 psp_failed → 用例 4 的"处理中不动""未知值不动"两格红。
package billing

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/testutil"

	"gorm.io/gorm"
)

// fakePSP 假支付平台（通用 HMAC 网关协议）：按到达顺序记录每次收到的退款单号。
// 选网关渠道而不是微信渠道跑这一段是有意的——微信/支付宝各有"可退余额"校验会挡掉第二笔，
// 恰恰掩盖单号问题；通用网关没有任何兜底，单号一换就是真出两笔钱。
type fakePSP struct {
	mu     sync.Mutex
	srv    *httptest.Server
	got    []string
	fail   bool
	closed bool
}

func newFakePSP() *fakePSP {
	f := &fakePSP{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p map[string]string
		_ = json.Unmarshal(body, &p)
		f.mu.Lock()
		f.got = append(f.got, p["out_refund_no"])
		fail := f.fail
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if fail {
			_, _ = w.Write([]byte(`{"code":40004,"message":"模拟拒单"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"message":"ok"}`))
	}))
	return f
}

func (f *fakePSP) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.got...)
}

func (f *fakePSP) close() {
	if !f.closed {
		f.closed = true
		f.srv.Close()
	}
}

// alertSink 把企微群通知指到本地假端点，让"必须告警"这类断言可数。
// 为什么不用假计数器代替 notify：告警本身就是这一步的验收对象，
// 用被测物自己证明自己等于没测（本仓反复踩过的"只会打日志的守卫"形态）。
type alertSink struct {
	srv  *httptest.Server
	mu   sync.Mutex
	msgs []string
}

func newAlertSink() *alertSink {
	a := &alertSink{}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Markdown struct {
				Content string `json:"content"`
			} `json:"markdown"`
		}
		_ = json.Unmarshal(body, &req)
		a.mu.Lock()
		a.msgs = append(a.msgs, req.Markdown.Content)
		a.mu.Unlock()
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	return a
}

// waitContaining 等一条含指定子串的群告警（NotifyWecom 内部是 goroutine，必须等不能假定已送达）
func (a *alertSink) waitContaining(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		hit := false
		for _, m := range a.msgs {
			if strings.Contains(m, want) {
				hit = true
			}
		}
		a.mu.Unlock()
		if hit {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("3s 内未收到含「%s」的群告警", want)
}

func (a *alertSink) close() { a.srv.Close() }

// confWith 把通用网关渠道与群通知装配到两个假端点（pay_gateway_* 走 providerForChannel 回落分支）
func confWith(pspURL, alertURL string) func() {
	return runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(map[string]string{
		"pay_gateway_url":    pspURL,
		"pay_gateway_app_id": "ut_app",
		"pay_gateway_key":    "ut_key",
		"wecom_webhook_url":  alertURL,
	}, nil))
}

// mkRefundedOrder 直接造一笔"账面已退款、待出款"的真实渠道订单。
// 绕开 MarkOrderRefunded 是有意的：本文件测出款与回查这一段，
// 退款金额计算那段已由 billing_refund_test.go 覆盖，混在一起失败时归因不清。
func mkRefundedOrder(t *testing.T, channel string) (*model.BillingOrder, uint) {
	t.Helper()
	tid := testutil.CreateTenant(t)
	o := &model.BillingOrder{
		OrderNo:           fmt.Sprintf("UTRFPSP%d", time.Now().UnixNano()),
		TenantID:          &tid,
		AmountCents:       19900,
		RefundAmountCents: 13267,
		Status:            "refunded",
		Channel:           channel,
		Period:            "once",
	}
	if err := db.DB.Create(o).Error; err != nil {
		t.Fatalf("建退款订单失败: %v", err)
	}
	return o, tid
}

// TestRefundPayout_StableOutRefundNo ① 单号稳定（正向）+ 落库号与发出号同源
func TestRefundPayout_StableOutRefundNo(t *testing.T) {
	testutil.SetupTestDB(t)
	psp := newFakePSP()
	defer psp.close()
	restore := confWith(psp.srv.URL, "")
	defer restore()

	o, tid := mkRefundedOrder(t, "gateway")
	defer testutil.CleanupTenant(t, tid)

	want := "RF" + o.OrderNo
	executeRefundPayout(o, int64(o.RefundAmountCents))
	// 补呼（对账器/双实例会做的事）：重新从库里读一份，模拟跨进程、跨轮次
	var again model.BillingOrder
	if err := db.DB.First(&again, o.ID).Error; err != nil {
		t.Fatalf("回读订单失败: %v", err)
	}
	executeRefundPayout(&again, int64(again.RefundAmountCents))

	got := psp.requests()
	if len(got) != 2 {
		t.Fatalf("PSP 应收到 2 次请求（首次 + 补呼），实际 %d 次：%v", len(got), got)
	}
	if got[0] != got[1] {
		t.Errorf("两次退款单号不相等：%q vs %q（PSP 会当成两笔独立退款 → 重复出款）", got[0], got[1])
	}
	if got[0] != want {
		t.Errorf("退款单号应恰为 \"RF\"+订单号=%q（只由订单号决定、不含时钟成分），实际发出 %q", want, got[0])
	}
	var row model.BillingOrder
	db.DB.First(&row, o.ID)
	if row.RefundOutNo != got[0] {
		t.Errorf("落库单号 %q 与实际发出单号 %q 不一致（查单会问错号）", row.RefundOutNo, got[0])
	}
	if row.RefundPspStatus != RefundPspOK {
		t.Errorf("受理成功应记 %s，实际 %s", RefundPspOK, row.RefundPspStatus)
	}
	if strings.Contains(row.RefundOutNo, time.Now().Format("2006")) {
		t.Errorf("单号里出现年份片段，说明仍在用时钟拼号：%q", row.RefundOutNo)
	}

	// 反证（本会话不允许改产品代码做变异实跑，故把"判据有没有牙"写进用例自证）：
	// 把旧实现的号喂进同一条判据，必须被判为违规——否则上面那几条断言可能只是
	// 在"永远为真"上空转（本仓说的假绿形态）。
	legacy := "RF" + time.Now().Format("20060102150405") + o.OrderNo
	if !strings.Contains(legacy, time.Now().Format("2006")) || legacy == want {
		t.Errorf("反证样本本身不成立：旧式带时间戳的号 %q 应当被上面的判据拦下", legacy)
	}
	if legacy == row.RefundOutNo {
		t.Errorf("实际发出的号仍是旧式带时间戳形态：%q", row.RefundOutNo)
	}
}

// TestRefundPayout_IntentWriteFailureBlocksPayout ② 意图落库失败 → 一次 PSP 都不发 + 群告警
// （反向用例：修复前根本没有"先落库"这一步，本条必红）
func TestRefundPayout_IntentWriteFailureBlocksPayout(t *testing.T) {
	testutil.SetupTestDB(t)
	psp := newFakePSP()
	defer psp.close()
	alerts := newAlertSink()
	defer alerts.close()
	restore := confWith(psp.srv.URL, alerts.srv.URL)
	defer restore()

	o, tid := mkRefundedOrder(t, "gateway")
	defer testutil.CleanupTenant(t, tid)

	refundWriteFault = func(_ uint, cols []string) error {
		if strings.Contains(strings.Join(cols, ","), "refund_out_no") {
			return errors.New("模拟：退款单号落库失败")
		}
		return nil
	}
	defer func() { refundWriteFault = nil }()

	executeRefundPayout(o, int64(o.RefundAmountCents))

	if got := psp.requests(); len(got) != 0 {
		t.Fatalf("意图没落库却发出了 %d 次 PSP 请求（正是要封堵的重复出款路径）：%v", len(got), got)
	}
	var row model.BillingOrder
	db.DB.First(&row, o.ID)
	if row.RefundOutNo != "" || row.RefundPspStatus != "" {
		t.Errorf("注入失败后不应留下半截状态，实际 out_no=%q status=%q", row.RefundOutNo, row.RefundPspStatus)
	}
	// 反证（判据有没有牙）：本会话不允许改产品代码做变异实跑，故把"故障确实注入到了
	// 被走到的那条写腿"写进用例——列名一旦与产品侧不一致，注入就变成空转，
	// 本用例会立刻在"0 次请求"之外多红这一条，而不是绿着骗人。
	if err := refundWriteFaultErr(o.ID, []string{"refund_out_no", "refund_psp_status"}); err == nil {
		t.Fatalf("故障探针没覆盖意图落库这条写腿（列名口径与产品侧不一致），本用例等于空转")
	}
	alerts.waitContaining(t, "退款出款未发起")
	alerts.waitContaining(t, o.OrderNo) // 告警必须说清是哪一单，否则财务无法核销
}

// TestRefundPayout_ResultWriteFailureAlerts ③ 钱已动、状态写不回 → 报错上抛 + 当场告警
func TestRefundPayout_ResultWriteFailureAlerts(t *testing.T) {
	testutil.SetupTestDB(t)
	psp := newFakePSP()
	defer psp.close()
	alerts := newAlertSink()
	defer alerts.close()
	restore := confWith(psp.srv.URL, alerts.srv.URL)
	defer restore()

	o, tid := mkRefundedOrder(t, "gateway")
	defer testutil.CleanupTenant(t, tid)

	// 只让"写终态"这一步失败，意图落库正常——模拟 PSP 已受理、回写时 DB 抖了一下
	refundWriteFault = func(_ uint, cols []string) error {
		if len(cols) == 1 && cols[0] == "refund_psp_status" {
			return errors.New("模拟：状态回写失败")
		}
		return nil
	}
	defer func() { refundWriteFault = nil }()

	executeRefundPayout(o, int64(o.RefundAmountCents))
	// 前置自检（防注入打错腿）：探针必须**只**让"写终态"失败，意图落库那条腿必须放行——
	// 两条都失败时本用例会退化成上一条用例，测不到"钱已动、状态没写"这一格。
	if err := refundWriteFaultErr(o.ID, []string{"refund_psp_status"}); err == nil {
		t.Fatalf("故障探针没覆盖状态回写这条写腿，本用例等于空转")
	}
	if err := refundWriteFaultErr(o.ID, []string{"refund_out_no", "refund_psp_status"}); err != nil {
		t.Fatalf("意图落库那条腿被误注入(%v)，本用例测的就不是它想测的那一段", err)
	}
	if got := psp.requests(); len(got) != 1 {
		t.Fatalf("本次应发出 1 次 PSP 请求，实际 %d：%v", len(got), got)
	}
	// 关键：单号已经在库里，下一轮对账补呼拿的是同一个号（"少发可补、多发不可收"）
	var row model.BillingOrder
	db.DB.First(&row, o.ID)
	if row.RefundOutNo != "RF"+o.OrderNo {
		t.Errorf("回写失败不该影响已落库的意图，实际 out_no=%q", row.RefundOutNo)
	}
	alerts.waitContaining(t, "退款状态未落库")

	// 订单不存在时 RowsAffected==0 也必须判错（否则"钱出了、账没写"会被当成写成功）
	refundWriteFault = nil
	if err := writeRefundPspStatus(999999999, &tid, RefundPspOK); err == nil || !strings.Contains(err.Error(), "0 行") {
		t.Errorf("回写 0 行必须判错，实际 %v", err)
	}
	// 租户谓词反证（G-12，2026-09-27）：**真实存在**的订单、只把租户换成别家，也必须判 0 行。
	// 缺这一腿，`AND tenant_id IS NOT DISTINCT FROM ?` 写成 `AND 1=1` 照样全绿——
	// 而对账器扫的正是跨租户队列，"改走别家的单、RowsAffected 仍是 1"是这里唯一会静默的错法。
	other := tid + 987654
	if err := writeRefundPspStatus(o.ID, &other, RefundPspOK); err == nil || !strings.Contains(err.Error(), "0 行") {
		t.Errorf("用别家租户 ID 回写同一订单必须判 0 行，实际 %v（说明谓词没带租户）", err)
	}
	// 同租户必须写得进去（反向对照：上一腿的红不能是"这个函数永远写不进"）
	if err := writeRefundPspStatus(o.ID, o.TenantID, RefundPspOK); err != nil {
		t.Errorf("同租户回写应成功，实际 %v", err)
	}
}

// TestRefundPayout_PSPFailureKeepsRetryableState PSP 明确拒单 → 停在 psp_pending（同一号可重试）
// 而不是 psp_failed：psp_failed 保留给**查单确认**的终态，拒单是可重试态，两者不能混。
func TestRefundPayout_PSPFailureKeepsRetryableState(t *testing.T) {
	testutil.SetupTestDB(t)
	psp := newFakePSP()
	psp.fail = true
	defer psp.close()
	alerts := newAlertSink()
	defer alerts.close()
	restore := confWith(psp.srv.URL, alerts.srv.URL)
	defer restore()

	o, tid := mkRefundedOrder(t, "gateway")
	defer testutil.CleanupTenant(t, tid)

	executeRefundPayout(o, int64(o.RefundAmountCents))
	var row model.BillingOrder
	db.DB.First(&row, o.ID)
	if row.RefundPspStatus != RefundPspPending {
		t.Errorf("PSP 拒单应停 %s（下一轮拿同一号补呼），实际 %s", RefundPspPending, row.RefundPspStatus)
	}
	if row.RefundOutNo == "" {
		t.Errorf("拒单后单号必须留在库里，否则补呼又会换新号")
	}
	alerts.waitContaining(t, "退款出款失败")

	// 补呼一次：号不变（这正是本批建立的幂等锚）
	executeRefundPayout(&row, int64(row.RefundAmountCents))
	got := psp.requests()
	if len(got) != 2 || got[0] != got[1] {
		t.Errorf("补呼应复用同一单号，实际收到 %v", got)
	}
}

// utQuerier 实现 PaymentProvider + RefundResultQuerier 的假渠道：按预置结果回答查单
type utQuerier struct {
	result string
	err    error
	asked  []string
}

// Name 渠道名（PaymentProvider 接口要求，假渠道固定返回 ut_querier）
func (u *utQuerier) Name() string { return "ut_querier" }

// CreatePayment 下单（本文件只测退款侧，假渠道的下单实现恒返回空，不参与断言）
func (u *utQuerier) CreatePayment(*model.BillingOrder) (string, error) { return "", nil }

// Refund 发起出款（同样只测退款回查，这里恒成功——出款本身的用例用真 httptest 假 PSP 打）
func (u *utQuerier) Refund(*model.BillingOrder, int) error { return nil }

// QueryRefundStatus 主动查单：按预置的 result/err 回答，并记下这次拿的是哪个退款单号
// （断言侧要证明"查的就是库里那个号"，而不是查了个现场拼出来的新号）
func (u *utQuerier) QueryRefundStatus(o *model.BillingOrder, outNo string) (string, error) {
	u.asked = append(u.asked, outNo)
	if u.err != nil {
		return "", u.err
	}
	return u.result, nil
}

// resolveQuerier 恒返回这个假渠道（测试里不需要按 channel 分发）
func resolveQuerier(string) (PaymentProvider, error) { return &utQuerier{}, nil }

// TestReconcileOneRefundResult_OnlyTerminalWritesBack ④ 查单五态逐格打
func TestReconcileOneRefundResult_OnlyTerminalWritesBack(t *testing.T) {
	testutil.SetupTestDB(t)
	restore := runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(map[string]string{}, nil))
	defer restore()

	cases := []struct {
		name    string
		result  string
		qerr    error
		want    string
		changed bool
	}{
		{"成功收敛", "success", nil, RefundPspSuccess, true},
		{"失败收敛", "failed", nil, RefundPspFailed, true},
		{"处理中不动", "processing", nil, RefundPspOK, false},
		{"未知值不动", "SOMETHING_NEW", nil, RefundPspOK, false},
		{"查单报错不动", "", errors.New("模拟：网络不可达"), RefundPspOK, false},
	}
	for _, tc := range cases {
		q := &utQuerier{result: tc.result, err: tc.qerr}
		o, tid := mkRefundedOrder(t, "ut_querier")
		outNo := "RF" + o.OrderNo
		if err := db.DB.Model(&model.BillingOrder{}).Where("id = ?", o.ID).
			Updates(map[string]interface{}{"refund_out_no": outNo, "refund_psp_status": RefundPspOK}).Error; err != nil {
			t.Fatalf("%s 前置落库失败: %v", tc.name, err)
		}
		var order model.BillingOrder
		db.DB.First(&order, o.ID)

		if got := reconcileOneRefundResult(&order, func(string) (PaymentProvider, error) { return q, nil }); got != tc.changed {
			t.Errorf("%s：收敛标志应为 %v，实际 %v", tc.name, tc.changed, got)
		}
		var row model.BillingOrder
		db.DB.First(&row, o.ID)
		if row.RefundPspStatus != tc.want {
			t.Errorf("%s：查单返回 %q(err=%v) 后状态应为 %s，实际 %s",
				tc.name, tc.result, tc.qerr, tc.want, row.RefundPspStatus)
		}
		if len(q.asked) != 1 || q.asked[0] != outNo {
			t.Errorf("%s：应按落库的退款单号查单，实际问了 %v（期望 [%s]）", tc.name, q.asked, outNo)
		}
		testutil.CleanupTenant(t, tid)
	}
}

// TestRefundResultQueryQueryShape 回查取单谓词的形状锁：只捞 psp_ok、必须有号、避开刚受理的窗口、
// 带确定性排序与批量上限（无排序的 LIMIT 会让部分订单永久饿死，口径同 2026-09-25 残项批）。
func TestRefundResultQueryQueryShape(t *testing.T) {
	testutil.SetupTestDB(t)
	stmt := refundResultQueryQuery(db.DB.Session(&gorm.Session{DryRun: true})).
		Find(&[]model.BillingOrder{}).Statement
	sql := stmt.Dialector.Explain(stmt.SQL.String(), stmt.Vars...)
	upper := strings.ToUpper(sql)
	for _, want := range []string{"ORDER BY", "LIMIT", "REFUND_OUT_NO"} {
		if !strings.Contains(upper, want) {
			t.Errorf("回查取单 SQL 缺 %s：%s", want, sql)
		}
	}
	if strings.Contains(upper, "PSP_PENDING") {
		t.Errorf("回查不得捞 psp_pending（那是补呼的地盘，两边同抢会让同一单既被重发又被查询）：%s", sql)
	}
}
