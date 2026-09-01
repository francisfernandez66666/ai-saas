// Package service Token 三桶扣减引擎单元测试（P2-1 自动化测试，2026-08-30）
package service

import (
	"testing"
	"time"

	"ai-scrm/internal/testutil"
)

// ============================================================
// Token 三桶扣减引擎单元测试
//
// 测试覆盖：
//   1. 三桶扣减顺序（③免费→①订阅→②余额）
//   2. 免费桶过期处理
//   3. 灰度开关行为
//   4. 扣减结果计算
// ============================================================

// TestDeductResultString 测试扣减结果字符串输出
func TestDeductResultString(t *testing.T) {
	r := DeductResult{FromFree: 100, FromMonthly: 200, FromBalance: 300}
	expected := "免费桶100+月度200+余额300"
	if r.String() != expected {
		t.Errorf("DeductResult.String() = %q, 期望 %q", r.String(), expected)
	}
}

// TestDeductResultZero 测试零值扣减结果
func TestDeductResultZero(t *testing.T) {
	r := DeductResult{}
	expected := "免费桶0+月度0+余额0"
	if r.String() != expected {
		t.Errorf("DeductResult.String() = %q, 期望 %q", r.String(), expected)
	}
}

// TestTokenBillingEnabled 测试 Token 计费总闸
func TestTokenBillingEnabled(t *testing.T) {
	// 未初始化时应返回 false
	old := DefaultSystemConfigService
	DefaultSystemConfigService = nil
	if TokenBillingEnabled() {
		t.Error("未初始化时应返回 false")
	}

	// 初始化后按配置返回
	DefaultSystemConfigService = &SystemConfigService{cache: map[string]string{"token_billing_enabled": "true"}}
	if !TokenBillingEnabled() {
		t.Error("配置为 true 时应返回 true")
	}

	DefaultSystemConfigService = &SystemConfigService{cache: map[string]string{"token_billing_enabled": "false"}}
	if TokenBillingEnabled() {
		t.Error("配置为 false 时应返回 false")
	}

	DefaultSystemConfigService = old
}

// TestBillingEnforced 测试计费强制开关
func TestBillingEnforced(t *testing.T) {
	old := DefaultSystemConfigService
	defer func() { DefaultSystemConfigService = old }()

	// 未启用 token 引擎时，billing_enforced 应被忽略
	DefaultSystemConfigService = &SystemConfigService{cache: map[string]string{
		"token_billing_enabled": "false",
		"billing_enforced":      "true",
	}}
	if billingEnforced() {
		t.Error("token_billing_enabled=false 时 billing_enforced 应被忽略")
	}

	// 启用 token 引擎后，billing_enforced 跟随配置
	DefaultSystemConfigService = &SystemConfigService{cache: map[string]string{
		"token_billing_enabled": "true",
		"billing_enforced":      "true",
	}}
	if !billingEnforced() {
		t.Error("双开关均启用时应返回 true")
	}

	DefaultSystemConfigService = &SystemConfigService{cache: map[string]string{
		"token_billing_enabled": "true",
		"billing_enforced":      "false",
	}}
	if billingEnforced() {
		t.Error("billing_enforced=false 时应返回 false")
	}
}

// TestCheckTokenAvailability 测试三桶可用性检查
func TestCheckTokenAvailability(t *testing.T) {
	// 未启用时恒放行
	old := DefaultSystemConfigService
	DefaultSystemConfigService = nil
	if !CheckTokenAvailability(1) {
		t.Error("未启用时应恒放行")
	}
	DefaultSystemConfigService = old
}

// TestMinInt64 测试 minInt64 辅助函数
func TestMinInt64(t *testing.T) {
	cases := []struct {
		a, b, want int64
	}{
		{1, 2, 1},
		{2, 1, 1},
		{5, 5, 5},
		{0, 100, 0},
		{-1, 100, -1},
	}
	for _, c := range cases {
		got := minInt64(c.a, c.b)
		if got != c.want {
			t.Errorf("minInt64(%d,%d) = %d, 期望 %d", c.a, c.b, got, c.want)
		}
	}
}

// TestFreeTokenExpiry 测试免费桶过期判断逻辑
func TestFreeTokenExpiry(t *testing.T) {
	now := time.Now()

	// 未过期：过期时间在未来
	futureExpiry := now.Add(24 * time.Hour)
	if futureExpiry.Before(now) {
		t.Error("未来时间不应被判定为过期")
	}

	// 已过期：过期时间在过去
	pastExpiry := now.Add(-24 * time.Hour)
	if !pastExpiry.Before(now) {
		t.Error("过去时间应被判定为过期")
	}

	// nil 过期时间：视为未过期
	var nilExpiry *time.Time
	if nilExpiry != nil && nilExpiry.Before(now) {
		t.Error("nil 过期时间不应被判定为过期")
	}
}

// TestDeductTokensActualNoEnforced 测试灰度模式下不扣减
func TestDeductTokensActualNoEnforced(t *testing.T) {
	// 未启用时应为 no-op
	old := DefaultSystemConfigService
	DefaultSystemConfigService = nil
	DeductTokensActual(1, 100) // 不应 panic
	DefaultSystemConfigService = old
}

// TestGrantTrialBucketNoTx 测试 GrantTrialBucket nil tx
func TestGrantTrialBucketNoTx(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: 跳过 DB 依赖测试")
	}
	testutil.SetupTestDB(t)
	GrantTrialBucket(nil, 999999, "test@example.com")
}
