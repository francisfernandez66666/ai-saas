// 生产就绪探针单测：debug 门控、release 逐项判级、汇总口径。
package metrics

import (
	"os"
	"testing"
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

func TestReadinessReleaseLevels(t *testing.T) {
	SetDeploymentContext(1, true)
	defer SetDeploymentContext(1, false)
	// 无配置服务时走各默认值：registration_review=false→crit、contentsafety shadow→warn、
	// pay_mode=mock→warn；环境变量组合验证 crit 项
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
