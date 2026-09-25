// G-24(2026-09-25) 演示数据绑定守卫单测：BindSeedDemoTenantPack 只在
// "种子声明的那个演示租户 + 它还没有任何绑定" 这一格动手。
//
// 闸的判据用桩测（demoPackApplier）：真落包要读 data/packs 里的 .aipack 与签名密钥，CI 上
// 未必在场（prod-compose-smoke 就是空 packs 目录）——把环境依赖塞进守卫用例只会得到一条
// "看机器脸色"的红。落包内部另有三条覆盖：档位门禁见 G-22c 用例与 smoke §四十，
// "包在场时真物化"见本文件最后一条用例（data/packs 或密钥不在场即 SKIP，不是 FAIL）。
//
// 三条"绝不动手"的用例各守一种真实事故：
//   - 已绑过的演示租户被每轮重启改写 → 运维在后台手动换包白做（用户明确的选择被启动期覆盖）；
//   - 按 code 查不到却退化成"随便挑一个租户" → 启动期给陌生真租户塞内容，等于绕过付费墙；
//   - 行业集合没声明包族却照落 auto → 换行业的演示库仍被刷回汽车包。
package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-scrm/internal/db"
	"ai-scrm/internal/industrypack"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// callRec 记录 demoPackApplier 被调用时的入参（一次调用一条）
type callRec struct {
	tenantID uint
	indCode  string
	entCode  string
}

// stubDemoApplier 换掉落包实现并在用例结束时还原；返回调用记录切片（用例内按需断言长度）
func stubDemoApplier(t *testing.T) *[]callRec {
	t.Helper()
	var calls []callRec
	old := demoPackApplier
	demoPackApplier = func(tt model.Tenant, indCode, entCode string) bool {
		calls = append(calls, callRec{tenantID: tt.ID, indCode: indCode, entCode: entCode})
		return true
	}
	t.Cleanup(func() { demoPackApplier = old })
	return &calls
}

// mustTenantCode 按 ID 取回租户 code（testutil 建的租户 code 带进程后缀，不能硬拼）
func mustTenantCode(t *testing.T, tid uint) string {
	t.Helper()
	var tt model.Tenant
	if err := db.DB.First(&tt, tid).Error; err != nil {
		t.Fatalf("读取租户 %d 失败: %v", tid, err)
	}
	return tt.Code
}

// mustBind 给租户写一条行业包绑定行（只建 binding，不物化——本测不动真包）。
// 注意这样建出来的就是"空壳绑定"，需要真内容时再配 mustPackContent。
func mustBind(t *testing.T, tid uint, code string) {
	t.Helper()
	row := model.TenantPackBinding{TenantID: tid, PackCode: code, AppliedVersion: "1.0.0"}
	if err := db.DB.Create(&row).Error; err != nil {
		t.Fatalf("建绑定行失败: %v", err)
	}
	t.Cleanup(func() { db.DB.Where("tenant_id = ?", tid).Delete(&model.TenantPackBinding{}) })
}

// mustPackContent 在该租户私有层补一条带包前缀的模板行，让"这个包真落过内容"成立。
// 空壳判据读的就是这类行，所以不补它，任何 binding 看起来都像是 seed 旧写法留下的假记录。
func mustPackContent(t *testing.T, tid uint, code string) {
	t.Helper()
	row := model.Template{
		ID:             industrypack.IDPrefix(code, tid) + "smoke1", // 包前缀＝包归属，判据按它匹配
		TenantID:       tid,                                         // 事务外直插也必须显式盖章（C7）
		AnchorType:     1,
		Name:           "空壳判据用模板",
		PromptTemplate: "非空话术一条",
		Status:         1,
	}
	if err := db.DB.Create(&row).Error; err != nil {
		t.Fatalf("建包内容行失败: %v", err)
	}
	t.Cleanup(func() { db.DB.Where("tenant_id = ?", tid).Delete(&model.Template{}) })
}

