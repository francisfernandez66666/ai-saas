// 生产就绪探针（Readiness，2026-09-15 价值增强批）
// 背景：registration_review / contentsafety_mode / pay_mode / TRUSTED_PROXIES 这类开关
// 属"上线忘开=烧钱/合规裸奔"型配置，靠 DEPLOY_CHECKLIST.md 人肉勾选迟早漏。
// 本文件把检查单代码化：/status 返回 readiness 分组，运维一眼看到哪项没配好。
// 设计口径：
//   - 仅 release 模式逐项评估（debug/本地不评，避免红灯噪音，与 G6 instance_coordination 同款门控）；
//   - crit=资金/合规洞（注册审核未开）；warn=需人工确认的部署项（反代未声明/内容安全仍 shadow 等）；
//   - 只作展示与冒烟断言，不并入 MaybeAlert 群通知——配置问题重启前会一直红，刷群无意义。
package metrics

import (
	"os"
	"strings"

	"ai-scrm/internal/runtimecfg"
)

// readinessCheck 单条就绪检查构造助手：ok 之外的都带整改建议文案
func readinessCheck(name string, pass bool, value string, level HealthStatus, hint string) HealthCheck {
	if pass {
		return HealthCheck{Name: name, Status: StatusOK, Value: value, WarnAt: "-", CritAt: "-", Desc: name + " 就绪"}
	}
	desc := hint
	if value == "" {
		value = "unset"
	}
	return HealthCheck{Name: name, Status: level, Value: value, WarnAt: "-", CritAt: "-", Desc: desc}
}

// ComputeReadiness 生产就绪检查清单。
// 返回按严重度排序的检查项；debug 模式仅返回一条 skipped。
func ComputeReadiness() []HealthCheck {
	if !deployIsRelease {
		return []HealthCheck{{
			Name: "skipped", Status: StatusOK, Value: "debug", WarnAt: "-", CritAt: "-",
			Desc: "非 release 模式不评估生产就绪项（本地/联调免打扰）",
		}}
	}
	cfg := runtimecfg.DefaultSystemConfigService
	checks := []HealthCheck{}

	// R1 注册审核开关：出厂 false=注册即送真实 AI 额度（防薅红线），生产必开
	reviewOn := false
	if cfg != nil {
		reviewOn = cfg.GetBoolForTenant(0, "registration_review", false)
	}
	checks = append(checks, readinessCheck("registration_review", reviewOn,
		map[bool]string{true: "true", false: "false"}[reviewOn], StatusCrit,
		"注册审核未开启：新租户注册即白烧真实 AI 额度，请开 system_config registration_review=true 或确认已有其他防薅措施"))

	// R2 内容安全模式：enabled=false 直接 crit（闸门全关）；shadow 属演练期，warn 提醒收口
	csEnabled := true
	csMode := "shadow"
	if cfg != nil {
		csEnabled = cfg.GetBoolForTenant(0, "contentsafety_enabled", true)
		csMode = cfg.GetStringForTenant(0, "contentsafety_mode", "shadow")
	}
	if !csEnabled {
		checks = append(checks, readinessCheck("contentsafety", false, "disabled", StatusCrit,
			"AI 出站内容安全闸门已关闭：PIPL/广告法风险敞口，请开 contentsafety_enabled 并演练后切 enforce"))
	} else {
		checks = append(checks, readinessCheck("contentsafety", csMode == "enforce", csMode, StatusWarn,
			"内容安全仍为 shadow（只观察不拦截）：首周演练误杀率后请切 contentsafety_mode=enforce"))
	}

	// R3 收款模式：mock=模拟到账；release+ALLOW_MOCK_PAY=true 时 0 元白嫖洞，warn 提醒收口
	payMode := "mock"
	if cfg != nil {
		payMode = cfg.GetStringForTenant(0, "pay_mode", "mock")
	}
	checks = append(checks, readinessCheck("pay_mode", payMode == "sdk", payMode, StatusWarn,
		"pay_mode 非 sdk：到账依赖模拟/人工确认，正式收款前请配 pay_provider+商户凭据并切 pay_mode=sdk"))
	allowMockPay := strings.EqualFold(os.Getenv("ALLOW_MOCK_PAY"), "true")
	checks = append(checks, readinessCheck("allow_mock_pay", !allowMockPay,
		map[bool]string{true: "true", false: "false"}[allowMockPay], StatusCrit,
		"ALLOW_MOCK_PAY=true 且 release：mock-pay 模拟到账端点对 admin 开放=0 元白嫖洞，请移除该环境变量"))

	// R4 反向代理声明：未配置=不信任任何 XFF（直连安全），但反代部署漏配会让限流按代理 IP 聚合
	tp := strings.TrimSpace(os.Getenv("TRUSTED_PROXIES"))
	checks = append(checks, readinessCheck("trusted_proxies", tp != "", tp, StatusWarn,
		"TRUSTED_PROXIES 未配置：直连部署无碍；经 Nginx/网关部署必配，否则防爆破/注册限流按代理 IP 聚合失效"))

	// R5 真实 AI：release+AI_MOCK_MODE=true 全站假回复
	aiMock := strings.EqualFold(os.Getenv("AI_MOCK_MODE"), "true")
	checks = append(checks, readinessCheck("ai_real_call", !aiMock,
		map[bool]string{true: "mock", false: "real"}[aiMock], StatusCrit,
		"AI_MOCK_MODE=true 且 release：全站回复为模拟话术，请配真实供应商 Key 并关闭 mock"))

	// R6 告警/通知触达：企微群 webhook 与 SMTP 缺失意味着 crit 告警发不出去
	wecomURL := ""
	if cfg != nil {
		wecomURL = cfg.GetStringForTenant(0, "wecom_webhook_url", "")
	}
	checks = append(checks, readinessCheck("alert_channel", wecomURL != "", map[bool]string{true: "wecom_configured", false: ""}[wecomURL != ""], StatusWarn,
		"wecom_webhook_url 未配置：健康 crit/死信等告警无触达渠道（仅 /status 可见）"))
	smtpReady := strings.TrimSpace(os.Getenv("SMTP_HOST")) != "" && strings.TrimSpace(os.Getenv("SMTP_USER")) != ""
	emailVerifyOn := false
	if cfg != nil {
		emailVerifyOn = cfg.GetBoolForTenant(0, "email_verify_enabled", false)
	}
	checks = append(checks, readinessCheck("smtp", smtpReady || !emailVerifyOn,
		map[bool]string{true: "configured", false: "missing"}[smtpReady || !emailVerifyOn], StatusWarn,
		"email_verify_enabled=true 但 SMTP 未配置：注册/重置密码验证码发不出去"))

	return checks
}

// ReadinessSummary 汇总就绪结论：ready=无 crit；worst=最严重级别名
func ReadinessSummary(checks []HealthCheck) (ready bool, crit int, warn int) {
	for _, c := range checks {
		if c.Status == StatusCrit {
			crit++
		}
		if c.Status == StatusWarn {
			warn++
		}
	}
	return crit == 0, crit, warn
}
