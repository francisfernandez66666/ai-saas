package seed

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/pkg/utils"
	"encoding/json"
	"log"
)

// ============================================================
// 种子数据初始化
// 系统首次启动时插入默认数据，保证开箱即用
// 包含：用户、话术模板、卖点、流程定义、模拟客户
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
	seedUsers()
	seedTags()
	seedTagRules()
	seedTagWeightMappings()
	seedFeatures()
	seedTemplates()
	seedFlowDefinition()
	seedBrands()
	seedCarModels()
	seedModelSpecs()
	seedCompetitorCompares()
	seedKnowledgeFragments()
	seedCustomers()

	log.Println("种子数据初始化完成")
}

// ============================================================
// 1. 种子用户：1个管理员 + 3个销售
// ============================================================

// seedTenants 创建默认租户（幂等，独立于用户种子）
// SaaS 化改造：租户是系统层一等实体，必须先于一切业务数据存在
func seedTenants() {
	var tenantCount int64
	db.DB.Model(&model.Tenant{}).Count(&tenantCount)
	if tenantCount > 0 {
		return
	}
	defaultTenant := &model.Tenant{
		Name:           "rox-sales",
		Code:           "default",
		Tier:           "personal",
		PrimaryColor:   "#1890ff",
		SecondaryColor: "#909399",
		Status:         "active",
		MaxUsers:       100,
		MaxCustomers:   10000,
		MaxAICTalls:    10000,
		UsedAICTalls:   0,
		UsedCustomers:  0,
	}
	if err := db.DB.Create(defaultTenant).Error; err != nil {
		log.Printf("创建默认租户失败: %v", err)
		return
	}
	log.Println("已创建默认租户：name=rox-sales code=default")
}

func seedUsers() {
	var count int64
	db.DB.Model(&model.User{}).Count(&count)
	if count > 0 {
		// 已有数据：尝试将老 admin 的 role 升级为 super_admin（兼容旧版本）
		var admin model.User
		db.DB.First(&admin, "username = ?", "admin")
		if admin.Role == "admin" {
			admin.Role = "super_admin"
			db.DB.Save(&admin)
			log.Println("检测到老管理员 role='admin'，已自动升级为 super_admin")
		}
		log.Println("用户数据已存在，跳过写入")
		return
	}

	// 获取默认租户ID（租户由 seedTenants() 统一创建，见 InitSeedData）
	var defaultTenant model.Tenant
	db.DB.First(&defaultTenant, "code = ?", "default")
	defaultTenantID := defaultTenant.ID

	// 管理员 - role=super_admin，tenant_id=0 表示超级管理员（全局不受租户隔离约束）
	adminPwd, _ := utils.HashPassword("admin123")
	admin := &model.User{
		Username:     "admin",
		PasswordHash: adminPwd,
		RealName:     "系统管理员",
		Role:         "super_admin",
		Phone:        "13800000000",
		Email:        "admin@autoscrm.com",
		Department:   "运营部",
		TenantID:     nil, // NULL 表示超级管理员
		Status:       1,
	}
	db.DB.Create(admin)
	log.Println("已创建超级管理员: admin (role=super_admin, tenant_id=NULL)")

	// 销售 - 归属默认租户
	salesPwd, _ := utils.HashPassword("sales123")
	salesNames := []string{"张伟", "李娜", "王磊"}
	salesPhones := []string{"13800000001", "13800000002", "13800000003"}

	for i := 0; i < 3; i++ {
		sales := &model.User{
			Username:     "sales" + string(rune('1'+i)),
			PasswordHash: salesPwd,
			RealName:     salesNames[i],
			Role:         "sales",
			Phone:        salesPhones[i],
			Department:   "销售部",
			TenantID:     &defaultTenantID, // 归属默认租户
			Status:       1,
		}
		db.DB.Create(sales)
		log.Println("已创建销售账号: sales" + string(rune('1'+i)) + " (tenant_id=" + string(rune('1'+defaultTenantID)) + ")")
	}

	log.Println("已创建 1个超级管理员 + 3个销售账号")
}

// ============================================================
// 2. 种子标签
// ============================================================

func seedTags() {
	var count int64
	db.DB.Model(&model.Tag{}).Count(&count)
	if count > 0 {
		log.Println("标签数据已存在，跳过")
		return
	}

	tags := []model.Tag{
		// ---- 意向类 ----
		{Name: "高意向", Code: "high_intent", Category: "意向", Weight: 2.0, Description: "购买意向强烈"},
		{Name: "对比中", Code: "comparing", Category: "意向", Weight: 1.0, Description: "正在对比多款车型"},
		{Name: "待试驾", Code: "test_drive_pending", Category: "意向", Weight: 1.5, Description: "预约了试驾还未到店"},
		// ---- 属性类 ----
		{Name: "价格敏感", Code: "price_sensitive", Category: "属性", Weight: 1.5, Description: "对价格比较敏感"},
		{Name: "品牌忠诚", Code: "brand_loyal", Category: "属性", Weight: 1.0, Description: "对品牌认可度高"},
		{Name: "家庭用户", Code: "family_user", Category: "属性", Weight: 1.0, Description: "家庭使用为主"},
		{Name: "年轻首购", Code: "young_first", Category: "属性", Weight: 1.0, Description: "年轻人第一次买车"},
		{Name: "企业主", Code: "business_owner", Category: "属性", Weight: 1.5, Description: "私营企业主"},
		// ---- 抗性类 ----
		{Name: "价格抗性", Code: "price_resistance", Category: "抗性", Weight: 1.5, Description: "对价格有明显抗性"},
		{Name: "配置抗性", Code: "spec_resistance", Category: "抗性", Weight: 1.0, Description: "对配置/动力有疑虑"},
		{Name: "品牌抗性", Code: "brand_resistance", Category: "抗性", Weight: 1.0, Description: "对品牌认可度低"},
		{Name: "服务抗性", Code: "service_resistance", Category: "抗性", Weight: 1.0, Description: "担心售后问题"},
		// ---- 价值类 ----
		{Name: "越野爱好者", Code: "offroad_lover", Category: "价值", Weight: 1.5, Description: "喜欢越野活动"},
		{Name: "技术控", Code: "tech_geek", Category: "价值", Weight: 1.2, Description: "对技术参数感兴趣"},
		{Name: "品质追求", Code: "quality_seeker", Category: "价值", Weight: 1.0, Description: "看重品质和做工"},
		// ---- 行为类 ----
		{Name: "已到店", Code: "visited_store", Category: "行为", Weight: 1.5, Description: "已经到店看过车"},
		{Name: "老客户", Code: "old_customer", Category: "行为", Weight: 1.0, Description: "之前购买过"},
	}

	for _, tag := range tags {
		db.DB.Create(&tag)
	}

	log.Printf("已创建 %d 个标签", len(tags))
}

// ============================================================
// 3. 种子卖点数据（5-8条）
// ============================================================

func seedFeatures() {
	var count int64
	db.DB.Model(&model.Feature{}).Count(&count)
	if count > 0 {
		log.Println("卖点数据已存在，跳过")
		return
	}

	features := []model.Feature{
		{
			ID:               "feat_offroad_4x4",
			FeatureName:      "双电机全时四驱+L3越野认证",
			Category:         "性能",
			DescTemplate:     "极石ADAMAS搭载前后双电机全时四驱系统，综合功率350kW、峰值扭矩740N·m，272mm最小离地间隙、770mm涉水深度，7种全地形模式随心切换。中汽研L3级越野认证，沙漠戈壁雨雪泥泞都能稳稳通过。",
			ShortDesc:        "双电机全时四驱，L3越野认证，真正的全地形能力",
			Params:           jsonParams(map[string]string{"power": "350kW/740N·m", "clearance": "272mm离地间隙", "wading": "770mm涉水深度", "cert": "L3级越野认证"}),
			ApplicableTags:   jsonArr([]string{"越野爱好者"}),
			ApplicableModels: jsonArr([]string{"极石ADAMAS"}),
			Priority:         10,
			Status:           1,
		},
		{
			ID:               "feat_safety_pro",
			FeatureName:      "全铝车身+高阶智驾安全",
			Category:         "安全",
			DescTemplate:     "极石全系采用全铝承载式车身，轻量化高刚性，碰撞安全性远超同级别。ADAMAS标配空气悬架+7种全地形模式保障行驶安全；极石01高阶版搭载3颗激光雷达+双Orin-X芯片（508TOPS），支持城市NOA和高速领航辅助，长途开车不累，全家出行更安心。",
			ShortDesc:        "全铝车身+激光雷达智驾，安全配置拉满",
			Params:           jsonParams(map[string]string{"body": "全铝承载式车身", "lidar": "3颗激光雷达(01高阶版)", "chip": "双Orin-X 508TOPS"}),
			ApplicableTags:   jsonArr([]string{"家庭用户"}),
			ApplicableModels: jsonArr([]string{"极石01", "极石ADAMAS"}),
			Priority:         9,
			Status:           1,
		},
		{
			ID:               "feat_luxury_interior",
			FeatureName:      "零重力座椅+智能座舱",
			Category:         "舒适",
			DescTemplate:     "极石ADAMAS二排零重力航空座椅，加热/通风/按摩一应俱全，6/7座灵活布局。15.7英寸3K中控屏+后排3K娱乐屏，高通8155芯片驱动，小石头AI助手融合DeepSeek+文心一言大模型，语音交互自然智能。电吸门全系标配（全车+尾门），8.5L冷暖双模车载冰箱全系标配，每一次出行都是豪华享受。",
			ShortDesc:        "零重力座椅+8155智能座舱，全系标配豪华配置",
			Params:           jsonParams(map[string]string{"seat": "二排零重力航空座椅", "screen": "15.7英寸3K中控屏+后排娱乐屏", "chip": "高通8155", "fridge": "8.5L冷暖双模冰箱", "door": "电吸门全车+尾门"}),
			ApplicableTags:   jsonArr([]string{"企业主", "家庭用户"}),
			ApplicableModels: jsonArr([]string{"极石ADAMAS"}),
			Priority:         8,
			Status:           1,
		},
		{
			ID:               "feat_engine_power",
			FeatureName:      "1.5T增程器+双电机350kW",
			Category:         "性能",
			DescTemplate:     "极石搭载1.5T高效增程器+前后双电机，系统综合功率350kW、峰值扭矩740N·m。增程器只发电不直接驱动，始终工作在最优效率区间，馈电油耗仅6.76L/100km。CLTC综合续航1405km（ADAMAS）/1362km（01），纯电续航215-306km，日常通勤不烧油，长途出行无焦虑。",
			ShortDesc:        "350kW/740N·m增程动力，1405km综合续航",
			Params:           jsonParams(map[string]string{"power": "350kW/740N·m", "range": "CLTC 1405km综合续航", "ev_range": "纯电215-306km", "fuel_consumption": "馈电6.76L/100km"}),
			ApplicableTags:   jsonArr([]string{"越野爱好者", "年轻首购"}),
			ApplicableModels: jsonArr([]string{"极石01", "极石ADAMAS"}),
			Priority:         9,
			Status:           1,
		},
		{
			ID:               "feat_warranty",
			FeatureName:      "整车质保+道路救援",
			Category:         "服务",
			DescTemplate:     "极石提供整车5年/15万公里质保，三电系统8年/15万公里质保，增程器专项保障更安心。全国服务网点持续扩展中，24小时道路救援随时待命，让您无论城市通勤还是长途穿越都无后顾之忧。",
			ShortDesc:        "整车5年/三电8年质保，长途穿越更安心",
			Params:           jsonParams(map[string]string{"warranty": "5年/15万公里整车", "battery": "8年/15万公里三电", "rescue": "24小时道路救援"}),
			ApplicableTags:   jsonArr([]string{"家庭用户", "老客户"}),
			ApplicableModels: jsonArr([]string{"极石01", "极石ADAMAS"}),
			Priority:         7,
			Status:           1,
		},
		{
			ID:               "feat_finance",
			FeatureName:      "极石专属金融方案",
			Category:         "服务",
			DescTemplate:     "极石01起售价29.99万、ADAMAS起售价34.99万，提供多种灵活金融方案：最低首付20%起，最长5年分期，还有0利率方案可选。增程车免购置税再省3万+，审批快至当天，轻松把爱车开回家。",
			ShortDesc:        "29.99万起+免购置税，金融方案灵活",
			Params:           jsonParams(map[string]string{"price_01": "29.99万起", "price_adamas": "34.99万起", "tax": "增程免购置税省3万+", "down_payment": "20%起"}),
			ApplicableTags:   jsonArr([]string{"价格敏感", "年轻首购"}),
			ApplicableModels: jsonArr([]string{"极石01", "极石ADAMAS"}),
			Priority:         8,
			Status:           1,
		},
		{
			ID:               "feat_offroad_package",
			FeatureName:      "尾门餐吧+户外生态系统",
			Category:         "性能",
			DescTemplate:     "极石ADAMAS独创尾门餐吧2.0系统，折叠操作台+模块化收纳+LED照明+一键供水，3分钟搭出户外厨房。5.7kW双路外放电（3.5kW+2.2kW），支持电磁炉+咖啡机同时使用。车顶300kg载荷加装车顶帐篷，二三排放倒纯平大床，车就是你的移动山居。7种全地形模式+AT胎可选，越野穿越更从容。",
			ShortDesc:        "尾门餐吧2.0+5.7kW外放电，户外生活全能装备",
			Params:           jsonParams(map[string]string{"kitchen": "尾门餐吧2.0系统", "v2l": "5.7kW双路外放电", "roof": "车顶300kg载荷", "mode": "7种全地形模式"}),
			ApplicableTags:   jsonArr([]string{"越野爱好者"}),
			ApplicableModels: jsonArr([]string{"极石ADAMAS"}),
			Priority:         6,
			Status:           1,
		},
	}

	for _, f := range features {
		db.DB.Create(&f)
	}

	log.Printf("已创建 %d 条卖点数据", len(features))
}

// ============================================================
// 4. 种子话术模板（20+条，覆盖8类锚）
// ============================================================

