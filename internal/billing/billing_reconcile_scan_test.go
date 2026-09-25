// 对账取单查询的形状回归锁（2026-09-25 残项收口批）。
//
// 背景：ReconcileBilling 的两条扫描原写法是「Limit(200) 不带 ORDER BY」。
// 这不是性能问题，是**公平性问题**：Postgres 对无排序的 LIMIT 取哪 N 行由执行计划决定，
// 一旦"paid 缺台账"的行数长期超过 200（DB 抖动、发放事务反复失败、批量补录都可能），
// 每轮扫到的都是同一批计划偏爱的订单，**另一些单会被永久饿死**——
// 客户钱付了、权益永远不发，而日志每轮都写"对账完成"，现场看不出任何异常。
//
// 为什么这么测：sqlite 不保证 Postgres 的行序，拿"两轮扫到同一批"当判据是**测试自伤**
// （在 sqlite 上恒绿、在 PG 上才是真判据）。故这里直接对**生产用的那个查询构造器**
// 做 DryRun 取 SQL——排序一旦从单点消失，本用例立刻红，且它测的就是线上真正跑的那段。
//
// 反证（2026-09-25 实测）：把 Order("id ASC") 从两个构造器里删掉，本文件三条断言全红；
// 把它改成 id DESC 两条用例同样各自红——排序键是**等值断言**，不是"有个 ORDER BY 就算过"。
package billing

import (
	"strings"
	"testing"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"

	"gorm.io/gorm"
)

// explainQuery 以 DryRun 方式取出生成 SQL（不执行、不碰数据）
func explainQuery(t *testing.T, build func(*gorm.DB) *gorm.DB) string {
	t.Helper()
	if build == nil {
		t.Fatal("谓词构造器未注入：本用例等于空转")
	}
	stmt := build(db.DB.Session(&gorm.Session{DryRun: true})).
		Find(&[]model.BillingOrder{}).Statement
	if stmt.SQL.Len() == 0 {
		t.Fatal("DryRun 未生成 SQL，断言无从谈起")
	}
	return stmt.Dialector.Explain(stmt.SQL.String(), stmt.Vars...)
}

// TestReconcileScansAreDeterministicallyOrdered 两条对账扫描都必须带确定性排序与批量上限。
func TestReconcileScansAreDeterministicallyOrdered(t *testing.T) {
	testutil.SetupTestDB(t)
	cases := []struct {
		name  string
		build func(*gorm.DB) *gorm.DB
	}{
		{"paid缺台账", reconcileUngrantedQuery},
		{"退款出款意图缺失", reconcilePayoutMissingQuery},
	}
	for _, tc := range cases {
		sql := explainQuery(t, tc.build)
		upper := strings.ToUpper(sql)
		if !strings.Contains(upper, "ORDER BY") {
			t.Errorf("%s 扫描缺 ORDER BY（无排序的 LIMIT 会让部分订单永久饿死）: %s", tc.name, sql)
			continue
		}
		if !strings.Contains(upper, "LIMIT") {
			t.Errorf("%s 扫描缺 LIMIT（对账单轮不设上限会跑成长事务）: %s", tc.name, sql)
			continue
		}
		// 等值锁而非单向锁：排序键也要钉死为 id ASC（最老的先补），
		// 只断"有 ORDER BY"的话，改成 id DESC 一样绿——那恰好把饿死方向反过来。
		if !strings.Contains(upper, `ORDER BY "BILLING_ORDERS"."ID" ASC`) &&
			!strings.Contains(upper, "ORDER BY ID ASC") {
			t.Errorf("%s 扫描排序不是 id ASC（口径=先付钱的先补，且取到的集合唯一确定）: %s", tc.name, sql)
		}
	}
}

// TestReconcileUngrantedKeepsOldestFirst 排序键取 id ASC 的业务含义：
// 同一批待补单里，最老的那笔必须在第一页里，而不是被"计划偏爱"挤出窗口。
func TestReconcileUngrantedKeepsOldestFirst(t *testing.T) {
	testutil.SetupTestDB(t)
	upper := strings.ToUpper(explainQuery(t, reconcileUngrantedQuery))
	if idxID, idxCreated := strings.Index(upper, `ORDER BY "BILLING_ORDERS"."ID" ASC`), strings.Index(upper, "ORDER BY ID ASC"); idxID < 0 && idxCreated < 0 {
		t.Fatalf("未断到 id 升序: %s", upper)
	}
	// LIMIT 必须排在 ORDER BY 之后（否则 SQL 语法即错，说明构造器被改坏）
	if strings.Index(upper, "ORDER BY") > strings.Index(upper, "LIMIT") {
		t.Errorf("生成 SQL 里 LIMIT 出现在 ORDER BY 之前，排序不再生效: %s", upper)
	}
}
