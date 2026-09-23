// Package model 业务领域模型与 GORM 表结构定义（SaaS 多租户，tenant_id 隔离，写入经自动盖章回调）。
package model

import "time"

// ============================================================
// 商机与报价（2026-09-23 商机批）
//
// 这批要补的洞很具体：客户身上只有一个写着"已成交"的阶段字符串，没有金额、没有报价单、
// 也没有"这张单子在谁手里卡了几天"。于是销售管理者看到的永远是**结果**（成交了几个），
// 看不到**过程**（12 张单子在报价环节停了 20 天）。过程看不见，就没法干预——
// 而销售的业绩恰恰是在过程里丢掉的。
//
// 三张表为什么是两张：
//   - opportunities（商机）：一张单子一行，带金额、阶段、预期成交日，是管道的主体；
//   - quotes（报价单）：一张商机的**版本序列**，改版不覆盖旧版，作废也是历史事实。
//     合到一张表里做不到这一点（一条商机可以有 N 张报价单，而商机的阶段只有一个）。
//
// 为什么金额用「分」（amount_cents/total_cents，int64）：与 billing_orders.amount_cents 同量纲。
// 项目里所有"钱"都是分，只有 token 成本用微元（cost_micro）——同一张表里混两种口径
// 迟早有人把 1 万当成 1 百万统计，所以跟钱走钱的规矩，不发明新单位。
//
// 为什么**不在商机上存归属销售**（没有 owner_user_id 这一列）：
// 单子的归属就是客户的归属。快照一份到商机上，就会在客户改派后出现
// "单子还挂在离职顾问名下、新顾问看不见"的孤儿单——销售管道最忌讳这个。
// 读侧一律按客户当前的归属裁剪（见 internal/deal.VisibleDeals）。
//
// 为什么"一个客户同时只能有一张在途商机"要用 DB 部分唯一索引兜底：
// 口径同 G1 会话单活跃（迁移 013）。只靠代码判重，多实例并发建单会撞出两张，
// 而"在途商机数"和"有在途单的客户数"是看板上的两个格子，它们不相等时没人能解释为什么。
// 索引只管非终局行，所以单子丢了可以重开一张新的，历史那张照原样留着。
//
// 与 migrations/023 的关系：建表真源在这里（AutoMigrate 先建列），
// 023 补的是 AutoMigrate 建不出来的**部分索引/部分唯一索引**。
// ============================================================

// 商机阶段码（列 size:24；新增取值请开新值，勿复用旧字面量——历史行按它读）
const (
	DealStageLead        = "lead"        // 线索确认：还没问清需求
	DealStageQualified   = "qualified"   // 需求确认：要什么、预算多少已问过
	DealStageQuoted      = "quoted"      // 已报价：报价单发出去了
	DealStageNegotiating = "negotiating" // 商务谈判：价格/条款在拉锯
	DealStageWon         = "won"         // 成交（终局）
	DealStageLost        = "lost"        // 流失（终局）
)

// DealStages 管道的**顺序**：数组下标即阶段深浅，推进只能往深走（判据见 deal.CanMoveStage）。
// 终局两态（won/lost）不在顺序里参与比较，单独由 IsDealTerminal 判。
var DealStages = []string{
	DealStageLead,
	DealStageQualified,
	DealStageQuoted,
	DealStageNegotiating,
}

// DealStageNames 阶段的中文显示名。**由后端下发而不是前端各写一份**：
// 看板、名单、导出三处要显示同一个词，前端各写一份迟早出现"谈判中/商务谈判"两个说法。
var DealStageNames = map[string]string{
	DealStageLead:        "线索确认",
	DealStageQualified:   "需求确认",
	DealStageQuoted:      "已报价",
	DealStageNegotiating: "商务谈判",
	DealStageWon:         "成交",
	DealStageLost:        "流失",
}

// 商机流失原因码（列 size:24；稳定枚举，报表按它分组，不让自由文本毁掉统计）
const (
	DealLostPrice      = "price"       // 价格没谈拢
	DealLostCompetitor = "competitor"  // 输给竞品
	DealLostNoBudget   = "no_budget"   // 预算取消/没有
	DealLostNoDecision = "no_decision" // 决策没定/拖延
	DealLostTimeout    = "timeout"     // 客户失联
	DealLostOther      = "other"       // 其它（备注必填由前端引导，后端不强制）
)

// DealLostReasonNames 流失原因中文名（同上，后端下发）
var DealLostReasonNames = map[string]string{
	DealLostPrice:      "价格没谈拢",
	DealLostCompetitor: "输给竞品",
	DealLostNoBudget:   "预算取消",
	DealLostNoDecision: "决策未定",
	DealLostTimeout:    "客户失联",
	DealLostOther:      "其它",
}

// 商机来源（建单那一刻的渠道快照；与活码归因是"记一次不动"的同一口径）
const (
	DealSourceManual      = "manual"      // 人工建单
	DealSourceAcquisition = "acquisition" // 由获客活码带来的客户开出的单
	DealSourceAI          = "ai"          // AI 在对话里识别到报价/成交信号自动建单（热开关，默认关）
)

