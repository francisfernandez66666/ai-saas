package metrics

import (
	"strings"
	"testing"

	"ai-scrm/internal/db"
)

// TestRenderLabeledCounterDeadLetter 覆盖 F6 通道死信指标：计数按 reason 维度、积压水位 gauge。
func TestRenderLabeledCounterDeadLetter(t *testing.T) {
	IncChannelDeadLetter("fatal")
	IncChannelDeadLetter("fatal")
	IncChannelDeadLetter("exhausted")
	SetChannelDeadLetterPending(7)

	var b []byte
	renderLabeledCounter(&b, "ai_scrm_channel_dead_letter_total", "help", "reason", &channelDeadLetterTotal)
	out := string(b)
	if !strings.Contains(out, `ai_scrm_channel_dead_letter_total{reason="fatal"} 2`) {
		t.Fatalf("fatal 计数缺失或错误:\n%s", out)
	}
	if !strings.Contains(out, `ai_scrm_channel_dead_letter_total{reason="exhausted"} 1`) {
		t.Fatalf("exhausted 计数缺失:\n%s", out)
	}
	if !strings.Contains(out, "# TYPE ai_scrm_channel_dead_letter_total counter") {
		t.Fatalf("缺少 TYPE 头:\n%s", out)
	}

	// RenderPrometheus 依赖 ComputeHealth→db.DB，无库环境下跳过 gauge 全量渲染断言，
	// 仅校验计数渲染（上方）；有库环境下（如 test_all 集成）再验 pending gauge 落地。
	if db.DB == nil {
		t.Skip("db.DB 未初始化，跳过 RenderPrometheus 全量渲染")
	}
	rendered := RenderPrometheus()
	if !strings.Contains(rendered, "ai_scrm_channel_dead_letter_pending") {
		t.Fatalf("死信积压 gauge 未渲染")
	}
}
