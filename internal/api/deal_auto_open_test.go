// AI 询价自动开单钩子测试（商机批 · 批次2 补，2026-09-24）。
//
// 这个钩子的危险不在"开不出单"，而在"不该开的时候开了"：它跑在客户对话链上，
// 一次路由判定就会写一张租户可见的商机。所以用例的正反两面同等重要——
//
//	关（出厂默认）：一次查询都不该发，商机表必须还是空的；
//	开：只此一张，且重复询价**复用**而不是刷单。
//
// 依赖本地 PostgreSQL（testutil.SetupTestDB，CI 下 DB 不可用直接 Fatal，本地无 DB 自动 Skip）；
// 不调用任何真实 AI——这里测的是"信号落地成什么"，不是"信号怎么识别"。
package api

import (
	"strings"
	"testing"

	"ai-scrm/internal/db"
	"ai-scrm/internal/deal"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/testutil"
)

// dealAutoCfg 给某租户写一条热配并让读取层看到（钩子读的是 GetBoolForTenant，
// 只写库不 Reload 的话，测的还是缓存里的旧值——那种绿没有任何意义）。
func dealAutoCfg(t *testing.T, tid uint, key, value string) {
	t.Helper()
	runtimecfg.InitSystemConfigService() // 单测无启动序，服务自己拉起来（幂等）
	svc := runtimecfg.DefaultSystemConfigService
	if svc == nil {
		t.Skip("系统配置服务未就绪，跳过热配用例")
	}
	row := model.SystemConfig{
		TenantID: tid, Category: "strategy", Key: key, Value: value,
		ValueType: "bool", Description: "单测注入", DefaultValue: value,
	}
	if err := db.DB.Where("tenant_id = ? AND key = ?", tid, key).Delete(&model.SystemConfig{}).Error; err != nil {
		t.Fatalf("清理旧配置失败: %v", err)
	}
	if err := db.DB.Create(&row).Error; err != nil {
		t.Fatalf("写租户热配失败: %v", err)
	}
	svc.Reload()
	t.Cleanup(func() {
		db.DB.Where("tenant_id = ? AND key = ?", tid, key).Delete(&model.SystemConfig{})
		svc.Reload()
	})
}

// dealAutoCount 这个客户名下的商机行数
func dealAutoCount(t *testing.T, tid, cid uint) int64 {
	t.Helper()
	var n int64
	if err := db.DB.Model(&model.Opportunity{}).
		Where("tenant_id = ? AND customer_id = ?", tid, cid).Count(&n).Error; err != nil {
		t.Fatalf("统计商机失败: %v", err)
	}
	return n
}

// TestDealAutoTitleWithinLimit 标题按整条裁到上限内，空名有兜底。
// 上限破了不报错而是**静默丢一张单**（后台链没人点重试），所以这条得单独钉。
func TestDealAutoTitleWithinLimit(t *testing.T) {
	long := dealAutoOpenTitlePrefix + strings.Repeat("客", deal.MaxTitleRunes+20)
	if got := dealAutoOpenTitle(long); len([]rune(got)) > deal.MaxTitleRunes {
		t.Fatalf("标题未裁到上限内：%d 字（上限 %d）", len([]rune(got)), deal.MaxTitleRunes)
	} else if r := deal.ValidateTitle(got); r != "" {
		t.Fatalf("裁出来的标题应过校验，实得 reason=%s", r)
	}
	if got := dealAutoOpenTitle("   "); !strings.Contains(got, "未命名客户") {
		t.Fatalf("空客户名应有兜底称呼，实得 %q", got)
	}
	if got := dealAutoOpenTitle("张三"); got != dealAutoOpenTitlePrefix+"张三" {
		t.Fatalf("正常客户名不该被改写：%q", got)
	}
}