// Opportunity 一张商机（销售管道里的一行，终局后不再改动）
type Opportunity struct {
	ID       uint `gorm:"primaryKey" json:"id"`
	TenantID uint `gorm:"index:idx_deal_tenant_stage,priority:1;not null;default:0" json:"tenant_id"` // 租户ID（归属，不可跨租户读写）
	// CustomerID 单子挂在谁身上。**可见性由这个客户当前的归属销售决定**（见文件头说明），
	// 所以这里不重复存一份 owner。
	CustomerID uint   `gorm:"index:idx_deal_customer;not null" json:"customer_id"`
	Title      string `gorm:"size:120;not null;default:''" json:"title"` // 单子叫什么，如"XT5 两台大客户单"
	// Stage 阶段码。StageEnteredAt 是"这张单子在当前这一步停了几天"的唯一依据——
	// 用 updated_at 判停滞会被任何一次备注编辑冲掉，等于停滞永远为零。
	Stage       string    `gorm:"index:idx_deal_tenant_stage,priority:2;size:24;not null;default:'qualified'" json:"stage"`
	StageAt     time.Time `gorm:"column:stage_entered_at;index:idx_deal_tenant_stage,priority:3" json:"stage_entered_at"`
	AmountCents int64     `gorm:"not null;default:0" json:"amount_cents"` // 预计金额（分），0=还没问到价
	// Source/SourceCode 渠道快照。存字符串码而不是 code_id：成交归因要能回答
	// "这单当初从哪张码进来"，而码停用后统计必须照读（活码批同一口径，见 022 注释）。
	Source     string `gorm:"size:24;not null;default:'manual'" json:"source"`
	SourceCode string `gorm:"size:16;not null;default:''" json:"source_code"`
	// ExpectedCloseAt 预期成交日。可空——"销售不肯填"不是缺陷，硬塞一个默认值才是：
	// 报表会把塞出来的日期当成真实承诺去算超期。
	ExpectedCloseAt *time.Time `json:"expected_close_at"`
	WonAt           *time.Time `json:"won_at"`  // 成交时刻（终局戳）
	LostAt          *time.Time `json:"lost_at"` // 流失时刻（终局戳）
	// LostReason 流失原因码。**只有 lost 才允许有值**：成交单挂个"价格没谈拢"
	// 会让赢单分析整张表作废（判据见 deal.DecideStageMove）。
	LostReason string    `gorm:"size:24;not null;default:''" json:"lost_reason"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// TableName 显式表名（与 023 迁移一致，勿依赖复数推断）
func (Opportunity) TableName() string { return "opportunities" }

// 报价单状态（列 size:16）
const (
	QuoteStatusDraft      = "draft"      // 草稿：还没发出去，可改
	QuoteStatusSent       = "sent"       // 已发出：内容锁定，要改只能出新版
	QuoteStatusAccepted   = "accepted"   // 客户接受
	QuoteStatusDeclined   = "declined"   // 客户拒绝
	QuoteStatusSuperseded = "superseded" // 被新版取代（历史行，不参与"当前报价"读取）
	QuoteStatusVoid       = "void"       // 人工作废
	QuoteStatusExpired    = "expired"    // 过了有效期（sweep 落库，读侧不实时改写）
)

// QuoteStatusNames 报价单状态中文名（后端下发，同阶段名口径）
var QuoteStatusNames = map[string]string{
	QuoteStatusDraft:      "草稿",
	QuoteStatusSent:       "已发出",
	QuoteStatusAccepted:   "已接受",
	QuoteStatusDeclined:   "已拒绝",
	QuoteStatusSuperseded: "已被新版取代",
	QuoteStatusVoid:       "已作废",
	QuoteStatusExpired:    "已过期",
}

// QuoteLine 报价明细的一行。**只存名称/数量/单价，小计与合计一律后端算**：
// 前端传上来的合计从来不可信（改一行忘改合计是常态），
// 而报价单是要给客户看的钱数，错一位就是商务事故。
type QuoteLine struct {
	Name       string `json:"name"`        // 项目名，如"XT5 豪华版"
	Qty        int    `json:"qty"`         // 数量（>0）
	UnitCents  int64  `json:"unit_cents"`  // 单价（分，>=0）
	TotalCents int64  `json:"total_cents"` // 小计=Qty×UnitCents（后端回填，读时以此为准）
}

// Quote 一张报价单（同商机内版本递增，改版不覆盖）
type Quote struct {
	ID       uint `gorm:"primaryKey" json:"id"`
	TenantID uint `gorm:"not null;default:0" json:"tenant_id"`
	// OpportunityID 指向 opportunities.id。部分唯一索引只允许一张"活着"的单
	// （draft/sent），见 023——两张在途报价单等于让顾问不知道哪张发给了客户。
	OpportunityID uint   `gorm:"index:idx_quote_deal;not null" json:"opportunity_id"`
	Version       int    `gorm:"not null;default:1" json:"version"`        // 第几版（1 起）
	Lines         string `gorm:"type:text;not null;default:'[]'" json:"-"` // 明细 JSON（列表接口不下发，详情才给）
	TotalCents    int64  `gorm:"not null;default:0" json:"total_cents"`    // 合计（分，后端算）
	Status        string `gorm:"index:idx_quote_status;size:16;not null;default:'draft'" json:"status"`
	// ValidUntil 报价有效期。可空=不设到期；过期由 sweep 落库为 expired，
	// 读侧不改写数据库（读接口做写操作会让并发读取互相踩，也让统计口径随谁先看而漂移）。
	ValidUntil *time.Time `json:"valid_until"`
	CreatedBy  uint       `gorm:"not null;default:0" json:"created_by"` // 开单的人（审计维度，不参与可见性）
	SentAt     *time.Time `json:"sent_at"`
	DecidedAt  *time.Time `json:"decided_at"` // 客户接受/拒绝的时刻
	Note       string     `gorm:"size:255;not null;default:''" json:"note"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// TableName 显式表名
func (Quote) TableName() string { return "quotes" }
