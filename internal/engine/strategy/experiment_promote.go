// Package strategy L2 晋升执行骨架（批五 C）：实验判优后的自动调权落库路径——
// 本批**只接线不投产**：experiment_auto_promote 默认 false，任何调用都会在闸前返回，不产生写库。
// 量纲已拍板（2026-09-23）：ab_weight 按 0~100 整数百分点，与人工改权重入口同口径，
// 自动晋升产出的值必然能通过人工校验。
package strategy

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// ApplyPromoteDecision 晋升计划执行：双闸（auto_promote 开关 + plan.ShouldApply）都过才写库。
// 写模板用字段级 Updates（红线：勿整行 Save，防 stale 覆写实验分组等相邻列）；
// 审计走 tenant_audit_logs（action=pack_experiment_promote），后台链路无请求 ctx，
// TenantID 显式设值防盖章回调写成 0（见 AGENTS 事务内写租户表红线）。
// 注意：生产调用方是 RunAutoPromotionSweep（main.go 小时任务），本函数自身仍保留
// 开关复核—— sweeper 与执行器各判一次，防未来新增调用方绕过闸门直接写权重。
func ApplyPromoteDecision(tenantID uint, plan PromotePlan) error {
	if !autoPromoteEnabledFor(tenantID) {
		return nil // L2 默认关：只留路径不留行为，触发面为零（租户覆盖开才继续，见 autoPromoteEnabledFor）
	}
	if !plan.ShouldApply || tenantID == 0 || plan.TemplateID == "" {
		return nil
	}
	// 租户条件与 db.DB 同行：满足 G-12 白名单 A 类（行内显式 tenant_id 约束）口径，不推高棘轮
	res := db.DB.Model(&model.Template{}).Where("id = ? AND tenant_id = ?", plan.TemplateID, tenantID).
		Updates(map[string]interface{}{"ab_weight": plan.NewWeight, "updated_at": time.Now()})
	if res.Error != nil {
		return fmt.Errorf("晋升写权重失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("晋升目标模板不存在: tenant=%d template=%s", tenantID, plan.TemplateID)
	}
	detail, _ := json.Marshal(map[string]interface{}{
		"template_id": plan.TemplateID,
		"prev_weight": plan.PreviousWeight,
		"new_weight":  plan.NewWeight,
		"computed_at": plan.ComputedAt,
		"auto":        true,
	})
	// 审计失败不回滚权重（旁路留痕语义，与 webhook/billing 台账一致），但必须打日志人工追补
	if err := db.DB.Create(&model.TenantAuditLog{
		TenantID:  tenantID,
		UserID:    0, // 0=系统自动操作（区别于人工改权重的登录态审计）
		Action:    "pack_experiment_promote",
		Resource:  "template:" + plan.TemplateID,
		Detail:    string(detail),
		CreatedAt: time.Now(),
	}).Error; err != nil {
		log.Printf("[实验晋升] 权重已更新但审计写入失败，需人工追补: tenant=%d template=%s err=%v", tenantID, plan.TemplateID, err)
	}
	return nil
}

// ============================================================
// L2 巡检接线（2026-09-23 批六补做）：把判优结果接进执行器
//
// 为什么必须补这一段：ApplyPromoteDecision 与纯函数 PromoteDecision 都写好了，
// 但全仓**没有任何调用方**——那就是"看起来有自动化、实际永远不转"的装饰代码，
// 比没有更贵（每个读代码的人都会以为 L2 已在跑）。本段把它接进 main.go 的
// pack:quality:sweep 小时任务，同时保持默认零行为：
//   - experiment_auto_promote 默认 false → 函数第一行返回，一次库都不查；
//   - 只处理 L1 判优结论为 leading（显著胜出）的模板，insufficient/keep_watching 一律不动；
//   - 冷却按"上一次晋升审计时刻 + experiment_promote_cooldown_hours"计，
//     防止同一模板每小时被顶一格、几天内冲到 100 独占分流（步长 10 看起来很小，架不住小时级复利）。
// ============================================================

// promoteAuditAction 晋升审计动作名（冷却窗口据此回查，改名须同步）
const promoteAuditAction = "pack_experiment_promote"

// autoPromoteEnabledFor L2 自动调权闸（**租户生效版**，2026-09-23 批六）：
// 系统默认关=全租户关，任一租户可在自己后台覆盖开。与 banditEnabledFor 同写法——
// 这套键不是平台级键，读系统层就等于"租户改了值、作业照旧"的静默失效。
func autoPromoteEnabledFor(tenantID uint) bool {
	return cfgBoolFor(tenantID, "experiment_auto_promote", false)
}

// promoteKnobs 单租户的调权旋钮（步长/上限/冷却），均已做误配置钳位。
type promoteKnobs struct {
	Step      int           // ab_weight 单次上调百分点
	MaxWeight int           // ab_weight 自动上调上限（0~100）
	Cooldown  time.Duration // 同一模板两次晋升的最小间隔
}

// promotionKnobsFor 装配该租户的晋升旋钮。
// 为什么要钳位而不是照用：这些值是后台手填的——step=0 会每轮白写一条审计、
// max 填成 10000 会让一次显著胜出就把模板推到"独占分流"、冷却填负数等于拆掉防叠幅保护
// （步长 10 看着小，架不住小时级复利）。
func promotionKnobsFor(tenantID uint) promoteKnobs {
	step := cfgIntFor(tenantID, "experiment_weight_step", 10)
	if step < 1 {
		step = 1
	}
	maxWeight := cfgIntFor(tenantID, "experiment_weight_max", abWeightCeil)
	if maxWeight < 0 {
		maxWeight = 0
	}
	if maxWeight > abWeightCeil {
		maxWeight = abWeightCeil
	}
	hours := cfgIntFor(tenantID, "experiment_promote_cooldown_hours", 72)
	if hours < 0 {
		hours = 0
	}
	return promoteKnobs{Step: step, MaxWeight: maxWeight, Cooldown: time.Duration(hours) * time.Hour}
}

// RunAutoPromotionSweep L2 自动调权巡检：返回本轮实际写库的模板数。
// 任何一步查库失败都只记日志不 panic——建议卡片（L1）是主产出，自动调权是旁路，
// 旁路故障绝不能把小时任务整体带崩（同 kb_rerank / 择臂层的 fail-open 纪律）。
func RunAutoPromotionSweep(now time.Time) (int, error) {
	if !cfgBoolFor(0, "experiment_auto_promote", false) && !runtimecfg.SafeAnyTenantFlagOn("experiment_auto_promote") {
		return 0, nil // 全平台（系统层+任何租户覆盖）都没开：只留路径不留行为，触发面为零
	}
	inputs, weights, err := loadPromotionStatInputs()
	if err != nil {
		return 0, fmt.Errorf("读取 pack_stats/templates 失败: %w", err)
	}
	if len(inputs) == 0 {
		return 0, nil
	}
	// 判优按租户分组跑：experiment_* 是租户可自配的键，共用一份参数等于拿 A 租户的门槛判 B 租户的话术
	sugs := BuildTemplateSuggestionsByTenant(inputs)

	applied := 0
	for _, s := range sugs {
		if s.Status != SuggestionStatusLeading {
			continue
		}
		if !autoPromoteEnabledFor(s.TenantID) {
			continue // 该租户未开 L2：建议卡片照出，权重一律不动
		}
		knobs := promotionKnobsFor(s.TenantID)
		prev, ok := weights[tplWeightKey{s.TenantID, s.TemplateID}]
		if !ok {
			continue // 模板已停用/删除：没有可加权对象
		}
		last, hasLast, err := lastPromoteAt(s.TenantID, s.TemplateID)
		if err != nil {
			log.Printf("[实验晋升] 冷却回查失败 tenant=%d template=%s: %v（按冷却未知处理，跳过本轮）", s.TenantID, s.TemplateID, err)
			continue
		}
		remain := time.Duration(0)
		if hasLast {
			remain = knobs.Cooldown - now.Sub(last)
		}
		// leading 状态本身就等价于"被评臂显著胜出"（见 BuildTemplateSuggestions 分支），
		// 这里还原 verdict 只为喂给纯函数，判定逻辑不在执行器里重写一遍。
		plan := PromoteDecision(s.TemplateID, prev, ExperimentVerdict{
			Decision:   DecisionWinnerB,
			Confidence: s.Confidence,
		}, knobs.Step, knobs.MaxWeight, remain, now)
		if !plan.ShouldApply {
			if plan.BlockedReason != "" && plan.BlockedReason != "cooldown" {
				log.Printf("[实验晋升] 模板%s 未晋升: %s", s.TemplateID, plan.BlockedReason)
			}
			continue
		}
		if err := ApplyPromoteDecision(s.TenantID, plan); err != nil {
			log.Printf("[实验晋升] 写权重失败 tenant=%d template=%s: %v", s.TenantID, s.TemplateID, err)
			continue
		}
		applied++
		log.Printf("[实验晋升] 模板%s 权重 %d→%d（奖励指标%s=%.1f%%，n=%d，置信%.3f）",
			s.TemplateID, plan.PreviousWeight, plan.NewWeight, s.Metric, s.RewardRate*100, s.SampleCount, s.Confidence)
	}
	return applied, nil
}

// loadPromotionStatInputs 装配判优输入 + (租户,模板)→当前权重快照。
// 两侧都限定 tenant_id <> 0（平台层 tenant 0 的预置话术不参与租户级自动调权）。
// 权重随输入一起返回而不是另建进程内缓存：sweep 是单轮读一次，缓存只会带来
// 跨轮读到旧值的风险（尤其单测里两轮之间 pack_stats 会被改写）。
func loadPromotionStatInputs() ([]TemplateStatInput, map[tplWeightKey]int, error) {
	var snaps []model.PackStatSnapshot
	if err := db.DB.Where("tenant_id <> 0").Find(&snaps).Error; err != nil {
		return nil, nil, err
	}
	if len(snaps) == 0 {
		return nil, map[tplWeightKey]int{}, nil
	}
	var tpls []model.Template
	if err := db.DB.Select("id, tenant_id, anchor_type, ab_weight").
		Where("tenant_id <> 0 AND status = 1").Find(&tpls).Error; err != nil {
		return nil, nil, err
	}
	// 键为 (租户,模板)：同 ID 在不同租户下是两条不同话术，不能只按 ID 建索引
	anchors := make(map[tplWeightKey]int, len(tpls))
	weights := make(map[tplWeightKey]int, len(tpls))
	for _, t := range tpls {
		k := tplWeightKey{t.TenantID, t.ID}
		anchors[k] = t.AnchorType
		weights[k] = t.AbWeight
	}
	inputs := make([]TemplateStatInput, 0, len(snaps))
	for _, s := range snaps {
		anchor, ok := anchors[tplWeightKey{s.TenantID, s.TemplateID}]
		if !ok {
			continue // 模板已删除或已停用：不给它加权
		}
		inputs = append(inputs, strategyStatInputFromSnap(s, anchor))
	}
	return inputs, weights, nil
}

// strategyStatInputFromSnap 快照行 → 判优输入（单独抽出让上面的装配函数只讲流程）
func strategyStatInputFromSnap(s model.PackStatSnapshot, anchor int) TemplateStatInput {
	return TemplateStatInput{
		TenantID: s.TenantID, PackCode: s.PackCode, PackVersion: s.PackVersion,
		TemplateID: s.TemplateID, AnchorType: anchor,
		SampleCount: s.SampleCount,
		HookRate:    s.HookRate, LeadRate: s.LeadRate,
		ArriveRate: s.ArriveRate, DealRate: s.DealRate,
	}
}

// tplWeightKey 模板标识的 (租户, ID) 复合键
type tplWeightKey struct {
	tenantID uint
	id       string
}

// lastPromoteAt 回查该模板最近一次晋升时间（冷却窗口锚点）。
// 无记录返回 has=false（首次晋升不受冷却约束）。
// 用 Find+Limit 而非 First：First 在无行时返回 ErrNoRows，要把"没有历史"和"查库故障"
// 分开就得引 gorm 错误比较；Find 空集是零值 + 无错误，分支更少也不会误判成故障。
func lastPromoteAt(tenantID uint, templateID string) (time.Time, bool, error) {
	var rows []model.TenantAuditLog
	if err := db.DB.Select("created_at").
		Where("tenant_id = ? AND action = ? AND resource = ?",
			tenantID, promoteAuditAction, "template:"+templateID).
		Order("id DESC").Limit(1).Find(&rows).Error; err != nil {
		return time.Time{}, false, err
	}
	if len(rows) == 0 {
		return time.Time{}, false, nil
	}
	return rows[0].CreatedAt, true, nil
}
