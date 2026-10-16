// 034（2026-10-16）豁免的行为契约：cdp_tag_definitions 是双层字典表，等值策略会掐断系统层读腿。
//
// 现场还原（RLS 本机激活后 uat_advisor G-19 实锤，非推断）：
//   builtin 原子标签行 tenant_id=0 由 StartIngestConsumer 启动播种、全租户共用；
//   ListTagDefinitions 按 tenant_id IN (0,tid) 读——字典本就设计成"系统层+租户自定义"双层。
//   摄入事务 processEvent 内 SetTenantRLS(tx, tid) 之后，ApplyTagTx 按 code 查字典被
//   策略 tenant_id = current_setting(...) 过滤，tenant_id=0 行不可见 → First not found →
//   只 WARN 不中断 → 全部内置标签静默丢失（G-19 beh_* 四条期望=y 实际=n，事件落库全 PASS）。
//   休眠态策略恒真，故缺陷只在 RLS_ENABLED=true 形态现身——休眠式设计"藏到激活才炸"的又一例。
//
// 本文件两条用例分工：
//   - TestCdpTagDictExemptFromTenantIsolationPolicy：钉**落地形态**（库内无策略未通电 +
//     Go 清单在豁免集），带 customers 反向对照防"查询失配的假绿"；
//   - TestTenantIsolationPolicyHidesDictionaryBuiltinRows：钉**因果链**——探针表上挂策略时
//    激活事务里 builtin 行必然不可见（缺陷复现），摘策略后必然可见（034 修法）。
//     在探针表上做而非真表：真表按 034 必须保持无策略，本用例只证明"若挂上会怎样"。
package db

import (
	"strings"
	"testing"

	"gorm.io/gorm"
)

// TestCdpTagDictExemptFromTenantIsolationPolicy 钉 034 迁移 + rlsExemptTables 登记的落地形态。
// 前置：newTestDB → Init → MigrateUp 会先应用 034，所以本用例跑到的就是迁移后的库。
// 变异自证（改坏必须翻红）：把 034 up 改成 no-op / 把表搬回 rlsTenantTables，本用例 ①/③ 各有一腿会红。
func TestCdpTagDictExemptFromTenantIsolationPolicy(t *testing.T) {
	gdb := newTestDB(t)

	// ① 库内形态：无 tenant_isolation 策略（034 的 DROP POLICY 产物）
	var polN int
	if err := gdb.Raw(`SELECT count(*) FROM pg_policies WHERE schemaname = current_schema()
		AND tablename = 'cdp_tag_definitions' AND policyname = 'tenant_isolation'`).Scan(&polN).Error; err != nil {
		t.Fatalf("查询字典表策略失败: %v", err)
	}
	if polN != 0 {
		t.Errorf("cdp_tag_definitions 上仍有 %d 条 tenant_isolation 策略：迁移 034 未应用或被回退——"+
			"激活事务内 builtin 字典行（tenant_id=0）将被等值策略过滤，ApplyTagTx 只 WARN 不中断，全部内置标签静默丢失", polN)
	}

	// ② 未通电（034 的 DISABLE 产物）：无策略时若仍 ENABLE，默认拒绝对非 owner 角色生效，
	//    残留 FORCE 还会殃及 owner——形态必须是迁移 002 之前的未通电，而不是"摘了策略还通着电"。
	var rlsOn bool
	if err := gdb.Raw(`SELECT relrowsecurity FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relname = 'cdp_tag_definitions'`).Scan(&rlsOn).Error; err != nil {
		t.Fatalf("查询字典表 relrowsecurity 失败: %v", err)
	}
	if rlsOn {
		t.Errorf("cdp_tag_definitions 仍被 ENABLE ROW LEVEL SECURITY：034 的 DISABLE 未落地——"+
			"无策略+通电的形态对非 owner 连接是默认拒绝，比挂着策略更危险")
	}

	// ③ 反向对照：customers（002 基线首表、租户数据表）必须仍有策略——证明 ① 的 0 不是
	//    "整库都没策略 / search_path 指错库 / 查询写错表名"的假绿。
	var ctrlN int
	if err := gdb.Raw(`SELECT count(*) FROM pg_policies WHERE schemaname = current_schema()
		AND tablename = 'customers' AND policyname = 'tenant_isolation'`).Scan(&ctrlN).Error; err != nil {
		t.Fatalf("查询对照表策略失败: %v", err)
	}
	if ctrlN != 1 {
		t.Fatalf("前置不成立：customers 上 tenant_isolation 策略数=%d（期望 1）——对照表不在场时，① 的 0 毫无判别力", ctrlN)
	}

	// ④ Go 侧清单：字典表必须在豁免集且理由非空，不许回租户清单（回了启动即重建策略）。
	//    与 rls_coverage_test.go 的封闭性断言分工：那边钉"豁免集恰为这些键"，这边钉
	//    "这一张表的归属"——单独改一边（比如只动清单不动迁移）由两条各自红。
	if reason, ok := rlsExemptTables["cdp_tag_definitions"]; !ok || strings.TrimSpace(reason) == "" {
		t.Errorf("cdp_tag_definitions 未登记进 rlsExemptTables（或理由为空）：Go 侧与 034 迁移已分叉")
	}
	for _, tbl := range rlsTenantTables {
		if tbl == "cdp_tag_definitions" {
			t.Errorf("cdp_tag_definitions 又回到了 rlsTenantTables：EnableRLS 启动时会重建策略，把 G-19 缺陷装回库上")
		}
	}
}

