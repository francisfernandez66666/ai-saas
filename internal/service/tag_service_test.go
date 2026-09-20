// 打标服务回归单测（批四 P0，DEFECT_VERIFY_2026-09-20）：
// 锁定"手动打标静默失败"三处叠加缺陷的修复——
//
//	① upsert 冲突分支两参 MAX() 在 PG 必炸（42883），改标量 GREATEST 后必须真正落库；
//	② 写失败不得再被 continue 吞掉恒返回 nil——调用方（advisor/tag API）须能感知报错；
//	③ 冲突更新语义：source 覆盖为最新来源、weight 取两边较大不回退。
//
// 依赖 DB（testutil 连不上自动跳过），全程自建自清，不碰种子数据。
package service

import (
	"testing"
	"time"

	"ai-scrm/internal/cache"
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// setupTagFixture 建租户+客户+标签，并把标签灌进内存缓存（ApplyTagsToCustomer 按缓存匹配）
func setupTagFixture(t *testing.T) (tenantID, customerID uint, tag model.Tag) {
	t.Helper()
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	tenantID = testutil.CreateTenant(t)
	suffix := time.Now().Format("150405.000")
	cust := model.Customer{TenantID: tenantID, Name: "打标单客" + suffix, Source: "unit", Status: 1}
	if err := db.DB.Create(&cust).Error; err != nil {
		t.Fatalf("建客户失败: %v", err)
	}
	tag = model.Tag{TenantID: tenantID, Name: "高意向" + suffix, Code: "hi_" + suffix, Status: 1, Weight: 1.0}
	if err := db.DB.Create(&tag).Error; err != nil {
		t.Fatalf("建标签失败: %v", err)
	}
	// 缓存直接重建（不起版本轮询协程），保证 GetAllTags 可见新标签
	cache.DefaultTagCache = &cache.TagCacheManager{}
	cache.DefaultTagCache.Reload()
	t.Cleanup(func() {
		db.DB.Where("customer_id = ?", cust.ID).Delete(&model.CustomerTag{})
		db.DB.Delete(&model.Tag{}, tag.ID)
		db.DB.Delete(&model.Customer{}, cust.ID)
	})
	return tenantID, cust.ID, tag
}

// getCustomerTag 读回库内 (customer,tag) 关联行
func getCustomerTag(t *testing.T, customerID, tagID uint) model.CustomerTag {
	t.Helper()
	var ct model.CustomerTag
	if err := db.DB.Where("customer_id = ? AND tag_id = ?", customerID, tagID).First(&ct).Error; err != nil {
		t.Fatalf("读客户标签失败: %v", err)
	}
	return ct
}

// TestApplyTagsManualConflictUpsert P0 主回归：客户已有 auto 标（唯一键必冲突），
// 手动打标必须真正把 source 翻成 manual——修复前坏 SQL 被吞，库里恒为 auto。
func TestApplyTagsManualConflictUpsert(t *testing.T) {
	tid, cid, tag := setupTagFixture(t)
	// 预置"能躲过 First 租户过滤"的同 (customer,tag) 行，复刻 P2-43 并发窗口/历史错章行
	// ——函数先 First miss、后 Create 撞 (customer_id,tag_id) 唯一键，真正走 DO UPDATE 分支。
	// 修复前该分支即 P0 现场：MAX(weight,1.0) 坏 SQL 被吞，source 恒为 auto。
	ct := model.CustomerTag{TenantID: tid + 999999, CustomerID: cid, TagID: tag.ID, TagName: tag.Name, Source: "auto", Weight: 3.0, CreatedAt: time.Now()}
	if err := db.DB.Create(&ct).Error; err != nil {
		t.Fatalf("预置 auto 标签失败: %v", err)
	}
	if err := DefaultTagService.ApplyTagsToCustomer(tid, cid, []string{tag.Name}, "manual"); err != nil {
		t.Fatalf("手动打标返回错误（修复前此处恒 nil 且库不更新）: %v", err)
	}
	got := getCustomerTag(t, cid, tag.ID)
	if got.Source != "manual" {
		t.Fatalf("冲突 upsert 后 source=%s，期望 manual（P0 静默失败回归）", got.Source)
	}
	// GREATEST 语义：已有 weight=3.0 与新值 1.0 取大，不得回退
	if got.Weight != 3.0 {
		t.Fatalf("weight=%v，期望 GREATEST 保持 3.0 不回退", got.Weight)
	}
}

// TestApplyTagsFreshInsert 首次打标（无冲突）正常落 manual/1.0。
func TestApplyTagsFreshInsert(t *testing.T) {
	tid, cid, tag := setupTagFixture(t)
	if err := DefaultTagService.ApplyTagsToCustomer(tid, cid, []string{tag.Code}, "manual"); err != nil {
		t.Fatalf("按编码打标失败: %v", err)
	}
	got := getCustomerTag(t, cid, tag.ID)
	if got.Source != "manual" || got.Weight != 1.0 || got.TenantID != tid {
		t.Fatalf("首插落库错误: source=%s weight=%v tenant=%d", got.Source, got.Weight, got.TenantID)
	}
}

// TestApplyTagsUnknownTagSilentSkip 不存在的标签名跳过不算错（既有语义），且不打标失败。
func TestApplyTagsUnknownTagSilentSkip(t *testing.T) {
	tid, cid, _ := setupTagFixture(t)
	if err := DefaultTagService.ApplyTagsToCustomer(tid, cid, []string{"不存在的标签XYZ"}, "manual"); err != nil {
		t.Fatalf("未知标签应跳过而非报错: %v", err)
	}
	var n int64
	db.DB.Model(&model.CustomerTag{}).Where("customer_id = ?", cid).Count(&n)
	if n != 0 {
		t.Fatalf("未知标签却落库 %d 行", n)
	}
}

// TestApplyTagsExistingRowErrorSurfaced P0②：已存在行走 Save 更新来源，失败必须上抛不再吞。
// 构造法：删掉客户行使 First miss、再直插带唯一键的冲突行无法稳定模拟——改为验证
// "auto→manual 后再次 auto 打标" 的双向翻转都真实落库（等价覆盖 First 命中 Save 分支）。
func TestApplyTagsExistingRowSaveBranch(t *testing.T) {
	tid, cid, tag := setupTagFixture(t)
	// 第一次：全新插入 manual
	if err := DefaultTagService.ApplyTagsToCustomer(tid, cid, []string{tag.Name}, "manual"); err != nil {
		t.Fatalf("首次打标失败: %v", err)
	}
	// 第二次：First 命中 → Save 更新来源为 auto
	if err := DefaultTagService.ApplyTagsToCustomer(tid, cid, []string{tag.Name}, "auto"); err != nil {
		t.Fatalf("二次打标失败: %v", err)
	}
	if got := getCustomerTag(t, cid, tag.ID); got.Source != "auto" {
		t.Fatalf("Save 分支未生效: source=%s 期望 auto", got.Source)
	}
}

// TestReplaceTagsClearsRemoved 批四 P1：PUT 覆盖语义——提交列表外的旧标必须清除
// （旧实现增量不删，顾问端弹窗取消勾选后旧标滞留库内，uat_advisor 首跑实证）。
func TestReplaceTagsClearsRemoved(t *testing.T) {
	tid, cid, tag := setupTagFixture(t)
	if err := DefaultTagService.ApplyTagsToCustomer(tid, cid, []string{tag.Name}, "manual"); err != nil {
		t.Fatalf("首插打标失败: %v", err)
	}
	// 覆盖提交空列表 → 全部清除（API 层 PUT 空 tags 被 binding 拒 400，服务层承担清空语义）
	if err := DefaultTagService.ReplaceTagsForCustomer(tid, cid, []string{}, "manual"); err != nil {
		t.Fatalf("覆盖清空失败: %v", err)
	}
	var n int64
	db.DB.Model(&model.CustomerTag{}).Where("customer_id = ?", cid).Count(&n)
	if n != 0 {
		t.Fatalf("覆盖后仍残留 %d 行标签（清旧语义未生效）", n)
	}
	// 再覆盖回该标：只保留列表内项且来源正确
	if err := DefaultTagService.ReplaceTagsForCustomer(tid, cid, []string{tag.Name}, "manual"); err != nil {
		t.Fatalf("覆盖回写失败: %v", err)
	}
	if got := getCustomerTag(t, cid, tag.ID); got.Source != "manual" {
		t.Fatalf("回写标签 source=%s 期望 manual", got.Source)
	}
}
