package seed

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"log"
)

// ============================================================
// 6. 种子模拟客户（10个）
// ============================================================

// seedCustomers 写入 customers 表（幂等：已有客户则跳过）。
// 副作用：除写入 10 个演示客户外，还按 tagMap 为每个客户调用 SetTags 打标签，
// 并通过 GetTVector/SaveTVector 计算并落库其 T 向量（策略中心打标驱动的载体）。
func seedCustomers() {
	var count int64
	db.DB.Model(&model.Customer{}).Count(&count)
	if count > 0 {
		log.Println("客户数据已存在，跳过")
		return
	}

	customers := []model.Customer{
		{
			Name: "陈先生", Phone: "13900000001", WechatID: "chen_001",
			Gender: 1, Age: 32, Region: "华东", City: "上海", Career: "工程师",
			CustomerType: "potential", InterestModel: "极石01", CurrentCar: "大众途观", CarAge: 4,
			Source: "抖音", Budget: 35, DecisionCycle: 30, StoreVisited: 0,
			TrustLevel: 0.3, IntentScore: 0.25, PriceSensitivity: 0.6, BrandAwareness: 0.3,
			ResistanceType: "none", AssignedUserID: 2,
		},
		{
			Name: "李女士", Phone: "13900000002", WechatID: "li_002",
			Gender: 2, Age: 28, Region: "华北", City: "北京", Career: "白领",
			CustomerType: "potential", InterestModel: "极石01", CurrentCar: "", CarAge: 0,
			Source: "小红书", Budget: 25, DecisionCycle: 60, StoreVisited: 0,
			TrustLevel: 0.25, IntentScore: 0.15, PriceSensitivity: 0.7, BrandAwareness: 0.2,
			ResistanceType: "price", AssignedUserID: 2,
		},
		{
			Name: "王总", Phone: "13900000003", WechatID: "wang_003",
			Gender: 1, Age: 45, Region: "华南", City: "广州", Career: "企业主",
			CustomerType: "potential", InterestModel: "极石ADAMAS", CurrentCar: "丰田霸道", CarAge: 6,
			Source: "老客转介绍", Budget: 80, DecisionCycle: 15, StoreVisited: 1,
			TrustLevel: 0.5, IntentScore: 0.65, PriceSensitivity: 0.3, BrandAwareness: 0.6,
			ResistanceType: "none", AssignedUserID: 3,
		},
		{
			Name: "张女士", Phone: "13900000004", WechatID: "zhang_004",
			Gender: 2, Age: 35, Region: "华东", City: "杭州", Career: "教师",
			CustomerType: "potential", InterestModel: "极石01", CurrentCar: "本田CR-V", CarAge: 5,
			Source: "百度", Budget: 32, DecisionCycle: 45, StoreVisited: 1,
			TrustLevel: 0.45, IntentScore: 0.5, PriceSensitivity: 0.5, BrandAwareness: 0.4,
			ResistanceType: "none", AssignedUserID: 4,
		},
		{
			Name: "刘先生", Phone: "13900000005", WechatID: "liu_005",
			Gender: 1, Age: 29, Region: "华中", City: "武汉", Career: "自由职业",
			CustomerType: "potential", InterestModel: "极石ADAMAS", CurrentCar: "Jeep自由光", CarAge: 3,
			Source: "抖音", Budget: 50, DecisionCycle: 20, StoreVisited: 0,
			TrustLevel: 0.35, IntentScore: 0.55, PriceSensitivity: 0.4, BrandAwareness: 0.5,
			ResistanceType: "spec", AssignedUserID: 3,
		},
		{
			Name: "赵先生", Phone: "13900000006", WechatID: "zhao_006",
			Gender: 1, Age: 50, Region: "西南", City: "成都", Career: "公务员",
			CustomerType: "potential", InterestModel: "极石ADAMAS", CurrentCar: "帕萨特", CarAge: 7,
			Source: "门店自然", Budget: 45, DecisionCycle: 90, StoreVisited: 2,
			TrustLevel: 0.6, IntentScore: 0.7, PriceSensitivity: 0.4, BrandAwareness: 0.7,
			ResistanceType: "none", AssignedUserID: 4,
		},
		{
			Name: "孙女士", Phone: "13900000007", WechatID: "sun_007",
			Gender: 2, Age: 38, Region: "华北", City: "天津", Career: "医生",
			CustomerType: "potential", InterestModel: "极石01", CurrentCar: "奥迪Q3", CarAge: 4,
			Source: "微信", Budget: 38, DecisionCycle: 30, StoreVisited: 0,
			TrustLevel: 0.4, IntentScore: 0.45, PriceSensitivity: 0.5, BrandAwareness: 0.5,
			ResistanceType: "service", AssignedUserID: 2,
		},
		{
			Name: "周先生", Phone: "13900000008", WechatID: "zhou_008",
			Gender: 1, Age: 33, Region: "华东", City: "苏州", Career: "白领",
			CustomerType: "potential", InterestModel: "极石01", CurrentCar: "朗逸", CarAge: 6,
			Source: "小红书", Budget: 33, DecisionCycle: 40, StoreVisited: 0,
			TrustLevel: 0.3, IntentScore: 0.3, PriceSensitivity: 0.6, BrandAwareness: 0.3,
			ResistanceType: "none", AssignedUserID: 3,
		},
		{
			Name: "吴先生", Phone: "13900000009", WechatID: "wu_009",
			Gender: 1, Age: 42, Region: "华南", City: "深圳", Career: "高管",
			CustomerType: "owner", InterestModel: "极石ADAMAS", CurrentCar: "极石01", CarAge: 3,
			Source: "老客转介绍", Budget: 60, DecisionCycle: 10, StoreVisited: 3,
			TrustLevel: 0.7, IntentScore: 0.8, PriceSensitivity: 0.3, BrandAwareness: 0.85,
			ResistanceType: "none", AssignedUserID: 4,
		},
		{
			Name: "郑先生", Phone: "13900000010", WechatID: "zheng_010",
			Gender: 1, Age: 36, Region: "西北", City: "西安", Career: "工程师",
			CustomerType: "potential", InterestModel: "极石ADAMAS", CurrentCar: "哈弗H6", CarAge: 5,
			Source: "百度", Budget: 40, DecisionCycle: 50, StoreVisited: 1,
			TrustLevel: 0.4, IntentScore: 0.4, PriceSensitivity: 0.5, BrandAwareness: 0.4,
			ResistanceType: "brand", AssignedUserID: 2,
		},
	}

	// 给每个客户设置标签和T向量
	tagMap := map[int][]string{
		0: {"年轻首购", "对比中"},
		1: {"价格敏感", "年轻首购", "家庭用户"},
		2: {"企业主", "高意向", "已到店", "越野爱好者"},
		3: {"家庭用户", "已到店", "对比中"},
		4: {"越野爱好者", "对比中", "年轻首购"},
		5: {"高意向", "已到店", "老客户"},
		6: {"家庭用户", "价格敏感"},
		7: {"对比中", "年轻首购"},
		8: {"高意向", "老客户", "品牌忠诚", "已到店"},
		9: {"对比中", "越野爱好者"},
	}

	for i := range customers {
		customer := &customers[i]
		if tags, ok := tagMap[i]; ok {
			customer.SetTags(tags)
		}
		tVector := customer.GetTVector()
		customer.SaveTVector(tVector)
		db.DB.Create(customer)
	}

	log.Printf("已创建 %d 个模拟客户", len(customers))
}

