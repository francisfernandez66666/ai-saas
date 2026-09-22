// Package db_test 2026-09-22 复核 P1-2 的回归断言：多租户复合唯一索引必须真的生效
//
// 缺陷原貌：tags/brands 等租户级表的"复合唯一索引"在存量库里实际是单列全局唯一，
// 于是租户 A 建了标签"价格敏感"之后，**租户 B 就再也建不了同名标签**——对 SaaS 是硬伤。
// 根因不是模型：实测全新库用同样的 GORM 标签能建出正确的 (tenant_id, name)；
// 真因是 AutoMigrate 只增不删，历史单列索引残留且按名判重，导致正确版永远补不上。
//
// 本文件做两层断言，缺一不可：
//  1. 结构性：目标列上不存在"单列全局唯一"，且存在含 tenant_id 的复合唯一
//  2. 行为性：两个不同租户写入同名标签，第二个必须成功（这是用户真正感知到的东西）
package db_test

import (
	"regexp"
	"testing"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// 说明：本文件用 db_test 外部包——testutil 依赖 internal/db，同包测试会成环。

// tenantUniqueTargets 与 database.go ensureTenantUniqueIndexes 的处置目标一一对应。
var tenantUniqueTargets = []struct{ table, col string }{
	{"tags", "name"},
	{"tags", "code"},
	{"brands", "name"},
	{"brands", "code"},
	{"car_models", "code"},
	{"cdp_tag_definitions", "code"},
	{"cdp_profiles", "cdp_id"},
	{"flow_definitions", "code"},
}

// TestTenantCompositeUniqueIndexStructure 结构性断言：不得再有单列全局唯一索引。
func TestTenantCompositeUniqueIndexStructure(t *testing.T) {
	testutil.SetupTestDB(t)

	for _, tc := range tenantUniqueTargets {
		t.Run(tc.table+"."+tc.col, func(t *testing.T) {
			rows, err := db.DB.Raw(
				"SELECT indexname, indexdef FROM pg_indexes WHERE tablename = ?", tc.table).Rows()
			if err != nil {
				t.Fatalf("查询索引失败: %v", err)
			}
			defer rows.Close()

			want := regexp.MustCompile(`\(tenant_id, ` + tc.col + `\)`)
			bad := regexp.MustCompile(`\(` + tc.col + `\)`)
			gotComposite, gotSingle := false, ""
			for rows.Next() {
				var name, def string
				if err := rows.Scan(&name, &def); err != nil {
					t.Fatalf("扫描失败: %v", err)
				}
				if want.MatchString(def) {
					gotComposite = true
				}
				// 单列唯一 = 只有该列、没有逗号 —— 这正是"跨租户抢名"的元凶
				if bad.MatchString(def) && !regexp.MustCompile(`\([a-z_]+, `).MatchString(def) {
					gotSingle = name + " => " + def
				}
			}
			if !gotComposite {
				t.Errorf("缺少含 tenant_id 的复合唯一索引 (tenant_id, %s)", tc.col)
			}
			if gotSingle != "" {
				t.Errorf("仍存在单列唯一索引（会导致跨租户命名冲突）: %s", gotSingle)
			}
		})
	}
}

// TestCrossTenantSameTagNameAllowed 行为性断言：不同租户可以各建同名标签。
func TestCrossTenantSameTagNameAllowed(t *testing.T) {
	testutil.SetupTestDB(t)

	// 取两个不存在的租户 ID 做隔离试验（tags 表无外键约束，无需真实租户）
	var maxID uint
	if err := db.DB.Raw("SELECT COALESCE(MAX(id),0) FROM tenants").Scan(&maxID).Error; err != nil {
		t.Fatalf("取最大租户 ID 失败: %v", err)
	}
	tidA, tidB := maxID+100001, maxID+100002
	tagName := "跨租户同名标签"
	defer db.DB.Exec("DELETE FROM tags WHERE tenant_id IN (?,?) AND name = ?", tidA, tidB, tagName)

	a := model.Tag{TenantID: tidA, Name: tagName, Status: 1}
	if err := db.DB.Create(&a).Error; err != nil {
		t.Fatalf("租户 A 建标签失败（前置条件不成立）: %v", err)
	}
	b := model.Tag{TenantID: tidB, Name: tagName, Status: 1}
	if err := db.DB.Create(&b).Error; err != nil {
		t.Errorf("租户 B 建同名标签失败 —— P1-2 回归：说明索引仍是全局唯一: %v", err)
	}

	// 反对照：同一租户内重复建同名标签，必须仍然被拒（否则等于把约束整个去掉了）
	dup := model.Tag{TenantID: tidA, Name: tagName, Status: 1}
	if err := db.DB.Create(&dup).Error; err == nil {
		t.Errorf("同租户内重复建同名标签应当被拒 —— 说明约束被误删成了无约束")
	}
}
