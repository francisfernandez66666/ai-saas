// 询价敏感词的行业族别分流回归单测（PLAN_FIX_2026-09-21 B1）。
// 背景：批四只把**询价话术**（priceReplyFallback）按 TenantUsesAutoTalk 分成了
// 汽车版/中立版，**关键词**（IndustryPriceKeywordsForTenant）仍恒回退汽车词表
// （含"落地价/车价/多少钱一辆/多少钱一台"）——同类点漏改：edu/wedding/realty 等
// 非 auto 租户插着一套汽车语义词表，与中立话术口径自相矛盾。
// 本测覆盖：无租户上下文（兼容既有 IsPriceInquiry 行为断言）→ 汽车默认；
// auto 族租户 → 汽车词表；非 auto 包租户 → 中立词表且**不含汽车专属词**。
package service

import (
	"testing"

	"ai-scrm/internal/db"
	"ai-scrm/internal/testutil"
)

// autoOnlyWords 汽车交易专属词（中立词表必须剔除）
var autoOnlyWords = []string{"落地价", "车价", "多少钱一辆", "多少钱一台", "售价多少"}

func containsAny(list []string, words []string) string {
	for _, w := range words {
		for _, l := range list {
			if l == w {
				return w
			}
		}
	}
	return ""
}

// TestPriceKeywordsTenantIDZero 无租户上下文保持历史默认（兼容性护栏）。
// strategy.IsPriceInquiry 走本函数且 behavior_test.go 断言"落地价多少"=true，
// 该调用无租户上下文；若此处切中立词表会打断既有行为断言。
func TestPriceKeywordsTenantIDZero(t *testing.T) {
	got := IndustryPriceKeywordsForTenant(0)
	if containsAny(got, []string{"多少钱"}) == "" {
		t.Fatalf("无租户上下文应沿用汽车默认词表（含通用词 多少钱），实际: %v", got)
	}
	if containsAny(got, autoOnlyWords) == "" {
		t.Fatalf("无租户上下文应保持汽车专属词（兼容性：IsPriceInquiry 行为测试依赖 落地价），实际: %v", got)
	}
}

// TestPriceKeywordsByIndustryFamily 验证价格关键词按行业族（汽车/教育等）返回对应词表，未知行业退化到默认族。
func TestPriceKeywordsByIndustryFamily(t *testing.T) {
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	// 汽车族租户：保留汽车专属词
	autoTID := bindTestPack(t, "kw_auto", "kwpack_auto", "auto", false)
	autoKW := IndustryPriceKeywordsForTenant(autoTID)
	if containsAny(autoKW, autoOnlyWords) == "" {
		t.Errorf("汽车族租户应保留汽车专属询价词，实际: %v", autoKW)
	}

	// 非汽车族租户：中立词表，且不得出现汽车专属词
	eduTID := bindTestPack(t, "kw_edu", "kwpack_edu", "edu", false)
	eduKW := IndustryPriceKeywordsForTenant(eduTID)
	if w := containsAny(eduKW, autoOnlyWords); w != "" {
		t.Errorf("非 auto 租户不应出现汽车专属询价词 %q，实际: %v", w, eduKW)
	}
	// 中立词表仍须覆盖通用询价表达，否则询价硬拦截会漏召回
	if containsAny(eduKW, []string{"多少钱", "报价", "价格"}) == "" {
		t.Errorf("中立词表须保留通用询价词（漏召回风险），实际: %v", eduKW)
	}
}
