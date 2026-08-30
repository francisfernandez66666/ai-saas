// 通知触达：重置码/验证码邮件通道（log/SMTP）+ 企微/钉钉群机器人推送。
package service

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
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
// 注意：log 模式等于"知道用户名即可看到码"，故 reset 接口额外要求提供注册时的
// 手机号/邮箱匹配才发码；注册验证码场景由 email_verify_enabled 开关独立控制
type LogSender struct{}

// SendResetCode 实现：打日志
func (LogSender) SendResetCode(to string, code string) error {
	log.Printf("[重置码] 账号=%s 验证码=***(已脱敏,10分钟内有效,一次性)", MaskEmail(MaskPhoneInText(to)))
	return nil
}

// SendRaw 实现：打日志
func (LogSender) SendRaw(to []string, subject, body string) error {
	// body 为外发邮件内容（可能含验证码），仅 log 通道开发态可见；此处脱敏收件人
	log.Printf("[邮件-log通道] to=%s subject=%s body=%s", MaskEmail(MaskPhoneInText(strings.Join(to, ","))), subject, strings.ReplaceAll(body, "\n", " | "))
	return nil
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
// 双通道：587 STARTTLS（net/smtp 自动升级）/ 465 隐式TLS（tls.Dial 先握手）
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

// sendMailTLS 统一发送入口：按端口自动选择 465 隐式 TLS 或 587 STARTTLS
func sendMailTLS(s *SMTPSender, to []string, subject, body string) error {
	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)
	msg := buildMailMessage(s.From, to[0], subject, body)
	auth := smtp.PlainAuth("", s.User, s.Pass, s.Host)

	if s.Port == 465 {
		// 465 隐式TLS：先建立TLS连接再走SMTP会话（net/smtp.SendMail 不支持此模式）
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: s.Host})
		if err != nil {
			return fmt.Errorf("TLS连接失败: %w", err)
		}
		defer conn.Close()
		cli, err := smtp.NewClient(conn, s.Host)
		if err != nil {
			return fmt.Errorf("SMTP会话失败: %w", err)
		}
		defer cli.Close()
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

	// 587/25：STARTTLS 明文起连自动升级
	return smtp.SendMail(addr, auth, s.From, to, msg)
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

// DefaultResetSender 当前生效的发送通道（按 reset_code_channel 配置解析）
func DefaultResetSender() ResetCodeSender {
	channel := "log"
	if DefaultSystemConfigService != nil {
		channel = DefaultSystemConfigService.GetString("reset_code_channel", "log")
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
	if DefaultSystemConfigService == nil {
		return
	}
	url := DefaultSystemConfigService.GetString("wecom_webhook_url", "")
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
	if DefaultSystemConfigService == nil {
		return
	}
	url := DefaultSystemConfigService.GetString("dingtalk_webhook_url", "")
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
	return emailRe.ReplaceAllStringFunc(s, MaskEmail)
}
