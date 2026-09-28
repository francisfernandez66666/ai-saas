// 生产就绪探针（Readiness，2026-09-15 价值增强批）
// 背景：registration_review / contentsafety_mode / pay_mode / TRUSTED_PROXIES 这类开关
// 属"上线忘开=烧钱/合规裸奔"型配置，靠 DEPLOY_CHECKLIST.md 人肉勾选迟早漏。
// 本文件把检查单代码化：/status 返回 readiness 分组，运维一眼看到哪项没配好。
// 设计口径：
//   - release 逐项评估并按 Crit/Warn 判级；非 release 同表评估但 Crit 降 Warn（2026-09-18 P0-1 批改，
//     旧"debug 整块不评"恰好在默认 fail-open 组合下自我关闭）；
//   - crit=资金/合规洞（注册审核未开）；warn=需人工确认的部署项（反代未声明/内容安全仍 shadow 等）；
//   - 只作展示与冒烟断言，不并入 MaybeAlert 群通知——配置问题重启前会一直红，刷群无意义。
//   - 泄露收敛（P2-4 批三口径，2026-09-28 复核确认）：本清单**只**经 /status/detail
//     （X-Health-Token 守卫）直出；公开 /status 精简面仅 version/uptime/status/ok 四键，
//     readiness 各新增观测名（pay_mode crit 档、ai_decisions_disabled 等）一律不上公开面。
//     新增检查项时勿把 Name/Value 顺手塞进 ComputeHealth 的 snap.Checks——那才会经公开面聚合泄露。
package metrics

import (
	"fmt"
	"os"
	"strings"

	"ai-scrm/config"
	"ai-scrm/internal/db"
	"ai-scrm/internal/notify"
	"ai-scrm/internal/redisclient"
	"ai-scrm/internal/runtimecfg"
)

// readinessProdStrict 就绪面的"按生产严格态评估"判据（FIX-B/FIX-E，2026-09-28 审计复核批）。
// 为什么不用包内 deployIsRelease：它是 main 按 cfg.Server.Mode=="release" 注入的，而 Mode 的
// 默认值是 "debug"（GIN_MODE 不设即隐式 debug）——用它当资金闸的判据会被默认值 fail-open，
// 与复核批 P0-1 堵掉的那族缺陷同源。config.IsDevModeConfirmed 要求 GIN_MODE **显式**声明
// debug/test 才算开发态，未设置一律严格态，与 mock-pay/SSRF 闸同一单点判据。
// 注意：本判据只决定"表里写 Crit 还是 Warn"；debug 的统一降档（Crit→Warn）仍由
// ComputeReadiness 的 deployIsRelease 分支承担，两层叠加后本机显式 debug 不会被刷红。
func readinessProdStrict() bool {
	return !config.IsDevModeConfirmed()
}

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
// 结构重构后（2026-09-28 超长函数拆分批）主体只做**有序拼接**：五个分组函数 + redisReadiness 收尾，
// 判序即契约——/status/detail 的展示顺序与冒烟按名取项都锁定在这里，新增项进对应组、勿重排组序。
func computeReadinessChecks() []HealthCheck {
	cfg := runtimecfg.DefaultSystemConfigService
	checks := []HealthCheck{}
	checks = appendPlatformShapeChecks(checks)
	checks = appendComplianceGateChecks(checks, cfg)
	checks = appendPaymentFundChecks(checks, cfg)
	checks = appendDeployEnvChecks(checks)
	checks = appendNotifyReachChecks(checks, cfg)
	checks = appendCredentialSurfaceChecks(checks)
	// R10 多实例协调层（O1-a，2026-09-23 批六）：REDIS_ENABLED=true 却连不上 = "声明了多实例语义、
	// 实际各实例单干"（合并队列裁决/跨实例 WS 广播/登录锁全部退化），比没配更危险——
	// 没配是明知单机，配了没连上是静默降级。旧实现探测一次定终身，compose 里 app 先于 redis ready
	// 就永久降级；现 redisclient 有后台自愈，故这里按"当前是否已连通"实时判定：
	// 未连通判 Crit（多副本会双处理），自愈恢复后本项自动转绿。
	checks = append(checks, redisReadiness())
	return checks
}

