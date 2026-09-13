// Package db 版本化 SQL 迁移执行器（C4 迁移纪律，2026-09-12）
// 背景：项目此前 schema 全靠 GORM AutoMigrate（只增列不删、改不了型），
// 破坏性变更（删列/改型/回填/索引替换/新表预登记）无落地处，迁移纪律缺失。
// 本执行器把 migrations/*.up.sql 纳入版本管理：
//   - schema_migrations 表记录已应用版本，按文件名升序执行未应用项；
//   - 每个迁移文件自带 BEGIN/COMMIT，整段单 Exec 原子应用；失败即中断（fail-fast）。
//
// 启动顺序：autoMigrate()（建/补最新表）→ MigrateUp()（破坏性/回填/索引改造）。
// 之所以后置：002_rls_coverage 的 FORCE RLS + 策略 USING(tenant_id) 依赖所有租户表已由
// AutoMigrate 建出，先置会在空库上报 relation not exists；后置则对存量库与全新库都幂等。
// 001/002 均为 IF NOT EXISTS / DROP IF EXISTS + CREATE，重放无害，首次纳入即被记录。
package db

import (
	"fmt"
	"io/fs"
	"log"
	"sort"
	"strings"

	"gorm.io/gorm"

	"ai-scrm/migrations"
)

// MigrateUp 应用 migrations 嵌入 FS 中所有未执行的 .up.sql（默认入口）
func MigrateUp() error {
	return MigrateUpFrom(DB, migrations.FS)
}

// MigrateDown 回退 migrations 嵌入 FS 中已执行的 .down.sql（默认按版本号倒序）
func MigrateDown(steps int) error {
	return MigrateDownFrom(DB, migrations.FS, steps)
}

// baselineVersions 版本化纪律建立之前的历史快照文件（存量库首跑只标记不重放）。
// 约定：仅这两个是"基线描述"，2026-09-12 之后新增的 0xx 一律是真实可执行迁移。
var baselineVersions = []string{"001_baseline", "002_rls_coverage"}

// hasFile 判断 entries（含 .up.sql 后缀）里是否存在某版本的 up 脚本
func hasFile(entries []string, version string) bool {
	want := version + ".up.sql"
	for _, e := range entries {
		if e == want {
			return true
		}
	}
	return false
}