// TestBindSeedDemoTenantPackFires 正向：未绑定的演示租户拿到种子声明的行业包+企业包。
func TestBindSeedDemoTenantPackFires(t *testing.T) {
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	tid := testutil.CreateTenantCode(t, "g24_demo")
	code := mustTenantCode(t, tid)
	calls := stubDemoApplier(t)

	BindSeedDemoTenantPack(code, "auto", "auto_rox")

	if len(*calls) != 1 {
		t.Fatalf("演示租户未绑定时应落包一次，实际 %d 次", len(*calls))
	}
	got := (*calls)[0]
	if got.tenantID != tid || got.indCode != "auto" || got.entCode != "auto_rox" {
		t.Fatalf("落包入参不符: %+v，期望租户 %d + auto/auto_rox", got, tid)
	}
}

// TestBindSeedDemoTenantPackKeepsManualChoice 已绑过且有内容即跳过：人工换包优先于启动期。
//
// 内容行是这个用例的一部分，不是陪衬：没有它，"跳过"到底是"尊重人工选择"还是"空壳判据没生效"
// 就分不开——那正是本机演示租户卡了很久的状态。
func TestBindSeedDemoTenantPackKeepsManualChoice(t *testing.T) {
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	tid := testutil.CreateTenantCode(t, "g24_bound")
	code := mustTenantCode(t, tid)
	mustBind(t, tid, "edu")        // 模拟运维已手动换成教育包
	mustPackContent(t, tid, "edu") // 且该包内容确实物化过
	calls := stubDemoApplier(t)

	BindSeedDemoTenantPack(code, "auto", "auto_rox")

	if len(*calls) != 0 {
		t.Fatalf("已有绑定的租户不得被启动期改写，实际调用 %+v", *calls)
	}
}

// TestBindSeedDemoTenantPackSkipsUnknownCode 只认声明的那个 code：查不到就什么都不做，
// 绝不退化成"随便挑一个未绑定租户"（那会把内容白送给付费墙外的真租户）。
//
// 库里同时留一个真实存在且未绑定的租户作对照：它不在声明范围内，整轮调用后绑定数必须仍为 0。
// 「先证明有个未绑定租户在场，再证明它没被动」——缺前一步的话，"零次调用"会在空库上假绿。
func TestBindSeedDemoTenantPackSkipsUnknownCode(t *testing.T) {
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	other := testutil.CreateTenantCode(t, "g24_real")
	calls := stubDemoApplier(t)

	BindSeedDemoTenantPack("no_such_demo_tenant_code", "auto", "auto_rox")

	if len(*calls) != 0 {
		t.Fatalf("演示租户不存在时不得动手，实际 %+v", *calls)
	}
	var cnt int64
	db.DB.Model(&model.TenantPackBinding{}).Where("tenant_id = ?", other).Count(&cnt)
	if cnt != 0 {
		t.Fatalf("未声明的真租户不得被启动期落包，实际绑定行数 %d", cnt)
	}
}

// TestBindSeedDemoTenantPackNeedsCodes 行业集合没声明包族（空 code）即完全不参与绑定。
func TestBindSeedDemoTenantPackNeedsCodes(t *testing.T) {
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	tid := testutil.CreateTenantCode(t, "g24_nocode")
	code := mustTenantCode(t, tid)
	calls := stubDemoApplier(t)

	BindSeedDemoTenantPack("", "auto", "auto_rox") // 没有演示租户
	BindSeedDemoTenantPack(code, "", "auto_rox")   // 没声明行业基包
	BindSeedDemoTenantPack(code, "auto", "")       // 只落行业基包：允许，且企业码原样传空
	if len(*calls) != 1 {
		t.Fatalf("只应命中第三条，实际 %+v", *calls)
	}
	if got := (*calls)[0]; got.entCode != "" || got.indCode != "auto" {
		t.Fatalf("企业包为空必须原样传下去（不得凭空补一个包），实际 %+v", got)
	}
}

