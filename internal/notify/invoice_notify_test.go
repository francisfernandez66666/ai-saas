// 开票交付邮件（FIX-9，2026-09-27）本包单测：钉的是**结果码如实**这几条判据本身。
//
// billing 包那侧的测试（internal/billing/invoice_notify_test.go）用的是计数桩，证的是
// "开具会触达、结果会落库"；本文件证的是**发信函数自己**：五种码分别在什么条件下出现，尤其
// "SMTP 没配"这一条绝不能报成 smtp_sent——它是本批全部价值的落点
// （LogSender.SendRaw 恒返回 nil，也就是"永远成功"，任何偷懒的错误判定都会顺势把它当成发出去了）。
//
// 四条外呼用例**都不碰真外网**：
// no_recipient / log_only 在函数入口就被挡住，根本走不到 dial（用例里直接反证这一点）；
// send_failed 拨 127.0.0.1:1（本机保留端口，必然拒连，前提另有自证用例）；
// send_timeout 拨本用例自己起的"只接不回话"监听器，并把会话截止临时调到 200ms，确定性触发。
package notify

import (
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestInvoiceIssuedEmail_NoRecipient 空/空白收件人必须在**判通道之前**返回 no_recipient。
// 反证方向：把判空挪到 ready() 之后，本用例红在 called——
// 无邮箱的单会先去探 SMTP，运营看到的提示就从"去问客户要邮箱"变成"去配 SMTP"，动作完全指错。
func TestInvoiceIssuedEmail_NoRecipient(t *testing.T) {
	probed := false
	got := InvoiceIssuedEmail(InvoiceEmailInput{
		To:      "  \t ",
		OrderNo: "UT1",
		SMTP:    func() bool { probed = true; return true },
	})
	if got != InvoiceNotifyNoRecipient {
		t.Fatalf("空白收件人应判 no_recipient，实际 %q", got)
	}
	if probed {
		t.Fatalf("判空发生在通道探测之后：无邮箱的单被记成 SMTP 问题")
	}
}

// TestInvoiceIssuedEmail_LogOnlyIsNotSuccess SMTP 未配置 → log_only，且不得与 smtp_sent 混同。
// 这是整个改动的判别核心：LogSender.SendRaw **返回 nil**，若实现照抄
// `if err := sender.SendRaw(...); err != nil { ... }; return smtp_sent`，结果码就成了 smtp_sent，
// 而客户一封信都没收到。反证方向：把该分支返回值改成 InvoiceNotifySMTPSent，本用例必红。
func TestInvoiceIssuedEmail_LogOnlyIsNotSuccess(t *testing.T) {
	got := InvoiceIssuedEmail(InvoiceEmailInput{
		To: "buyer@example.test", OrderNo: "UT2", InvoiceNo: "FP-UT2", Title: "测试抬头",
		SMTP: func() bool { return false },
	})
	if got != InvoiceNotifyLogOnly {
		t.Fatalf("SMTP 未配置应判 log_only，实际 %q", got)
	}
	if got == InvoiceNotifySMTPSent {
		t.Fatalf("log_only 与 smtp_sent 同码：客户没收到任何东西却被记成已交付")
	}
	if txt := InvoiceNotifyText(got); !strings.Contains(txt, "未收到") {
		t.Fatalf("log_only 的话术没写清客户未收到：%q", txt)
	}
}

// TestInvoiceIssuedEmail_SendFailed 通道就绪但拨号被拒 → send_failed（既不是 smtp_sent 也不是 timeout）。
// 反证方向：把所有 error 一律折成 send_timeout，本用例红——两类故障的运维动作不同。
func TestInvoiceIssuedEmail_SendFailed(t *testing.T) {
	useSMTPPointingTo(t, "127.0.0.1", 1)
	if !SMTPReady() {
		t.Skip("SMTPReady 判定与预期不同构（Host/User 已设却未就绪），本用例的前提不成立")
	}
	got := InvoiceIssuedEmail(InvoiceEmailInput{
		To: "buyer@example.test", OrderNo: "UT3", InvoiceNo: "FP-UT3", Title: "测试抬头",
	})
	if got != InvoiceNotifySendFailed {
		t.Fatalf("拨号被拒应判 send_failed，实际 %q", got)
	}
}

// TestInvoiceIssuedEmail_SendTimeout 连得上但服务器一言不发 → send_timeout。
// 会话截止临时调到 200ms（smtpSessionTimeout 之所以是 var 而不是 const，就是为了这一条能跑）；
// 监听器 accept 之后只持有连接、不写一个字节，客户端卡在 SMTP greeting 的读上直到截止。
// 反证方向：摘掉 conn.SetDeadline，本用例不会拿到 send_timeout，而是**一直挂着**，
// 由 5s 的总墙钟兜底报"发送挂住未返回"——那正是旧实现把 HTTP 请求吊死的形态。
func TestInvoiceIssuedEmail_SendTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("本机无法监听回环端口: %v", err)
	}
	defer ln.Close()

	// 连接必须被接住并持有：listener 队列满了之后内核会直接 RST，
	// 那会让拨号方拿到"连接被重置"（send_failed）而不是"连上了没人说话"（send_timeout），
	// 两条分支就此混在一起，本用例测的就不再是超时。
	var mu sync.Mutex
	held := []net.Conn{}
	done := make(chan struct{})
	defer func() {
		close(done)
		mu.Lock()
		for _, c := range held {
			_ = c.Close()
		}
		mu.Unlock()
	}()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
		}
	}()

	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("解析监听地址失败: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("端口非法 %q: %v", portStr, err)
	}
	oldDial, oldSess := smtpDialTimeout, smtpSessionTimeout
	smtpDialTimeout, smtpSessionTimeout = 2*time.Second, 200*time.Millisecond
	defer func() { smtpDialTimeout, smtpSessionTimeout = oldDial, oldSess }()

	useSMTPPointingTo(t, host, port)
	res := make(chan string, 1)
	go func() {
		res <- InvoiceIssuedEmail(InvoiceEmailInput{
			To: "buyer@example.test", OrderNo: "UT4", InvoiceNo: "FP-UT4", Title: "测试抬头",
		})
	}()
	select {
	case got := <-res:
		if got != InvoiceNotifySendTimeout {
			t.Fatalf("服务器不回复应判 send_timeout，实际 %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("发送挂住未返回：conn.SetDeadline 没生效（旧实现的真实形态——HTTP 请求会永远吊着）")
	}
}

// TestInvoiceNotifyTextDistinct 五种码 + 空码各有一句互不相同的话（文案单点的自证）。
// 反证方向：把任一 case 落到 default，本用例即在"共用一句话"上判红。
func TestInvoiceNotifyTextDistinct(t *testing.T) {
	codes := []string{"", InvoiceNotifySMTPSent, InvoiceNotifyLogOnly, InvoiceNotifyNoRecipient,
		InvoiceNotifySendFailed, InvoiceNotifySendTimeout}
	seen := map[string]string{}
	for _, c := range codes {
		txt := InvoiceNotifyText(c)
		if txt == "" {
			t.Fatalf("结果码 %q 没有对应话术", c)
		}
		if prev, dup := seen[txt]; dup {
			t.Fatalf("结果码 %q 与 %q 共用一句话：%s", c, prev, txt)
		}
		seen[txt] = c
	}
	// 未知码不得伪装成任何一种已知结论（静默折成"已发送"是最坏形态）
	if InvoiceNotifyText("ut_bogus_code") == InvoiceNotifyText(InvoiceNotifySMTPSent) {
		t.Fatalf("未知结果码被判成已发送")
	}
	if !strings.Contains(InvoiceNotifyText("ut_bogus_code"), "ut_bogus_code") {
		t.Fatalf("未知结果码的话术没把码本身带出来，运维无从查起")
	}
}

// TestSelfCheckDeadPortRefused 前置自证：send_failed 那条用例的靶点必须真的是"拒连"。
// 若哪天本机 127.0.0.1:1 被真服务占了，拨号会成功，那条用例测的就不是 send_failed 分支了。
func TestSelfCheckDeadPortRefused(t *testing.T) {
	c, err := net.DialTimeout("tcp", "127.0.0.1:1", 2*time.Second)
	if err == nil {
		_ = c.Close()
		t.Skip("本机 127.0.0.1:1 居然可连，send_failed 用例的前提不成立（换靶点，勿放宽断言）")
	}
}

// useSMTPPointingTo 把 SMTP 环境变量指向指定靶点。
// 用 t.Setenv 而不是直接 os.Setenv：它自动复原，且会让本用例禁止并行——
// SMTPReady 读的是**进程环境**，并行用例互踩配置就测不准了。
func useSMTPPointingTo(t *testing.T, host string, port int) {
	t.Helper()
	t.Setenv("SMTP_HOST", host)
	t.Setenv("SMTP_PORT", strconv.Itoa(port))
	t.Setenv("SMTP_USER", "ut@example.test")
	t.Setenv("SMTP_PASS", "ut-pass-not-real")
	t.Setenv("SMTP_FROM", "ut@example.test")
}
