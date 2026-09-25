// 换包清旧内容（G-22，2026-09-24）单测：PurgeOtherPacks + 行 ID 反解 packCodeFromPrefixedID。
//
// 这一族断言要钉住的是"换包 ≠ 加包"这件事。缺陷现场很安静：
// ApplyToTenant 的先删后插只删本包前缀 pk_{code}_t{tenant}_ 的行，租户从 auto 换成 general 之后，
// auto 的模板/卖点/标签/覆盖键全都还在库里；而召回层（strategy.templatesForTenant）只看
// tenant_id + status，不看包绑定——AI 于是拿到两套人设混着说（切成通用行业还在约试驾）。
// 解绑口 UnbindFromTenant 一直存在，绑定路径上却从没调用过，所以缺陷不报错、只错内容。
//
// 断言方向刻意配了反向用例：keep 两个 code 时必须**什么都不删**（返回 0）。
// 只测"该删的删了"，实现很容易写成"全清"，那种实现在真接口上会顺手毁掉客户内容。
package industrypack

import (
	"testing"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// TestPackCodeFromPrefixedID 从物化行 ID 反解包 code：正向/歧义/非包行三向都钉
func TestPackCodeFromPrefixedID(t *testing.T) {
	cases := []struct {
		name string
		id   string
		tid  uint
		want string
	}{
		{"普通 code", "pk_auto_t7_tpl_1", 7, "auto"},
		// code 本身带下划线（auto_rox / auto_rox_sales 是真实存在的包名）：
		// 反解必须取**第一个** _t{tid}_，取最后一个会把 auto_rox 认成 auto_rox_sales 的前缀
		{"code 带下划线", "pk_auto_rox_t7_tpl_1", 7, "auto_rox"},
		{"code 多段下划线", "pk_auto_rox_sales_t7_tpl_1", 7, "auto_rox_sales"},
		// 租户号 1 与 11 同前缀：租户 1 的反解不能把 _t11_ 当成 _t1_ + "1..."
		{"租户号位数歧义", "pk_auto_t11_tpl_1", 11, "auto"},
		{"租户号不匹配则不认", "pk_auto_t11_tpl_1", 1, ""},
		{"非包前缀行（租户自建/种子）", "tpl_preset_001", 7, ""},
		{"缺租户段", "pk_auto_tpl_1", 7, ""},
		{"code 段为空", "pk__t7_x", 7, ""},
		{"空串", "", 7, ""},
	}
	for _, c := range cases {
		if got := packCodeFromPrefixedID(c.id, c.tid, "pk_"); got != c.want {
			t.Errorf("%s: packCodeFromPrefixedID(%q, tid=%d) = %q，期望 %q", c.name, c.id, c.tid, got, c.want)
		}
	}
}

// seedPackRows 往租户里塞一份"某个包已经物化过"的最小真相：模板+卖点+标签+三个覆盖键
func seedPackRows(t *testing.T, tid uint, code string, deptID *uint) {
	t.Helper()
	prefix := IDPrefix(code, tid)
	tpl := model.Template{
		ID: prefix + "tpl_1", TenantID: tid, Name: code + " 话术",
		AnchorType: 1, PromptTemplate: "开场：你好，我是" + code + "顾问",
		Status: 1, DepartmentID: deptID,
	}
	if err := db.DB.Create(&tpl).Error; err != nil {
		t.Fatalf("插入测试模板失败: %v", err)
	}
	feat := model.Feature{
		ID: prefix + "feat_1", TenantID: tid, FeatureName: code + " 卖点",
		DescTemplate: code + " 的描述", Status: 1, DepartmentID: deptID,
	}
	if err := db.DB.Create(&feat).Error; err != nil {
		t.Fatalf("插入测试卖点失败: %v", err)
	}
	tag := model.Tag{
		// 标签只到租户级（无部门列，见 materializeTags 注释），部门包也写租户-wide
		Code: prefix + "tag_1", TenantID: tid, Name: code + " 标签_" + prefix, Category: "behavior",
	}
	if err := db.DB.Create(&tag).Error; err != nil {
		t.Fatalf("插入测试标签失败: %v", err)
	}
	for _, k := range []string{"pack_prompts_" + code, "pack_params_" + code, "pack_mindset_" + code} {
		cfg := model.SystemConfig{
			TenantID: tid, Category: "industry_pack", Key: k,
			Value: `{"persona":"` + code + `"}`, ValueType: "json",
		}
		if err := db.DB.Create(&cfg).Error; err != nil {
			t.Fatalf("插入测试覆盖键 %s 失败: %v", k, err)
		}
	}
}

// rowsPerSeededPack seedPackRows 一个包会留下的行数：模板 1 + 卖点 1 + 标签 1 + 覆盖键 3。
// 覆盖键是三个（pack_prompts_/pack_params_/pack_mindset_），别按"一路一键=4"去断言，
// 那条等式会把每次改动都拖进一次算术自查。
const rowsPerSeededPack = 6

// countPackRows 数一下某个包在该租户租户级还留下多少行（模板/卖点/标签/覆盖键四路）
func countPackRows(t *testing.T, tid uint, code string) (tpl, feat, tag, cfg int64) {
	t.Helper()
	prefix := IDPrefix(code, tid)
	must := func(model any, query string, args ...any) int64 {
		var n int64
		if err := db.DB.Model(model).Where(query, args...).Count(&n).Error; err != nil {
			t.Fatalf("统计失败: %v", err)
		}
		return n
	}
	tpl = must(&model.Template{}, "tenant_id = ? AND id LIKE ? AND department_id IS NULL", tid, prefix+"%")
	feat = must(&model.Feature{}, "tenant_id = ? AND id LIKE ? AND department_id IS NULL", tid, prefix+"%")
	tag = must(&model.Tag{}, "tenant_id = ? AND code LIKE ?", tid, prefix+"%")
	cfg = must(&model.SystemConfig{}, "tenant_id = ? AND \"key\" IN ?", tid,
		[]string{"pack_prompts_" + code, "pack_params_" + code, "pack_mindset_" + code})
	return
}

// TestPurgeOtherPacksOnPackSwitch 换包（auto → general）：旧包四路产物全清、新包一行不少
func TestPurgeOtherPacksOnPackSwitch(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenantCode(t, "packsw_a")
	defer testutil.CleanupTenant(t, tid)

	seedPackRows(t, tid, "auto", nil)
	seedPackRows(t, tid, "general", nil)
	// 前提自检：先证明"两个包的内容都真的在库里"，否则下面的等式会在 0==0 上假绿
	if a1, _, _, _ := countPackRows(t, tid, "auto"); a1 != 1 {
		t.Fatalf("前提不成立：auto 模板行数 = %d，期望 1", a1)
	}

	n, err := PurgeOtherPacks(tid, []string{"general"})
	if err != nil {
		t.Fatalf("PurgeOtherPacks 失败: %v", err)
	}
	if n != 1 {
		t.Errorf("应只清掉 1 个旧包，实际 %d", n)
	}
	if a1, a2, a3, a4 := countPackRows(t, tid, "auto"); a1+a2+a3+a4 != 0 {
		t.Errorf("旧包 auto 未被清干净：模板%d 卖点%d 标签%d 覆盖键%d", a1, a2, a3, a4)
	}
	if g1, g2, g3, g4 := countPackRows(t, tid, "general"); g1+g2+g3+g4 != rowsPerSeededPack {
		t.Errorf("新包 general 被误伤：模板%d 卖点%d 标签%d 覆盖键%d（期望各 1）", g1, g2, g3, g4)
	}
}

// TestPurgeOtherPacksKeepsListedCodes 反向用例：keep 覆盖全部包时必须什么都不删。
// 缺这条，实现退化成"清空租户全部包内容"也照样能过上一条测试——
// 而真接口上那等于 reapply 把自己刚物化进去的内容删掉。
func TestPurgeOtherPacksKeepsListedCodes(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenantCode(t, "packsw_b")
	defer testutil.CleanupTenant(t, tid)

	seedPackRows(t, tid, "auto", nil)
	seedPackRows(t, tid, "auto_rox", nil) // 企业包与行业包共存，二者都在 keep 里

	n, err := PurgeOtherPacks(tid, []string{"auto", "auto_rox"})
	if err != nil {
		t.Fatalf("PurgeOtherPacks 失败: %v", err)
	}
	if n != 0 {
		t.Errorf("keep 已覆盖全部包，不应清任何包，实际清了 %d 个", n)
	}
	for _, code := range []string{"auto", "auto_rox"} {
		if x1, x2, x3, x4 := countPackRows(t, tid, code); x1+x2+x3+x4 != rowsPerSeededPack {
			t.Errorf("%s 内容被误删：模板%d 卖点%d 标签%d 覆盖键%d", code, x1, x2, x3, x4)
		}
	}
}

// TestPurgeOtherPacksSparesDeptAndOtherTenants 边界：部门包行与他租户同名包都不许动。
// 部门包是另一条继承链（一部门一包），租户换包动作无权支配它；跨租户误删则是隔离事故。
// 部门包刻意用另一个 code（auto_rox_sales）而不是"同 code 的部门级行"：templates.id 是全局主键，
// 同 code 同租户的两级物化在生产里也不可能共存（IDPrefix 只含 code+租户），
// 造那种数据只会撞主键，测不到我想测的那件事。
func TestPurgeOtherPacksSparesDeptAndOtherTenants(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenantCode(t, "packsw_c")
	dept := uint(91001)
	other := testutil.CreateTenantCode(t, "packsw_d")
	defer testutil.CleanupTenant(t, tid)
	defer testutil.CleanupTenant(t, other)

	seedPackRows(t, tid, "auto", nil)
	seedPackRows(t, tid, "auto_rox_sales", &dept) // 部门包产物（行带 department_id）
	seedPackRows(t, other, "auto", nil)           // 别家租户的同名包内容

	if _, err := PurgeOtherPacks(tid, []string{"general"}); err != nil {
		t.Fatalf("PurgeOtherPacks 失败: %v", err)
	}
	// 租户级 auto 已清（countPackRows 只数 department_id IS NULL）
	if a1, a2, _, _ := countPackRows(t, tid, "auto"); a1+a2 != 0 {
		t.Errorf("本租户租户级 auto 未清干净：模板%d 卖点%d", a1, a2)
	}
	// 部门级行必须还在：模板/卖点按 department_id 过滤，扫描面就不该包含它们
	var deptTpl, deptFeat int64
	if err := db.DB.Model(&model.Template{}).
		Where("tenant_id = ? AND id LIKE ? AND department_id = ?", tid, IDPrefix("auto_rox_sales", tid)+"%", dept).
		Count(&deptTpl).Error; err != nil {
		t.Fatalf("统计部门级行失败: %v", err)
	}
	if err := db.DB.Model(&model.Feature{}).
		Where("tenant_id = ? AND id LIKE ? AND department_id = ?", tid, IDPrefix("auto_rox_sales", tid)+"%", dept).
		Count(&deptFeat).Error; err != nil {
		t.Fatalf("统计部门级卖点失败: %v", err)
	}
	if deptTpl != 1 || deptFeat != 1 {
		t.Errorf("部门包产物被租户换包动作波及：模板%d 卖点%d（期望各 1）", deptTpl, deptFeat)
	}
	// 他租户的 auto 内容一行不许少
	if o1, o2, o3, o4 := countPackRows(t, other, "auto"); o1+o2+o3+o4 != rowsPerSeededPack {
		t.Errorf("跨租户误删：other 剩 模板%d 卖点%d 标签%d 覆盖键%d", o1, o2, o3, o4)
	}
}

// TestPurgeOtherPacksRejectsSystemLayer 系统层（tenant_id=0）禁止清除：
// 种子内容就住在 tenant_id=0，一旦放行，一次误调用能把全平台预置话术清空。
func TestPurgeOtherPacksRejectsSystemLayer(t *testing.T) {
	if _, err := PurgeOtherPacks(0, []string{"auto"}); err == nil {
		t.Fatal("tenantID=0 必须报错，实际放行")
	}
}

// TestPurgeOtherPacksCoversConfigOnlyGhost 只写过覆盖键、一条模板都没出的空壳包也要被清。
// 这类包在库里没有任何 pk_ 前缀行，只从行 ID 反解就会漏掉它——
// 而它的 pack_prompts_/pack_params_/pack_mindset_ 键会继续遮蔽系统层，等于"删不掉的空壳"。
func TestPurgeOtherPacksCoversConfigOnlyGhost(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenantCode(t, "packsw_e")
	defer testutil.CleanupTenant(t, tid)

	for _, k := range []string{"pack_prompts_ghost", "pack_params_ghost", "pack_mindset_ghost"} {
		if err := db.DB.Create(&model.SystemConfig{
			TenantID: tid, Category: "industry_pack", Key: k, Value: `{"persona":"ghost"}`, ValueType: "json",
		}).Error; err != nil {
			t.Fatalf("插入空壳覆盖键失败: %v", err)
		}
	}
	n, err := PurgeOtherPacks(tid, []string{"general"})
	if err != nil {
		t.Fatalf("PurgeOtherPacks 失败: %v", err)
	}
	if n != 1 {
		t.Errorf("空壳包应被识别并清除，实际清了 %d 个", n)
	}
	var left int64
	if err := db.DB.Model(&model.SystemConfig{}).
		Where("tenant_id = ? AND \"key\" LIKE ?", tid, "pack_%ghost").Count(&left).Error; err != nil {
		t.Fatalf("统计覆盖键失败: %v", err)
	}
	if left != 0 {
		t.Errorf("ghost 的覆盖键还剩 %d 条，期望 0（空壳键会永久遮蔽系统层）", left)
	}
}
