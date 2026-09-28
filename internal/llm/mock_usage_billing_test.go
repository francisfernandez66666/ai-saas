// 模拟模式的虚拟计量单测（FIX-M, 2026-09-28）
//
// 缺陷形态不是"算错账"而是"整条账不存在"：模拟模式在真正调模型**之前**就短路返回模板话术，
// 于是 usage_ledger 零行、三桶零扣减。「③免费桶→①订阅额度→②余额」这条商业红线的中间三格
// 因此从来没有机器判据——端到端只能在接了真模型 Key 的环境里"看起来过了"，
// 本机/CI（零凭证）跑到的只是最后那格"三桶全空→降级"。
//
// 本文件钉三件事，每件都对应一个会静默复发的写法：
//  1. **键出厂为 0 时行为逐字节不变**：不落账、不扣桶。
//     缺这条，"给测试开虚拟用量"就等于给生产埋一个"mock 也扣钱"的默认态。
//  2. **键生效时走的是同一条落账腿**：台账行 provider='mock' 且扣减额恰为配置值，
//     随后 UsageSink 收账真扣③免费桶。判据取库里的列值，不取日志串——
//     日志落哪个文件是部署细节（AGENTS 已登记过一次因此假失败的先例）。
//  3. **真实模式忽略该键**：非模拟模式下即使配置残留也不凭空造账。
//     这条防的是"把判据同时挂在两条路上"，让 mock 开关本身失去意义。
package llm

