// 行业包"同编码唯一上架版本"回归单测（2026-09-25 残项收口批·残项5）。
//
// 缺陷现场：industry_packs.code 只有普通索引、从来没有唯一约束，而三处写入点都不做
// "同 code 兄弟让位"，其中启动期 AutoRegisterLocalPacks 对每个 .aipack 文件无条件写
// status=active 是主因。本机实查 21 条 active 行里 8 个编码各自多版本并存
// （auto 1.0.0/1.1.0/1.2.0、auto_rox 三版、b2b/ecom/edu/... 各两版）。
// 后果不是观感问题：租户侧"汽车"在下拉里出现三行同名可选、继承链只能靠 id 倒序猜新版、
// 包质量归因按 (pack_code, version) 分样本被两版各吃一半。
//
// 修法是两侧同时到位：迁移 027 的部分唯一索引（DB 兜底）+ ActivatePackExclusive（写侧单点）。
// 因此本测也分两侧，缺一侧都不算修好：
//
//	① 纯函数侧：版本号比较必须是**数值序**——字符串比较会把 1.10.0 判成小于 1.9.0，
//	   于是"保留最高版本"会把真新版本下架掉，而且错得很安静；
//	② DB 侧：既断"第二行 active 必须建不上"（约束在），也断"两行 disabled 必须建得上"
//	   （约束是部分的，历史版本还得留着随时回滚上架）。只断前者会得到"整表 code 唯一"
//	   这种过强实现照样全绿——那是把版本历史功能一起删了的写法。
package industrypack

import (
	"errors"
	"strings"
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// TestCompareVersionsOrdersNumerically 版本号比较的取值域，含"字符串比较会判反"的那一组。
func TestCompareVersionsOrdersNumerically(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		why  string
	}{
		{"1.10.0", "1.9.0", 1, "两位数段：字典序会判反，这一组是数值序的唯一证据"},
		{"1.2.0", "1.2", 1, "缺段按 0 补齐：1.2.0 新于 1.2"},
		{"1.2.3", "1.2.3", 0, "同版本必须判等，否则择新退化成按 id 猜"},
		{"2.0.0", "10.0.0", -1, "首位两位数同样按数值"},
		{"", "1.0.0", -1, "空版本（脏数据）沉底，不许抢到上架位"},
		{"v1beta", "1.0.0", -1, "非数字版本沉底，且比较本身不得 panic"},
		{"1.0.0", "1.0.0-beta", 1, "带后缀不可解析 → 沉底；可解析的一律优先"},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q,%q)=%d 期望 %d（%s）", c.a, c.b, got, c.want, c.why)
		}
		// 反对称性：交换参数必须给出相反的结论。只写对一半分支的实现过不了这一行。
		if rev := CompareVersions(c.b, c.a); rev != -c.want {
			t.Errorf("CompareVersions(%q,%q)=%d 与正向不自洽（期望 %d）", c.b, c.a, rev, -c.want)
		}
	}
}

// TestNewerPackIDPicksHighestVersion 择新：版本优先、同版本比 id、脏版本全灭时退化成 id。
func TestNewerPackIDPicksHighestVersion(t *testing.T) {
	row := func(id uint, ver string) model.IndustryPack {
		return model.IndustryPack{ID: id, Code: "picktest", Version: ver}
	}
	// 顺序刻意打乱：择新不能依赖入参顺序（启动扫描是文件名字典序）
	group := []model.IndustryPack{row(11, "1.9.0"), row(12, "1.10.0"), row(10, "1.2.0")}
	if got := NewerPackID(group); got != 12 {
		t.Errorf("NewerPackID=%d 期望 12（1.10.0 才是最高版本，按字符串会选成 1.9.0 那行）", got)
	}
	// 同版本号（例如重复注册）→ id 大者优先，结果唯一确定
	same := []model.IndustryPack{row(21, "1.1.0"), row(22, "1.1.0")}
	if got := NewerPackID(same); got != 22 {
		t.Errorf("同版本时 NewerPackID=%d 期望 22（后注册优先，且必须唯一）", got)
	}
	// 全是脏版本号 → 仍要给出确定答案（id 最大），不能返回 0 让调用方以为"没包可上架"
	dirty := []model.IndustryPack{row(31, ""), row(32, "nightly"), row(33, "beta")}
	if got := NewerPackID(dirty); got == 0 {
		t.Error("全脏版本号时 NewerPackID 返回 0：调用方会当成无包可上架，行业包整体失踪")
	}
	// 反证：空入参必须返回 0（不是"随便挑一个"），否则上层"有没有包"的判定失去依据
	if got := NewerPackID(nil); got != 0 {
		t.Errorf("空入参 NewerPackID=%d 期望 0", got)
	}
}

