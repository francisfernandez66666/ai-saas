// outcome_backfill.go 终局标签回填（批五 A，2026-09-23，PLAN_FIX_2026-09-22 §6-A）。
// 把客户级"到店/成交"（customers.journey_stage，经 analytics.ArrivedStages/OrderedStages 判定）
// 下沉为 message 级归因行的 arrived_at/dealt_at 标签，供 SyncPackStats 聚合 arrive_rate/deal_rate、
// 后续择臂层（template_bandit）与实验判优层（experiment_decision）当奖励读取。

package attribution

import (
	"time"

	"gorm.io/gorm"

	"ai-scrm/internal/analytics"
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
)

// outcomeWindowDays 读取终局观察窗（天）。窗口语义是本文件的核心（见 BackfillOutcomes 注释），
// 键缺失/服务未初始化一律回默认 30，与 config_defaults 播种值一致。
func outcomeWindowDays() int {
	d := runtimecfg.SafeCfgInt("outcome_window_days", 30)
	if d < 0 {
		d = 0
	}
	return d
}

// outcomeScope 一个待巡检的（租户×客户）分组。按 tenant_id 分组读，
// 与包内既有写法保持一致：不用事务、全部语句显式带 tenant_id 条件，避免踩
// 「事务内写租户表丢盖章上下文」红线（见 AGENTS.md C7 回归教训）。
type outcomeScope struct {
	TenantID   uint
	CustomerID uint
}

// BackfillOutcomes 按客户当前旅程阶段回填归因行的终局标签；limit<=0 默认 500（限客户分组数，防一次全表）。
// tenantIDs[0]>0 时只处理该租户（与 ScoreReplyAttributions 同款测试隔离参数：共享库下
// 全局待巡检队列会把其它租户的行挤进 limit，导致单测断言漂移；生产调用不传即全量）。
//
// 窗口语义（关键取舍，2026-09-23）：销售是周级因果，一条刚发出的回复两周后才带来到店很正常。
// 因此 created_at 距今 **不足** outcome_window_days 的行即使当前还没到店，也绝不写
// outcome_checked_at——它们留在待巡检队列（部分索引 idx_ra_outcome_pending 正是
// `outcome_checked_at IS NULL AND template_id <> ”`），每小时巡检一次直到窗口过期。
// 本轮对窗口内行的动作只有"正向补标"：客户已到店/已成交就立刻写 arrived_at/dealt_at
// （已确立的事实无需等窗口）；但 checked 保持 NULL，窗口内仍可能被补上更高的 dealt 标签。
// 窗口已过的行才**终判**：按当前阶段补齐正标后写 outcome_checked_at=now()，
// 此时 arrived_at 仍为 NULL 即"窗口已过、结论是没有"（NULL 不覆写、不回填哨兵值）。
// 成交（dealt_at 非空）视为完全定局，窗口未过也直接终判，不再占用巡检队列。
//
// 幂等性：所有写入都带 arrived_at IS NULL / dealt_at IS NULL / outcome_checked_at IS NULL
// 守卫——同一行重复跑不改变已确立的值；客户阶段被改回退也不清空已写时间
// （到店是既成事实，CRM 回退多半是纠错改备注之外的误操作，正向标签只进不退）。
//
// 近似取值：arrived_at/dealt_at 取 customer.updated_at——项目未记录"阶段变更事件时刻"，
// 归因行的 updated_at 会被离线评分/近端信号回填等无关动作刷新（与阶段推进无因果），
// 而 customers 行恰在阶段变更时被更新，updated_at 是最贴近推进时刻的可用近似；
// 后续 CRM 编辑会把它向后漂移，属已知偏差（宁可近似可用也不伪造精确时刻，口径见本注释）。
func BackfillOutcomes(limit int, tenantIDs ...uint) (int64, error) {
	if limit <= 0 {
		limit = 500
	}
	cutoff := time.Now().AddDate(0, 0, -outcomeWindowDays())

	// 待巡检队列：只捞参与择优（template_id 非空）且未终判的行，按（租户,客户）分组去重
	var scopes []outcomeScope
	// g12:platform 跨租户巡检队列：本作业由 main.go 小时任务发起，无请求 ctx，
	// 按设计遍历全部租户的归因行（租户维度在 group by 结果里逐组回写，见下方 outcomeScope 循环）。
	q := db.DB.Model(&model.ReplyAttribution{}). // g12:platform
							Select("tenant_id, customer_id").
							Where("template_id <> '' AND customer_id > 0 AND outcome_checked_at IS NULL")
	if len(tenantIDs) > 0 && tenantIDs[0] > 0 {
		q = q.Where("tenant_id = ?", tenantIDs[0])
	}
	if err := q.Group("tenant_id, customer_id").
		Order("tenant_id ASC, customer_id ASC").
		Limit(limit).
		Scan(&scopes).Error; err != nil {
		return 0, err
	}

	var updated int64
	now := time.Now()
	for _, s := range scopes {
		var cust model.Customer
		stage := ""
		approx := now // 客户已删除时退化用本轮巡检时刻（仅影响终判行的阶段快照）
		err := db.DB.Select("id", "journey_stage", "updated_at").
			Where("tenant_id = ? AND id = ?", s.TenantID, s.CustomerID).
			First(&cust).Error
		if err == nil {
			stage = cust.JourneyStage
			if !cust.UpdatedAt.IsZero() {
				approx = cust.UpdatedAt
			}
		} else if err != gorm.ErrRecordNotFound {
			return updated, err
		}

		// ① 正向补标：只写"还没写过"的行，checked 行不动（幂等 + 阶段回退不清空）
		if analytics.InStageSet(stage, analytics.ArrivedStages) {
			if n, err := outcomeWrite(s, "arrived_at", approx, stage); err != nil {
				return updated, err
			} else {
				updated += n
			}
		}
		if analytics.InStageSet(stage, analytics.OrderedStages) {
			if n, err := outcomeWrite(s, "dealt_at", approx, stage); err != nil {
				return updated, err
			} else {
				updated += n
			}
		}

		// ② 终判：窗口已过（无论有无正标）或已成交（完全定局）的行盖 outcome_checked_at。
		// 窗口内且未成交的行刻意跳过——本轮不写 checked，留待下轮巡检（窗口语义见函数头注释）。
		res := db.DB.Model(&model.ReplyAttribution{}).
			Where("tenant_id = ? AND customer_id = ? AND template_id <> '' AND outcome_checked_at IS NULL", s.TenantID, s.CustomerID).
			Where("created_at < ? OR dealt_at IS NOT NULL", cutoff).
			Updates(map[string]interface{}{"outcome_checked_at": now, "updated_at": now})
		if res.Error != nil {
			return updated, res.Error
		}
		updated += res.RowsAffected
	}
	return updated, nil
}

// outcomeWrite 对未终判行补写单个终局时间列 + 阶段快照；列已有值的行不覆写。
func outcomeWrite(s outcomeScope, column string, at time.Time, stage string) (int64, error) {
	res := db.DB.Model(&model.ReplyAttribution{}).
		Where("tenant_id = ? AND customer_id = ? AND template_id <> '' AND outcome_checked_at IS NULL", s.TenantID, s.CustomerID).
		Where(column + " IS NULL").
		Updates(map[string]interface{}{column: at, "outcome_stage": stage, "updated_at": time.Now()})
	return res.RowsAffected, res.Error
}
