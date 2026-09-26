// 开票交付触达（FIX-9，2026-09-27）单测。
//
// 本文件钉住四件事，每件都对应一条"不做会怎样"：
//  1. 开具即触达、且**只触达一次**——重复调用 IssueInvoice 必须被状态机挡住（第二次连发信都不该发生），
//     否则"给客户发两封同一张发票的邮件"会由状态机漏判直接带到客户面前；
//  2. 五种结果码逐字落库——若桩只回 bool，log_only（客户其实没收到）与 smtp_sent 就分不开，
//     这条测试也就没有存在价值（这正是本批改掉的那个"记了但没做"的形态）；
//  3. 重发入口只认 issued，且会刷新时刻——没有这条，运营对"开了票没寄出"的单只能作废重开两张票；
//  4. 无邮箱的订单在**生产装配**下直接判 no_recipient，一次都不碰 SMTP——
//     判定次序如果反过来（先连 SMTP 再发现没收件人），配置了真凭证的库就会对空地址发起投递。
//
// 外呼一律走 newSilentInvoiceSink 计数桩（口径同 d3_notify.go）：本包单测与开发库共用同一份 .env，
// SMTP 凭证是真值，而 invoice_email 是**客户填的外部地址**，不是可以随便投的测试邮箱。
// 第 4 条刻意不桩（走 prodInvoiceSink），因为它要证的正是"生产路径自己不会走到发信"。
package billing

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/notify"
	"ai-scrm/internal/testutil"
)

// invCall 一次开票触达的入参快照（断言"信里到底写了什么"用，不看桩的返回值）
type invCall struct {
	In   notify.InvoiceEmailInput
	Code string
	At   time.Time
}

// newSilentInvoiceSink 构造只记账不发信的桩。
// codes 按调用次序依次返回（越界用最后一个），这样"第二次调用被状态机挡住"和
// "第二次返回了不同的码"两种实现能被分开测——只测"发了几封"会放过后者。
//
// 收件人为空这一档**由桩自己判**（与生产 InvoiceIssuedEmail 同构）：no_recipient 是
// "有没有人可以收"的事实，不是被测代码的选择。若桩无条件吐 codes[0]，
// "无邮箱单被判成 smtp_sent"这种实现照样全绿——那正是本批要防的假绿形态（口径同 newSilentD3Sink：
// 有 SMTP 且有收件人才算发出）。
func newSilentInvoiceSink(log *[]invCall, codes ...string) invoiceSink {
	return invoiceSink{
		SendInvoiceEmail: func(in notify.InvoiceEmailInput) string {
			code := notify.InvoiceNotifySMTPSent
			if strings.TrimSpace(in.To) == "" {
				code = notify.InvoiceNotifyNoRecipient
			} else if len(codes) > 0 {
				code = codes[min(len(*log), len(codes)-1)]
			}
			*log = append(*log, invCall{In: in, Code: code, At: time.Now()})
			return code
		},
		Now: func() time.Time { return time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC) },
	}
}

// seedInvoiceOrder 造一笔"已支付、已申请发票、抬头税号邮箱齐备"的订单，返回其 ID。
// 直接插行而不走 RequestInvoice：那是一条独立的链路（申请侧已有 smoke §二十覆盖），
// 本文件要钉的是"开具之后发生了什么"，把两件事分在一个用例里会让失败归因指错方向。
func seedInvoiceOrder(t *testing.T, email string) uint {
	t.Helper()
	uid := uint(9)
	o := model.BillingOrder{
		TenantID: &uid, OrderNo: fmt.Sprintf("UTINV%d", time.Now().UnixNano()),
		AmountCents: 12345, Status: "paid", Channel: "mock",
		InvoiceRequested: true, InvoiceStatus: "requested",
		InvoiceTitle: "单元测试抬头", InvoiceTaxNo: "91310000TEST", InvoiceEmail: email,
	}
	if err := db.DB.Create(&o).Error; err != nil {
		t.Fatalf("造发票订单失败: %v", err)
	}
	t.Cleanup(func() { db.DB.Unscoped().Delete(&model.BillingOrder{}, o.ID) })
	return o.ID
}

