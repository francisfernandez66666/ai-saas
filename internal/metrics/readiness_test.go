// 生产就绪探针单测：debug 门控、release 逐项判级、汇总口径。
package metrics

import (
	"os"
	"strings"
	"testing"

	"ai-scrm/config"
	"ai-scrm/internal/redisclient"
	"ai-scrm/internal/runtimecfg"
)

func findCheck(checks []HealthCheck, name string) *HealthCheck {
	for i := range checks {
		if checks[i].Name == name {
			return &checks[i]
		}
	}
	return nil
}

// countCheck 统计某观测名出现次数——"聚合为恰一条"的断言必须有计数而非 find 首个，
// 否则逐条刷三项的实现照样能被 findCheck 找到"那一条"而假绿（FIX-E，2026-09-28）。
func countCheck(checks []HealthCheck, name string) int {
	n := 0
	for i := range checks {
		if checks[i].Name == name {
			n++
		}
	}
	return n
}

// TestReadinessDebugDowngraded 复核批 P0-1(2026-09-18)：debug 不再整块 skipped——
// 同表评估但 Crit 降 Warn、ready 不被刷红，且首项 runtime_mode 提示部署收口。
func TestReadinessDebugDowngraded(t *testing.T) {
	SetDeploymentContext(1, false)
	defer SetDeploymentContext(1, false)
	t.Setenv("GIN_MODE", "") // 未显式设置：值非空即视为声明，空串按未设置同族处理
	t.Setenv("ALLOW_MOCK_PAY", "true")
	checks := ComputeReadiness()
	if len(checks) < 5 {
		t.Fatalf("debug 应返回完整评估清单而非占位，got %d 项", len(checks))
	}
	if checks[0].Name != "runtime_mode" {
		t.Fatalf("首项应为 runtime_mode，got %s", checks[0].Name)
	}
	for _, c := range checks {
		if c.Status == StatusCrit {
			t.Fatalf("debug 模式 Crit 应统一降为 Warn，残留: %+v", c)
		}
	}
	ready, crit, _ := ReadinessSummary(checks)
	if !ready || crit != 0 {
		t.Fatalf("debug 降档后应 ready（不刷红）: ready=%v crit=%d", ready, crit)
	}
}

// TestReadinessReleaseLevels 覆盖 release 级就绪评估：registration_review=false 记 crit，contentsafety shadow 与 pay_mode=mock 记 warn。
func TestReadinessReleaseLevels(t *testing.T) {
	SetDeploymentContext(1, true)
	defer SetDeploymentContext(1, false)
	// 无配置服务时走各默认值：registration_review=false→crit、contentsafety shadow→warn、
	// pay_mode 兜底为 static_qr（非 sdk）→warn；环境变量组合验证 crit 项
	t.Setenv("ALLOW_MOCK_PAY", "true")
	t.Setenv("AI_MOCK_MODE", "true")
	t.Setenv("TRUSTED_PROXIES", "")
	checks := ComputeReadiness()
	if findCheck(checks, "registration_review").Status != StatusCrit {
		t.Fatalf("注册审核未开应为 crit")
	}
	if findCheck(checks, "allow_mock_pay").Status != StatusCrit {
		t.Fatalf("release+ALLOW_MOCK_PAY=true 应为 crit")
	}
	if findCheck(checks, "ai_real_call").Status != StatusCrit {
		t.Fatalf("release+AI_MOCK_MODE=true 应为 crit")
	}
	if findCheck(checks, "contentsafety").Status != StatusWarn {
		t.Fatalf("shadow 模式应为 warn")
	}
	if findCheck(checks, "trusted_proxies").Status != StatusWarn {
		t.Fatalf("TRUSTED_PROXIES 未配应为 warn")
	}
	// E1-2(2026-09-24)：微信回调验签强度必须是一项**看得见**的观测位——无配置服务时按默认
	// 宽松态如实报 decrypt-only（而不是缺项或假装 ok），否则上线时没人知道该开这个开关。
	wcv := findCheck(checks, "wechat_cert_verify")
	if wcv == nil || wcv.Value != "decrypt-only" || wcv.Status != StatusWarn {
		t.Fatalf("微信验签观测位缺失或判级错误: %+v", wcv)
	}
	ready, crit, warn := ReadinessSummary(checks)
	if ready || crit < 3 || warn < 2 {
		t.Fatalf("汇总口径错误: ready=%v crit=%d warn=%d", ready, crit, warn)
	}

	// 配齐后应全绿（SMTP/企微除外仅 warn，不影响 ready）
	t.Setenv("ALLOW_MOCK_PAY", "")
	t.Setenv("AI_MOCK_MODE", "false")
	t.Setenv("TRUSTED_PROXIES", "10.0.0.0/8")
	t.Setenv("SMTP_HOST", "smtp.example.com")
	t.Setenv("SMTP_USER", "bot@example.com")
	checks = ComputeReadiness()
	if findCheck(checks, "allow_mock_pay").Status != StatusOK {
		t.Fatalf("ALLOW_MOCK_PAY 移除后应 ok")
	}
	if findCheck(checks, "trusted_proxies").Status != StatusOK {
		t.Fatalf("TRUSTED_PROXIES 配置后应 ok")
	}
	if os.Getenv("SMTP_HOST") == "" && findCheck(checks, "smtp").Status != StatusWarn {
		t.Fatalf("SMTP 缺失应 warn")
	}
}

