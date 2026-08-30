// Package service：internal/service 模块（自动补包注释）。
package service

import (
	"sync"
	"sync/atomic"
	"testing"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
)

// TestEstimateCostMicro 成本估算纯函数（P2-1：usage_service 计量单测）
// 验证：硅基流动档默认单价 8000 微元/千token × markup 1.5；智谱档 15000。
// 纯函数，无需 DB：本地构造 SystemConfigService 空缓存（走默认值分支）。
func TestEstimateCostMicro(t *testing.T) {
	// 本地构造空缓存服务，GetInt/GetFloat 走默认值（不触库）
	DefaultSystemConfigService = &SystemConfigService{cache: map[string]string{}}
	defer func() { DefaultSystemConfigService = nil }()

	cases := []struct {
		provider    string
		totalTokens int
		wantMicro   int64 // 期望精确值：tokens/1000 × 单价 × 1.5
	}{
		{"siliconflow", 1000, 12000}, // 1 × 8000 × 1.5
		{"zhipu", 1000, 22500},       // 1 × 15000 × 1.5
		{"unknown", 2000, 24000},     // 2 × 8000 × 1.5
	}
	for _, c := range cases {
		got := estimateCostMicro(c.provider, c.totalTokens)
		if got != c.wantMicro {
			t.Errorf("estimateCostMicro(%q,%d)=%d want %d", c.provider, c.totalTokens, got, c.wantMicro)
		}
	}
}

// TestConsumeAIQuotaNonEnforced 灰度模式下恒放行且累计（P2-1）
// 依赖 DB：无可用连接时跳过（项目零 DB 单测哲学，纯逻辑用 mask/embedding 测试覆盖）。
func TestConsumeAIQuotaNonEnforced(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: 跳过 DB 依赖测试")
	}
	if err := db.Init(); err != nil {
		t.Skipf("DB 不可用，跳过: %v", err)
	}

	tenant := createTestTenant(t)
	defer cleanupTestTenant(t, tenant)

	before := getUsedAI(tenant)
	if !ConsumeAIQuota(tenant) {
		t.Fatalf("灰度模式应放行")
	}
	after := getUsedAI(tenant)
	if after != before+1 {
		t.Errorf("used_ai_calls 应 +1: before=%d after=%d", before, after)
	}
}

// TestConsumeAIQuotaConcurrentOverflow 并发超额（P2-1 核心）
// 月配额设小（如 50），100 并发请求，成功次数应 ≤ 50（条件 UPDATE 原子预留）。
// 依赖 DB：无连接跳过。
func TestConsumeAIQuotaConcurrentOverflow(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: 跳过 DB 依赖测试")
	}
	if err := db.Init(); err != nil {
		t.Skipf("DB 不可用，跳过: %v", err)
	}

	tenant := createTestTenant(t)
	defer cleanupTestTenant(t, tenant)

	// 设月配额 50、已用 0、余额 0（强制走月配额闸门）
	setTenantQuota(t, tenant, 50, 0, 0)
	// 强制 enforced=true 走原子预留路径
	old := DefaultSystemConfigService
	DefaultSystemConfigService = &SystemConfigService{
		cache: map[string]string{"billing_enforced": "true"},
	}
	defer func() { DefaultSystemConfigService = old }()

	const N = 100
	var okCount int32
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			if ConsumeAIQuota(tenant) {
				atomic.AddInt32(&okCount, 1)
			}
		}()
	}
	wg.Wait()
	if okCount > 50 {
		t.Errorf("并发超额：成功次数 %d > 配额 50（条件UPDATE未生效）", okCount)
	}
	if okCount == 0 {
		t.Errorf("并发超额：成功次数 0（配额未生效？）")
	}
}

// ---- test helpers ----

// getUsedAI 获取（自动补注释，原为缺注释的顶层声明）。
func getUsedAI(tenant uint) int64 {
	var used int64
	db.DB.Model(&model.Tenant{}).Where("id = ?", tenant).
		Pluck("used_ai_calls", &used)
	return used
}

// setTenantQuota 设置（自动补注释，原为缺注释的顶层声明）。
func setTenantQuota(t *testing.T, tenant uint, maxAI, balance, used int) {
	if err := db.DB.Model(&model.Tenant{}).Where("id = ?", tenant).Updates(map[string]interface{}{
		"max_ai_calls":    maxAI,
		"ai_call_balance": balance,
		"used_ai_calls":   used,
	}).Error; err != nil {
		t.Fatalf("setTenantQuota: %v", err)
	}
}

// createTestTenant 创建（自动补注释，原为缺注释的顶层声明）。
func createTestTenant(t *testing.T) uint {
	var cnt int64
	db.DB.Model(&model.Tenant{}).Where("code = ?", "unit_test_tenant").Count(&cnt)
	if cnt > 0 {
		var id uint
		db.DB.Model(&model.Tenant{}).Where("code = ?", "unit_test_tenant").Pluck("id", &id)
		return id
	}
	tt := &model.Tenant{Name: "单元测试租户", Code: "unit_test_tenant", Status: "active",
		MaxAICalls: 1000, UsedAICalls: 0, AICallBalance: 0}
	if err := db.DB.Create(tt).Error; err != nil {
		t.Fatalf("createTestTenant: %v", err)
	}
	return tt.ID
}

// cleanupTestTenant 函数（自动补注释，原为缺注释的顶层声明）。
func cleanupTestTenant(t *testing.T, id uint) {
	_ = db.DB.Where("id = ?", id).Delete(&model.Tenant{}).Error
}
