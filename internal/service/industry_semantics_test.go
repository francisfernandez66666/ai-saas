// 行业语义读取层校验（泛行业化 P2，2026-09-03）
// 覆盖：无配置回退汽车默认、系统层配置生效、租户覆盖优先、关键词/话术确定性
package service

import (
	"testing"
)

// TestIndustryListFallback 未配置行业键时回退代码内置（汽车）
func TestIndustryListFallback(t *testing.T) {
	old := DefaultSystemConfigService
	DefaultSystemConfigService = nil
	defer func() { DefaultSystemConfigService = old }()

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
	old := DefaultSystemConfigService
	defer func() { DefaultSystemConfigService = old }()

	DefaultSystemConfigService = &SystemConfigService{cache: map[string]string{
		"industry.topic_keywords": `["产品","套餐","服务"]`,
	}}

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
	old := DefaultSystemConfigService
	defer func() { DefaultSystemConfigService = old }()

	svc := &SystemConfigService{
		cache: map[string]string{
			"industry.offtopic_keywords": `["银行","信贷"]`,
		},
		tenantCache: map[uint]map[string]string{
			9: {"industry.offtopic_keywords": `["股票","期货"]`},
		},
	}
	DefaultSystemConfigService = svc

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
	old := DefaultSystemConfigService
	defer func() { DefaultSystemConfigService = old }()

	DefaultSystemConfigService = nil
	if p := GetOffTopicReplyForTenant(1, "abc"); p == "" {
		t.Error("回退话术不应为空")
	}
	if GetOffTopicReplyForTenant(1, "abcd") != GetOffTopicReplyForTenant(1, "abcd") {
		t.Error("确定性：同内容应返回同一条话术")
	}

	// 租户配置自定义话术
	DefaultSystemConfigService = &SystemConfigService{tenantCache: map[uint]map[string]string{
		2: {"industry.offtopic_replies": `["我们不聊这个，聊点正事吧"]`},
	}}
	if p := GetOffTopicReplyForTenant(2, "anything"); p != "我们不聊这个，聊点正事吧" {
		t.Errorf("租户自定义话术未生效, got %q", p)
	}
}