// TestReadinessRedisDeclaredButDown O1-a(2026-09-23 批六)：Redis 声明位与连通位对账。
// 口径：未声明启用=设计内单机（判 OK 只展示）；声明了却连不上=静默降级成单实例语义，
// release 判 Crit（多副本会双处理/丢广播），debug 由统一降档规则转 Warn。
func TestReadinessRedisDeclaredButDown(t *testing.T) {
	t.Cleanup(func() {
		redisclient.StopSelfHeal()
		redisclient.Init(config.RedisConfig{Enabled: false}) // 复位声明位，别污染同包其他用例
	})

	redisclient.Init(config.RedisConfig{Enabled: true, Addr: "127.0.0.1:1"}) // 必然连不上
	redisclient.StopSelfHeal()                                               // 单测不留后台重探
	if redisclient.IsEnabled() {
		t.Skip("本机 127.0.0.1:1 意外可连，跳过降级态断言")
	}

	SetDeploymentContext(1, true)
	defer SetDeploymentContext(1, false)
	c := findCheck(ComputeReadiness(), "redis_declared_but_down")
	if c == nil {
		t.Fatalf("readiness 缺 redis_declared_but_down 观测位")
	}
	if c.Status != StatusCrit || c.Value != "declared_but_down" {
		t.Errorf("声明启用却未连通应判 Crit(declared_but_down)，实际 %s/%s", c.Status, c.Value)
	}

	// 未声明启用：判 OK 且明示单机语义（不得刷红，本地/CI 默认态）
	redisclient.Init(config.RedisConfig{Enabled: false})
	c = findCheck(ComputeReadiness(), "redis_declared_but_down")
	if c.Status != StatusOK || c.Value != "not_declared" {
		t.Errorf("REDIS_ENABLED=false 应判 OK(not_declared)，实际 %s/%s", c.Status, c.Value)
	}
}

// TestReadinessPayModeMockGate FIX-B(2026-09-28 审计复核批)：pay_mode 生产闸四格中的两格——
// release+mock ⇒ Crit（零收入发货资金敞口，旧实现只 Warn 会被红灯堆淹没）；
// 显式 debug+mock ⇒ 仍 Warn 不 Crit（本机九套冒烟全依赖 mock 到账，不能因此判红）。
// 另附 release+static_qr ⇒ Warn 不 Crit：两种非 sdk 形态必须分档，
// 否则"生产忘切 mock"与"刻意人工确认"判级相同，升级就失去判别力。
func TestReadinessPayModeMockGate(t *testing.T) {
	restore := runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(map[string]string{"pay_mode": "mock"}, nil))
	defer restore()
	SetDeploymentContext(1, true)
	defer SetDeploymentContext(1, false)

	t.Setenv("GIN_MODE", "release")
	c := findCheck(ComputeReadiness(), "pay_mode")
	if c == nil || c.Status != StatusCrit || c.Value != "mock" {
		t.Fatalf("release+pay_mode=mock 应判 Crit(mock)，实际 %+v", c)
	}

	t.Setenv("GIN_MODE", "debug")
	SetDeploymentContext(1, false)
	c = findCheck(ComputeReadiness(), "pay_mode")
	if c == nil || c.Status != StatusWarn || c.Value != "mock" {
		t.Fatalf("debug+pay_mode=mock 应维持 Warn（现判级不动），实际 %+v", c)
	}

	t.Setenv("GIN_MODE", "release")
	SetDeploymentContext(1, true)
	restore2 := runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(map[string]string{"pay_mode": "static_qr"}, nil))
	defer restore2()
	c = findCheck(ComputeReadiness(), "pay_mode")
	if c == nil || c.Status != StatusWarn || c.Value != "static_qr" {
		t.Fatalf("release+static_qr 应判 Warn 不 Crit，实际 %+v", c)
	}
}

// TestReadinessWechatCertVerifyWarn FIX-B(2026-09-28)：pay_wechat_cert_verify 两向都在场——
// false ⇒ Warn（未开平台证书严格验签时 APIv3Key 泄露可伪造到账通知）；true ⇒ OK。
// 反向格（true 也必须绿）不可省：只测 false 亮灯的实现会把常亮红灯当合规，运维从此不看 warn。
func TestReadinessWechatCertVerifyWarn(t *testing.T) {
	SetDeploymentContext(1, true)
	defer SetDeploymentContext(1, false)
	t.Setenv("GIN_MODE", "release")

	restore := runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(map[string]string{"pay_wechat_cert_verify": "false"}, nil))
	c := findCheck(ComputeReadiness(), "wechat_cert_verify")
	if c == nil || c.Status != StatusWarn || c.Value != "decrypt-only" {
		t.Fatalf("release+cert_verify=false 应判 Warn(decrypt-only)，实际 %+v", c)
	}
	restore()

	restore2 := runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(map[string]string{"pay_wechat_cert_verify": "true"}, nil))
	defer restore2()
	c = findCheck(ComputeReadiness(), "wechat_cert_verify")
	if c == nil || c.Status != StatusOK || c.Value != "strict" {
		t.Fatalf("cert_verify=true 应判 OK(strict)，实际 %+v", c)
	}
}

