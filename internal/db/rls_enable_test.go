// FIX-A（2026-09-28）机理反证：ENABLE ROW LEVEL SECURITY 才是"策略通电"的那一位，
// FORCE 只关"表 owner 旁路"，不含 ENABLE 语义。
//
// 要钉住的事实链：本缺陷能静默存活三年（002 起两条挂策略路径都只写 FORCE），
// 正是因为没人用机器验证过"PG 到底在什么条件下才咨询策略"。启动日志宣称
// "已对 N 张租户表启用行级隔离"，而 pg_class.relrowsecurity 全为 false——
// 2026-09-28 本机实测锤实（14 张表挂 tenant_isolation 策略、通电 0 张）。
//
// 本用例把这条 PG 语义固化成断言：同一张表、同一条策略，
// 只跑 FORCE 时 relrowsecurity 必须为 false（缺陷机理在场：策略挂了但永不生效）；
// 补上 ENABLE 后必须翻成 true（修法有效）。缺了这个前提，
// rls.go 里"ENABLE 排在 FORCE 之前"的写法就只是没有根据的仪式。
//
// 同文件其余用例是通电之后才看得见的形态（每条都带自己的反证，2026-09-28 本轮实跑抓到）：
//   - TestRLSGUCResetToEmptyStringOnPooledConn：SET LOCAL 提交后同连接 GUC 复位成**空串**而非 NULL，
//     只看 IS NULL 的休眠腿在这种连接上把"未激活"判成"激活且租户号为空"→ 读空集／写 42501
//     （internal/billing 九条用例的首发现现场）；
//   - TestSetTenantRLSRejectsNonTransactionHandle：非事务句柄上的 SET LOCAL 是"只 WARNING 不报错"的
//     no-op，现在必须当场拒绝；
//   - TestRLSMigrationPolicyTextMatchesGoTemplate：迁移 031 的 SQL 文本与 Go 模板 rlsPolicyUsing
//     必须渲染成同一份判据（两份表达式各写一处即"测 A 建 B"）；
//   - TestRLSPolicyFaceObservation：观测位必须能点名"已通电但策略缺休眠腿"的表（走生产读路径 + 逐腿反证），
//     否则 /status 上"通电"与"策略内容合格"仍被混为一谈。
package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// TestRLSForceWithoutEnableLeavesPolicyDormant FIX-A 机理反证：FORCE ≠ 通电，ENABLE 才是。
func TestRLSForceWithoutEnableLeavesPolicyDormant(t *testing.T) {
	gdb := newTestDB(t)
	const tbl = "rls_enable_probe"

	if err := gdb.Exec("DROP TABLE IF EXISTS " + tbl).Error; err != nil {
		t.Fatalf("清理探针表失败: %v", err)
	}
	t.Cleanup(func() {
		_ = gdb.Exec("DROP POLICY IF EXISTS tenant_isolation ON " + tbl)
		_ = gdb.Exec("DROP TABLE IF EXISTS " + tbl)
	})
	if err := gdb.Exec("CREATE TABLE " + tbl + " (id serial PRIMARY KEY, tenant_id bigint)").Error; err != nil {
		t.Fatalf("建探针表失败: %v", err)
	}
	// 用生产同一模板建策略：先证明"策略在场"，否则后面的 false 可能只是"没挂上策略"，
	// 断言就抓不到"挂了但永不咨询"这个真正的缺陷形态。
	if err := gdb.Exec("DROP POLICY IF EXISTS tenant_isolation ON " + tbl).Error; err != nil {
		t.Fatalf("清理旧策略失败: %v", err)
	}
	if err := gdb.Exec(rlsPolicySQL(tbl)).Error; err != nil {
		t.Fatalf("按 rlsPolicySQL 模板建策略失败: %v", err)
	}
	var polN int
	if err := gdb.Raw("SELECT count(*) FROM pg_policies WHERE schemaname = current_schema() AND tablename = ? AND policyname = 'tenant_isolation'", tbl).Scan(&polN).Error; err != nil || polN != 1 {
		t.Fatalf("前置不成立：探针表上 tenant_isolation 策略数=%d（期望 1，err=%v）——策略不在场时后面的 relrowsecurity 断言毫无判别力", polN, err)
	}

	// ① 只跑旧路径（FORCE，不带 ENABLE）：策略挂着，但 pg_class.relrowsecurity 必须为 false。
	//    这就是本缺陷的机理——旧 EnableRLS 循环干完这件事就宣称"已启用"。
	if err := gdb.Exec("ALTER TABLE " + tbl + " FORCE ROW LEVEL SECURITY").Error; err != nil {
		t.Fatalf("FORCE 失败: %v", err)
	}
	var dormant bool
	if err := gdb.Raw("SELECT relrowsecurity FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = ?", tbl).Scan(&dormant).Error; err != nil {
		t.Fatalf("读取 relrowsecurity 失败: %v", err)
	}
	if dormant {
		t.Fatalf("PG 语义前提被破坏：只 FORCE 未 ENABLE，relrowsecurity 却为 true——" +
			"若真是这样，FIX-A 补 ENABLE 的整个立论都不成立，请重查本用例的探针表状态")
	}

	// ② 补上 FIX-A 的修法（EnableRLS 循环现在发的同一条语句）：必须翻成 true。
	if err := gdb.Exec("ALTER TABLE " + tbl + " ENABLE ROW LEVEL SECURITY").Error; err != nil {
		t.Fatalf("ENABLE 失败: %v", err)
	}
	var live bool
	if err := gdb.Raw("SELECT relrowsecurity FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = ?", tbl).Scan(&live).Error; err != nil {
		t.Fatalf("读取 relrowsecurity 失败: %v", err)
	}
	if !live {
		t.Fatalf("修法无效：ENABLE 之后 relrowsecurity 仍为 false，策略永远不会被咨询")
	}

	// ③ 顺带钉住 FORCE 自己的位没被 ENABLE 挤掉：两个标志位各管各的
	//（relforcerowsecurity 管 owner 旁路，relrowsecurity 管"是否咨询策略"），
	// 旧注释把 FORCE 当成"启用"的元凶正是混淆了这两位。
	var forced bool
	if err := gdb.Raw("SELECT relforcerowsecurity FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = ?", tbl).Scan(&forced).Error; err != nil {
		t.Fatalf("读取 relforcerowsecurity 失败: %v", err)
	}
	if !forced {
		t.Errorf("① 步的 FORCE 应留在表上（ENABLE 不冲掉 FORCE）：relforcerowsecurity=%v，期望 true", forced)
	}
}