// appendPlatformShapeChecks 平台形态两观测位：Sentry 双轨（E2）+ RLS 真实生效形态（P2-2）。
// 为什么单独成组：两者同属"如实展示部署形态、永不刷红"的展示位（未配置/未启用均判 OK），
// 与后面的资金/合规闸判级逻辑无关，混排会让运维把"形态说明"误读成"待整改红灯"。
func appendPlatformShapeChecks(checks []HealthCheck) []HealthCheck {
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
	// ①策略在 app.current_tenant 未设置（或空串，见下 FIX-A）时恒真放行（休眠 opt-in，后台任务依赖，非全时强隔离）；
	// ②连接角色为 SUPERUSER/BYPASSRLS 时 PG 无条件旁路。默认未启用属设计内（应用层 db.T/PQ 保证）判 OK；
	// 启用但被旁路判 Warn（给出的第二道闸是虚设）；启用且未旁路判 OK 但 Desc 明示恒真放行语义。
	// FIX-A(2026-09-28) Value 口径变更：从"清单条数"改成"实查 relrowsecurity=true 数/清单数"——
	// 旧写法把"循环没报错"当"策略生效"报，实测 14 张挂策略的表通电数为 0，观测位自己在撒谎。
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
			Name: "rls_effective", Status: StatusOK, Value: fmt.Sprintf("enabled(%d/%d enabled)", rs.EnableVerified, rs.Tables),
			WarnAt: "-", CritAt: "-",
			Desc: "策略已挂且已通电（实查 pg_class.relrowsecurity）；休眠式=app.current_tenant 未设置**或为空串**时恒真放行（后台任务依赖；PG 对跑过 SET LOCAL 的连接把自定义 GUC 复位成空串而非未定义，故两条腿都必须在场），强隔离走 db.WithTenantRLS 显式 opt-in",
		})
	}

	// FIX-A 策略面可观测（2026-09-28）：通电只证明"PG 会咨询策略"，策略文本对不对是另一件事。
	// 本条专查"已 ENABLE 且挂了 tenant_isolation 的表里，有几张缺休眠腿"——缺腿的后果不是"更严"，
	// 而是那条连接上普通查询被筛成空集、写入撞 42501（030 补 ENABLE 当天 billing 九例红即此形态）。
	// 判据与迁移 031 同源（db.rlsPolicyHasDormantLegs），两侧分叉会得到"迁移说没事、观测位也说没事、
	// 库其实没修"这种双绿，故这里复用同一谓词而不是再写一遍 SQL 判断。
	pf := db.RLSPolicyFace()
	switch {
	case pf.QueryFailed:
		checks = append(checks, readinessCheck("rls_policy_face", false, "query_failed", StatusWarn,
			"策略面无法验证（pg_policy 查询失败：权限或库不可用？）——rls_effective 的 OK 只说明策略会被咨询，不代表策略内容合格"))
	case pf.Checked == 0:
		// 策略面为空是合法形态：全新库既没跑过 002/030 也没跑过 EnableRLS，无表处于"通电且有策略"。
		checks = append(checks, HealthCheck{
			Name: "rls_policy_face", Status: StatusOK, Value: "no_policy",
			WarnAt: "-", CritAt: "-",
			Desc: "无「已通电且挂 tenant_isolation」的表（全新库或 RLS 从未启用）：本项无对象可判",
		})
	case pf.DormantLegMissing > 0:
		checks = append(checks, readinessCheck("rls_policy_face", false,
			fmt.Sprintf("%d/%d_missing_dormant_leg(%s)", pf.DormantLegMissing, pf.Checked, strings.Join(pf.MissingTables, ",")),
			StatusCrit,
			"这些表已通电但 tenant_isolation 表达式缺休眠腿（IS NULL 或空串）：该连接上未激活 RLS 的查询会被筛成空集、写入撞 42501，跑 migrations/031_rls_policy_guc_empty.up.sql 重建"))
	default:
		checks = append(checks, HealthCheck{
			Name: "rls_policy_face", Status: StatusOK, Value: fmt.Sprintf("ok(%d tables)", pf.Checked),
			WarnAt: "-", CritAt: "-",
			Desc: "通电表的政策表达式均含休眠双腿（未设置/空串放行）——与迁移 031 同一判据实查 pg_policy",
		})
	}
	return checks
}