// TestBindSeedDemoTenantPackMaterializesRealPacks 走"包真在场"的那一支：注册本地包 → 给一个
// 未绑定租户落 auto + auto_rox → 断绑定行两级都写上、且两个包的内容都真落进了租户私有层。
//
// 上面四条用例把落包实现换成了桩，证明的是"该不该动手"；本条用真实现，证明"动手之后得到的是
// 有内容的绑定，而不是一行空 binding"——后者才是租户永久卡死的那种失败（sweep 按"已绑过"跳过它）。
// data/packs 与签名密钥任一不在场即 SKIP：那是环境状态，不是产品缺陷（prod-compose-smoke 就是空目录）。
func TestBindSeedDemoTenantPackMaterializesRealPacks(t *testing.T) {
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	// packStoreDir 与默认密钥路径都是相对仓库根的常量，测试进程 cwd 在本包目录，故临时切根。
	// 同包其余用例按裸文件名读源码（chat_main.go 等），internal/api 无 t.Parallel 用例，
	// 切回去跑是顺序安全的——但仍必须还原，否则后跑的按名读取会全部落空。
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录失败: %v", err)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("解析仓库根失败: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("切换到仓库根失败: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	if _, err := os.Stat(packStoreDir); err != nil {
		t.Skip(packStoreDir + " 目录不在场，跳过真包物化用例")
	}
	if _, err := packKeys(); err != nil {
		t.Skip("行业包签名密钥不在场，跳过真包物化用例")
	}

	// 前置自检：两个包必须都能以 active 状态查到，否则下面"零内容"到底是绑定没生效还是包没注册
	// 就分不清楚了。AutoRegisterLocalPacks 按 (code, version) 幂等，重复跑不产生新行。
	AutoRegisterLocalPacks()
	for _, code := range []string{"auto", "auto_rox"} {
		if _, _, err := openActivePackByCode(code); err != nil {
			t.Fatalf("包 %s 注册后仍查不到 active 行: %v（本用例前置不成立）", code, err)
		}
	}

	tid := testutil.CreateTenantCode(t, "g24_apply")
	code := mustTenantCode(t, tid)
	t.Cleanup(func() { cleanupPackAppliedRows(t, tid) })

	BindSeedDemoTenantPack(code, "auto", "auto_rox")

	var b model.TenantPackBinding
	if err := db.DB.Where("tenant_id = ?", tid).First(&b).Error; err != nil {
		t.Fatalf("演示租户落包后应有绑定行: %v", err)
	}
	if b.PackCode != "auto" || b.EnterpriseCode != "auto_rox" {
		t.Fatalf("绑定两级不符: 行业包 %q / 企业包 %q，期望 auto / auto_rox", b.PackCode, b.EnterpriseCode)
	}

	// 内容必须真落地：两级包各自前缀下都要有非空模板，否则就是"有绑定、无内容"的空壳行
	tpls := packTemplateIDs(t, tid)
	for _, pk := range []string{"auto", "auto_rox"} {
		prefix := industrypack.IDPrefix(pk, tid)
		n := 0
		for _, id := range tpls {
			if strings.HasPrefix(id, prefix) {
				n++
			}
		}
		if n == 0 {
			t.Fatalf("包 %s 前缀 %s 下一条模板都没物化（绑定行存在即成空壳，sweep 会永远跳过该租户）", pk, prefix)
		}
		t.Logf("包 %s 物化模板 %d 条", pk, n)
	}
}

// packTemplateIDs 取该租户私有层的全部模板 ID（字符串主键，前缀即包归属）
func packTemplateIDs(t *testing.T, tid uint) []string {
	t.Helper()
	var ids []string
	if err := db.DB.Model(&model.Template{}).Where("tenant_id = ?", tid).Pluck("id", &ids).Error; err != nil {
		t.Fatalf("读取租户 %d 模板失败: %v", tid, err)
	}
	return ids
}

