// Package seed 种子数据填充：本文件为车型域——预置品牌/车型/竞品数据（约 2400 行大文件）。
package seed

import (
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

	// 查找品牌ID（按 BrandCode 解析为 BrandID）
	getBrandID := func(code string) uint {
		var brand model.Brand
		if err := db.DB.Where("code = ?", code).First(&brand).Error; err == nil {
			return brand.ID
		}
		return 1
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
}

// ============================================================
// 11. 种子规格参数（每款车10+核心参数）
// ============================================================

// ============================================================
// 11. 种子规格参数（361条，覆盖13款车型）
// ============================================================

// seedModelSpecs 写入 model_specs 表（幂等：已有规格则整块跳过）。
// 注：规格参数数据为 auto_rox 行业 demo（P1-4 去硬编码待迁移至 seed/data，随 SeedIndustry 切换）。
// 作用：为 13 款车型逐条写入核心规格参数（动力/车身/电池/底盘/越野/智驾/座舱/舒适/户外），
// 按 Category 分组、Sort 排序；被车型详情页与 AI 对比话术检索使用。
// 注意：getModelID 查不到车型时回退返回 1，避免外键空值导致整条写入失败。
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

// seedCompetitorCompares 写入 competitor_compares 表（幂等：已有对比则跳过）。
// 作用：预置极石 vs 坦克/理想/方程豹/仰望 的多组对比话术（58 组），每组 Content 为
// JSON 数组（aspect/our_value/their_value/conclusion 多维对比），ContainsPrice 标记是否含价格。
// 供 AI 对比锚话术与销售对比物料使用；getModelID 同样以 1 作为查不到时的兜底。
// 注：竞品对比数据为 auto_rox 行业 demo（P1-4 去硬编码待迁移至 seed/data，随 SeedIndustry 切换）。
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
