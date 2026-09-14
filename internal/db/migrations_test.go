// C4 迁移执行器单测：版本表自举、按序应用、幂等跳过、失败中断不记录
// 依赖真实 PG（本地 dev 库；DB 不可用时 Skip，与 testutil 同哲学——不能 import testutil 是因其反向依赖 db）
package db

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"ai-scrm/config"
	"ai-scrm/migrations"

	"github.com/joho/godotenv"
	"gorm.io/gorm"
)

// newTestDB 提供当前包的辅助逻辑。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	if DB == nil {
		// 向上找项目根 .env → LoadConfig → Init（与 testutil.SetupTestDB 同序，避免 import cycle 内联实现）
		dir, _ := os.Getwd()
		for i := 0; i < 6; i++ {
			if _, err := os.Stat(filepath.Join(dir, ".env")); err == nil {
				_ = godotenv.Load(filepath.Join(dir, ".env"))
				break
			}
			dir = filepath.Dir(dir)
		}
		config.LoadConfig()
		if err := Init(); err != nil {
			if os.Getenv("CI") != "" {
				t.Fatalf("DB 不可用（CI）：%v", err)
			}
			t.Skipf("无可用数据库，跳过迁移测试: %v", err)
		}
	}
	return DB
}

// TestMigrateUpFromAppliesAndSkips 覆盖 MigrateUpFromAppliesAndSkips 相关行为与边界。
func TestMigrateUpFromAppliesAndSkips(t *testing.T) {
	gdb := newTestDB(t)
	const tag = "migrateunit"
	if err := MigrateUpFrom(gdb, migrationsTestFS()); err != nil {
		t.Fatalf("首次应用失败: %v", err)
	}
	// 二次应用：全部已记录 → 幂等无副作用、不报错
	if err := MigrateUpFrom(gdb, migrationsTestFS()); err != nil {
		t.Fatalf("重放应幂等，却失败: %v", err)
	}
	var n int64
	gdb.Table("schema_migrations").Where("version IN ?", []string{tag + "_a", tag + "_b"}).Count(&n)
	if n != 2 {
		t.Fatalf("两个测试迁移都应被记录, got %d", n)
	}
	// 清理测试记录与临时表
	gdb.Exec("DELETE FROM schema_migrations WHERE version LIKE ?", tag+"_%")
	gdb.Exec("DROP TABLE IF EXISTS migrateunit_t")
	gdb.Exec("DROP TABLE IF EXISTS migrateunit_t2")
}

// TestMigrateUpFromFailFastNotRecorded 覆盖 MigrateUpFromFailFastNotRecorded 相关行为与边界。
func TestMigrateUpFromFailFastNotRecorded(t *testing.T) {
	gdb := newTestDB(t)
	badFS := fstest.MapFS{
		"900_broken.up.sql": {Data: []byte("BEGIN; SELECT raise_this_is_an_error; COMMIT;")},
	}
	before := countVersion(gdb, "900_broken")
	if err := MigrateUpFrom(gdb, badFS); err == nil {
		t.Fatalf("损坏迁移应返回错误")
	}
	after := countVersion(gdb, "900_broken")
	if after != before {
		t.Fatalf("失败迁移不得记录版本: before=%d after=%d", before, after)
	}
}

// TestMigrationsIncludeKbEmbeddingVector 覆盖 MigrationsIncludeKbEmbeddingVector 相关行为与边界。
func TestMigrationsIncludeKbEmbeddingVector(t *testing.T) {
	const name = "008_kb_embedding_vector.up.sql"
	content, err := fs.ReadFile(migrations.FS, name)
	if err != nil {
		t.Fatalf("D3 pgvector 迁移未嵌入: %v", err)
	}
	sql := string(content)
	for _, want := range []string{"CREATE EXTENSION IF NOT EXISTS vector", "embedding vector(1536)", "idx_kf_embedding"} {
		if !strings.Contains(sql, want) {
			t.Errorf("迁移缺少关键语句 %q", want)
		}
	}
}

// countVersion 统计迁移版本记录数量。
func countVersion(gdb *gorm.DB, v string) int64 {
	var n int64
	gdb.Table("schema_migrations").Where("version = ?", v).Count(&n)
	return n
}

// migrationsTestFS 两个无害幂等迁移（建临时表 + 插入版本记录由 runner 完成），验证按序执行
func migrationsTestFS() fstest.MapFS {
	return fstest.MapFS{
		"migrateunit_a.up.sql":   {Data: []byte("CREATE TABLE IF NOT EXISTS migrateunit_t (id INT);")},
		"migrateunit_b.up.sql":   {Data: []byte("CREATE TABLE IF NOT EXISTS migrateunit_t2 (id INT);")},
		"migrateunit_b.down.sql": {Data: []byte("DROP TABLE IF EXISTS migrateunit_t2;")}, // down 不应被执行
	}
}
