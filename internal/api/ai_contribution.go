// AI 贡献度看板接口（D2，PLAN_FIX_2026-09-21 商业化第一缺口）。
//
// 背景：改造前 funnel/contribution 全仓零命中——系统能证明"AI 在回复"，
// 但证明不了"AI 带来了多少生意"。对长周期高复购行业的 SaaS 客户，
// 「AI 独立接待了多少客户、其中多少留了资、多少到店/成交」才是续费的依据。
//
// 指标口径全部收敛到 internal/analytics 的纯函数（可单测），本文件只负责聚合原始计数。
//
// 数据隔离（2026-09-21 收口：全口径统一为同一范围，不再混用）：
//   - db.RQ(c) 自动注入 tenant_id（跨租户不可见）
//   - db.DataScope(c) 注入部门级数据范围（落地在 conversations.assigned_user_id 上）
//     · 租户管理员/平台超管 → 全租户；部门管理员/只读 → 本部门子树；销售 → 自己名下
//
// 改造前的错位：会话/接待量带 DataScope，消息量不带。实测租户 1 的 235 个活跃会话中
// 233 个 assigned_user_id=0（未分配），销售账号于是看到「active_conversations=0 而
// ai_messages=261」——两个数字都对，但口径不同，放在同一张卡片上就是自相矛盾。
// 现统一为「登录者可见会话 ∩ 窗口内有消息」，四项计数同范围同窗口，结构上不可能再错位。
//
// 为何 messages 不能直接套 DataScope：该表没有 assigned_user_id 列，套用会生成非法 SQL。
// 做法是把 DataScope 落在 conversations 子查询上，再让 messages 按 conversation_id 收敛。
package api

import (
	"strconv"
	"time"

	"ai-scrm/internal/analytics"
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// contributionMaxDays 统计窗口上限（防止 days=999999 拉全表打爆 DB）。
const contributionMaxDays = 365

// contributionDefaultDays 默认窗口：30 天（与模型成本核算、包质量视图口径一致）。
const contributionDefaultDays = 30

/*
GetAIContribution 返回租户级 AI 贡献度指标。

用途：管理后台看板「AI 贡献度」卡片 / 销售负责人周会材料 / 续费谈判数据支撑。

查询参数：
  - days：统计窗口天数，默认 30，取值 (0, 365]；非法值回退默认并在响应中体现为 30

返回：schema.AIContribution（含 notes 口径说明，随响应下发避免前端/客户误读）

权限：登录态任意角色（与 /stats/overview 口径一致——这是聚合看板，不含客户明细）。
*/
// GetAIContribution 返回 AI 贡献度指标。
// apidump:ts AIContribution
// AI 贡献度全量指标 + 后端口径说明 notes。
func GetAIContribution(c *gin.Context) {
	days := contributionDefaultDays
	if v := c.Query("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= contributionMaxDays {
			days = n
		}
	}
	// until 在入口取一次并向下传递，保证同一响应内各分位数共享同一时间窗
	// （若每个查询各自 time.Now()，跨秒边界会出现"窗口内会话数"与"窗口内消息数"不同窗的错位）。
	until := time.Now()
	since := until.AddDate(0, 0, -days)

	raw := collectContributionRaw(c, since)

	RespOK(c, "success", analytics.Build(raw, days, since, until))
}

