// UATFOLLOWUP F3 单测(2026-09-15)：无行业包绑定租户的系统 Prompt 必须走行业中立口径。
//
// 背景：修复前 BuildSystemPrompt 的人设兜底写死"越野SUV品牌的销售顾问"、
// 领域约束写死"只聊车"、询价话术参考写死"约个试驾"——general/新行业租户
// 全部被汽车销售话术污染（UAT 字节级实测复现：general 租户询价硬拦截返回试驾话术）。
// 修复后：无包绑定（单测环境 db.DB 为 nil）→ 中立人设/中立约束/中立话术参考；
// 行业键 industry.salesperson 配置过的租户仍注入行业人设（车企租户行为不变）。
package ai

import (
	"strings"
	"testing"

	"ai-scrm/internal/runtimecfg"
)

// TestNeutralPromptForUnboundTenant 无包绑定租户 Prompt 不含汽车专属词
func TestNeutralPromptForUnboundTenant(t *testing.T) {
	old := runtimecfg.DefaultSystemConfigService
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(nil, nil)
	defer func() { runtimecfg.DefaultSystemConfigService = old }()

	p := BuildSystemPrompt(7, nil, 0, false, 0, false, false, false)
	for _, bad := range []string{"越野SUV", "只聊车", "约个试驾", "试驾体验过后"} {
		if strings.Contains(p, bad) {
			t.Errorf("无包绑定租户系统 Prompt 不应含汽车专属词 %q", bad)
		}
	}
	if !strings.Contains(p, "销售顾问") {
		t.Error("中立人设应保留销售顾问身份描述")
	}
}

// TestIndustryPersonaInjectedIntoPrompt 行业键人设注入 Prompt（车企租户口径保持）
func TestIndustryPersonaInjectedIntoPrompt(t *testing.T) {
	old := runtimecfg.DefaultSystemConfigService
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(nil, map[uint]map[string]string{
		9: {"industry.salesperson": "\"你是汽车品牌的销售顾问，微信聊天风格。\""},
	})
	defer func() { runtimecfg.DefaultSystemConfigService = old }()

	p := BuildSystemPrompt(9, nil, 0, false, 0, false, false, false)
	if !strings.Contains(p, "汽车品牌的销售顾问") {
		t.Error("行业键人设应注入 Prompt（车企租户行为不变）")
	}
}
