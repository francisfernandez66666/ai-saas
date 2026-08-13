package db

import (
	"ai-scrm/config"
	"ai-scrm/internal/model"
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
	return nil
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
	)
}

// GetDB 获取数据库连接
func GetDB() *gorm.DB {
	return DB
}
