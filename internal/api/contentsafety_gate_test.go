// 内容安全出站闸门单测（C1 收口，2026-10-16）。
//
// 背景：contentsafety_mode 出厂默认 2026-10-16 由 shadow 翻 enforce（用户拍板），
// 而 enforce 的两条真拦分支（BLOCK→GateBlock / MASK→GateRewrite）此前**零单测覆盖**
// ——默认 shadow 时代它们永远走不到。翻默认值不放断言=把"真拦"交给运气。
//
// 词库用临时文件自造（不依赖仓库 config/sensitive_words.txt 的内容漂移），
// 并带前置自检：词库没装进去时后续"必拦"断言会在空库上假绿。
package api

import (
	"os"
	"path/filepath"
	"testing"

	"ai-scrm/internal/contentsafety"
	"ai-scrm/internal/runtimecfg"
)

// withGateWords 装一份自制词库（BLOCK:洗钱 / MASK:全球第一），返回还原函数。
// 与 contentsafety 包的 withWordFile 同思路，但 api 包摸不到对方私有 path 变量，
// 故用 Chdir 到临时目录 + contentsafety.Load()（读相对路径 config/sensitive_words.txt）。
func withGateWords(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "sensitive_words.txt"),
		[]byte("BLOCK:洗钱\nMASK:全球第一\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	contentsafety.Load()
	if err := os.Chdir(wd); err != nil {
		t.Fatal(err)
	}
	// 前置自检：词库必须真装进去——Check 对两个词都命中才继续，
	// 否则后面每条"必拦"断言都是在空词库上永绿。
	if r := contentsafety.Check("洗钱"); !r.Hit || r.Level != contentsafety.LevelBlock {
		t.Fatalf("前置自检失败：BLOCK 词未加载，后续断言将假绿: %+v", r)
	}
	if r := contentsafety.Check("全球第一"); !r.Hit || r.Level != contentsafety.LevelMask {
		t.Fatalf("前置自检失败：MASK 词未加载，后续断言将假绿: %+v", r)
	}
	t.Cleanup(func() {
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
		contentsafety.Load() // 空目录下加载=清空词库，别污染同包其他用例
		if err := os.Chdir(wd); err != nil {
			t.Fatal(err)
		}
		contentsafety.SetReviewer(nil)
	})
}

// withGateMode 注入平台级 contentsafety_mode，返回还原函数。
func withGateMode(t *testing.T, mode string) {
	t.Helper()
	restore := runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(
		map[string]string{"contentsafety_mode": mode}, nil))
	t.Cleanup(restore)
}

// TestContentsafetyGateEnforceBlock enforce + BLOCK 命中：必须 GateBlock 且出站文本置空
// （调用方收到空串后走转人工退场话术，原回复不得出站）。
func TestContentsafetyGateEnforceBlock(t *testing.T) {
	withGateWords(t)
	withGateMode(t, "enforce")

	action, out := ContentsafetyGate("我们这不支持洗钱", 42)
	if action != GateBlock {
		t.Fatalf("enforce+BLOCK 应为 GateBlock，实际 action=%d out=%q", action, out)
	}
	if out != "" {
		t.Fatalf("BLOCK 时出站文本必须置空（原回复不得流出），实际 out=%q", out)
	}
}

// TestContentsafetyGateEnforceMask enforce + MASK 命中：必须 GateRewrite 且替换为 **。
func TestContentsafetyGateEnforceMask(t *testing.T) {
	withGateWords(t)
	withGateMode(t, "enforce")

	action, out := ContentsafetyGate("这款车全球第一省油", 42)
	if action != GateRewrite {
		t.Fatalf("enforce+MASK 应为 GateRewrite，实际 action=%d out=%q", action, out)
	}
	if out == "这款车全球第一省油" {
		t.Fatalf("MASK 命中后文本必须被改写，实际原样返回: %q", out)
	}
	if want := "这款车**省油"; out != want {
		t.Fatalf("MASK 改写结果不符: 期望 %q 实际 %q", want, out)
	}
}

// TestContentsafetyGateShadowStillPass shadow 模式（平台级回退态）恒放行：
// 判级逻辑必须保留——出厂翻了 enforce，但应急回退 shadow 时不能变成"shadow 也拦"。
func TestContentsafetyGateShadowStillPass(t *testing.T) {
	withGateWords(t)
	withGateMode(t, "shadow")

	action, out := ContentsafetyGate("我们这不支持洗钱", 42)
	if action != GatePass || out != "我们这不支持洗钱" {
		t.Fatalf("shadow 命中应原样放行（只计数），实际 action=%d out=%q", action, out)
	}
}

// TestContentsafetyGateDisabledAndClean 两个"不应拦"的腿：
// 闸门总关（enabled=false）与 enforce 下无命中——都不得改写。
func TestContentsafetyGateDisabledAndClean(t *testing.T) {
	withGateWords(t)

	restore := runtimecfg.SetDefaultForTest(runtimecfg.NewStaticService(
		map[string]string{"contentsafety_enabled": "false", "contentsafety_mode": "enforce"}, nil))
	action, out := ContentsafetyGate("我们这不支持洗钱", 42)
	restore()
	if action != GatePass || out != "我们这不支持洗钱" {
		t.Fatalf("enabled=false 应整体放行，实际 action=%d out=%q", action, out)
	}

	withGateMode(t, "enforce")
	action, out = ContentsafetyGate("今天天气不错", 42)
	if action != GatePass || out != "今天天气不错" {
		t.Fatalf("无命中应放行，实际 action=%d out=%q", action, out)
	}

	// 空回复短路：连词库都不该过（早期实现靠 Cleaned 初值兜底，这里锁住行为）
	action, out = ContentsafetyGate("", 42)
	if action != GatePass || out != "" {
		t.Fatalf("空回复应直接放行，实际 action=%d out=%q", action, out)
	}
}