func seedTemplates() {
	var count int64
	db.DB.Model(&model.Template{}).Count(&count)
	if count > 0 {
		log.Println("话术模板数据已存在，跳过")
		return
	}

	templates := []model.Template{
		// ---- 同类/场景锚 (aggressiveness=1) ----
		// 入门破冰：极石品牌介绍，吸引客户关注
		{
			ID:               "tpl_same_001",
			AnchorType:       1,
			Name:             "入门破冰-车型推荐",
			Category:         "同类锚",
			TriggerTags:      jsonArr([]string{}),
			RequiredTags:     jsonArr([]string{}),
			MinIntent:        0.0,
			MaxIntent:        0.4,
			ApplicableModels: jsonArr([]string{"极石ADAMAS", "极石01"}),
			PromptTemplate:   "您好呀~ 欢迎关注极石！我看您对我们的{{model}}挺感兴趣的对吧？极石是做全地形新能源SUV的品牌，增程式动力、全系标配豪华配置，很多像您这样的客户都特别喜欢，现在关注的人越来越多了。",
			HookTemplate:     "请问您是自己开还是家里用呀？平时会开车去户外玩吗？",
			HookFields:       jsonArr([]string{"usage", "offroad_interest"}),
			RequiredFeatures: jsonArr([]string{}),
			Priority:         10,
			Status:           1,
		},
		// 周末自驾场景：强调6/7座家庭出行+零重力座椅舒适
		{
			ID:               "tpl_scene_001",
			AnchorType:       1,
			Name:             "场景锚-周末自驾",
			Category:         "场景锚",
			TriggerTags:      jsonArr([]string{"家庭用户"}),
			RequiredTags:     jsonArr([]string{}),
			PromptTemplate:   "现在周末自驾游越来越流行了，一家人开个极石SUV出去玩特别舒服。6/7座布局，二排零重力航空座椅，老人孩子坐长途都不累。后备箱放婴儿车、行李什么的都没问题，还有8.5L车载冰箱，夏天冰饮料冬天热奶茶，一路都是享受。",
			HookTemplate:     "您平时周末会带家人出去玩吗？一般都去什么地方呀？",
			HookFields:       jsonArr([]string{"weekend_activity"}),
			RequiredFeatures: jsonArr([]string{"feat_luxury_interior"}),
			Priority:         9,
			Status:           1,
		},
		// 越野爱好者场景：ADAMAS的L3越野认证+全地形能力
		{
			ID:               "tpl_scene_002",
			AnchorType:       1,
			Name:             "场景锚-越野爱好者",
			Category:         "场景锚",
			TriggerTags:      jsonArr([]string{"越野爱好者"}),
			RequiredTags:     jsonArr([]string{}),
			PromptTemplate:   "听说您喜欢越野？那您可找对车了！极石ADAMAS就是为越野而生的——双电机全时四驱350kW、272mm离地间隙、770mm涉水深度，中汽研L3级越野认证，7种全地形模式。很多越野圈的朋友都开它去穿越，口碑特别好。",
			HookTemplate:     "您平时都去哪里越野呀？是玩沙漠还是山地？",
			HookFields:       jsonArr([]string{"offroad_place", "offroad_level"}),
			RequiredFeatures: jsonArr([]string{"feat_offroad_4x4"}),
			Priority:         9,
			Status:           1,
		},
		// 同价位推荐：与竞品对比时的通用切入
		{
			ID:               "tpl_same_002",
			AnchorType:       1,
			Name:             "同类锚-同价位推荐",
			Category:         "同类锚",
			TriggerTags:      jsonArr([]string{"对比中"}),
			RequiredTags:     jsonArr([]string{}),
			PromptTemplate:   "您在对比车型是吧？很正常，买车是大事，多对比对比没错。跟您说，这个价位的新能源SUV里，极石的性价比真的很高——全系标配空气悬架、电吸门、车载冰箱，别的品牌这些都要选装。很多客户对比完最后还是选了极石。",
			HookTemplate:     "您现在在看哪几款车呀？我帮您分析分析~",
			HookFields:       jsonArr([]string{"comparing_models"}),
			RequiredFeatures: jsonArr([]string{}),
			Priority:         8,
			Status:           1,
		},

		// ---- 拆解锚 (aggressiveness=2) ----
		// 动力系统详解：1.5T增程器+双电机+长续航
		{
			ID:               "tpl_disassemble_001",
			AnchorType:       2,
			Name:             "拆解锚-动力系统详解",
			Category:         "拆解锚",
			TriggerTags:      jsonArr([]string{"对比中", "年轻首购"}),
			RequiredTags:     jsonArr([]string{}),
			MinIntent:        0.2,
			PromptTemplate:   "说到买车，动力肯定是您最关心的吧？我给您好好讲讲极石的动力系统。{{feature_desc}}简单说就是：增程器只发电不直接驱动，效率高还省油，纯电215-306km日常通勤不烧油，综合续航最长1405km长途无焦虑。",
			HookTemplate:     "您平时开车是偏激烈一点还是稳一点呀？对纯电续航和油耗在意吗？",
			HookFields:       jsonArr([]string{"driving_style"}),
			RequiredFeatures: jsonArr([]string{"feat_engine_power"}),
			Priority:         8,
			Status:           1,
		},
		// 安全配置详解：全铝车身+智驾+空气悬架
		{
			ID:               "tpl_disassemble_002",
			AnchorType:       2,
			Name:             "拆解锚-安全配置",
			Category:         "拆解锚",
			TriggerTags:      jsonArr([]string{"家庭用户"}),
			RequiredTags:     jsonArr([]string{}),
			MinIntent:        0.2,
			PromptTemplate:   "家用的话，安全肯定是第一位的。极石在安全上从来不打折扣——{{feature_desc}}全铝承载式车身轻量化高刚性，空气悬架保障行驶稳定，还有7种全地形模式应对各种路况。",
			HookTemplate:     "家里有小朋友吧？极石6/7座布局带老人孩子都方便，您对安全配置还有什么特别关注的吗？",
			HookFields:       jsonArr([]string{"safety_concern"}),
			RequiredFeatures: jsonArr([]string{"feat_safety_pro"}),
			Priority:         8,
			Status:           1,
		},
		// 内饰品质详解：零重力座椅+8155座舱+电吸门+冰箱
		{
			ID:               "tpl_disassemble_003",
			AnchorType:       2,
			Name:             "拆解锚-内饰品质",
			Category:         "拆解锚",
			TriggerTags:      jsonArr([]string{"企业主", "品牌忠诚"}),
			RequiredTags:     jsonArr([]string{}),
			MinIntent:        0.3,
			PromptTemplate:   "其实一辆车好不好，坐进去开一下就知道了。极石的座舱质感真的没话说——{{feature_desc}}电吸门轻轻一推就优雅闭合，8.5L车载冰箱夏天冰饮料冬天热奶茶，这种豪华感真的是同价位独一份。",
			HookTemplate:     "您对内饰的材质和做工要求高吗？有没有特别喜欢的内饰颜色呀？",
			HookFields:       jsonArr([]string{"interior_preference"}),
			RequiredFeatures: jsonArr([]string{"feat_luxury_interior"}),
			Priority:         7,
			Status:           1,
		},

		// ---- 对比锚 (aggressiveness=3) ----
		// 规格对比：ADAMAS vs 坦克500/理想L8/方程豹豹8
		{
			ID:               "tpl_compare_spec_001",
			AnchorType:       3,
			SubType:          "spec",
			Name:             "对比锚-规格对比",
			Category:         "对比锚",
			TriggerTags:      jsonArr([]string{"对比中"}),
			RequiredTags:     jsonArr([]string{}),
			MinIntent:        0.4,
			ApplicableModels: jsonArr([]string{"极石ADAMAS"}),
			PromptTemplate:   "说到对比，我给您客观分析一下。看坦克500的话，ADAMAS是承载式全铝车身，公路好开太多了，非承载式大梁车颠得慌；看理想L8的话，ADAMAS能真越野，272mm离地间隙+L3越野认证，L8只是加高的MPV；看方程豹豹8的话，ADAMAS便宜3-7万，配置反而更全——空气悬架、电吸门、车载冰箱全系标配。{{feature_desc}}",
			HookTemplate:     "您对比的那几款车试驾过了吗？实际开起来感觉怎么样？",
			HookFields:       jsonArr([]string{"test_drive_experience"}),
			RequiredFeatures: jsonArr([]string{"feat_engine_power", "feat_offroad_4x4"}),
			Priority:         7,
			Status:           1,
		},
		// 服务对比：极石质保+道路救援
		{
			ID:               "tpl_compare_service_001",
			AnchorType:       3,
			SubType:          "service",
			Name:             "对比锚-服务对比",
			Category:         "对比锚",
			TriggerTags:      jsonArr([]string{"对比中", "老客户"}),
			RequiredTags:     jsonArr([]string{}),
			MinIntent:        0.4,
			ApplicableModels: jsonArr([]string{"极石ADAMAS", "极石01"}),
			PromptTemplate:   "买车其实只是开始，售后才是关键。极石的服务您完全不用担心，{{feature_desc}}增程器结构简单可靠，保养成本比插混低，这一点很多老客户都特别认可。",
			HookTemplate:     "您之前的车保养维修方便吗？对售后这一块是不是挺看重的？",
			HookFields:       jsonArr([]string{"aftersales_concern"}),
			RequiredFeatures: jsonArr([]string{"feat_warranty"}),
			Priority:         6,
			Status:           1,
		},
		// 性价比对比：全系标配 vs 竞品选装
		{
			ID:               "tpl_compare_price_001",
			AnchorType:       3,
			SubType:          "spec",
			Name:             "对比锚-性价比",
			Category:         "对比锚",
			TriggerTags:      jsonArr([]string{"价格敏感", "年轻首购"}),
			RequiredTags:     jsonArr([]string{}),
			MinIntent:        0.3,
			ApplicableModels: jsonArr([]string{"极石ADAMAS", "极石01"}),
			PromptTemplate:   "说实话，极石这个价位能买到这个配置的车，真的挺值的。您算一笔账：ADAMAS 34.99万起步，空气悬架、电吸门全车+尾门、8.5L冰箱全系标配；同样的钱买坦克500只能买中配，买理想L8这些配置都得选装。极石01更划算，29.99万起步，增程免购置税再省3万+，落地比同级便宜一大截。",
			HookTemplate:     "您的购车预算大概是多少呀？我帮您算算哪个配置最划算。",
			HookFields:       jsonArr([]string{"budget_range"}),
			RequiredFeatures: jsonArr([]string{}),
			Priority:         7,
			Status:           1,
		},

		// ---- 损失锚 (aggressiveness=4) ----
		// 错过优惠：强调极石当前购车权益
		{
			ID:               "tpl_loss_001",
			AnchorType:       4,
			Name:             "损失锚-错过优惠",
			Category:         "损失锚",
			TriggerTags:      jsonArr([]string{"高意向", "对比中"}),
			RequiredTags:     jsonArr([]string{}),
			MinIntent:        0.5,
			ApplicableModels: jsonArr([]string{"极石ADAMAS", "极石01"}),
			PromptTemplate:   "跟您说个实在话，现在极石的购车权益真的是今年最大的了——金融免息、置换补贴、终身质保选装权，这些权益过了这月就回收了。很多客户就是因为犹豫了一下，后来再买就少了好几万的权益，都挺后悔的。",
			HookTemplate:     "您要是真看好了，这两天定下来最划算。您看这周方便来店里看看吗？",
			HookFields:       jsonArr([]string{"visit_intention"}),
			RequiredFeatures: jsonArr([]string{}),
			Priority:         6,
			Status:           1,
		},
		// 现车紧张：强调极石热销
		{
			ID:               "tpl_loss_002",
			AnchorType:       4,
			Name:             "损失锚-现车紧张",
			Category:         "损失锚",
			TriggerTags:      jsonArr([]string{"高意向", "已到店"}),
			RequiredTags:     jsonArr([]string{}),
			MinIntent:        0.5,
			ApplicableModels: jsonArr([]string{"极石ADAMAS", "极石01"}),
			PromptTemplate:   "您看中的这个配置这个颜色，现在现车真的不多了。极石最近卖得特别好，这个月已经走了十几台，库里就剩最后两台了。您要是再等等，说不定就得等下一批车，那得等一两个月呢。",
			HookTemplate:     "您是想提现车还是愿意等呀？现车的话我建议您早点定。",
			HookFields:       jsonArr([]string{"car_availability"}),
			RequiredFeatures: jsonArr([]string{}),
			Priority:         6,
			Status:           1,
		},
		// 保养费用：增程比插混/油车更省
		{
			ID:               "tpl_loss_003",
			AnchorType:       4,
			Name:             "损失锚-保养费用",
			Category:         "损失锚",
			TriggerTags:      jsonArr([]string{"价格敏感"}),
			RequiredTags:     jsonArr([]string{}),
			MinIntent:        0.4,
			ApplicableModels: jsonArr([]string{"极石ADAMAS", "极石01"}),
			PromptTemplate:   "很多人买车只看车价，不看后期养车成本，其实这才是大头。极石是增程式，增程器只发电不驱动，结构简单故障率低，保养比插混和油车便宜很多。纯电通勤每公里一毛钱，馈电油耗才6.76L，5年下来油费保养费能省好几万。买别的车看似便宜几万，后期油费保养都给你挣回去。",
			HookTemplate:     "您之前那辆车保养贵吗？一年养车大概花多少钱？",
			HookFields:       jsonArr([]string{"maintenance_cost"}),
			RequiredFeatures: jsonArr([]string{"feat_warranty"}),
			Priority:         5,
			Status:           1,
		},

		// ---- 稀缺锚 (aggressiveness=5) ----
		// 限量版：ADAMAS专属高配
		{
			ID:               "tpl_scarcity_001",
			AnchorType:       5,
			Name:             "稀缺锚-限量版",
			Category:         "稀缺锚",
			TriggerTags:      jsonArr([]string{"高意向", "企业主"}),
			RequiredTags:     jsonArr([]string{}),
			MinIntent:        0.6,
			ApplicableModels: jsonArr([]string{"极石ADAMAS"}),
			PromptTemplate:   "跟您说个消息，极石ADAMAS马上要出一个高配限定版，配置拉满还有专属标识和配色，全国配额有限，特别有收藏价值。这个消息我只跟几个重点客户说了，您要是感兴趣我帮您预留个名额。",
			HookTemplate:     "您对限定版这种感兴趣吗？我把配置单发给您看看？",
			HookFields:       jsonArr([]string{"limited_edition_interest"}),
			RequiredFeatures: jsonArr([]string{"feat_offroad_package"}),
			Priority:         5,
			Status:           1,
		},
		// 专属福利：到店专属权益
		{
			ID:               "tpl_scarcity_002",
			AnchorType:       5,
			Name:             "稀缺锚-专属福利",
			Category:         "稀缺锚",
			TriggerTags:      jsonArr([]string{"高意向", "已到店"}),
			RequiredTags:     jsonArr([]string{"已到店"}),
			MinIntent:        0.6,
			ApplicableModels: jsonArr([]string{"极石ADAMAS", "极石01"}),
			PromptTemplate:   "您今天来得真是时候！我们店刚好有个极石店长特批的名额，能额外送您一个大礼包——全车贴膜、脚垫、还有三次保养，加上极石官方的金融免息和置换补贴，综合算下来能省好几万。这个名额一天就一个，您要是今天定我帮您申请。",
			HookTemplate:     "您看这个福利怎么样？今天定下来真的很划算。",
			HookFields:       jsonArr([]string{"deal_today_intention"}),
			RequiredFeatures: jsonArr([]string{}),
			Priority:         5,
			Status:           1,
		},

		// ---- 代价自担锚 (aggressiveness=6) ----
		// 安全代价：全铝车身+智驾不能省
		{
			ID:               "tpl_selfpay_001",
			AnchorType:       6,
			Name:             "代价自担锚-安全代价",
			Category:         "代价自担锚",
			TriggerTags:      jsonArr([]string{"家庭用户", "对比中"}),
			RequiredTags:     jsonArr([]string{}),
			MinIntent:        0.6,
			ApplicableModels: jsonArr([]string{"极石ADAMAS", "极石01"}),
			PromptTemplate:   "我跟您说句掏心窝子的话，买车千万别图便宜省那几万。安全这东西，平时觉得没用，真出事的时候就是保命的。极石全系全铝承载式车身，轻量化高刚性，碰撞安全性远超同级别；高阶版还有3颗激光雷达+508TOPS智驾算力，主动安全拉满。您想想，家人的安全，能用钱衡量吗？",
			HookTemplate:     "您说是不是这个理儿？安全真的不能省。",
			HookFields:       jsonArr([]string{"safety_importance"}),
			RequiredFeatures: jsonArr([]string{"feat_safety_pro"}),
			Priority:         4,
			Status:           1,
		},
		// 品质代价：全系标配豪华不能省
		{
			ID:               "tpl_selfpay_002",
			AnchorType:       6,
			Name:             "代价自担锚-品质代价",
			Category:         "代价自担锚",
			TriggerTags:      jsonArr([]string{"价格敏感"}),
			RequiredTags:     jsonArr([]string{}),
			MinIntent:        0.5,
			ApplicableModels: jsonArr([]string{"极石ADAMAS", "极石01"}),
			PromptTemplate:   "很多客户一开始也觉得别的品牌更便宜，但是对比完配置都说极石值。为什么？因为极石全系标配空气悬架、电吸门、车载冰箱，别的品牌这些都要选装加钱。您要是买个看起来便宜的，后面选装加一起反而更贵，配置还不如极石全。这个账您可得算明白。",
			HookTemplate:     "您之前的车开了几年？是不是后期各种加装花了好多钱？",
			HookFields:       jsonArr([]string{"old_car_problems"}),
			RequiredFeatures: jsonArr([]string{"feat_warranty"}),
			Priority:         4,
			Status:           1,
		},

		// ---- 不抛锚 (aggressiveness=0) ----
		// 安抚倾听：低意向客户的温和引导
		{
			ID:               "tpl_nothrow_001",
			AnchorType:       0,
			Name:             "不抛锚-安抚倾听",
			Category:         "不抛",
			TriggerTags:      jsonArr([]string{}),
			RequiredTags:     jsonArr([]string{}),
			MaxIntent:        0.2,
			ApplicableModels: jsonArr([]string{"极石ADAMAS", "极石01"}),
			PromptTemplate:   "好的，我明白了。您先慢慢了解极石，有什么问题随时问我。不管您最后买不买，能帮到您我就很开心了。",
			HookTemplate:     "您现在对极石哪方面还想多了解一些呀？",
			HookFields:       jsonArr([]string{"interest_area"}),
			RequiredFeatures: jsonArr([]string{}),
			Priority:         5,
			Status:           1,
		},
		// 纯粹解答：最低压力的信息提供
		{
			ID:               "tpl_nothrow_002",
			AnchorType:       0,
			Name:             "不抛锚-纯粹解答",
			Category:         "不抛",
			TriggerTags:      jsonArr([]string{}),
			RequiredTags:     jsonArr([]string{}),
			MaxIntent:        0.15,
			ApplicableModels: jsonArr([]string{"极石ADAMAS", "极石01"}),
			PromptTemplate:   "没问题，您有什么关于极石的问题尽管问，我知道的都告诉您。买车是大事，多了解了解没错的。",
			HookTemplate:     "您现在最关心的是什么问题呀？",
			HookFields:       jsonArr([]string{"top_concern"}),
			RequiredFeatures: jsonArr([]string{}),
			Priority:         4,
			Status:           1,
		},

		// 补充更多模板
		// 老客户推荐：极石品牌忠诚度
		{
			ID:               "tpl_same_003",
			AnchorType:       1,
			Name:             "同类锚-老客户推荐",
			Category:         "同类锚",
			TriggerTags:      jsonArr([]string{"老客户"}),
			RequiredTags:     jsonArr([]string{}),
			ApplicableModels: jsonArr([]string{"极石ADAMAS", "极石01"}),
			PromptTemplate:   "您是我们的老客户了，那您肯定知道极石的品质。全铝车身+空气悬架+增程长续航，很多老客户换车还是选极石，就是因为开着放心、用着省心。您这次是想换个更大的ADAMAS还是不同配置的呀？",
			HookTemplate:     "您之前那辆车开了几年了？这次想换个什么价位的？",
			HookFields:       jsonArr([]string{"old_car_years", "new_budget"}),
			RequiredFeatures: jsonArr([]string{}),
			Priority:         8,
			Status:           1,
		},
		// 四驱系统详解：ADAMAS双电机全时四驱
		{
			ID:               "tpl_disassemble_004",
			AnchorType:       2,
			Name:             "拆解锚-四驱系统",
			Category:         "拆解锚",
			TriggerTags:      jsonArr([]string{"越野爱好者", "对比中"}),
			RequiredTags:     jsonArr([]string{}),
			MinIntent:        0.3,
			ApplicableModels: jsonArr([]string{"极石ADAMAS"}),
			PromptTemplate:   "说到越野能力，四驱系统是核心。极石ADAMAS这套双电机全时四驱真的很厉害——{{feature_desc}}前后双电机独立驱动，综合功率350kW、扭矩740N·m，272mm离地间隙、770mm涉水深度，7种全地形模式，中汽研L3级越野认证。不是那种城市SUV的假四驱，是真能带你去野的。",
			HookTemplate:     "您之前开的是两驱还是四驱呀？有没有去试过越野？",
			HookFields:       jsonArr([]string{"drivetrain_experience"}),
			RequiredFeatures: jsonArr([]string{"feat_offroad_4x4"}),
			Priority:         8,
			Status:           1,
		},
		// 越野能力对比：ADAMAS vs 坦克500/豹8
		{
			ID:               "tpl_compare_spec_002",
			AnchorType:       3,
			SubType:          "spec",
			Name:             "对比锚-越野能力对比",
			Category:         "对比锚",
			TriggerTags:      jsonArr([]string{"越野爱好者", "对比中"}),
			RequiredTags:     jsonArr([]string{}),
			MinIntent:        0.4,
			ApplicableModels: jsonArr([]string{"极石ADAMAS"}),
			PromptTemplate:   "您要是喜欢越野，那可得好好比比四驱系统。坦克500是非承载式车身，大梁颠得慌，公路开着像开船；而ADAMAS是全铝承载式+空气悬架，公路舒适越野也硬核。豹8虽然越野参数也不错，但贵了3-7万，配置还不如ADAMAS全。{{feature_desc}}",
			HookTemplate:     "您有没有去试驾过对比的那款车？越野能力怎么样？",
			HookFields:       jsonArr([]string{"competitor_offroad"}),
			RequiredFeatures: jsonArr([]string{"feat_offroad_4x4"}),
			Priority:         6,
			Status:           1,
		},
	}

	for _, t := range templates {
		db.DB.Create(&t)
	}

	log.Printf("已创建 %d 条话术模板", len(templates))
}

// ============================================================
// 5. 种子流程定义（默认对话流程）
// ============================================================

func seedFlowDefinition() {
	var count int64
	db.DB.Model(&model.FlowDefinition{}).Count(&count)
	if count > 0 {
		log.Println("流程定义数据已存在，跳过")
		return
	}

	// 构建节点
	nodes := []model.FlowNode{
		{
			ID:        "start",
			Type:      "start",
			Name:      "开始",
			Config:    map[string]interface{}{},
			NextNodes: []string{"strategy_1"},
		},
		{
			ID:   "strategy_1",
			Type: "strategy",
			Name: "策略中心决策",
			Config: map[string]interface{}{
				"conditions": []string{"ai", "human", "fish"},
			},
			NextNodes: []string{"ai_chat", "human_intervene", "fish_mode"},
		},
		{
			ID:        "ai_chat",
			Type:      "ai",
			Name:      "AI对话",
			Config:    map[string]interface{}{},
			NextNodes: []string{"tag_update_1"},
		},
		{
			ID:        "human_intervene",
			Type:      "human",
			Name:      "人工介入",
			Config:    map[string]interface{}{},
			NextNodes: []string{"tag_update_1"},
		},
		{
			ID:        "fish_mode",
			Type:      "wait",
			Name:      "养鱼模式",
			Config:    map[string]interface{}{},
			NextNodes: []string{"strategy_1"},
		},
		{
			ID:   "tag_update_1",
			Type: "tag_update",
			Name: "更新标签",
			Config: map[string]interface{}{
				"add_tags": []string{},
			},
			NextNodes: []string{"strategy_1"},
		},
		{
			ID:        "end",
			Type:      "end",
			Name:      "结束",
			Config:    map[string]interface{}{},
			NextNodes: []string{},
		},
	}

	// 构建连线
	edges := []model.FlowEdge{
		{From: "start", To: "strategy_1", Condition: ""},
		{From: "strategy_1", To: "ai_chat", Condition: "ai"},
		{From: "strategy_1", To: "human_intervene", Condition: "human"},
		{From: "strategy_1", To: "fish_mode", Condition: "fish"},
		{From: "ai_chat", To: "tag_update_1", Condition: ""},
		{From: "human_intervene", To: "tag_update_1", Condition: ""},
		{From: "fish_mode", To: "strategy_1", Condition: ""},
		{From: "tag_update_1", To: "strategy_1", Condition: ""},
	}

	nodesJSON, _ := json.Marshal(nodes)
	edgesJSON, _ := json.Marshal(edges)

	flowDef := &model.FlowDefinition{
		Name:        "默认对话流程",
		Code:        "default_chat_flow",
		Description: "标准AI对话流程：策略中心决策→AI对话/人工/养鱼→循环",
		NodesJSON:   string(nodesJSON),
		EdgesJSON:   string(edgesJSON),
		StartNodeID: "start",
		IsDefault:   true,
		Status:      1,
		Version:     "1.0",
	}

	db.DB.Create(flowDef)

	log.Println("已创建默认对话流程")
}

// ============================================================
// 6. 种子模拟客户（10个）
// ============================================================

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

// ============================================================
// 9. 种子品牌数据（3个品牌）
// ============================================================

// ============================================================
// 9. 种子品牌数据（极石+竞品5品牌）
// ============================================================

func seedBrands() {
	var count int64
	db.DB.Model(&model.Brand{}).Count(&count)
	if count > 0 {
		log.Println("品牌数据已存在，跳过")
		return
	}

	brands := []model.Brand{
		{
			Name: "极石汽车", Code: "rox",
			Logo:        "https://www.roxmotor.com/logo.png",
			Description: "极石汽车（ROX Motor）成立于2021年，由石头科技创始人昌敬与汽车行业老兵闫枫联合创办，背靠世界500强魏桥创业集团战略投资。专注全地形、智能化、新能源技术的全球化豪华汽车品牌。",
			Country:     "中国", FoundedYear: 2021, Status: 1, Sort: 1,
		},
		{
			Name: "坦克", Code: "tank",
			Logo:        "",
			Description: "长城汽车旗下高端越野品牌，以硬派越野著称。",
			Country:     "中国", FoundedYear: 2020, Status: 1, Sort: 2,
		},
		{
			Name: "理想", Code: "lixiang",
			Logo:        "",
			Description: "中国新能源汽车品牌，主打家庭智能SUV。",
			Country:     "中国", FoundedYear: 2015, Status: 1, Sort: 3,
		},
		{
			Name: "方程豹", Code: "leopard",
			Logo:        "",
			Description: "比亚迪旗下专业个性化品牌，主打新能源硬派越野。",
			Country:     "中国", FoundedYear: 2023, Status: 1, Sort: 4,
		},
		{
			Name: "仰望", Code: "yangwang",
			Logo:        "",
			Description: "比亚迪旗下百万级高端新能源品牌。",
			Country:     "中国", FoundedYear: 2023, Status: 1, Sort: 5,
		},
	}

	for _, brand := range brands {
		db.DB.Create(&brand)
	}

	log.Printf("已创建 %d 个品牌", len(brands))
}

// ============================================================
// 10. 种子车型数据（5+款）
// ============================================================

// ============================================================
// 10. 种子车型数据（极石2款+竞品11款）
// ============================================================