// appendComplianceGateChecks 合规三闸：注册审核（R1）+ 内容安全（R2）+ AI 决策开关聚合（R11）。
// 为什么单独成组：三者同属"合规/防薅敞口"判据族——registration_review 与 contentsafety 在
// 严格态判 Crit/Warn 的表内定级互相引用（FIX-E 复核即以本组为对照），R11 的聚合一条也只在
// 严格态出现，三者与后面的资金闸（pay 族）分档不同，混排会打乱"红灯堆里先看哪一行"的读表顺序。
func appendComplianceGateChecks(checks []HealthCheck, cfg *runtimecfg.SystemConfigService) []HealthCheck {
	// R1 注册审核开关：出厂 false=注册即送真实 AI 额度（防薅红线），生产必开。
	// FIX-E(2026-09-28) 复核结论：本判据在 release 态**已是 Crit**（表内恒 Crit，
	// 降档只作用于显式 debug），无需升级——新登的"生产安全默认包"各项与之同表评估。
	reviewOn := false
	if cfg != nil {
		reviewOn = cfg.GetBoolForTenant(0, "registration_review", false)
	}
	checks = append(checks, readinessCheck("registration_review", reviewOn,
		map[bool]string{true: "true", false: "false"}[reviewOn], StatusCrit,
		"注册审核未开启：新租户注册即白烧真实 AI 额度，请开 system_config registration_review=true 或确认已有其他防薅措施"))

	// R2 内容安全模式：enabled=false 直接 crit（闸门全关）；shadow 属演练期，warn 提醒收口。
	// FIX-E(2026-09-28)：判级核实——release 态 mode!="enforce" 恒 warn（本表 Warn 不受
	// debug 降档影响，两态同判），正合"生产必切 enforce、shadow 只观察不拦截"的口径，不改判级只收口文案。
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
			"内容安全仍为 "+csMode+"（只观察不拦截）：生产必切 contentsafety_mode=enforce，首周演练误杀率后即收口"))
	}

	// R11 AI 决策开关聚合观测位（FIX-E，2026-09-28 生产安全默认包）：
	// 模板择臂（template_bandit_enabled）/实验自动晋升（experiment_auto_promote）/销售路径机
	// （sales_path_enabled）三把均出厂默认 false——这是刻意口径（防"忘了关就自动改话术"），
	// 不是缺陷；但生产三把全关意味着 AI 销售闭环整体没开跑，属上线就绪缺口，判 Warn 提醒
	// "上线时按场景显式开启"。聚合为**一条**而非逐条刷三条：同一件事给运维提醒一次就够，
	// 三条各占一行会把真正的资金/合规红灯稀释掉。只在生产严格态出现——开发态没有
	// "上线就绪"可评，逐项出现反而干扰本机冒烟读数。
	if readinessProdStrict() {
		var on []string
		if cfg != nil {
			if cfg.GetBoolForTenant(0, "template_bandit_enabled", false) {
				on = append(on, "template_bandit_enabled")
			}
			if cfg.GetBoolForTenant(0, "experiment_auto_promote", false) {
				on = append(on, "experiment_auto_promote")
			}
			if cfg.GetBoolForTenant(0, "sales_path_enabled", false) {
				on = append(on, "sales_path_enabled")
			}
		}
		anyOn := len(on) > 0
		value, desc := "all_off",
			"AI 决策三开关（template_bandit_enabled/experiment_auto_promote/sales_path_enabled）全关：出厂默认关属刻意口径（防忘关即自动改话术）；上线时按场景显式开启"
		if anyOn {
			value = strings.Join(on, ",")
			desc = "AI 决策开关已开至少一项：" + value + "（其余项上线时按场景显式开启）"
		}
		level := StatusWarn
		if anyOn {
			level = StatusOK
		}
		checks = append(checks, HealthCheck{
			Name: "ai_decisions_disabled", Status: level, Value: value,
			WarnAt: "-", CritAt: "-", Desc: desc,
		})
	}
	return checks
}

