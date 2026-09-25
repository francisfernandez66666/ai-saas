// Package seed 车型域种子数据一致性与解析规则测试（G-22d，2026-09-25）。
//
// 这里全是纯函数用例，不连库：规格 361 条 / 竞品对比 58 组是否还能按 code 解析出来，
// 是"换行业后数据还能用"的唯一判据，而它不需要 DB 就能证伪——
// 把 data_car.go 的 code 集合与 CarModels 的 code 集合一比就知道。
package seed

import (
	"testing"

	"ai-scrm/internal/model"
)

// carModelCodes 当前种子行业的车型 code 集合
func carModelCodes() map[string]bool {
	set := make(map[string]bool, len(SeedIndustry.CarModels))
	for _, m := range SeedIndustry.CarModels {
		set[m.Code] = true
	}
	return set
}

// TestSeedIndustryDataCoherent 行业数据包自洽：规格与竞品对比引用的车型 code 必须在车型库里。
//
// 这条为什么值得钉死：外置之前，规格是"按 code 查不到就挂 ID=1"，所以 code 与车型库脱钩
// 不会有任何症状（数据照样入库，只是挂错车）。现在解析不出来会整批跳过——
// 那是**部署后**才看得见的现象（详情页空了），所以把判据前置到 CI。
// 反证用例见 TestSeedIndustryDataCoherentDetectsDrift。
func TestSeedIndustryDataCoherent(t *testing.T) {
	specs, compares := SeedIndustry.ModelSpecs, SeedIndustry.CompetitorCompares
	// 前提自检：数据没搬空（空集合会让下面的"全都在库里"在 0 条上假绿）
	if len(specs) < 300 {
		t.Fatalf("规格种子条数异常（%d），疑外置时丢数据", len(specs))
	}
	if len(compares) < 50 {
		t.Fatalf("竞品对比种子条数异常（%d），疑外置时丢数据", len(compares))
	}
	codes := carModelCodes()
	for i, s := range specs {
		if !codes[s.ModelCode] {
			t.Fatalf("规格 #%d（%s/%s）引用了车型库中不存在的 code %q", i, s.Category, s.ParamName, s.ModelCode)
		}
	}
	for i, c := range compares {
		if !codes[c.OurModelCode] {
			t.Fatalf("竞品对比 #%d（vs %s）引用了车型库中不存在的 code %q", i, c.CompetitorModel, c.OurModelCode)
		}
	}
}

// TestSeedIndustryDataCoherentDetectsDrift 反向自证：往集合里塞一条对不上的 code，
// 上面的判据必须能报出来——否则"全都在库里"可能只是因为比较根本没跑。
func TestSeedIndustryDataCoherentDetectsDrift(t *testing.T) {
	orig := SeedIndustry.ModelSpecs
	t.Cleanup(func() { SeedIndustry.ModelSpecs = orig })
	SeedIndustry.ModelSpecs = append(append([]ModelSpecSeed{}, orig...),
		ModelSpecSeed{ModelCode: "no_such_model_xyz", ParamName: "探测", ParamValue: "1", Category: "探测"})
	codes := carModelCodes()
	hit := false
	for _, s := range SeedIndustry.ModelSpecs {
		if !codes[s.ModelCode] {
			hit = true
		}
	}
	if !hit {
		t.Fatal("漂移未被检出：TestSeedIndustryDataCoherent 是空转护栏")
	}
}

// TestPlanModelSpecsSkipsUnknownCode 解析规则：已知 code 出对 ID，未知 code 只跳过。
// 旧实现的行为是"未知 → 固定返回 1"，本用例即封堵该回退（换行业时 ID=1 是别家车型）。
func TestPlanModelSpecsSkipsUnknownCode(t *testing.T) {
	ids := map[string]uint{"adamas": 7, "rox01": 8}
	seeds := []ModelSpecSeed{
		{ModelCode: "adamas", ParamName: "能源类型", ParamValue: "增程式", Category: "动力系统", Status: 1, Sort: 0},
		{ModelCode: "ghost", ParamName: "不存在的车型", ParamValue: "x", Category: "动力系统", Status: 1, Sort: 1},
		{ModelCode: "rox01", ParamName: "轴距", ParamValue: "2815", ParamUnit: "mm", Category: "车身尺寸", Status: 1, Sort: 0},
	}
	rows, skipped := planModelSpecs(seeds, ids)
	if len(rows) != 2 {
		t.Fatalf("应只写 2 行（未知 code 整条跳过），实际 %d 行", len(rows))
	}
	for _, r := range rows {
		if r.ModelID == 1 || r.ModelID == 0 {
			t.Fatalf("出现回退/空车型归属（ModelID=%d），旧缺陷复活：参数会挂到表里第一款车身上", r.ModelID)
		}
	}
	if rows[0].ModelID != 7 || rows[1].ModelID != 8 {
		t.Fatalf("code→ID 解析错位：%d,%d（期望 7,8）", rows[0].ModelID, rows[1].ModelID)
	}
	if got := skipped["ghost"]; got != 1 {
		t.Fatalf("未知 code 应计数 1 次以便汇总告警，实际 %d", got)
	}
	// 字段搬运不许悄悄丢：单位/价格标记/排序都得跟着走
	withUnit := model.ModelSpec{ModelID: 8, ParamName: "轴距", ParamValue: "2815", ParamUnit: "mm"}
	if rows[1].ParamUnit != withUnit.ParamUnit || rows[1].ParamName != withUnit.ParamName {
		t.Fatalf("字段丢失或错位： %+v", rows[1])
	}
}

// TestPlanCompetitorComparesSkipsUnknownCode 竞品对比同口径。
func TestPlanCompetitorComparesSkipsUnknownCode(t *testing.T) {
	ids := map[string]uint{"adamas": 7}
	seeds := []CompetitorCompareSeed{
		{OurModelCode: "adamas", CompetitorBrand: "坦克", CompetitorModel: "坦克300", CompareType: "优势", Content: "[]", Status: 1},
		{OurModelCode: "ghost", CompetitorBrand: "理想", CompetitorModel: "理想L7", CompareType: "优势", Content: "[]", Status: 1},
	}
	rows, skipped := planCompetitorCompares(seeds, ids)
	if len(rows) != 1 {
		t.Fatalf("未知 code 的对比应整组跳过，实际写了 %d 组", len(rows))
	}
	if rows[0].OurModelID != 7 || rows[0].CompetitorModel != "坦克300" {
		t.Fatalf("我方车型或竞品名解析错位： %+v", rows[0])
	}
	if skipped["ghost"] != 1 {
		t.Fatalf("未知 code 未计数（告警会漏报）：%v", skipped)
	}
}

// TestLogSkippedSeedsDoesNotPanicOnEmpty 汇总告警在无跳过时不得输出——
// 播种日志每次启动都打，多一行 WARN 会被运维当成真问题查半天。
func TestLogSkippedSeedsDoesNotPanicOnEmpty(t *testing.T) {
	logSkippedSeeds("规格参数", map[string]int{}, 10)
	logSkippedSeeds("规格参数", map[string]int{"b": 2, "a": 1}, 10) // 含乱序 key，内部需排序后打印
}