func seedCarModels() {
	var count int64
	db.DB.Model(&model.CarModel{}).Count(&count)
	if count > 0 {
		log.Println("车型数据已存在，跳过")
		return
	}

	// 查找品牌ID
	getBrandID := func(code string) uint {
		var brand model.Brand
		if err := db.DB.Where("code = ?", code).First(&brand).Error; err == nil {
			return brand.ID
		}
		return 1
	}

	carModels := []model.CarModel{
		{BrandID: getBrandID("rox"), BrandName: "极石汽车", Name: "极石ADAMAS", Code: "adamas", PriceRange: "34.99-35.99万", Level: "中大型", BodyType: "SUV", FuelType: "增程式", Status: 1, Sort: 1},
		{BrandID: getBrandID("rox"), BrandName: "极石汽车", Name: "极石01", Code: "rox01", PriceRange: "29.99-34.99万", Level: "中大型", BodyType: "SUV", FuelType: "增程式", Status: 1, Sort: 2},
		{BrandID: getBrandID("tank"), BrandName: "坦克", Name: "坦克300 Hi4-T", Code: "tank300", PriceRange: "25-30万", Level: "紧凑型", BodyType: "SUV", FuelType: "插混", Status: 1, Sort: 3},
		{BrandID: getBrandID("tank"), BrandName: "坦克", Name: "坦克400 Hi4-T", Code: "tank400", PriceRange: "28-35万", Level: "中型", BodyType: "SUV", FuelType: "插混", Status: 1, Sort: 4},
		{BrandID: getBrandID("tank"), BrandName: "坦克", Name: "坦克500 Hi4-T", Code: "tank500", PriceRange: "34-42万", Level: "中大型", BodyType: "SUV", FuelType: "插混", Status: 1, Sort: 5},
		{BrandID: getBrandID("tank"), BrandName: "坦克", Name: "坦克700 Hi4-T", Code: "tank700", PriceRange: "43-70万", Level: "中大型", BodyType: "SUV", FuelType: "插混", Status: 1, Sort: 6},
		{BrandID: getBrandID("lixiang"), BrandName: "理想", Name: "理想L6", Code: "lixiangl6", PriceRange: "24.98-27.98万", Level: "中型", BodyType: "SUV", FuelType: "增程式", Status: 1, Sort: 7},
		{BrandID: getBrandID("lixiang"), BrandName: "理想", Name: "理想L7", Code: "lixiangl7", PriceRange: "30-35万", Level: "中大型", BodyType: "SUV", FuelType: "增程式", Status: 1, Sort: 8},
		{BrandID: getBrandID("lixiang"), BrandName: "理想", Name: "理想L8", Code: "lixiangl8", PriceRange: "33-40万", Level: "中大型", BodyType: "SUV", FuelType: "增程式", Status: 1, Sort: 9},
		{BrandID: getBrandID("lixiang"), BrandName: "理想", Name: "理想L9", Code: "lixiangl9", PriceRange: "40-45万", Level: "全尺寸", BodyType: "SUV", FuelType: "增程式", Status: 1, Sort: 10},
		{BrandID: getBrandID("leopard"), BrandName: "方程豹", Name: "方程豹豹5", Code: "leopard5", PriceRange: "23.98-30.98万", Level: "中型", BodyType: "SUV", FuelType: "插混", Status: 1, Sort: 11},
		{BrandID: getBrandID("leopard"), BrandName: "方程豹", Name: "方程豹豹8", Code: "leopard8", PriceRange: "37.98-40.98万", Level: "中大型", BodyType: "SUV", FuelType: "插混", Status: 1, Sort: 12},
		{BrandID: getBrandID("yangwang"), BrandName: "仰望", Name: "仰望U8", Code: "yangwangu8", PriceRange: "109.8万", Level: "全尺寸", BodyType: "SUV", FuelType: "增程式", Status: 1, Sort: 13},
	}

	for _, m := range carModels {
		db.DB.Create(&m)
	}

	log.Printf("已创建 %d 款车型", len(carModels))
}

// ============================================================
// 11. 种子规格参数（每款车10+核心参数）
// ============================================================

// ============================================================
// 11. 种子规格参数（361条，覆盖13款车型）
// ============================================================