// TestRLSGUCResetToEmptyStringOnPooledConn 把"补 ENABLE 之后才第一次能被观察到的连接池形态"钉成断言。
//
// 立论前提（本轮不是推断，是 psql 逐条量出来的）：PG 对**自定义 GUC** 的 SET LOCAL 复位，
// 是把值置成**空串**而不是变回"未定义"。于是任何跑过一次 WithTenantRLS/SetTenantRLS 的连接，
// 此后 current_setting('app.current_tenant', true) 恒为 ”。策略只要只看 `IS NULL` 一腿判休眠，
// 这条连接就把"未激活"读成"激活且租户号为空"——读为空集、写为 42501。
// 030 通电之前这套判据永不被咨询，所以缺陷从 002 活到今天无人可见；
// 本轮 internal/billing 九条用例红（`new row violates row-level security policy for table "billing_orders"`，
// 单跑绿、整包红）就是它第一次现形。
//
// 为什么这里要"真建表 + ENABLE + FORCE + 真策略"而不是像 rls_coverage_test 那样在 WHERE 里求值：
// 本用例要证的是**策略本身在脏连接上的行为**，求值位置（谓词在策略里 vs 在 WHERE 里）正是要考察的对象；
// FORCE 是必需的——不 FORCE 时表 owner 旁路策略，探针会稳定读到全表而行数恒等，断言毫无判别力。
//
// 反证同批在场：同一台脏连接上，旧两腿写法建的探针表必须读到 0 行。
// 缺了它，"三腿表读到 3 行"可能是 owner 旁路或策略没生效造成的假绿。
func TestRLSGUCResetToEmptyStringOnPooledConn(t *testing.T) {
	gdb := newTestDB(t)
	ctx := context.Background()
	const tblThreeLeg = "rls_guc_three_leg"
	const tblTwoLeg = "rls_guc_two_leg"

	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("取 *sql.DB 失败（本用例要钉住一条具体连接，必须有原生连接池）: %v", err)
	}
	// 前置自检：拿不到"同一条连接"就没有本用例可谈——Conn() 失败必须直接红，不能退化成"随便查一台"。
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatalf("固定单条连接失败: %v", err)
	}
	defer conn.Close()

	dropAll := func() {
		_ = gdb.Exec("DROP POLICY IF EXISTS tenant_isolation ON " + tblThreeLeg)
		_ = gdb.Exec("DROP POLICY IF EXISTS tenant_isolation ON " + tblTwoLeg)
		_ = gdb.Exec("DROP TABLE IF EXISTS " + tblThreeLeg)
		_ = gdb.Exec("DROP TABLE IF EXISTS " + tblTwoLeg)
	}
	dropAll()
	t.Cleanup(dropAll)

	mkProbe := func(tbl, using string) {
		t.Helper()
		if err := gdb.Exec("CREATE TABLE " + tbl + " (id serial PRIMARY KEY, tenant_id bigint)").Error; err != nil {
			t.Fatalf("建探针表 %s 失败: %v", tbl, err)
		}
		if err := gdb.Exec("INSERT INTO " + tbl + " (tenant_id) VALUES (11), (22), (11)").Error; err != nil {
			t.Fatalf("插探针数据 %s 失败: %v", tbl, err)
		}
		if err := gdb.Exec("ALTER TABLE " + tbl + " ENABLE ROW LEVEL SECURITY").Error; err != nil {
			t.Fatalf("ENABLE %s 失败: %v", tbl, err)
		}
		if err := gdb.Exec("ALTER TABLE " + tbl + " FORCE ROW LEVEL SECURITY").Error; err != nil {
			t.Fatalf("FORCE %s 失败（不 FORCE 则 owner 旁路，本用例判别力为零）: %v", tbl, err)
		}
		if err := gdb.Exec(fmt.Sprintf("CREATE POLICY tenant_isolation ON %s FOR ALL USING (%s)", tbl, using)).Error; err != nil {
			t.Fatalf("建策略 %s 失败: %v", tbl, err)
		}
	}
	// 三腿＝修好的写法（直接用生产模板，模板改了这里跟着变）；两腿＝本轮踩到的旧写法。
	mkProbe(tblThreeLeg, rlsPolicyUsing)
	mkProbe(tblTwoLeg, `tenant_id::text = current_setting('app.current_tenant', true)
				OR current_setting('app.current_tenant', true) IS NULL`)

	countOn := func(tbl string) int {
		t.Helper()
		var n int
		if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM "+tbl).Scan(&n); err != nil {
			t.Fatalf("在钉住的连接上查 %s 失败: %v", tbl, err)
		}
		return n
	}

	// ① 干净状态先量一遍：这台连接此刻的 GUC 是什么。
	//    注意这台连接是从池子里借的，**不保证**是全新的——全新才该是 NULL。
	//    所以这里不断言 NULL（那会因为借到一台脏连接而假红），只把读数打进日志。
	var gucBefore sql.NullString
	if err := conn.QueryRowContext(ctx, "SELECT current_setting('app.current_tenant', true)").Scan(&gucBefore); err != nil {
		t.Fatalf("读 GUC 现值失败: %v", err)
	}
	t.Logf("借到的连接初始 GUC：nil=%v 值=%q", !gucBefore.Valid, gucBefore.String)

	// ② 在这条连接上跑一次"事务内激活 + 提交"（生产里每次 WithTenantRLS 都留下这个痕迹）
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatalf("BEGIN 失败: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "SET LOCAL app.current_tenant = '11'"); err != nil {
		t.Fatalf("SET LOCAL 失败: %v", err)
	}
	// 事务内先自证激活态确实收敛（只放行 11 的两行）——否则"提交后读到什么"都无从比较
	var inTx int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM "+tblThreeLeg).Scan(&inTx); err != nil {
		t.Fatalf("事务内计数失败: %v", err)
	}
	if inTx != 2 {
		t.Fatalf("前置不成立：事务内 SET LOCAL='11' 后三腿表读到 %d 行（期望 2）——"+
			"等值腿没生效说明策略没被咨询（FORCE/ENABLE 或角色旁路），后面的脏连接断言全是空转", inTx)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatalf("COMMIT 失败: %v", err)
	}

	// ③ 本用例的立论前提：同一条连接、事务结束之后，GUC 复位成**空串**（不是 NULL）。
	var gucAfter sql.NullString
	if err := conn.QueryRowContext(ctx, "SELECT current_setting('app.current_tenant', true)").Scan(&gucAfter); err != nil {
		t.Fatalf("读复位后 GUC 失败: %v", err)
	}
	if gucAfter.Valid && gucAfter.String != "" {
		t.Fatalf("前提变了：提交后 GUC=%q（期望空串）——若 PG 不再把复位值置成空串，"+
			"rlsPolicyUsing 的空串腿与本用例的判据都要重新评估，请把这条红当作\"设计前提需复核\"处理", gucAfter.String)
	}
	t.Logf("提交后 GUC：NULL=%v 值=%q（NULL 与空串都必须按未激活处理）", !gucAfter.Valid, gucAfter.String)

	// ④ 关键性质：脏连接上未激活的普通查询必须仍看得见全表（三腿写法）
	if n := countOn(tblThreeLeg); n != 3 {
		t.Errorf("修好的写法没兜住连接池复位形态：SET LOCAL 提交后同连接查三腿表读到 %d/3 行（期望 3 全放行）——"+
			"生产现场就是这个数字变成 0，表现为\"跑过一次 WithTenantRLS 的实例随后全站读空集/写 42501\"", n)
	}
	// ⑤ 反证：旧两腿写法在同一台脏连接上必须读到 0 行——证明 ④ 真的在考察这条腿
	if n := countOn(tblTwoLeg); n != 0 {
		t.Errorf("反证失效：旧两腿写法在脏连接上读到 %d 行（期望 0）——"+
			"说明策略根本没被咨询（owner 旁路/ENABLE 未生效），④ 的 3 行是假绿，本用例判别力为零", n)
	}
}

