// 生产就绪探针单测：debug 门控、release 逐项判级、汇总口径。
package metrics

import (
	"os"
	"testing"

	"ai-scrm/config"
	"ai-scrm/internal/redisclient"
)

func findCheck(checks []HealthCheck, name string) *HealthCheck {
	for i := range checks {
		if checks[i].Name == name {
			return &checks[i]
		}
	}
	return nil
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
