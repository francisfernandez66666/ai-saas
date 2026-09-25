// Package seed 种子数据填充：本文件为车型域**逻辑**（品牌/车型/规格/竞品对比的写库流程）。
//
// G-22d（2026-09-25）：原先 2300 余行里的绝大部分是内联字面量（361 条规格 + 58 组竞品对比），
// 逻辑与数据混在一个文件里，且数据硬绑 auto_rox——换行业时只有品牌/车型会切，
// 规格与对比仍按旧车型 code 去查、查不到就回退到 ID=1（把参数静默挂到别家车型身上）。
// 现在数据搬到 seed/data_car.go（按 code 关联、随 SeedIndustry 切换），本文件只留解析与写库。
package seed

import (
	"sort"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"log"
)

// ============================================================
// 9. 种子品牌数据（3个品牌）
// ============================================================

// ============================================================
// 9. 种子品牌数据（极石+竞品5品牌）
// ============================================================

// seedBrands 写入 brands 表（幂等：已有品牌则跳过）。
// 作用：预置极石及竞品品牌（坦克/理想/方程豹/仰望），供车型表通过 brand_code 关联；
// 竞品品牌用于话术模板与竞品对比数据的对比口径。
// P1-4 去硬编码：品牌数据已外置 seed/data（随 SeedIndustry 切换，默认 auto_rox）。
func seedBrands() {
	var count int64
	db.DB.Model(&model.Brand{}).Count(&count)
	if count > 0 {
		log.Println("品牌数据已存在，跳过")
		return
	}

	for _, brand := range SeedIndustry.Brands {
		db.DB.Create(&brand)
	}

	log.Printf("已创建 %d 个品牌", len(SeedIndustry.Brands))
}

// ============================================================
// 10. 种子车型数据（5+款）
// ============================================================

// ============================================================
// 10. 种子车型数据（极石2款+竞品11款）
// ============================================================

// seedCarModels 写入 car_models 表（幂等：已有车型则整块跳过）。
// 作用：预置极石 2 款（ADAMAS/极石01）+ 竞品 11 款车型，含价格/级别/能源类型等；
// 通过 getBrandID 按 BrandCode 关联品牌，code 为后续规格参数与竞品对比的外键查找键。
// P1-4 去硬编码：车型数据已外置 seed/data（随 SeedIndustry 切换，默认 auto_rox），
// 数据层用 BrandCode 关联品牌，写入时解析为 BrandID。
func seedCarModels() {
	var count int64
	db.DB.Model(&model.CarModel{}).Count(&count)
	if count > 0 {
		log.Println("车型数据已存在，跳过")
		return
	}

	// 查找品牌ID（按 BrandCode 解析为 BrandID）。查不到**不回退成 1**——
	// 挂到表里第一个品牌身上会让"理想的车出现在极石品牌页"，且没有任何报错；
	// 留 0（未归属）+ 汇总告警，缺数据看得见，错归属看不见。
	var missingBrand []string
	getBrandID := func(code string) uint {
		var brand model.Brand
		if err := db.DB.Where("code = ?", code).First(&brand).Error; err == nil {
			return brand.ID
		}
		missingBrand = append(missingBrand, code)
		return 0
	}

	carModels := make([]model.CarModel, 0, len(SeedIndustry.CarModels))
	for _, cm := range SeedIndustry.CarModels {
		carModels = append(carModels, model.CarModel{
			BrandID:    getBrandID(cm.BrandCode),
			BrandName:  cm.BrandName,
			Name:       cm.Name,
			Code:       cm.Code,
			PriceRange: cm.PriceRange,
			Level:      cm.Level,
			BodyType:   cm.BodyType,
			FuelType:   cm.FuelType,
			Status:     cm.Status,
			Sort:       cm.Sort,
		})
	}

	for _, m := range carModels {
		db.DB.Create(&m)
	}

	log.Printf("已创建 %d 款车型", len(carModels))
	if len(missingBrand) > 0 {
		sort.Strings(missingBrand)
		log.Printf("[WARN] %d 款车型的品牌 code 在 brands 中不存在，已按未归属(BrandID=0)写入：%v",
			len(missingBrand), missingBrand)
	}
}

// ============================================================
// 11~12. 种子规格参数 / 竞品对比（数据已外置 seed/data_car.go，G-22d）
// ============================================================

// resolveCarModelIDs 一次性取回 car_models 的 code→id 映射。
// 为什么整表取而不是逐条查：规格 361 条 + 对比 58 组，旧写法每行一次 SELECT First，
// 播种要打卡四百多次；改一次读表后内存解析，播种路径上的 DB 往返与数据量无关。
func resolveCarModelIDs() map[string]uint {
	var models []model.CarModel
	db.DB.Select("id", "code").Find(&models)
	ids := make(map[string]uint, len(models))
	for _, m := range models {
		ids[m.Code] = m.ID
	}
	return ids
}

