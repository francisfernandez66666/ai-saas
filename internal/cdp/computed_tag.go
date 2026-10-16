// Package cdp CDP 数据底座：OneID 身份归并、画像/标签/事件写收口与分群引擎（四维标签体系）。
package cdp

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"ai-scrm/internal/model"

	"gorm.io/gorm"
)

// ============================================================
// CDP 计算标签层（P2-4 扩展/复合标签，配置驱动）
//
// 设计目标：业务在 cdp_computed_tag_rules 写"何时打某个派生标签"的条件，
// 摄入流水线按规则评估、命中即 ApplyTag，扩展标签能力**不进代码**。
//
// 红线（与原子标签同）：规则目标标签若属 attitude 维（态度维），一律跳过——
// 态度严禁由行为/规则派生，只能来自零方渠道或 NLP 语义（见 applyAttitudeTagsTx 注释）。
// 本层只消费"已存在的原子标签 + 当前事件属性"，绝不能反向推导态度。
//
// 条件 DSL（Condition 字段的 JSON）：
//
//	{ "all": [ <fact>, ... ] }   // 全部满足
//	{ "any": [ <fact>, ... ] }   // 任一满足
//	<fact>                       // 单条事实
//
// fact：
//   - {"fact":"tag","op":"exists"|"not_exists","code":"xxx"}
//     判定画像当前是否已打某原子标签（含本事务刚写入的原子标签，Postgres 同事务可见）
//   - {"fact":"attr","op":"eq"|"contains"|"gt"|"lt"|"exists","key":"k","value":"v"}
//     判定当前事件属性（ev.Attributes）
// ============================================================

// applyComputedTagsTx 计算标签评估（在原子标签计算之后、同一摄入事务内调用）。
// 加载租户启用的规则，逐条评估；命中且目标标签不触碰态度红线时 ApplyTag。
func applyComputedTagsTx(tx *gorm.DB, ev *ingestEvent, tid, profileID uint) {
	var rules []model.CdpComputedTagRule
	if err := tx.Where("tenant_id = ? AND status = 1", tid).
		Order("priority ASC, id ASC").Find(&rules).Error; err != nil {
		log.Printf("[CDP] 计算标签规则加载失败 tenant=%d: %v", tid, err)
		return
	}
	if len(rules) == 0 {
		return // 无规则 = 零行为变更，现有测试不受影响
	}

	// 当前画像已打标签集合（含本事务刚写入的原子标签，Postgres 同事务可见）
	tagSet := currentTagSet(tx, profileID)

	for _, r := range rules {
		var cond map[string]any
		if err := json.Unmarshal([]byte(r.Condition), &cond); err != nil || len(cond) == 0 {
			log.Printf("[CDP] 计算标签规则 %q 条件非法，跳过: %v", r.Name, err)
			continue
		}
		if !evalCondition(cond, tagSet, ev.Attributes) {
			continue
		}
		// 红线护栏：派生标签若属态度维，严禁由规则派生
		if isAttitudeTarget(tx, r.TargetTagCode) {
			log.Printf("[CDP][红线] 计算标签规则 %q 目标 %q 属态度维，跳过（态度严禁由规则派生）", r.Name, r.TargetTagCode)
			continue
		}
		ApplyTagTx(tx, tid, profileID, r.TargetTagCode, "1")
	}
}

// currentTagSet 读取画像当前已打标签的 code 集合（同一摄入事务内可见未提交写入）。
func currentTagSet(tx *gorm.DB, profileID uint) map[string]bool {
	set := map[string]bool{}
	rows := []struct{ Code string }{}
	tx.Table("cdp_tag_assignments a").
		Select("df.code as code").
		Joins("LEFT JOIN cdp_tag_definitions df ON a.definition_id = df.id").
		Where("a.cdp_profile_id = ?", profileID).
		Scan(&rows)
	for _, r := range rows {
		if r.Code != "" {
			set[r.Code] = true
		}
	}
	return set
}

// isAttitudeTarget 目标标签是否属态度维（红线判定）。
func isAttitudeTarget(tx *gorm.DB, code string) bool {
	var def model.CdpTagDefinition
	if err := tx.Where("code = ?", code).First(&def).Error; err != nil {
		return false
	}
	return def.Category == "attitude"
}

// evalCondition 递归评估条件树（纯函数，可单测）。
func evalCondition(cond map[string]any, tagSet map[string]bool, attrs map[string]any) bool {
	if all, ok := cond["all"].([]any); ok {
		for _, sub := range all {
			if m, ok := sub.(map[string]any); ok && !evalCondition(m, tagSet, attrs) {
				return false
			}
		}
		return true
	}
	if anyList, ok := cond["any"].([]any); ok {
		for _, sub := range anyList {
			if m, ok := sub.(map[string]any); ok && evalCondition(m, tagSet, attrs) {
				return true
			}
		}
		return false
	}
	return evalFact(cond, tagSet, attrs)
}

// evalFact 评估单条事实。
func evalFact(f map[string]any, tagSet map[string]bool, attrs map[string]any) bool {
	fact, _ := f["fact"].(string)
	op, _ := f["op"].(string)
	switch fact {
	case "tag":
		code, _ := f["code"].(string)
		exists := tagSet[code]
		return (op == "exists" && exists) || (op == "not_exists" && !exists)
	case "attr":
		key, _ := f["key"].(string)
		v, ok := attrs[key]
		if !ok {
			return op == "exists" && false // 属性不存在：仅 exists 可能关心，恒否
		}
		if op == "exists" {
			return true
		}
		want := f["value"]
		switch op {
		case "eq":
			return fmt.Sprintf("%v", v) == fmt.Sprintf("%v", want)
		case "contains":
			sub, _ := want.(string)
			s, _ := v.(string)
			return strings.Contains(s, sub)
		case "gt", "lt":
			return compareNumeric(v, want, op)
		}
	}
	return false
}

// compareNumeric 把属性值与阈值都按 float64 比较（事件属性常为 float64 / json.Number）。
func compareNumeric(v, want any, op string) bool {
	vf, ok1 := toFloat(v)
	wf, ok2 := toFloat(want)
	if !ok1 || !ok2 {
		return false
	}
	if op == "gt" {
		return vf > wf
	}
	return vf < wf // "lt"
}

// toFloat 把常见数值类型统一转 float64。
func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		var f float64
		if _, err := fmt.Sscanf(x, "%f", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}
