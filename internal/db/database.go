package db

import (
	"ai-scrm/config"
	"ai-scrm/internal/model"
	"fmt"
	"log"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ============================================================
// 数据库初始化层
// 为什么单独封装？统一管理数据库连接、迁移、事务
// ============================================================

// DB 全局数据库连接
var DB *gorm.DB

// Init 初始化数据库
// 步骤：1.连接PostgreSQL  2.自动迁移表结构  3.配置连接池
func Init() error {
	var err error
	// 连接PostgreSQL数据库
	// SaaS 化改造：从 SQLite 单文件切换为 PostgreSQL，支持多租户 + 并发写入
	DB, err = gorm.Open(postgres.Open(config.GlobalConfig.Database.DSN()), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Info), // 输出SQL日志，方便调试
	})
	if err != nil {
		log.Printf("数据库连接失败: %v", err)
		return err
	}

	log.Println("数据库连接成功")

	// 注册写入自动盖章回调（context 租户 → TenantID 零值填充，见 tenant_ctx.go）
	registerTenantStampCallback(DB)

	// 配置连接池
	// 为什么配置？PostgreSQL 需要连接池控制并发写入，SQLite 是文件锁不需要
	sqlDB, err := DB.DB()
	if err != nil {
		log.Printf("获取底层连接失败: %v", err)
		return err
	}
	sqlDB.SetMaxOpenConns(config.GlobalConfig.Database.MaxOpenConns)
	sqlDB.SetMaxIdleConns(config.GlobalConfig.Database.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(time.Duration(config.GlobalConfig.Database.ConnMaxLifetime) * time.Second)

	// 自动迁移表结构
	// GORM的AutoMigrate会自动创建表、添加缺失的字段和索引
	// 注意：不会删除已有的字段，是安全的
	err = autoMigrate()
	if err != nil {
		log.Printf("数据库迁移失败: %v", err)
		return err
	}

	log.Println("数据库表结构迁移完成")

	// 清理旧版单列唯一索引（AutoMigrate 只增不删，需手动降级）
	// - system_configs.key 原全局唯一 → 已改为 (tenant_id, key) 联合唯一
	//   不删旧索引会导致不同租户无法配置同名参数
	// 说明：正式的版本化 SQL 迁移目录（migrations/）在 Phase P0 建立，
	// 本处为 Phase S 阻塞项的临时处置
	dropLegacyIndexes()

	// 存量业务数据回填默认租户（幂等，见函数注释）
	backfillTenantIDs()

	return nil
}

// dropLegacyIndexes 删除多租户改造后不再兼容的旧索引
func dropLegacyIndexes() {
	legacy := []string{
		"idx_system_configs_key", // 旧 key 全局唯一索引，被 idx_tenant_key 取代
	}
	for _, idx := range legacy {
		if err := DB.Exec(fmt.Sprintf("DROP INDEX IF EXISTS %s", idx)).Error; err != nil {
			log.Printf("[migrate] 清理旧索引 %s 失败: %v", idx, err)
		} else {
			log.Printf("[migrate] 已清理旧索引 %s", idx)
		}
	}
}

// autoMigrate 自动迁移所有数据表
// 按顺序创建，确保外键依赖关系正确
func autoMigrate() error {
	return DB.AutoMigrate(
		&model.User{},
		&model.Tag{},
		&model.TagRule{},
		&model.TagWeightMapping{},
		&model.Customer{},
		&model.CustomerTag{},
		&model.Template{},
		&model.Feature{},
		// ---- 车型品牌知识库
		&model.Brand{},
		&model.CarModel{},
		&model.ModelSpec{},
		&model.CompetitorCompare{},
		&model.KnowledgeFragment{},
		// ---- 流程&对话
		&model.FlowDefinition{},
		&model.FlowInstance{},
		&model.Conversation{},
		&model.Message{},
		&model.FollowUp{},
		&model.TestDrive{},
		// ---- 系统配置（后台管理可调参数）
		&model.SystemConfig{},
		// ---- SaaS 租户订阅
		&model.Tenant{},
		&model.SubscriptionPlan{},
		// ---- SaaS 计费/审计/API Key（Phase P0 补齐；支付逻辑后置但表先就位）
		&model.TenantAuditLog{},
		&model.BillingOrder{},
		&model.UsageRecord{},
		&model.ApiKey{},
		// ---- CDP 数据底座（Phase P4 真实化前表先就位）
		&model.CdpProfile{},
		&model.CdpTagAssignment{},
		&model.EventLog{},
		// ---- 流程状态机（SAAS_PLAN §十七）
		&model.FlowStateMachine{},
		// ---- 四级组织架构（P2 组织树）
		&model.Department{},
		// ---- 消息中心（SAAS_PLAN §2.5）
		&model.InboxEvent{},
		&model.MessageEventRecord{},
	)
}