// TestIssueInvoiceNotifiesExactlyOnce 开具即触达，且重复开具不再发第二封。
func TestIssueInvoiceNotifiesExactlyOnce(t *testing.T) {
	testutil.SetupTestDB(t)
	var calls []invCall
	restore := withInvoiceSink(newSilentInvoiceSink(&calls, notify.InvoiceNotifySMTPSent))
	defer restore()

	oid := seedInvoiceOrder(t, "buyer@example.test")
	o, err := IssueInvoice(oid, "FP2026092701")
	if err != nil {
		t.Fatalf("开具失败: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("开具应恰好触发一次触达，实际 %d 次", len(calls))
	}
	c := calls[0]
	// 正文入参必须带客户当场填的那些字段——只发一句"发票开好了"的话，客户仍然不知道号是多少
	if c.In.To != "buyer@example.test" || c.In.InvoiceNo != "FP2026092701" ||
		c.In.Title != "单元测试抬头" || c.In.TaxNo != "91310000TEST" ||
		c.In.AmountYuan != "¥123.45" {
		t.Fatalf("触达入参不完整: %+v", c.In)
	}
	if o.InvoiceNotifyResult != notify.InvoiceNotifySMTPSent {
		t.Fatalf("返回值未带结果码: %q", o.InvoiceNotifyResult)
	}
	var row model.BillingOrder
	if err := db.DB.First(&row, oid).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if row.InvoiceNotifyResult != notify.InvoiceNotifySMTPSent || row.InvoiceNotifiedAt == nil {
		t.Fatalf("结果码/时刻未落库: %q %v", row.InvoiceNotifyResult, row.InvoiceNotifiedAt)
	}

	// 第二次开具必须被状态机拒绝，且**一次信都不发**
	if _, err := IssueInvoice(oid, "FP2026092702"); err == nil {
		t.Fatalf("重复开具竟然成功——双答客户邮件的入口就此打开")
	}
	if len(calls) != 1 {
		t.Fatalf("重复开具后仍发了信，调用数=%d", len(calls))
	}
	// 反证配套：号码也不许被第二次调用改掉（库里必须还是第一张票的号）
	// ⚠ 必须写 `.Error`：`db.DB.First(...)` 返回的是 *gorm.DB，直接和 nil 比是**恒真**的
	// （指针永不为 nil），于是这条断言变成"无论对错都报错"的自伤形态——首跑就是这样红的，
	// 而且红得像是"号真被覆盖了"。读结论前先看错误值本身，别只看红绿。
	if err := db.DB.First(&row, oid).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if row.InvoiceNo != "FP2026092701" {
		t.Fatalf("发票号被第二次调用覆盖: %q", row.InvoiceNo)
	}
}

// TestNotifyInvoiceIssuedRecordsEveryCode 五种结果码逐字入库，且文案单点不重不漏。
func TestNotifyInvoiceIssuedRecordsEveryCode(t *testing.T) {
	testutil.SetupTestDB(t)
	codes := []string{
		notify.InvoiceNotifySMTPSent, notify.InvoiceNotifyLogOnly, notify.InvoiceNotifyNoRecipient,
		notify.InvoiceNotifySendFailed, notify.InvoiceNotifySendTimeout,
	}
	seenText := map[string]string{}
	for _, code := range codes {
		var calls []invCall
		restore := withInvoiceSink(newSilentInvoiceSink(&calls, code))
		oid := seedInvoiceOrder(t, "buyer@example.test")
		if _, err := IssueInvoice(oid, "FP_"+code); err != nil {
			restore()
			t.Fatalf("开具失败(%s): %v", code, err)
		}
		restore()
		var row model.BillingOrder
		if err := db.DB.First(&row, oid).Error; err != nil {
			t.Fatalf("回读失败: %v", err)
		}
		if row.InvoiceNotifyResult != code {
			t.Fatalf("结果码 %s 没有如实入库，读到 %q", code, row.InvoiceNotifyResult)
		}
		txt := notify.InvoiceNotifyText(code)
		if txt == "" || seenText[txt] != "" {
			t.Fatalf("结果码 %s 的文案为空或与 %s 重复：列表将无法区分该做什么", code, seenText[txt])
		}
		seenText[txt] = code
	}
	if len(seenText) != len(codes) {
		t.Fatalf("五种结果码应有五种说法，实际 %d 种", len(seenText))
	}
	// 空码（从未尝试）必须有独立话术：把 nil 结果翻成"发送失败"会让运营去查一条不存在的故障
	if notify.InvoiceNotifyText("") == notify.InvoiceNotifyText(notify.InvoiceNotifySendFailed) {
		t.Fatalf("「尚未通知」与「发送失败」共用一句话，列表读不出区别")
	}
}

// TestResendInvoiceNoticeGate 重发入口：未开具必拒、已开具可发且时刻前进。
func TestResendInvoiceNoticeGate(t *testing.T) {
	testutil.SetupTestDB(t)
	var calls []invCall
	// 时刻做成可推进的：不刷新时刻，列表上就看不出"这次补发到底发生了什么没有"
	cur := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	sink := newSilentInvoiceSink(&calls, notify.InvoiceNotifySMTPSent)
	sink.Now = func() time.Time { return cur }
	restore := withInvoiceSink(sink)
	defer restore()

	// ① requested 态重发：既不该发信，也不该改状态。
	//    ⚠ 这一条必须是**带着旧发票号**的 requested，否则测不出状态闸门——
	//    首次写这条时用的是刚申请的裸单（invoice_no 为空），变异掉 `!= "issued"` 闸门之后用例**照样全绿**：
	//    拦截来自后面那句"尚无发票号"，而不是来自状态判定，等于什么都没锁住。
	//    而"作废后重新申请"恰好会造出这个形态：VoidInvoice 只把状态置 voided、发票号留在行上，
	//    RequestInvoice 又把状态改回 requested（它允许重提改抬头），于是库里躺着一张**旧号 + 待开具**。
	//    闸门被摘掉时，客户会收到一封"您的发票 FP… 已开具"的邮件，而那张票其实已经作废了。
	oidPending := seedInvoiceOrder(t, "buyer@example.test")
	if _, err := IssueInvoice(oidPending, "FP_STALE"); err != nil {
		t.Fatalf("开具失败: %v", err)
	}
	if _, err := VoidInvoice(oidPending); err != nil {
		t.Fatalf("作废失败: %v", err)
	}
	if _, err := RequestInvoice(oidPending, "单元测试抬头", "91310000TEST", "buyer@example.test"); err != nil {
		t.Fatalf("重新申请失败: %v", err)
	}
	var stale model.BillingOrder
	if err := db.DB.First(&stale, oidPending).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	// 前置自检：这条用例的全部判别力都建立在"状态回到 requested 但旧号还在"上，
	// 若哪天 RequestInvoice 顺手清了号，本用例就退回上面那种"恒绿的空转"形态。
	if stale.InvoiceStatus != "requested" || stale.InvoiceNo == "" {
		t.Fatalf("前置形态不成立（应为 requested + 残留旧号）：status=%q no=%q", stale.InvoiceStatus, stale.InvoiceNo)
	}
	beforePending := len(calls)
	if _, _, err := ResendInvoiceNotice(oidPending); err == nil {
		t.Fatalf("未开具的单允许重发——客户会收到一张已作废发票的交付邮件")
	}
	if len(calls) != beforePending {
		t.Fatalf("被拒的重发仍然发了信，调用数=%d", len(calls)-beforePending)
	}

	// ② 无邮箱的已开具单：允许走（状态与号码都在），但结果码必须是 no_recipient
	oidNoMail := seedInvoiceOrder(t, "")
	nBefore := len(calls)
	if _, err := IssueInvoice(oidNoMail, "FP_NOMAIL"); err != nil {
		t.Fatalf("开具失败: %v", err)
	}
	if len(calls) != nBefore+1 || calls[nBefore].Code != notify.InvoiceNotifyNoRecipient {
		t.Fatalf("无邮箱单的判定错: 新增 %d 次 %+v", len(calls)-nBefore, calls[nBefore:])
	}

	// ③ issued 态重发：再发一封、时刻前进
	oid := seedInvoiceOrder(t, "buyer@example.test")
	if _, err := IssueInvoice(oid, "FP_RESEND"); err != nil {
		t.Fatalf("开具失败: %v", err)
	}
	before := len(calls)
	var row model.BillingOrder
	if err := db.DB.First(&row, oid).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	firstAt := *row.InvoiceNotifiedAt
	cur = cur.Add(3 * time.Hour)
	o2, code, err := ResendInvoiceNotice(oid)
	if err != nil {
		t.Fatalf("重发失败: %v", err)
	}
	if len(calls) != before+1 {
		t.Fatalf("重发没有真的再发一次，调用数=%d 期望=%d", len(calls), before+1)
	}
	if code != notify.InvoiceNotifySMTPSent || o2.InvoiceNotifyResult != code {
		t.Fatalf("重发结果码错: %q", code)
	}
	if err := db.DB.First(&row, oid).Error; err != nil {
		t.Fatalf("重发后回读失败: %v", err)
	}
	if row.InvoiceNotifiedAt == nil {
		t.Fatalf("重发后时刻为空，列表看不出这次补发发生过")
	}
	if !row.InvoiceNotifiedAt.After(firstAt) {
		t.Fatalf("重发未刷新时刻：%v 不在 %v 之后", row.InvoiceNotifiedAt, firstAt)
	}
}

// TestInvoiceIssuedEmailNoRecipientNeverTouchesSMTP 生产装配下、无邮箱的订单必须在
// 判通道之前就返回 no_recipient——这里**故意不桩**，让真实函数跑一遍。
// 反证方向：若把 no_recipient 那条判据挪到 ready() 之后，本用例会走到 LogSender/SMTP，
// 在配了 SMTP 的开发机上就是"对一个空收件人发起投递"（轻则报错日志，重则一封乱码邮件）。
func TestInvoiceIssuedEmailNoRecipientNeverTouchesSMTP(t *testing.T) {
	if got := notify.InvoiceIssuedEmail(notify.InvoiceEmailInput{To: "   ", OrderNo: "UT_X"}); got != notify.InvoiceNotifyNoRecipient {
		t.Fatalf("空白收件人应判 no_recipient，实际 %q", got)
	}
}
