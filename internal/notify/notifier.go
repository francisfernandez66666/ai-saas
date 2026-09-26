// 通知触达：重置码/验证码邮件通道（log/SMTP）+ 企微/钉钉群机器人推送。
package notify

import "ai-scrm/internal/pii"

import (
	"ai-scrm/internal/runtimecfg"
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// maskEmail 邮箱脱敏打日志（防完整地址落日志）
func maskEmail(email string) string {
	at := strings.Index(email, "@")
	if at <= 0 {
		return "***"
	}
	return email[:1] + "***" + email[at:]
}

// MaskEmailForLog 服务端日志专用邮箱脱敏：保留首字符和域名，避免完整地址落日志。
func MaskEmailForLog(email string) string {
	return maskEmail(email)
}

// ============================================================
// 通知触达抽象（商业化第一批，2026-08-23）
//
// 设计（对齐实施文档 §三 Sender 抽象 + §七 触达）：
//   1. ResetCodeSender 重置码发送通道：本期 LogSender 打日志（开发可用），
//      批次三填 SMTP 密钥后换 SMTPSender 即启用邮件，业务代码零改动
//   2. NotifyWecom 企微群机器人：http post 即可接入，留资线索/人工确认订单/
//      到期提醒三类高价值事件推送销售群；URL 未配置时静默跳过
//
// 敏感配置约定：wecom_webhook_url 入 system_configs 系统层(tenant_id=0)，不入 git
// ============================================================

// ResetCodeSender 邮件发送通道接口（M3 重置码 + M邮箱验证码共用）
type ResetCodeSender interface {
	SendResetCode(to string, code string) error
	SendRaw(to []string, subject, body string) error // 通用邮件（验证码内容按用途组装）
}

// LogSender 日志通道：邮件打到服务端日志（reset_code_channel=log 默认）
// 注意：log 通道明文落日志 + 水位标记（P1-20 修复），内测/开发环境可用
type LogSender struct{}

// SendResetCode 实现：打日志
// P1-20 修复(2026-09-09)：统一 log 通道语义为"明文落日志 + 水位标记"（内测可用）。
// 原实现把码打码（与 EmailCodeService.log 通道同样自相矛盾——用户拿不到码），
// 且同文件 SendRaw 又明文打印 body——一处过度脱敏致不可用、一处明文泄露。
// 现统一：log 通道明码 + [DEBUG-WATERMARK 仅log通道] 标记；smtp 通道走真实邮件不受影响。
func (LogSender) SendResetCode(to string, code string) error {
	log.Printf("[重置码][DEBUG-WATERMARK 仅log通道] 账号=%s 验证码=%s (10分钟内有效,一次性,生产请配置SMTP)", pii.MaskEmail(pii.MaskPhoneInText(to)), code)
	return nil
}

// SendRaw 实现：打日志
func (LogSender) SendRaw(to []string, subject, body string) error {
	// body 为外发邮件内容（可能含验证码），仅 log 通道开发态可见；此处脱敏收件人
	log.Printf("[邮件-log通道] to=%s subject=%s body=%s", pii.MaskEmail(pii.MaskPhoneInText(strings.Join(to, ","))), subject, strings.ReplaceAll(body, "\n", " | "))
	return nil
}

// sendToLogChannel 把"只能落日志"的邮件正文集中到这一个函数里打。
//
// 为什么再包一层、不让调用方直接用 LogSender{}.SendRaw：LogSender.SendRaw **恒返回 nil**，
// 也就是"永远成功"。它作为 ResetCodeSender 的实现是对的（通道未就绪时开发态确实要能拿到码），
// 但一旦外呼方图省事写 `if err := sender.SendRaw(...); err != nil {...}`，
// 拿到的就是"发送成功"——这正是本仓反复根除的"只会打日志并返回成功的守卫等于没有守卫"。
// 本函数不返回 error，签名上就**不给任何人把它当投递成功依据**的机会；
// 谁要判投递结果，必须回到通道选择那一层去如实记（见 invoice_notify.go 的 log_only 档）。
func sendToLogChannel(to []string, subject, body string) {
	_ = LogSender{}.SendRaw(to, subject, body)
}

// SMTPSender SMTP邮件通道（2026-08-23 代码就绪，填环境变量即启用）
// 环境变量：SMTP_HOST / SMTP_PORT / SMTP_USER / SMTP_PASS / SMTP_FROM(缺省用SMTP_USER)
// 批次三只需在 .env 填密钥 + system_config 设 reset_code_channel=smtp，零代码切换
type SMTPSender struct {
	Host string
	Port int
	User string
	Pass string
	From string
}

// NewSMTPSenderFromEnv 从环境变量装配；Host 为空表示未配置
// From 的取值优先级：SMTP_FROM（发件人名义地址）> SMTP_USER（登录账号）。
// 企业邮箱常要求"登录账号 ≠ 对外发件地址"，两者必须能分开配。
func NewSMTPSenderFromEnv() *SMTPSender {
	port, _ := strconv.Atoi(os.Getenv("SMTP_PORT"))
	if port <= 0 {
		port = 587 // STARTTLS 惯例端口
	}
	from := os.Getenv("SMTP_FROM")
	if from == "" {
		from = os.Getenv("SMTP_USER")
	}
	return &SMTPSender{
		Host: os.Getenv("SMTP_HOST"),
		Port: port,
		User: os.Getenv("SMTP_USER"),
		Pass: os.Getenv("SMTP_PASS"),
		From: from,
	}
}

// SendResetCode 实现：发验证码邮件
// 双通道：587 STARTTLS（自实现会话，带时间墙）/ 465 隐式TLS（先握手再走同一场会话）
// ⚠ 587 一支在 2026-09-27 之前是 smtp.SendMail，那条 API 无法设任何超时；
// 现在改由 runSMTPSession 自己持有连接，才拿得到 SetDeadline 的控制权（详见 smtpSessionTimeout 注释）。
func (s *SMTPSender) SendResetCode(to string, code string) error {
	subject := "跨山 LexCross 密码重置验证码"
	body := fmt.Sprintf(
		"您正在重置跨山 LexCross 账号密码。\n\n验证码：%s\n\n10 分钟内有效，仅可使用一次。若非本人操作请忽略本邮件。",
		code)
	return sendMailTLS(s, []string{to}, subject, body)
}

// SendRaw 通用邮件发送（注册验证码/绑定邮箱验证码）
func (s *SMTPSender) SendRaw(to []string, subject, body string) error {
	return sendMailTLS(s, to, subject, body)
}

// SMTP 外呼的时间预算（FIX-9 发票触达批，2026-09-27）。
//
// 为什么要单独管：旧实现 465 走 tls.Dial、587 走 smtp.SendMail，**两条路都没有任何超时**。
// tcp.Dial 的默认超时来自 OS（Linux 上是 SYN 重试那么久，几十秒起步），而连上之后
// 一个不回复的服务器能让 Read 挂到进程结束——落在这个调用点上的 HTTP 请求就永远不返回。
// 此前这条链只在"重置码/验证码"里同步调用，没人把它当问题；FIX-9 要在超管点「开具」时
// 同步给客户发一封交付邮件并**如实记录结果码**，一旦挂住，"结果码"这个字段就永远停在上一次的值，
// 于是"发不出去"又变回只有日志知道的静默故障——正是本批一直在根除的那一类。
// 所以这里给拨号与整场会话各一道墙：拨号 8s（够跨区 DNS+TCP+TLS），整场 15s（覆盖 AUTH+MAIL+RCPT+DATA）。
// 会话截止用 SetDeadline 挂在裸 conn 上：TLS 读写都从它过，因此 STARTTLS 升级后的会话同样受管。
//
// 两个值是 var 而不是 const：唯一区别是**超时这一档因此可测**。
// 写成 const 15s 时，"服务器连上了但一言不发"这条分支要吃满 15 秒才出结果，
// 没有单测会在合理时间内覆盖它——于是一个只会打日志并返回成功的守卫换个形态回来了：
// 分支存在、没人验过、真出事时才第一次跑。单测里临时调小即可确定性触发（见 invoice_notify_test.go）。
var (
	smtpDialTimeout    = 8 * time.Second
	smtpSessionTimeout = 15 * time.Second
)

// sendMailTLS 统一发送入口：按端口自动选择 465 隐式 TLS 或 587 STARTTLS
func sendMailTLS(s *SMTPSender, to []string, subject, body string) error {
	// JoinHostPort 而不是 "%s:%d"：SMTP_HOST 可以是 IPv6 字面量（::1 或 fd00::1），
	// 裸拼出来的 "::1:465" 是坏地址（vet 亦按此判），而 SMTPSender 的调用点全在 SMTPSender.SendRaw 之后，
	// 一旦配了 IPv6 就是所有外发邮件 100% 失败且只在日志里留一行"连接失败"。
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	msg := buildMailMessage(s.From, to[0], subject, body)
	auth := smtp.PlainAuth("", s.User, s.Pass, s.Host)

	d := net.Dialer{Timeout: smtpDialTimeout}
	conn, err := d.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("SMTP连接失败: %w", err)
	}
	// 整场会话一个绝对截止：拨号已完成，从这里起 TLS 握手、逐条命令、正文传输共用这 15s。
	if err = conn.SetDeadline(time.Now().Add(smtpSessionTimeout)); err != nil {
		_ = conn.Close()
		return fmt.Errorf("设置发送超时失败: %w", err)
	}
	defer conn.Close()

	if s.Port == 465 {
		// 465 隐式TLS：先建立TLS连接再走SMTP会话（net/smtp.SendMail 不支持此模式）
		tconn := tls.Client(conn, &tls.Config{ServerName: s.Host})
		if err = tconn.Handshake(); err != nil {
			return fmt.Errorf("TLS连接失败: %w", err)
		}
		return runSMTPSession(tconn, s, to, msg, auth, false)
	}

	// 587/25：明文起连，会话内 STARTTLS 自动升级（与 smtp.SendMail 内部动作一致，
	// 只是换成自己持有 conn，才拿得到上面那道 SetDeadline 的控制权）
	return runSMTPSession(conn, s, to, msg, auth, true)
}