// mkPack 建一条包目录行（平台表，无租户归属），并登记清理。
// code 必须每个用例唯一：迁移 027 之后同 code 只允许一条 active，
// 用固定 code 的用例集会在第二例上撞 23505，那是测试自己踩约束，不是被测行为。
func mkPack(t *testing.T, code, version, status string) *model.IndustryPack {
	t.Helper()
	p := &model.IndustryPack{
		Code: code, Name: "单测包" + code + " " + version, Industry: "auto",
		Version: version, PackLevel: LevelIndustry, Status: status,
	}
	if err := db.DB.Create(p).Error; err != nil {
		t.Fatalf("建包行 %s %s(%s) 失败: %v", code, version, status, err)
	}
	t.Cleanup(func() { db.DB.Where("code = ?", code).Delete(&model.IndustryPack{}) })
	return p
}

// countActive 数某 code 当前 active 行数（本文件所有等式都建立在它上面）
func countActive(t *testing.T, code string) int {
	t.Helper()
	var n int64
	if err := db.DB.Model(&model.IndustryPack{}).
		Where("code = ? AND status = ?", code, StatusActive).Count(&n).Error; err != nil {
		t.Fatalf("统计 active 失败: %v", err)
	}
	return int(n)
}

// packStatus 读一条包行的当前状态
func packStatus(t *testing.T, id uint) string {
	t.Helper()
	var p model.IndustryPack
	if err := db.DB.Where("id = ?", id).First(&p).Error; err != nil {
		t.Fatalf("读包行%d 失败: %v", id, err)
	}
	return p.Status
}

// TestActivatePackExclusiveDemotesSiblings 上架新版必须把同 code 旧版挤下去，且只留一条 active。
func TestActivatePackExclusiveDemotesSiblings(t *testing.T) {
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	code := "utpkexc" + time.Now().Format("150405")

	old := mkPack(t, code, "1.1.0", StatusActive)
	// 前提自检：不先证明"旧版确实在架上"，下面的等式会在 0==0 上假绿
	if got := countActive(t, code); got != 1 {
		t.Fatalf("前提不成立：上架前 active=%d，期望 1", got)
	}
	nw := mkPack(t, code, "1.2.0", StatusDisabled)

	demoted, err := ActivatePackExclusive(db.DB, nw.ID)
	if err != nil {
		t.Fatalf("ActivatePackExclusive 失败: %v", err)
	}
	// ① 全 code 只剩一条 active（等值锁，不是"变少了"）
	if got := countActive(t, code); got != 1 {
		t.Errorf("上架后 active=%d 期望恰 1", got)
	}
	// ② 那条就是新版；旧版被挤下去（各自钉死，防止"两条都 disabled"这种两头空的实现）
	if got := packStatus(t, nw.ID); got != StatusActive {
		t.Errorf("新版状态=%q 期望 %s", got, StatusActive)
	}
	if got := packStatus(t, old.ID); got != StatusDisabled {
		t.Errorf("旧版状态=%q 期望 %s（同 code 只允许一个上架版本）", got, StatusDisabled)
	}
	// ③ 返回值必须如实报告"谁被下架了"：这是给超管回执与审计用的，漏报等于静默改数据
	if len(demoted) != 1 || demoted[0].ID != old.ID {
		t.Errorf("demoted 返回 %+v，期望恰好一条且 id=%d", demoted, old.ID)
	}
}

// TestActivatePackExclusiveIsIdempotent 重复上架同一版不该有副作用（启动期/重试都会走到）。
func TestActivatePackExclusiveIsIdempotent(t *testing.T) {
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	code := "utpkidm" + time.Now().Format("150405")
	only := mkPack(t, code, "1.0.0", StatusActive)
	mkPack(t, code, "0.9.0", StatusDisabled) // 历史版本留在库里，不该被反复改动

	for i := 0; i < 3; i++ {
		demoted, err := ActivatePackExclusive(db.DB, only.ID)
		if err != nil {
			t.Fatalf("第%d 次重复上架失败: %v", i+1, err)
		}
		if len(demoted) != 0 {
			t.Errorf("第%d 次重复上架报出 demoted=%d 行，期望 0（本来就没有兄弟在架）", i+1, len(demoted))
		}
		if got := countActive(t, code); got != 1 {
			t.Fatalf("第%d 次重复上架后 active=%d 期望 1", i+1, got)
		}
		if s := packStatus(t, only.ID); s != StatusActive {
			t.Fatalf("第%d 次重复上架把在架行弄丢了：状态=%q", i+1, s)
		}
	}
}