// planModelSpecs 把种子规格解析成待写行（纯函数，不碰 DB）。
//
// 查不到的车型 code 一律**跳过并计数**，绝不再回退成"固定挂到 ID=1"：
// 旧兜底在换行业时必然踩中——SeedIndustry 指到非 auto_rox 后，car_models 里没有 adamas，
// 361 条极石参数就会静默写进表里第一款车型的详情页，接口 200、页面有数据、没人报错。
// 少几行参数是可见的缺失（数字对不上能查），挂错车型是看不见的污染，前者远好于后者。
func planModelSpecs(seeds []ModelSpecSeed, ids map[string]uint) ([]model.ModelSpec, map[string]int) {
	rows := make([]model.ModelSpec, 0, len(seeds))
	skipped := map[string]int{}
	for _, s := range seeds {
		mid, ok := ids[s.ModelCode]
		if !ok {
			skipped[s.ModelCode]++
			continue
		}
		rows = append(rows, model.ModelSpec{
			// 平台级演示数据（tenant_id=0 是本表的出厂约定，非归属丢失） // g12:platform
			ModelID:    mid,
			ParamName:  s.ParamName,
			ParamValue: s.ParamValue,
			ParamUnit:  s.ParamUnit,
			Category:   s.Category,
			IsPrice:    s.IsPrice,
			Status:     s.Status,
			Sort:       s.Sort,
		})
	}
	return rows, skipped
}

// planCompetitorCompares 把种子竞品对比解析成待写行（纯函数，口径同 planModelSpecs）。
// 对比数据的"我方车型"同样按 code 解析；解析不出来的整组跳过——把极石 vs 坦克的结论
// 挂到别家车型身上，销售拿去用的那一刻才被发现，代价太大。
func planCompetitorCompares(seeds []CompetitorCompareSeed, ids map[string]uint) ([]model.CompetitorCompare, map[string]int) {
	rows := make([]model.CompetitorCompare, 0, len(seeds))
	skipped := map[string]int{}
	for _, c := range seeds {
		mid, ok := ids[c.OurModelCode]
		if !ok {
			skipped[c.OurModelCode]++
			continue
		}
		rows = append(rows, model.CompetitorCompare{
			// 平台级演示数据（tenant_id=0 是本表出厂约定，非归属丢失） // g12:platform
			OurModelID:      mid,
			CompetitorBrand: c.CompetitorBrand,
			CompetitorModel: c.CompetitorModel,
			CompareType:     c.CompareType,
			Content:         c.Content,
			ContainsPrice:   c.ContainsPrice,
			Status:          c.Status,
		})
	}
	return rows, skipped
}

// logSkippedSeeds 汇总打印解析不出车型的种子条目（一行一条 WARN，键有序，便于比对两次跑批）。
func logSkippedSeeds(what string, skipped map[string]int, total int) {
	if len(skipped) == 0 {
		return
	}
	codes := make([]string, 0, len(skipped))
	for code := range skipped {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	n := 0
	for _, c := range codes {
		n += skipped[c]
	}
	log.Printf("[WARN] %s：%d/%d 条因车型 code 在 car_models 中不存在而跳过（codes=%v）——"+
		"多半是 SeedIndustry 与车型库不同行业，请把数据一起换掉", what, n, total, codes)
}

// seedModelSpecs 写入 model_specs 表（幂等：已有规格则整块跳过）。
// 作用：为在售车型逐条写入核心规格参数（动力/车身/电池/底盘/越野/智驾/座舱/舒适/户外），
// 按 Category 分组、Sort 排序；被车型详情页与 AI 对比话术检索使用。
// G-22d（2026-09-25）：数据从本文件外置到 seed/data_car.go 并改按车型 code 关联，
// 现在随 SeedIndustry 一起切换（旧写法把 361 条硬绑在 auto_rox 源码里，且查不到 code 时回退 ID=1）。
func seedModelSpecs() {
	var count int64
	db.DB.Model(&model.ModelSpec{}).Count(&count)
	if count > 0 {
		log.Println("规格参数数据已存在，跳过")
		return
	}
	ids := resolveCarModelIDs()
	rows, skipped := planModelSpecs(SeedIndustry.ModelSpecs, ids)
	created := 0
	for _, spec := range rows {
		if err := db.DB.Create(&spec).Error; err != nil {
			log.Printf("[WARN] 规格参数写入失败（车型 %d / 参数 %s）: %v", spec.ModelID, spec.ParamName, err)
			continue
		}
		created++
	}
	log.Printf("已创建 %d 条规格参数", created)
	logSkippedSeeds("规格参数", skipped, len(SeedIndustry.ModelSpecs))
}

// seedCompetitorCompares 写入 competitor_compares 表（幂等：已有对比则跳过）。
// 作用：预置我方车型 vs 竞品的多组对比话术，每组 Content 为 JSON 数组
// （aspect/our_value/their_value/conclusion 多维对比），ContainsPrice 标记是否含价格。
// 供 AI 对比锚话术与销售对比物料使用。
// G-22d（2026-09-25）：数据外置 + 按 code 解析（与 seedModelSpecs 同口径，不再回退 ID=1）。
func seedCompetitorCompares() {
	var count int64
	db.DB.Model(&model.CompetitorCompare{}).Count(&count)
	if count > 0 {
		log.Println("竞品对比数据已存在，跳过")
		return
	}
	ids := resolveCarModelIDs()
	rows, skipped := planCompetitorCompares(SeedIndustry.CompetitorCompares, ids)
	created := 0
	for _, comp := range rows {
		if err := db.DB.Create(&comp).Error; err != nil {
			log.Printf("[WARN] 竞品对比写入失败（车型 %d vs %s）: %v", comp.OurModelID, comp.CompetitorModel, err)
			continue
		}
		created++
	}
	log.Printf("已创建 %d 组竞品对比", created)
	logSkippedSeeds("竞品对比", skipped, len(SeedIndustry.CompetitorCompares))
}
