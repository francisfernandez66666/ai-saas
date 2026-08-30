// Package seed 系统首次启动写入默认数据的种子包。
// 全部 seed 函数都幂等：先按表计数，已有数据就跳过，避免重复插入。
// 写入顺序：租户→套餐→商业包→用户→标签→规则→权重→卖点→模板
// →流程→品牌车型→竞品→知识→模拟客户，保证外键和依赖链正确。
package seed

import (
	"log"
)

// 种子数据初始化（seed 包）
// 系统首次启动时由 InitSeedData 统一写入默认数据，保证开箱即用。
// 全部 seed 函数均为幂等设计：先按表计数，已有数据则跳过，避免重复插入。
// 数据分类（按写入顺序）：
//   1) 租户 Tenant              —— SaaS 系统层一等实体，必须先于一切业务数据
//   2) 订阅套餐 SubscriptionPlan —— 商业化套餐档位（个人/企业/定制）
//   3) 商业包 Package           —— 注册试用/包月/增量买断包（M2 商业化）
//   4) 用户 User                —— 1 个超级管理员 + 3 个销售（含出厂弱密码强改密）
//   5) 标签 Tag                 —— 意向/属性/抗性/价值/行为五大类
//   6) 打标规则 TagRule         —— 关键词→自动打标签的匹配规则
//   7) 权重映射 TagWeightMapping —— 标签→T向量维度的驱动映射
//   8) 卖点 Feature             —— 车型核心卖点话术
//   9) 话术模板 Template         —— 8 类锚（同类/拆解/对比/损失/稀缺/代价/不抛）话术
//  10) 流程定义 FlowDefinition   —— 默认对话流程（策略中心→AI/人工/养鱼循环）
//  11) 品牌 Brand / 车型 CarModel / 规格 ModelSpec —— 极石+竞品车型参数库（数据外置 seed/data，随 SeedIndustry 切换）
//  12) 竞品对比 CompetitorCompare —— 极石 vs 坦克/理想/方程豹/仰望 话术对比（auto_rox demo，P1-4 待外置）
//  13) 知识片段 KnowledgeFragment —— 营销/技术/竞品话术知识库（数据外置 seed/data，随 SeedIndustry 切换）
//  14) 模拟客户 Customer         —— 10 个演示客户（含标签与 T 向量）
// ============================================================

// InitSeedData 初始化种子数据
// 幂等设计：如果已有数据则不重复插入
func InitSeedData() {
	log.Println("开始初始化种子数据...")

	// 默认租户必须最先创建（幂等）：
	// 1) TenantResolver fail-closed 解析依赖它（debug 本地兜底/超管默认落点）
	// 2) db.backfillTenantIDs 存量回填依赖它
	// 修复：原先租户创建嵌在 seedUsers 内，已有用户数据时提前 return 导致租户永远建不出来
	seedTenants()
	seedSubscriptionPlans()
	seedPackages()
	seedUsers()
	seedTags()
	seedTagRules()
	seedTagWeightMappings()
	seedFeatures()
	seedTemplates()
	seedFlowDefinition()
	seedWelcomePaidFlow()
	seedBrands()
	seedCarModels()
	seedModelSpecs()
	seedCompetitorCompares()
	seedKnowledgeFragments()
	seedCustomers()

	log.Println("种子数据初始化完成")
}