// cleanupPackAppliedRows 清掉本用例为该测试租户物化的包内容与绑定行（租户行留给 cleanup_test_tenants）。
func cleanupPackAppliedRows(t *testing.T, tid uint) {
	t.Helper()
	for _, m := range []any{
		model.Template{}, model.Feature{}, model.Tag{}, model.FlowDefinition{}, model.SystemConfig{},
		model.TenantPackBinding{},
	} {
		if err := db.DB.Where("tenant_id = ?", tid).Delete(m).Error; err != nil {
			t.Logf("清理租户 %d 的 %T 失败（留给清理脚本）: %v", tid, m, err)
		}
	}
}

// TestBindSeedDemoTenantPackRepairsShellBinding 空壳绑定必须被删掉重落，而不是被"已绑过"永久锁死。
//
// 现场（不是假想）：本机演示租户 code=default 的绑定行是 seed 旧写法留下的"只写 binding 不物化"，
// pk_auto_t1_ 前缀下零模板——AutoApplyDefaultIndustryPack 见 cnt>0 即跳过，于是它永远拿不到任何包内容。
func TestBindSeedDemoTenantPackRepairsShellBinding(t *testing.T) {
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	tid := testutil.CreateTenantCode(t, "g24_shell")
	code := mustTenantCode(t, tid)
	mustBind(t, tid, "auto") // 只有一行 binding，没有内容＝空壳
	calls := stubDemoApplier(t)

	BindSeedDemoTenantPack(code, "auto", "auto_rox")

	if len(*calls) != 1 {
		t.Fatalf("空壳绑定应触发重落一次，实际 %+v", *calls)
	}
	if got := (*calls)[0]; got.tenantID != tid || got.entCode != "auto_rox" {
		t.Fatalf("重落入参不符: %+v", got)
	}
	// 假壳不写绑定行，所以此刻该租户必须一条 binding 都没有——壳确实被删了，
	// 而不是"又落一遍导致唯一索引撞车后静默放弃"（TenantID 上有唯一索引）。
	var cnt int64
	db.DB.Model(&model.TenantPackBinding{}).Where("tenant_id = ?", tid).Count(&cnt)
	if cnt != 0 {
		t.Fatalf("空壳绑定未被清除，残留 %d 行", cnt)
	}
}

// TestPackAppliedContentExists 空壳判据本身的三条腿：前缀模板算内容、配置存证也算内容、
// 别家的包前缀不算本包的内容。
func TestPackAppliedContentExists(t *testing.T) {
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	tid := testutil.CreateTenantCode(t, "g24_content")

	if packAppliedContentExists(tid, "auto") {
		t.Fatal("该租户私有层还没有任何行，判据必须回 false")
	}
	// 反证：别的包前缀下的行不能替本包作证（否则 auto_rox 的内容会把 auto 的空壳洗白）
	mustPackContent(t, tid, "auto_rox")
	if packAppliedContentExists(tid, "auto") {
		t.Fatal("auto 前缀下零内容，不得因 auto_rox 有内容就判成已落地")
	}
	if !packAppliedContentExists(tid, "auto_rox") {
		t.Fatal("auto_rox 前缀下有模板，必须判成已落地")
	}

	// 配置存证那一腿：只配了 prompts 没配话术的包同样算落过内容
	tid2 := testutil.CreateTenantCode(t, "g24_cfg")
	cfg := model.SystemConfig{TenantID: tid2, Category: "strategy", Key: "pack_prompts_auto", Value: `[{"k":"v"}]`, ValueType: "json"}
	if err := db.DB.Create(&cfg).Error; err != nil {
		t.Fatalf("建配置存证失败: %v", err)
	}
	t.Cleanup(func() { db.DB.Where("tenant_id = ?", tid2).Delete(&model.SystemConfig{}) })
	if !packAppliedContentExists(tid2, "auto") {
		t.Fatal("pack_prompts_auto 存证在，说明包确实物化过，不得判成空壳")
	}
}
