// 用量预警触达（D3，2026-09-23）：租户的 AI 额度快用完/已用完时，主动说出口。
//
// 为什么需要它（此前真实断点）：用量只有**事后**看板 /admin/usage/summary，
// 而超配额后的产品行为是"静默降级为规则话术"——租户端看到的现象是"AI 变笨了"，
// 不是"我额度用完了"。于是真实结果是客户流失而不是加购，我方还完全不知情。
// 本文件把同一个事实从"看板里等人来看"改成"越档即定向通知其管理员"，
// 并把"哪天说过、说了哪一档、走没走通通道"落成 usage_alerts 行，可审计、可去重。
//
// 三条口径决定（都写进测，防后来人改糊涂）：
//  1. **一租户一指标一轮只发最高那一档**：用量一小时内从 30% 跳到 100% 时，
//     [80,95,100] 三档同时越过，若逐档发就是一轮三封邮件。只发最高档，
//     低档视为已被更高档覆盖（pctHit 内实现）。
//  2. **余额桶只在 0<余额<=阈值 时预警**：余额恰为 0 的租户绝大多数是从来没买过的免费/试用户，
//     那不是"快没钱"而是"本来就没有"，给他们发欠费预警既无行动价值又是骚扰（月度配额档另有覆盖）。
//  3. **平台通道坏了不记账、租户没人可记**：SMTP/群都没配 = 我方基础设施问题，
//     不落 usage_alerts 行，配好后下一轮自愈补发（宁可晚说，不可假装说过）；
//     反之租户压根没有绑定邮箱的管理员 = 它的常态，落一条 channels 为空的行，
//     免得每小时重复一次"命中→发不出→写日志"的噪音。
//
// 依赖方向：本文件只读 db/model/runtimecfg/notify/metrics，不碰 api 层，也不反依赖 chatflow。
package billing

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/metrics"
	"ai-scrm/internal/model"
	"ai-scrm/internal/redisclient"
	"ai-scrm/internal/runtimecfg"
)

// usageAlertLockKey 用量预警扫描的 Redis 锁键（多实例只有一台跑：
// 虽有唯一键兜底，但"两实例各自判定为首次并各发一封"仍是两次真实外呼，锁比事后合并干净）
const usageAlertLockKey = "lock:billing:usage_alert"

// usageAlertLockTTL 锁持有时长：与小时 ticker 同量级，留出余量但不跨轮
const usageAlertLockTTL = 50 * time.Minute

// tenantUsageRow 一个租户的配额快照（sweep 一趟查询取回，纯裁决函数只吃它，便于单测直接构造）
//
// 列名一律显式 gorm:"column:"：本结构体字段名与列名不是一一对应（MaxAICallsMonthly vs
// max_ai_calls_monthly 这类缩写的转换结果不稳定），靠命名策略猜会把整列扫成零值，
// 而"零值=没配额"是静默不发预警，比报错更难发现。
type tenantUsageRow struct {
	ID                uint   `gorm:"column:id"`
	Name              string `gorm:"column:name"`
	Code              string `gorm:"column:code"`
	UsedAICalls       int    `gorm:"column:used_ai_calls"`
	MaxAICallsMonthly int    `gorm:"column:max_ai_calls_monthly"`
	MonthlyTokenUsed  int64  `gorm:"column:monthly_token_used"`
	MonthlyTokenQuota int64  `gorm:"column:monthly_token_quota"`
	TokenBalance      int64  `gorm:"column:token_balance"`
}

// usageAlertHit 一条"该通知了"的裁决结果
type usageAlertHit struct {
	TenantID  uint
	Metric    string // model.UsageMetric*
	Threshold int    // 命中的档位值（余额口径存的是阈值本身）
	UsagePct  int    // 已用百分比（余额口径 0）
	Remaining int64  // 剩余量
	Label     string // 邮件里的人话指标名
	Exhausted bool   // 是否已耗尽（决定文案与是否惊动平台群）
}

