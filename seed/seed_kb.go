// Package seed：seed 模块（自动补包注释）。
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

	// P1-4 去硬编码：知识片段 demo 数据已外置 seed/data（随 SeedIndustry 切换，默认 auto_rox）
	fragments := SeedIndustry.KnowledgeFragments

	for _, frag := range fragments {
		db.DB.Create(&frag)
	}

	log.Printf("已创建 %d 条知识片段", len(fragments))
}
