// 知识库公开面可见性取数的回归单测（PLAN_FIX_2026-09-21 B2 / 迁移 016）。
// 背景：014（P2-4）只给 knowledge_fragments 收敛了公开面，brands / car_models /
// competitor_compares 三个主实体没有可见性概念，匿名可全量拉走商家产品目录与
// 竞品优劣对比话术。016 补列后由 GetVisible* 系列承担"仅 public"语义。
// 本测锁定两件事：① 公开面只出 public；② 租户隔离不被可见性过滤破坏（P0-4 语义保持）。
package cache

import (
	"testing"

	"ai-scrm/internal/model"
)

// newVisibleFixture 构造含 public/private/跨租户 三类样本的缓存实例
func newVisibleFixture() *KnowledgeCacheManager {
	return &KnowledgeCacheManager{
		brands: []model.Brand{
			{ID: 1, TenantID: 0, Name: "系统预置-public", Visibility: "public"},
			{ID: 2, TenantID: 7, Name: "本租户-public", Visibility: "public"},
			{ID: 3, TenantID: 7, Name: "本租户-private", Visibility: "private"},
			{ID: 4, TenantID: 9, Name: "他租户-public", Visibility: "public"},
		},
		models: []model.CarModel{
			{ID: 11, TenantID: 0, Name: "预置车", BrandID: 1, Visibility: "public"},
			{ID: 12, TenantID: 7, Name: "本租户车", BrandID: 2, Visibility: "public"},
			{ID: 13, TenantID: 7, Name: "本租户私车", BrandID: 2, Visibility: "private"},
			{ID: 14, TenantID: 9, Name: "他租户车", BrandID: 4, Visibility: "public"},
		},
		compares: []model.CompetitorCompare{
			{ID: 21, TenantID: 7, OurModelID: 12, CompetitorBrand: "竞品", Visibility: "public"},
			{ID: 22, TenantID: 7, OurModelID: 12, CompetitorBrand: "竞品私", Visibility: "private"},
			{ID: 23, TenantID: 9, OurModelID: 14, CompetitorBrand: "他租户", Visibility: "public"},
		},
	}
}

// TestGetVisibleBrands 验证品牌可见性：公开面仅返回系统预置 public 与本租户 public，他租户与 private 一律剔除。
func TestGetVisibleBrands(t *testing.T) {
	m := newVisibleFixture()
	got := m.GetVisibleBrands(7)
	want := map[uint]bool{1: true, 2: true} // 系统预置 public + 本租户 public
	if len(got) != len(want) {
		t.Fatalf("公开品牌条数 = %d, want %d（实际: %+v）", len(got), len(want), got)
	}
	for _, b := range got {
		if !want[b.ID] {
			t.Errorf("混入非预期品牌 id=%d visibility=%s tenant=%d", b.ID, b.Visibility, b.TenantID)
		}
	}
}

// TestGetVisibleModels 验证车型可见性（全量与按品牌两个入口）：本租户 private 与他租户车型不得出现在公开面。
func TestGetVisibleModels(t *testing.T) {
	m := newVisibleFixture()
	// 全量：预置 + 本租户 public
	got := m.GetVisibleModels(7)
	if len(got) != 2 {
		t.Fatalf("公开车型条数 = %d, want 2（实际: %+v）", len(got), got)
	}
	// 按品牌：brand 2 下只剩本租户 public 一条（私车被过滤）
	byBrand := m.GetVisibleModelsByBrandID(7, 2)
	if len(byBrand) != 1 || byBrand[0].ID != 12 {
		t.Fatalf("按品牌公开车型 = %+v, want 仅 id=12", byBrand)
	}
	// 单条：private 不可见（返回 nil，handler 会转 404）
	if m.GetVisibleModelByID(7, 13) != nil {
		t.Error("private 车型不应在公开面可见（GetVisibleModelByID 应返回 nil）")
	}
	if m.GetVisibleModelByID(7, 12) == nil {
		t.Error("public 车型应在公开面可见")
	}
	// 跨租户 public 仍不可见（租户隔离优先于可见性）
	if m.GetVisibleModelByID(7, 14) != nil {
		t.Error("他租户 public 车型不得出现在本租户公开面（租户隔离被破坏）")
	}
}

// TestGetVisibleCompares 验证竞品对比可见性：按车型取公开对比时，private 与他租户行必须被过滤。
func TestGetVisibleCompares(t *testing.T) {
	m := newVisibleFixture()
	got := m.GetVisibleComparesByModelID(7, 12)
	if len(got) != 1 || got[0].ID != 21 {
		t.Fatalf("公开竞品对比 = %+v, want 仅 id=21（private 与他租户均须剔除）", got)
	}
}

// TestGetVisibleKeepsInternalFull 内部取数不受可见性影响（AI 链路/管理面仍看全量）。
// 设计取舍见 knowledge_cache.go：公开面用独立方法，既有 GetAllBrands 等保持原语义。
func TestGetVisibleKeepsInternalFull(t *testing.T) {
	m := newVisibleFixture()
	if n := len(m.GetAllBrands(7)); n != 3 { // 系统预置 + 本租户两条（含 private）
		t.Errorf("内部全量品牌 = %d, want 3（公开面过滤不得波及内部取数）", n)
	}
	if n := len(m.GetAllModels(7)); n != 3 {
		t.Errorf("内部全量车型 = %d, want 3", n)
	}
	if m.GetModelByID(7, 13) == nil {
		t.Error("内部按 ID 取 private 车型应仍可取到（AI 链路 getCustomerModelID 依赖）")
	}
}