// pctHit 百分比类指标的越档判定：越过 thresholds 中最高一档才返回（低档被覆盖即放弃）。
// max<=0 沿用本仓既有口径=不限额（见 usage_service.go 设计要点），不限则无从预警——
// 对"不限"的租户发"已用 100%"是纯粹的误导。
func pctHit(tenantID uint, metric string, used, max int, thresholds []int, label string) (usageAlertHit, bool) {
	if max <= 0 || used < 0 || len(thresholds) == 0 {
		return usageAlertHit{}, false
	}
	pct := used * 100 / max
	if pct > 100 {
		pct = 100 // 「次」轨仅统计不拦截（扣减闸在 token 桶），用量可超发；邮件里不报 130% 这种怪数
	}
	best := 0
	for _, th := range thresholds {
		if th > 0 && th <= 100 && pct >= th && th > best {
			best = th
		}
	}
	if best == 0 {
		return usageAlertHit{}, false
	}
	return usageAlertHit{
		TenantID: tenantID, Metric: metric, Threshold: best, UsagePct: pct,
		Label: label, Exhausted: used >= max,
	}, true
}

// decideUsageAlerts 纯裁决：给定配额快照与阈值，返回本轮应发的预警（每指标至多一条，取越过档位中的最高档）。
// 无 DB、无时间依赖，故可表驱动单测（越档/未越/不限额/零余额/跨档跳跃全覆盖）。
func decideUsageAlerts(row tenantUsageRow, thresholds []int, balanceBelow int64) []usageAlertHit {
	var hits []usageAlertHit

	// ①「次」旧轨（used_ai_calls / max_ai_calls_monthly）
	if h, ok := pctHit(row.ID, model.UsageMetricMonthlyCalls, row.UsedAICalls, row.MaxAICallsMonthly,
		thresholds, "本月 AI 调用次数配额"); ok {
		h.Remaining = int64(row.MaxAICallsMonthly - row.UsedAICalls)
		hits = append(hits, h)
	}

	// ②月度订阅 token 额度（三桶之①，随月底清零）
	if h, ok := pctHit(row.ID, model.UsageMetricMonthlyTokens, int(row.MonthlyTokenUsed), int(row.MonthlyTokenQuota),
		thresholds, "本月 AI 额度（tokens）"); ok {
		h.Remaining = row.MonthlyTokenQuota - row.MonthlyTokenUsed
		hits = append(hits, h)
	}

	// ③预充值永久余额（三桶之②）：只在 0<余额<=阈值 时提示（口径决定 2）
	if balanceBelow > 0 && row.TokenBalance > 0 && row.TokenBalance <= balanceBelow {
		hits = append(hits, usageAlertHit{
			TenantID: row.ID, Metric: model.UsageMetricTokenBalance, Threshold: int(balanceBelow),
			Remaining: row.TokenBalance, Label: "预充值 AI 余额（tokens）",
			// 只剩阈值一成以内按"即将耗尽"对待：文案升级为"马上就没有了"，并同步平台群
			Exhausted: row.TokenBalance <= balanceBelow/10,
		})
	}
	return hits
}

// usageAlertPeriodKey 账期锚：'YYYY-MM'。
// 为什么百分比指标也要带账期：月度用量每月 1 日清零（ResetAllTenantsMonthlyUsageIfDue），
// 去重锚若不带账期，租户第二个月再用满 80% 就永远不会再被通知——那正是最需要续费通知的时刻。
func usageAlertPeriodKey(now time.Time) string { return now.Format("2006-01") }

// UsageAlertPeriod 当前账期锚（'YYYY-MM'）。API 层要按同一个口径回"本期已通知"，
// 故导出薄封装而不是让调用方各自 time.Format——两处格式串迟早写歪，歪了就查不到行。
func UsageAlertPeriod() string { return usageAlertPeriodKey(time.Now()) }

// SweepUsageAlerts 用量预警巡检（main.go 小时 ticker 调用，Redis TryLock 选主）。
// 返回本轮实际新落库（= 真正说过话）的预警条数；开关关闭直接零开销返回。
func SweepUsageAlerts() int { return sweepUsageAlerts(nil) }

// SweepUsageAlertsForTenants 只巡检指定租户（单测/人工补发用）。
// 为什么要开这个口子而不是让测试直接调全表版：本包单测与开发库共用真库，
// 全表巡检会给**其它真实租户**写进"已通知"的留痕行，而那些通知其实由桩代发了、
// 客户根本没收到——唯一键还会让这个档在本账期内永不再发。这不是测试洁癖，是防污染账本。
func SweepUsageAlertsForTenants(tenantIDs []uint) int { return sweepUsageAlerts(tenantIDs) }

