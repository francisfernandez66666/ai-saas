// Package db 多租户复合唯一索引「禁止无谓重建」守卫（2026-09-23 假红根因修复）。
//
// 为什么需要这一层：ensureTenantUniqueIndexes 的 drop 清单里混着正确索引**自己的名字**
// （历史存量下它们曾承载单列定义），旧实现每次 db.Init() 都先 DROP 再 CREATE——
// 两步各自 autocommit，中间是一段"约束不存在"的空窗：
//   - 并行 go test（每个进程都跑一遍 Init）里另一进程的结构断言撞上空窗 → 报
//     "缺少含 tenant_id 的复合唯一索引"（本轮 test_all --fast 单测层唯一红项，
//     brands.name/brands.code 同时缺、且零条"仍存在单列唯一"，即空窗指纹）；
//   - 多实例同时重启时，空窗内真实写入可插进重复数据，令随后的 CREATE 永久失败
//     （只告警不阻断）——约束会**真的**丢掉，这是资金/隔离级风险而非测试噪声。
//
// 判定用 pg_class.oid（索引身份）：DROP+CREATE 必换 OID，索引没被碰过则 OID 不变。
// 这比"数一遍 pg_indexes 行数"强——后者在空窗前后都可能是对的，锁不住"是否被重建"。
//
// 修复路径的可逆性由临时表证明：故意在临时表上建一条**错的**单列唯一，断言收敛后
// 被换成正确的 (tenant_id, col)；若只测"正确索引不重建"，等于可以整段删掉重建逻辑仍全绿。
package db

import (
	"fmt"
	"testing"

	"gorm.io/gorm"
)