// collectContributionRaw 聚合 AI 贡献度所需的原始计数。
//
// 两条统一原则（2026-09-21 收口，见文件头）：
//  1. 时间窗唯一真相源是 messages.created_at，不是 conversations.last_message_at
//     ——后者是汇总量，并非所有落库路径都维护它（service/message_queue.go:724、
//     chatflow/drive_consumer.go:80 两条异步路径就只写 messages 不回填该列），
//     以它划窗会让"会话数"与同一函数里的"消息数"不同窗。
//  2. 数据范围统一为登录者可见会话（conversations 套 DataScope），messages 经
//     conversation_id 子查询收敛到同一范围，不再出现"一个带范围一个不带"。
func collectContributionRaw(c *gin.Context, since time.Time) analytics.ContributionRaw {
	var raw analytics.ContributionRaw

	// scopeConv：登录者可见的会话 id 集合（DataScope 的唯一落点）
	scopeConv := func() *gorm.DB {
		return db.RQ(c).Scopes(db.DataScope(c)).Model(&model.Conversation{}).Select("id")
	}

	// activeWindowMsgs：窗口内有消息往来的会话 id（时间窗的唯一落点，与下钻共用）
	activeWindowMsgs := func() *gorm.DB { return contributionWindowMsgConvs(c, since) }

	// 1) 窗口内新建会话数：衡量获客流入速度，与"有消息往来"回答的是不同问题。
	//    刻意保留 created_at 口径——"新建"就是"新建"，被活动量取代会失去增速含义。
	db.RQ(c).Scopes(db.DataScope(c)).Model(&model.Conversation{}).
		Where("created_at >= ?", since).Count(&raw.NewConversations)

	// 2) 口径边界：登录者可见 ∩ 窗口内有消息往来的会话数。
	//    接待量与归因都限定在这个集合内，故恒有 AIServed+HumanServed ≤ ActiveConversations
	//    （同一客户可有多个活跃会话，故是 ≤ 而非 =）。
	db.RQ(c).Scopes(db.DataScope(c)).Model(&model.Conversation{}).
		Where("id IN (?)", activeWindowMsgs()).Count(&raw.ActiveConversations)

	// 3+4) 客户级归属与结果归因：与下钻名单同源（见 contributionCustomers）
	counts := analytics.ClassifyContributionCounts(contributionCustomers(c, since))
	raw.AIServedCustomers = counts.AIServed
	raw.HumanServedCustomers = counts.HumanServed
	raw.AILeads = counts.AILeads
	raw.AssistedLeads = counts.AssistedLeads
	raw.AIArrived = counts.AIArrived
	raw.AIOrdered = counts.AIOrdered

	// 5) 消息量结构：与上述同范围同窗口（经 conversation_id 收敛到 DataScope）
	type msgRow struct {
		SenderType string
		N          int64
	}
	var msgRows []msgRow
	db.RQ(c).Model(&model.Message{}).
		Select("sender_type, COUNT(*) AS n").
		Where("created_at >= ?", since).
		Where("conversation_id IN (?)", scopeConv()).
		Group("sender_type").
		Scan(&msgRows)
	for _, m := range msgRows {
		switch m.SenderType {
		case "ai":
			raw.AIMessages = m.N
		case "human":
			raw.HumanMessages = m.N
		case "customer":
			raw.CustomerMessages = m.N
		}
	}

	// 6) 待接管会话数（瞬时值，不受时间窗限制——它是运营当下要处理的事）
	db.RQ(c).Scopes(db.DataScope(c)).Model(&model.Conversation{}).
		Where("pending_handoff = ?", true).Count(&raw.PendingHandoffNow)

	return raw
}

// contributionWindowMsgConvs 「窗口内有消息往来」的会话 id 子查询——时间窗的唯一落点。
//
// 抽成包级函数的原因：看板计数与下钻名单两条路径必须用同一个子查询，
// 各写一遍就会不同窗（D2 抓到过的同类错位：会话数与消息数不同窗）。
func contributionWindowMsgConvs(c *gin.Context, since time.Time) *gorm.DB {
	return db.RQ(c).Model(&model.Message{}).
		Select("conversation_id").
		Where("created_at >= ?", since)
}

// contributionCustomers 取本次「窗口 + 登录者数据范围」内每个客户的接待归属与当前阶段。
//
// 这是卡片六个客户级数字与下钻名单的**唯一真相源**：计数走它、取名单也走它，
// 结构上就不可能出现"卡片说 8 个、点进去 20 行"。
//
// 归属判据用 last_human_reply_at（会话级既有列）而非逐客户扫 messages：
// 一次聚合胜过 N 次查询，且该列由人工回复路径统一维护，是既有的单一事实源。
// 已知近似：该列不随窗口重置，人工仅在窗口外介入过的客户会被计为"人工参与"（口径已写入 notes）。
func contributionCustomers(c *gin.Context, since time.Time) []analytics.ContributionCustomer {
	type custRow struct {
		CustomerID uint
		HasHuman   int
	}
	var rows []custRow
	db.RQ(c).Scopes(db.DataScope(c)).Model(&model.Conversation{}).
		Select("customer_id, MAX(CASE WHEN last_human_reply_at IS NULL THEN 0 ELSE 1 END) AS has_human").
		Where("id IN (?)", contributionWindowMsgConvs(c, since)).
		Group("customer_id").
		Scan(&rows)
	if len(rows) == 0 {
		return nil
	}

	// 旅程阶段一次取回（阶段集合判定在 analytics 侧，避免 SQL/Go 两侧各写一遍）
	ids := make([]uint, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.CustomerID)
	}
	type stageRow struct {
		ID           uint
		JourneyStage string
	}
	var stageRows []stageRow
	db.RQ(c).Scopes(db.DataScope(c)).Model(&model.Customer{}).
		Select("id, journey_stage").Where("id IN ?", ids).Scan(&stageRows)
	stageOf := make(map[uint]string, len(stageRows))
	for _, s := range stageRows {
		stageOf[s.ID] = s.JourneyStage
	}

	out := make([]analytics.ContributionCustomer, 0, len(rows))
	for _, r := range rows {
		out = append(out, analytics.ContributionCustomer{
			CustomerID: r.CustomerID,
			HasHuman:   r.HasHuman == 1,
			Stage:      stageOf[r.CustomerID],
		})
	}
	return out
}