func seedModelSpecs() {
	var count int64
	db.DB.Model(&model.ModelSpec{}).Count(&count)
	if count > 0 {
		log.Println("规格参数数据已存在，跳过")
		return
	}

	// 查找车型ID
	getModelID := func(code string) uint {
		var m model.CarModel
		if err := db.DB.Where("code = ?", code).First(&m).Error; err == nil {
			return m.ID
		}
		return 1
	}

	specs := []model.ModelSpec{
		{
			ModelID:   getModelID("adamas"),
			ParamName: "能源类型", ParamValue: "增程式（1.5T增程器+前后双电机）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 0,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "系统综合功率", ParamValue: "350kW（476马力）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 1,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "系统综合扭矩", ParamValue: "740N·m", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 2,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "前电机功率/扭矩", ParamValue: "150kW / 340N·m", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 3,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "后电机功率/扭矩", ParamValue: "200kW / 400N·m", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 4,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "增程器型号", ParamValue: "三菱4A95TD 1.5T四缸", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 5,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "增程器热效率", ParamValue: "42%", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 6,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "百公里加速", ParamValue: "5.5秒", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 7,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "最高车速", ParamValue: "190km/h（电子限速）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 8,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "变速箱", ParamValue: "电动车单速变速箱", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 9,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "驱动形式", ParamValue: "双电机全时四驱", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 10,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "推荐燃油标号", ParamValue: "95号", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 11,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "长×宽×高", ParamValue: "5050×1985×1856mm（含备胎5298mm）", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 12,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "轴距", ParamValue: "3010mm", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 13,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "车身结构", ParamValue: "5门SUV 承载式车身（全铝底盘）", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 14,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "整备质量", ParamValue: "2715kg", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 15,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "最大满载质量", ParamValue: "3300kg", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 16,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "油箱容积", ParamValue: "70L", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 17,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "座位数", ParamValue: "6座/7座", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 18,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "电池类型", ParamValue: "宁德时代三元锂电池", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 19,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "电池容量", ParamValue: "44.5kWh", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 20,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "CLTC纯电续航", ParamValue: "215km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 21,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "WLTC纯电续航", ParamValue: "180km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 22,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "CLTC综合续航", ParamValue: "1405km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 23,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "WLTC综合续航", ParamValue: "1171km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 24,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "直流快充峰值功率", ParamValue: "80kW", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 25,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "快充时间（20%-80%）", ParamValue: "27分钟", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 26,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "对外放电功率", ParamValue: "5.7kW（3.5kW V2L + 2.2kW 220V）", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 27,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "WLTC综合油耗", ParamValue: "0.64L/100km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 28,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "馈电油耗（WLTC）", ParamValue: "6.76L/100km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 29,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "前悬架类型", ParamValue: "全铝合金双叉臂式独立悬架", ParamUnit: "",
			Category: "底盘悬挂", IsPrice: false, Status: 1, Sort: 30,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "后悬架类型", ParamValue: "全铝合金H臂型多连杆独立悬架", ParamUnit: "",
			Category: "底盘悬挂", IsPrice: false, Status: 1, Sort: 31,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "副车架材质", ParamValue: "全铝合金前后副车架", ParamUnit: "",
			Category: "底盘悬挂", IsPrice: false, Status: 1, Sort: 32,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "空气悬架", ParamValue: "标配（闭式空气弹簧，7级高度可调）", ParamUnit: "",
			Category: "底盘悬挂", IsPrice: false, Status: 1, Sort: 33,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "空气悬架最大行程", ParamValue: "140mm", ParamUnit: "",
			Category: "底盘悬挂", IsPrice: false, Status: 1, Sort: 34,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "DCC智能阻尼控制", ParamValue: "标配（舒适/标准/运动/全地形/智能 五模式）", ParamUnit: "",
			Category: "底盘悬挂", IsPrice: false, Status: 1, Sort: 35,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "轮胎规格", ParamValue: "275/45 R21 四季胎", ParamUnit: "",
			Category: "底盘悬挂", IsPrice: false, Status: 1, Sort: 36,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "最小离地间隙", ParamValue: "272mm", ParamUnit: "",
			Category: "越野参数", IsPrice: false, Status: 1, Sort: 37,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "电池包离地间隙", ParamValue: "324mm", ParamUnit: "",
			Category: "越野参数", IsPrice: false, Status: 1, Sort: 38,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "接近角", ParamValue: "27.5°", ParamUnit: "",
			Category: "越野参数", IsPrice: false, Status: 1, Sort: 39,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "离去角", ParamValue: "27.9°", ParamUnit: "",
			Category: "越野参数", IsPrice: false, Status: 1, Sort: 40,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "纵向通过角", ParamValue: "24.6°", ParamUnit: "",
			Category: "越野参数", IsPrice: false, Status: 1, Sort: 41,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "最大涉水深度", ParamValue: "770mm（极限脱困模式）", ParamUnit: "",
			Category: "越野参数", IsPrice: false, Status: 1, Sort: 42,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "最大爬坡度", ParamValue: "100%（45°）", ParamUnit: "",
			Category: "越野参数", IsPrice: false, Status: 1, Sort: 43,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "全地形模式数量", ParamValue: "7种", ParamUnit: "",
			Category: "越野参数", IsPrice: false, Status: 1, Sort: 44,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "越野认证", ParamValue: "中汽研L3级进阶越野能力认证", ParamUnit: "",
			Category: "越野参数", IsPrice: false, Status: 1, Sort: 45,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "四驱形式", ParamValue: "双电机全时四驱（电子限滑，150ms响应）", ParamUnit: "",
			Category: "越野参数", IsPrice: false, Status: 1, Sort: 46,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "智驾芯片", ParamValue: "地平线征程®6M（128TOPS）", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 47,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "毫米波雷达", ParamValue: "3个", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 48,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "摄像头数量", ParamValue: "13个", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 49,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "超声波雷达", ParamValue: "12个", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 50,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "智驾等级", ParamValue: "L2+级辅助驾驶", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 51,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "高速领航辅助（NOA）", ParamValue: "支持", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 52,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "城区车道居中（LCC）", ParamValue: "支持（含信号灯识别）", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 53,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "自动泊车（APA）", ParamValue: "支持", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 54,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "遥控泊车（RPA）", ParamValue: "支持", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 55,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "记忆泊车（HAVP）", ParamValue: "支持", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 56,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "车机芯片", ParamValue: "高通骁龙8155", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 57,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "中控屏幕", ParamValue: "15.7英寸 3K悬浮触控屏", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 58,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "仪表盘", ParamValue: "12.3英寸全液晶仪表", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 59,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "后排娱乐屏", ParamValue: "15.7英寸3K触控屏（5档电动调节）", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 60,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "流媒体后视镜", ParamValue: "9.2英寸窄边框", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 61,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "屏数", ParamValue: "4屏联动", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 62,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "语音助手", ParamValue: "小石头AI助手（融合DeepSeek+文心一言大模型）", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 63,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "系统", ParamValue: "ROX OS智能生态座舱", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 64,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "安全气囊", ParamValue: "8个", ParamUnit: "",
			Category: "安全配置", IsPrice: false, Status: 1, Sort: 65,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "车身高强钢占比", ParamValue: "87.7%", ParamUnit: "",
			Category: "安全配置", IsPrice: false, Status: 1, Sort: 66,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "热成型钢占比", ParamValue: "32.3%", ParamUnit: "",
			Category: "安全配置", IsPrice: false, Status: 1, Sort: 67,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "车顶最大承压", ParamValue: "16.3吨", ParamUnit: "",
			Category: "安全配置", IsPrice: false, Status: 1, Sort: 68,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "整车扭转刚度", ParamValue: "40000+ N·m/度", ParamUnit: "",
			Category: "安全配置", IsPrice: false, Status: 1, Sort: 69,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "C-NCAP评级", ParamValue: "五星", ParamUnit: "",
			Category: "安全配置", IsPrice: false, Status: 1, Sort: 70,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "中保研（CIRI）", ParamValue: "全G（优秀）", ParamUnit: "",
			Category: "安全配置", IsPrice: false, Status: 1, Sort: 71,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "电池防护等级", ParamValue: "IP68级防水防尘", ParamUnit: "",
			Category: "安全配置", IsPrice: false, Status: 1, Sort: 72,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "电吸门", ParamValue: "全车标配（含侧开尾门）", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 73,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "车载冰箱", ParamValue: "8.5L智能冷暖双模冰箱（0℃-50℃）", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 74,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "座椅材质", ParamValue: "Nappa真皮", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 75,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "前排座椅功能", ParamValue: "12向电动调节/加热/通风/腰部按摩", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 76,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "二排座椅（6座版）", ParamValue: "零重力航空座椅/150°躺角/8点式按摩/腿托", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 77,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "空调系统", ParamValue: "三区恒温自动新风空调", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 78,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "空气净化", ParamValue: "PM2.5滤芯+负离子发生器+空气质量监测", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 79,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "氛围灯", ParamValue: "256色座舱氛围灯", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 80,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "尾门餐吧", ParamValue: "户外餐吧2.0（折叠操作台/收纳/LED照明/一键供水）", ParamUnit: "",
			Category: "户外配置", IsPrice: false, Status: 1, Sort: 81,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "车顶载荷", ParamValue: "300kg静态（支持车顶帐篷/行李箱）", ParamUnit: "",
			Category: "户外配置", IsPrice: false, Status: 1, Sort: 82,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "露营模式", ParamValue: "一键成床/露营哨兵模式", ParamUnit: "",
			Category: "户外配置", IsPrice: false, Status: 1, Sort: 83,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "外挂备胎", ParamValue: "可选（背负式全尺寸备胎）", ParamUnit: "",
			Category: "户外配置", IsPrice: false, Status: 1, Sort: 84,
		},
		{
			ModelID:   getModelID("adamas"),
			ParamName: "涉水深度检测", ParamValue: "支持（双后视镜探测）", ParamUnit: "",
			Category: "户外配置", IsPrice: false, Status: 1, Sort: 85,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "能源类型", ParamValue: "增程式（1.5T增程器+前后双电机）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 86,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "系统综合功率", ParamValue: "350kW（476马力）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 87,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "系统综合扭矩", ParamValue: "740N·m", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 88,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "前电机功率/扭矩", ParamValue: "150kW / 340N·m", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 89,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "后电机功率/扭矩", ParamValue: "200kW / 400N·m", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 90,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "增程器型号", ParamValue: "新晨B15F 1.5T四缸", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 91,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "增程器热效率", ParamValue: "40.5%", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 92,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "百公里加速", ParamValue: "5.5秒", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 93,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "最高车速", ParamValue: "190km/h（电子限速）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 94,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "变速箱", ParamValue: "电动车单速变速箱", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 95,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "驱动形式", ParamValue: "双电机全时四驱", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 96,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "推荐燃油标号", ParamValue: "95号", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 97,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "长×宽×高", ParamValue: "5050×1980×1869mm", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 98,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "轴距", ParamValue: "3010mm", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 99,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "车身结构", ParamValue: "5门SUV 承载式车身（全铝底盘）", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 100,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "整备质量", ParamValue: "2545-2660kg（不同版本）", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 101,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "最大满载质量", ParamValue: "3189kg", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 102,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "油箱容积", ParamValue: "70L", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 103,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "座位数", ParamValue: "6座/7座", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 104,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "电池类型", ParamValue: "宁德时代三元锂电池", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 105,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "标准续航版电池容量", ParamValue: "44.5kWh", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 106,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "长续航版电池容量", ParamValue: "58.4kWh", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 107,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "CLTC纯电续航（标准续航）", ParamValue: "215km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 108,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "CLTC纯电续航（长续航）", ParamValue: "306km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 109,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "WLTC纯电续航（长续航）", ParamValue: "252km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 110,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "CLTC综合续航（长续航）", ParamValue: "1362km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 111,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "直流快充峰值功率", ParamValue: "100kW", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 112,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "快充时间（20%-80%）", ParamValue: "约24分钟", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 113,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "对外放电功率", ParamValue: "2.2kW-4.4kW", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 114,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "馈电油耗（WLTC）", ParamValue: "7.95L/100km（长续航版）", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 115,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "前悬架类型", ParamValue: "全铝合金双叉臂式独立悬架", ParamUnit: "",
			Category: "底盘悬挂", IsPrice: false, Status: 1, Sort: 116,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "后悬架类型", ParamValue: "全铝合金H臂多连杆独立悬架", ParamUnit: "",
			Category: "底盘悬挂", IsPrice: false, Status: 1, Sort: 117,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "副车架材质", ParamValue: "全铝合金", ParamUnit: "",
			Category: "底盘悬挂", IsPrice: false, Status: 1, Sort: 118,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "DCC可变阻尼", ParamValue: "全系标配（毫秒级响应）", ParamUnit: "",
			Category: "底盘悬挂", IsPrice: false, Status: 1, Sort: 119,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "空气悬架", ParamValue: "长续航版标配（标准续航版无）", ParamUnit: "",
			Category: "底盘悬挂", IsPrice: false, Status: 1, Sort: 120,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "轮胎规格", ParamValue: "275/45 R21（可选AT胎）", ParamUnit: "",
			Category: "底盘悬挂", IsPrice: false, Status: 1, Sort: 121,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "最小离地间隙", ParamValue: "205mm（满载）", ParamUnit: "",
			Category: "越野参数", IsPrice: false, Status: 1, Sort: 122,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "接近角", ParamValue: "22.2°", ParamUnit: "",
			Category: "越野参数", IsPrice: false, Status: 1, Sort: 123,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "离去角", ParamValue: "25.1°", ParamUnit: "",
			Category: "越野参数", IsPrice: false, Status: 1, Sort: 124,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "纵向通过角", ParamValue: "19.7°", ParamUnit: "",
			Category: "越野参数", IsPrice: false, Status: 1, Sort: 125,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "最大涉水深度", ParamValue: "700mm", ParamUnit: "",
			Category: "越野参数", IsPrice: false, Status: 1, Sort: 126,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "最大爬坡度", ParamValue: "100%（45°）", ParamUnit: "",
			Category: "越野参数", IsPrice: false, Status: 1, Sort: 127,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "全地形模式数量", ParamValue: "5+种", ParamUnit: "",
			Category: "越野参数", IsPrice: false, Status: 1, Sort: 128,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "四驱形式", ParamValue: "双电机全时四驱（150ms电子限滑）", ParamUnit: "",
			Category: "越野参数", IsPrice: false, Status: 1, Sort: 129,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "标准智驾芯片", ParamValue: "德州仪器TDA4", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 130,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "高阶智驾芯片", ParamValue: "双英伟达Orin-X（508TOPS）", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 131,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "激光雷达", ParamValue: "3颗禾赛纯固态激光雷达（高阶版）", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 132,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "毫米波雷达", ParamValue: "5个", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 133,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "摄像头数量", ParamValue: "12个", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 134,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "超声波雷达", ParamValue: "12个", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 135,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "标准智驾等级", ParamValue: "L2级", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 136,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "高阶智驾功能", ParamValue: "高速NOA/城市领航/全地形智能穿越（选装）", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 137,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "车机芯片", ParamValue: "高通骁龙8155", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 138,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "中控屏幕", ParamValue: "15.7英寸3K悬浮触控屏", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 139,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "仪表盘", ParamValue: "12.3英寸全液晶仪表", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 140,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "后排娱乐屏", ParamValue: "15.6英寸高清娱乐屏（全系标配）", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 141,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "流媒体后视镜", ParamValue: "9英寸（尊享版标配）", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 142,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "屏数", ParamValue: "4屏联动", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 143,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "语音助手", ParamValue: "智能语音交互系统", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 144,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "系统", ParamValue: "ROX OS智能座舱系统", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 145,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "安全气囊", ParamValue: "7-10个（不同版本）", ParamUnit: "",
			Category: "安全配置", IsPrice: false, Status: 1, Sort: 146,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "车身高强钢占比", ParamValue: "87%", ParamUnit: "",
			Category: "安全配置", IsPrice: false, Status: 1, Sort: 147,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "热成型钢占比", ParamValue: "32%+", ParamUnit: "",
			Category: "安全配置", IsPrice: false, Status: 1, Sort: 148,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "顶压峰值载荷", ParamValue: "160000N+", ParamUnit: "",
			Category: "安全配置", IsPrice: false, Status: 1, Sort: 149,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "整车扭转刚度", ParamValue: "40000+ N·m/deg", ParamUnit: "",
			Category: "安全配置", IsPrice: false, Status: 1, Sort: 150,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "CIRI碰撞评级", ParamValue: "G级（优秀）", ParamUnit: "",
			Category: "安全配置", IsPrice: false, Status: 1, Sort: 151,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "电池防护等级", ParamValue: "IP68级防水防尘", ParamUnit: "",
			Category: "安全配置", IsPrice: false, Status: 1, Sort: 152,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "座椅材质", ParamValue: "高级真皮", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 153,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "主驾座椅调节", ParamValue: "14向电动（含腿托+腰托）", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 154,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "副驾座椅调节", ParamValue: "10向电动", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 155,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "前排座椅加热", ParamValue: "标配", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 156,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "前排座椅通风", ParamValue: "标配", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 157,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "二排座椅（6座版）", ParamValue: "零重力航空座椅/12向调节/按摩/加热/通风", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 158,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "空调系统", ParamValue: "三区恒温自动空调", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 159,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "氛围灯", ParamValue: "256色", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 160,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "电动尾门", ParamValue: "侧开式电吸尾门", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 161,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "尾门餐吧", ParamValue: "首创尾门餐厨系统（可选装）", ParamUnit: "",
			Category: "户外配置", IsPrice: false, Status: 1, Sort: 162,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "车载天幕", ParamValue: "L型快搭车载天幕（可选装）", ParamUnit: "",
			Category: "户外配置", IsPrice: false, Status: 1, Sort: 163,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "露营模式", ParamValue: "大床模式（二三排全平放倒）", ParamUnit: "",
			Category: "户外配置", IsPrice: false, Status: 1, Sort: 164,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "车顶行李架", ParamValue: "纵向行李架标配", ParamUnit: "",
			Category: "户外配置", IsPrice: false, Status: 1, Sort: 165,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "外挂备胎", ParamValue: "可选", ParamUnit: "",
			Category: "户外配置", IsPrice: false, Status: 1, Sort: 166,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "AT轮胎", ParamValue: "20寸AT胎可选", ParamUnit: "",
			Category: "户外配置", IsPrice: false, Status: 1, Sort: 167,
		},
		{
			ModelID:   getModelID("rox01"),
			ParamName: "对外放电", ParamValue: "2.2kW-4.4kW", ParamUnit: "",
			Category: "户外配置", IsPrice: false, Status: 1, Sort: 168,
		},
		{
			ModelID:   getModelID("tank300"),
			ParamName: "能源类型", ParamValue: "插电式混合动力（2.0T+P2电机）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 169,
		},
		{
			ModelID:   getModelID("tank300"),
			ParamName: "系统综合功率", ParamValue: "300kW（408马力）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 170,
		},
		{
			ModelID:   getModelID("tank300"),
			ParamName: "系统综合扭矩", ParamValue: "750N·m", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 171,
		},
		{
			ModelID:   getModelID("tank300"),
			ParamName: "百公里加速", ParamValue: "6.7秒", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 172,
		},
		{
			ModelID:   getModelID("tank300"),
			ParamName: "变速箱", ParamValue: "9HAT混动专用变速箱", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 173,
		},
		{
			ModelID:   getModelID("tank300"),
			ParamName: "长×宽×高", ParamValue: "4750×1930×1903mm", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 174,
		},
		{
			ModelID:   getModelID("tank300"),
			ParamName: "轴距", ParamValue: "2750mm", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 175,
		},
		{
			ModelID:   getModelID("tank300"),
			ParamName: "车身结构", ParamValue: "非承载式车身（带大梁）", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 176,
		},
		{
			ModelID:   getModelID("tank300"),
			ParamName: "电池容量", ParamValue: "37.1kWh", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 177,
		},
		{
			ModelID:   getModelID("tank300"),
			ParamName: "WLTC纯电续航", ParamValue: "105km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 178,
		},
		{
			ModelID:   getModelID("tank300"),
			ParamName: "馈电油耗", ParamValue: "8.8L/100km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 179,
		},
		{
			ModelID:   getModelID("tank300"),
			ParamName: "前悬架", ParamValue: "双叉臂式独立悬架", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 180,
		},
		{
			ModelID:   getModelID("tank300"),
			ParamName: "后悬架", ParamValue: "整体桥非独立悬架", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 181,
		},
		{
			ModelID:   getModelID("tank300"),
			ParamName: "四驱形式", ParamValue: "分时四驱（机械）+三把锁", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 182,
		},
		{
			ModelID:   getModelID("tank300"),
			ParamName: "最小离地间隙", ParamValue: "224mm", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 183,
		},
		{
			ModelID:   getModelID("tank300"),
			ParamName: "接近角/离去角", ParamValue: "33° / 30°", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 184,
		},
		{
			ModelID:   getModelID("tank300"),
			ParamName: "车机芯片", ParamValue: "高通骁龙8155", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 185,
		},
		{
			ModelID:   getModelID("tank300"),
			ParamName: "中控屏幕", ParamValue: "14.6英寸触控屏", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 186,
		},
		{
			ModelID:   getModelID("tank300"),
			ParamName: "对外放电", ParamValue: "6kW", ParamUnit: "",
			Category: "户外配置", IsPrice: false, Status: 1, Sort: 187,
		},
		{
			ModelID:   getModelID("tank400"),
			ParamName: "能源类型", ParamValue: "插电式混合动力（2.0T+电机）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 188,
		},
		{
			ModelID:   getModelID("tank400"),
			ParamName: "系统综合功率", ParamValue: "300kW（408马力）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 189,
		},
		{
			ModelID:   getModelID("tank400"),
			ParamName: "系统综合扭矩", ParamValue: "750N·m", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 190,
		},
		{
			ModelID:   getModelID("tank400"),
			ParamName: "变速箱", ParamValue: "9HAT变速箱", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 191,
		},
		{
			ModelID:   getModelID("tank400"),
			ParamName: "长×宽×高", ParamValue: "4964×1970×1905mm", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 192,
		},
		{
			ModelID:   getModelID("tank400"),
			ParamName: "轴距", ParamValue: "2850mm", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 193,
		},
		{
			ModelID:   getModelID("tank400"),
			ParamName: "车身结构", ParamValue: "非承载式车身", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 194,
		},
		{
			ModelID:   getModelID("tank400"),
			ParamName: "电池容量", ParamValue: "37.1kWh", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 195,
		},
		{
			ModelID:   getModelID("tank400"),
			ParamName: "WLTC纯电续航", ParamValue: "105km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 196,
		},
		{
			ModelID:   getModelID("tank400"),
			ParamName: "前悬架", ParamValue: "双叉臂式独立悬架", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 197,
		},
		{
			ModelID:   getModelID("tank400"),
			ParamName: "后悬架", ParamValue: "整体桥非独立悬架", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 198,
		},
		{
			ModelID:   getModelID("tank400"),
			ParamName: "四驱形式", ParamValue: "分时四驱+前后桥差速锁", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 199,
		},
		{
			ModelID:   getModelID("tank400"),
			ParamName: "最小离地间隙", ParamValue: "224mm", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 200,
		},
		{
			ModelID:   getModelID("tank400"),
			ParamName: "接近角/离去角", ParamValue: "33° / 31°", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 201,
		},
		{
			ModelID:   getModelID("tank400"),
			ParamName: "最大涉水深度", ParamValue: "800mm", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 202,
		},
		{
			ModelID:   getModelID("tank400"),
			ParamName: "车机芯片", ParamValue: "高通骁龙8155", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 203,
		},
		{
			ModelID:   getModelID("tank400"),
			ParamName: "中控屏幕", ParamValue: "15.6英寸2.5K屏", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 204,
		},
		{
			ModelID:   getModelID("tank400"),
			ParamName: "车载冰箱", ParamValue: "5.4L冷暖冰箱", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 205,
		},
		{
			ModelID:   getModelID("tank400"),
			ParamName: "对外放电", ParamValue: "6kW", ParamUnit: "",
			Category: "户外配置", IsPrice: false, Status: 1, Sort: 206,
		},
		{
			ModelID:   getModelID("tank500"),
			ParamName: "能源类型", ParamValue: "插电式混合动力（2.0T+电机）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 207,
		},
		{
			ModelID:   getModelID("tank500"),
			ParamName: "系统综合功率", ParamValue: "300kW（408马力）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 208,
		},
		{
			ModelID:   getModelID("tank500"),
			ParamName: "系统综合扭矩", ParamValue: "750N·m", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 209,
		},
		{
			ModelID:   getModelID("tank500"),
			ParamName: "百公里加速", ParamValue: "6.9秒", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 210,
		},
		{
			ModelID:   getModelID("tank500"),
			ParamName: "变速箱", ParamValue: "9HAT变速箱", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 211,
		},
		{
			ModelID:   getModelID("tank500"),
			ParamName: "长×宽×高", ParamValue: "5078×1934×1905mm", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 212,
		},
		{
			ModelID:   getModelID("tank500"),
			ParamName: "轴距", ParamValue: "2850mm", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 213,
		},
		{
			ModelID:   getModelID("tank500"),
			ParamName: "车身结构", ParamValue: "非承载式车身", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 214,
		},
		{
			ModelID:   getModelID("tank500"),
			ParamName: "电池容量", ParamValue: "37.1kWh", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 215,
		},
		{
			ModelID:   getModelID("tank500"),
			ParamName: "WLTC纯电续航", ParamValue: "110km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 216,
		},
		{
			ModelID:   getModelID("tank500"),
			ParamName: "前悬架", ParamValue: "双叉臂式独立悬架", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 217,
		},
		{
			ModelID:   getModelID("tank500"),
			ParamName: "后悬架", ParamValue: "整体桥非独立悬架", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 218,
		},
		{
			ModelID:   getModelID("tank500"),
			ParamName: "四驱形式", ParamValue: "分时四驱+机械锁止", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 219,
		},
		{
			ModelID:   getModelID("tank500"),
			ParamName: "最小离地间隙", ParamValue: "213mm", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 220,
		},
		{
			ModelID:   getModelID("tank500"),
			ParamName: "接近角/离去角", ParamValue: "30° / 24°", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 221,
		},
		{
			ModelID:   getModelID("tank500"),
			ParamName: "智驾系统", ParamValue: "Coffee Pilot（咖啡智驾）", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 222,
		},
		{
			ModelID:   getModelID("tank500"),
			ParamName: "座椅材质", ParamValue: "Nappa真皮", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 223,
		},
		{
			ModelID:   getModelID("tank500"),
			ParamName: "音响系统", ParamValue: "丹拿音响", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 224,
		},
		{
			ModelID:   getModelID("tank700"),
			ParamName: "能源类型", ParamValue: "插电式混合动力（3.0T V6+P2电机）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 225,
		},
		{
			ModelID:   getModelID("tank700"),
			ParamName: "系统综合功率", ParamValue: "385kW（524马力）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 226,
		},
		{
			ModelID:   getModelID("tank700"),
			ParamName: "系统综合扭矩", ParamValue: "800N·m", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 227,
		},
		{
			ModelID:   getModelID("tank700"),
			ParamName: "百公里加速", ParamValue: "5.6秒", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 228,
		},
		{
			ModelID:   getModelID("tank700"),
			ParamName: "变速箱", ParamValue: "9HAT变速箱", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 229,
		},
		{
			ModelID:   getModelID("tank700"),
			ParamName: "长×宽×高", ParamValue: "5090×2061×1952mm", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 230,
		},
		{
			ModelID:   getModelID("tank700"),
			ParamName: "轴距", ParamValue: "3000mm", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 231,
		},
		{
			ModelID:   getModelID("tank700"),
			ParamName: "车身结构", ParamValue: "非承载式车身", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 232,
		},
		{
			ModelID:   getModelID("tank700"),
			ParamName: "电池容量", ParamValue: "37.1kWh", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 233,
		},
		{
			ModelID:   getModelID("tank700"),
			ParamName: "WLTC纯电续航", ParamValue: "约90km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 234,
		},
		{
			ModelID:   getModelID("tank700"),
			ParamName: "前悬架", ParamValue: "双叉臂式独立悬架", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 235,
		},
		{
			ModelID:   getModelID("tank700"),
			ParamName: "后悬架", ParamValue: "整体桥非独立悬架", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 236,
		},
		{
			ModelID:   getModelID("tank700"),
			ParamName: "四驱形式", ParamValue: "分时四驱+三把锁+可断开防倾杆", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 237,
		},
		{
			ModelID:   getModelID("tank700"),
			ParamName: "接近角/离去角", ParamValue: "37.8° / 37.6°", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 238,
		},
		{
			ModelID:   getModelID("tank700"),
			ParamName: "最大涉水深度", ParamValue: "970mm", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 239,
		},
		{
			ModelID:   getModelID("tank700"),
			ParamName: "智驾芯片", ParamValue: "NVIDIA Thor-U（700TOPS）", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 240,
		},
		{
			ModelID:   getModelID("tank700"),
			ParamName: "激光雷达", ParamValue: "1颗", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 241,
		},
		{
			ModelID:   getModelID("tank700"),
			ParamName: "音响系统", ParamValue: "哈曼卡顿16扬声器", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 242,
		},
		{
			ModelID:   getModelID("lixiangl6"),
			ParamName: "能源类型", ParamValue: "增程式（1.5T增程器+前后双电机）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 243,
		},
		{
			ModelID:   getModelID("lixiangl6"),
			ParamName: "系统综合功率", ParamValue: "300kW（408马力）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 244,
		},
		{
			ModelID:   getModelID("lixiangl6"),
			ParamName: "系统综合扭矩", ParamValue: "529N·m", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 245,
		},
		{
			ModelID:   getModelID("lixiangl6"),
			ParamName: "百公里加速", ParamValue: "5.4秒", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 246,
		},
		{
			ModelID:   getModelID("lixiangl6"),
			ParamName: "长×宽×高", ParamValue: "4925×1960×1735mm", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 247,
		},
		{
			ModelID:   getModelID("lixiangl6"),
			ParamName: "轴距", ParamValue: "2920mm", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 248,
		},
		{
			ModelID:   getModelID("lixiangl6"),
			ParamName: "车身结构", ParamValue: "承载式车身", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 249,
		},
		{
			ModelID:   getModelID("lixiangl6"),
			ParamName: "电池容量", ParamValue: "36.8kWh（Pro）/ 51kWh（Max）", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 250,
		},
		{
			ModelID:   getModelID("lixiangl6"),
			ParamName: "CLTC纯电续航", ParamValue: "212km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 251,
		},
		{
			ModelID:   getModelID("lixiangl6"),
			ParamName: "CLTC综合续航", ParamValue: "1390km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 252,
		},
		{
			ModelID:   getModelID("lixiangl6"),
			ParamName: "前悬架", ParamValue: "双叉臂式独立悬架", ParamUnit: "",
			Category: "底盘悬挂", IsPrice: false, Status: 1, Sort: 253,
		},
		{
			ModelID:   getModelID("lixiangl6"),
			ParamName: "后悬架", ParamValue: "五连杆独立悬架", ParamUnit: "",
			Category: "底盘悬挂", IsPrice: false, Status: 1, Sort: 254,
		},
		{
			ModelID:   getModelID("lixiangl6"),
			ParamName: "底盘配置", ParamValue: "CDC可变阻尼（无空悬）", ParamUnit: "",
			Category: "底盘悬挂", IsPrice: false, Status: 1, Sort: 255,
		},
		{
			ModelID:   getModelID("lixiangl6"),
			ParamName: "智驾芯片", ParamValue: "地平线6M / Thor-U（700TOPS）", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 256,
		},
		{
			ModelID:   getModelID("lixiangl6"),
			ParamName: "激光雷达", ParamValue: "标配（ATL全天候）", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 257,
		},
		{
			ModelID:   getModelID("lixiangl6"),
			ParamName: "车机芯片", ParamValue: "高通骁龙8295P", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 258,
		},
		{
			ModelID:   getModelID("lixiangl6"),
			ParamName: "中控屏幕", ParamValue: "双15.7英寸3K屏", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 259,
		},
		{
			ModelID:   getModelID("lixiangl6"),
			ParamName: "音响系统", ParamValue: "19扬声器全景声", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 260,
		},
		{
			ModelID:   getModelID("lixiangl7"),
			ParamName: "能源类型", ParamValue: "增程式（1.5T增程器+前后双电机）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 261,
		},
		{
			ModelID:   getModelID("lixiangl7"),
			ParamName: "系统综合功率", ParamValue: "330kW（449马力）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 262,
		},
		{
			ModelID:   getModelID("lixiangl7"),
			ParamName: "系统综合扭矩", ParamValue: "620N·m", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 263,
		},
		{
			ModelID:   getModelID("lixiangl7"),
			ParamName: "百公里加速", ParamValue: "5.3秒", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 264,
		},
		{
			ModelID:   getModelID("lixiangl7"),
			ParamName: "长×宽×高", ParamValue: "5050×1995×1750mm", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 265,
		},
		{
			ModelID:   getModelID("lixiangl7"),
			ParamName: "轴距", ParamValue: "3005mm", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 266,
		},
		{
			ModelID:   getModelID("lixiangl7"),
			ParamName: "车身结构", ParamValue: "承载式车身", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 267,
		},
		{
			ModelID:   getModelID("lixiangl7"),
			ParamName: "电池容量", ParamValue: "42.8kWh（Pro）/ 52.3kWh（Max）", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 268,
		},
		{
			ModelID:   getModelID("lixiangl7"),
			ParamName: "CLTC纯电续航", ParamValue: "230km / 280km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 269,
		},
		{
			ModelID:   getModelID("lixiangl7"),
			ParamName: "空气悬架", ParamValue: "标配（魔毯空悬）", ParamUnit: "",
			Category: "底盘悬挂", IsPrice: false, Status: 1, Sort: 270,
		},
		{
			ModelID:   getModelID("lixiangl7"),
			ParamName: "智驾芯片", ParamValue: "地平线6M / NVIDIA Thor-U", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 271,
		},
		{
			ModelID:   getModelID("lixiangl7"),
			ParamName: "车机芯片", ParamValue: "高通骁龙8295P", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 272,
		},
		{
			ModelID:   getModelID("lixiangl7"),
			ParamName: "屏幕配置", ParamValue: "中控+副驾+仪表 三屏", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 273,
		},
		{
			ModelID:   getModelID("lixiangl7"),
			ParamName: "特色功能", ParamValue: "皇后座（后排右侧独立电动座椅）", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 274,
		},
		{
			ModelID:   getModelID("lixiangl7"),
			ParamName: "座椅布局", ParamValue: "5座", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 275,
		},
		{
			ModelID:   getModelID("lixiangl8"),
			ParamName: "能源类型", ParamValue: "增程式（1.5T增程器+前后双电机）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 276,
		},
		{
			ModelID:   getModelID("lixiangl8"),
			ParamName: "系统综合功率", ParamValue: "330kW（449马力）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 277,
		},
		{
			ModelID:   getModelID("lixiangl8"),
			ParamName: "系统综合扭矩", ParamValue: "620N·m", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 278,
		},
		{
			ModelID:   getModelID("lixiangl8"),
			ParamName: "百公里加速", ParamValue: "5.3秒", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 279,
		},
		{
			ModelID:   getModelID("lixiangl8"),
			ParamName: "长×宽×高", ParamValue: "5080×1995×1800mm", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 280,
		},
		{
			ModelID:   getModelID("lixiangl8"),
			ParamName: "轴距", ParamValue: "3005mm", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 281,
		},
		{
			ModelID:   getModelID("lixiangl8"),
			ParamName: "车身结构", ParamValue: "承载式车身", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 282,
		},
		{
			ModelID:   getModelID("lixiangl8"),
			ParamName: "电池容量", ParamValue: "42.8kWh / 52.3kWh", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 283,
		},
		{
			ModelID:   getModelID("lixiangl8"),
			ParamName: "CLTC纯电续航", ParamValue: "210km / 280km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 284,
		},
		{
			ModelID:   getModelID("lixiangl8"),
			ParamName: "CLTC综合续航", ParamValue: "1315km / 1415km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 285,
		},
		{
			ModelID:   getModelID("lixiangl8"),
			ParamName: "空气悬架", ParamValue: "标配双腔空气弹簧+CDC", ParamUnit: "",
			Category: "底盘悬挂", IsPrice: false, Status: 1, Sort: 286,
		},
		{
			ModelID:   getModelID("lixiangl8"),
			ParamName: "智驾芯片", ParamValue: "地平线征程5 / 双Orin-X / Thor-U", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 287,
		},
		{
			ModelID:   getModelID("lixiangl8"),
			ParamName: "车机芯片", ParamValue: "高通骁龙8295P（双芯片）", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 288,
		},
		{
			ModelID:   getModelID("lixiangl8"),
			ParamName: "座椅布局", ParamValue: "6座（2+2+2）", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 289,
		},
		{
			ModelID:   getModelID("lixiangl8"),
			ParamName: "音响系统", ParamValue: "19扬声器7.3.4全景声", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 290,
		},
		{
			ModelID:   getModelID("lixiangl9"),
			ParamName: "能源类型", ParamValue: "增程式（1.5T增程器+前后双电机）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 291,
		},
		{
			ModelID:   getModelID("lixiangl9"),
			ParamName: "系统综合功率", ParamValue: "330kW（449马力）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 292,
		},
		{
			ModelID:   getModelID("lixiangl9"),
			ParamName: "系统综合扭矩", ParamValue: "620N·m", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 293,
		},
		{
			ModelID:   getModelID("lixiangl9"),
			ParamName: "百公里加速", ParamValue: "5.18秒", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 294,
		},
		{
			ModelID:   getModelID("lixiangl9"),
			ParamName: "长×宽×高", ParamValue: "5218×1998×1800mm", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 295,
		},
		{
			ModelID:   getModelID("lixiangl9"),
			ParamName: "轴距", ParamValue: "3105mm", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 296,
		},
		{
			ModelID:   getModelID("lixiangl9"),
			ParamName: "车身结构", ParamValue: "承载式车身", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 297,
		},
		{
			ModelID:   getModelID("lixiangl9"),
			ParamName: "电池容量", ParamValue: "52.3kWh", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 298,
		},
		{
			ModelID:   getModelID("lixiangl9"),
			ParamName: "CLTC纯电续航", ParamValue: "280km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 299,
		},
		{
			ModelID:   getModelID("lixiangl9"),
			ParamName: "CLTC综合续航", ParamValue: "1412km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 300,
		},
		{
			ModelID:   getModelID("lixiangl9"),
			ParamName: "空气悬架", ParamValue: "标配双腔双阀魔毯空悬+CDC", ParamUnit: "",
			Category: "底盘悬挂", IsPrice: false, Status: 1, Sort: 301,
		},
		{
			ModelID:   getModelID("lixiangl9"),
			ParamName: "智驾芯片", ParamValue: "地平线6M（128TOPS）/ Thor-U（700TOPS）", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 302,
		},
		{
			ModelID:   getModelID("lixiangl9"),
			ParamName: "激光雷达", ParamValue: "标配ATL全天候激光雷达", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 303,
		},
		{
			ModelID:   getModelID("lixiangl9"),
			ParamName: "车机芯片", ParamValue: "高通骁龙8295P（双芯片）", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 304,
		},
		{
			ModelID:   getModelID("lixiangl9"),
			ParamName: "座椅布局", ParamValue: "6座（2+2+2）", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 305,
		},
		{
			ModelID:   getModelID("lixiangl9"),
			ParamName: "音响系统", ParamValue: "27扬声器7.3.4全景声（4720W）", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 306,
		},
		{
			ModelID:   getModelID("leopard5"),
			ParamName: "能源类型", ParamValue: "插电式混合动力（DMO平台）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 307,
		},
		{
			ModelID:   getModelID("leopard5"),
			ParamName: "系统综合功率", ParamValue: "505kW（687马力）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 308,
		},
		{
			ModelID:   getModelID("leopard5"),
			ParamName: "系统综合扭矩", ParamValue: "760N·m", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 309,
		},
		{
			ModelID:   getModelID("leopard5"),
			ParamName: "百公里加速", ParamValue: "4.8秒", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 310,
		},
		{
			ModelID:   getModelID("leopard5"),
			ParamName: "长×宽×高", ParamValue: "4890×1970×1920mm", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 311,
		},
		{
			ModelID:   getModelID("leopard5"),
			ParamName: "轴距", ParamValue: "2800mm", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 312,
		},
		{
			ModelID:   getModelID("leopard5"),
			ParamName: "车身结构", ParamValue: "非承载式大梁车身", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 313,
		},
		{
			ModelID:   getModelID("leopard5"),
			ParamName: "电池容量", ParamValue: "31.8kWh / 47.8kWh（刀片电池）", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 314,
		},
		{
			ModelID:   getModelID("leopard5"),
			ParamName: "CLTC纯电续航", ParamValue: "125km / 210km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 315,
		},
		{
			ModelID:   getModelID("leopard5"),
			ParamName: "CLTC综合续航", ParamValue: "1310km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 316,
		},
		{
			ModelID:   getModelID("leopard5"),
			ParamName: "馈电油耗", ParamValue: "8.43L/100km（WLTC）", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 317,
		},
		{
			ModelID:   getModelID("leopard5"),
			ParamName: "四驱形式", ParamValue: "电四驱+三把锁（DMO）", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 318,
		},
		{
			ModelID:   getModelID("leopard5"),
			ParamName: "最小离地间隙", ParamValue: "220mm", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 319,
		},
		{
			ModelID:   getModelID("leopard5"),
			ParamName: "最大涉水深度", ParamValue: "790mm", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 320,
		},
		{
			ModelID:   getModelID("leopard5"),
			ParamName: "云辇-P", ParamValue: "可选配", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 321,
		},
		{
			ModelID:   getModelID("leopard5"),
			ParamName: "智驾系统", ParamValue: "天神之眼（华为乾崑可选）", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 322,
		},
		{
			ModelID:   getModelID("leopard5"),
			ParamName: "车机系统", ParamValue: "DiLink智能座舱", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 323,
		},
		{
			ModelID:   getModelID("leopard5"),
			ParamName: "对外放电", ParamValue: "6kW", ParamUnit: "",
			Category: "户外配置", IsPrice: false, Status: 1, Sort: 324,
		},
		{
			ModelID:   getModelID("leopard8"),
			ParamName: "能源类型", ParamValue: "插电式混合动力（DMO平台）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 325,
		},
		{
			ModelID:   getModelID("leopard8"),
			ParamName: "系统综合功率", ParamValue: "550kW（750马力）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 326,
		},
		{
			ModelID:   getModelID("leopard8"),
			ParamName: "系统综合扭矩", ParamValue: "760N·m", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 327,
		},
		{
			ModelID:   getModelID("leopard8"),
			ParamName: "百公里加速", ParamValue: "4.8秒", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 328,
		},
		{
			ModelID:   getModelID("leopard8"),
			ParamName: "长×宽×高", ParamValue: "5195×1994×1905mm", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 329,
		},
		{
			ModelID:   getModelID("leopard8"),
			ParamName: "轴距", ParamValue: "2920mm", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 330,
		},
		{
			ModelID:   getModelID("leopard8"),
			ParamName: "车身结构", ParamValue: "非承载式大梁车身", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 331,
		},
		{
			ModelID:   getModelID("leopard8"),
			ParamName: "电池容量", ParamValue: "36.8kWh（刀片电池）", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 332,
		},
		{
			ModelID:   getModelID("leopard8"),
			ParamName: "WLTC纯电续航", ParamValue: "100km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 333,
		},
		{
			ModelID:   getModelID("leopard8"),
			ParamName: "综合续航", ParamValue: "约1200km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 334,
		},
		{
			ModelID:   getModelID("leopard8"),
			ParamName: "四驱形式", ParamValue: "电四驱+三把锁", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 335,
		},
		{
			ModelID:   getModelID("leopard8"),
			ParamName: "云辇-P", ParamValue: "全系标配（140mm行程）", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 336,
		},
		{
			ModelID:   getModelID("leopard8"),
			ParamName: "最小离地间隙", ParamValue: "220mm（抬升后310mm）", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 337,
		},
		{
			ModelID:   getModelID("leopard8"),
			ParamName: "最大涉水深度", ParamValue: "890-900mm", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 338,
		},
		{
			ModelID:   getModelID("leopard8"),
			ParamName: "智驾系统", ParamValue: "华为乾崑智驾ADS（标配）", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 339,
		},
		{
			ModelID:   getModelID("leopard8"),
			ParamName: "车机系统", ParamValue: "DiLink 150智能座舱", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 340,
		},
		{
			ModelID:   getModelID("leopard8"),
			ParamName: "音响系统", ParamValue: "帝瓦雷音响", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 341,
		},
		{
			ModelID:   getModelID("leopard8"),
			ParamName: "座椅布局", ParamValue: "5座/6座/7座可选", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 342,
		},
		{
			ModelID:   getModelID("yangwangu8"),
			ParamName: "能源类型", ParamValue: "插电式混合动力（易四方）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 343,
		},
		{
			ModelID:   getModelID("yangwangu8"),
			ParamName: "系统综合功率", ParamValue: "880kW（1197马力）", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 344,
		},
		{
			ModelID:   getModelID("yangwangu8"),
			ParamName: "系统综合扭矩", ParamValue: "1280N·m", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 345,
		},
		{
			ModelID:   getModelID("yangwangu8"),
			ParamName: "百公里加速", ParamValue: "3.6秒", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 346,
		},
		{
			ModelID:   getModelID("yangwangu8"),
			ParamName: "驱动形式", ParamValue: "四电机独立驱动", ParamUnit: "",
			Category: "动力系统", IsPrice: false, Status: 1, Sort: 347,
		},
		{
			ModelID:   getModelID("yangwangu8"),
			ParamName: "长×宽×高", ParamValue: "5319×2050×1930mm", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 348,
		},
		{
			ModelID:   getModelID("yangwangu8"),
			ParamName: "轴距", ParamValue: "3050mm", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 349,
		},
		{
			ModelID:   getModelID("yangwangu8"),
			ParamName: "车身结构", ParamValue: "非承载式大梁车身", ParamUnit: "",
			Category: "车身尺寸", IsPrice: false, Status: 1, Sort: 350,
		},
		{
			ModelID:   getModelID("yangwangu8"),
			ParamName: "电池容量", ParamValue: "49.05kWh（刀片电池）", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 351,
		},
		{
			ModelID:   getModelID("yangwangu8"),
			ParamName: "CLTC纯电续航", ParamValue: "230km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 352,
		},
		{
			ModelID:   getModelID("yangwangu8"),
			ParamName: "CLTC综合续航", ParamValue: "1205km", ParamUnit: "",
			Category: "电池续航", IsPrice: false, Status: 1, Sort: 353,
		},
		{
			ModelID:   getModelID("yangwangu8"),
			ParamName: "云辇-P", ParamValue: "智能液压车身控制系统", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 354,
		},
		{
			ModelID:   getModelID("yangwangu8"),
			ParamName: "最大涉水深度", ParamValue: "1000mm（豪华版）/ 1400mm（越野版）", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 355,
		},
		{
			ModelID:   getModelID("yangwangu8"),
			ParamName: "特色功能", ParamValue: "应急浮水/坦克掉头/蟹行模式", ParamUnit: "",
			Category: "底盘越野", IsPrice: false, Status: 1, Sort: 356,
		},
		{
			ModelID:   getModelID("yangwangu8"),
			ParamName: "智驾系统", ParamValue: "高阶智驾辅助系统", ParamUnit: "",
			Category: "智能驾驶", IsPrice: false, Status: 1, Sort: 357,
		},
		{
			ModelID:   getModelID("yangwangu8"),
			ParamName: "车机系统", ParamValue: "智能座舱（5屏联动）", ParamUnit: "",
			Category: "智能座舱", IsPrice: false, Status: 1, Sort: 358,
		},
		{
			ModelID:   getModelID("yangwangu8"),
			ParamName: "座椅布局", ParamValue: "4座/5座", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 359,
		},
		{
			ModelID:   getModelID("yangwangu8"),
			ParamName: "音响系统", ParamValue: "丹拿信心系列22扬声器", ParamUnit: "",
			Category: "舒适配置", IsPrice: false, Status: 1, Sort: 360,
		},
	}

	for _, spec := range specs {
		db.DB.Create(&spec)
	}

	log.Printf("已创建 %d 条规格参数", len(specs))
}

