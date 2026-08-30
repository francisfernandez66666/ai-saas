// Package seed 行业种子选择器（P1-4 去硬编码，2026-08-30）
//
// SeedIndustry 指向当前系统开箱即用的行业演示数据包。默认 auto_rox（极石汽车）。
// 换行业：在 seed/data 包下新增一个 Industry 实例，把本变量指向它，
// seed 逻辑（seedBrands/seedCarModels/seedKnowledgeFragments 等）无需改动即自动切换。
//
// 注意：车型规格(model_specs)与竞品对比(competitor_compares) 仍内联于 seed_car.go，
// 暂未外置；其数据同为 auto_rox 行业 demo，后续按 seed/data 同模式迁移（见 seed_car.go 顶部说明）。
package seed

import "ai-scrm/seed/data"

// SeedIndustry 当前种子行业标识对应的数据集合。
var SeedIndustry = &data.AutoRox