// runSMTPSession 走完一场 SMTP 会话（AUTH→MAIL→RCPT→DATA→QUIT）。
// startTLS=true 时先探测服务器是否支持 STARTTLS：支持则升级，**不支持则如实报错而不是明文发出去**
// ——旧路径 smtp.SendMail 在服务器不声明 STARTTLS 时同样拒发（"server refused STARTTLS"），
// 这里保持同一个判定，避免"改成自实现顺手放宽了加密要求"这种隐性安全回归。
func runSMTPSession(conn net.Conn, s *SMTPSender, to []string, msg []byte, auth smtp.Auth, startTLS bool) error {
	cli, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return fmt.Errorf("SMTP会话失败: %w", err)
	}
	defer cli.Close()
	if startTLS {
		if ok, _ := cli.Extension("STARTTLS"); !ok {
			return fmt.Errorf("服务器不支持 STARTTLS，拒绝明文发送")
		}
		if err = cli.StartTLS(&tls.Config{ServerName: s.Host}); err != nil {
			return fmt.Errorf("STARTTLS失败: %w", err)
		}
	}
	if err = cli.Auth(auth); err != nil {
		return fmt.Errorf("认证失败: %w", err)
	}
	if err = cli.Mail(s.From); err != nil {
		return fmt.Errorf("设置发件人失败: %w", err)
	}
	for _, rcpt := range to {
		if err = cli.Rcpt(rcpt); err != nil {
			return fmt.Errorf("收件人被拒(%s): %w", maskEmail(rcpt), err)
		}
	}
	w, err := cli.Data()
	if err != nil {
		return fmt.Errorf("写入正文失败: %w", err)
	}
	if _, err = w.Write(msg); err != nil {
		return fmt.Errorf("传输失败: %w", err)
	}
	if err = w.Close(); err != nil {
		return fmt.Errorf("结束数据失败: %w", err)
	}
	return cli.Quit()
}

