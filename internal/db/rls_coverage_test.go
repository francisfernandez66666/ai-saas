// G-14 收口批（2026-09-24）：RLS 清单完整性断言。
//
// 要防的事故形态：新增一张含 tenant_id 的租户表，AutoMigrate 自动建了、模型也写了，
// 唯独没往 rlsTenantTables 里登记——RLS_ENABLED 部署形态下这张表**零策略覆盖**，
// 而应用层 db.RQ 一旦有旁路（后台任务、裸句柄、未来新代码），这张表就是跨租户泄露的落点。
// 这个形态历史上命中三次（P1-5 修批一、复核批 P1-8 补 11 张、本批补 5 张获客/商机/存档表），
// 每一次都靠"人翻代码时发现"。
//
// 为什么必须落在测试层：此前唯一的对账写在 EnableRLS 里，而它受 RLS_ENABLED（默认 false）
// 控制，且发现问题只 log.Printf 一行 WARN——启动日志没人看，WARN 等于没有。
// 本文件把同一判据（rlsCoverageCheck + rlsTenantIDTables，与启动路径共用）变成 CI 硬断言：
// 漏登记即测试红。
package db

import (
	"sort"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// TestRLSChecklistCoversEveryTenantTable 主断言：库内每一张含 tenant_id 的基表，
// 要么在 RLS 清单里，要么在**封闭的**豁免集里；清单里也不许有库里不存在的表。
func TestRLSChecklistCoversEveryTenantTable(t *testing.T) {
	gdb := newTestDB(t)

	dbTables, err := rlsTenantIDTables(gdb)
	if err != nil {
		t.Fatalf("查询含 tenant_id 的基表失败: %v", err)
	}
	// 前置自检（缺它整段会在空集上假绿）：库必须真有租户表，否则"零漏网"毫无意义。
	// 判据取"清单条数量级"而不是硬编码数字——清库、只跑部分迁移的实例都可能表数不同，
	// 但一条真表都没查出来一定是查询本身失配（比如 search_path 指错库）。
	if len(dbTables) < 20 {
		t.Fatalf("information_schema 只查出 %d 张含 tenant_id 的表，前提不成立（查询失配或库未迁移）：%v", len(dbTables), dbTables)
	}

	unlisted, phantom, deadExempt := rlsCoverageCheck(dbTables, rlsTenantTables, rlsExemptTables)
	if len(unlisted) > 0 {
		t.Errorf("下列含 tenant_id 的表未登记进 rlsTenantTables（RLS 激活后不受 DB 收敛）。请逐张判定：属租户数据→加进清单；属平台表→加进 rlsExemptTables 并写明理由：%v", unlisted)
	}
	if len(phantom) > 0 {
		t.Errorf("rlsTenantTables 含库中不存在（或已不含 tenant_id 列）的表：%v——启用 RLS 时此处即 Fatal", phantom)
	}
	if len(deadExempt) > 0 {
		t.Errorf("豁免集含库中不存在（或不含 tenant_id）的键：%v——死豁免要删，留着等于给未来同名新表预先开门", deadExempt)
	}
	if dups := rlsTenantTablesDuplicate(rlsTenantTables); len(dups) > 0 {
		t.Errorf("rlsTenantTables 有重复登记：%v", dups)
	}
	// 清单非空且每条都有租户归属：把"整个清单被误清空"这种最恶性漂移也钉住
	if len(rlsTenantTables) < 50 {
		t.Errorf("rlsTenantTables 只有 %d 条，明显少于历史规模（≥50），疑似清单被误清空或整段丢失", len(rlsTenantTables))
	}
}

// TestRLSExemptSetIsClosed 豁免集封闭性：新增一条豁免必须显式改这个测试，
// 否则"把租户表塞进豁免集"就能绕过上面的主断言——豁免集本身若不设防，整套对账形同虚设。
func TestRLSExemptSetIsClosed(t *testing.T) {
	var keys []string
	for k, reason := range rlsExemptTables {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("豁免表 %s 没有写理由：豁免是关掉一道 DB 级隔离，必须留下为什么", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	got := strings.Join(keys, ",")
	want := "system_configs" // 目前唯一设计内豁免：平台双层配置表（理由见 rls.go 注释）
	if got != want {
		t.Errorf("豁免集已变化\n得到: %s\n期望: %s\n若确属新增平台表：改本断言并在 rlsExemptTables 补理由；若那是租户业务表：加进 rlsTenantTables，不要走豁免", got, want)
	}
	// 反向：豁免与清单不许交叉——同一张表既登记又豁免，等于用豁免把清单里的漏网洗白
	for _, tbl := range rlsTenantTables {
		if _, ok := rlsExemptTables[tbl]; ok {
			t.Errorf("表 %s 同时在 rlsTenantTables 与 rlsExemptTables：二者必须互斥", tbl)
		}
	}
}

// TestRLSCoverageCheckDetectsDrift 反证用例：证明主断言不是空转。
// 全部跑在**合成数据**上，不读库也不改包变量——缺这组用例时，"主断言绿"可能只是因为
// 判据函数写错了（比如把集合比对写成了恒返回空），而这类错误在真库上看不出来。
func TestRLSCoverageCheckDetectsDrift(t *testing.T) {
	all := []string{"customers", "messages", "system_configs", "deals_new"}
	listed := []string{"customers", "messages", "deals_new"}
	exempt := map[string]string{"system_configs": "平台表"}

	t.Run("健康态必须零问题", func(t *testing.T) {
		unlisted, phantom, dead := rlsCoverageCheck(all, listed, exempt)
		if len(unlisted)+len(phantom)+len(dead) != 0 {
			t.Fatalf("健康态被报出问题（说明判据过严，真库上会恒红）：%v / %v / %v", unlisted, phantom, dead)
		}
	})

	t.Run("新表漏登记必须被点名", func(t *testing.T) {
		// 从清单副本里抽掉一张真在库里的表——正是"加了模型忘了加清单"的形态
		drifted := []string{"customers", "messages"}
		unlisted, _, _ := rlsCoverageCheck(all, drifted, exempt)
		if len(unlisted) != 1 || unlisted[0] != "deals_new" {
			t.Fatalf("漏登记没被抓到，得到 %v（期望 [deals_new]）", unlisted)
		}
	})

	t.Run("清单里的拼错表名必须被点名", func(t *testing.T) {
		// customers 保住，messages 拼成 mesages：清单侧报幻影表名，库侧 messages 同时变成漏网
		unlisted, phantom, _ := rlsCoverageCheck(all, []string{"customers", "mesages"}, exempt)
		if len(phantom) != 1 || phantom[0] != "mesages" {
			t.Fatalf("拼错的表名没被抓到，得到 %v（期望 [mesages]）", phantom)
		}
		if len(unlisted) != 2 || unlisted[0] != "messages" || unlisted[1] != "deals_new" {
			t.Fatalf("真表 messages 应报漏网（顺序跟 all），得到 %v（期望 [messages deals_new]）", unlisted)
		}
	})

	t.Run("表被删后豁免键必须报死豁免", func(t *testing.T) {
		remaining := []string{"customers", "messages", "deals_new"}
		_, _, dead := rlsCoverageCheck(remaining, []string{"customers", "messages", "deals_new"}, exempt)
		if len(dead) != 1 || dead[0] != "system_configs" {
			t.Fatalf("死豁免没被抓到，得到 %v（期望 [system_configs]）", dead)
		}
	})

	t.Run("把租户表塞进豁免集不算登记（空理由不生效）", func(t *testing.T) {
		// 有人想靠"加个空壳豁免"绕过主断言：deals_new 不进清单、只塞一条理由为空的豁免
		unlisted, _, _ := rlsCoverageCheck(all, []string{"customers", "messages"}, map[string]string{"system_configs": "平台表", "deals_new": "  "})
		if len(unlisted) != 1 || unlisted[0] != "deals_new" {
			t.Fatalf("空理由豁免被当成了有效豁免，得到 %v（期望 [deals_new]）", unlisted)
		}
	})
}

// TestRLSDormantPolicyQualSemantics 把休眠式策略的行为契约钉成 SQL 求值。
//
// 契约两条：
//  1. **未 SET app.current_tenant → 表达式对全表恒真**（后台任务、迁移、超管跨租户视图全靠这条腿）；
//  2. **SET 之后 → 只对本租户那几行成立**，且设成不存在的租户时谁都不成立。
//
// 为什么不是"建行表、开 RLS、数返回行数"：本仓连接账号 `ai_scrm` 无 CREATEROLE 权限
// （`SELECT rolcreaterole FROM pg_roles WHERE rolname=current_user` = f），造不出
// 非 owner 的探针角色；而 PG 对表 owner 与 SUPERUSER 默认旁路策略（正因如此生产才 FORCE），
// 用 owner 身份数行数会得到恒等于 2 的假通过。所以这里改测**谓词本身**：
// 先用真表建策略并从 pg_policies 读回 qual，证明"PG 存下来的表达式 = rlsPolicyUsing"，
// 再把同一段表达式放进 WHERE 对真行求值——这两步合起来等价于策略生效时的逐行判定。
func TestRLSDormantPolicyQualSemantics(t *testing.T) {
	gdb := newTestDB(t)
	const tbl = "rls_qual_probe"

	if err := gdb.Exec("DROP TABLE IF EXISTS " + tbl).Error; err != nil {
		t.Fatalf("清理探针表失败: %v", err)
	}
	t.Cleanup(func() { _ = gdb.Exec("DROP TABLE IF EXISTS " + tbl) })
	if err := gdb.Exec("CREATE TABLE " + tbl + " (id serial PRIMARY KEY, tenant_id bigint)").Error; err != nil {
		t.Fatalf("建探针表失败: %v", err)
	}
	if err := gdb.Exec("INSERT INTO " + tbl + " (tenant_id) VALUES (11), (22), (11)").Error; err != nil {
		t.Fatalf("插探针数据失败: %v", err)
	}
	if err := gdb.Exec("ALTER TABLE " + tbl + " ENABLE ROW LEVEL SECURITY").Error; err != nil {
		t.Fatalf("开启 RLS 失败: %v", err)
	}
	// 用生产同一模板建策略：模板若写成非法 SQL，这里直接红（而不是等到启用 RLS 的部署启动失败）
	if err := gdb.Exec("DROP POLICY IF EXISTS tenant_isolation ON " + tbl).Error; err != nil {
		t.Fatalf("清理旧策略失败: %v", err)
	}
	if err := gdb.Exec(rlsPolicySQL(tbl)).Error; err != nil {
		t.Fatalf("按 rlsPolicySQL 模板建策略失败（模板已不是合法 PG 语法？）: %v", err)
	}
	t.Cleanup(func() { _ = gdb.Exec("DROP POLICY IF EXISTS tenant_isolation ON " + tbl) })

	// ① pg_policies 里存的 qual 必须与模板表达式一致（空白归一后逐字相等）
	var stored string
	if err := gdb.Raw("SELECT qual FROM pg_policies WHERE schemaname = current_schema() AND tablename = ? AND policyname = 'tenant_isolation'", tbl).Scan(&stored).Error; err != nil {
		t.Fatalf("读回策略 qual 失败: %v", err)
	}
	if norm := normalizeSQLExpr(stored); !strings.Contains(norm, "current_setting('app.current_tenant'::text, true) is null") {
		t.Fatalf("PG 存的策略 qual 里没有\"GUC 未设置即放行\"那条腿（休眠式设计被破坏）: %s", stored)
	}
	// 反向自证：这个检查不是空转——把 NULL 腿手工删掉后，同一段判据必须认不出来
	if norm := normalizeSQLExpr("tenant_id::text = current_setting('app.current_tenant', true)"); strings.Contains(norm, "current_setting('app.current_tenant'::text, true) is null") {
		t.Fatalf("判据空转：删掉 NULL 腿的表达式仍被判为合格")
	}

	// ② 表达式对真行求值（WHERE 里直接用读回的 qual，保证测的就是 PG 存的那一份）
	qualExpr := stored
	countWith := func(setting *string) int {
		t.Helper()
		var n int
		err := gdb.Transaction(func(tx *gorm.DB) error {
			// is_local=true：提交即复位，绝不把 GUC 泄漏进连接池会话（否则污染同进程其它测试）
			if setting == nil {
				// "未设置"这一腿必须真的不 SET：set_config 传空串得到的是 ''（非 NULL），
				// '' IS NULL 为假 → 测的是"设置了但不匹配"，不是休眠放行，两者不能混。
				return tx.Raw("SELECT count(*) FROM " + tbl + " WHERE (" + qualExpr + ")").Scan(&n).Error
			}
			if err := tx.Exec("SELECT set_config('app.current_tenant', ?, true)", *setting).Error; err != nil {
				return err
			}
			return tx.Raw("SELECT count(*) FROM " + tbl + " WHERE (" + qualExpr + ")").Scan(&n).Error
		})
		if err != nil {
			t.Fatalf("表达式求值失败: %v", err)
		}
		return n
	}
	t11, t999 := "11", "999"
	if n := countWith(nil); n != 3 {
		t.Errorf("休眠式放行失效：未 SET app.current_tenant 时表达式只对 %d/3 行成立（期望 3 行全放行）——后台任务与迁移依赖这条腿", n)
	}
	if n := countWith(&t11); n != 2 {
		t.Errorf("RLS 未收敛：SET app.current_tenant='11' 后表达式命中 %d 行（期望 2 行，即只放行本租户）", n)
	}
	if n := countWith(&t999); n != 0 {
		t.Errorf("RLS 串租户：SET app.current_tenant='999'（无此租户）后表达式命中 %d 行（期望 0）", n)
	}
	// ③ 反证：删掉 NULL 腿后，"未设置"必须从 3 行翻成 0 行——证明 ② 的第一条断言真的在看这条腿
	var noDormant int
	if err := gdb.Raw("SELECT count(*) FROM " + tbl + " WHERE (tenant_id::text = current_setting('app.current_tenant', true))").Scan(&noDormant).Error; err != nil {
		t.Fatalf("反证查询失败: %v", err)
	}
	if noDormant != 0 {
		t.Fatalf("反证失效：删掉 NULL 腿后未设置 GUC 仍命中 %d 行（期望 0），说明上面的断言抓不到这类回归", noDormant)
	}
}

// normalizeSQLExpr 折叠空白并转小写，用于比较 PG 规整过的表达式文本。
func normalizeSQLExpr(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}