// appendPaymentFundChecks 资金四闸：收款模式（R3，FIX-B 生产 mock 升 Crit）+ 微信回调验签强度
// （R3b）+ ALLOW_MOCK_PAY 环境变量 + 网关回调金额归属（R8）。
// 为什么单独成组：四项都是"到账/发货放行"链路上的闸，且共用同一个判据
// readinessProdStrict()（FIX-B 的 Crit 升档与 R3b 的"release 恒 Warn 两态同判"核实结论都以它为准），
// 判级改动必须四项一起复核，故物理隔离在一组。
func appendPaymentFundChecks(checks []HealthCheck, cfg *runtimecfg.SystemConfigService) []HealthCheck {
	// R3 收款模式：mock=模拟到账；release+ALLOW_MOCK_PAY=true 时 0 元白嫖洞，warn 提醒收口
	// 2026-09-22 复核 P0-1：配置缺失时的兜底改用 static_qr（安全侧），不再默认 mock。
	// FIX-B(2026-09-28) 生产闸：旧实现对所有非 sdk 一律 warn——把"生产忘切 mock"这种
	// 零收入发货敞口（订单不经任何 PSP 即被标记到账、权益照发）和 static_qr（尚有人工
	// 确认环节）混在同一档，红灯堆里没人单独看它。现：生产严格态且 pay_mode=="mock"
	// 升 Crit；显式 debug 维持 Warn 不动（本机九套冒烟全依赖 mock 到账，不能因此判红）。
	// 判据用 readinessProdStrict()（!config.IsDevModeConfirmed）而非 deployIsRelease，理由见该函数注释。
	payMode := "static_qr"
	if cfg != nil {
		payMode = cfg.GetStringForTenant(0, "pay_mode", "static_qr")
	}
	payLevel := StatusWarn
	payHint := "pay_mode 非 sdk：到账依赖模拟/人工确认，正式收款前请配 pay_provider+商户凭据并切 pay_mode=sdk"
	if payMode == "mock" {
		payHint = "生产态 pay_mode=mock：到账为模拟回执，订单不经支付渠道即放行权益＝零收入发货敞口，必切 pay_mode=sdk（真实网关）或 static_qr（人工确认）"
		if readinessProdStrict() {
			payLevel = StatusCrit
		}
	}
	checks = append(checks, readinessCheck("pay_mode", payMode == "sdk", payMode, payLevel, payHint))

	// R3b 微信回调验签强度（E1-2，2026-09-24）：pay_wechat_cert_verify=false 时，
	// "没带 Wechatpay-Signature" 的回调仍只靠 APIv3Key 解密证明放行——密钥一泄露就能凭空造到账报文。
	// （带了签名头的报文无论本开关如何都必验，所以这不是"没有防线"，而是"防线可被不发签名绕过"。）
	// FIX-B(2026-09-28) 核实：本判据 release+false 恒 Warn（Warn 不受 debug 降档影响，两态同判），
	// 文案已写明"APIv3Key 泄露即可伪造到账"——判级与口径均已满足，不重复造轮子，仅登记核实结论。
	certVerify := false
	if cfg != nil {
		certVerify = cfg.GetBoolForTenant(0, "pay_wechat_cert_verify", false)
	}
	checks = append(checks, readinessCheck("wechat_cert_verify", certVerify,
		map[bool]string{true: "strict", false: "decrypt-only"}[certVerify], StatusWarn,
		"微信回调未开严格验签：缺 Wechatpay-Signature 的报文只按 APIv3Key 解密放行，APIv3Key 泄露即可伪造到账。配好商户私钥/商户号后开 pay_wechat_cert_verify=true（平台证书由 /v3/certificates 自动拉取）"))
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
	return checks
}

