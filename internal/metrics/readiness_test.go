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

func TestReadinessDebugSkipped(t *testing.T) {
	SetDeploymentContext(1, false)
	defer SetDeploymentContext(1, false)
	checks := ComputeReadiness()
	if len(checks) != 1 || checks[0].Name != "skipped" || checks[0].Status != StatusOK {
		t.Fatalf("debug 模式应只返回一条 skipped/ok，got %+v", checks)
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