// IsTimeoutErr 判定一个错误是不是"超时导致"（FIX-9 触达结果码要区分 send_failed 与 send_timeout）。
// 必须顺着错误链找：拨号超时包在 *net.OpError 里、会话截止（SetDeadline 触发）被 fmt.Errorf
// 的 %w 包在更外层，只看最外层字符串会把"连不上"和"发不出去"混成一类——
// 而这两类在运维上是完全不同的动作（前者多半是网络/防火墙，后者是服务器不回复）。
func IsTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	// 拨号超时与会话截止（SetDeadline 触发的 os.ErrDeadlineExceeded）都会以 net.Error
	// 出现在错误链上，errors.As 会穿过 fmt.Errorf 的 %w 包装找到它。
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// buildMailMessage 组装 RFC 5322 报文（Subject/Base64 处理中文标题乱码）
func buildMailMessage(from, to, subject, body string) []byte {
	headers := map[string]string{
		"From":                      from,
		"To":                        to,
		"Subject":                   "=?" + "UTF-8" + "?B?" + base64Std(subject) + "?=",
		"MIME-Version":              "1.0",
		"Content-Type":              `text/plain; charset="UTF-8"`,
		"Content-Transfer-Encoding": "base64",
	}
	var buf bytes.Buffer
	for k, v := range headers {
		buf.WriteString(k + ": " + v + "\r\n")
	}
	buf.WriteString("\r\n" + base64Std(body))
	return buf.Bytes()
}