// TestSetTenantRLSRejectsNonTransactionHandle 钉住 SetTenantRLS 的 fail-closed 护栏。
//
// 旧写法把 `SET LOCAL` 直接 Exec 在任何传进来的句柄上。传成根句柄（db.DB）时 PG 只给一句
// WARNING 就返回成功——调用方 `if r.Error != nil` 接不到任何东西，以为这一句之后就被 DB 强制
// 按租户收敛了，实际策略从未激活；而它顺带把那台连接的自定义 GUC 置成了空串（见上面的机理用例）。
// 补 ENABLE 之前这纯属"白跑一句 SQL"，之后它变成"以为有第二道闸、其实一道都没有"。
// 所以现在必须当场报错：判据是底层连接池类型是不是 *sql.Tx，而不是"看起来像事务"。
func TestSetTenantRLSRejectsNonTransactionHandle(t *testing.T) {
	gdb := newTestDB(t)

	// ① 反向：非事务句柄必须报错（这就是护栏本身）
	if r := SetTenantRLS(gdb, 1234); r.Error == nil {
		t.Fatalf("护栏失效：把根句柄传给 SetTenantRLS 竟然没报错——" +
			"SET LOCAL 在事务块外是 no-op，调用方会以为 RLS 已激活")
	} else {
		t.Logf("非事务句柄按预期被拒: %v", r.Error)
	}

	// ② 正向：真事务句柄必须放行（防"改成恒拒"）
	var inTxSetOK bool
	err := gdb.Transaction(func(tx *gorm.DB) error {
		if r := SetTenantRLS(tx, 1234); r.Error != nil {
			return r.Error
		}
		inTxSetOK = true
		return nil
	})
	if err != nil {
		t.Fatalf("事务句柄上的 SetTenantRLS 被误拒: %v", err)
	}
	if !inTxSetOK {
		t.Fatalf("前置不成立：正向对照没走到 SetTenantRLS 成功分支")
	}

	// ③ 判据本身要自证不是空转：*sql.Tx 与 *sql.DB 必须真能被区分开
	if _, isTx := gdb.Statement.ConnPool.(*sql.Tx); isTx {
		t.Fatalf("判据空转：根句柄的 ConnPool 竟然就是 *sql.Tx，① 的报错没有区分度")
	}
}