func sweepUsageAlerts(scope []uint) int {
	if !runtimecfg.SafeCfgBool("usage_alert_enabled", false) {
		return 0
	}
	// P2-5 同款处置：Redis 可用时选主，未启用则各实例直跑（唯一键仍能兜住重复）
	// 定向巡检不抢全局锁：它本来就是"给指定几家补一次"的人工/测试动作
	if len(scope) == 0 && redisclient.IsEnabled() {
		h := redisclient.TryLock(usageAlertLockKey, usageAlertLockTTL)
		if h == nil {
			return 0
		}
		defer h.Unlock()
	}

	thresholds := normalizeInts(runtimecfg.SafeCfgIntSlice("usage_alert_thresholds", []int{80, 95, 100}), 1, 100)
	balanceBelow := int64(runtimecfg.SafeCfgInt("usage_alert_token_balance_below", 200000))
	period := usageAlertPeriodKey(time.Now())

	var rows []tenantUsageRow
	q := db.DB.Model(&model.Tenant{}).
		Select("id, name, code, used_ai_calls, max_ai_calls_monthly, monthly_token_used, monthly_token_quota, token_balance").
		Where("status IN ?", []string{"active", "trial"})
	if len(scope) > 0 {
		q = q.Where("id IN ?", scope)
	}
	// 只看在用租户：expired/suspended 的续费问题归 dunning 管，两条链路各说各的话，不重复轰炸
	if err := q.Scan(&rows).Error; err != nil {
		log.Printf("[用量预警] 读租户配额失败: %v", err)
		return 0
	}

	// 平台侧通道是否可用只判一次：全不可用时逐租户重试没有意义，但**不落库**，
	// 配好 SMTP 后下一轮自愈补发（口径决定 3）。
	infraReady := d3.SMTPReady() || d3.GroupReady()
	if !infraReady {
		log.Printf("[用量预警] 邮件与群通道均未配置，本轮命中不落库（配置就绪后自动补发）")
	}

	sent := 0
	for _, r := range rows {
		for _, hit := range decideUsageAlerts(r, thresholds, balanceBelow) {
			if recordUsageAlert(r, hit, period, infraReady) == usageAlertRecorded {
				sent++
			}
		}
	}
	if sent > 0 {
		log.Printf("[用量预警] 本轮新增通知 %d 条（账期 %s）", sent, period)
	}
	return sent
}

// usageAlertOutcome recordUsageAlert 的结果：新落锚 / 本轮不做声
type usageAlertOutcome int

const (
	usageAlertSkipped  usageAlertOutcome = iota // 已发过、或本轮发不出且不该记账
	usageAlertRecorded                          // 新增一条预警留痕
)

// recordUsageAlert 去重 + 投递 + 落锚。
// 去重靠 usage_alerts 唯一键（先查后插）：先查是为了不白发一封信，
// 插失败即视为"另一实例已抢先落锚"，本条按已发处理（宁少发不重发）。
func recordUsageAlert(r tenantUsageRow, hit usageAlertHit, period string, infraReady bool) usageAlertOutcome {
	if usageAlertExists(hit.TenantID, hit.Metric, hit.Threshold, period) {
		return usageAlertSkipped
	}
	channels, admins := deliverUsageAlert(r, hit)
	if channels == "" {
		metrics.IncUsageAlertSkipped()
		// 平台通道就绪、只是这家没人可通知 → 落一条空通道行停止每小时重试；
		// 平台通道坏了 → 不落库，等配置就绪后自愈补发。
		if infraReady && admins == 0 {
			insertUsageAlert(hit, period, "")
		}
		return usageAlertSkipped
	}
	if !insertUsageAlert(hit, period, channels) {
		return usageAlertSkipped
	}
	metrics.IncUsageAlertSent()
	return usageAlertRecorded
}

// deliverUsageAlert 实际投递：邮件发租户管理员；耗尽档额外通知平台群（我方要看到加购信号）。
// 返回 (实际通道串, 该租户可用管理员邮箱数)。
func deliverUsageAlert(r tenantUsageRow, hit usageAlertHit) (string, int) {
	admins := len(d3.AdminEmails(hit.TenantID))
	var parts []string
	if d3.SMTPReady() && admins > 0 {
		if d3.UsageEmail(hit.TenantID, r.Name, r.Code, hit.Label, hit.UsagePct, hit.Remaining, hit.Exhausted) {
			parts = append(parts, "email")
		}
	}
	if hit.Exhausted && d3.GroupReady() {
		d3.Group(fmt.Sprintf("【用量耗尽】租户「%s」(%s) %s 已用满（剩 %d），AI 已降级规则话术，请关注加购机会",
			r.Name, r.Code, hit.Label, hit.Remaining))
		parts = append(parts, "group")
	}
	return strings.Join(parts, ","), admins
}

