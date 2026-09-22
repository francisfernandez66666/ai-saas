// 生产就绪探针（Readiness，2026-09-15 价值增强批）
// 背景：registration_review / contentsafety_mode / pay_mode / TRUSTED_PROXIES 这类开关
// 属"上线忘开=烧钱/合规裸奔"型配置，靠 DEPLOY_CHECKLIST.md 人肉勾选迟早漏。
// 本文件把检查单代码化：/status 返回 readiness 分组，运维一眼看到哪项没配好。
// 设计口径：
//   - release 逐项评估并按 Crit/Warn 判级；非 release 同表评估但 Crit 降 Warn（2026-09-18 P0-1 批改，
//     旧"debug 整块不评"恰好在默认 fail-open 组合下自我关闭）；
//   - crit=资金/合规洞（注册审核未开）；warn=需人工确认的部署项（反代未声明/内容安全仍 shadow 等）；
//   - 只作展示与冒烟断言，不并入 MaybeAlert 群通知——配置问题重启前会一直红，刷群无意义。
package metrics

import (
	"fmt"
	"os"
	"strings"

	"ai-scrm/internal/db"
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
// 返回按严重度排序的检查项。
// 复核批 P0-1(2026-09-18)：非 release 不再整块 skipped——旧行为恰好绕开
// "GIN_MODE 不设（隐式 debug）+ pay_mode 默认 mock"这套默认组合的检查。
// 现同表评估，但 Crit 统一降为 Warn 并置顶 runtime_mode 提示（保留"本地联调不刷红"口径）。
func ComputeReadiness() []HealthCheck {
	checks := computeReadinessChecks()
	if !deployIsRelease {
		for i := range checks {
			if checks[i].Status == StatusCrit {
				checks[i].Status = StatusWarn
			}
		}
		_, explicit := os.LookupEnv("GIN_MODE")
		checks = append([]HealthCheck{{
			Name: "runtime_mode", Status: StatusWarn,
			Value:  map[bool]string{true: "debug(explicit)", false: "debug(GIN_MODE未设置)"}[explicit],
			WarnAt: "-", CritAt: "-",
			Desc: "非 release 模式：mock-pay/弱配置守卫按开发姿态。部署前务必显式设 GIN_MODE=release（资金闸按显式模式判定，未设置按严格态）",
		}}, checks...)
	}
	return checks
}

// computeReadinessChecks R1-R8 生产就绪逐项评估（release 与降级态共用同一张检查表）。
func computeReadinessChecks() []HealthCheck {
	cfg := runtimecfg.DefaultSystemConfigService
	checks := []HealthCheck{}

	// E2(2026-09-19 增强批)：Sentry/GlitchTip 双轨观测位。未配置≠未就绪——
	// /client-errors 自建通道仍在岗，故恒 OK 只作展示，不计 warn 红灯
	sentryOn := os.Getenv("SENTRY_DSN") != ""
	checks = append(checks, HealthCheck{
		Name: "sentry_configured", Status: StatusOK,
		Value:  map[bool]string{true: "configured", false: "unset"}[sentryOn],
		WarnAt: "-", CritAt: "-",
		Desc: "Sentry/GlitchTip 错误上报双轨观测位；未配置时 /client-errors 自建通道兜底",
	})

	// P2-2(2026-09-20 批三)：RLS 真实生效形态显式观测——"RLS_ENABLED=true"≠"DB 级兜底恒成立"：
	// ①策略在 app.current_tenant 未设置时恒真放行（休眠 opt-in，后台任务依赖，非全时强隔离）；
	// ②连接角色为 SUPERUSER/BYPASSRLS 时 PG 无条件旁路。默认未启用属设计内（应用层 db.T/PQ 保证）判 OK；
	// 启用但被旁路判 Warn（给出的第二道闸是虚设）；启用且未旁路判 OK 但 Desc 明示恒真放行语义。
	rs := db.GetRLSStatus()
	switch {
	case !rs.Enabled:
		checks = append(checks, HealthCheck{
			Name: "rls_effective", Status: StatusOK, Value: "disabled",
			WarnAt: "-", CritAt: "-",
			Desc: "RLS 未启用（默认）：租户隔离仅应用层 db.T/PQ；勿当 DB 级兜底已成立",
		})
	case rs.Bypass:
		checks = append(checks, readinessCheck("rls_effective", false, "enabled_but_bypassed", StatusWarn,
			"RLS_ENABLED=true 但连接角色为 SUPERUSER/BYPASSRLS → PG 无条件旁路策略，DB 级兜底形同虚设；生产须为应用建 NOSUPERUSER NOBYPASSRLS 角色（见 DEPLOY_CHECKLIST）"))
	default:
		checks = append(checks, HealthCheck{
			Name: "rls_effective", Status: StatusOK, Value: fmt.Sprintf("enabled(%d tables)", rs.Tables),
			WarnAt: "-", CritAt: "-",
			Desc: "策略已挂但休眠式：app.current_tenant 未设置的查询恒真放行（后台任务依赖），强隔离走 db.WithTenantRLS 显式 opt-in",
		})
	}

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
	// 2026-09-22 复核 P0-1：配置缺失时的兜底改用 static_qr（安全侧），不再默认 mock。
	payMode := "static_qr"
	if cfg != nil {
		payMode = cfg.GetStringForTenant(0, "pay_mode", "static_qr")
	}
	checks = append(checks, readinessCheck("pay_mode", payMode == "sdk", payMode, StatusWarn,
		"pay_mode 非 sdk：到账依赖模拟/人工确认，正式收款前请配 pay_provider+商户凭据并切 pay_mode=sdk"))
	allowMockPay := strings.EqualFold(os.Getenv("ALLOW_MOCK_PAY"), "true")
	checks = append(checks, readinessCheck("allow_mock_pay", !allowMockPay,
		map[bool]string{true: "true", false: "false"}[allowMockPay], StatusCrit,
		"ALLOW_MOCK_PAY=true 且 release：mock-pay 模拟到账端点对 admin 开放=0 元白嫖洞，请移除该环境变量"))

	// R8 网关回调金额归属（P1-2，2026-09-20 审计批）：gateway_webhook_amount_required
	// 关掉=存量聚合商兼容放行无金额回调，共享密钥方错配/被劫持可按订单面额全额发货，资金敞口
	amtRequired := true
	if cfg != nil {
		amtRequired = cfg.GetBoolForTenant(0, "gateway_webhook_amount_required", true)
	}
	checks = append(checks, readinessCheck("gateway_webhook_amount", amtRequired,
		map[bool]string{true: "required", false: "compat(legacy)"}[amtRequired], StatusWarn,
		"gateway_webhook_amount_required=false：网关回调缺金额兼容放行，尽快让聚合商升级五段签名并回开严格态"))

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