// TestRLSMigrationPolicyTextMatchesGoTemplate 证明"迁移 031 重建出来的策略"与
// "Go 模板 rlsPolicySQL 建出来的策略"是同一份判据——两处各写一份表达式文本，
// 就是"测的是 A、线上建的是 B"的经典现场（rls.go 里 rlsPolicySQL 的注释已点名这条风险）。
//
// 比较对象是**两份都由 PG 渲染过**的文本（pg_get_expr 出来的规整形态），不是文件原文：
// PG 会补 ::text 与括号，拿常量原文去比 SQL 文件里的字面量必然假红。
// 前置自检：库里必须真有一张挂着 tenant_isolation 的租户表（0==0 的等值断言不算证明）。
func TestRLSMigrationPolicyTextMatchesGoTemplate(t *testing.T) {
	gdb := newTestDB(t)
	const tbl = "rls_tpl_vs_migration"

	// 前置自检：真表里的 tenant_isolation 策略数（迁移 031/运行时 EnableRLS 的产物）
	var realPol int
	if err := gdb.Raw(`SELECT count(*) FROM pg_policies WHERE schemaname = current_schema()
		AND policyname = 'tenant_isolation' AND tablename <> ?`, tbl).Scan(&realPol).Error; err != nil {
		t.Fatalf("统计真表策略失败: %v", err)
	}
	if realPol == 0 {
		t.Fatalf("前置不成立：库里一张 tenant_isolation 策略都没有——" +
			"本用例要等值比较的两份文本之一不存在，等值断言会在空集上假绿（请确认迁移 031 已应用）")
	}

	if err := gdb.Exec("DROP POLICY IF EXISTS tenant_isolation ON " + tbl).Error; err != nil {
		t.Fatalf("清理探针策略失败: %v", err)
	}
	t.Cleanup(func() {
		_ = gdb.Exec("DROP POLICY IF EXISTS tenant_isolation ON " + tbl)
		_ = gdb.Exec("DROP TABLE IF EXISTS " + tbl)
	})
	if err := gdb.Exec("CREATE TABLE " + tbl + " (id serial PRIMARY KEY, tenant_id bigint)").Error; err != nil {
		t.Fatalf("建探针表失败: %v", err)
	}
	if err := gdb.Exec(rlsPolicySQL(tbl)).Error; err != nil {
		t.Fatalf("按模板建策略失败: %v", err)
	}
	readQual := func(name string) string {
		t.Helper()
		var q string
		if err := gdb.Raw("SELECT qual FROM pg_policies WHERE schemaname = current_schema() AND tablename = ? AND policyname = 'tenant_isolation'", name).Scan(&q).Error; err != nil {
			t.Fatalf("读 %s 的策略 qual 失败: %v", name, err)
		}
		return normalizeSQLExpr(q)
	}
	tplQual := readQual(tbl)
	// 取库里第一张真表的 qual（按表名排序取首个，结果稳定可复现）
	var realTbl string
	if err := gdb.Raw(`SELECT tablename FROM pg_policies WHERE schemaname = current_schema()
		AND policyname = 'tenant_isolation' AND tablename <> ? ORDER BY tablename LIMIT 1`, tbl).Scan(&realTbl).Error; err != nil {
		t.Fatalf("取真表名失败: %v", err)
	}
	migrQual := readQual(realTbl)
	if tplQual != migrQual {
		t.Fatalf("两份表达式漂移：Go 模板建的（探针表）与迁移/运行时建的（%s）不是同一份判据\n  模板 = %s\n  真表 = %s", realTbl, tplQual, migrQual)
	}
	// 反向自证：这个等值不是"谁都相等"的空转——旧两腿写法必须与模板不等
	if old := normalizeSQLExpr(`tenant_id::text = current_setting('app.current_tenant', true)
		OR current_setting('app.current_tenant', true) IS NULL`); old == tplQual {
		t.Fatalf("判据空转：旧两腿写法与三腿模板归一后相同，说明比较没在考察空串腿")
	}
}