// ============================================================
// 12. 种子竞品对比（2+组）
// ============================================================

// ============================================================
// 12. 种子竞品对比数据（58组对比）
// ============================================================

func seedCompetitorCompares() {
	var count int64
	db.DB.Model(&model.CompetitorCompare{}).Count(&count)
	if count > 0 {
		log.Println("竞品对比数据已存在，跳过")
		return
	}

	// 查找车型ID
	getModelID := func(code string) uint {
		var m model.CarModel
		if err := db.DB.Where("code = ?", code).First(&m).Error; err == nil {
			return m.ID
		}
		return 1
	}

	compares := []model.CompetitorCompare{
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克300 Hi4-T",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"价格与定位（我方优势）\", \"our_value\": \"价格高10万但级别高一档（中大型vs紧凑型），轴距长260mm空间更宽敞，6/7座布局家庭更实用，空气悬架+全铝底盘舒适性越级\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"价格与定位（对方优势）\", \"our_value\": \"\", \"their_value\": \"价格便宜10万入门门槛低，非承载式车身+三把锁越野极限更高，方盒子造型经典，坦克品牌越野认知度高\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"价格与定位（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"两者不在同一价位和级别：ADAMAS是35万中大型豪华家庭SUV，坦克300是25万紧凑型硬派越野。预算有限玩越野选坦克300，兼顾家用和豪华选ADAMAS。\"}]",
			ContainsPrice: true, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克300 Hi4-T",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"空间与实用性（我方优势）\", \"our_value\": \"车身长300mm、轴距长260mm，车内空间明显更大；提供6/7座（坦克300只有5座），家庭多人出行更方便；后备箱和储物空间更丰富\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"空间与实用性（对方优势）\", \"our_value\": \"\", \"their_value\": \"车身短灵活好开好停，方盒子造型头部空间不错，5座布局每个座位都宽敞，后备箱装载能力也不差\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"空间与实用性（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"空间差距明显——ADAMAS是中大型SUV的空间水平，坦克300是紧凑型。需要6/7座或经常满载出行选ADAMAS。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克300 Hi4-T",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"舒适性与豪华感（我方优势）\", \"our_value\": \"空气悬架+全铝底盘公路舒适性好太多，Nappa真皮+零重力座椅+车载冰箱+电吸门配置豪华，承载式车身NVH更好，长途出行更舒适\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"舒适性与豪华感（对方优势）\", \"our_value\": \"\", \"their_value\": \"非承载式车身颠簸感明显，整体桥后悬架舒适度一般，配置以实用为主，豪华感不足\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"舒适性与豪华感（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"舒适性是两者最大的差距之一。ADAMAS是豪华SUV的舒适水平，坦克300是硬派越野的工具属性。日常通勤和家庭使用ADAMAS体验好很多。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克300 Hi4-T",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"越野能力对比（我方优势）\", \"our_value\": \"空气悬架升高后离地间隙272mm比坦克300更高，双电机四驱响应150ms比机械锁更快，7种全地形模式+透明底盘+涉水检测智能化程度高\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"越野能力对比（对方优势）\", \"our_value\": \"\", \"their_value\": \"非承载式大梁车身抗扭刚性强，机械三把锁可靠性高，分时四驱结构简单耐造，接近角更大（33°vs 27.5°），改装生态成熟\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"越野能力对比（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"越野定位不同：坦克300是纯硬派越野，极限攀爬和重度越野更强；ADAMAS是智能豪华越野，轻中度越野和长途穿越更舒适智能。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克300 Hi4-T",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"续航与能耗（我方优势）\", \"our_value\": \"纯电续航更长（215km vs 105km），日常通勤纯电覆盖更多里程；增程器工作在最优工况，长途油耗更稳定；综合续航1405km更远\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"续航与能耗（对方优势）\", \"our_value\": \"\", \"their_value\": \"馈电油耗略高但差距不大，可加92号汽油（ADAMAS需95号），加油成本略低；插混结构低速也能用电\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"续航与能耗（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"ADAMAS纯电续航是坦克300的两倍，日常通勤更省。长途出行两者都需要加油，综合成本相差不大。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克300 Hi4-T",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"综合选购建议（我方优势）\", \"our_value\": \"级别更高、空间更大、配置更豪华、舒适性更好、智能化程度更高，适合需要兼顾家庭使用和偶尔越野的消费者，是可城可野的全能选择\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"综合选购建议（对方优势）\", \"our_value\": \"\", \"their_value\": \"价格更低、越野底子更硬、品牌认知度高、改装生态成熟，适合预算有限、以越野玩乐为主的硬核玩家\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"综合选购建议（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"一句话：预算25万玩越野选坦克300；预算35万想要豪华舒适+家庭实用+偶尔越野选ADAMAS。产品定位和目标人群差异很大。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克400 Hi4-T",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"空间与座椅布局（我方优势）\", \"our_value\": \"轴距长160mm（3010mm vs 2850mm），车内纵向空间更宽敞；提供6/7座布局（坦克400只有5座），家庭多人出行更实用；二排零重力座椅舒适性越级\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"空间与座椅布局（对方优势）\", \"our_value\": \"\", \"their_value\": \"车身宽度接近，5座布局每个座位都宽敞，后备箱装载空间不错，方盒子造型头部空间充裕\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"空间与座椅布局（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"核心差异在座位数——需要6/7座选ADAMAS，5座够用可以考虑坦克400。轴距差距带来的后排腿部空间差异明显。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克400 Hi4-T",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"底盘舒适性（我方优势）\", \"our_value\": \"全铝底盘+空气悬架+承载式车身，公路行驶质感和舒适性明显更好；空气悬架140mm行程可调，高速降低、越野升高、装载模式等多场景适应\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"底盘舒适性（对方优势）\", \"our_value\": \"\", \"their_value\": \"非承载式车身+整体桥后悬架，颠簸感明显，公路舒适性一般；没有空气悬架，离地间隙固定\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"底盘舒适性（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"底盘舒适性是ADAMAS的核心优势。日常城市通勤和高速长途，ADAMAS的行驶质感和舒适性远超坦克400。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克400 Hi4-T",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"越野能力（我方优势）\", \"our_value\": \"空气悬架升高后离地间隙更大（272mm vs 224mm），双电机电子四驱响应更快，7种全地形模式+智能越野辅助对新手更友好\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"越野能力（对方优势）\", \"our_value\": \"\", \"their_value\": \"非承载式车身+三把锁机械四驱越野极限更高，接近角/离去角更大（33°/31° vs 27.5°/27.9°），涉水800mm更深，硬派越野底子更好\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"越野能力（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"越野取向不同：坦克400是硬派机械越野，极限能力强；ADAMAS是智能豪华越野，通过性不错且更舒适。重度越野选坦克400，轻中度越野+舒适选ADAMAS。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克400 Hi4-T",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"智能驾驶与座舱（我方优势）\", \"our_value\": \"地平线征程6M芯片128TOPS算力更高，13颗摄像头感知更全面，高速NOA+城区LCC功能更丰富，后排娱乐屏3K分辨率更清晰\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"智能驾驶与座舱（对方优势）\", \"our_value\": \"\", \"their_value\": \"五屏联动设计有特色（仪表+中控+HUD+流媒体+后排屏），8155芯片也够流畅，咖啡智驾功能基本够用\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"智能驾驶与座舱（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"智驾硬件ADAMAS略优，但坦克400的五屏联动设计也有亮点。座舱体验各有特色，ADAMAS偏向豪华舒适，坦克400偏向科技感。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克400 Hi4-T",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"性价比与经济性（我方优势）\", \"our_value\": \"价格高5万但级别和配置高一档：多了空气悬架、全铝底盘、6/7座、零重力座椅、车载冰箱、电吸门等豪华配置，综合性价比更高\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"性价比与经济性（对方优势）\", \"our_value\": \"\", \"their_value\": \"起售价更低（28.58万 vs 34.99万），入门门槛低；非承载式车身+三把锁的越野配置也值这个价\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"性价比与经济性（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"ADAMAS比坦克400贵约6.4万，但多出的配置（空悬+全铝底盘+6/7座+冰箱+电吸门等）价值远超6万。追求豪华配置和舒适性选ADAMAS更值。\"}]",
			ContainsPrice: true, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克400 Hi4-T",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"综合选购建议（我方优势）\", \"our_value\": \"空间更大、座位更多、舒适性更好、豪华配置更全，适合以家庭使用为主、偶尔越野的消费者，是真正的全场景SUV\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"综合选购建议（对方优势）\", \"our_value\": \"\", \"their_value\": \"价格更低、越野硬实力更强、造型更个性，适合年轻玩家、越野爱好者，是硬派越野的潮流之选\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"综合选购建议（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"一句话总结：家用为主选ADAMAS，玩越野为主选坦克400。两者价位接近但定位取向差异明显，核心看用户的使用场景和需求优先级。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克500 Hi4-T 智享版",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"座椅布局与家庭实用性（我方优势）\", \"our_value\": \"提供6座/7座两种布局（坦克500只有5座），二排零重力航空座椅150°躺角+8点按摩，三排可坐成人，家庭多人出行更实用\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"座椅布局与家庭实用性（对方优势）\", \"our_value\": \"\", \"their_value\": \"5座布局每个座位空间都宽裕，座椅宽大舒适，Nappa真皮+麂皮顶篷豪华感不错，适合小家庭或个人使用\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"座椅布局与家庭实用性（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"核心差异在座位数——有6/7座需求直接选ADAMAS。5座够用的话两者各有千秋，坦克500越野更强，ADAMAS配置更全。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克500 Hi4-T 智享版",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"豪华配置丰富度（我方优势）\", \"our_value\": \"全系标配空气悬架、电吸门、车载冰箱、零重力座椅、后排娱乐屏，这些在坦克500上要么高配才有要么没有；配置性价比更高\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"豪华配置丰富度（对方优势）\", \"our_value\": \"\", \"their_value\": \"电吸门、车载冰箱等部分配置高配才有，整体配置水平也不错，但同等价格下ADAMAS配置更全面\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"豪华配置丰富度（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"35万价位ADAMAS的配置丰富度确实有优势——很多坦克500需要高配甚至选装的配置，ADAMAS都是全系标配。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克700 Hi4-T 极致版",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"价格与性价比（我方优势）\", \"our_value\": \"价格低8万（34.99万 vs 42.8万），性价比更高；多出的配置（6/7座、冰箱、电吸门）都是日常高频使用的实用配置\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"价格与性价比（对方优势）\", \"our_value\": \"\", \"their_value\": \"3.0T V6大排量动力更强，非承载式车身+三把锁越野极限更高，品牌档次感更强，定位更高端\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"价格与性价比（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"两者定位不同：ADAMAS是35万级家庭豪华SUV，坦克700是43万起的硬派越野旗舰。预算40万以上追求极致越野选坦克700。\"}]",
			ContainsPrice: true, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克700 Hi4-T 极致版",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"动力与性能（我方优势）\", \"our_value\": \"增程技术动力响应更快，电动机起步即峰值扭矩，城市驾驶更轻快；350kW/740N·m日常使用完全够用，5.5秒破百成绩也不错\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"动力与性能（对方优势）\", \"our_value\": \"\", \"their_value\": \"3.0T V6发动机底蕴更足，综合功率385kW/800N·m，高速后段加速更有底气；5.6秒破百数据接近但大排量质感不同\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"动力与性能（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"动力数据接近，但驾驶质感不同：ADAMAS是电动车的平顺轻快，坦克700是大排量发动机的厚重感。动力都够用，风格不同。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克700 Hi4-T 极致版",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"空间与实用性（我方优势）\", \"our_value\": \"轴距仅差10mm空间接近，但ADAMAS提供6/7座（坦克700只有5座），家庭实用性翻倍；承载式车身空间利用率更高\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"空间与实用性（对方优势）\", \"our_value\": \"\", \"their_value\": \"车身更宽（2061mm vs 1985mm），横向空间更好，5座布局非常宽敞，后备箱装载能力强；车身更宽更霸气\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"空间与实用性（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"空间实用性的核心差异还是座位数。5座SUV横向空间坦克700有优势，但需要6/7座只能选ADAMAS。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克700 Hi4-T 极致版",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"越野能力（我方优势）\", \"our_value\": \"空气悬架升高后通过性不错，智能越野辅助对新手友好，日常轻度越野和长途穿越完全够用\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"越野能力（对方优势）\", \"our_value\": \"\", \"their_value\": \"非承载式车身+三把锁+可断开防倾杆，越野硬实力拉满；接近角37.8°/离去角37.6°、涉水970mm都是顶级水平；3.0T低扭强脱困好\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"越野能力（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"越野能力不在一个级别：坦克700是顶级硬派越野，ADAMAS是豪华SUV的越野水平。重度越野玩家选坦克700没错。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克700 Hi4-T 极致版",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"日常驾驶舒适性（我方优势）\", \"our_value\": \"空气悬架+全铝底盘+承载式车身，城市驾驶和高速巡航舒适性更好；车身更窄更好开好停；电吸门+冰箱+零重力座椅日常体验更豪华\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"日常驾驶舒适性（对方优势）\", \"our_value\": \"\", \"their_value\": \"座椅配置也很高（按摩/加热/通风/145mm厚度），哈曼卡顿音响也不错，但非承载式车身+宽车体日常驾驶还是偏笨重\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"日常驾驶舒适性（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"城市和高速日常驾驶，ADAMAS的舒适性和便利性更好。坦克700更适合越野场景，日常通勤略显笨重。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "方程豹", CompetitorModel: "方程豹豹5 云辇旗舰版",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"价格与定位（我方优势）\", \"our_value\": \"价格高约5万但级别更高（中大型vs中型），轴距长210mm空间更大，6/7座布局更实用，空气悬架+全铝底盘舒适性越级，豪华配置更全\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"价格与定位（对方优势）\", \"our_value\": \"\", \"their_value\": \"起售价更低（23.98万 vs 34.99万），入门门槛低很多；非承载式车身+三把锁越野底子硬；比亚迪技术背书强\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"价格与定位（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"价格差10万+、级别差一级，其实不在同一竞争区间。预算25万玩越野选豹5，预算35万要豪华家用+越野选ADAMAS。\"}]",
			ContainsPrice: true, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "方程豹", CompetitorModel: "方程豹豹5 云辇旗舰版",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"动力与加速（我方优势）\", \"our_value\": \"350kW/740N·m动力足够日常使用，5.5秒破百；增程技术动力输出平顺，没有变速箱换挡顿挫\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"动力与加速（对方优势）\", \"our_value\": \"\", \"their_value\": \"505kW/760N·m动力更强，4.8秒破百加速更快；DMO电四驱越野脱困能力强；1.5T发动机参数也不弱\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"动力与加速（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"豹5动力参数确实更强、加速更快。但日常驾驶中5.5秒和4.8秒的差距感知不强，更多是账面数据差异。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "方程豹", CompetitorModel: "方程豹豹5 云辇旗舰版",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"空间与舒适性（我方优势）\", \"our_value\": \"轴距长210mm（3010mm vs 2800mm），后排腿部空间明显更宽敞；提供6/7座选择（豹5只有5座）；空气悬架+全铝底盘舒适性好很多\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"空间与舒适性（对方优势）\", \"our_value\": \"\", \"their_value\": \"5座布局空间够用，非承载式车身颠簸感明显；座椅配置也有加热通风，但整体舒适性和ADAMAS有差距\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"空间与舒适性（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"空间和舒适性ADAMAS优势明显——毕竟级别高一级。家庭用户、注重乘坐舒适性选ADAMAS。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "方程豹", CompetitorModel: "方程豹豹5 云辇旗舰版",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"越野能力对比（我方优势）\", \"our_value\": \"空气悬架升高后离地间隙272mm比豹5的220mm更高，电池包离地324mm保护更好，智能越野辅助对新手更友好\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"越野能力对比（对方优势）\", \"our_value\": \"\", \"their_value\": \"非承载式大梁车身刚性强，三把机械锁可靠性高，云辇-P版本也有液压悬架可调，DMO电四驱脱困能力强，越野改装生态好\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"越野能力对比（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"越野取向不同：豹5是专业硬派越野底子，极限能力强；ADAMAS是豪华智能越野，通过性不错但更偏舒适。重度越野选豹5，轻中度越野+舒适选ADAMAS。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "方程豹", CompetitorModel: "方程豹豹5 云辇旗舰版",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"品牌与售后（我方优势）\", \"our_value\": \"全球40+国家上市，海外市场认可度高，产品经过全球市场验证；增程技术路线简单可靠，保养成本低\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"品牌与售后（对方优势）\", \"our_value\": \"\", \"their_value\": \"比亚迪旗下品牌，技术背书强，渠道网点密，售后有保障；豹5销量大保有量高，保值率相对更好\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"品牌与售后（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"品牌和渠道比亚迪方程豹有明显优势，这是购车的重要考量。极石在渠道建设上还需要加速。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "方程豹", CompetitorModel: "方程豹豹8 智勇旗舰版",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"座椅布局与空间利用率（我方优势）\", \"our_value\": \"轴距长90mm（3010mm vs 2920mm），承载式车身空间利用率更高；二排零重力座椅150°躺角体验更出色，三排空间更宽裕\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"座椅布局与空间利用率（对方优势）\", \"our_value\": \"\", \"their_value\": \"车身更长更宽外观更霸气，可选5/6/7座布局灵活，非承载式车身改装潜力大，方盒子造型头部空间不错\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"座椅布局与空间利用率（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"实际乘坐空间ADAMAS略优，尤其是第三排。豹8的优势是外观气场和越野硬实力，座位选择也灵活。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "理想", CompetitorModel: "理想L8 Pro",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"定位与取向差异（我方优势）\", \"our_value\": \"硬派越野SUV+增程，既能城市通勤又能越野撒野，场景覆盖更全面；方盒子造型更有个性，户外属性更强\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"定位与取向差异（对方优势）\", \"our_value\": \"\", \"their_value\": \"城市家庭SUV+增程，主打舒适家用，城市通勤和长途自驾舒适性出色；品牌认知度高，家庭用户首选\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"定位与取向差异（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"最大的差异在越野和户外能力：ADAMAS是可城可野的全地形SUV，理想L8是纯城市取向的家庭SUV。需要越野选ADAMAS，纯家用选L8。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "理想", CompetitorModel: "理想L8 Pro",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"越野与通过性（我方优势）\", \"our_value\": \"双电机全时四驱+7种全地形模式+空气悬架升高，越野能力远超城市SUV；272mm离地间隙+770mm涉水+45°爬坡，烂路烂泥都能走\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"越野与通过性（对方优势）\", \"our_value\": \"\", \"their_value\": \"城市SUV底盘，离地间隙低，没有越野模式，只能走铺装路面；双电机四驱仅提升雨雪天行驶稳定性，不能越野\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"越野与通过性（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"越野能力是核心差异——ADAMAS可以带你去理想L8去不了的地方。喜欢露营、越野、走非铺装路的用户，ADAMAS是更好的选择。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "理想", CompetitorModel: "理想L8 Pro",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"配置丰富度对比（我方优势）\", \"our_value\": \"全系标配空气悬架、电吸门、车载冰箱、零重力座椅、后排娱乐屏，这些在L8上要么高配才有要么没有；尾门餐吧+外放电户外配置更全\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"配置丰富度对比（对方优势）\", \"our_value\": \"\", \"their_value\": \"副驾娱乐屏是特色，理想同学语音交互体验好，全车座椅加热通风按摩，舒适性配置也很高；车机生态更完善\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"配置丰富度对比（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"配置各有特色：ADAMAS豪华配置更全（空悬全系标、电吸门、冰箱），理想L8智能体验更好（语音、副驾屏、车机生态）。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "理想", CompetitorModel: "理想L8 Pro",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"智能驾驶与座舱（我方优势）\", \"our_value\": \"地平线征程6M芯片128TOPS算力，高速NOA+城区LCC够用；后排娱乐屏更大更清晰；小石头AI语音支持多轮对话\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"智能驾驶与座舱（对方优势）\", \"our_value\": \"\", \"their_value\": \"理想AD智驾体验成熟，高速和城区NOA口碑好；双8295P芯片+多屏交互流畅；理想同学响应快识别准；车机应用生态更丰富\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"智能驾驶与座舱（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"智驾和座舱体验理想明显更成熟，这是理想的核心优势。ADAMAS的硬件不差，但软件成熟度和生态完善度还有提升空间。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "理想", CompetitorModel: "理想L8 Pro",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"续航与经济性（我方优势）\", \"our_value\": \"电池44.5kWh，CLTC纯电215km，与L8 Pro版接近；综合续航1405km更远（L8是1315km）；增程器42%热效率更高\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"续航与经济性（对方优势）\", \"our_value\": \"\", \"their_value\": \"增程器技术成熟稳定，油耗控制口碑好；智能能耗管理优化到位；全国超充网络布局快，补能便利性更好\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"续航与经济性（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"续航数据接近，都是增程技术的优秀代表。纯电续航日常通勤都够用，长途都靠加油。补能网络理想有优势。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "理想", CompetitorModel: "理想L8 Pro",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"综合选购建议（我方优势）\", \"our_value\": \"有越野能力、户外配置丰富、造型个性、豪华配置全系标配，适合喜欢户外生活、偶尔越野、追求个性的家庭用户\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"综合选购建议（对方优势）\", \"our_value\": \"\", \"their_value\": \"智驾和座舱体验成熟、品牌力强、渠道多售后方便、产品经过充分验证，适合纯城市家用、看重品牌和智驾的用户\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"综合选购建议（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"一句话：90%场景在城市选理想L8，10%以上场景需要越野/户外/走烂路选ADAMAS。两款都是好车，取向不同而已。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "仰望", CompetitorModel: "仰望U8 豪华版",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"价格与性价比（我方优势）\", \"our_value\": \"价格仅为仰望U8的1/3（35万 vs 100万），但核心体验（增程、四驱、越野、豪华配置）都能享受到，性价比极高\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"价格与性价比（对方优势）\", \"our_value\": \"\", \"their_value\": \"百万级定位，四电机独立驱动技术天花板，品牌定位高端，产品力拉满，是中国品牌顶级技术的代表\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"价格与性价比（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"完全不是一个价位的产品。预算百万追求极致技术和品牌选U8，预算35万想要豪华越野全能体验选ADAMAS。\"}]",
			ContainsPrice: true, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "仰望", CompetitorModel: "仰望U8 豪华版",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"技术与性能（我方优势）\", \"our_value\": \"增程+双电机四驱技术成熟可靠，维护成本低；350kW/740N·m日常使用完全够用；空气悬架+全铝底盘舒适性好\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"技术与性能（对方优势）\", \"our_value\": \"\", \"their_value\": \"四电机独立驱动（易四方）是行业天花板，880kW/1280N·m动力怪兽，3.6秒破百；云辇-P液压悬架技术先进；应急浮水等黑科技\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"技术与性能（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"技术上仰望U8确实领先不止一个档次，四电机的能力（原地掉头、应急浮水、蟹行模式等）是双电机做不到的。但日常使用中ADAMAS的动力也完全够用。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "仰望", CompetitorModel: "仰望U8 豪华版",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"越野能力对比（我方优势）\", \"our_value\": \"中汽研L3级越野认证，轻中度越野和长途穿越完全够用；智能越野辅助对新手友好；空气悬架可调提升通过性\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"越野能力对比（对方优势）\", \"our_value\": \"\", \"their_value\": \"越野能力天花板级：涉水1000mm（越野版1400mm）、云辇-P可调行程大、四电机独立控制脱困能力极强、坦克掉头、蟹行模式\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"越野能力对比（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"越野能力差距巨大——U8是百万级硬派越野天花板，ADAMAS是35万级的优秀水平。不同预算和需求下的选择。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("adamas"),
			CompetitorBrand: "仰望", CompetitorModel: "仰望U8 豪华版",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"日常使用成本（我方优势）\", \"our_value\": \"35万的车价，保养便宜，保险费用低，日常使用成本可控；增程结构简单故障率低\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"日常使用成本（对方优势）\", \"our_value\": \"\", \"their_value\": \"百万级车价，保险贵，保养维修成本高；四电机+液压悬架等复杂技术，长期使用可靠性和维修成本是未知数\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"日常使用成本（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"日常使用成本ADAMAS低得多。百万级豪车的使用成本和35万家用SUV完全不在一个量级。\"}]",
			ContainsPrice: true, Status: 1,
		},
		{
			OurModelID:      getModelID("rox01"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克300 Hi4-T",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"价格与定位（我方优势）\", \"our_value\": \"价格高5万（29.99万 vs 24.98万）但级别高一档（中大型vs紧凑型），轴距长260mm空间更大，6/7座布局家庭更实用\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"价格与定位（对方优势）\", \"our_value\": \"\", \"their_value\": \"价格更低入门门槛低，非承载式车身+三把锁越野极限更高，坦克品牌越野认知度强，改装生态成熟\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"价格与定位（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"级别差一级，目标人群不同。预算25万玩越野选坦克300，预算30万要兼顾家庭实用选极石01。\"}]",
			ContainsPrice: true, Status: 1,
		},
		{
			OurModelID:      getModelID("rox01"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克300 Hi4-T",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"空间与实用性（我方优势）\", \"our_value\": \"车身长300mm、轴距长260mm，车内空间明显更大；6/7座布局（坦克300只有5座）适合多孩家庭；后备箱空间更大\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"空间与实用性（对方优势）\", \"our_value\": \"\", \"their_value\": \"车身短小灵活，城市驾驶和停车方便；方盒子造型头部空间不错；5座布局前排和后排都够用\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"空间与实用性（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"空间差距是级别差异带来的。家庭用户、需要6/7座直接选极石01。单身或小两口玩越野选坦克300。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("rox01"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克300 Hi4-T",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"舒适性对比（我方优势）\", \"our_value\": \"全铝底盘+承载式车身公路舒适性好，长续航版还有空气悬架；真皮座椅+加热通风按摩；整车NVH更好\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"舒适性对比（对方优势）\", \"our_value\": \"\", \"their_value\": \"非承载式车身+整体桥后悬架，颠簸感明显，长途乘坐容易累；座椅偏硬，整体舒适性一般\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"舒适性对比（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"舒适性差距是最明显的。极石01是家庭SUV的舒适水平，坦克300是硬派越野的工具属性。日常通勤家用选极石01。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("rox01"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克300 Hi4-T",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"续航与能耗（我方优势）\", \"our_value\": \"纯电续航更长（215km/306km vs 105km），日常通勤基本不用油；增程技术油耗稳定；综合续航1275-1362km\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"续航与能耗（对方优势）\", \"our_value\": \"\", \"their_value\": \"馈电油耗8.8L，可加92号汽油；插混结构也能纯电通勤，但续航短需要更频繁充电\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"续航与能耗（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"纯电续航极石01是坦克300的2-3倍，日常使用成本更低。有家充桩的话优势更明显。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("rox01"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克300 Hi4-T",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"智能化水平（我方优势）\", \"our_value\": \"长续航版可选激光雷达+双Orin-X高阶智驾，智驾硬件天花板高；高通8155芯片+15.7英寸3K大屏+后排娱乐屏\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"智能化水平（对方优势）\", \"our_value\": \"\", \"their_value\": \"8155芯片+14.6英寸中控屏，基础智驾够用，车机流畅度不错；但高阶智驾能力有限\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"智能化水平（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"智驾硬件极石01高阶版有优势，激光雷达+508TOPS算力是30万级少见的配置。基础版两者水平接近。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("rox01"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克300 Hi4-T",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"越野能力对比（我方优势）\", \"our_value\": \"双电机全时四驱+150ms电子限滑响应快，5+种全地形模式，长续航版有空气悬架升高通过性更好；智能越野辅助有特色\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"越野能力对比（对方优势）\", \"our_value\": \"\", \"their_value\": \"非承载式大梁+机械三把锁，越野硬实力强，可靠性高；接近角33°更大；改装生态成熟配件多\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"越野能力对比（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"越野底子坦克300更强，这是硬派越野的本职工作。极石01的越野能力应对轻中度越野和长途穿越也完全够用。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("rox01"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克400 Hi4-T",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"性价比对比（我方优势）\", \"our_value\": \"起售价仅高1.4万（29.99万 vs 28.58万）但级别更高（轴距长160mm）、6/7座可选、全铝底盘、配置更全，性价比更高\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"性价比对比（对方优势）\", \"our_value\": \"\", \"their_value\": \"非承载式车身+三把锁越野配置值这个价；造型个性独特；价格也有竞争力\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"性价比对比（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"同价位极石01的空间、配置、实用性更有优势。坦克400的优势是越野硬实力和个性外观。\"}]",
			ContainsPrice: true, Status: 1,
		},
		{
			OurModelID:      getModelID("rox01"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克400 Hi4-T",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"空间与座椅（我方优势）\", \"our_value\": \"轴距长160mm（3010mm vs 2850mm），后排腿部空间更宽敞；6/7座布局家庭实用（坦克400只有5座）；二三排放倒可露营大床\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"空间与座椅（对方优势）\", \"our_value\": \"\", \"their_value\": \"5座布局空间也够用，车身宽度大横向空间不错，后备箱装载能力强，方盒子造型储物灵活\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"空间与座椅（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"家庭多人出行选极石01，5座够用且喜欢越野选坦克400。座位数是核心差异点。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("rox01"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克400 Hi4-T",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"续航与能耗（我方优势）\", \"our_value\": \"纯电续航215-306km（坦克400仅105km），日常通勤更省；长续航版58.4kWh大电池一周充一次电足够\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"续航与能耗（对方优势）\", \"our_value\": \"\", \"their_value\": \"馈电油耗尚可，可加92号油；插混技术成熟可靠；油箱70-77L\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"续航与能耗（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"纯电续航差距2-3倍，日常使用成本差异明显。有家充桩极石01长续航版基本可以当纯电车开。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("rox01"),
			CompetitorBrand: "坦克", CompetitorModel: "坦克400 Hi4-T",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"智能驾驶与座舱（我方优势）\", \"our_value\": \"高阶版可选双Orin-X（508TOPS）+3颗激光雷达，硬件配置拉满；全地形智能穿越功能是越野场景的加分项\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"智能驾驶与座舱（对方优势）\", \"our_value\": \"\", \"their_value\": \"五屏联动设计有特色，8155芯片流畅；咖啡智驾基本够用；后排娱乐屏高配才有\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"智能驾驶与座舱（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"智驾硬件极石01高阶版明显更强，激光雷达+508TOPS在30万级很有竞争力。基础智驾两者接近。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("rox01"),
			CompetitorBrand: "方程豹", CompetitorModel: "方程豹豹5 210KM领航版",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"价格与性价比（我方优势）\", \"our_value\": \"同价位（26-30万）极石01级别更高、空间更大、6/7座更实用、增程技术长途无忧；性价比突出\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"价格与性价比（对方优势）\", \"our_value\": \"\", \"their_value\": \"起售价更低（23.98万起），非承载式车身+三把锁越野硬实力强；比亚迪DMO技术背书强\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"价格与性价比（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"入门价豹5更低，但同价位对比极石01的空间和配置优势明显。看预算和需求。\"}]",
			ContainsPrice: true, Status: 1,
		},
		{
			OurModelID:      getModelID("rox01"),
			CompetitorBrand: "方程豹", CompetitorModel: "方程豹豹5 210KM领航版",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"空间与家庭实用性（我方优势）\", \"our_value\": \"轴距长210mm（3010mm vs 2800mm），空间大一级；6/7座布局（豹5只有5座）适合多孩家庭；承载式车身空间利用率高\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"空间与家庭实用性（对方优势）\", \"our_value\": \"\", \"their_value\": \"5座布局够用，方盒子造型头部空间不错；后备箱空间也挺大，但整体空间和极石01有级别差距\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"空间与家庭实用性（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"家庭用户、需要6/7座选极石01。两个人或小家庭、注重越野选豹5。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("rox01"),
			CompetitorBrand: "方程豹", CompetitorModel: "方程豹豹5 210KM领航版",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"动力与加速（我方优势）\", \"our_value\": \"350kW/740N·m动力充足，5.5秒破百；增程技术动力输出平顺，没有变速箱顿挫\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"动力与加速（对方优势）\", \"our_value\": \"\", \"their_value\": \"505kW/760N·m动力更强，4.8秒破百加速更快；DMO电四驱脱困能力强\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"动力与加速（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"动力参数豹5更强、加速更快。但日常驾驶中两者都够用，0.7秒的差距感知不强。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("rox01"),
			CompetitorBrand: "方程豹", CompetitorModel: "方程豹豹5 210KM领航版",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"越野能力（我方优势）\", \"our_value\": \"双电机全时四驱+电子限滑响应快，长续航版空气悬架提升通过性；智能越野辅助功能实用；电池包离地高保护好\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"越野能力（对方优势）\", \"our_value\": \"\", \"their_value\": \"非承载式大梁+三把锁+DMO电四驱，越野硬实力强；可选云辇-P液压悬架；越野改装生态好\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"越野能力（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"越野硬实力豹5更强，但极石01长续航版配空气悬架后通过性也不错。重度越野选豹5，轻中度+舒适选极石01。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("rox01"),
			CompetitorBrand: "方程豹", CompetitorModel: "方程豹豹5 210KM领航版",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"舒适性与日常驾驶（我方优势）\", \"our_value\": \"全铝底盘+承载式车身公路舒适性好；长续航版有空气悬架；座椅配置高（按摩/加热/通风）；整车NVH更好\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"舒适性与日常驾驶（对方优势）\", \"our_value\": \"\", \"their_value\": \"非承载式车身颠簸感明显，整体舒适性一般；座椅也有加热通风，但底盘质感和ADAMAS有差距\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"舒适性与日常驾驶（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"日常驾驶和乘坐舒适性极石01明显更好。经常跑长途、注重乘坐质感选极石01。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("rox01"),
			CompetitorBrand: "方程豹", CompetitorModel: "方程豹豹5 210KM领航版",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"品牌与售后（我方优势）\", \"our_value\": \"全球40+国家上市，海外市场表现亮眼；产品经过全球市场验证；5年免费保养权益超值\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"品牌与售后（对方优势）\", \"our_value\": \"\", \"their_value\": \"比亚迪旗下品牌，渠道网点密，售后方便；豹5销量大保有量高；品牌认知度更高\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"品牌与售后（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"品牌和渠道豹5有比亚迪背书优势。极石在国内渠道建设还在加速，但海外市场表现不错。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("rox01"),
			CompetitorBrand: "理想", CompetitorModel: "理想L6 Max",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"定位与场景（我方优势）\", \"our_value\": \"增程SUV+越野能力，可城可野场景更全面；6/7座布局（L6只有5座）家庭多人出行更方便；户外露营配置丰富\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"定位与场景（对方优势）\", \"our_value\": \"\", \"their_value\": \"城市家庭SUV，主打舒适家用；品牌力强、智驾成熟；价格24.98万起门槛更低\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"定位与场景（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"核心差异在越野能力和座位数。需要6/7座或越野选极石01，纯城市家用、看重品牌和智驾选L6。\"}]",
			ContainsPrice: true, Status: 1,
		},
		{
			OurModelID:      getModelID("rox01"),
			CompetitorBrand: "理想", CompetitorModel: "理想L6 Max",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"续航与动力（我方优势）\", \"our_value\": \"长续航版58.4kWh大电池，CLTC纯电306km（L6 Max约212km），纯电续航优势明显；350kW/740N·m动力更强\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"续航与动力（对方优势）\", \"our_value\": \"\", \"their_value\": \"300kW/529N·m动力够用，5.4秒破百和极石01接近；增程技术成熟，综合续航1390km；能耗管理口碑好\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"续航与动力（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"纯电续航极石01长续航版优势明显（多94km），日常通勤更省。动力数据极石01也更强。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("rox01"),
			CompetitorBrand: "理想", CompetitorModel: "理想L6 Max",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"空间与实用性（我方优势）\", \"our_value\": \"轴距长约90mm，车内空间更大；提供6/7座（L6只有5座），家庭多人出行实用性翻倍；二三排放倒可露营大床\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"空间与实用性（对方优势）\", \"our_value\": \"\", \"their_value\": \"5座布局每个座位都宽敞，后备箱空间规整；车长短一点好开好停；家庭储物空间设计贴心\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"空间与实用性（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"空间实用性极石01优势明显——6/7座布局+更大车身。只需要5座的话L6也够用。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("rox01"),
			CompetitorBrand: "理想", CompetitorModel: "理想L6 Max",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"智能驾驶与座舱（我方优势）\", \"our_value\": \"高阶版可选激光雷达+双Orin-X（508TOPS），智驾硬件更强；后排娱乐屏全系标配；8155芯片座舱流畅\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"智能驾驶与座舱（对方优势）\", \"our_value\": \"\", \"their_value\": \"Max版Thor-U 700TOPS算力+城市NOA，智驾体验成熟；8295P芯片+双3K屏座舱体验好；理想同学语音交互出色\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"智能驾驶与座舱（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"智驾体验理想更成熟、功能更完善，这是理想的核心优势。极石01高阶版硬件不差但软件成熟度待提升。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("rox01"),
			CompetitorBrand: "理想", CompetitorModel: "理想L6 Max",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"性价比与选购建议（我方优势）\", \"our_value\": \"价格高2万但空间更大、座位更多、有越野能力、动力更强、户外配置全，功能场景更多元；长续航版纯电里程更长\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"性价比与选购建议（对方优势）\", \"our_value\": \"\", \"their_value\": \"品牌力强、智驾成熟、售后方便、产品验证充分；起售价更低入门门槛低；车机生态完善\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"性价比与选购建议（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"追求品牌、智驾和售后便利性选理想L6；需要6/7座、有越野/露营需求、看重性价比选极石01。各有千秋。\"}]",
			ContainsPrice: true, Status: 1,
		},
		{
			OurModelID:      getModelID("rox01"),
			CompetitorBrand: "理想", CompetitorModel: "理想L7 Pro",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"户外与越野场景（我方优势）\", \"our_value\": \"双电机全时四驱+越野模式+高离地间隙，非铺装路和轻越野轻松应对；尾门餐吧+外放电+车顶行李架户外配置全；AT胎可选\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"户外与越野场景（对方优势）\", \"our_value\": \"\", \"their_value\": \"城市SUV底盘，不能越野；也有外放电和露营模式，但通过性有限，只能走铺装路\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"户外与越野场景（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"户外和越野能力是最大差异——极石01能去理想L7去不了的地方。喜欢露营、自驾穿越的用户选极石01。\"}]",
			ContainsPrice: false, Status: 1,
		},
		{
			OurModelID:      getModelID("rox01"),
			CompetitorBrand: "理想", CompetitorModel: "理想L7 Pro",
			CompareType:   "优势",
			Content:       "[{\"aspect\": \"座椅布局灵活性（我方优势）\", \"our_value\": \"提供6座/7座两种布局选择（L7只有5座），家庭多人出行更灵活；6座版二排独立座椅+零重力模式体验好；7座版应急载人方便\", \"their_value\": \"\", \"conclusion\": \"我方更优\"}, {\"aspect\": \"座椅布局灵活性（对方优势）\", \"our_value\": \"\", \"their_value\": \"5座布局每个座位都宽敞，皇后座设计有特色；但无法满足6/7人出行需求\", \"conclusion\": \"对方更优\"}, {\"aspect\": \"座椅布局灵活性（综合评价）\", \"our_value\": \"\", \"their_value\": \"\", \"conclusion\": \"核心差异在座位数。需要6/7座直接选极石01。5座足够的话两者各有取向。\"}]",
			ContainsPrice: false, Status: 1,
		},
	}

	for _, comp := range compares {
		db.DB.Create(&comp)
	}

	log.Printf("已创建 %d 组竞品对比", len(compares))
}

// ============================================================
// 13. 种子知识片段（5+条）
// ============================================================

// ============================================================
// 13. 种子知识片段（42条营销话术）
// ============================================================

func seedKnowledgeFragments() {
	var count int64
	db.DB.Model(&model.KnowledgeFragment{}).Count(&count)
	if count > 0 {
		log.Println("知识片段数据已存在，跳过")
		return
	}

	fragments := []model.KnowledgeFragment{
		{
			Category: "核心技术", Title: "极石智能增程技术——续航无忧的全地形动力心脏",
			Content: "极石智能增程系统，是专为全地形出行打造的动力解决方案。搭载1.5T四缸高效增程器（热效率高达42%），配合前后双电机全时四驱，系统综合功率350kW、峰值扭矩740N·m，零百加速仅5.5秒。44.5kWh宁德时代三元锂电池带来CLTC 215km纯电续航，日常通勤零油耗；配合70L大油箱，CLTC综合续航高达1405km，长途穿越无焦虑。全栈自研的MDCU驱动域控制单元是三电系统的大脑，最快150ms内控制打滑车轮，比机械差速锁更智能、更精准。无论城市通勤还是荒野探索，极石增程都能让你收放自如、从容驰骋。",
			Tags:    "[\"增程技术\", \"智能动力\", \"MDCU\", \"全地形\"]", ApplicableModels: "[\"极石ADAMAS\"]",
			Status: 1, Sort: 1,
		},
		{
			Category: "核心技术", Title: "全铝底盘+空气悬架——可城可野的魔毯体验",
			Content: "极石ADAMAS率先在硬派SUV领域采用全铝合金底盘架构，前双叉臂、后H臂多连杆独立悬架，配合全铝合金副车架，轻量化与刚性兼得。全系标配闭式空气悬架系统，140mm超大调节行程，一键切换7级高度：高速行驶自动降低车身减少风阻，越野时升高80mm提升通过性，地库模式降低50mm方便进出，后备厢装载模式后轴降低60mm轻松搬运行李。DCC智能阻尼控制系统每10毫秒完成一次阻尼调节，每秒100次扫描路况，无论是铺装路面的细碎颠簸还是越野路面的剧烈冲击，都能精准过滤。承载式车身的公路舒适+空气悬架的越野通过性，真正实现可城可野、全场景胜任。",
			Tags:    "[\"全铝底盘\", \"空气悬架\", \"DCC\", \"可城可野\"]", ApplicableModels: "[\"极石ADAMAS\"]",
			Status: 1, Sort: 2,
		},
		{
			Category: "核心技术", Title: "中汽研L3级越野认证——硬核实力的官方背书",
			Content: "极石ADAMAS荣获中汽研L3级进阶越野能力认证，这是对其全地形实力的权威认可。最小离地间隙272mm、最大涉水深度770mm、接近角27.5°、离去角27.9°、纵向通过角24.6°、最大爬坡度100%（45°）——每一个数据都远超普通城市SUV。7种全地形驾驶模式，配合越野自动巡航、陡坡缓降、透明底盘、涉水深度检测等智能越野辅助功能，让越野新手也能轻松应对复杂路况。电池包离地间隙高达324mm，比底盘最低点还高52mm，从根本上避免越野托底损伤电池的风险。越野不是冒险，而是有底气的探索。",
			Tags:    "[\"越野认证\", \"L3级\", \"全地形\", \"中汽研\"]", ApplicableModels: "[\"极石ADAMAS\"]",
			Status: 1, Sort: 3,
		},
		{
			Category: "用户场景", Title: "家庭露营场景——把家开进山野",
			Content: "周末带全家去露营？极石ADAMAS就是你的移动山居。5.7kW大功率对外放电，电磁炉、咖啡机、投影仪同时使用都没问题；尾门户外餐吧2.0，集成折叠操作台、模块化收纳、LED照明和一键供水系统，3分钟搭建户外厨房。七座版二、三排一键放平，形成2米多长的纯平大床空间，铺上充气床垫就是孩子的游戏区、大人的午休间。车顶支持300kg静态载荷，加装车顶帐篷就是二层观景房。三区恒温空调+车载冰箱+后排娱乐屏，老人孩子各得其乐。别人露营要搬半车装备，你开极石去，车就是全部装备。",
			Tags:    "[\"家庭露营\", \"户外生活\", \"尾门餐吧\", \"外放电\"]", ApplicableModels: "[\"极石ADAMAS\"]",
			Status: 1, Sort: 4,
		},
		{
			Category: "用户场景", Title: "长途穿越场景——一万公里的从容",
			Content: "从大理到拉萨，从额济纳到喀纳斯，长途自驾最担心的就是续航和可靠性。极石增程系统CLTC综合续航1405km，满油满电一口气跑1000公里+，不用到处找充电桩，沿途加油站就能补能。全铝底盘+空气悬架，连续几千公里烂路也不心疼；770mm涉水深度、45°爬坡能力，遇到突发路况也能从容应对。Nappa真皮座椅+加热通风按摩+二排零重力模式，坐多久都不累；车载冰箱随时有冰水，后排娱乐屏让旅途不再无聊。还有L2+级辅助驾驶帮你分担高速驾驶压力。开极石长途穿越，你只需要关心风景，剩下的交给车。",
			Tags:    "[\"长途穿越\", \"自驾旅行\", \"续航无忧\"]", ApplicableModels: "[\"极石ADAMAS\"]",
			Status: 1, Sort: 5,
		},
		{
			Category: "用户场景", Title: "城市通勤场景——豪华舒适的日常",
			Content: "别以为硬派SUV在城里就不好开。极石ADAMAS的承载式全铝底盘+空气悬架，在城市里的行驶质感完全不输豪华城市SUV。纯电模式下215km续航，一周通勤不烧油，静谧性和加速响应都是纯电动车的体验。电吸门轻轻一推就优雅闭合，老人小孩都好用；车载冰箱夏天冰饮料冬天热奶茶，通勤路上就有小确幸。L2+级辅助驾驶在早晚高峰的拥堵路段能自动跟车，高速通勤更轻松。方盒子造型停在公司停车场气场十足，流沙金配色低调又有品质感。谁说越野SUV就不能是日常通勤的好伙伴？极石告诉你：可以，而且很豪华。",
			Tags:    "[\"城市通勤\", \"日常驾驶\", \"豪华舒适\"]", ApplicableModels: "[\"极石ADAMAS\"]",
			Status: 1, Sort: 6,
		},
		{
			Category: "常见问题", Title: "关于续航和油耗——真实表现怎么样？",
			Content: "很多用户关心极石增程的实际续航表现。先说结论：日常通勤纯电够用，长途出行增程无忧。CLTC纯电续航215km（长续航版306km），按照大多数人每天50km通勤计算，一周充一次电完全够用，日常使用成本约合每公里1毛钱。长途出行用增程模式，WLTC馈电油耗约6.76L/100km，比同级别燃油SUV省30%以上，加满70L油箱能跑1000公里+。极石的MDCU智能能量管理系统会根据路况、电量自动在纯电/混动/燃油模式间切换，你只管开，不用操心什么时候该用电什么时候该用油。增程技术最大的好处就是——既有电动车的驾驶质感和使用成本，又没有纯电车的续航焦虑。",
			Tags:    "[\"续航\", \"油耗\", \"充电\", \"增程器\"]", ApplicableModels: "[\"极石ADAMAS\"]",
			Status: 1, Sort: 7,
		},
		{
			Category: "常见问题", Title: "关于保养与质保——养车贵不贵？",
			Content: "买新能源车，保养和质保是大家最关心的问题。极石汽车提供5年或10万公里整车质保，首任车主还可享受终身守护服务包和5年免费保养权益。增程式车型的保养非常简单：因为发动机主要负责发电、工作在最优工况，所以保养周期长、项目少。常规保养主要是检查增程器、更换机油机滤、检查三电系统，单次小保养费用约500-800元，比同级别燃油SUV便宜不少。电池方面，采用宁德时代三元锂电池，配备液冷温控和多重安全保护，正常使用下电池寿命远超车辆使用周期，厂家提供标准电池质保。简单算一笔账：每年开2万公里，纯电行驶占80%，一年电费+油费+保养总费用不到5000元，比燃油车省一半以上。",
			Tags:    "[\"保养\", \"质保\", \"售后\", \"养车成本\"]", ApplicableModels: "[\"极石ADAMAS\"]",
			Status: 1, Sort: 8,
		},
		{
			Category: "常见问题", Title: "关于越野能力——承载式车身真的能越野吗？",
			Content: "承载式车身也能越野？这是很多人对极石的疑问。答案是：不仅能，而且比大多数硬派SUV更好开、更智能。首先看硬件：272mm最小离地间隙、770mm涉水深度、27.5°接近角、100%爬坡度，这些数据已经达到了硬派越野的入门门槛；再看软件：双电机全时四驱+150ms电子限滑，响应速度比机械差速锁还快；7种全地形模式+透明底盘+涉水检测，让越野变得更简单、更安全。承载式车身的优势在于公路舒适性更好，长途穿越人不累。如果你不是要去爬石头、玩极限越野，而是要跑长途、走烂路、穿沙漠、露营穿越，极石的智能越野反而更实用——中汽研L3级越野认证就是最好的证明。",
			Tags:    "[\"越野能力\", \"承载式车身\", \"空气悬架\", \"四驱\"]", ApplicableModels: "[\"极石ADAMAS\"]",
			Status: 1, Sort: 9,
		},
		{
			Category: "常见问题", Title: "关于品牌与售后——小众品牌靠谱吗？",
			Content: "极石这个牌子没怎么听过，会不会不靠谱？这是一个很实在的问题。让我们用事实说话：极石汽车成立于2021年，背靠魏桥创业集团（世界500强）10亿美金战略投资，资金实力有保障。2025年全球交付15318台，同比增长近3倍，已进入全球40多个国家，在中东市场份额超越宝马X5——能出海、能被海外市场认可的品牌，产品力不会差。国内市场2026年正在加速渠道布局，采用代理合作模式，更多体验中心正在陆续开业。售后方面，整车5年10万公里质保，首任车主终身守护服务包，5年免费保养，这些都是实打实的保障。小众不等于不靠谱，专注户外赛道的极石，反而更懂热爱户外的你。",
			Tags:    "[\"品牌\", \"售后\", \"渠道\", \"可靠性\"]", ApplicableModels: "[\"极石ADAMAS\"]",
			Status: 1, Sort: 10,
		},
		{
			Category: "用户场景", Title: "30万级家庭户外SUV的性价比之选",
			Content: "预算30万想入手一台能家用、能越野、能露营的增程SUV？极石01绝对值得你放进备选清单。29.99万的起售价，就能买到前后双电机全时四驱（350kW/740N·m）、1.5T增程无焦虑、44.5kWh宁德时代大电池、全铝合金底盘+DCC可变阻尼、15.7英寸3K大屏+高通8155芯片、二排独立座椅……这个配置放在同价位几乎找不到对手。加1万能升级6座航空座椅，加5万能升级58.4kWh长续航电池+空气悬架+高阶智驾选装权，每一分钱都花在刀刃上。极石01不是完美的车，但它用最实在的价格，给了你最全面的能力——这就是性价比的真正含义。",
			Tags:    "[\"家庭用户\", \"性价比\", \"6座7座\", \"入门选择\"]", ApplicableModels: "[\"极石01\"]",
			Status: 1, Sort: 11,
		},
		{
			Category: "核心技术", Title: "全球首发纯固态激光雷达——越野也有高阶智驾",
			Content: "极石01高阶智驾版，搭载全球量产首发的禾赛纯固态激光雷达，配合双英伟达Orin-X芯片（总算力508TOPS）、12颗摄像头、5颗毫米波雷达和12个超声波传感器，构建了全方位的感知系统。更厉害的是，极石把激光雷达从公路拓展到了越野——全地形智能穿越功能可以实时识别前方道路的凸起物、沟坎、水坑，通过3D透明底盘让你看清车底状况，辅助驾驶员精准决策越野路径，避免托底和刮蹭。这不是花架子，而是真正能在越野时帮上忙的黑科技。对于经常跑非铺装路面、喜欢探索未知路线的玩家来说，这套系统就是你的越野副驾。",
			Tags:    "[\"激光雷达\", \"Orin-X\", \"全地形智能穿越\", \"高阶智驾\"]", ApplicableModels: "[\"极石01\"]",
			Status: 1, Sort: 12,
		},
		{
			Category: "竞品话术", Title: "比坦克500更实用的家庭SUV——6/7座+大空间",
			Content: "同样35万预算，为什么选极石ADAMAS而不是坦克500？第一个理由就是空间和实用性。坦克500只有5座，而ADAMAS提供6座和7座两种布局，二胎家庭、三代同堂出行完全无压力。轴距方面，ADAMAS 3010mm比坦克500的2850mm长了160mm，后排腿部空间多了一拳多，长途乘坐更舒适。而且ADAMAS的二排零重力航空座椅，150°躺角+8点式按摩+腿托，舒适性远超坦克500的普通座椅。上有老下有小的家庭用户，6/7座布局才是刚需——坦克500更像一个人的大玩具，ADAMAS才是全家的好伙伴。",
			Tags:    "[\"坦克500\", \"对比优势\", \"6座7座\", \"空间\"]", ApplicableModels: "[\"极石ADAMAS\"]",
			Status: 1, Sort: 13,
		},
		{
			Category: "竞品话术", Title: "配置碾压坦克500——全系标配的豪华体验",
			Content: "35万买SUV，你想要空气悬架、电吸门、车载冰箱、零重力座椅、后排娱乐屏？坦克500要高配甚至选装才有，而且价格直奔40万+。但极石ADAMAS呢？这些配置全系标配，起步价只要34.99万。空气悬架是闭式系统，140mm超大调节行程；电吸门是全车四扇+尾门都有，轻轻一推就优雅闭合；8.5L车载冰箱能冰饮料也能热奶茶；后排娱乐屏还是15.7英寸3K触控屏。同样的钱，在坦克500那里只能买到中配，在ADAMAS这里直接给你满配豪华体验。配置党选ADAMAS，绝对不亏。",
			Tags:    "[\"坦克500\", \"对比优势\", \"配置\", \"空气悬架\", \"电吸门\"]", ApplicableModels: "[\"极石ADAMAS\"]",
			Status: 1, Sort: 14,
		},
		{
			Category: "竞品话术", Title: "比坦克500更省油——增程技术长途无焦虑",
			Content: "同样35万级硬派SUV，为什么极石ADAMAS更省油？核心是技术路线不同。坦克500是插电混动，发动机要参与驱动，馈电油耗8.35L/100km；而ADAMAS是增程式，增程器只发电不直接驱动，始终工作在最优效率区间，馈电油耗只要6.76L/100km，差了快2升。纯电续航方面，ADAMAS 215km几乎是坦克500 110km的两倍，日常通勤基本不用加油。综合续航ADAMAS 1405km也比坦克500的900km远了500多公里，长途穿越少加好几次油。省油就是省钱，一年开2万公里，光油费就能省出好几千。",
			Tags:    "[\"坦克500\", \"对比优势\", \"油耗\", \"续航\", \"增程\"]", ApplicableModels: "[\"极石ADAMAS\"]",
			Status: 1, Sort: 15,
		},
		{
			Category: "竞品话术", Title: "比坦克500好开太多——全铝底盘的公路质感",
			Content: "硬派SUV一定不好开吗？开了极石ADAMAS你就知道——完全不是。坦克500是非承载式车身，大梁车身颠簸感明显，过弯侧倾大，开起来像开船；而ADAMAS是承载式全铝底盘+空气悬架，公路行驶质感跟豪华城市SUV没区别，过弯支撑到位，滤震细腻，NVH控制也更好。日常上下班代步、周末带家人出游，ADAMAS的驾驶和乘坐舒适性完胜坦克500。90%的人90%的时间都在城市和高速上开，那为什么不选一台更好开、更舒适的车呢？越野能力够用就行，日常体验才是真的。",
			Tags:    "[\"坦克500\", \"对比优势\", \"舒适性\", \"全铝底盘\", \"公路体验\"]", ApplicableModels: "[\"极石ADAMAS\"]",
			Status: 1, Sort: 16,
		},
		{
			Category: "竞品话术", Title: "比坦克500更智能——更懂中国人的智能座舱",
			Content: "同样35万，极石ADAMAS的智能化体验比坦克500领先一个时代。智驾方面，ADAMAS搭载地平线征程6M芯片，128TOPS算力，13颗摄像头+3颗毫米波雷达+12颗超声波雷达，支持高速NOA+城区LCC，比坦克500的Coffee Pilot感知硬件更强。座舱方面，ADAMAS的小石头AI助手融合了DeepSeek和文心一言大模型，语音交互更自然、更智能，六音区识别让每个座位的乘客都能控制车机。15.7英寸3K中控屏+15.7英寸3K后排娱乐屏，显示效果比坦克500更清晰。智能体验，ADAMAS是新能源时代的产物，坦克500还停留在燃油车的智能化水平。",
			Tags:    "[\"坦克500\", \"对比优势\", \"智能化\", \"智驾\", \"座舱\"]", ApplicableModels: "[\"极石ADAMAS\"]",
			Status: 1, Sort: 17,
		},
		{
			Category: "竞品话术", Title: "比理想L8更全能——能越野的SUV才是真SUV",
			Content: "同样35万级增程SUV，为什么选极石ADAMAS而不是理想L8？因为ADAMAS是真·SUV，而理想L8只是加高的MPV。理想L8离地间隙低、没有四驱越野能力，只能走铺装路面，遇到个烂路、泥地、沙地就傻眼了；而ADAMAS拥有双电机全时四驱、272mm最小离地间隙、770mm涉水深度、7种全地形模式，中汽研L3级越野认证可不是白拿的。周末全家去露营，理想L8只能停在停车场，ADAMAS能直接开到营地旁边。人生不只有柏油路，还有诗和远方——选一台能带你去更多地方的车吧。",
			Tags:    "[\"理想L8\", \"对比优势\", \"越野\", \"通过性\", \"户外\"]", ApplicableModels: "[\"极石ADAMAS\"]",
			Status: 1, Sort: 18,
		},
		{
			Category: "竞品话术", Title: "配置比理想L8更实在——全系标配的豪华",
			Content: "35万买增程SUV，极石ADAMAS的配置比理想L8更实在。空气悬架？ADAMAS全系标配，理想L8 Pro版都没有。电吸门？ADAMAS全车四扇+尾门都有，理想L8只有高配才有。车载冰箱？ADAMAS 8.5L冷暖双模全系标配，理想L8需要选装。零重力座椅？ADAMAS二排就是零重力航空座椅，理想L8二排虽然也不错但还没到零重力级别。算一笔账：ADAMAS 34.99万的起步价，这些配置都给你了；理想L8要拿到同等配置，至少得37万以上。同样的钱，ADAMAS给你更多实实在在的豪华体验。",
			Tags:    "[\"理想L8\", \"对比优势\", \"配置\", \"电吸门\", \"冰箱\", \"全系标配\"]", ApplicableModels: "[\"极石ADAMAS\"]",
			Status: 1, Sort: 19,
		},
		{
			Category: "竞品话术", Title: "比理想L8更懂户外——专为露营打造的移动山居",
			Content: "如果你喜欢露营和户外生活，极石ADAMAS比理想L8更懂你。ADAMAS有独创的尾门餐吧2.0，折叠操作台+模块化收纳+LED照明+一键供水，3分钟搭出户外厨房；而理想L8只有个普通的尾门，做饭还得搬桌子。外放电方面，ADAMAS 5.7kW（3.5kW+2.2kW双路）比理想L8的外放电功率更大、更灵活。车顶载荷300kg，加装车顶帐篷就是二层楼；二三排放倒就是纯平大床。别人露营要带一堆装备，开ADAMAS去，车本身就是装备。这才是户外SUV该有的样子。",
			Tags:    "[\"理想L8\", \"对比优势\", \"户外\", \"尾门餐吧\", \"露营\", \"外放电\"]", ApplicableModels: "[\"极石ADAMAS\"]",
			Status: 1, Sort: 20,
		},
		{
			Category: "竞品话术", Title: "比理想L8更有气场——方盒子造型的硬派魅力",
			Content: "看腻了千篇一律的城市SUV？极石ADAMAS的方盒子硬派造型，开在路上回头率绝对比理想L8高。理想L8的设计太居家了，像个大号MPV，没什么个性；ADAMAS的方盒子造型+宽体轮拱+外挂备胎选装，硬派气息拉满，停在任何地方都是焦点。流沙金配色低调又高级，黑武士风格霸气外露。开理想L8你就是个普通奶爸，开ADAMAS你就是个有态度的生活家。车不只是交通工具，也是你的个人名片——选一台有个性的车，活出不一样的自己。",
			Tags:    "[\"理想L8\", \"对比优势\", \"造型\", \"个性\", \"气场\"]", ApplicableModels: "[\"极石ADAMAS\"]",
			Status: 1, Sort: 21,
		},
		{
			Category: "竞品话术", Title: "续航比理想L8更远——1405km说走就走",
			Content: "长途出行谁更从容？极石ADAMAS CLTC综合续航1405km，比理想L8的1315km多了90km。这是因为ADAMAS的增程器热效率高达42%，比理想的增程器效率更高，70L大油箱也比理想的65L更大。别小看这90公里，关键时刻就是能不能撑到下一个加油站的区别。纯电续航215km也和理想L8 Pro版的210km基本持平，日常通勤都够用。长途自驾、进藏穿越，续航越长心里越踏实。ADAMAS的增程技术经过全球40多个国家市场验证，在中东那种极端环境下都稳定可靠——可靠性，你完全可以放心。",
			Tags:    "[\"理想L8\", \"对比优势\", \"续航\", \"增程器\", \"热效率\"]", ApplicableModels: "[\"极石ADAMAS\"]",
			Status: 1, Sort: 22,
		},
		{
			Category: "竞品话术", Title: "比豹8便宜5万——性价比之王ADAMAS",
			Content: "同样是硬派新能源SUV，为什么极石ADAMAS比方程豹豹8更值得买？第一个理由就是价格。豹8起售价37.98万，顶配直奔42.78万；而ADAMAS起售价只要34.99万，顶配35.99万，便宜了3-7万。这差价够你加好几年的油、买全套露营装备、甚至来一次长途自驾旅行了。更关键的是，ADAMAS的配置比豹8更实用：空气悬架全系标配、电吸门全系标配、车载冰箱全系标配、6/7座布局更灵活。花更少的钱，得到更全面的豪华体验，这才是真正的性价比。",
			Tags:    "[\"方程豹豹8\", \"对比优势\", \"价格\", \"性价比\", \"便宜5万\"]", ApplicableModels: "[\"极石ADAMAS\"]",
			Status: 1, Sort: 23,
		},
		{
			Category: "竞品话术", Title: "比豹8空间更大更实用——轴距长90mm的差距",
			Content: "别看豹8车身更长（5195mm vs 5050mm），但实际乘坐空间极石ADAMAS更有优势。为什么？因为豹8是非承载式车身，大梁占用了车内空间，空间利用率低；而ADAMAS是承载式车身，空间利用率更高。轴距方面，ADAMAS 3010mm比豹8的2920mm长了90mm，后排腿部空间更宽敞。更重要的是座位数：ADAMAS提供6座和7座，二排零重力座椅舒适性拉满；豹8虽然也可选6/7座，但第三排空间受大梁影响比较局促。家庭用户选车，空间实用性才是王道。",
			Tags:    "[\"方程豹豹8\", \"对比优势\", \"空间\", \"6座7座\", \"轴距\"]", ApplicableModels: "[\"极石ADAMAS\"]",
			Status: 1, Sort: 24,
		},
		{
			Category: "竞品话术", Title: "比豹8好开太多——公路舒适性完胜",
			Content: "硬派SUV在城市里就一定难开吗？极石ADAMAS用实力说不。豹8是非承载式车身+液压悬架，过减速带、走烂路颠簸感明显，城市开着像开船；而ADAMAS是全铝承载式车身+闭式空气悬架，公路行驶的静谧性、滤震质感、过弯支撑性都完胜豹8。日常通勤、高速长途，ADAMAS的舒适性和驾驶质感跟豪华城市SUV没区别。液压悬架虽然越野调节幅度大，但响应速度和公路舒适性不如空气悬架。记住：你90%的时间都在公路上开，公路舒适性才是陪伴你最多的体验。",
			Tags:    "[\"方程豹豹8\", \"对比优势\", \"舒适性\", \"全铝底盘\", \"公路体验\"]", ApplicableModels: "[\"极石ADAMAS\"]",
			Status: 1, Sort: 25,
		},
		{
			Category: "竞品话术", Title: "增程比插混更可靠——技术简单就是优势",
			Content: "硬派新能源车，技术路线选择很重要。极石ADAMAS是增程式，方程豹豹8是插混DMO。增程的优势是什么？结构简单、可靠性高、保养便宜。增程器只发电不直接驱动，没有复杂的变速箱和动力耦合机构，出故障的概率低很多；而豹8的DMO插混结构复杂得多，发动机要参与驱动，还有变速箱、差速器等一堆机械结构，坏了修起来也贵。越野场景下，越简单的结构越可靠——这也是为什么硬派越野车都崇尚机械简单。增程式虽然结构简单，但越野能力一点都不弱，反而更可靠、更省心。",
			Tags:    "[\"方程豹豹8\", \"对比优势\", \"增程\", \"可靠性\", \"保养\"]", ApplicableModels: "[\"极石ADAMAS\"]",
			Status: 1, Sort: 26,
		},
		{
			Category: "竞品话术", Title: "豪华配置更全面——电吸门零重力座椅全系标配",
			Content: "40万级硬派SUV，豪华配置谁更实在？极石ADAMAS 35万就给你：电吸门（全车+尾门）、车载冰箱、二排零重力座椅、空气悬架、后排3K娱乐屏，全系标配。方程豹豹8呢？38万起步，电吸门可能有但冰箱不一定有，零重力座椅不一定到这个级别，很多配置还要选装。同样的豪华体验，ADAMAS让你少花3万块。而且ADAMAS的内饰用料更细腻，Nappa真皮+超纤绒顶棚+256色氛围灯，豪华感的营造比豹8更到位。买豪华SUV，享受的就是这些细节——ADAMAS细节拉满。",
			Tags:    "[\"方程豹豹8\", \"对比优势\", \"豪华配置\", \"电吸门\", \"零重力座椅\"]", ApplicableModels: "[\"极石ADAMAS\"]",
			Status: 1, Sort: 27,
		},
		{
			Category: "竞品话术", Title: "比坦克300大一号——30万级家庭SUV的正确选择",
			Content: "预算30万，选坦克300还是极石01？如果你有家庭，答案很明确——极石01。坦克300是紧凑型SUV，轴距只有2750mm，后排空间只能说够用；极石01是中大型SUV，轴距3010mm，后排腿部空间两拳有余，长途乘坐不憋屈。更关键的是座位数：坦克300只有5座，极石01有6座和7座两种布局，二胎家庭、带父母出行都没问题。29.99万的起售价，比坦克300 Hi4-T贵5万，但换来的是整整大一级的空间和实用性。这笔投资，每天都能感受到回报。",
			Tags:    "[\"坦克300\", \"对比优势\", \"空间\", \"6座7座\", \"家庭\"]", ApplicableModels: "[\"极石01\"]",
			Status: 1, Sort: 28,
		},
		{
			Category: "竞品话术", Title: "纯电续航是坦克300的2-3倍——日常通勤更省",
			Content: "同样是带电机的硬派SUV，极石01的纯电续航比坦克300 Hi4-T强太多了。坦克300纯电续航才105km，冬天开空调实际可能只有七八十公里，两三天就得充一次；极石01标准续航版215km、长续航版306km，是坦克300的2-3倍，一周充一次电就够了。日常通勤纯电出行，每公里成本一毛钱，比油车省太多。长续航版58.4kWh大电池，周末来回近郊玩都不用烧油。有家充桩的话，极石01用起来跟纯电车一样省，还没有续航焦虑。",
			Tags:    "[\"坦克300\", \"对比优势\", \"续航\", \"纯电通勤\", \"油耗\"]", ApplicableModels: "[\"极石01\"]",
			Status: 1, Sort: 29,
		},
		{
			Category: "竞品话术", Title: "舒适性吊打坦克300——家庭用户在乎的就是这个",
			Content: "硬派SUV不等于硬邦邦的底盘。坦克300是非承载式车身+整体桥后悬架，过个减速带颠得厉害，走烂路后排能跳起来，坐久了腰酸背痛；极石01是全铝承载式底盘+双叉臂+多连杆独立悬架，长续航版还有空气悬架，滤震细腻、乘坐舒适，老人孩子坐长途也不累。日常通勤上下班，极石01开着跟城市SUV一样舒服；坦克300开着就像一台工具车，颠得慌。别拿越野当借口——你90%的时间都在城市里开，舒适性才是真正影响你用车体验的东西。",
			Tags:    "[\"坦克300\", \"对比优势\", \"舒适性\", \"全铝底盘\", \"日常体验\"]", ApplicableModels: "[\"极石01\"]",
			Status: 1, Sort: 30,
		},
		{
			Category: "竞品话术", Title: "智能驾驶领先一个时代——激光雷达+508TOPS算力",
			Content: "30万级硬派SUV，谁的智能化程度更高？极石01高阶版搭载3颗禾赛纯固态激光雷达+双英伟达Orin-X芯片，总算力508TOPS，这配置放在30万级SUV里是天花板级别的。坦克300 Hi4-T呢？没有激光雷达，智驾芯片也只是基础水平，高速NOA和城区辅助驾驶能力有限。智能座舱方面，极石01是高通8155芯片+15.7英寸3K大屏+后排娱乐屏+4屏联动，坦克300就是普通的8155+14.6英寸屏。智能化时代了，买车还是要看未来几年的体验——极石01的智能硬件够你用五年不落后。",
			Tags:    "[\"坦克300\", \"对比优势\", \"智驾\", \"激光雷达\", \"高阶智能\"]", ApplicableModels: "[\"极石01\"]",
			Status: 1, Sort: 31,
		},
		{
			Category: "竞品话术", Title: "比坦克300更懂户外——专为露营设计的移动山居",
			Content: "喜欢露营和户外的你，应该选极石01而不是坦克300。为什么？因为极石01的户外配置更专业、更贴心。首创的尾门餐吧系统，折叠操作台+收纳空间，不用额外买桌子就能做饭；L型快搭车载天幕，车停好就能遮阳；二三排放倒就是纯平大床，不用搭帐篷就能睡觉；车顶行李架+300kg载荷，装车顶帐篷也行。坦克300呢？这些都没有，你得自己买一堆装备。开极石01去露营，车就是你的房子和厨房——这才是户外生活方式的正确打开方式。",
			Tags:    "[\"坦克300\", \"对比优势\", \"户外配置\", \"露营\", \"尾门餐吧\"]", ApplicableModels: "[\"极石01\"]",
			Status: 1, Sort: 32,
		},
		{
			Category: "竞品话术", Title: "比豹5大一号更实用——6/7座家庭刚需",
			Content: "同样30万级硬派新能源SUV，为什么极石01比方程豹豹5更适合家庭？空间和座位数是关键。豹5是中型SUV，轴距2800mm，只有5座；极石01是中大型SUV，轴距3010mm，有6座和7座。差210mm轴距是什么概念？后排腿部空间差出一拳多，满载出行时差距更明显。有了二胎、经常带父母出门的家庭，6/7座是刚需，5座真的不够用。而且极石01的承载式车身空间利用率更高，第三排不是小板凳，成年人也能坐。家用SUV，空间和座位数才是硬通货。",
			Tags:    "[\"方程豹豹5\", \"对比优势\", \"空间\", \"6座7座\", \"家庭实用\"]", ApplicableModels: "[\"极石01\"]",
			Status: 1, Sort: 33,
		},
		{
			Category: "竞品话术", Title: "比豹5舒服太多——日常通勤的体验差很多",
			Content: "每天都要开的车，舒适性太重要了。方程豹豹5是非承载式车身，大梁车身+硬派悬架，颠簸感明显，城市开着又颠又吵；极石01是全铝承载式车身+前后独立悬架，长续航版还有空气悬架，开着坐著都舒服。NVH方面，承载式车身和风阻设计也更优，高速巡航时车内更安静。别小看这个——你每天上下班通勤1小时，一年就是200多小时在车里，舒服一点真的很重要。豹5是越野玩具，极石01是家用伙伴——定位不同，体验差很多。",
			Tags:    "[\"方程豹豹5\", \"对比优势\", \"舒适性\", \"全铝底盘\", \"日常通勤\"]", ApplicableModels: "[\"极石01\"]",
			Status: 1, Sort: 34,
		},
		{
			Category: "竞品话术", Title: "长续航版纯电306km——豹5顶配都比不上",
			Content: "纯电续航有多重要？开过新能源车的人都懂。极石01长续航版58.4kWh大电池，CLTC纯电306km，日常通勤一周充一次电就行；方程豹豹5长续航版才47.8kWh，纯电210km，比极石01标准续航版还少5km。什么概念？极石01长续航版能做到一周充一次电，豹5可能得一周充两次。而且极石是增程式，发动机只发电不驱动，效率更高、更平顺；豹5是插混，发动机要参与驱动，馈电时的平顺性和NVH都不如增程。纯电续航长、驾驶体验平顺，这就是增程的优势。",
			Tags:    "[\"方程豹豹5\", \"对比优势\", \"增程\", \"续航\", \"长续航版\"]", ApplicableModels: "[\"极石01\"]",
			Status: 1, Sort: 35,
		},
		{
			Category: "竞品话术", Title: "豪华配置更丰富——8155+后排娱乐屏全系标配",
			Content: "30万买硬派新能源车，极石01的配置比豹5更厚道。高通8155芯片？极石01全系标配，豹5部分车型才用。后排娱乐屏？极石01全系标配15.6英寸高清娱乐屏，豹5得高配才有。座椅配置？极石01二排独立座椅带加热通风按摩，6座版还是零重力航空座椅，豹5的二排就普通多了。内饰用料？极石01真皮座椅+256色氛围灯+高级音响，豪华感更到位。算下来，同等配置水平下，极石01的价格反而更有优势。买SUV不只是买越野能力，内饰和配置也是每天都要接触的东西。",
			Tags:    "[\"方程豹豹5\", \"对比优势\", \"豪华配置\", \"8155\", \"后排娱乐屏\"]", ApplicableModels: "[\"极石01\"]",
			Status: 1, Sort: 36,
		},
		{
			Category: "竞品话术", Title: "30万价位的全能之选——性价比甩豹5一条街",
			Content: "预算30万，极石01和方程豹豹5谁更值？我选极石01。豹5起售价23.98万看似便宜，但那是最低配，电池小（31.8kWh）、配置少、云辇和高阶智驾都没有；买到跟极石01差不多配置的版本，价格直奔28-30万去了。而极石01起售价29.99万，直接给你中大型车身、6/7座、44.5kWh电池、全铝底盘、8155芯片、后排娱乐屏……基础配置就很能打。加5万上长续航版，58.4kWh大电池+空气悬架+高阶智驾选装权，这个配置豹5根本没有对标的版本。性价比，极石01是认真的。",
			Tags:    "[\"方程豹豹5\", \"对比优势\", \"性价比\", \"30万\", \"全能SUV\"]", ApplicableModels: "[\"极石01\"]",
			Status: 1, Sort: 37,
		},
		{
			Category: "竞品话术", Title: "比理想L6更全能——能越野的才是真SUV",
			Content: "同样30万级增程SUV，为什么选极石01而不是理想L6？因为01是真正的SUV，而L6只是加高的轿车。理想L6没有四驱越野能力，离地间隙只有100多mm，遇到个烂路泥地就过不去，底盘低得连个马路牙子都得小心；极石01有双电机全时四驱、205mm离地间隙（满载）、700mm涉水、5+种全地形模式，轻中度越野根本不在话下。周末去露营，L6只能停在铺装路上，01能直接开到营地边上。人生不只有城市道路，还有山野和远方——选一台能带你去更多地方的车。",
			Tags:    "[\"理想L6\", \"对比优势\", \"越野\", \"通过性\", \"户外全能\"]", ApplicableModels: "[\"极石01\"]",
			Status: 1, Sort: 38,
		},
		{
			Category: "竞品话术", Title: "比理想L6座位更多——6/7座家庭刚需",
			Content: "理想L6只有5座，极石01有6座和7座——这是最本质的区别。三口之家可能5座够用，但如果有两个孩子、经常带父母出行，5座真的不够用。极石01的6座版，二排独立航空座椅带按摩加热通风，老人孩子坐长途特别舒服；7座版应急载人也方便，二三排放倒就是纯平大床。理想L6呢？后排坐三个人，中间那个人特别难受，而且永远只能坐5个人。家庭在长大，座位数不能少——多两个座位，就多了太多可能性。",
			Tags:    "[\"理想L6\", \"对比优势\", \"空间\", \"6座7座\", \"家庭多人出行\"]", ApplicableModels: "[\"极石01\"]",
			Status: 1, Sort: 39,
		},
		{
			Category: "竞品话术", Title: "长续航版纯电306km——比L6 Max远了近100km",
			Content: "纯电续航越长越好，这是新能源车主的共识。极石01长续航版CLTC纯电306km，理想L6 Max版才212km，差了94公里。什么概念？极石01长续航版能做到一周充一次电（按每天40km算），理想L6可能4-5天就得充一次。而且极石01的电池是58.4kWh，比L6 Max的51kWh还大7.4度电，冬天续航衰减的空间也更大。纯电续航越长，越接近纯电车的使用体验，用油的次数就越少，省下的油钱都是真金白银。",
			Tags:    "[\"理想L6\", \"对比优势\", \"续航\", \"长续航版\", \"306km纯电\"]", ApplicableModels: "[\"极石01\"]",
			Status: 1, Sort: 40,
		},
		{
			Category: "竞品话术", Title: "户外配置甩L6几条街——露营玩家的好伙伴",
			Content: "如果你喜欢露营和户外生活，理想L6真的不够看。极石01有尾门餐吧系统、L型车载天幕、二三排纯平大床模式、车顶行李架、AT胎可选……从设计之初就是为户外生活准备的。理想L6呢？虽然也有外放电，但车身低、通过性差，很多露营地都开不进去；没有尾门餐吧，做饭还得搬桌子；车顶载荷有限，装不了车顶帐篷。开极石01去露营，车就是你的全部装备；开L6去露营，你还得带半车装备。这就是专业和业余的区别。",
			Tags:    "[\"理想L6\", \"对比优势\", \"户外配置\", \"露营\", \"尾门餐吧\", \"外放电\"]", ApplicableModels: "[\"极石01\"]",
			Status: 1, Sort: 41,
		},
		{
			Category: "竞品话术", Title: "动力比L6强一截——350kW/740N·m的底气",
			Content: "30万级增程SUV，动力谁更强？极石01系统综合功率350kW、峰值扭矩740N·m，理想L6才300kW、529N·m，差了50kW和211N·m。实际体验上，极石01的加速感更猛、中后段超车更有底气，满载上坡也不费劲。而且极石01是全时四驱，雨雪天气行驶稳定性更好，越野脱困能力更强。L6虽然也有四驱，但定位是城市四驱，越野能力基本没有。动力这东西可以不用，但不能没有——关键时刻超个车、爬个坡，动力强的车就是更从容、更安全。",
			Tags:    "[\"理想L6\", \"对比优势\", \"动力\", \"350kW\", \"740N·m\"]", ApplicableModels: "[\"极石01\"]",
			Status: 1, Sort: 42,
		},
	}

	for _, frag := range fragments {
		db.DB.Create(&frag)
	}

	log.Printf("已创建 %d 条知识片段", len(fragments))
}

// ============================================================
// 辅助函数
// ============================================================

func jsonArr(arr []string) string {
	data, _ := json.Marshal(arr)
	return string(data)
}

func jsonParams(params map[string]string) string {
	data, _ := json.Marshal(params)
	return string(data)
}

// jsonCompareItems 对比项数组转JSON字符串
func jsonCompareItems(items []model.CompareItem) string {
	data, _ := json.Marshal(items)
	return string(data)
}
