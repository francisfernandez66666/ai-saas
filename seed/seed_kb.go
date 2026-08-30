package seed

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"log"
)

// ============================================================
// 2. 种子标签
// ============================================================

// seedTags 写入 tags 表（幂等：已有标签则整块跳过）。
// 作用：预置五大类标签（意向/属性/抗性/价值/行为），供打标规则与权重映射引用。
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

// seedFeatures 写入 features 表（幂等：已有卖点则跳过）。
// 作用：预置极石各车型核心卖点话术（增程动力/越野/座舱/质保/金融/户外生态），
// 被话术模板的 RequiredFeatures 关联引用，用于话术生成时注入卖点描述。
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

// seedTemplates 写入 templates 表（幂等：已有模板则跳过）。
// 作用：预置 8 类锚（aggressiveness 0~6）话术模板，是 AI 销售话术生成的素材库；
// 按意向分区间(MinIntent/MaxIntent)、触发标签、适用车型匹配，输出 PromptTemplate 与追问 HookTemplate。
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
// 13. 种子知识片段（5+条）
// ============================================================

// ============================================================
// 13. 种子知识片段（42条营销话术）
// ============================================================

// seedKnowledgeFragments 写入 knowledge_fragments 表（幂等：已有片段则跳过）。
// 作用：预置 42 条营销/技术/竞品/ FAQ 知识片段（含 Tags 与 ApplicableModels 的 JSON 数组），
// 作为 AI 话术生成与知识问答的素材库，按分类(Category)与 Sort 排序展示。
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
