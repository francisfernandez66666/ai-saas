// 开票交付触达（FIX-9，2026-09-27）：发票开具后把"号是多少、怎么拿"寄到客户留的邮箱。
//
// 为什么单独一个文件、不复用 sendToTenantAdmins：commercial_notify.go 里那三条外呼
// （用量预警 / 催缴 / 到期提醒）的收件人都是**该租户的管理员账号**，走 TenantAdminEmails 查库；
// 而发票的收件人是**申请发票时当场填下的 invoice_email**——它可能是财务、可能是外部代理记账，
// 未必是系统里任何一个账号。把两种收件人口径混进一个函数，迟早有一侧发错人
// （给财务发催缴、或给不相关的管理员发带抬头税号的票据信息，后者还是敏感信息外溢）。
//
// 返回值为什么是结果码字符串而不是 bool：见迁移 029。核心是 log_only 这一档——
// SMTP 未配置时 LogSender.SendRaw 只打日志并 **return nil**，也就是"发送成功"。
// 若本函数返回 bool，那条分支会被记成"已通知客户"，超管台的发票列表因此永远挑不出
// "开了票但客户其实什么都没收到"这批单，只能等客户提工单——这正是本仓反复根除的
// "只会打日志并返回成功的守卫等于没有守卫"（M1 / backup.sh 两次同因事故）。
package notify

import (
	"fmt"
	"log"
	"strings"
)

// 开票触达结果码（落 billing_orders.invoice_notify_result，字面量即入库值，勿随意改名）
const (
	// InvoiceNotifySMTPSent 真发出去了且 SMTP 返回成功——唯一可对客户承诺"已发送"的一档
	InvoiceNotifySMTPSent = "smtp_sent"
	// InvoiceNotifyLogOnly SMTP 未配置，内容只落到服务端日志。客户**没有**收到任何东西。
	InvoiceNotifyLogOnly = "log_only"
	// InvoiceNotifyNoRecipient 订单上没有接收邮箱（申请时没填），无从触达
	InvoiceNotifyNoRecipient = "no_recipient"
	// InvoiceNotifySendFailed 配置齐备但发送被拒（认证失败/收件人拒收/连接被断等）
	InvoiceNotifySendFailed = "send_failed"
	// InvoiceNotifySendTimeout 发送超时（拨号或会话撞上 notify 侧的时间墙）
	InvoiceNotifySendTimeout = "send_timeout"
)

// InvoiceEmailInput 开票交付邮件入参（结构体而非位置参数：这封信要同时回显抬头、税号、
// 发票号、金额四个字段，五个 string 位置参数传错一位就是一封"金额对不上"的票据邮件）。
type InvoiceEmailInput struct {
	To          string      // 收件人：订单上的 invoice_email（客户当场填的，不是系统账号）
	Title       string      // 发票抬头
	TaxNo       string      // 纳税人识别号（空则正文省略该行）
	InvoiceNo   string      // 发票号
	AmountYuan  string      // 金额（元，已格式化；本包不做分→元换算，口径归 lib/money 与 billing）
	OrderNo     string      // 订单号（客户在自己收银台里能对上号）
	IssuedAtStr string      // 开票日期（"2006-01-02"）
	SMTP        func() bool // 注入点：单测可把"通道就绪态"设为假，无需真发信
}