// ============================================================
// 7. 种子打标规则（5+条）
// 关键词匹配规则：客户说了什么 → 自动打上对应标签
// ============================================================

// seedTagRules 写入 tag_rules 表（幂等：已有规则则跳过）。
// 作用：预置关键词/抗性/意向三类匹配规则，命中客户消息即自动打对应标签并加权重；
// 通过 getTagID 关联标签表的 code，WeightBonus 为命中后叠加的标签权重。
func seedTagRules() {
	var count int64
	db.DB.Model(&model.TagRule{}).Count(&count)
	if count > 0 {
		log.Println("打标规则数据已存在，跳过")
		return
	}

	// 查找目标标签ID
	getTagID := func(code string) uint {
		var tag model.Tag
		if err := db.DB.Where("code = ?", code).First(&tag).Error; err == nil {
			return tag.ID
		}
		return 0
	}

	rules := []model.TagRule{
		{
			Name: "价格抗性识别", RuleType: "resistance",
			MatchPattern:  jsonArr([]string{"太贵", "价格高", "贵了", "便宜点", "超出预算", "预算不够", "性价比不高", "优惠力度不够"}),
			TargetTagID:   getTagID("price_resistance"),
			TargetTagName: "价格抗性",
			WeightBonus:   1.5,
			Status:        1,
		},
		{
			Name: "配置抗性识别", RuleType: "resistance",
			MatchPattern:  jsonArr([]string{"动力不够", "配置低", "参数不行", "续航短", "空间小", "配置差", "油耗高"}),
			TargetTagID:   getTagID("spec_resistance"),
			TargetTagName: "配置抗性",
			WeightBonus:   1.0,
			Status:        1,
		},
		{
			Name: "品牌抗性识别", RuleType: "resistance",
			MatchPattern:  jsonArr([]string{"没听过这牌子", "小众品牌", "不如丰田", "不如哈佛", "品牌力不够", "杂牌"}),
			TargetTagID:   getTagID("brand_resistance"),
			TargetTagName: "品牌抗性",
			WeightBonus:   1.0,
			Status:        1,
		},
		{
			Name: "技术控识别", RuleType: "keyword",
			MatchPattern:  jsonArr([]string{"发动机型号", "马力多少", "扭矩", "变速箱", "四驱系统", "底盘", "悬挂", "差速锁", "接近角", "离去角"}),
			TargetTagID:   getTagID("tech_geek"),
			TargetTagName: "技术控",
			WeightBonus:   1.2,
			Status:        1,
		},
		{
			Name: "越野兴趣识别", RuleType: "intent",
			MatchPattern:  jsonArr([]string{"越野", "穿越", "沙漠", "戈壁", "山地", "涉水", "攀爬", "玩泥", "户外", "自驾游"}),
			TargetTagID:   getTagID("offroad_lover"),
			TargetTagName: "越野爱好者",
			WeightBonus:   1.5,
			Status:        1,
		},
		{
			Name: "家庭用户识别", RuleType: "intent",
			MatchPattern:  jsonArr([]string{"家里", "老婆", "孩子", "家人", "宝宝", "安全座椅", "后排空间", "后备箱", "接送孩子"}),
			TargetTagID:   getTagID("family_user"),
			TargetTagName: "家庭用户",
			WeightBonus:   1.0,
			Status:        1,
		},
		{
			Name: "品质追求识别", RuleType: "keyword",
			MatchPattern:  jsonArr([]string{"做工", "内饰", "真皮", "质感", "nappa", "豪华", "品质感", "用料"}),
			TargetTagID:   getTagID("quality_seeker"),
			TargetTagName: "品质追求",
			WeightBonus:   1.0,
			Status:        1,
		},
	}

	for _, rule := range rules {
		db.DB.Create(&rule)
	}

	log.Printf("已创建 %d 条打标规则", len(rules))
}

