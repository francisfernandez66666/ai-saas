package seed

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/pkg/utils"
	"golang.org/x/crypto/bcrypt"
	"log"
)

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
		MaxAICalls:     10000,
		UsedAICalls:    0,
		UsedCustomers:  0,
	}
	if err := db.DB.Create(defaultTenant).Error; err != nil {
		log.Printf("创建默认租户失败: %v", err)
		return
	}
	log.Println("已创建默认租户：name=rox-sales code=default")
}

// ============================================================
// 1.5 商业包预置（商业化 M2，2026-08-23）：4 档演示包
// 幂等：按 code 判断存在则跳过（后台可改价格，seed 不回写覆盖运营配置）
// ============================================================
func seedPackages() {
	// P1.5 Token统一计费（2026-08-26）：套餐 token 档位化（售价≈上游成本×3）
	// 旧"次"字段保留只读兼容历史；新发放走 TokenAmount
	packages := []model.Package{
		{Code: "trial_500", Name: "注册试用包", PType: model.PackageTypeFree, AICalls: 500, TokenAmount: 300000, PriceCents: 0, DurationDays: 0,
			Description: "注册即送30万token（14天有效），体验完整智能接待能力", SortOrder: 1},
		{Code: "starter_1000", Name: "入门包月", PType: model.PackageTypePaid, AICalls: 1000, TokenAmount: 3000000, PriceCents: 9900, DurationDays: 30,
			Description: "每月300万token，适合1-5人销售团队", SortOrder: 2},
		{Code: "std_5000", Name: "标准包月", PType: model.PackageTypePaid, AICalls: 5000, TokenAmount: 15000000, PriceCents: 39900, DurationDays: 30,
			Description: "每月1500万token，适合10人以上团队规模化使用", SortOrder: 3},
		{Code: "booster_1000", Name: "AI加油包", PType: model.PackageTypeIncrement, AICalls: 1000, TokenAmount: 3000000, PriceCents: 19900, DurationDays: 0,
			Description: "一次性充值300万token，永久有效买断制", SortOrder: 4},
	}
	inserted := 0
	for i := range packages {
		var count int64
		db.DB.Model(&model.Package{}).Where("code = ?", packages[i].Code).Count(&count)
		if count > 0 {
			continue
		}
		if err := db.DB.Create(&packages[i]).Error; err != nil {
			log.Printf("创建商业包 %s 失败: %v", packages[i].Code, err)
			continue
		}
		inserted++
	}
	if inserted > 0 {
		log.Printf("已预置 %d 个商业包（试用/包月/增量）", inserted)
	}
}

// seedUsers 写入 users 表（幂等：已有用户则跳过，仅做旧账户兼容升级）。
// 副作用：创建 1 个 tenant_id=NULL 的超级管理员 admin + 3 个归属默认租户的销售；
// 若已有数据，则尝试将老 role='admin' 升级为 super_admin，并给仍用出厂弱密码的账号补置首登强改密标记。
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
		// M3 存量补齐：仍在用出厂弱密码的默认账号补置首登强改密标记
		// 注意：必须校验密码哈希确实等于出厂值，否则用户已自改密码会被误标记
		for _, name := range []string{"admin", "sales1", "sales2", "sales3"} {
			var u model.User
			if err := db.DB.Where("username = ?", name).First(&u).Error; err != nil {
				continue
			}
			factoryPwd := "sales123"
			if name == "admin" {
				factoryPwd = "admin123"
			}
			if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(factoryPwd)) == nil {
				db.DB.Model(&u).Update("must_change_password", true)
				log.Printf("出厂弱密码账号 %s 已置首登强改密标记（M3 安全策略）", name)
			}
		}
		log.Println("用户数据已存在，跳过写入")
		return
	}

	// 获取默认租户ID（租户由 seedTenants() 统一创建，见 InitSeedData）
	var defaultTenant model.Tenant
	db.DB.First(&defaultTenant, "code = ?", "default")
	defaultTenantID := defaultTenant.ID

	// 管理员 - role=super_admin，tenant_id=0 表示超级管理员（全局不受租户隔离约束）
	// M3：默认演示账号 must_change_password=true（首登强制改密）
	adminPwd, _ := utils.HashPassword("admin123")
	admin := &model.User{
		Username:           "admin",
		PasswordHash:       adminPwd,
		RealName:           "系统管理员",
		Role:               "super_admin",
		Phone:              "13800000000",
		Email:              "admin@autoscrm.com",
		Department:         "运营部",
		TenantID:           nil, // NULL 表示超级管理员
		Status:             1,
		MustChangePassword: true,
	}
	db.DB.Create(admin)
	log.Println("已创建超级管理员: admin (role=super_admin, tenant_id=NULL)")

	// 销售 - 归属默认租户
	salesPwd, _ := utils.HashPassword("sales123")
	salesNames := []string{"张伟", "李娜", "王磊"}
	salesPhones := []string{"13800000001", "13800000002", "13800000003"}

	for i := 0; i < 3; i++ {
		sales := &model.User{
			Username:           "sales" + string(rune('1'+i)),
			PasswordHash:       salesPwd,
			RealName:           salesNames[i],
			Role:               model.RoleUser,
			Phone:              salesPhones[i],
			Department:         "销售部",
			TenantID:           &defaultTenantID, // 归属默认租户
			Status:             1,
			MustChangePassword: true, // M3：演示账号同样首登强改密
		}
		db.DB.Create(sales)
		log.Println("已创建销售账号: sales" + string(rune('1'+i)) + " (tenant_id=" + string(rune('1'+defaultTenantID)) + ")")
	}

	log.Println("已创建 1个超级管理员 + 3个销售账号")
}
