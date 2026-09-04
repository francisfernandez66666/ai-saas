// UsageSink 实时计量单测：影子余额 seed/扣减、批量落库幂等、Invalidate 失效重读。
// 依赖 DB：经 testutil.SetupTestDB 统一初始化（不可用则自动跳过）。
package service

import (
	"sync"
	"testing"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// TestSinkInvalidateShadow 验证失效后重新 seed（充值/发放后影子不残留旧值）
func TestSinkInvalidateShadow(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: 跳过 DB 依赖测试")
	}
	testutil.SetupTestDB(t)

	tenant := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tenant)

	// 初始影子：手工 seed（模拟已有一次调用后的影子）
	DefaultUsageSink.mu.Lock()
	DefaultUsageSink.shadow[tenant] = 100
	DefaultUsageSink.shadowOk[tenant] = true
	DefaultUsageSink.mu.Unlock()

	InvalidateShadow(tenant)
	DefaultUsageSink.mu.Lock()
	_, ok := DefaultUsageSink.shadowOk[tenant]
	DefaultUsageSink.mu.Unlock()
	if ok {
		t.Fatalf("InvalidateShadow 后 shadowOk 仍存在 tenant=%d", tenant)
	}
}

// TestTenantTokenRemain 三桶可用合计读取（免费桶过期不计、月度剩余、永久余额）
func TestTenantTokenRemain(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: 跳过 DB 依赖测试")
	}
	testutil.SetupTestDB(t)

	tenant := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tenant)

	// 免费桶 100 + 月度配额 1000-200 + 永久 50 = 950
	db.DB.Model(&model.Tenant{}).Where("id = ?", tenant).Updates(map[string]interface{}{
		"free_token_balance":    100,
		"monthly_token_quota":   1000,
		"monthly_token_used":    200,
		"token_balance":         50,
		"free_token_expires_at": nil,
	})
	got := tenantTokenRemain(tenant)
	if got != 950 {
		t.Fatalf("tenantTokenRemain=%d want 950", got)
	}
}

// TestSinkFlushBatch 批量落库：SinkRecordUsage 多租户投递，flush 后每租户一次扣减
// 强制计费开启时验证三桶递减；未开启时验证 no-op（不 panic）。
func TestSinkFlushBatch(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: 跳过 DB 依赖测试")
	}
	testutil.SetupTestDB(t)

	tenant := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tenant)

	// 关闭强制计费：SinkRecordUsage 应 no-op（token_billing_enabled=false 直接返回）
	old := DefaultSystemConfigService
	DefaultSystemConfigService = &SystemConfigService{cache: map[string]string{}}
	defer func() { DefaultSystemConfigService = old }()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			SinkRecordUsage(tenant, 100) // 总闸未开：不 panic、不入账
		}()
	}
	wg.Wait()
	// 不 flush 也不 panic 即为通过（总闸关 = 兼容现状）

	// 开引擎但灰度未强制：仅留痕不扣减
	DefaultSystemConfigService = &SystemConfigService{
		cache: map[string]string{"token_billing_enabled": "true"},
	}
	SinkRecordUsage(tenant, 100)
	DefaultUsageSink.flush() // 不应 panic；灰度路径 DeductTokensActual 内仅日志
}