// ============================================================
// 8. 种子权重映射（标签→T向量）
// 打标驱动策略的核心：客户打上某标签，T向量对应维度自动变化
// ============================================================

// seedTagWeightMappings 写入 tag_weight_mappings 表（幂等：已有映射则跳过）。
// 作用：建立「标签 → T向量维度」的驱动关系；客户被打上某标签时，按 TVectorIndex 指定的
// T向量维度(Direction/WeightDelta)自动调整分值。例如抗性类标签影响 T[14]抗性类型维度，
// 意向类标签影响 T[0]意向分——这是打标驱动策略引擎的核心数据。
func seedTagWeightMappings() {
	var count int64
	db.DB.Model(&model.TagWeightMapping{}).Count(&count)
	if count > 0 {
		log.Println("权重映射数据已存在，跳过")
		return
	}

	// 查找标签ID
	getTagID := func(code string) uint {
		var tag model.Tag
		if err := db.DB.Where("code = ?", code).First(&tag).Error; err == nil {
			return tag.ID
		}
		return 0
	}

	mappings := []model.TagWeightMapping{
		// ---- 意向类标签 → T[0]意向分 ----
		{TagID: getTagID("high_intent"), TagCode: "high_intent", TVectorIndex: 0, WeightDelta: 0.15, Direction: "up"},
		{TagID: getTagID("comparing"), TagCode: "comparing", TVectorIndex: 0, WeightDelta: 0.05, Direction: "up"},
		{TagID: getTagID("test_drive_pending"), TagCode: "test_drive_pending", TVectorIndex: 0, WeightDelta: 0.1, Direction: "up"},
		{TagID: getTagID("visited_store"), TagCode: "visited_store", TVectorIndex: 0, WeightDelta: 0.1, Direction: "up"},

		// ---- 属性类标签 → T[1]价格敏感度 ----
		{TagID: getTagID("price_sensitive"), TagCode: "price_sensitive", TVectorIndex: 1, WeightDelta: 0.2, Direction: "up"},

		// ---- 属性类标签 → T[2]品牌认知度 ----
		{TagID: getTagID("brand_loyal"), TagCode: "brand_loyal", TVectorIndex: 2, WeightDelta: 0.15, Direction: "up"},

		// ---- 属性类标签 → T[6]信任度 ----
		{TagID: getTagID("old_customer"), TagCode: "old_customer", TVectorIndex: 6, WeightDelta: 0.15, Direction: "up"},

		// ---- 抗性类标签 → T[14]抗性类型 ----
		{TagID: getTagID("price_resistance"), TagCode: "price_resistance", TVectorIndex: 14, WeightDelta: 1.0, Direction: "up"},
		{TagID: getTagID("spec_resistance"), TagCode: "spec_resistance", TVectorIndex: 14, WeightDelta: 2.0, Direction: "up"},
		{TagID: getTagID("brand_resistance"), TagCode: "brand_resistance", TVectorIndex: 14, WeightDelta: 4.0, Direction: "up"},
		{TagID: getTagID("service_resistance"), TagCode: "service_resistance", TVectorIndex: 14, WeightDelta: 3.0, Direction: "up"},

		// ---- 抗性类标签也会增加价格敏感度 ----
		{TagID: getTagID("price_resistance"), TagCode: "price_resistance", TVectorIndex: 1, WeightDelta: 0.1, Direction: "up"},

		// ---- 价值类标签 → 多维度影响 ----
		{TagID: getTagID("offroad_lover"), TagCode: "offroad_lover", TVectorIndex: 12, WeightDelta: 1.0, Direction: "up"}, // 用车场景
		{TagID: getTagID("tech_geek"), TagCode: "tech_geek", TVectorIndex: 2, WeightDelta: 0.1, Direction: "up"},          // 品牌认知
		{TagID: getTagID("quality_seeker"), TagCode: "quality_seeker", TVectorIndex: 2, WeightDelta: 0.05, Direction: "up"},
	}

	for _, mapping := range mappings {
		db.DB.Create(&mapping)
	}

	log.Printf("已创建 %d 条权重映射", len(mappings))
}
