//go:build ignore

// 说明：本文件为 SQLite→PostgreSQL 一次性迁移工具（已完成使命，2026-09-01 归档至 archived/）。
// 加 build ignore 标签排除出常规构建，避免 go build ./... 每次多编译一个 main 包。
// ============================================================
// SQLite → PostgreSQL 数据迁移工具
//
// 用法：
//
//	go run tools/migrate.go [sqlite文件路径]
//
// 功能：
//  1. 读取 SQLite 源库（默认 ./auto_scrm.db）
//  2. 在 PostgreSQL 目标库 AutoMigrate 全部表结构
//  3. 逐表复制数据（幂等：目标表已有数据则跳过）
//  4. 复制后修正各表 ID 序列，避免新插入 ID 冲突
//
// SaaS 化改造 Phase 0：只做"数据完整搬过去"，tenant_id/tenants 表
// 等多租户结构在 Phase 1 扩展，届时本工具追加租户标记逻辑。
// ============================================================
package main

import (
	"ai-scrm/config"
	"ai-scrm/internal/model"
	"fmt"
	"log"
	"os"

	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// main SQLite 存量数据迁移到 PostgreSQL 的工具入口：读 .env 配置 → 连接两端库 → 逐表搬运
func main() {
	log.Println("========================================")
	log.Println("  SQLite → PostgreSQL 数据迁移工具")
	log.Println("========================================")

	// 1. 加载 .env，获取 PostgreSQL 连接配置
	// 为什么这里也加载？迁移工具独立于主程序运行，需要自己的配置入口
	if err := godotenv.Load(); err != nil {
		log.Println("未找到.env文件，使用默认配置")
	}
	cfg := config.LoadConfig()

	// 2. SQLite 源路径：默认 ./auto_scrm.db，支持命令行参数覆盖
	sourcePath := "./auto_scrm.db"
	if len(os.Args) > 1 {
		sourcePath = os.Args[1]
	}

	// 3. 打开 SQLite 源库
	sqlDB, err := gorm.Open(sqlite.Open(sourcePath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		log.Fatalf("打开SQLite源库失败 [%s]: %v", sourcePath, err)
	}
	log.Printf("SQLite源库已打开: %s", sourcePath)

	// 4. 打开 PostgreSQL 目标库
	pgDB, err := gorm.Open(postgres.Open(cfg.Database.DSN()), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		log.Fatalf("连接PostgreSQL目标库失败: %v", err)
	}
	log.Printf("PostgreSQL目标库已连接: %s/%s", cfg.Database.Host, cfg.Database.Name)

	// 5. 目标库建表（AutoMigrate 全量模型，GORM 自动适配 SERIAL/索引）
	if err := pgDB.AutoMigrate(allModels()...); err != nil {
		log.Fatalf("目标库表结构迁移失败: %v", err)
	}
	log.Println("目标库表结构创建完成")

	// 6. 逐表复制数据（hasSeq=false 的为 string 主键表，无自增序列需修复）
	// 每张表用独立调用，因为泛型切片类型不同
	// 注：sys_users 已通过 seed/tenant_users 迁移，此处不再重复搬运
	total := 0
	total += run(sqlDB, pgDB, "tags", &[]model.Tag{})
	total += run(sqlDB, pgDB, "customer_tags", &[]model.CustomerTag{})
	total += run(sqlDB, pgDB, "tag_rules", &[]model.TagRule{})
	total += run(sqlDB, pgDB, "tag_weight_mappings", &[]model.TagWeightMapping{})
	total += run(sqlDB, pgDB, "customers", &[]model.Customer{})
	run(sqlDB, pgDB, "templates", &[]model.Template{})
	run(sqlDB, pgDB, "features", &[]model.Feature{})
	total += run(sqlDB, pgDB, "brands", &[]model.Brand{})
	total += run(sqlDB, pgDB, "car_models", &[]model.CarModel{})
	total += run(sqlDB, pgDB, "model_specs", &[]model.ModelSpec{})
	total += run(sqlDB, pgDB, "competitor_compares", &[]model.CompetitorCompare{})
	total += run(sqlDB, pgDB, "knowledge_fragments", &[]model.KnowledgeFragment{})
	total += run(sqlDB, pgDB, "flow_definitions", &[]model.FlowDefinition{})
	total += run(sqlDB, pgDB, "flow_instances", &[]model.FlowInstance{})
	total += run(sqlDB, pgDB, "conversations", &[]model.Conversation{})
	total += run(sqlDB, pgDB, "messages", &[]model.Message{})
	total += run(sqlDB, pgDB, "follow_ups", &[]model.FollowUp{})
	total += run(sqlDB, pgDB, "test_drives", &[]model.TestDrive{})
	total += run(sqlDB, pgDB, "system_configs", &[]model.SystemConfig{})
	total += run(sqlDB, pgDB, "cdp_profiles", &[]model.CdpProfile{})
	total += run(sqlDB, pgDB, "cdp_tag_definitions", &[]model.CdpTagDefinition{})
	total += run(sqlDB, pgDB, "cdp_tag_assignments", &[]model.CdpTagAssignment{})
	total += run(sqlDB, pgDB, "event_logs", &[]model.EventLog{})
	total += run(sqlDB, pgDB, "id_mappings", &[]model.IdMapping{})

	log.Printf("========================================")
	log.Printf("迁移完成，共复制 %d 行数据", total)
	log.Println("========================================")
}

// run 执行单表迁移并返回新增行数（跳过表返回0）；出错直接终止
func run[T any](src, dst *gorm.DB, table string, rows *[]T) int {
	n, err := migrateTable(src, dst, table, rows)
	if err != nil {
		log.Fatalf("迁移表 %s 失败: %v", table, err)
	}
	return n
}

// allModels 返回全部需要建表的模型列表
// 顺序与 internal/db/database.go 的 autoMigrate 保持一致
func allModels() []interface{} {
	return []interface{}{
		&model.User{},
		&model.Tag{},
		&model.TagRule{},
		&model.TagWeightMapping{},
		&model.Customer{},
		&model.CustomerTag{},
		&model.Template{},
		&model.Feature{},
		&model.Brand{},
		&model.CarModel{},
		&model.ModelSpec{},
		&model.CompetitorCompare{},
		&model.KnowledgeFragment{},
		&model.FlowDefinition{},
		&model.FlowInstance{},
		&model.Conversation{},
		&model.Message{},
		&model.FollowUp{},
		&model.TestDrive{},
		&model.SystemConfig{},
		&model.CdpProfile{},
		&model.CdpTagDefinition{},
		&model.CdpTagAssignment{},
		&model.EventLog{},
		&model.IdMapping{},
	}
}

// migrateTable 复制单张表数据
// 幂等设计：目标表已有数据 → 返回 0 表示跳过；出错返回错误
func migrateTable[T any](src, dst *gorm.DB, table string, rows *[]T) (int, error) {
	// 幂等检查：目标已有数据则跳过
	var count int64
	countErr := dst.Table(table).Count(&count).Error
	if countErr != nil {
		// 表不存在，将在后续 Find+Create 中自动创建
		count = 0
	}
	if count > 0 {
		log.Printf("  %-22s 目标已有 %d 行，跳过", table, count)
		return 0, nil
	}

	// 读取源表全部行
	if err := src.Table(table).Find(rows).Error; err != nil {
		return -1, fmt.Errorf("读取SQLite %s: %w", table, err)
	}
	n := len(*rows)
	if n == 0 {
		return 0, nil
	}

	// 写入目标表
	if err := dst.Table(table).Create(rows).Error; err != nil {
		return -1, fmt.Errorf("写入PG %s: %w", table, err)
	}

	// 修复 ID 序列：SQLite AUTOINCREMENT 主键在 PG 中需要手动对齐 sequence
	if err := fixSequence(dst, table); err != nil {
		return -1, err
	}

	log.Printf("  %-22s 复制 %d 行", table, n)
	return n, nil
}

// fixSequence 修正 PostgreSQL 自增序列
// 用 pg_get_serial_sequence 找到表实际关联的序列，再把 last_value 置为当前最大ID
func fixSequence(db *gorm.DB, table string) error {
	var seq string
	if err := db.Raw(fmt.Sprintf(
		"SELECT COALESCE(pg_get_serial_sequence('%s', 'id'), '')", table)).Scan(&seq).Error; err != nil {
		return fmt.Errorf("查询 %s 序列名: %w", table, err)
	}
	if seq == "" {
		// 该表无自增ID列（如 string 主键），无需修复
		return nil
	}
	if err := db.Exec(fmt.Sprintf(
		"SELECT setval('%s', COALESCE((SELECT MAX(id) FROM %s), 1))", seq, table)).Error; err != nil {
		return fmt.Errorf("修复 %s 序列: %w", table, err)
	}
	return nil
}
