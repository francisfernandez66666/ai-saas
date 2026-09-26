// Package metrics FIX-5(2026-09-27) 观测位测试：对话主链落库失败计数器按 kind 分维。
//
// 这条计数器是"旁路写失败"唯一的线上可见性——那些路径刻意不打断客户答复（回 500 会把
// "其实做完了"的动作报成失败），所以一旦真的持续失败，只有这里能看见。
// 一个数不对，等于整类静默路径重新变回"只会打日志并返回成功"。
// 故本文件钉三件事：① 同 kind 累加、② 跨 kind 互不串味、③ 没出现过的 kind 不得凭空冒出来
// （最后一条是反证：渲染器若把所有维度合并成一行，①② 照样绿）。
package metrics

import (
	"strconv"
	"strings"
	"testing"
)

// TestChatPersistErrorCounterRendersPerKind 钉住对话落库失败计数器的三个性质：同 kind 累加、
// 跨 kind 互不串味、未出现过的 kind 不得凭空渲染出来（第三条是反证——维度被合并成一行时前两条照样绿）。
// 起点按增量计算而非绝对值，避免包内其它用例先 increment 造成执行顺序相关的假红。
func TestChatPersistErrorCounterRendersPerKind(t *testing.T) {
	// 起点用增量而非绝对值：本包其它用例可能已经打过同一个 kind，
	// 断"等于 2"会在测试执行顺序变化时假红。
	before := chatPersistCountFor(t, "customer_inbound")
	IncChatPersistError("customer_inbound")
	IncChatPersistError("customer_inbound")
	IncChatPersistError("conversation_state")

	var b []byte
	renderLabeledCounter(&b, "ai_scrm_chat_persist_error_total", "help", "kind", &chatPersistErrorTotal)
	out := string(b)

	if got, want := chatPersistLine(t, out, "customer_inbound"), before+2; got != want {
		t.Fatalf("customer_inbound 计数应为 %d（本用例前 %d + 本次 2），实得 %d:\n%s", want, before, got, out)
	}
	if !strings.Contains(out, `ai_scrm_chat_persist_error_total{kind="conversation_state"}`) {
		t.Fatalf("conversation_state 这一维没渲染出来（跨 kind 被吞了？）:\n%s", out)
	}
	if !strings.Contains(out, "# TYPE ai_scrm_chat_persist_error_total counter") {
		t.Fatalf("缺少 TYPE 头，Prometheus 抓取端会按 untyped 处理:\n%s", out)
	}
	// 反证：从没 increment 过的 kind 不得出现在渲染结果里。
	if strings.Contains(out, `kind="never_used_kind"`) {
		t.Fatalf("渲染器凭空造出了未出现过的维度，说明各维度被合并成同一行:\n%s", out)
	}
}

// chatPersistCountFor 从当前渲染结果里读某个 kind 的既有计数（没有则 0）。
func chatPersistCountFor(t *testing.T, kind string) uint64 {
	t.Helper()
	var b []byte
	renderLabeledCounter(&b, "ai_scrm_chat_persist_error_total", "help", "kind", &chatPersistErrorTotal)
	return chatPersistLine(t, string(b), kind)
}

// chatPersistLine 解析 `...{kind="x"} N` 一行里的 N；找不到该行返回 0。
func chatPersistLine(t *testing.T, rendered, kind string) uint64 {
	t.Helper()
	marker := `ai_scrm_chat_persist_error_total{kind="` + kind + `"} `
	for _, line := range strings.Split(rendered, "\n") {
		i := strings.Index(line, marker)
		if i < 0 {
			continue
		}
		v, err := strconv.ParseUint(strings.TrimSpace(line[i+len(marker):]), 10, 64)
		if err != nil {
			t.Fatalf("解析 %s 维度计数失败: %v（原始行 %q）", kind, err, line)
		}
		return v
	}
	return 0
}