// usageAlertExists 唯一键预检
func usageAlertExists(tenantID uint, metric string, threshold int, period string) bool {
	var n int64
	db.DB.Model(&model.UsageAlert{}).
		Where("tenant_id = ? AND metric = ? AND threshold = ? AND period_key = ?", tenantID, metric, threshold, period).
		Count(&n)
	return n > 0
}

// insertUsageAlert 落锚；写入失败（含唯一键冲突）返回 false
func insertUsageAlert(hit usageAlertHit, period, channels string) bool {
	detail, _ := json.Marshal(map[string]any{
		"metric": hit.Metric, "threshold": hit.Threshold, "usage_pct": hit.UsagePct,
		"remaining": hit.Remaining, "exhausted": hit.Exhausted, "label": hit.Label,
	})
	res := db.DB.Create(&model.UsageAlert{ // g12:platform 小时巡检无请求 ctx，租户归属由 hit.TenantID 显式给出
		TenantID: hit.TenantID, Metric: hit.Metric, Threshold: hit.Threshold, PeriodKey: period,
		UsagePct: hit.UsagePct, Remaining: hit.Remaining, Channels: channels, Detail: string(detail),
	})
	if res.Error != nil {
		log.Printf("[用量预警] 落锚失败 tenant=%d metric=%s: %v", hit.TenantID, hit.Metric, res.Error)
		return false
	}
	return true
}