// base64Std RFC2045 Base64 编码（邮件主题/正文中文乱码防护）
func base64Std(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// smtpConfigured 判断 SMTP 是否真的可用（Host+User 齐备）。
// 抽出来的原因：通道"配置值"与"实际生效值"可能不一致——smtp 配了但环境变量缺失会降级 log，
// 前端文案与身份校验必须按实际生效值走，否则会对用户承诺"邮件已发送"而码其实只落在日志里（S2）。
func smtpConfigured() bool {
	s := NewSMTPSenderFromEnv()
	return s.Host != "" && s.User != ""
}

// ResetSenderKind 返回**实际生效**的重置码通道："smtp" 或 "log"（S2，2026-09-23）。
// 与直接读配置的区别：配置写 smtp 但 SMTP 环境变量缺失时，DefaultResetSender 会降级 log，
// 本函数如实报 log——调用方据此决定文案与"是否需要 contact 自证"。
func ResetSenderKind() string {
	channel := "log"
	if runtimecfg.DefaultSystemConfigService != nil {
		channel = runtimecfg.DefaultSystemConfigService.GetString("reset_code_channel", "log")
	}
	if channel == "smtp" && smtpConfigured() {
		return "smtp"
	}
	return "log"
}

// DefaultResetSender 当前生效的发送通道（按 reset_code_channel 配置解析）
func DefaultResetSender() ResetCodeSender {
	channel := "log"
	if runtimecfg.DefaultSystemConfigService != nil {
		channel = runtimecfg.DefaultSystemConfigService.GetString("reset_code_channel", "log")
	}
	switch channel {
	case "smtp":
		s := NewSMTPSenderFromEnv()
		if s.Host == "" || s.User == "" {
			log.Printf("[重置码] reset_code_channel=smtp 但未配置 SMTP_HOST/SMTP_USER 环境变量，降级 log 通道")
			return LogSender{}
		}
		return s
	default:
		return LogSender{}
	}
}

// ---- 企微群机器人 webhook ----

// httpClient 企微 webhook 推送专用 HTTP 客户端（5s 超时，失败仅告警不阻塞业务）
var httpClient = &http.Client{Timeout: 5 * time.Second}

// wecomReq 企微机器人 markdown 消息体
type wecomReq struct {
	MsgType  string        `json:"msgtype"`
	Markdown wecomMarkdown `json:"markdown"`
}

// wecomMarkdown 企微 markdown 消息正文（content 为 markdown 文本）
type wecomMarkdown struct {
	Content string `json:"content"`
}

// NotifyWecom 推送文本到企微群（webhook 未配置时静默跳过；失败仅告警不阻断业务）
func NotifyWecom(content string) {
	if runtimecfg.DefaultSystemConfigService == nil {
		return
	}
	url := runtimecfg.DefaultSystemConfigService.GetString("wecom_webhook_url", "")
	if url == "" {
		return // 未配置=功能关闭，不打扰主链路
	}
	go func() {
		defer func() { _ = recover() }()
		body, _ := json.Marshal(wecomReq{MsgType: "markdown", Markdown: wecomMarkdown{Content: content}})
		resp, err := httpClient.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			log.Printf("[Wecom] 推送失败: %v", err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			log.Printf("[Wecom] 推送响应异常: status=%d", resp.StatusCode)
		}
	}()
}

// dingtalkReq 钉钉机器人 markdown 消息体（格式与企微不同：title+text 双字段）
// dingtalkReq 钉钉机器人消息体（msgtype 固定 markdown）
type dingtalkReq struct {
	MsgType  string           `json:"msgtype"`
	Markdown dingtalkMarkdown `json:"markdown"`
}

// dingtalkMarkdown 钉钉 markdown 内容（title+text 双字段，与企微格式不同）
type dingtalkMarkdown struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

// NotifyDingtalk 推送文本到钉钉群（M4 双通道补齐；URL 未配置静默跳过）
func NotifyDingtalk(content string) {
	if runtimecfg.DefaultSystemConfigService == nil {
		return
	}
	url := runtimecfg.DefaultSystemConfigService.GetString("dingtalk_webhook_url", "")
	if url == "" {
		return
	}
	go func() {
		defer func() { _ = recover() }()
		body, _ := json.Marshal(dingtalkReq{MsgType: "markdown",
			Markdown: dingtalkMarkdown{Title: "跨山 LexCross 通知", Text: content}})
		resp, err := httpClient.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			log.Printf("[Dingtalk] 推送失败: %v", err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			log.Printf("[Dingtalk] 推送响应异常: status=%d", resp.StatusCode)
		}
	}()
}

// NotifyGroup 群通知统一入口：企微+钉钉双通道同时投递（业务侧只调这一个）
func NotifyGroup(content string) {
	NotifyWecom(content)
	NotifyDingtalk(content)
}

// NotifyManualConfirmPaid 「我已付费」告警推送（critical 级，催超管人工确认到账）
func NotifyManualConfirmPaid(orderNo, tenantName string, amountCents int) {
	NotifyGroup(fmt.Sprintf("【待确认收款】租户「%s」已提交付费凭证\n订单：%s\n金额：¥%.2f\n请尽快在超管后台核实确认",
		tenantName, orderNo, float64(amountCents)/100))
}

// NotifyLeadCaptured 新留资线索推送（SCRM 最高价值触达，批次三提到第一批）
func NotifyLeadCaptured(customerName, phoneMasked, interestModel string) {
	msg := fmt.Sprintf("【新留资线索】客户：%s\n手机：%s", customerName, phoneMasked)
	if interestModel != "" {
		msg += fmt.Sprintf("\n意向车型：%s", interestModel)
	}
	NotifyGroup(msg)
}

// MaskEmailAddr 邮箱脱敏（对外展示用）：委托集中工具 MaskEmail，保持行为一致
// emailRe 文本级邮箱识别（仅 ASCII 本地域，覆盖绝大多数 PII 邮箱）
var emailRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// MaskEmailAddr 文本级邮箱脱敏：仅替换其中邮箱子串，保留手机/中文等其它内容
func MaskEmailAddr(s string) string {
	return emailRe.ReplaceAllStringFunc(s, pii.MaskEmail)
}