// MigrateUpFrom 对指定 gorm.DB 应用 fsys 根目录下未执行的 .up.sql（单测注入 fstest.MapFS）
func MigrateUpFrom(gdb *gorm.DB, fsys fs.FS) error {
	// 1) 确保版本表存在（自举：不依赖任何迁移）
	if err := gdb.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`).Error; err != nil {
		return fmt.Errorf("迁移：创建 schema_migrations 失败: %w", err)
	}

	// 2) 列出嵌入的 .up.sql（排除 .down.sql），按文件名升序
	entries, err := fs.Glob(fsys, "*.up.sql")
	if err != nil {
		return fmt.Errorf("迁移：扫描文件失败: %w", err)
	}
	sort.Strings(entries)

	// 3) 读已应用版本集合
	var applied []string
	if err := gdb.Table("schema_migrations").Pluck("version", &applied).Error; err != nil {
		return fmt.Errorf("迁移：读取已应用版本失败: %w", err)
	}
	appliedSet := make(map[string]bool, len(applied))
	for _, v := range applied {
		appliedSet[v] = true
	}

	// 4) 基线模式（adopt-existing-schema）：版本表为空但业务表已存在
	//    ——存量库在版本化之前已由 AutoMigrate/手工建好 schema，历史基线文件（001/002）
	//    是"当时的快照描述"而非可重放脚本（部分索引/RLS 引用列与实库有差异，重放必炸）。
	//    首跑把 baselineVersions 这组历史文件标记为已应用（不重放），此后新增的 0xx 正常执行。
	//    即 golang-migrate 的 baseline 规程，且只覆盖已知历史、不误跳过未来迁移。
	if len(applied) == 0 {
		if baselined, berr := tenantsExist(gdb); berr == nil && baselined {
			marked := 0
			for _, bv := range baselineVersions {
				if !hasFile(entries, bv) {
					continue
				}
				if ierr := gdb.Exec("INSERT INTO schema_migrations(version) VALUES (?)", bv).Error; ierr != nil {
					return fmt.Errorf("迁移：基线标记 %s 失败: %w", bv, ierr)
				}
				marked++
			}
			if marked > 0 {
				log.Printf("[migrate] 基线模式：检测到存量 schema，%d 个历史迁移标记为已应用（不重放）", marked)
			}
			// 注意：基线后不 return，继续走第 5 步应用未来新增的 0xx（若有）
			appliedSet = make(map[string]bool)
			for _, bv := range baselineVersions {
				appliedSet[bv] = true
			}
		}
	}

	// 5) 逐个应用未执行项
	count := 0
	for _, name := range entries {
		version := strings.TrimSuffix(name, ".up.sql") // 如 001_baseline
		if appliedSet[version] {
			continue
		}
		content, rerr := fs.ReadFile(fsys, name)
		if rerr != nil {
			return fmt.Errorf("迁移：读取 %s 失败: %w", name, rerr)
		}
		// 事务外整段执行：文件自带 BEGIN/COMMIT，pg 单连接内原子完成
		if execErr := gdb.Exec(string(content)).Error; execErr != nil {
			return fmt.Errorf("迁移 %s 执行失败（已中断，未记录版本）: %w", name, execErr)
		}
		if ierr := gdb.Exec("INSERT INTO schema_migrations(version) VALUES (?)", version).Error; ierr != nil {
			return fmt.Errorf("迁移 %s 记录版本失败: %w", name, ierr)
		}
		log.Printf("[migrate] ✓ 已应用 %s", name)
		count++
	}
	if count > 0 {
		log.Printf("[migrate] 本轮应用 %d 个迁移", count)
	}
	return nil
}

// MigrateDownFrom 对指定 gorm.DB 回退最多 steps 个版本（steps<0 表示全部可回退版本）。
// 安全策略：
//   - 只回退存在 .down.sql 的版本，缺 down 即中断，避免把 schema_migrations 记录删掉后留下半回退状态；
//   - 001/002 这类基线文件需要显式 steps<0 才允许回退，日常 `down N` 不会误删基线表。
func MigrateDownFrom(gdb *gorm.DB, fsys fs.FS, steps int) error {
	if err := gdb.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`).Error; err != nil {
		return fmt.Errorf("迁移：创建 schema_migrations 失败: %w", err)
	}

	var applied []string
	if err := gdb.Table("schema_migrations").Order("version DESC").Pluck("version", &applied).Error; err != nil {
		return fmt.Errorf("迁移：读取已应用版本失败: %w", err)
	}

	downEntries, err := fs.Glob(fsys, "*.down.sql")
	if err != nil {
		return fmt.Errorf("迁移：扫描 down 文件失败: %w", err)
	}
	downSet := make(map[string]string, len(downEntries))
	for _, e := range downEntries {
		downSet[strings.TrimSuffix(e, ".down.sql")] = e
	}

	done := 0
	for _, version := range applied {
		if steps >= 0 && done >= steps {
			break
		}
		if steps >= 0 && containsStr(baselineVersions, version) {
			continue
		}
		name, ok := downSet[version]
		if !ok {
			return fmt.Errorf("迁移 %s 缺少 .down.sql，无法安全回退", version)
		}
		content, rerr := fs.ReadFile(fsys, name)
		if rerr != nil {
			return fmt.Errorf("迁移：读取 %s 失败: %w", name, rerr)
		}
		if execErr := gdb.Exec(string(content)).Error; execErr != nil {
			return fmt.Errorf("回退迁移 %s 执行失败（未删除版本记录）: %w", name, execErr)
		}
		if derr := gdb.Exec("DELETE FROM schema_migrations WHERE version = ?", version).Error; derr != nil {
			return fmt.Errorf("回退迁移 %s 删除版本记录失败: %w", name, derr)
		}
		log.Printf("[migrate] ⌐ 已回退 %s", name)
		done++
	}
	if done > 0 {
		log.Printf("[migrate] 本轮回退 %d 个迁移", done)
	}
	return nil
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// tenantsExist 检查业务基表 tenants 是否已存在（判断"存量库/已由 AutoMigrate 建表"）
func tenantsExist(gdb *gorm.DB) (bool, error) {
	var n int64
	err := gdb.Raw("SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='tenants'").
		Scan(&n).Error
	return n > 0, err
}

// AppliedMigrations 返回已应用版本列表（升序，供 /status、诊断与 restore_drill 校验）
func AppliedMigrations() ([]string, error) {
	var v []string
	if err := DB.Table("schema_migrations").Order("version").Pluck("version", &v).Error; err != nil {
		return nil, err
	}
	return v, nil
}
