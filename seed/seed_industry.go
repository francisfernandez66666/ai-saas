// Package seed 行业种子选择器（P1-4 去硬编码，2026-08-30）
//
// SeedIndustry 指向当前系统开箱即用的行业演示数据包。默认 auto_rox（极石汽车）。
// 换行业：在 seed 包下新增一个 Industry 实例，把本变量指向它，
// seed 逻辑（seedBrands/seedCarModels/seedKnowledgeFragments 等）无需改动即自动切换。
//
// 口径（G-22d，2026-09-25）：品牌/车型/知识片段/车型规格/竞品对比**全部**随本选择器切换。
// 换行业后若车型 code 对不上，规格与对比会在写库阶段整批跳过并打 WARN（见 seed_car.go planModelSpecs），
// 不再回退到车型 ID=1 把上一家的参数挂到新车款身上。
package seed

// SeedIndustry 当前种子行业标识对应的数据集合。
var SeedIndustry = &AutoRox

// SeedDemoTenantCode 开箱演示数据所属的那个租户 code（seedTenants 亲手创建的行）。
//
// 声明成常量而不是字面量抄两遍（G-24，2026-09-25）：演示包绑定是按 code 精确查租户的，
// 种子里"建哪个租户"和"给哪个租户落包"必须是同一个值——各写一遍，改名时绑定就会静默落空，
// 而落空只打一行 log，没人会在满屏启动日志里看见它。
const SeedDemoTenantCode = "default"

// DemoPackBinding 返回演示数据所声明的包绑定三元组：租户 code / 行业基包 code / 企业包 code。
//
// 由 main.go 在行业包注册完成之后、默认落包之前调用一次（G-24，2026-09-25）：
// seed 只能写"货"（品牌/车型/规格/知识），包目录此刻还不存在，绑定必须由装配层来串。
// 三个值任一为空即表示该行业集合不参与演示包绑定。
func DemoPackBinding() (tenantCode, industryPack, enterprisePack string) {
	if SeedIndustry == nil {
		return "", "", ""
	}
	return SeedIndustry.DemoTenantCode, SeedIndustry.IndustryPackCode, SeedIndustry.EnterprisePackCode
}
