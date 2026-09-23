// AI 询价信号自动开单（商机批 · 批次2 补，2026-09-24）。
//
// 挂点为什么是询价这一路：客户主动问价是全对话里最强的一段成交意图，也正是最容易掉的那一环——
// AI 答完价，话题就交给顾问了，而顾问看到的只是一句"多少钱"。这张自动单不是业绩，
// 是给顾问台留一个"这个人问过量、值得跟"的落点（阶段停在"需求确认"，等人工核实后再推）。
//
// 三道护栏（顺序即成本顺序，先做便宜的判断）：
//  1. 开关 deal_auto_open_enabled **出厂 false**（口径同主动触达批 outreach_enabled）：
//     一开就是"每个询价客户自动多一张单"，顾问台会先被没人核对过的自动单灌满，
//     看清自动单质量再放量。
//  2. 用 EnsureOpenDeal 而不是 CreateDeal：同一客户反复问价是常态，"已有在途单"对自动链
//     是**正常结果**不是错误——复用的那张带着人工已经推到的阶段，不会被自动链拉回起点。
//  3. 失败只记日志、不回传：客户的回复在计费与承诺上都优先于这张副产物，
//     绝不为它把回信拖没（也不因它把 200 变成 500）。
//
// 句柄约定：调用方传 db.RQ(c)。本函数只跑一条写入，无需 isolate（见 internal/deal 文件头），
// 但租户 ID 一律**显式入列**——同一段判据将来也要能被后台链（无 gin ctx）复用。
package api

import (
	"log"
	"strings"

	"gorm.io/gorm"

	"ai-scrm/internal/deal"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
)

// dealAutoOpenTitlePrefix 自动单的标题前缀（列表里一眼分辨"这张是 AI 开的、那张是人开的"）。
// 中文前缀是**给运营看的**，不是判据；判据是 source=ai 列。
const dealAutoOpenTitlePrefix = "AI 询价 · "

// dealAutoOpenTitle 拼自动单标题：整条按字素裁到标题上限内。
// 超长客户名不得把这条写入顶成 400——这是后台链上的静默失败，没人会来点"重试"。
func dealAutoOpenTitle(customerName string) string {
	name := strings.TrimSpace(customerName)
	if name == "" {
		name = "未命名客户" // 匿名开的号常没名字，标题总得说清是谁的单
	}
	title := []rune(dealAutoOpenTitlePrefix + name)
	if len(title) > deal.MaxTitleRunes {
		title = title[:deal.MaxTitleRunes]
	}
	return string(title)
}

// autoOpenDealFromAI 询价信号 → 在途商机（幂等）。开关未开时**一次查询都不发**。
func autoOpenDealFromAI(gdb *gorm.DB, tenantID, customerID uint, customerName string) {
	if gdb == nil || tenantID == 0 || customerID == 0 {
		return // 无租户语境即放弃：宁可少一张单，也不写出一张归属不明的单
	}
	svc := runtimecfg.DefaultSystemConfigService
	if svc == nil || !svc.GetBoolForTenant(tenantID, "deal_auto_open_enabled", false) {
		return
	}
	res, err := deal.EnsureOpenDeal(gdb, deal.CreateInput{
		TenantID:   tenantID,
		CustomerID: customerID,
		Title:      dealAutoOpenTitle(customerName),
		Stage:      model.DealStageQualified, // 只到"需求确认"：AI 没核对过预算与决策人，不给更高
		Source:     model.DealSourceAI,
	})
	if err != nil {
		log.Printf("[商机自动开单] 客户%d 建单失败（不影响本次回复）: %v", customerID, err)
		return
	}
	if res.Existing {
		log.Printf("[商机自动开单] 客户%d 已有在途商机%d，复用不新建", tenantID, res.Deal.ID)
		return
	}
	log.Printf("[商机自动开单] 客户%d 询价信号已开商机%d（来源标记 ai，等人工核实）", customerID, res.Deal.ID)
}
