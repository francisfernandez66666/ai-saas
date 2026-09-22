// OpenAPI 对话链路「必须走合并队列」静态守卫（A3 验收护栏，2026-09-22 批四）。
//
// A3 的原缺陷不是崩，而是"绕过去"：openapi_chat.go 直接 Infer + OrchestrateReply，
// 全文件零 EnqueueAndWait 命中。这类"少接一层"的回归最容易在改链路时又掉回去，
// 所以用源码级负向锁钉死三件事：
//  1. 队列三件套必须在场（EnqueueAndWait / ClaimReplyDelivery / SetReply）；
//  2. AI 生成只能出现在拿到处理权之后（holdsProcessing 置位之后才允许 OrchestrateReply）；
//  3. 持权者每条早退路径都必须归还批次（openAPIRelease），否则等待者挂到 600s 锁超时。
package api

import (
	"os"
	"strings"
	"testing"
)

// TestOpenAPIChatGoesThroughMergeQueue 锁 1 + 锁 2
func TestOpenAPIChatGoesThroughMergeQueue(t *testing.T) {
	raw, err := os.ReadFile("openapi_chat.go")
	if err != nil {
		t.Fatalf("读 openapi_chat.go 失败: %v", err)
	}
	src := stripComments(string(raw))
	for _, need := range []string{
		"EnqueueAndWait(", "ClaimReplyDelivery(", "SetReply(", "HumanTakeoverDecide(",
	} {
		if !strings.Contains(src, need) {
			t.Errorf("OpenAPI 链路缺少 %s：合并队列/人工锁定语义又掉了（A3）", need)
		}
	}
	// 生成必须在持权之后
	acquire := strings.Index(src, "s.holdsProcessing = true")
	gen := strings.Index(src, "OrchestrateReply(")
	if acquire < 0 || gen < 0 {
		t.Fatal("OpenAPI 链路结构变了（找不到持权置位或生成调用），请同步本守卫")
	}
	if gen < acquire {
		t.Error("OpenAPI 链路在拿到处理权之前就调了 OrchestrateReply（会双烧配额，A3 禁止）")
	}
	// OrchestrateReply 只允许一处：多处即"绕过队列另开一条生成路径"
	if n := strings.Count(src, "OrchestrateReply("); n != 1 {
		t.Errorf("OpenAPI 链路 OrchestrateReply 调用点应为 1 处，实际 %d 处", n)
	}
}

// TestOpenAPIProcessorReleasesBatch 锁 3：持权者函数体内每个 return 前都必须归还批次
func TestOpenAPIProcessorReleasesBatch(t *testing.T) {
	raw, err := os.ReadFile("openapi_chat.go")
	if err != nil {
		t.Fatalf("读 openapi_chat.go 失败: %v", err)
	}
	lines := strings.Split(stripComments(string(raw)), "\n")
	start := -1
	for i, l := range lines {
		if strings.Contains(l, "func (s *openAPIChatCtx) openAPIProcessAndReply()") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("找不到 openAPIProcessAndReply，函数已改名请同步本守卫")
	}
	end := start + 1
	for ; end < len(lines); end++ {
		if strings.HasPrefix(lines[end], "func ") {
			break
		}
	}
	releases := 0
	for i := start; i < end; i++ {
		if strings.TrimSpace(lines[i]) != "return" {
			continue
		}
		lookback := strings.Join(lines[max(start, i-8):i], "\n")
		if !strings.Contains(lookback, "openAPIRelease(") {
			t.Errorf("openapi_chat.go:%d 持权者早退未归还批次（等待者会挂到锁超时）", i+1)
		}
		releases++
	}
	if releases == 0 {
		t.Log("openAPIProcessAndReply 已无中途 return，归还锁由末尾统一承担")
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
