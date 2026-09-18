// §八-7 零测试包最小单测（2026-09-18）：notify 触达层的四条不变式——
//  1. 日志侧邮箱永远脱敏（退化输入统一 ***，完整地址不得落日志）；
//  2. RFC5322 报文组装的中文标题/正文必须 base64、头与正文以 \r\n 空行分隔
//     （乱码=租户收到的重置码邮件不可读，属触达链路可用性红线）；
//  3. DefaultResetSender 的降级方向：smtp 声明但缺 SMTP_HOST/SMTP_USER → log（不得拿空凭据去连）；
//  4. 群通知文案（金额分→¥、空意向车型不出"意向车型"行）——留资/待确认收款是销售群唯一的触发信号。
//
// 为什么钉住：本包此前零测试，而闸门类降级（3）与文案（4）都是"错一次就静默丢触达"的路径。
package notify

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/testutil"
)

// TestMain 统一出口：本包若有 DB 用例被 testutil 跳过，收尾把跳过条数打到 stderr（防静默绿）
func TestMain(m *testing.M) {
	os.Exit(testutil.RunMain(m))
}

// TestMaskEmailForLog 日志侧邮箱脱敏：正常地址保留首字符+域名，退化输入一律 ***
func TestMaskEmailForLog(t *testing.T) {
	cases := map[string]string{
		"alice@example.com": "a***@example.com",
		"bob@x.io":          "b***@x.io",
		"notanemail":        "***", // 无 @：不可识别即全掩，绝不回显原文
		"@nolatent.io":      "***", // @ 在首位：at<=0 同样退化
		"@":                 "***",
		"":                  "***",
		"   ":               "***",
	}
	for in, want := range cases {
		if got := MaskEmailForLog(in); got != want {
			t.Errorf("MaskEmailForLog(%q)=%q want %q", in, got, want)
		}
	}
	// 方向性断言：完整地址的本地域不得出现在脱敏结果里
	if strings.Contains(MaskEmailForLog("alice@example.com"), "alice") {
		t.Errorf("脱敏结果泄露完整本地域: %q", MaskEmailForLog("alice@example.com"))
	}
}

// TestBuildMailMessage 报文组装：标题 encoded-word + 正文 base64 + \r\n 头/空行分隔
func TestBuildMailMessage(t *testing.T) {
	const (
		from    = "svc@example.com"
		to      = "user@example.com"
		subject = "跨山 LexCross 密码重置验证码"
		body    = "验证码：123456\n10 分钟内有效。"
	)
	msg := string(buildMailMessage(from, to, subject, body))

	for _, must := range []string{
		"From: " + from + "\r\n",
		"To: " + to + "\r\n",
		"Subject: =?UTF-8?B?",
		"Content-Transfer-Encoding: base64",
		"MIME-Version: 1.0",
		"\r\n\r\n", // 头与正文之间的空行
	} {
		if !strings.Contains(msg, must) {
			t.Errorf("报文缺少必要片段 %q，实际=%q", must, msg)
		}
	}
	// 逐个头都以 \r\n 结尾：头区块恰好 6 行，末尾接一个空行 → \r\n\r\n 只出现一次
	if n := strings.Count(msg, "\r\n\r\n"); n != 1 {
		t.Errorf("头/正文分隔应只出现一次空行，实际 %d 次: %q", n, msg)
	}

	// 标题与正文都能原样解回（乱码防线）
	headAndBody := strings.SplitN(msg, "\r\n\r\n", 2)
	headerBlock, bodyPart := headAndBody[0], headAndBody[1]
	if bodyPart != base64Std(body) {
		t.Errorf("正文未整体 base64：实际=%q", bodyPart)
	}
	decoded, err := base64.StdEncoding.DecodeString(bodyPart)
	if err != nil || string(decoded) != body {
		t.Fatalf("正文 base64 回环失败: err=%v got=%q", err, decoded)
	}
	for _, line := range strings.Split(headerBlock, "\r\n") {
		if !strings.HasPrefix(line, "Subject: =?UTF-8?B?") {
			continue
		}
		b64 := strings.TrimSuffix(strings.TrimPrefix(line, "Subject: =?UTF-8?B?"), "?=")
		got, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || string(got) != subject {
			t.Fatalf("中文标题 encoded-word 回环失败: err=%v got=%q", err, got)
		}
	}
	// 明文正文/标题绝不允许以未编码形态出现在报文里
	if strings.Contains(msg, body) {
		t.Errorf("正文以明文出现在报文中（未 base64）")
	}
}

