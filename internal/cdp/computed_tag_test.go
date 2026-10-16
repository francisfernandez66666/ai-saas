// Package cdp 计算标签层（P2-4）测试：纯函数判定 + DB 集成（命中/不命中/态度红线）。
package cdp

import (
	"os"
	"testing"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// TestMain 统一出口：DB 不可用被跳过的用例数在收尾显式打印（防静默绿）。
func TestMain(m *testing.M) {
	os.Exit(testutil.RunMain(m))
}

// TestEvalCondition 纯函数判定：覆盖 tag 存在/不存在、attr 各算子、all/any 组合。
// 正向+负向成对，改动任一算子语义即会转红（变异自证）。
func TestEvalCondition(t *testing.T) {
	tagSet := map[string]bool{"beh_repeat_visit": true, "tran_order_paid": true}
	attrs := map[string]any{
		"channel":   "wechat",
		"price":     99.0,
		"msg_count": 3.0,
	}

	cases := []struct {
		name string
		cond map[string]any
		want bool
	}{
		{"tag 存在-命中", map[string]any{"fact": "tag", "op": "exists", "code": "beh_repeat_visit"}, true},
		{"tag 存在-不命中", map[string]any{"fact": "tag", "op": "exists", "code": "beh_viewed_model"}, false},
		{"tag 不存在-命中", map[string]any{"fact": "tag", "op": "not_exists", "code": "beh_viewed_model"}, true},
		{"tag 不存在-不命中", map[string]any{"fact": "tag", "op": "not_exists", "code": "tran_order_paid"}, false},
		{"attr eq-命中", map[string]any{"fact": "attr", "op": "eq", "key": "channel", "value": "wechat"}, true},
		{"attr eq-不命中", map[string]any{"fact": "attr", "op": "eq", "key": "channel", "value": "sms"}, false},
		{"attr contains-命中", map[string]any{"fact": "attr", "op": "contains", "key": "channel", "value": "chat"}, true},
		{"attr contains-不命中", map[string]any{"fact": "attr", "op": "contains", "key": "channel", "value": "sms"}, false},
		{"attr gt-命中", map[string]any{"fact": "attr", "op": "gt", "key": "price", "value": 50.0}, true},
		{"attr gt-不命中", map[string]any{"fact": "attr", "op": "gt", "key": "price", "value": 100.0}, false},
		{"attr lt-命中", map[string]any{"fact": "attr", "op": "lt", "key": "msg_count", "value": 5.0}, true},
		{"attr lt-不命中", map[string]any{"fact": "attr", "op": "lt", "key": "msg_count", "value": 1.0}, false},
		{"attr exists-命中", map[string]any{"fact": "attr", "op": "exists", "key": "channel"}, true},
		{"attr exists-不命中", map[string]any{"fact": "attr", "op": "exists", "key": "missing"}, false},
		{"all 全满足-命中", map[string]any{"all": []any{
			map[string]any{"fact": "tag", "op": "exists", "code": "beh_repeat_visit"},
			map[string]any{"fact": "attr", "op": "gt", "key": "price", "value": 50.0},
		}}, true},
		{"all 有不满足-不命中", map[string]any{"all": []any{
			map[string]any{"fact": "tag", "op": "exists", "code": "beh_repeat_visit"},
			map[string]any{"fact": "tag", "op": "exists", "code": "beh_viewed_model"},
		}}, false},
		{"any 有满足-命中", map[string]any{"any": []any{
			map[string]any{"fact": "tag", "op": "exists", "code": "beh_viewed_model"},
			map[string]any{"fact": "tag", "op": "exists", "code": "tran_order_paid"},
		}}, true},
		{"any 全不满足-不命中", map[string]any{"any": []any{
			map[string]any{"fact": "tag", "op": "exists", "code": "beh_viewed_model"},
			map[string]any{"fact": "tag", "op": "not_exists", "code": "tran_order_paid"},
		}}, false},
	}
	for _, c := range cases {
		if got := evalCondition(c.cond, tagSet, attrs); got != c.want {
			t.Errorf("evalCondition(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestApplyComputedTagsTx 集成：命中打派生标签、不命中不打、态度维红线跳过。
func TestApplyComputedTagsTx(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenantCode(t, "cmp_a")

	// 准备标签字典（派生标签目标 + 原子标签条件源 + 态度红线目标）
	for _, d := range []model.CdpTagDefinition{
		{TenantID: tid, Code: "beh_repeat_visit", Name: "复访", Category: "behavior"},
		{TenantID: tid, Code: "cmp_high_value", Name: "高价值客户", Category: "derived"},
		{TenantID: tid, Code: "att_emotion", Name: "情绪态度", Category: "attitude"},
	} {
		d := d
		var cnt int64
		db.DB.Model(&model.CdpTagDefinition{}).Where("code = ? AND tenant_id = ?", d.Code, d.TenantID).Count(&cnt)
		if cnt == 0 {
			if err := db.DB.Create(&d).Error; err != nil {
				t.Fatalf("建标签字典失败: %v", err)
			}
		}
	}

	profile := EnsureProfile(tid, "one_cmp_1", 0)
	if profile == nil {
		t.Fatalf("画像创建失败")
	}

	countAssigned := func(code string) int64 {
		var n int64
		db.DB.Table("cdp_tag_assignments a").
			Joins("LEFT JOIN cdp_tag_definitions df ON a.definition_id = df.id").
			Where("a.cdp_profile_id = ? AND df.code = ?", profile.ID, code).
			Count(&n)
		return n
	}

	// 场景1：原子标签 beh_repeat_visit 已打，规则"beh_repeat_visit 存在 → 打 cmp_high_value" 应命中
	ApplyTagTx(db.DB, tid, profile.ID, "beh_repeat_visit", "1")
	insertRule(t, tid, "r_hv", "cmp_high_value",
		`{"fact":"tag","op":"exists","code":"beh_repeat_visit"}`)
	applyComputedTagsTx(db.DB, &ingestEvent{Attributes: map[string]any{}}, tid, profile.ID)
	if n := countAssigned("cmp_high_value"); n != 1 {
		t.Errorf("场景1 派生标签应命中: 实际 count=%d", n)
	}

	// 场景2：删掉原子标签后，同规则不应再命中（验证判定基于真实状态，非永远通过）
	db.DB.Table("cdp_tag_assignments a").
		Joins("LEFT JOIN cdp_tag_definitions df ON a.definition_id = df.id").
		Where("a.cdp_profile_id = ? AND df.code = ?", profile.ID, "beh_repeat_visit").
		Delete(&model.CdpTagAssignment{})
	insertRule(t, tid, "r_hv2", "cmp_high_value",
		`{"fact":"tag","op":"exists","code":"beh_repeat_visit"}`)
	applyComputedTagsTx(db.DB, &ingestEvent{Attributes: map[string]any{}}, tid, profile.ID)
	if n := countAssigned("cmp_high_value"); n != 1 {
		t.Errorf("场景2 删原子标签后派生标签不应新增，实际 count=%d（变异自证失败）", n)
	}

	// 场景3：态度维红线——规则目标 att_emotion 必须跳过（态度严禁由规则派生）
	insertRule(t, tid, "r_att", "att_emotion",
		`{"fact":"tag","op":"exists","code":"beh_repeat_visit"}`)
	ApplyTagTx(db.DB, tid, profile.ID, "beh_repeat_visit", "1") // 重新打原子标签让条件满足
	applyComputedTagsTx(db.DB, &ingestEvent{Attributes: map[string]any{}}, tid, profile.ID)
	if n := countAssigned("att_emotion"); n != 0 {
		t.Errorf("场景3 态度维红线应跳过，实际 count=%d", n)
	}
}

// insertRule 插入一条计算标签规则（测试用，带租户隔离）。
func insertRule(t *testing.T, tid uint, name, target, cond string) {
	t.Helper()
	r := model.CdpComputedTagRule{
		TenantID:      tid,
		Name:          name,
		TargetTagCode: target,
		Condition:     cond,
		Status:        1,
	}
	if err := db.DB.Create(&r).Error; err != nil {
		t.Fatalf("插计算标签规则失败: %v", err)
	}
}
