// 话术离线评分：零成本护栏，纯函数预筛 + 回归基线。
package service

import (
	"strings"
	"unicode/utf8"
)

// EvalResult 离线评分结果（与 LLM evals 阶段互补，零成本护栏）
type EvalResult struct {
	Score   float64  // 0~5
	Reasons []string // 扣分/加分理由（短句）
}

// ScoreReplyOffline 销售话术离线评分（数据飞轮 evals 阶段）
// anchors：期望出现的关键词（需求针对性）；forbidden：禁止出现的泄露/违规词
// 纯函数、无外部依赖，可作为 LLM evals 的廉价预筛与回归基线
func ScoreReplyOffline(reply string, anchors, forbidden []string) EvalResult {
	r := EvalResult{Score: 5, Reasons: nil}
	text := strings.TrimSpace(reply)
	n := utf8.RuneCountInString(text)

	// 1) 长度：过短直接低分，过长温和扣
	if n == 0 {
		return EvalResult{Score: 0, Reasons: []string{"空回复"}}
	}
	if n < 10 {
		r.Score -= 2.5
		r.Reasons = append(r.Reasons, "回复过短")
	} else if n > 400 {
		r.Score -= 0.5
		r.Reasons = append(r.Reasons, "偏长")
	}

	// 2) 锚词命中：每命中一个 +0.4（上限 +1）
	hits := 0
	for _, a := range anchors {
		if a != "" && strings.Contains(text, a) {
			hits++
		}
	}
	if len(anchors) > 0 {
		add := float64(hits) * 0.4
		if add > 1 {
			add = 1
		}
		r.Score += add
		if hits == 0 {
			r.Reasons = append(r.Reasons, "未命中任何需求锚词")
		}
	}

	// 3) 违规词：命中即重扣
	for _, f := range forbidden {
		if f != "" && strings.Contains(text, f) {
			r.Score -= 2
			r.Reasons = append(r.Reasons, "含违规词:"+f)
		}
	}

	if r.Score < 0 {
		r.Score = 0
	}
	if r.Score > 5 {
		r.Score = 5
	}
	return r
}
