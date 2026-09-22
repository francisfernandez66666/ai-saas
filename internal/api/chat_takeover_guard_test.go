// 人工接管态「判定即落库」静态守卫（A1 验收护栏，2026-09-22 批四）。
//
// 为什么用源码扫描而不是行为测试：A1 的原缺陷是 chat_main 在 CheckHumanTimeout 之后
// 只把 Mode/IsHumanLocked 改在内存里、请求结束即丢，锁定态永不解除。这种"少写一行落库"
// 的回归，行为测试要造两条链的 HTTP 上下文才抓得到，而静态锁一眼就能封死。
// 本文件锁三件事：
//  1. 两条 C 端入口都必须委托 chatflow.HumanTakeoverDecide（唯一真相源），不得各自表述；
//  2. 会话接管列（is_human_locked / pending_handoff）的任何内存赋值，必须在紧邻若干行内
//     配对一次落库写（Updates 字段级 / Save / Select），否则判为"仅改内存不回写"；
//  3. chatEnsureConversation 函数体内不得再出现接管列赋值——裁决与落库已由 chatflow 负责。
package api

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// apiSourceFiles 读取 internal/api 下的非测试 Go 源文件名（本包测试的工作目录即该目录）
func apiSourceFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读取 api 目录失败: %v", err)
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		out = append(out, n)
	}
	return out
}

// stripComments 去掉整行注释与行尾注释，避免"负向锁命中自己的说明文字"
func stripComments(src string) string {
	var keep []string
	for _, line := range strings.Split(src, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "//") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "*") {
			continue
		}
		// 行尾 // 注释（字符串字面量里的 // 只出现在 URL，本锁关心的是赋值语句，误删无影响）
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		keep = append(keep, line)
	}
	return strings.Join(keep, "\n")
}

// TestTakeoverColumnsAlwaysPersisted 锁 2：接管列赋值必须配对落库写
func TestTakeoverColumnsAlwaysPersisted(t *testing.T) {
	assign := regexp.MustCompile(`\.(IsHumanLocked|PendingHandoff)\s*=\s*[^=]`)
	// 落库写的识别口径：字段级 Updates 的 map 键 / Save / Select 白名单更新
	persist := regexp.MustCompile(`"(is_human_locked|pending_handoff)"|\.Save\(|\.Select\(`)

	for _, name := range apiSourceFiles(t) {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("读 %s 失败: %v", name, err)
		}
		lines := strings.Split(stripComments(string(raw)), "\n")
		for i, line := range lines {
			if !assign.MatchString(line) {
				continue
			}
			end := i + 14
			if end > len(lines) {
				end = len(lines)
			}
			if !persist.MatchString(strings.Join(lines[i:end], "\n")) {
				t.Errorf("%s:%d 接管列只改内存未落库（A1 禁止的写法）: %s", name, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// TestBothEntriesDelegateToSharedDecider 锁 1：两条入口共用一个裁决真相源
func TestBothEntriesDelegateToSharedDecider(t *testing.T) {
	for _, name := range []string{"chat_main.go", "chat_unauthorized.go"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("读 %s 失败: %v", name, err)
		}
		src := stripComments(string(raw))
		if !strings.Contains(src, "chatflow.HumanTakeoverDecide(") {
			t.Errorf("%s 未委托 chatflow.HumanTakeoverDecide，人工锁定态语义又分叉了（A1）", name)
		}
	}
}

// TestEnsureConversationHasNoTakeoverMutation 锁 3：chatEnsureConversation 只查/建会话 + 取裁决，
// 不再亲手改接管列（历史缺陷现场即此函数末尾那段"只改内存"）。
func TestEnsureConversationHasNoTakeoverMutation(t *testing.T) {
	raw, err := os.ReadFile("chat_main.go")
	if err != nil {
		t.Fatalf("读 chat_main.go 失败: %v", err)
	}
	src := stripComments(string(raw))
	start := strings.Index(src, "func (s *chatSessionCtx) chatEnsureConversation()")
	if start < 0 {
		t.Fatal("找不到 chatEnsureConversation，函数已改名请同步本守卫")
	}
	end := strings.Index(src[start:], "\nfunc ")
	if end < 0 {
		t.Fatal("chatEnsureConversation 函数体边界解析失败")
	}
	body := src[start : start+end]
	for _, bad := range []string{"s.conversation.IsHumanLocked =", "s.conversation.Mode =", "s.conversation.IsAiReplyEnabled =", "s.conversation.PendingHandoff ="} {
		if strings.Contains(body, bad) {
			t.Errorf("chatEnsureConversation 内不应再手写接管列 %s（判定与落库统一由 chatflow 负责）", bad)
		}
	}
}
