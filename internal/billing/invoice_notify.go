// 开票交付触达的装配点（FIX-9，2026-09-27）。
//
// 为什么要这一层间接（口径与 d3_notify.go 完全一致，动机也一样）：
// 本仓单测与开发库共用同一份 .env，SMTP 凭证是真值。IssueInvoice 一旦被单测调用就直接给
// 库里那条 invoice_email 发真邮件——而 invoice_email 是**客户填的外部地址**，
// 不是我们可以随意投递的测试邮箱。所以真实外呼收在这一个装配点，单测把 inv 换成计数桩。
//
// 桩要能区分四档结果码（smtp_sent / log_only / no_recipient / send_failed / send_timeout），
// 而不只是"调了几次"：本批改动的全部价值就在于"结果码如实"，
// 若桩只回 bool，测试里 log_only 与 smtp_sent 就无法分开，等于没测。
package billing

import (
	"log"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/notify"
)

// invoiceSink 开票触达出口集合（函数字段，同 d3Sink 的取舍：动作只有两三个，接口反而藏住调用点）
type invoiceSink struct {
	// SendInvoiceEmail 发一封开票交付邮件并回结果码（生产装配直通 notify）
	SendInvoiceEmail func(in notify.InvoiceEmailInput) string
	// Now 时间源（单测要断"重发会刷新 invoice_notified_at"，就得能把时刻钉死）
	Now func() time.Time
}

// prodInvoiceSink 生产装配：邮件直通 notify，时刻取系统时间
func prodInvoiceSink() invoiceSink {
	return invoiceSink{
		SendInvoiceEmail: notify.InvoiceIssuedEmail,
		Now:              time.Now,
	}
}

// inv 当前生效的开票触达装配（包级变量；单测替换后必须还原，见 withInvoiceSink）
var inv = prodInvoiceSink()

// withInvoiceSink 临时替换装配并返回还原函数（供单测用，生产代码不调）
func withInvoiceSink(stub invoiceSink) func() {
	old := inv
	inv = stub
	return func() { inv = old }
}

// NotifyInvoiceIssued 开票后触达客户，并把**如实**的结果码与时刻写回订单。
//
// 返回值就是入库的那个结果码（调用方据此决定给超管看"已通知客户"还是"客户未收到，需人工补发"）。
// 落库用字段级 Updates 而不是整行 Save：本函数在 IssueInvoice 之后执行，手上的 o 是开具前的快照，
// 整行写回会把并发续费/退款改动的列一起覆盖掉（本仓已按此口径收口过 9 处，见复核批 P0-1）。
// 若这次写库失败，仍然把码返回给调用方——**不能因为记不上就谎报"没发出去"**：
// 邮件确实已经投了，回滚成"未通知"会导致运营重发一封重复票据邮件。
func NotifyInvoiceIssued(o *model.BillingOrder) string {
	if o == nil {
		return notify.InvoiceNotifyNoRecipient
	}
	code := inv.SendInvoiceEmail(notify.InvoiceEmailInput{
		To:          o.InvoiceEmail,
		Title:       o.InvoiceTitle,
		TaxNo:       o.InvoiceTaxNo,
		InvoiceNo:   o.InvoiceNo,
		AmountYuan:  "¥" + centsToYuan(o.AmountCents),
		OrderNo:     o.OrderNo,
		IssuedAtStr: inv.Now().Format("2006-01-02"),
	})
	// 结果码必须落库：只在 HTTP 响应里说一句"已通知"，刷新一次页面就没了，
	// 而"哪些单开了票客户却没收到"是一张需要天天看的清单，不是一次性的提示。
	// 谓词带 tenant_id（G-12，与 writeRefundPspStatus 同一口径）：本函数由超管代管租户时也会走到，
	// 只按主键写会把"改了别家订单的触达结果"这件事变成静默成功。
	// 用 IS NOT DISTINCT FROM 是为兼容历史 NULL 租户行（用 `=` 会让那些行永远匹配不上）。
	if err := db.DB.Model(&model.BillingOrder{}).
		Where("id = ? AND tenant_id IS NOT DISTINCT FROM ?", o.ID, o.TenantID).
		Updates(map[string]interface{}{
			"invoice_notify_result": code,
			"invoice_notified_at":   inv.Now(),
		}).Error; err != nil {
		// 记不上只告警、不改判定（理由见函数头注释）
		log.Printf("[发票触达] 订单%d 结果码%s 落库失败: %v", o.ID, code, err)
	}
	o.InvoiceNotifyResult = code
	now := inv.Now()
	o.InvoiceNotifiedAt = &now
	return code
}