// TestDefaultResetSenderFallback 通道解析方向：smtp 缺凭据必须降级 log，log/未配置走 log
func TestDefaultResetSender(t *testing.T) {
	// 1) reset_code_channel=smtp 但 SMTP_HOST 缺失 → LogSender
	restore := runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(
		map[string]string{"reset_code_channel": "smtp"}, nil))
	defer restore()
	t.Setenv("SMTP_HOST", "")
	t.Setenv("SMTP_USER", "someone@example.com")
	if _, ok := DefaultResetSender().(LogSender); !ok {
		t.Errorf("smtp 缺 SMTP_HOST 应降级 LogSender，实际 %T", DefaultResetSender())
	}

	// 2) smtp + 缺 SMTP_USER → 同样降级（空凭据发不出去，宁走 log 也不炸主链路）
	t.Setenv("SMTP_HOST", "smtp.example.com")
	t.Setenv("SMTP_USER", "")
	if _, ok := DefaultResetSender().(LogSender); !ok {
		t.Errorf("smtp 缺 SMTP_USER 应降级 LogSender，实际 %T", DefaultResetSender())
	}

	// 3) smtp + 双凭据齐 → SMTPSender（真实通道）
	t.Setenv("SMTP_HOST", "smtp.example.com")
	t.Setenv("SMTP_USER", "someone@example.com")
	t.Setenv("SMTP_PORT", "465")
	s := DefaultResetSender()
	smtp, ok := s.(*SMTPSender)
	if !ok {
		t.Fatalf("smtp 凭据齐全应返回 *SMTPSender，实际 %T", s)
	}
	if smtp.Port != 465 || smtp.From != "someone@example.com" {
		t.Errorf("SMTPSender 装配异常: port=%d from=%q（465 应为隐式TLS、From 缺省回落 User）", smtp.Port, smtp.From)
	}

	// 4) log 通道 + 未配置（DefaultSystemConfigService=nil）→ LogSender，不 panic
	restore2 := runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(
		map[string]string{"reset_code_channel": "log"}, nil))
	if _, ok := DefaultResetSender().(LogSender); !ok {
		t.Errorf("channel=log 应返回 LogSender，实际 %T", DefaultResetSender())
	}
	restore2()
	restoreNil := runtimecfg.SetDefaultForTest(nil)
	if _, ok := DefaultResetSender().(LogSender); !ok {
		t.Errorf("配置服务未初始化时应默认 LogSender，实际 %T", DefaultResetSender())
	}
	restoreNil()
}

// ---- 群通知文案（企微/钉钉双通道的 POST body 回环） ----

// postSink 把某个 webhook 配置键指向 httptest 假接收端，捕获收到的 POST body。
// 只填 wanted 键（另一通道 URL 留空=静默跳过），断言更聚焦。
type postSink struct {
	url   string
	calls chan string
}

func newPostSink(t *testing.T, key string) *postSink {
	t.Helper()
	calls := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls <- string(b)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	old := httpClient
	httpClient = srv.Client()
	t.Cleanup(func() { httpClient = old })
	restore := runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(
		map[string]string{key: srv.URL}, nil))
	t.Cleanup(restore)
	return &postSink{url: srv.URL, calls: calls}
}

// take 取一条推送体（Notify* 为异步 goroutine，最多等 2s）
func (p *postSink) take(t *testing.T) string {
	t.Helper()
	select {
	case b := <-p.calls:
		return b
	case <-time.After(2 * time.Second):
		t.Fatalf("2s 内未收到群推送（异步投递链路断了？）")
		return ""
	}
}

// contentOf 解出企微 markdown 消息正文
func contentOf(t *testing.T, raw string) string {
	t.Helper()
	var req wecomReq
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("企微消息体非合法 JSON: %v raw=%q", err, raw)
	}
	if req.MsgType != "markdown" {
		t.Errorf("msgtype 应为 markdown，实际 %q", req.MsgType)
	}
	return req.Markdown.Content
}

// TestNotifyLeadCapturedContent 留资线索文案：意向车型为空时不得出现该
func TestNotifyLeadCapturedContent(t *testing.T) {
	sink := newPostSink(t, "wecom_webhook_url")

	NotifyLeadCaptured("张三", "138****1111", "越野版")
	got := contentOf(t, sink.take(t))
	for _, must := range []string{"【新留资线索】", "客户：张三", "手机：138****1111", "意向车型：越野版"} {
		if !strings.Contains(got, must) {
			t.Errorf("留资文案缺片段 %q，实际=%q", must, got)
		}
	}

	// 泛行业化：非车企场景 interestModel 为空，"意向车型"整行不得出现（残留会误导销售）
	NotifyLeadCaptured("李四", "139****2222", "")
	got2 := contentOf(t, sink.take(t))
	if strings.Contains(got2, "意向车型") {
		t.Errorf("空意向车型不应输出\"意向车型\"行，实际=%q", got2)
	}
	if !strings.Contains(got2, "客户：李四") {
		t.Errorf("空意向车型时正文缺客户行，实际=%q", got2)
	}
}

// TestNotifyManualConfirmPaidContent 待确认收款文案：金额分→元两位小数（资金口径）
func TestNotifyManualConfirmPaidContent(t *testing.T) {
	sink := newPostSink(t, "wecom_webhook_url")

	NotifyManualConfirmPaid("SO20260918001", "极石汽车", 12345)
	got := contentOf(t, sink.take(t))
	for _, must := range []string{"【待确认收款】", "租户「极石汽车」", "SO20260918001", "¥123.45"} {
		if !strings.Contains(got, must) {
			t.Errorf("收款文案缺片段 %q，实际=%q", must, got)
		}
	}
	if strings.Contains(got, "¥12345") {
		t.Errorf("金额未按分→元换算，实际=%q", got)
	}
}

// TestNotifyGroupSkipsWhenUnconfigured 双通道均未配置=静默跳过（不打扰主链路、不 panic）
func TestNotifyGroupSkipsWhenUnconfigured(t *testing.T) {
	restore := runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(nil, nil))
	defer restore()
	old := httpClient
	httpClient = &http.Client{Timeout: 100 * time.Millisecond}
	defer func() { httpClient = old }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		NotifyGroup("无配置场景") // 不应发起任何 HTTP
	}()
	<-done
}