// BackfillTenantIDs 存量业务数据回填租户ID（导出供 main 在 seed 之后再次调用）
// 背景：多租户改造前入库的数据没有 tenant_id（为0），统一归入默认租户（rox-sales）
// 为什么每次启动都跑？幂等且廉价（只 UPDATE tenant_id=0 的行），同时能自愈
// 改造过渡期内新写入但未带 tenant_id 的数据（Phase P1 会逐点修复写入路径）
func BackfillTenantIDs() {
	backfillTenantIDs()
}

// backfillTenantIDs 存量业务数据回填租户ID
// 背景：多租户改造前入库的数据没有 tenant_id（为0），统一归入默认租户（rox-sales）
// 为什么每次启动都跑？幂等且廉价（只 UPDATE tenant_id=0 的行），同时能自愈
// 改造过渡期内新写入但未带 tenant_id 的数据（Phase P1 会逐点修复写入路径）
func backfillTenantIDs() {
	// 1. 找默认租户（id 最小的 active 租户，即 rox-sales）
	var tid uint
	err := DB.Model(&model.Tenant{}).Where("status IN ?", []string{"active", "trial"}).
		Order("id ASC").Limit(1).Pluck("id", &tid).Error
	if err != nil || tid == 0 {
		log.Println("[backfill] 未找到默认租户，跳过存量数据回填（首次启动 seed 会创建）")
		return
	}

	// 2. 存量用户绑定租户：tenant_users 旧行 tenant_id 为 NULL（sys_users 时代数据），
	//    统一归入默认租户；super_admin 保持 NULL（平台级，跨租户由中间件裁决）
	res := DB.Exec(`UPDATE tenant_users SET tenant_id = ? WHERE tenant_id IS NULL AND role IS DISTINCT FROM 'super_admin'`, tid)
	if res.Error != nil {
		log.Printf("[backfill] 用户租户绑定失败: %v", res.Error)
	} else if res.RowsAffected > 0 {
		log.Printf("[backfill] 已将 %d 个存量用户绑定到默认租户 %d", res.RowsAffected, tid)
	}

	// 3. 业务表回填：tenant_id=0 → 默认租户
	// 注意：tags/templates/features/brands 等预置类表本次也一并归入默认租户，
	// 原因：这些行是单租户时期为 rox-sales 创建的业务数据；"系统预置=0 全租户可见"
	// 的语义从 Phase P1 引入 WithPreset 查询时才生效，届时由 seed 重新生成 0 号预置数据
	tables := []string{
		"customers", "customer_tags", "conversations", "messages",
		"follow_ups", "test_drives", "flow_instances", "flow_definitions",
		"tags", "tag_rules", "tag_weight_mappings", "templates", "features",
		"brands", "car_models", "model_specs", "competitor_compares",
		"knowledge_fragments", "system_configs",
	}
	for _, t := range tables {
		res := DB.Exec(fmt.Sprintf("UPDATE %s SET tenant_id = ? WHERE tenant_id = 0", t), tid)
		if res.Error != nil {
			// 表可能尚未创建（新装环境 AutoMigrate 已建则不会错），仅告警不阻断
			log.Printf("[backfill] 表 %s 回填失败: %v", t, res.Error)
			continue
		}
		if res.RowsAffected > 0 {
			log.Printf("[backfill] 表 %s 回填 %d 行 → 租户 %d", t, res.RowsAffected, tid)
		}
	}
}

// GetDB 获取数据库连接
func GetDB() *gorm.DB {
	return DB
}
