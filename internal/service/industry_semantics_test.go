// 行业语义读取层校验（泛行业化 P2，2026-09-03）
// 覆盖：无配置回退汽车默认、系统层配置生效、租户覆盖优先、关键词/话术确定性
// UATFOLLOWUP F2(2026-09-15)：新增无包绑定租户的中立口径断言
package service

import (
	"ai-scrm/internal/runtimecfg"
	"strings"
	"testing"
)

// TestIndustryListFallback 未配置行业键时回退代码内置（汽车）
func TestIndustryListFallback(t *testing.T) {
	old := runtimecfg.DefaultSystemConfigService
	runtimecfg.DefaultSystemConfigService = nil
	defer func() { runtimecfg.DefaultSystemConfigService = old }()

	// 无关话题：纯无关消息拦截、白名单词放行
	if !IsOffTopicForTenant(1, "帮我解一下这道高数题") {
		t.Error("无配置时高数题应被拦截(回退黑名单)")
	}
	if IsOffTopicForTenant(1, "这款越野车四驱怎么样") {
		t.Error("无配置时车相关应放行(回退白名单)")
	}
	// 询价词回退汽车默认
	if !ContainsKeywordForTenant("这车多少钱", IndustryPriceKeywordsForTenant(1)) {
		t.Error("无配置时'多少钱'应命中询价关键词")
	}
}

// TestIndustryListSystemDefault 系统层(tenant_id=0)配置生效
func TestIndustryListSystemDefault(t *testing.T) {
	old := runtimecfg.DefaultSystemConfigService
	defer func() { runtimecfg.DefaultSystemConfigService = old }()

	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(map[string]string{
		"industry.topic_keywords": `["产品","套餐","服务"]`,
	}, nil)

	// 新行业词命中白名单放行
	if IsOffTopicForTenant(3, "你们的产品怎么样") {
		t.Error("行业包换词后'产品'应放行")
	}
	// 白名单被替换：旧汽车白名单词不再命中白名单，若其同时命中黑名单则被拦截
	// "帮我写个试驾报告"含旧白名单"试驾"+黑名单"帮我写"——换词后白名单无"试驾"应被拦截
	if !IsOffTopicForTenant(3, "帮我写个试驾报告") {
		t.Error("行业包换词后旧白名单'试驾'不再豁免黑名单'帮我写'")
	}
	// 黑名单未配置时回退代码默认
	if !IsOffTopicForTenant(3, "帮我解一下这道高数题") {
		t.Error("黑名单未配置应回退默认拦截'高数题'")
	}
}

// TestIndustryListTenantOverride 租户覆盖优先于系统默认
func TestIndustryListTenantOverride(t *testing.T) {
	old := runtimecfg.DefaultSystemConfigService
	defer func() { runtimecfg.DefaultSystemConfigService = old }()

	svc := runtimecfg.NewStaticService(map[string]string{
		"industry.offtopic_keywords": `["银行","信贷"]`,
	}, map[uint]map[string]string{
		9: {"industry.offtopic_keywords": `["股票","期货"]`},
	})
	runtimecfg.DefaultSystemConfigService = svc

	// 系统默认层：命中"银行"
	if !IsOffTopicForTenant(1, "聊聊银行理财") {
		t.Error("系统默认黑名单应拦截'银行'")
	}
	// 租户9覆盖：命中"股票"；覆盖层覆盖后系统默认词不生效
	if !IsOffTopicForTenant(9, "帮我算一下股票收益") {
		t.Error("租户覆盖黑名单应拦截'股票'")
	}
	if IsOffTopicForTenant(9, "聊聊银行理财") {
		t.Error("租户覆盖后系统默认'银行'不应再命中")
	}
}

// TestGetOffTopicReplyForTenant 话术确定性 + 租户配置话术
func TestGetOffTopicReplyForTenant(t *testing.T) {
	old := runtimecfg.DefaultSystemConfigService
	defer func() { runtimecfg.DefaultSystemConfigService = old }()

	runtimecfg.DefaultSystemConfigService = nil
	if p := GetOffTopicReplyForTenant(1, "abc"); p == "" {
		t.Error("回退话术不应为空")
	}
	if GetOffTopicReplyForTenant(1, "abcd") != GetOffTopicReplyForTenant(1, "abcd") {
		t.Error("确定性：同内容应返回同一条话术")
	}

	// 租户配置自定义话术
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(nil, map[uint]map[string]string{
		2: {"industry.offtopic_replies": `["我们不聊这个，聊点正事吧"]`},
	})
	if p := GetOffTopicReplyForTenant(2, "anything"); p != "我们不聊这个，聊点正事吧" {
		t.Errorf("租户自定义话术未生效, got %q", p)
	}
}

// TestNeutralFallbackForUnboundTenant F2(2026-09-15)：无行业包绑定租户走行业中立口径
// 单测环境 db.DB 为 nil → TenantHasIndustryPack 恒 false → 询价/无关话题兜底必须不含汽车专属词。
// 修复前：general 租户询价硬拦截返回"约试驾"、无关话题兜底自称"卖车的"（UAT 实测复现）。
func TestNeutralFallbackForUnboundTenant(t *testing.T) {
	old := runtimecfg.DefaultSystemConfigService
	runtimecfg.DefaultSystemConfigService = nil
	defer func() { runtimecfg.DefaultSystemConfigService = old }()

	for _, lead := range []bool{true, false} {
		for _, r := range IndustryPriceRepliesForTenant(1, lead) {
			if strings.Contains(r, "试驾") || strings.Contains(r, "车") {
				t.Errorf("无包绑定租户询价回复含汽车专属词（lead=%v）: %q", lead, r)
			}
		}
	}
	ot := GetOffTopicReplyForTenant(1, "abc")
	if strings.Contains(ot, "卖车") || strings.Contains(ot, "车") {
		t.Errorf("无包绑定租户无关话题兜底含汽车专属词: %q", ot)
	}
	if ot == "" {
		t.Error("中立兜底话术不应为空")
	}
}