// TestTenantIsolationPolicyHidesDictionaryBuiltinRows 因果链反证：字典表上一旦挂
// tenant_isolation 策略，激活事务里 builtin 行（tenant_id=0）必然不可见——G-19 的机理本身。
//
// 为什么在探针表上复现：真表按 034 必须保持无策略（由另一条用例钉形态），本用例要证明的是
// "挂策略 ⇒ builtin 不可见"这条因果，故自己在 rls_dict_probe 上建字典同构表（tenant_id + code），
// 先挂策略（缺陷态）再摘策略（034 态），同一激活事务下同一条查询的可见性必须从 0 翻成 1。
// 反证自证：两个方向的断言互为反证——"缺陷态读到 0"若不是 0，说明策略没生效（owner 旁路等），
// 因果链立论不成立；"修复态读到 1"若不是 1，说明 DROP/DISABLE 没起作用，034 修法无效。
func TestTenantIsolationPolicyHidesDictionaryBuiltinRows(t *testing.T) {
	gdb := newTestDB(t)
	const tbl = "rls_dict_probe"

	if err := gdb.Exec("DROP TABLE IF EXISTS " + tbl).Error; err != nil {
		t.Fatalf("清理探针表失败: %v", err)
	}
	t.Cleanup(func() {
		_ = gdb.Exec("DROP POLICY IF EXISTS tenant_isolation ON " + tbl)
		_ = gdb.Exec("DROP TABLE IF EXISTS " + tbl)
	})
	if err := gdb.Exec("CREATE TABLE " + tbl + " (id serial PRIMARY KEY, tenant_id bigint, code text)").Error; err != nil {
		t.Fatalf("建探针表失败: %v", err)
	}
	// 字典同构数据：一行 builtin（tenant_id=0，全租户共用）+ 一行租户自定义（tenant_id=7）。
	// ApplyTagTx 的真实查询就是 `WHERE code = ?`（不带租户条件），这里照搬。
	if err := gdb.Exec("INSERT INTO " + tbl + " (tenant_id, code) VALUES (0, 'beh_probe'), (7, 'beh_probe_custom')").Error; err != nil {
		t.Fatalf("插探针数据失败: %v", err)
	}

	// builtinVisible 在激活事务（SET LOCAL app.current_tenant='7'，与 SetTenantRLS 同语义）
	// 内按 code 查 builtin 行，返回行数。is_local=true 保证提交即复位，不污染连接池其它用例。
	builtinVisible := func() int {
		t.Helper()
		var n int
		if err := gdb.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("SELECT set_config('app.current_tenant', '7', true)").Error; err != nil {
				return err
			}
			return tx.Raw("SELECT count(*) FROM " + tbl + " WHERE code = 'beh_probe'").Scan(&n).Error
		}); err != nil {
			t.Fatalf("激活事务内查询字典失败: %v", err)
		}
		return n
	}

	// ① 缺陷态：挂策略（ENABLE+FORCE+三腿策略，与 EnableRLS/031 同款）后，激活事务里
	//    builtin 行必须不可见（=0）——这正是摄入事务内 ApplyTagTx 面对的世界。
	if err := gdb.Exec("ALTER TABLE " + tbl + " ENABLE ROW LEVEL SECURITY").Error; err != nil {
		t.Fatalf("探针表 ENABLE 失败: %v", err)
	}
	if err := gdb.Exec("ALTER TABLE " + tbl + " FORCE ROW LEVEL SECURITY").Error; err != nil {
		t.Fatalf("探针表 FORCE 失败: %v", err)
	}
	if err := gdb.Exec(rlsPolicySQL(tbl)).Error; err != nil {
		t.Fatalf("按生产模板建策略失败: %v", err)
	}
	if n := builtinVisible(); n != 0 {
		t.Fatalf("因果链立论不成立：字典表挂上 tenant_isolation 后，激活事务里 builtin 行仍可见 %d 行"+
			"（期望 0）——若不是 0，说明策略未真正生效（owner 旁路/未通电），G-19 的机理需要重估", n)
	}

	// ② 修复态：034 的同款动作（DROP POLICY + DISABLE）后，同一激活事务必须重新看到 builtin 行（=1）。
	if err := gdb.Exec("DROP POLICY IF EXISTS tenant_isolation ON " + tbl).Error; err != nil {
		t.Fatalf("探针表 DROP POLICY 失败: %v", err)
	}
	if err := gdb.Exec("ALTER TABLE " + tbl + " DISABLE ROW LEVEL SECURITY").Error; err != nil {
		t.Fatalf("探针表 DISABLE 失败: %v", err)
	}
	if n := builtinVisible(); n != 1 {
		t.Errorf("034 修法无效：摘策略+断电后，激活事务里 builtin 行仍只有 %d 行可见（期望 1）——"+
			"摄入事务会继续静默丢弃全部内置标签", n)
	}
}