import (
	"context"
	"strconv"
	"testing"
	"time"

	"ai-scrm/internal/billing"
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// fillBuckets 把③免费桶置成 5000（①②置零）：只给一条桶有余额，扣减归属才是唯一的。
// 返回前必做前置自检——三桶没落上时后面所有"扣了多少"的断言都会在 0==0 上假绿。
func fillBuckets(t *testing.T, tid uint) {
	t.Helper()
	if err := db.DB.Model(&model.Tenant{}).Where("id = ?", tid).Updates(map[string]any{
		"free_token_balance": 5000, "free_token_expires_at": time.Now().Add(24 * time.Hour),
		"monthly_token_quota": 0, "monthly_token_used": 0, "token_balance": 0,
	}).Error; err != nil {
		t.Fatalf("置三桶前置失败: %v", err)
	}
	if got := freeTokensOf(t, tid); got != 5000 {
		t.Fatalf("前置破坏：③免费桶读到 %d，期望 5000（余额没落上，后面的扣减断言全是空转）", got)
	}
}

// freeTokensOf 回读③免费桶余额（判据取列值，不取日志）
func freeTokensOf(t *testing.T, tid uint) int64 {
	t.Helper()
	var row model.Tenant
	if err := db.DB.Select("free_token_balance").First(&row, tid).Error; err != nil {
		t.Fatalf("回读租户三桶失败: %v", err)
	}
	return row.FreeTokenBalance
}

// oneMockReply 跑一次真实回复链路（模拟或真调用由热配决定），返回话术。
// 假客户 ID 与既有 reply_guard_matrix 用例同源：GenerateAIReply 只读画像字段，
// 计量腿里 customer_id 是留痕列、不是外键，无需为一个客户建行。
func oneMockReply(t *testing.T, tid uint) string {
	t.Helper()
	customer := newGuardCustomer(tid, model.JourneyAIConnected)
	conv := newGuardConversation(t, tid, customer.ID, 0, false)
	return GenerateAIReply(context.Background(), customer, conv.ID,
		"续航实际能跑多少", strategyOut(), nil)
}

// waitUsageLedger 等异步台账落账（RecordUsage 是 best-effort goroutine，不等就是在空集上比）
func waitUsageLedger(t *testing.T, tid uint, provider string) model.UsageLedger {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var row model.UsageLedger
		err := db.DB.Where("tenant_id = ? AND provider = ?", tid, provider).
			Order("id DESC").First(&row).Error
		if err == nil {
			return row
		}
		if time.Now().After(deadline) {
			t.Fatalf("等 usage_ledger(provider=%s) 超时: %v", provider, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// countLedger 数某租户指定 provider 的台账行数（"不该有账"那条断言的判据）
func countLedger(t *testing.T, tid uint, provider string) int64 {
	t.Helper()
	var n int64
	if err := db.DB.Model(&model.UsageLedger{}).
		Where("tenant_id = ? AND provider = ?", tid, provider).Count(&n).Error; err != nil {
		t.Fatalf("数台账(provider=%s)失败: %v", provider, err)
	}
	return n
}

// TestMockModeZeroUsageKeepsBehaviorUnchanged 键出厂 0 ⇒ 与改造前完全一致：
// 话术仍是规则兜底、台账零行、三桶分毫不动。
//
// 反证方向：若实现改成"mock 固定记 1000 token"，本用例的"台账零行 + 余额不变"两条会同时红，
// 而端到端会在每个没开虚拟用量的租户上凭空扣走额度——这条就是防那件事的。
func TestMockModeZeroUsageKeepsBehaviorUnchanged(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	fillBuckets(t, tid)

	guardConfig(t, map[string]string{"mock_mode": "true", "ai_mock_usage_tokens": "0"})
	srv := startFakeAI(t, func(map[string]any) string { return "不该被调用" })
	installGuardEnv(t, srv.URL, true)
	billing.InvalidateShadow(tid)

	if got := oneMockReply(t, tid); got == "" {
		t.Error("模拟模式必须给出模板话术，实际空串")
	}
	// 异步腿是 best-effort：给它一次自然落账的机会，再断"确实没有"
	time.Sleep(300 * time.Millisecond)

	if rows := countLedger(t, tid, "mock"); rows != 0 {
		t.Errorf("键=0 时落了 %d 行 mock 台账——虚拟计量默认开了，现网每个 mock 租户都会被凭空扣额度", rows)
	}
	if after := freeTokensOf(t, tid); after != 5000 {
		t.Errorf("键=0 时③免费桶从 5000 变成 %d——默认态必须零扣减", after)
	}
}

// TestMockModeVirtualUsageBillsThreeBucket 键>0 ⇒ 虚拟用量走同一条落账腿：
// 台账一行 provider='mock' 且 total=配置值，随后 sink 收账真扣③免费桶同样的数额。
func TestMockModeVirtualUsageBillsThreeBucket(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	fillBuckets(t, tid)

	const virtual = 1200
	// 前置自检：可用性闸必须放行，否则回复走不到计量那一步（断言会在降级分支上空转）
	if !billing.CheckTokenAvailability(tid) {
		t.Fatal("前置破坏：三桶有钱但可用性检查拒了，计量分支不会被执行")
	}

	// 计量引擎双闸都要开：总闸关着只落账不扣费，强制关着只留痕（灰度语义）
	guardConfig(t, map[string]string{
		"mock_mode": "true", "ai_mock_usage_tokens": strconv.Itoa(virtual),
		"token_billing_enabled": "true", "billing_enforced": "true",
	})
	srv := startFakeAI(t, func(map[string]any) string { return "不该被调用" })
	installGuardEnv(t, srv.URL, true)
	// 影子是进程级缓存，可能是别的用例 seed 的旧值；不失效则 decrShadow 按旧影子判欠账，
	// flush 会走"余额不足→挂账回滚"，红的是前置而不是被测分支。
	billing.InvalidateShadow(tid)

	if got := oneMockReply(t, tid); got == "" {
		t.Fatal("模拟模式必须给出模板话术")
	}

	row := waitUsageLedger(t, tid, "mock")
	if row.TotalTokens != virtual {
		t.Errorf("台账 total_tokens=%d，期望 %d（虚拟用量没按配置落账）", row.TotalTokens, virtual)
	}
	if row.Stage != "reply" {
		t.Errorf("台账 stage=%q，期望 reply", row.Stage)
	}

	// 强制收账：生产由周期 flusher/停机序列触发，单测里 sink 未起跑，只能显式 flush
	billing.DefaultUsageSink.Stop()

	if after := freeTokensOf(t, tid); after != 5000-virtual {
		var retry int64
		if err := db.DB.Model(&model.UsageFlushRetry{}).
			Where("tenant_id = ?", tid).Count(&retry).Error; err != nil {
			t.Fatalf("查挂账行失败: %v", err)
		}
		t.Errorf("③免费桶=%d，期望 %d（虚拟用量未扣减＝三桶级联在 mock 下仍不可判）；本租户挂账行=%d",
			after, 5000-virtual, retry)
	}
}

// TestMockUsageKeyIgnoredOutsideMockMode 真实模式忽略该键：
// 非模拟模式下即使配置残留，也不得凭空造一条 provider='mock' 的账，
// 真实调用报的 18 token 仍照原样扣（假端点 usage: prompt 11 + completion 7）。
func TestMockUsageKeyIgnoredOutsideMockMode(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	fillBuckets(t, tid)
	if !billing.CheckTokenAvailability(tid) {
		t.Fatal("前置破坏：三桶有钱但可用性检查拒了")
	}

	guardConfig(t, map[string]string{
		"mock_mode": "false", "ai_mock_usage_tokens": "999999",
		"token_billing_enabled": "true", "billing_enforced": "true",
	})
	srv := startFakeAI(t, func(map[string]any) string { return "真实模型的正常回复" })
	installGuardEnv(t, srv.URL, true)
	billing.InvalidateShadow(tid)

	if got := oneMockReply(t, tid); got == "" {
		t.Fatal("真实模式必须返回模型话术")
	}
	if row := waitUsageLedger(t, tid, "siliconflow"); row.TotalTokens != 18 {
		t.Errorf("真实调用台账 total=%d，期望 18（假端点报的用量）", row.TotalTokens)
	}
	billing.DefaultUsageSink.Stop()

	if rows := countLedger(t, tid, "mock"); rows != 0 {
		t.Errorf("非模拟模式造出 %d 行 mock 台账——该键只能在 mock 生效，否则扣减被凭空放大", rows)
	}
	if after := freeTokensOf(t, tid); after != 5000-18 {
		t.Errorf("③免费桶=%d，期望 %d（真实用量 18 应扣、虚拟 999999 不该扣）", after, 5000-18)
	}
}
