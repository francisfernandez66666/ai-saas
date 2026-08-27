// Package db 数据库连接、自动迁移、租户上下文注入与写入自动盖章（RQ/PQ/T/WithPreset/DataScope）。
package db

import (
	"ai-scrm/config"
	"ai-scrm/internal/model"
	"fmt"
	"log"
	"os"
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
	// J10修复(2026-08-26)：生产(release)降级为 Warn，避免 SQL 日志泄露密码哈希/手机号等 PII；
	// 非 release 仍输出 Info 便于调试
	dbLogLevel := logger.Info
	if os.Getenv("GIN_MODE") == "release" {
		dbLogLevel = logger.Warn
	}
	DB, err = gorm.Open(postgres.Open(config.GlobalConfig.Database.DSN()), &gorm.Config{
		Logger: logger.Default.LogMode(dbLogLevel),
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
	ensureRewardClaimIndexes()
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
		&model.IndustryPack{},
		&model.TenantPackBinding{},  // P1 行业包地基（2026-08-25）
		&model.DeptPackBinding{},    // 三级包架构：部门↔部门包绑定（2026-08-26）
		&model.KbFeedbackMaterial{}, // P3 数据飞轮素材池（2026-08-26）
		&model.RewardClaim{},        // P1.5 防薅v2：奖励领取台账（2026-08-26）
		&model.UsageRecord{},
		&model.ApiKey{},
		// ---- CDP 数据底座（Phase P4 真实化前表先就位）
		&model.CdpProfile{},
		&model.CdpTagAssignment{},
		&model.EventLog{},
		&model.IdMapping{},
		&model.CdpTagDefinition{},
		// ---- 流程状态机（SAAS_PLAN §十七）
		&model.FlowStateMachine{},
		// ---- 四级组织架构（P2 组织树）
		&model.Department{},
		// ---- 消息中心（SAAS_PLAN §2.5）
		&model.InboxEvent{},
		&model.MessageEventRecord{},
		// ---- 商业化第一批（2026-08-23，M2/M3）：商业包 + 密码重置码
		&model.Package{},
		&model.PasswordReset{},
		// ---- 商业化第二批（2026-08-24）：用户反馈 + Token 计量底座
		&model.Feedback{},
		&model.UsageLedger{},
		// ---- 邮箱验证码（注册/换绑邮箱）
		&model.EmailVerify{},
		// ---- L3：OneID 身份标识拆表
		&model.CustomerIdentity{},
		// ---- 协议签署台账（注册即同意《用户协议》《隐私政策》，超管审计）
		&model.AgreementSignature{},
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
	// 修复（2026-08-25，M1 配套）：预置语义表移出回填清单。
	// 原注释"届时由 seed 重新生成 0 号预置数据"的承诺未兑现，导致每次启动把
	// templates/features 等预置数据强行搬进默认租户——与 FIXLOG_2026-08-23 Bug1
	// （system_configs 被搬空）同一族问题。这些表的语义归属是 tenant_id=0 全局预置：
	//   - 读取端走 PQ(c)（IN (tid,0) 预置可见）
	//   - 策略引擎召回按 input.TenantID 过滤（预置0全员可见，私有仅本租户）
	//   - 未来行业包 .aipack 导入的出厂内容也落在 0
	// 若继续回填：新租户永远看不到任何预置话术/卖点/标签，且每次启动撤销人工修正。
	// 业务数据表（客户/会话/消息等）保留回填不变。
	tables := []string{
		"customers", "customer_tags", "conversations", "messages",
		"follow_ups", "test_drives", "flow_instances",
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

// ensureRewardClaimIndexes 奖励台账部分唯一索引（幂等，2026-08-26）
// 语义：trial 每(账户/邮箱)一生一次；referral 类每(类型,受邀对象)一次 —— 撞库即拒
func ensureRewardClaimIndexes() {
	stmts := []string{
		"CREATE UNIQUE INDEX IF NOT EXISTS ux_reward_trial_tenant ON reward_claims(tenant_id) WHERE grant_type = 'signup_trial'",
		"CREATE UNIQUE INDEX IF NOT EXISTS ux_reward_trial_email ON reward_claims(email) WHERE grant_type = 'signup_trial' AND email <> ''",
		// P0修复(2026-08-26)：原 ux_reward_ref(grant_type,ref_id) 是全局唯一——free_package 行
		// 复用 ref_id 存包ID后会跨租户误撞（租户1领过包1，其他租户全被拒）。
		// 收窄为仅 referral 类；free_package 由下方按租户索引接管。旧索引存在则替换。
		"DROP INDEX IF EXISTS ux_reward_ref",
		"CREATE UNIQUE INDEX IF NOT EXISTS ux_reward_ref_v2 ON reward_claims(grant_type, ref_id) WHERE ref_id IS NOT NULL AND grant_type LIKE 'referral%'",
		// P0修复(2026-08-26)：free 包直发去重——同租户对同包一生一次（原实现可无限叠加配额）
		"CREATE UNIQUE INDEX IF NOT EXISTS ux_reward_free_pkg ON reward_claims(tenant_id, ref_id) WHERE grant_type = 'free_package'",
		// P0修复(2026-08-26)：订单权益发放台账——每笔订单发放一生一次，
		// 兼作「改单成功但发放失败」的自愈判定锚（重复 confirm 时台账缺行即补发）
		"CREATE UNIQUE INDEX IF NOT EXISTS ux_reward_order_grant ON reward_claims(ref_id) WHERE grant_type = 'order_entitlement'",
	}
	for _, q := range stmts {
		if err := DB.Exec(q).Error; err != nil {
			log.Printf("[migrate] reward 索引执行失败: %v", err)
		}
	}
}