// normalizeInts 清洗配置里的整数序列：钳到 [lo,hi]、去重、升序。
// 为什么要清洗而不是直接用：档位是热配置，运维手写成 [100,80,0,-5,80] 也不会报错，
// 不排序就会"先发 100% 档、80% 档被当作已覆盖"；去重则防同档重复判定。
func normalizeInts(in []int, lo, hi int) []int {
	seen := make(map[int]bool, len(in))
	out := make([]int, 0, len(in))
	for _, v := range in {
		if v < lo || v > hi || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Ints(out)
	return out
}

// UsageAlertConfigView 预警生效配置（供 /admin/billing/alerts 展示"我们会在哪几档通知你"）
type UsageAlertConfigView struct {
	Enabled           bool  `json:"enabled"`
	Thresholds        []int `json:"thresholds"`
	TokenBalanceBelow int64 `json:"token_balance_below"`
	GroupReady        bool  `json:"group_ready"` // 平台群通道是否已配（租户侧看这个知道"我方有人在盯"）
	EmailReady        bool  `json:"email_ready"` // SMTP 是否已配
}

// CurrentUsageAlertConfig 读当前生效的预警配置（键缺失回退默认，与 sweep 同一套解析口径）
func CurrentUsageAlertConfig() UsageAlertConfigView {
	return UsageAlertConfigView{
		Enabled:           runtimecfg.SafeCfgBool("usage_alert_enabled", false),
		Thresholds:        normalizeInts(runtimecfg.SafeCfgIntSlice("usage_alert_thresholds", []int{80, 95, 100}), 1, 100),
		TokenBalanceBelow: int64(runtimecfg.SafeCfgInt("usage_alert_token_balance_below", 200000)),
		GroupReady:        d3.GroupReady(),
		EmailReady:        d3.SMTPReady(),
	}
}

// UsageAlertRowView 预警历史行（API 出参形态，字段名即契约）
// 列名显式声明：本结构体走 Scan 落值，命名策略猜错即整列为零值且无报错。
type UsageAlertRowView struct {
	Metric    string    `gorm:"column:metric" json:"metric"`
	Threshold int       `gorm:"column:threshold" json:"threshold"`
	PeriodKey string    `gorm:"column:period_key" json:"period_key"`
	UsagePct  int       `gorm:"column:usage_pct" json:"usage_pct"`
	Remaining int64     `gorm:"column:remaining" json:"remaining"`
	Channels  string    `gorm:"column:channels" json:"channels"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

// ListTenantUsageAlerts 某租户近 limit 条预警留痕（账期倒序）。
// 为什么暴露给租户自己看：管理员换人后，新人要能回答"上一任有没有被告知过额度问题"——
// 这条记录同时是我方的免责留痕。
// 空态刻意返回 []T{} 而不是 nil：nil 切片的 JSON 是 null，而契约（api.d.ts）与前端都按数组对待，
// "没有留痕"与"字段不存在"必须在响应里可区分，否则前端每处都要写 `list || []` 的补丁。
func ListTenantUsageAlerts(tenantID uint, limit int) []UsageAlertRowView {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows := []UsageAlertRowView{}
	db.DB.Model(&model.UsageAlert{}).
		Select("metric, threshold, period_key, usage_pct, remaining, COALESCE(channels,'') as channels, created_at").
		Where("tenant_id = ?", tenantID).
		Order("period_key DESC, threshold DESC").Limit(limit).Scan(&rows)
	return rows
}

// UsageProgressRow 单项指标的当前进度（后台卡片画进度条直接吃这一行）。
// Unlimited=true 表示这一项没有分母（"不限额"或"余额桶"），前端不画进度条只显示绝对量——
// 不能把 0 当分母算出 0% 再画满格，那是把"没配额"显示成"没用完"。
type UsageProgressRow struct {
	Metric    string `json:"metric"`
	Label     string `json:"label"`
	Used      int64  `json:"used"`
	Max       int64  `json:"max"`
	Pct       int    `json:"pct"`
	Remaining int64  `json:"remaining"`
	Unlimited bool   `json:"unlimited"`
	// WarnBelow 绝对水位预警线（只有余额桶这一行非 0）：这一项没有分母，
	// 卡片要说的不是"用了百分之几"而是"低于多少我就会通知你"，所以阈值跟着行进契约，
	// 而不是让前端去 config 里猜哪一档对应哪一行。
	WarnBelow int64 `json:"warn_below"`
}

// UsageProgress 读单租户三指标进度（与 sweep 同一份列、同一套百分比口径）。
// 为什么单独开一个读函数而不是让前端自己除：预警文案里的 80%/95% 就是我方算的那个数，
// 两处各算迟早对不上，届时"卡片说用了 79%、邮件说越了 80% 档"没人能解释。
func UsageProgress(tenantID uint) []UsageProgressRow {
	var r tenantUsageRow
	// 这里刻意不按 status 过滤：sweep 只看在用租户（不给欠费户发加购预警），
	// 但管理员回后台看自己的数时，欠费态更需要看得见进度。
	if err := db.DB.Model(&model.Tenant{}).
		Select("id, used_ai_calls, max_ai_calls_monthly, monthly_token_used, monthly_token_quota, token_balance").
		Where("id = ?", tenantID).Take(&r).Error; err != nil {
		return []UsageProgressRow{} // 与 ListTenantUsageAlerts 同口径：数组字段空态是 []，不是 null
	}
	balanceBelow := int64(runtimecfg.SafeCfgInt("usage_alert_token_balance_below", 200000))
	callsMax := int64(r.MaxAICallsMonthly)
	tokensMax := r.MonthlyTokenQuota
	return []UsageProgressRow{
		{
			Metric: model.UsageMetricMonthlyCalls, Label: "本月 AI 调用次数配额",
			Used: int64(r.UsedAICalls), Max: callsMax, Pct: usagePctOf(int64(r.UsedAICalls), callsMax),
			Remaining: callsMax - int64(r.UsedAICalls), Unlimited: callsMax <= 0,
		},
		{
			Metric: model.UsageMetricMonthlyTokens, Label: "本月 AI 额度（tokens）",
			Used: r.MonthlyTokenUsed, Max: tokensMax, Pct: usagePctOf(r.MonthlyTokenUsed, tokensMax),
			Remaining: tokensMax - r.MonthlyTokenUsed, Unlimited: tokensMax <= 0,
		},
		{
			// 余额桶是绝对水位（低于 balanceBelow 才提示），没有分母可言：
			// Remaining 放余额、标 unlimited 让前端不画进度条，预警线随行进 WarnBelow。
			Metric: model.UsageMetricTokenBalance, Label: "预充值 AI 余额（tokens）",
			Used: 0, Max: 0, Pct: 0, Remaining: r.TokenBalance, Unlimited: true,
			WarnBelow: balanceBelow,
		},
	}
}

// usagePctOf 已用百分比（整数下取整，超过 100 钳到 100）；max<=0 返回 0（不限额，无进度可言）
func usagePctOf(used, max int64) int {
	if max <= 0 || used < 0 {
		return 0
	}
	pct := int(used * 100 / max)
	if pct > 100 {
		pct = 100
	}
	return pct
}