// TestDealAutoOpenDisabledByDefault 开关没开（出厂默认）时**一条都不落**。
// 这是本钩子最要紧的一条：默认态必须真的默认，否则"忘了关就自动群发"的翻版
// 就是"忘了关就自动灌单"。
func TestDealAutoOpenDisabledByDefault(t *testing.T) {
	tid := dealTenant(t, "deal_auto_off")
	defer testutil.CleanupTenant(t, tid)
	cid := dealSeedCustomer(t, tid, "问价未开单0001", 0)

	autoOpenDealFromAI(db.DB, tid, cid, "问价未开单")
	if n := dealAutoCount(t, tid, cid); n != 0 {
		t.Fatalf("开关未开却建了 %d 张单（默认关失守）", n)
	}
	// 反向自证：同一个客户，开关一开就必须落得下来。
	// 少了这一句，上一条用例在"钩子根本没接线/写错了表"上也是绿的。
	dealAutoCfg(t, tid, "deal_auto_open_enabled", "true")
	autoOpenDealFromAI(db.DB, tid, cid, "问价未开单")
	if n := dealAutoCount(t, tid, cid); n != 1 {
		t.Fatalf("开关打开后应建 1 张，实得 %d（钩子没接线？）", n)
	}
}

// TestDealAutoOpenReusesOpenDeal 重复询价只留一张单，且这张单的来源/阶段/归属都对：
// source=ai（列表里能一眼分辨是 AI 开的）、stage=qualified（AI 没核对过预算，不给更高阶段）。
// 已终局的单**不挡**新建——上一单去年就流失了，今年重新问价该开新单（判据在 023 部分唯一索引）。
func TestDealAutoOpenReusesOpenDeal(t *testing.T) {
	tid := dealTenant(t, "deal_auto_on")
	defer testutil.CleanupTenant(t, tid)
	dealAutoCfg(t, tid, "deal_auto_open_enabled", "true")
	cid := dealSeedCustomer(t, tid, "反复问价0002", 0)

	for i := 0; i < 3; i++ {
		autoOpenDealFromAI(db.DB, tid, cid, "反复问价")
	}
	var rows []model.Opportunity
	if err := db.DB.Where("tenant_id = ? AND customer_id = ?", tid, cid).Find(&rows).Error; err != nil {
		t.Fatalf("查商机失败: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("三次询价应只留 1 张在途单，实得 %d 张（幂等入口没生效）", len(rows))
	}
	got := rows[0]
	if got.Source != model.DealSourceAI {
		t.Fatalf("来源应标 ai，实得 %q", got.Source)
	}
	if got.Stage != model.DealStageQualified {
		t.Fatalf("出厂阶段应是需求确认，实得 %q", got.Stage)
	}
	if got.TenantID != tid {
		t.Fatalf("租户归属错成 %d（应为 %d）", got.TenantID, tid)
	}
	if !strings.HasPrefix(got.Title, dealAutoOpenTitlePrefix) {
		t.Fatalf("标题应带自动单前缀，实得 %q", got.Title)
	}
}

// TestDealAutoOpenGuardsBadInput 无租户/无客户/空句柄一律直接放弃，不 panic 也不写脏行。
// 这条链上拿到的 ID 来自对话上下文，出 0 比出错更常见（匿名会话合并失败时就是 0）。
func TestDealAutoOpenGuardsBadInput(t *testing.T) {
	tid := dealTenant(t, "deal_auto_guard")
	defer testutil.CleanupTenant(t, tid)
	dealAutoCfg(t, tid, "deal_auto_open_enabled", "true")

	var n int64
	autoOpenDealFromAI(nil, tid, 1, "空句柄")   // 不 panic
	autoOpenDealFromAI(db.DB, 0, 1, "无租户")   // 归属不明的单宁可不建
	autoOpenDealFromAI(db.DB, tid, 0, "无客户") // 同上
	if err := db.DB.Model(&model.Opportunity{}).Where("tenant_id = ?", tid).Count(&n).Error; err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if n != 0 {
		t.Fatalf("非法入参不该落库，实得 %d 行", n)
	}
	// 自证这条用例不是空转：合法入参在同一句柄上必须落得下来
	cid := dealSeedCustomer(t, tid, "合法入参0003", 0)
	autoOpenDealFromAI(db.DB, tid, cid, "合法入参")
	if n = dealAutoCount(t, tid, cid); n != 1 {
		t.Fatalf("合法入参应建 1 张（说明上面的拒不是空转），实得 %d", n)
	}
}
