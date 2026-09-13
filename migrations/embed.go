// Package migrations 版本化 SQL 迁移（C4 迁移纪律，2026-09-12）
// 纪律：**凡破坏性变更（删列/改型/回填/索引替换/新表预登记）必须落 migrations/0xx_*.sql（up/down 成对）**；
// AutoMigrate 只承担开发期便利增量，不再作为唯一 schema 来源。
// 通过 go:embed 把 SQL 打进二进制，保证 Docker/裸机部署无需携带 migrations 目录。
package migrations

import "embed"

// FS 嵌入全部 .sql 迁移文件（命名约定：NNN_名称.up.sql / NNN_名称.down.sql，按号升序执行）
//
//go:embed *.sql
var FS embed.FS
