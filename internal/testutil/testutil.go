// Package testutil 提供跨包复用的测试基座：初始化配置/DB、租户构造等。
// 定位：替代各 *_test.go 里手写 db.Init() 的重复代码，并修复
// "config.GlobalConfig 未初始化 → db.Init 解引用 nil 指针 panic"的隐患。
package testutil

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"ai-scrm/config"
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"

	"github.com/joho/godotenv"
)

// SetupTestDB 初始化测试用 DB 连接。
// 行为：向上查找项目根 .env 并加载 → config.LoadConfig()（填充 GlobalConfig，否则 db.Init panic）
//
//	→ db.Init()。DB 不可用/未配置时 t.Skipf（延续"无 DB 也能跑纯逻辑测试"的哲学）。
//
// 注意：
//   - go test 的工作目录是各包目录，必须手动定位项目根 .env（不能用默认相对路径）
//   - 本包刻意不 import service（避免 service 测试包反向依赖形成 import cycle）；
//     依赖 DefaultSystemConfigService 的测试（如计费/配额）需在 SetupTestDB 后自行
//     调用 service.InitSystemConfigService()（同包测试可直接调用）。
func SetupTestDB(t *testing.T) {
	t.Helper()
	loadRootEnv()
	config.LoadConfig()
	if err := db.Init(); err != nil {
		t.Skipf("DB 不可用，跳过 DB 依赖测试: %v", err)
	}
}

// loadRootEnv 从当前目录向上逐级查找项目根 .env 并加载（幂等，找不到则忽略）
func loadRootEnv() {
	dir, _ := os.Getwd()
	for i := 0; i < 6; i++ {
		p := filepath.Join(dir, ".env")
		if _, err := os.Stat(p); err == nil {
			_ = godotenv.Load(p)
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

// CreateTenant 创建单测租户（code=unit_test_tenant，已存在则复用），返回租户ID
// 复用语义：同 code 同租户，供"仅需一个租户上下文"的测试使用
// 注意：code 自动带进程级随机后缀（见 processSuffix），并发跑多个 go test 进程
// 共用同一 dev 库时不会互相撞 idx_tenants_code 唯一索引
func CreateTenant(t *testing.T) uint {
	t.Helper()
	return CreateTenantCode(t, "unit_test_tenant")
}

// processSuffix 进程级租户码后缀（测试基建竞态根因修复，2026-09-08）。
// 背景：此前 CreateTenant/CreateTenantCode 用固定 code（unit_test_tenant / rls_a 等），
// 并发跑多个 go test 进程共用同一 dev 库时，两个进程同时 Count=0 后都 INSERT，
// 后者撞 idx_tenants_code 唯一索引（TOCTOU）导致单测随机失败。
// 修复：每进程一个唯一后缀，进程间 code 互不重叠；进程内多次调用仍复用同一租户（语义不变）。
var processSuffix = newProcessSuffix()

// newProcessSuffix 生成进程唯一后缀：PID + 3字节crypto/rand（crypto/rand 失败时退化为 PID 后缀）
func newProcessSuffix() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err == nil {
		return fmt.Sprintf("p%06dx%s", os.Getpid(), hex.EncodeToString(b))
	}
	return fmt.Sprintf("p%06d", os.Getpid())
}

// CreateTenantCode 创建指定 code 的单测租户（已存在则复用），返回租户ID
// 跨租户隔离测试需两个不同 code 的租户（A/B）——code 自动带进程后缀，
// 一方保证并发进程不互踩，另一方进程内调用方仍可用形如 "rls_a"/"rls_b" 的语义码
func CreateTenantCode(t *testing.T, code string) uint {
	t.Helper()
	code = processSuffix + "_" + code
	var cnt int64
	db.DB.Model(&model.Tenant{}).Where("code = ?", code).Count(&cnt)
	if cnt > 0 {
		var id uint
		db.DB.Model(&model.Tenant{}).Where("code = ?", code).Pluck("id", &id)
		return id
	}
	tt := &model.Tenant{Name: "单元测试租户-" + code, Code: code, Status: "active",
		MaxAICalls: 1000, UsedAICalls: 0, AICallBalance: 0,
		// InviteCode 有 uniqueIndex，不能留空串（多个空串冲突）；本地生成避免 import service 成环
		InviteCode: randInviteCode()}
	if err := db.DB.Create(tt).Error; err != nil {
		t.Fatalf("CreateTenantCode(%s): %v", code, err)
	}
	return tt.ID
}

// randInviteCode 生成随机 8 位邀请码（与 service.GenerateInviteCode 同字符集，测试用）
// 不能直接调用 service（service 测试包反向依赖 testutil 会成环）
func randInviteCode() string {
	const alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	out := make([]byte, 8)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return fmt.Sprintf("T%07d", i+1)
		}
		out[i] = alphabet[n.Int64()]
	}
	return string(out)
}

// CleanupTenant 删除单测租户（幂等，测试收尾清理）
func CleanupTenant(t *testing.T, id uint) {
	t.Helper()
	_ = db.DB.Where("id = ?", id).Delete(&model.Tenant{}).Error
}