// TestRLSPolicyFaceObservation 钉住 FIX-A 最后一块可观测位：
// RLSPolicyFace() 必须能把"已通电但策略缺休眠腿"的表**点名**出来。
//
// 为什么单独立一条：通电（relrowsecurity）与策略文本（polqual）是两件事，
// 旧观测位只看前者，于是"031 没跑/跑了但被人工 SQL 改回两腿写法"在 /status 上完全看不出来。
// 本用例走的是**生产读路径**（同一句 pg_policy 查询），不是把谓词单独测一遍：
// 反证阶段真的把探针表策略换成旧两腿写法，要求它出现在 MissingTables 里——
// 只测纯函数的话，"查询条件写错导致一张表都没圈进来"这种失效形态照样全绿。
func TestRLSPolicyFaceObservation(t *testing.T) {
	gdb := newTestDB(t)
	const tbl = "rls_policy_face_probe"

	if err := gdb.Exec("DROP POLICY IF EXISTS tenant_isolation ON " + tbl).Error; err != nil {
		t.Fatalf("清理探针策略失败: %v", err)
	}
	if err := gdb.Exec("DROP TABLE IF EXISTS " + tbl).Error; err != nil {
		t.Fatalf("清理探针表失败: %v", err)
	}
	t.Cleanup(func() {
		_ = gdb.Exec("DROP POLICY IF EXISTS tenant_isolation ON " + tbl)
		_ = gdb.Exec("DROP TABLE IF EXISTS " + tbl)
	})
	if err := gdb.Exec("CREATE TABLE " + tbl + " (id serial PRIMARY KEY, tenant_id bigint)").Error; err != nil {
		t.Fatalf("建探针表失败: %v", err)
	}
	// 通电 + 按模板挂三腿策略：让探针表落进 RLSPolicyFace 的圈定范围（"ENABLED 且有策略"）
	if err := gdb.Exec("ALTER TABLE " + tbl + " ENABLE ROW LEVEL SECURITY").Error; err != nil {
		t.Fatalf("ENABLE 失败: %v", err)
	}
	if err := gdb.Exec(rlsPolicySQL(tbl)).Error; err != nil {
		t.Fatalf("按模板建策略失败: %v", err)
	}

	// ① 正向：模板建的策略必须在"合格"那一侧，且 Checked 至少数到探针表（否则整段在空集上假绿）
	pf := RLSPolicyFace()
	if pf.QueryFailed {
		t.Fatalf("策略面查询失败（pg_policy 读不到？本用例的前提是查询可用）")
	}
	if pf.Checked < 1 {
		t.Fatalf("前置不成立：Checked=%d，探针表已 ENABLE 且挂了 tenant_isolation，却没被圈进来——"+
			"说明 RLSPolicyFace 的 SQL 条件写错，下面的等式会在空集上假绿", pf.Checked)
	}
	if pf.DormantLegMissing != 0 {
		t.Errorf("三腿策略被判缺腿：%v（missing=%d）——谓词与 PG 实际渲染文本对不上，观测位会把合格库报成故障", pf.MissingTables, pf.DormantLegMissing)
	}

	// ② 反证：换回旧两腿写法（没有空串腿），同一条读路径必须把它点名
	if err := gdb.Exec("DROP POLICY IF EXISTS tenant_isolation ON " + tbl).Error; err != nil {
		t.Fatalf("清策略失败: %v", err)
	}
	if err := gdb.Exec(`CREATE POLICY tenant_isolation ON ` + tbl + ` FOR ALL USING (
		tenant_id::text = current_setting('app.current_tenant', true)
		OR current_setting('app.current_tenant', true) IS NULL)`).Error; err != nil {
		t.Fatalf("建旧两腿策略失败: %v", err)
	}
	pf2 := RLSPolicyFace()
	if pf2.QueryFailed {
		t.Fatalf("反证查询失败: 不可判定")
	}
	found := false
	for _, n := range pf2.MissingTables {
		if n == tbl {
			found = true
		}
	}
	if !found {
		t.Fatalf("反证失效：探针表已换成缺空串腿的策略，RLSPolicyFace 却没把它列进 MissingTables（%v）——"+
			"这条观测位抓不到本轮修的缺陷形态，等于没有守卫", pf2.MissingTables)
	}
	// 反向对照：反证只准动探针表，别家的好策略不得被误伤计数
	for _, n := range pf2.MissingTables {
		if n != tbl {
			t.Errorf("误伤：非探针表 %s 也被判缺腿（本用例只改了探针表）", n)
		}
	}

	// ③ 谓词本身的逐腿反证（纯文本、零依赖）：只缺一条腿也必须判不合格
	canonical := func(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }
	threeLeg := canonical(`tenant_id::text = current_setting('app.current_tenant'::text, true)
		OR current_setting('app.current_tenant'::text, true) IS NULL
		OR current_setting('app.current_tenant'::text, true) = ''::text`)
	if !rlsPolicyHasDormantLegs(threeLeg) {
		t.Fatalf("判据空转：PG 规整后的三腿文本被自己判成缺腿")
	}
	noNull := canonical(`tenant_id::text = current_setting('app.current_tenant'::text, true)
		OR current_setting('app.current_tenant'::text, true) = ''::text`)
	noEmpty := canonical(`tenant_id::text = current_setting('app.current_tenant'::text, true)
		OR current_setting('app.current_tenant'::text, true) IS NULL`)
	if rlsPolicyHasDormantLegs(noNull) || rlsPolicyHasDormantLegs(noEmpty) {
		t.Errorf("缺任一腿必须判不合格（IS NULL 腿与空串腿各缺一次都要红），否则休眠式设计能被逐条拆掉")
	}

	// ④ nil-DB 安全：观测位在 DB 未连接时判"未能验证"，不得 panic、也不得伪装成"没事"
	saved := DB
	DB = nil
	if got := RLSPolicyFace(); !got.QueryFailed {
		t.Errorf("DB==nil 时必须置 QueryFailed（区分\"没看成\"与\"看成了、没事\"），got=%+v", got)
	}
	DB = saved
}