// TestActivatePackNotFound 目标行不存在必须回 ErrPackNotFound（调用方据此回 404，不是 500）。
func TestActivatePackNotFound(t *testing.T) {
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	if _, err := ActivatePackExclusive(db.DB, 999999999); !errors.Is(err, ErrPackNotFound) {
		t.Errorf("上架不存在的包行 err=%v 期望 ErrPackNotFound（报成 500 会让超管以为是服务故障）", err)
	}
}

// TestDBIndexAllowsDisabledSiblings 约束必须是**部分的**：同 code 允许多条历史版本共存，
// 只禁"同时上架"。只断"第二条 active 建不上"会得到"code 全表唯一"的过强实现照样全绿，
// 而那种实现等于把版本历史与回滚能力一起删掉。
func TestDBIndexAllowsDisabledSiblings(t *testing.T) {
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	code := "utpkpart" + time.Now().Format("150405")
	mkPack(t, code, "1.0.0", StatusDisabled)
	// 第二条 disabled 同 code：必须建得上（反向用例）
	p2 := &model.IndustryPack{Code: code, Name: "同编码第二条下架版本", Version: "1.1.0", PackLevel: LevelIndustry, Status: StatusDisabled}
	if err := db.DB.Create(p2).Error; err != nil {
		t.Fatalf("同 code 第二条 disabled 行被拒: %v —— 部分唯一索引写成了全表唯一，版本历史会被删掉", err)
	}
	// 前提自检：先数"两条 disabled 并存"真的在库里，再去断它们没被索引挡住——
	// 首版把删除写在了计数前面，于是这条永远在 1 上判前提不成立（测试自伤，非产品缺陷）。
	var m int64
	if err := db.DB.Model(&model.IndustryPack{}).
		Where("code = ? AND status = ?", code, StatusDisabled).Count(&m).Error; err != nil {
		t.Fatalf("统计 disabled 失败: %v", err)
	}
	if m < 2 {
		t.Fatalf("前提不成立：disabled 同编码行数=%d（期望 ≥2），反向用例失去意义", m)
	}
	// 反向再反一层：这 2 条历史版本之上仍然可以上架**一条**，
	// 只有第二条 active 才被挡——这样才证明索引是"部分"的，而不是"同 code 不许有 active"。
	first := &model.IndustryPack{Code: code, Name: "在架版本", Version: "1.2.0", PackLevel: LevelIndustry, Status: StatusActive}
	if err := db.DB.Create(first).Error; err != nil {
		t.Fatalf("同 code 第一条 active 被拒: %v —— 部分索引写歪了，历史行会把上架一起封死", err)
	}
	dup := &model.IndustryPack{Code: code, Name: "第二条 active（必须建不上）", Version: "1.3.0", PackLevel: LevelIndustry, Status: StatusActive}
	if err := db.DB.Create(dup).Error; err == nil {
		db.DB.Where("id = ?", dup.ID).Delete(&model.IndustryPack{})
		t.Fatal("两条 disabled 共存成立，却仍能让第二条 active 落库：索引的 WHERE 条件不生效")
	}
	db.DB.Where("id = ?", dup.ID).Delete(&model.IndustryPack{})
}

// TestDBIndexRejectsSecondActiveRow 迁移 027 的部分唯一索引必须真的在库里生效。
// 这条是"绕过应用直插"的唯一防线：应用侧 ActivatePackExclusive 只在 HTTP/启动路径上执行，
// 人工 SQL、旧版本二进制、以及任何没走这两个口的写入都只能靠索引挡住。
func TestDBIndexRejectsSecondActiveRow(t *testing.T) {
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	code := "utpkuniq" + time.Now().Format("150405")
	mkPack(t, code, "1.0.0", StatusActive)
	dup := &model.IndustryPack{Code: code, Name: "第二条 active（必须建不上）", Version: "1.1.0", PackLevel: LevelIndustry, Status: StatusActive}
	err := db.DB.Create(dup).Error
	if err == nil {
		db.DB.Where("id = ?", dup.ID).Delete(&model.IndustryPack{})
		t.Fatal("同 code 第二条 active 直插成功：迁移 027 的 ux_pack_one_active_per_code 不在场，" +
			"残项5 只在应用层修了半边")
	}
	msg := err.Error()
	// 钉住"是这条约束报的错"，而不是任何别的失败（连接断了也算 err，那样护栏会空转）
	if !strings.Contains(msg, "ux_pack_one_active_per_code") && !strings.Contains(msg, "23505") {
		t.Fatalf("直插第二条 active 被拒的原因不是唯一索引: %v", err)
	}
}
