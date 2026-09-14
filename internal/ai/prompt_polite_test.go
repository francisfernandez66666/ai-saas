// Q5 单测(2026-09-12)：策略 prompt 正向话术不得再出现「您」开头的敬语示例
// 负例（"不要反问「您什么时候来」"这类标注要避免的写法）允许含「您」，不在禁列。
package ai

import (
	"strings"
	"testing"

	"ai-scrm/internal/strategytypes"
)

// TestStrategyPromptNoPolitePositiveExamples 覆盖 StrategyPromptNoPolitePositiveExamples 相关行为与边界。
func TestStrategyPromptNoPolitePositiveExamples(t *testing.T) {
	// 促到店-对比锚：原「您可以到店来看看」应为「你可以…」
	pCompare := BuildStrategyPrompt(&strategytypes.StrategyOutput{FinalAnchor: strategytypes.AnchorCompare},
		nil, 0, false, true, false)
	// 条件交换（促单解锁）：原「您要是今天能定，我帮您…」应为「你要…我帮你…」
	pSwap := BuildStrategyPrompt(&strategytypes.StrategyOutput{
		FinalAnchor: strategytypes.AnchorDisassemble, ExchangeFlag: true, ExchangeType: "finance",
	}, nil, 0, false, true, false)
	// 倾听探索（不抛锚未留资）：原「您主要是想买来日常通勤」应为「你主要…」
	pListen := BuildStrategyPrompt(&strategytypes.StrategyOutput{FinalAnchor: strategytypes.AnchorNoThrow},
		nil, 0, false, true, false)

	for _, c := range []struct {
		name   string
		prompt string
		bad    string
		good   string
	}{
		{"对比锚-到店陈述", pCompare, "「您可以到店", "「你可以到店"},
		{"条件交换-话术", pSwap, "「您要是今天能定", "「你要是今天能定"},
		{"条件交换-后半句", pSwap, "我帮您申请个金融优惠", "我帮你申请个金融优惠"},
		{"倾听探索-开放问", pListen, "「您主要是想买来日常通勤", "「你主要是想买来日常通勤"},
	} {
		if !strings.Contains(c.prompt, c.good) {
			t.Errorf("%s: 应含正向话术 %q（Q5 清洗），实际缺失。prompt 片段: %.120s", c.name, c.good, c.prompt)
		}
		if strings.Contains(c.prompt, c.bad) {
			t.Errorf("%s: 不应再含敬语话术 %q（Q5 清洗未生效）", c.name, c.bad)
		}
	}
}