// indexOids 取给定索引名的 relid（身份指纹）。空 map 表示该索引不存在。
func indexOids(t *testing.T, gdb *gorm.DB, names ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	rows, err := gdb.Raw(
		"SELECT c.relname, c.oid::text FROM pg_class c WHERE c.relkind = 'i' AND c.relname IN ?", names).Rows()
	if err != nil {
		t.Fatalf("查询索引身份失败: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, oid string
		if err := rows.Scan(&name, &oid); err != nil {
			t.Fatalf("扫描索引身份失败: %v", err)
		}
		out[name] = oid
	}
	return out
}

// indexDefsOf 取给定索引名的 indexdef 原文（列序/唯一性的权威口径）。
func indexDefsOf(t *testing.T, gdb *gorm.DB, names ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	rows, err := gdb.Raw(
		"SELECT indexname, indexdef FROM pg_indexes WHERE indexname IN ?", names).Rows()
	if err != nil {
		t.Fatalf("查询索引定义失败: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			t.Fatalf("扫描索引定义失败: %v", err)
		}
		out[name] = def
	}
	return out
}

// TestIsTenantUniqueOn 谓词真值表：只有"UNIQUE + 列串完全匹配"才算正确形态。
// 非唯一的同名索引必须判 false——否则会被当成已收敛跳过重建，约束永久缺失。
func TestIsTenantUniqueOn(t *testing.T) {
	ok := "CREATE UNIQUE INDEX idx_brand_tenant_name ON public.brands USING btree (tenant_id, name)"
	cases := []struct {
		name, def, cols, want string
	}{
		{"正确复合", ok, "(tenant_id, name)", "true"},
		{"列不符", ok, "(tenant_id, code)", "false"},
		{"非唯一同列", "CREATE INDEX idx_brand_tenant_name ON public.brands USING btree (tenant_id, name)", "(tenant_id, name)", "false"},
		{"历史单列全局唯一", "CREATE UNIQUE INDEX idx_brands_name ON public.brands USING btree (name)", "(tenant_id, name)", "false"},
		{"缺失索引", "", "(tenant_id, name)", "false"},
		{"前缀口径命中任意租户级唯一", ok, "(tenant_id, ", "true"},
		{"前缀口径不命中单列", "CREATE UNIQUE INDEX idx_brands_code ON public.brands USING btree (code)", "(tenant_id, ", "false"},
	}
	for _, tc := range cases {
		if got := fmt.Sprintf("%v", isTenantUniqueOn(tc.def, tc.cols)); got != tc.want {
			t.Errorf("%s: isTenantUniqueOn(%q, %q)=%s，期望 %s", tc.name, tc.def, tc.cols, got, tc.want)
		}
	}
}

// TestEnsureTenantUniqueIndexesRebuildsWrongDefinition 临时表证明修复路径仍在：
// 正确的做法是"删掉错的、建出对的"，而不是"什么都不做"。
func TestEnsureTenantUniqueIndexesRebuildsWrongDefinition(t *testing.T) {
	gdb := newTestDB(t)
	const tbl = "ut_tenant_unique_probe"
	const idx = "ut_probe_tenant_name"
	t.Cleanup(func() { gdb.Exec("DROP TABLE IF EXISTS " + tbl) })

	// 建一个**错形态**的租户表：(name) 全局单列唯一（正是 P1-2 的原缺陷形态）
	if err := gdb.Exec("DROP TABLE IF EXISTS " + tbl).Error; err != nil {
		t.Fatalf("清临时表失败: %v", err)
	}
	if err := gdb.Exec(
		"CREATE TABLE " + tbl + " (id bigserial PRIMARY KEY, tenant_id bigint NOT NULL, name text NOT NULL, code text)").Error; err != nil {
		t.Fatalf("建临时表失败: %v", err)
	}
	if err := gdb.Exec(
		"CREATE UNIQUE INDEX " + idx + " ON " + tbl + " (name)").Error; err != nil {
		t.Fatalf("建错的单列唯一索引失败: %v", err)
	}
	before := indexOids(t, gdb, idx)

	targets := []tenantUnique{{table: tbl, drop: []string{idx}, index: idx, cols: "(tenant_id, name)"}}
	ensureTenantUniqueIndexesFor(targets)

	def := indexDefsOf(t, gdb, idx)[idx]
	if !isTenantUniqueOn(def, "(tenant_id, name)") {
		t.Fatalf("错定义未被收敛，indexdef=%q", def)
	}
	if before[idx] == indexOids(t, gdb, idx)[idx] {
		t.Error("错索引 OID 没变 —— 说明根本没重建，收敛路径是空转")
	}
	// 反对照：同租户重名必须仍被拒（约束是真的，不是被整个去掉了）
	if err := gdb.Exec("INSERT INTO " + tbl + " (tenant_id, name) VALUES (1,'a'),(1,'a')").Error; err == nil {
		t.Error("同租户插两行同名竟然成功 —— 唯一约束没生效")
	}
	// 不同租户同名必须放行（SaaS 可用性本体）
	if err := gdb.Exec("INSERT INTO " + tbl + " (tenant_id, name) VALUES (1,'x'),(2,'x')").Error; err != nil {
		t.Errorf("跨租户同名被拒（P1-2 回归）: %v", err)
	}
}

// TestEnsureTenantUniqueIndexesRebuildsWrongColumnCombination 目标索引挂着"也是 tenant_id 打头、
// 但列组合不对"的定义（如 (tenant_id, code) 顶在 (tenant_id, name) 的名字下）时必须重建。
// 若只按"tenant_id 打头就别删"放过它，约束会以错误形态永久留存。
func TestEnsureTenantUniqueIndexesRebuildsWrongColumnCombination(t *testing.T) {
	gdb := newTestDB(t)
	const tbl = "ut_tenant_unique_probe2"
	const idx = "ut_probe2_tenant_name"
	t.Cleanup(func() { gdb.Exec("DROP TABLE IF EXISTS " + tbl) })

	if err := gdb.Exec("CREATE TABLE " + tbl +
		" (id bigserial PRIMARY KEY, tenant_id bigint NOT NULL, name text NOT NULL, code text)").Error; err != nil {
		t.Fatalf("建临时表失败: %v", err)
	}
	if err := gdb.Exec(
		"CREATE UNIQUE INDEX " + idx + " ON " + tbl + " (tenant_id, code)").Error; err != nil {
		t.Fatalf("建错列组合索引失败: %v", err)
	}
	before := indexOids(t, gdb, idx)[idx]

	ensureTenantUniqueIndexesFor([]tenantUnique{{table: tbl, drop: []string{idx}, index: idx, cols: "(tenant_id, name)"}})

	def := indexDefsOf(t, gdb, idx)[idx]
	if !isTenantUniqueOn(def, "(tenant_id, name)") {
		t.Fatalf("错列组合未被收敛，indexdef=%q", def)
	}
	if before == indexOids(t, gdb, idx)[idx] {
		t.Error("OID 未变 —— 错列组合被当成正确形态跳过了")
	}
	// 行为反证：同租户同 name 拒、同租户不同 name（哪怕 code 相同）必须放行
	if err := gdb.Exec("INSERT INTO " + tbl + " (tenant_id, name, code) VALUES (1,'a','c'),(1,'b','c')").Error; err != nil {
		t.Errorf("新约束列组合不对，按 name 唯一应放行: %v", err)
	}
}

// TestEnsureTenantUniqueIndexesNeverRebuildsHealthyIndexes 主守卫：健康库上再跑一遍
// 收敛不得产生任何 DROP/CREATE —— 所有目标索引 OID 必须逐一不变。
// 这是本轮假红的正向封堵：一旦有人把"先删后建"改回来，本测试即红。
func TestEnsureTenantUniqueIndexesNeverRebuildsHealthyIndexes(t *testing.T) {
	gdb := newTestDB(t)
	names := make([]string, 0, len(tenantUniqueTargets))
	for _, tc := range tenantUniqueTargets {
		names = append(names, tc.index)
	}
	before := indexOids(t, gdb, names...)
	for _, n := range names {
		if _, ok := before[n]; !ok {
			t.Fatalf("前置不成立：目标索引 %s 在库上不存在（Init 未收敛？）", n)
		}
	}

	ensureTenantUniqueIndexesFor(tenantUniqueTargets)

	after := indexOids(t, gdb, names...)
	for _, n := range names {
		if before[n] != after[n] {
			t.Errorf("%s 被重建了（OID %s → %s）—— 删→建之间的无约束空窗会重现 2026-09-23 假红，多实例并发重启时更会丢约束",
				n, before[n], after[n])
		}
	}
	// 定义也必须仍是租户级复合唯一（防"跳过重建但库本来就是错的"蒙混过关）
	defs := indexDefsOf(t, gdb, names...)
	for _, tc := range tenantUniqueTargets {
		if !isTenantUniqueOn(defs[tc.index], tc.cols) {
			t.Errorf("%s 定义不是 (%s) 复合唯一，indexdef=%q", tc.index, tc.cols, defs[tc.index])
		}
	}
}

// TestCurrentIndexDefsByTableMissingTableIsSafe 取现状失败/表不存在时不得 panic，
// 且必须退化为"看不见现状 → 走重建路径"（保守侧：宁发一次幂等 DDL，也不盲删）。
func TestCurrentIndexDefsByTableMissingTableIsSafe(t *testing.T) {
	gdb := newTestDB(t)
	_ = gdb
	if m := currentIndexDefsByTable("ut_no_such_table_at_all"); len(m) != 0 {
		t.Errorf("不存在的表应返回空 map，实得 %d 条：%v", len(m), m)
	}
	if isTenantUniqueOn(currentIndexDefsByTable("ut_no_such_table_at_all")["whatever"], "(tenant_id, x)") {
		t.Error("空定义必须判 false（缺失索引要走重建路径）")
	}
	// 真表必须有内容，否则本测试会因"永远查不到"而假绿
	if m := currentIndexDefsByTable("brands"); len(m) == 0 {
		t.Error("brands 表索引快照为空 —— 取现状逻辑失效，健康跳过判定会全部退化为重建")
	}
}
