// Package db 数据库连接、自动迁移、租户上下文注入与写入自动盖章（RQ/PQ/T/WithPreset/DataScope）。
package db

import (
	"ai-scrm/config"
	"ai-scrm/internal/logx"
	"ai-scrm/internal/model"
	"fmt"
	"log"
	"strings"
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
	// 复核批 P0-1(2026-09-18)：改为"显式 debug/test 才有 Info"——GIN_MODE 不设时旧口径
	// 隐式按 debug 全量打 SQL（PII 落日志面），与 mock-pay 同族 fail-open，统一收口。
	dbLogLevel := logger.Warn
	if config.IsDevModeConfirmed() {
		dbLogLevel = logger.Info
	}
	DB, err = gorm.Open(postgres.Open(config.GlobalConfig.Database.DSN()), &gorm.Config{
		// C3(2026-09-12)：包一层脱敏 logger，Info 态 SQL 参数(手机号/身份证/邮箱)掩码后再落盘
		Logger: logx.NewGormLogger(logger.Default.LogMode(dbLogLevel)),
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

	// C4 迁移纪律(2026-09-12)：版本化 SQL 迁移（migrations/*.up.sql）在 AutoMigrate 之后应用。
	// 后置原因见 migrations.go：002 的 RLS 策略依赖 AutoMigrate 已建出的全部租户表。
	if err := MigrateUp(); err != nil {
		log.Printf("[migrate] 版本化迁移失败: %v", err)
		return err
	}

	// 清理旧版单列唯一索引（AutoMigrate 只增不删，需手动降级）
	// - system_configs.key 原全局唯一 → 已改为 (tenant_id, key) 联合唯一
	//   不删旧索引会导致不同租户无法配置同名参数
	// 说明：正式的版本化 SQL 迁移目录（migrations/）在 Phase P0 建立，
	// 本处为 Phase S 阻塞项的临时处置
	dropLegacyIndexes()

	// 多租户复合唯一索引收敛（2026-09-22 复核 P1-2）：删掉历史遗留的单列全局唯一索引，
	// 重建为 (tenant_id, ...) 复合唯一。详见 ensureTenantUniqueIndexes 注释。
	ensureTenantUniqueIndexes()

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

/*
ensureTenantUniqueIndexes 把多租户复合唯一索引真正落到库上

背景（2026-09-22 复核 P1-2，实测）：多张租户级表的"复合唯一索引"在存量库里
实际是【单列全局唯一】——于是租户 A 建了标签"价格敏感"，租户 B 就再也建不了，
报 duplicate key value violates unique constraint "idx_tag_tenant_name"。对 SaaS 是硬伤。

根因不是模型写错：实测在全新库上用同样的 GORM 标签，AutoMigrate 能正确建出
(tenant_id, name) 复合索引。真正原因是 AutoMigrate **只增不删**——早期版本建出的
单列唯一索引在模型改为复合后仍然留在库里；又因为 AutoMigrate 按索引名判断"已存在"，
同名的正确复合索引永远不会被补建。car_models 库里同时存在正确的 (tenant_id, code)
与错误的单列 (code)，正是这条路径的活证据。

处置：显式 DROP 错索引 → CREATE UNIQUE INDEX IF NOT EXISTS 正确复合索引。
两步都幂等，且建索引失败只告警不阻断启动（存量脏数据要先由运营清理，
启动阶段硬失败会把整个服务拖死）。

⚠ 但"删自己再建自己"这条路径必须禁止（2026-09-23 假红根因）：上表的 drop 列里
混着正确索引自己的名字（idx_tag_tenant_name / idx_brand_tenant_name 等，历史存量里
它们曾承载单列定义），于是每次 Init 都会先 DROP 掉一个**已经是正确形态**的索引再重建。
DROP 与 CREATE 各自 autocommit，中间是一段无约束空窗：
  - 多个 go test 进程并行（每个都跑一遍 Init）、或多实例同时重启时，另一进程的
    pg_indexes 结构断言正好落在空窗里 → 报"缺少含 tenant_id 的复合唯一索引"（假红）；
  - 更糟的是空窗内真实写入可以插进重复数据，令随后的 CREATE UNIQUE INDEX 永久失败
    （只告警不阻断），约束就真的没了。

故改为"先看现状再决定动作"：已是 (tenant_id, …) 正确复合唯一的索引一律不删，
目标索引定义正确时连 CREATE 都不重发（避免无谓的锁与索引重建）。
*/
func ensureTenantUniqueIndexes() {
	ensureTenantUniqueIndexesFor(tenantUniqueTargets)
}

// tenantUnique 描述一条"租户级列该按 (tenant_id, col) 唯一"的收敛目标。
type tenantUnique struct {
	table string   // 表名
	drop  []string // 需删除的历史错索引（单列全局唯一 / 同名旧版）
	index string   // 正确的复合唯一索引名
	cols  string   // 索引列，tenant_id 必须为首列
}

// tenantUniqueTargets 收敛目标清单（与 internal/db 结构断言测试的清单同源）。
// 抽成包级变量+下方 For 变体，是为了让"错定义被重建、正确定义绝不被删"
// 这两条能在临时表上被测到——否则本函数只能靠真库快照证明，无法构造"故意建错"。
var tenantUniqueTargets = []tenantUnique{
	// tags：库里曾同时存在 idx_tags_name/code（旧命名）与 idx_tag_tenant_name/code（单列版）
	{"tags", []string{"idx_tags_name", "idx_tags_code", "idx_tag_tenant_name", "idx_tag_tenant_code"},
		"idx_tag_tenant_name", "(tenant_id, name)"},
	{"tags", nil, "idx_tag_tenant_code", "(tenant_id, code)"},
	{"brands", []string{"idx_brands_name", "idx_brands_code", "idx_brand_tenant_name", "idx_brand_tenant_code"},
		"idx_brand_tenant_name", "(tenant_id, name)"},
	{"brands", nil, "idx_brand_tenant_code", "(tenant_id, code)"},
	// car_models：正确版 idx_carmodel_tenant_code 已存在，这里只清错的两条
	{"car_models", []string{"idx_car_models_code", "idx_model_tenant_code"},
		"idx_carmodel_tenant_code", "(tenant_id, code)"},
	{"cdp_tag_definitions", []string{"idx_cdp_tag_tenant_code"},
		"idx_cdp_tag_tenant_code", "(tenant_id, code)"},
	{"cdp_profiles", []string{"idx_cdp_profile_tenant_cdpid"},
		"idx_cdp_profile_tenant_cdpid", "(tenant_id, cdp_id)"},
	{"flow_definitions", []string{"idx_flow_definitions_code", "idx_flow_tenant_code"},
		"idx_flow_tenant_code", "(tenant_id, code)"},
}

// ensureTenantUniqueIndexesFor 按给定目标清单收敛索引（见 ensureTenantUniqueIndexes 注释）。
func ensureTenantUniqueIndexesFor(targets []tenantUnique) {
	for _, t := range targets {
		defs := currentIndexDefsByTable(t.table)
		targetOK := isTenantUniqueOn(defs[t.index], t.cols)
		for _, idx := range t.drop {
			// 分两类判："目标索引自己"只要定义正确就绝不删（删→建空窗是本轮假红根因）；
			// 定义不对才删掉重建，哪怕它已经挂着某个 (tenant_id, 别的列) 的错列组合。
			if idx == t.index {
				if targetOK {
					continue
				}
			} else if isTenantUniqueOn(defs[idx], "(tenant_id, ") {
				// 历史别名但已是租户级复合唯一：等价形态，留着它不伤隔离，删它反而造空窗
				continue
			}
			if _, existed := defs[idx]; !existed {
				continue // 库里根本没有这条索引：不发无意义的 DDL，也不打"已删除"误导排障
			}
			if err := DB.Exec(fmt.Sprintf("DROP INDEX IF EXISTS %s", idx)).Error; err != nil {
				log.Printf("[migrate] 删除历史错索引 %s 失败: %v", idx, err)
			} else {
				// 把被删掉的原定义一起记：下次再看到这条日志能直接判断"删的是错索引，
				// 不是刚建好的正确索引"（本轮假红就是从这句无差别的"已删除"里查出来的）。
				log.Printf("[migrate] 已删除历史错索引 %s（原定义: %s）", idx, defs[idx])
			}
		}
		if targetOK {
			continue
		}
		sql := fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s %s", t.index, t.table, t.cols)
		if err := DB.Exec(sql).Error; err != nil {
			// 存量脏数据（同租户内重名）会让这里失败：告警而非中断启动，
			// 否则一台库脏就全站起不来。
			log.Printf("[migrate] 建复合唯一索引 %s(%s) 失败（需先清理同租户内重复数据）: %v", t.index, t.cols, err)
		}
	}
}

// currentIndexDefsByTable 取表上现有索引的 name => indexdef 快照（pg_indexes 权威口径）。
// 查询失败返回空 map，调用方据此退化为"按原路径重建"——宁可多发一次幂等 DDL，
// 也不要在看不见现状时盲删。
func currentIndexDefsByTable(table string) map[string]string {
	out := map[string]string{}
	rows, err := DB.Raw("SELECT indexname, indexdef FROM pg_indexes WHERE tablename = ?", table).Rows()
	if err != nil {
		log.Printf("[migrate] 查询 %s 索引现状失败(将按重建路径处理): %v", table, err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			log.Printf("[migrate] 扫描 %s 索引行失败(将按重建路径处理): %v", table, err)
			return map[string]string{}
		}
		out[name] = def
	}
	return out
}

// isTenantUniqueOn 判断 indexdef 是否是"以 tenant_id 为首列、列序与 cols 完全一致"的 UNIQUE 索引。
// cols 传 "(tenant_id, name)" 这类目标列串，或传 "(tenant_id, " 这类前缀（用于"任一租户级唯一都别删"）。
// 必须同时含 UNIQUE：非唯一同名索引若被当作正确形态跳过，约束就永久缺失了。
func isTenantUniqueOn(def, cols string) bool {
	return def != "" && strings.Contains(def, "UNIQUE") && strings.Contains(def, cols)
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
		&model.UsageFlushRetry{}, // P0-1(2026-09-20)：计量扣减挂账表，弃批改延后扣
		// ---- 邮箱验证码（注册/换绑邮箱）
		&model.EmailVerify{},
		// ---- L3：OneID 身份标识拆表
		&model.CustomerIdentity{},
		// ---- 协议签署台账（注册即同意《用户协议》《隐私政策》，超管审计）
		&model.AgreementSignature{},
		// ---- 企微/微信通道接入（W2/W6，2026-09-12；建表真源见 migrations/003、004，AutoMigrate 幂等共存）
		&model.Channel{},
		&model.ChannelOutbound{},
		&model.ChannelIdentity{},
		&model.ChannelInboundMsg{}, // D4：入站幂等去重（回调重推防双回复）
		// ---- PIPL 删除请求（C2，2026-09-12；建表真源见 migrations/006，AutoMigrate 幂等共存）
		&model.DeletionRequest{},
		// ---- 出站事件 webhook（D6，2026-09-12；建表真源见 migrations/007）
		&model.TenantWebhook{},
		&model.WebhookDelivery{},
		// ---- 行业包质量归因（D9，2026-09-13；建表真源见 migrations/009/010）
		&model.ReplyAttribution{},
		&model.PackStatSnapshot{},
		// ---- 主动触达任务（触达最小闭环，2026-09-23；调度索引见 migrations/020）
		&model.OutreachTask{},
		// ---- 用量预警留痕 + 到期催缴状态机（D3，2026-09-23；去重唯一键与 running 部分索引见 migrations/021）
		&model.UsageAlert{},
		&model.BillingDunning{},
		// ---- 获客活码：码配置 + 扫码事件（获客批，2026-09-23；三条部分索引与 customers 归因列见 migrations/022）
		&model.AcquisitionCode{},
		&model.AcquisitionScan{},
		// ---- 商机 + 报价单（商机批，2026-09-23；在途唯一的部分索引见 migrations/023）
		&model.Opportunity{},
		&model.Quote{},
		// ---- 会话存档（E8，2026-09-24；channels 存档五列与两条索引见 migrations/024）
		&model.ChatArchiveRecord{},
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
	// R15 修复(2026-09-11)：tenant_users.email 此前无唯一约束，注册"重复邮箱"预检在事务外——
	// 并发同邮箱两请求可同时通过预检各自建号（OneID 邮箱锚被击穿，注册礼邮箱维度防撞也失效）。
	// 补部分唯一索引（仅约束非空邮箱，存量多用户空邮箱不受影响）。
	if err := DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS ux_tenant_users_email_nonempty ON tenant_users(email) WHERE email <> ''`).Error; err != nil {
		log.Printf("[migrate] tenant_users.email 唯一索引创建失败(可能存在存量重复邮箱，需人工清洗): %v", err)
	}
	backfillOrderEntitlementLedger()
}

// backfillOrderEntitlementLedger R10/R2 配套迁移(2026-09-11)：为存量 paid 订单回填
// order_entitlement 发放台账。新对账器以"paid 且缺台账行"为唯一补发信号，若不回填，
// 上线首轮会把全部历史订单重发一遍（历史订单当年确已发放，只是无台账可证）。
// 代价：历史上极少数"到账但发放失败"的单会被标记为已发放——该窗口仅存在于旧代码期，
// 新代码发放全程台账先行单事务，此后缺行即真相。幂等：唯一索引 + NOT EXISTS 双保险。
func backfillOrderEntitlementLedger() {
	res := DB.Exec(`
		INSERT INTO reward_claims (grant_type, tenant_id, email, ref_id, note, created_at)
		SELECT 'order_entitlement', o.tenant_id, '', o.id, 'order:' || o.order_no, NOW()
		FROM billing_orders o
		WHERE o.status = 'paid' AND o.package_id > 0 AND o.tenant_id IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM reward_claims r
		                  WHERE r.grant_type = 'order_entitlement' AND r.ref_id = o.id)
		ON CONFLICT DO NOTHING`)
	if res.Error != nil {
		log.Printf("[migrate] order_entitlement 台账回填失败: %v", res.Error)
		return
	}
	if res.RowsAffected > 0 {
		log.Printf("[migrate] order_entitlement 台账回填 %d 行（存量已发放订单）", res.RowsAffected)
	}
}
