// Package seed 演示数据与行业包声明的一致性测试（G-24，2026-09-25）。
//
// 这里测的是"声明"而不是"落包"：seed 集合说自己是 auto/auto_rox 那一套货，而货真价实的
// 包目录由 tools/build_packs.sh 的参数表定义（code/层级/父包/行业）。两边各写一遍、又互不
// 引用，是这类"数据 ↔ 配置"漂移最常见的成因——启动期 BindSeedDemoTenantPack 按 code 查包，
// 查不到就只打一行 log 然后什么都不做，于是全新库上的演示租户静默停在无包状态，
// 表现为"极石的车型配通用话术"，而日志里那句 WARN 没人翻。
//
// 判据全部不连库：读常量、读参数表、读源目录，纯静态。
package seed

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// packTableRow 一行发布清单（tools/build_packs.sh 的参数表）
type packTableRow struct {
	src      string // 包源目录，如 packs-src/auto_rox
	code     string // 包 code
	level    string // industry / enterprise / department
	parent   string // 父包 code（行业包为空）
	industry string // 行业归属，如 auto
}

// loadPackTable 解析发布清单。
//
// 表在 shell 的 heredoc 里，格式一旦漂移（分隔符增减、字段换位），解析结果会变成"零行"，
// 而零行在这类一致性断言里看起来和"全都对上了"一样绿——所以先自证行数，解析不出即 FAIL。
func loadPackTable(t *testing.T) []packTableRow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "tools", "build_packs.sh"))
	if err != nil {
		t.Fatalf("读取打包参数表失败: %v", err)
	}
	var rows []packTableRow
	// 只认 7 段的表行：源目录|code|名称|版本|层级|父包|行业（父包可为空）
	lineRe := regexp.MustCompile(`^packs-src/[^\|]+\|[^\|]*\|[^\|]*\|[^\|]*\|[^\|]*\|[^\|]*\|[^\|]*$`)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !lineRe.MatchString(line) {
			continue
		}
		f := strings.Split(line, "|")
		rows = append(rows, packTableRow{src: f[0], code: f[1], level: f[4], parent: f[5], industry: f[6]})
	}
	dirs, err := os.ReadDir(filepath.Join("..", "packs-src"))
	if err != nil {
		t.Fatalf("读取 packs-src 目录失败: %v", err)
	}
	dirCount := 0
	for _, d := range dirs {
		if d.IsDir() { // .DS_Store 之类的系统杂物不是包源，别让它把计数带偏
			dirCount++
		}
	}
	if len(rows) < 8 {
		t.Fatalf("打包参数表解析出的行数为 %d（明显少于源目录数即格式漂移，一致性断言会在此假绿）", len(rows))
	}
	if len(rows) != dirCount {
		t.Fatalf("发布清单 %d 行与 packs-src 目录 %d 个不一致——有包没进清单（启动期注册不到）或有目录没被打包",
			len(rows), dirCount)
	}
	return rows
}

func packRowByCode(rows []packTableRow, code string) (packTableRow, bool) {
	for _, r := range rows {
		if r.code == code {
			return r, true
		}
	}
	return packTableRow{}, false
}

// TestDemoPackDeclarationMatchesBuildTable 声明的行业包/企业包必须真在发布清单里，
// 且层级与父子关系对得上——BindSeedDemoTenantPack 落的就是这一对。
func TestDemoPackDeclarationMatchesBuildTable(t *testing.T) {
	rows := loadPackTable(t)
	tenantCode, indCode, entCode := DemoPackBinding()

	// 前置自检：当前集合确实声明了包族（否则后面全是空转的"零命中即通过"）
	if tenantCode == "" || indCode == "" {
		t.Fatalf("种子集合未声明演示包族（tenant=%q industry=%q），G-24 的绑定链在源头就是断的", tenantCode, indCode)
	}

	if tenantCode != SeedDemoTenantCode {
		t.Fatalf("声明的演示租户 code %q 与 seedTenants 实际创建的 %q 不一致——绑定会按查不到的 code 静默落空",
			tenantCode, SeedDemoTenantCode)
	}

	ind, ok := packRowByCode(rows, indCode)
	if !ok {
		t.Fatalf("声明的行业基包 %q 不在发布清单里（packs-src 无从打包，启动期必然查不到 active 包行）", indCode)
	}
	if ind.level != "industry" {
		t.Fatalf("行业基包 %q 的层级必须是 industry，实际 %q——applyPackPairToTenant 只认 industry 层", indCode, ind.level)
	}
	if _, err := os.Stat(filepath.Join("..", ind.src)); err != nil {
		t.Fatalf("行业基包 %q 的源目录不存在: %v", indCode, err)
	}

	if entCode == "" {
		return // 只落基包的集合：没有企业层可核
	}
	ent, ok := packRowByCode(rows, entCode)
	if !ok {
		t.Fatalf("声明的企业包 %q 不在发布清单里", entCode)
	}
	if ent.level != "enterprise" {
		t.Fatalf("企业包 %q 的层级必须是 enterprise，实际 %q——applyPackPairToTenant 只认 enterprise 层", entCode, ent.level)
	}
	if ent.parent != indCode {
		t.Fatalf("企业包 %q 的父包是 %q，与种子声明的行业基包 %q 不同族——两级包内容对不上，落包会互相覆盖",
			entCode, ent.parent, indCode)
	}
	if _, err := os.Stat(filepath.Join("..", ent.src)); err != nil {
		t.Fatalf("企业包 %q 的源目录不存在: %v", entCode, err)
	}
	if ent.industry != ind.industry {
		t.Fatalf("企业包 %q 与基包 %q 的行业归属不同（%s vs %s）——按行业分流话术谓词会判成两个族",
			entCode, indCode, ent.industry, ind.industry)
	}
}

// TestDemoPackBindingEmptyWhenNoSelection 集合被置空（换行业施工中）即整条绑定链不参与：
// 回三个空串，调用方据此什么都不做，而不是拿上一个行业的包名继续落。
func TestDemoPackBindingEmptyWhenNoSelection(t *testing.T) {
	old := SeedIndustry
	t.Cleanup(func() { SeedIndustry = old })

	SeedIndustry = nil
	code, ind, ent := DemoPackBinding()
	if code != "" || ind != "" || ent != "" {
		t.Fatalf("集合为空时必须回三个空串，实际 %q/%q/%q", code, ind, ent)
	}

	// 只声明基包不声明企业包，是"换行业但不做品牌层"的合法形态——企业码原样传空，不得凭空补
	SeedIndustry = &Industry{DemoTenantCode: "some_demo", IndustryPackCode: "general"}
	code, ind, ent = DemoPackBinding()
	if code != "some_demo" || ind != "general" || ent != "" {
		t.Fatalf("未声明企业包时 entCode 必须为空，实际 %q/%q/%q", code, ind, ent)
	}
}