// TestReadinessAIDecisionsDisabledAggregate FIX-E(2026-09-28 生产安全默认包)：
// 三个 AI 决策开关（template_bandit_enabled/experiment_auto_promote/sales_path_enabled，
// 系统层 tenant_id=0）全 false 时——
//   - release：聚合**恰一条** ai_decisions_disabled/Warn（逐条刷三条会把资金/合规红灯稀释掉；
//     countCheck 而非 findCheck，防"三项各一条"的实现被首个命中假绿）；
//   - 显式 debug：该项完全不出现（开发态没有"上线就绪"可评）；
//   - release+任一开：判 OK 且 Value 列出已开键（观测位保持在场，运维看得见开了哪几把）。
func TestReadinessAIDecisionsDisabledAggregate(t *testing.T) {
	SetDeploymentContext(1, true)
	defer SetDeploymentContext(1, false)

	// release + 三开关全缺省 false
	t.Setenv("GIN_MODE", "release")
	restore := runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(map[string]string{}, nil))
	defer restore()
	checks := ComputeReadiness()
	if n := countCheck(checks, "ai_decisions_disabled"); n != 1 {
		t.Fatalf("release+三开关全关应恰聚合一格，实际 %d 格", n)
	}
	c := findCheck(checks, "ai_decisions_disabled")
	if c.Status != StatusWarn || c.Value != "all_off" {
		t.Fatalf("全关应判 Warn(all_off)，实际 %+v", c)
	}
	// 反证：三个键名不得各自成格（聚合口径的本体）
	for _, key := range []string{"template_bandit_enabled", "experiment_auto_promote", "sales_path_enabled"} {
		if findCheck(checks, key) != nil {
			t.Fatalf("不得逐键刷格，发现独立观测位 %s", key)
		}
	}

	// 显式 debug：该项不出现
	t.Setenv("GIN_MODE", "debug")
	SetDeploymentContext(1, false)
	if c := findCheck(ComputeReadiness(), "ai_decisions_disabled"); c != nil {
		t.Fatalf("debug 态不应出现 ai_decisions_disabled 观测位，实际 %+v", c)
	}

	// release + 任一开：判 OK 且 Value 含已开键
	t.Setenv("GIN_MODE", "release")
	SetDeploymentContext(1, true)
	restore2 := runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(map[string]string{"sales_path_enabled": "true"}, nil))
	defer restore2()
	c = findCheck(ComputeReadiness(), "ai_decisions_disabled")
	if c == nil || c.Status != StatusOK || !strings.Contains(c.Value, "sales_path_enabled") {
		t.Fatalf("任一开应判 OK 且列出已开键，实际 %+v", c)
	}
}

// TestReadinessRLSPolicyFaceSingleLine FIX-A(2026-09-28)：策略面观测位必须**恰一条**，
// 且 Crit 档只留给真故障（缺休眠腿）——三种非故障形态（未启用/从未建策略/查不到库）
// 一律 OK 或 Warn。理由：这一项每 5s 就会被 /status/detail 读一次，逐表刷行会把
// "运维一眼看到哪项没配好"的读表节奏破坏掉；而把"没查成"判成 Crit 会让 DB 抖一下
// 就把就绪面刷红，与 readiness 只作展示/冒烟的定位不符。
func TestReadinessRLSPolicyFaceSingleLine(t *testing.T) {
	SetDeploymentContext(1, true)
	defer SetDeploymentContext(1, false)
	checks := ComputeReadiness()
	if n := countCheck(checks, "rls_policy_face"); n != 1 {
		t.Fatalf("rls_policy_face 必须恰一条（聚合判据），got %d", n)
	}
	c := findCheck(checks, "rls_policy_face")
	if c == nil {
		t.Fatalf("找不到 rls_policy_face 观测位")
	}
	if c.Status == StatusCrit && !strings.Contains(c.Value, "missing_dormant_leg") {
		t.Fatalf("Crit 只能用于「已通电但策略缺休眠腿」这一种真故障，got value=%q desc=%q", c.Value, c.Desc)
	}
	// 反证自证：本用例不是空转——非 Crit 时必须落在这三种已知安全形态之一
	if c.Status != StatusCrit {
		known := c.Value == "no_policy" || c.Value == "query_failed" || strings.HasPrefix(c.Value, "ok(")
		if !known {
			t.Fatalf("rls_policy_face 出现未知取值形态（判据分叉）：value=%q status=%v", c.Value, c.Status)
		}
	}
}