// appendDeployEnvChecks 进程环境变量两项：TRUSTED_PROXIES（R4）+ AI_MOCK_MODE（R5）。
// 为什么单独成组：这两项判据只读 os.Getenv、不经热配置层——与前后两组（依赖 cfg 的库内开关）
// 取数通道不同；单测里它们也只受 t.Setenv 影响，隔离后"改了 system_config 为何不生效"这类误判可免。
func appendDeployEnvChecks(checks []HealthCheck) []HealthCheck {
	// R4 反向代理声明：未配置=不信任任何 XFF（直连安全），但反代部署漏配会让限流按代理 IP 聚合
	tp := strings.TrimSpace(os.Getenv("TRUSTED_PROXIES"))
	checks = append(checks, readinessCheck("trusted_proxies", tp != "", tp, StatusWarn,
		"TRUSTED_PROXIES 未配置：直连部署无碍；经 Nginx/网关部署必配，否则防爆破/注册限流按代理 IP 聚合失效"))

	// R5 真实 AI：release+AI_MOCK_MODE=true 全站假回复
	aiMock := strings.EqualFold(os.Getenv("AI_MOCK_MODE"), "true")
	checks = append(checks, readinessCheck("ai_real_call", !aiMock,
		map[bool]string{true: "mock", false: "real"}[aiMock], StatusCrit,
		"AI_MOCK_MODE=true 且 release：全站回复为模拟话术，请配真实供应商 Key 并关闭 mock"))
	return checks
}

// appendNotifyReachChecks 触达三闸：告警群 webhook（R6）+ SMTP（R6）+ 重置码通道实际形态（R9）。
// 为什么单独成组：三者回答同一个问题——"该发出去的东西（告警/验证码/重置码）到底发不发得出去"，
// 且 R9 判的是"实际生效通道"而非配置值（SMTP 缺失时实现降级 log），两条 smtp 判据必须并排才可
// 交叉核对，拆散后会出现"smtp 位绿、重置码位红"却没人看得出是同一根因。
func appendNotifyReachChecks(checks []HealthCheck, cfg *runtimecfg.SystemConfigService) []HealthCheck {
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

	// R9 找回密码通道安全性（S2，2026-09-23 批二）：
	// 判"实际生效通道"而非配置值——reset_code_channel=smtp 但 SMTP 环境变量缺失时实现会降级 log，
	// 只看配置会误判为安全。log 通道=一次性重置码明文进服务端日志，拥有日志读取权者
	// 可对任意"已绑定邮箱"的账号完成改密；同时真实用户根本无法自助找回密码（拿不到码）。
	// 判 Warn 不判 Crit：管理员仍可代改，属"生产可用性 + 运维卫生"缺口而非资金洞。
	resetKind := notify.ResetSenderKind()
	checks = append(checks, readinessCheck("reset_code_channel_secure", resetKind == "smtp", resetKind, StatusWarn,
		"重置码走 log 通道：用户无法自助找回密码且验证码明文落日志。请配 SMTP_HOST/SMTP_USER 并设 reset_code_channel=smtp（见 DEPLOY_CHECKLIST）"))
	return checks
}

// redisReadiness Redis 声明位与连通位对账（O1-a）。
// 未声明启用属设计内单机模式，判 OK 只作展示，不计红。
func redisReadiness() HealthCheck {
	if !redisclient.DeclaredEnabled() {
		return HealthCheck{
			Name: "redis_declared_but_down", Status: StatusOK, Value: "not_declared",
			WarnAt: "-", CritAt: "-",
			Desc: "REDIS_ENABLED=false：单机内存模式（多副本部署必须开 Redis，否则队列/锁/广播各实例单干）",
		}
	}
	if redisclient.IsEnabled() {
		return HealthCheck{
			Name: "redis_declared_but_down", Status: StatusOK, Value: "connected",
			WarnAt: "-", CritAt: "-", Desc: "Redis 已连通，多实例协调语义生效",
		}
	}
	return readinessCheck("redis_declared_but_down", false, "declared_but_down", StatusCrit,
		"REDIS_ENABLED=true 但当前未连通：合并队列/跨实例广播/登录锁退化为单实例语义，正在后台自愈重探。请确认 Redis 服务可达（compose 须给 redis 加 healthcheck 并让 app depends_on redis）")
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