// InvoiceIssuedEmail 发一封开票交付邮件，返回**如实**的结果码。
//
// 判定次序（每一档都对应一种运维动作，顺序不可随意调）：
//  1. 收件人为空 → no_recipient（此时连"通道是否就绪"都不必问，别把没邮箱的单报成"SMTP 故障"）
//  2. SMTP 未配置 → log_only（只打日志，客户没收到，运营需人工补发）
//  3. 发送失败 → send_failed / send_timeout（按错误链判定超时，见 IsTimeoutErr）
//  4. 成功 → smtp_sent
//
// 同步发送是刻意的：调用点在超管「开具」这一次 HTTP 请求里，异步发信会把结果码留在"上一次的值"上，
// 发票列表当场显示不出这次的成败。最坏阻塞由 notify 侧的时间墙兜住（拨号 8s + 会话 15s），
// 而 IssueInvoice 本身只改两列、没有事务持有，多等 15s 不会锁住任何账。
func InvoiceIssuedEmail(in InvoiceEmailInput) string {
	to := strings.TrimSpace(in.To)
	if to == "" {
		log.Printf("[发票触达] 订单%s 无接收邮箱，未发送（请在超管台核对后人工联系客户）", in.OrderNo)
		return InvoiceNotifyNoRecipient
	}
	ready := SMTPReady
	if in.SMTP != nil {
		ready = in.SMTP
	}
	if !ready() {
		// 走 log 通道只为留一份可核对的正文，绝不据此对客户承诺"已发送"。
		// 这里刻意不写成 `if err := sender.SendRaw(); err != nil`：LogSender 恒返回 nil，
		// 那样写会得到一个假的"发送成功"（见 notifier.go 里 sendToLogChannel 的注释）。
		sendToLogChannel([]string{to}, "发票已开具（log 通道，客户未收到）", invoiceMailBody(in))
		log.Printf("[发票触达] SMTP 未配置，订单%s 的交付邮件降级日志（收件人 %s）", in.OrderNo, MaskEmailForLog(to))
		return InvoiceNotifyLogOnly
	}
	s := NewSMTPSenderFromEnv()
	subject := fmt.Sprintf("【LexCross 跨山】您的发票 %s 已开具", in.InvoiceNo)
	if err := s.SendRaw([]string{to}, subject, invoiceMailBody(in)); err != nil {
		if IsTimeoutErr(err) {
			log.Printf("[发票触达] 发送超时 订单%s to=%s: %v", in.OrderNo, MaskEmailForLog(to), err)
			return InvoiceNotifySendTimeout
		}
		log.Printf("[发票触达] 发送失败 订单%s to=%s: %v", in.OrderNo, MaskEmailForLog(to), err)
		return InvoiceNotifySendFailed
	}
	log.Printf("[发票触达] 已发送 订单%s to=%s", in.OrderNo, MaskEmailForLog(to))
	return InvoiceNotifySMTPSent
}

// InvoiceNotifyText 结果码 → 给人看的一句话（文案单点：超管台列表、开具/重发的 HTTP message
// 都从这里取，避免同一个码在两个地方一个说"已通知"一个说"发送成功"）。
// 四条否定档必须各自给出下一步动作，只说"失败"等于把问题原样丢回给运营。
func InvoiceNotifyText(code string) string {
	switch code {
	case InvoiceNotifySMTPSent:
		return "已发送交付邮件"
	case InvoiceNotifyLogOnly:
		return "SMTP 未配置，客户未收到（请配置邮件通道后点「重发」）"
	case InvoiceNotifyNoRecipient:
		return "该订单没有接收邮箱，客户未收到（请向客户索取后点「重发」）"
	case InvoiceNotifySendTimeout:
		return "发送超时，客户可能未收到（可点「重发」，重复投递由收件人自行判重）"
	case InvoiceNotifySendFailed:
		return "发送失败，客户未收到（见服务端日志，可点「重发」）"
	case "":
		return "尚未通知客户"
	default:
		return "未知结果码 " + code
	}
}

// invoiceMailBody 组装正文。
// 口径三条：① 短句、不用"您"堆敬语（AGENTS 去 AI 味约定，票据邮件更该克制）；
// ② 不回显完整收件人邮箱以外的敏感项，税号是**客户自己提供的抬头信息**、寄回给同一收件人不算外溢；
// ③ 明确写"电子/纸质开具方式以销售方为准，本邮件仅作交付通知"——
// 本平台是"人工开票 + 回录发票号"（未接税务直连），把邮件写成"发票已在附件里"是虚假承诺。
func invoiceMailBody(in InvoiceEmailInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "订单 %s 的发票已开具。\n\n", in.OrderNo)
	fmt.Fprintf(&b, "发票号：%s\n", in.InvoiceNo)
	fmt.Fprintf(&b, "开票日期：%s\n", in.IssuedAtStr)
	fmt.Fprintf(&b, "金额：%s\n", in.AmountYuan)
	fmt.Fprintf(&b, "发票抬头：%s\n", in.Title)
	if strings.TrimSpace(in.TaxNo) != "" {
		fmt.Fprintf(&b, "纳税人识别号：%s\n", in.TaxNo)
	}
	b.WriteString("\n本邮件为开票完成通知。发票原件的交付方式（电子/纸质）以销售方为准，" +
		"如与贵司要求不符，回复本邮件即可重新开具。\n")
	b.WriteString("\n—— LexCross 跨山 · AI SCRM")
	return b.String()
}
