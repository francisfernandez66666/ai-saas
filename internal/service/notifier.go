package service

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/smtp"
	"os"
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

// ResetCodeSender 重置密码验证码发送通道接口（M3）
type ResetCodeSender interface {
	SendResetCode(to string, code string) error
}

// LogSender 日志通道：验证码打到服务端日志（reset_code_channel=log 默认）
// 注意：log 模式等于"知道用户名即可看到码"，故 reset 接口额外要求提供注册时的
// 手机号/邮箱匹配才发码（见 api/auth_password.go 的 contact 校验）
type LogSender struct{}

// SendResetCode 实现：打日志
func (LogSender) SendResetCode(to string, code string) error {
	log.Printf("[重置码] 账号=%s 验证码=%s（10分钟内有效，一次性）", to, code)
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

// SendResetCode 实现：发验证码邮件（Plain AUTH + STARTTLS 语义由 net/smtp 处理）
func (s *SMTPSender) SendResetCode(to string, code string) error {
	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)
	subject := "AI-SCRM 密码重置验证码"
	body := fmt.Sprintf(
		"您正在重置 AI-SCRM 账号密码。\n\n验证码：%s\n\n10 分钟内有效，仅可使用一次。若非本人操作请忽略本邮件。",
		code)
	msg := buildMailMessage(s.From, to, subject, body)

	auth := smtp.PlainAuth("", s.User, s.Pass, s.Host)
	// 先尝试 STARTTLS（绝大多数服务商要求），失败回退明文（本地调试 mailhog 等）
	if err := smtp.SendMail(addr, auth, s.From, []string{to}, msg); err != nil {
		log.Printf("[重置码] SMTP 发送失败 to=%s: %v", maskEmail(to), err)
		return err
	}
	log.Printf("[重置码] 邮件已发送 to=%s", maskEmail(to))
	return nil
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
type dingtalkReq struct {
	MsgType  string          `json:"msgtype"`
	Markdown dingtalkMarkdown `json:"markdown"`
}

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
			Markdown: dingtalkMarkdown{Title: "AI-SCRM 通知", Text: content}})
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
