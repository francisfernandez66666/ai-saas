// Package contentsafety 内容安全闸门（C1，2026-09-12）
// 背景：国内生成式 AI 商用的合规前置——AI 出站回复必须有敏感内容过滤。
// 设计：
//   - 两级词库（config/sensitive_words.txt，可热加载）：
//     BLOCK:<词>  实锤违规 → enforce 模式丢弃 AI 回复、调用方转人工（客户无感知）
//     MASK:<词>   规则词   → enforce 模式整词替换为 **（如绝对化用语、误杀率高的擦边词）
//   - 模式：contentsafety_mode=shadow|enforce。shadow 只告警+计数不改文本（上线首周演练误杀率）。
//   - 开关：contentsafety_enabled（平台级热开关，默认 true）。
//   - 第三方机审：Reviewer 接口预留（本地 DFA 恒先行；HTTP 机审按配置叠加，凭证走平台配置）。
package contentsafety

import (
	"log"
	"os"
	"strings"
	"sync"
)

// Level 命中等级
type Level string

const (
	LevelNone  Level = ""
	LevelMask  Level = "mask"
	LevelBlock Level = "block"
)

// Result 检查结论
type Result struct {
	Hit     bool     // 是否命中
	Level   Level    // 最高命中等级
	Words   []string // 命中词（审计展示）
	Cleaned string   // MASK 替换后的文本（BLOCK 时为空串由调用方决定话术）
}

// ============================================================
// DFA 词库（trie + fail 指针的简化版：短词库用逐位置匹配足够，≤千级词无压力）
// ============================================================

type wordSet struct {
	mask  []string
	block []string
}

var (
	mu     sync.RWMutex
	loaded wordSet
	path   = "config/sensitive_words.txt"
)

// Load 加载词库（启动调用；文件缺失时静默空库不阻塞启动——闸门自动退化为 no-op）
func Load() {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[内容安全] 词库文件缺失(%s)，闸门以空词库运行（仅第三方机审生效）", path)
		return
	}
	var ws wordSet
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lvl, word, ok := strings.Cut(line, ":")
		if !ok {
			word = lvl // 无前缀按 MASK 处理
			lvl = "MASK"
		}
		word = strings.TrimSpace(word)
		if word == "" {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(lvl)) {
		case "BLOCK":
			ws.block = append(ws.block, word)
		default:
			ws.mask = append(ws.mask, word)
		}
	}
	mu.Lock()
	loaded = ws
	mu.Unlock()
	log.Printf("[内容安全] 词库加载完成: BLOCK=%d MASK=%d", len(ws.block), len(ws.mask))
}

// Check 对文本做词库扫描：返回最高等级命中 + MASK 清洗结果
func Check(text string) Result {
	mu.RLock()
	ws := loaded
	mu.RUnlock()
	res := Result{Cleaned: text}
	if text == "" || (len(ws.block) == 0 && len(ws.mask) == 0) {
		return res
	}
	for _, w := range ws.block {
		if strings.Contains(text, w) {
			res.Hit = true
			res.Level = LevelBlock // BLOCK 最高，短路
			res.Words = append(res.Words, w)
			return res
		}
	}
	cleaned := text
	for _, w := range ws.mask {
		if strings.Contains(cleaned, w) {
			res.Hit = true
			if res.Level == "" {
				res.Level = LevelMask
			}
			res.Words = append(res.Words, w)
			cleaned = strings.ReplaceAll(cleaned, w, "**")
		}
	}
	res.Cleaned = cleaned
	return res
}

// Reviewer 第三方机审接口（可选叠加，如阿里绿网/腾讯天御）。
// nil 表示未配置——本地词库结果即为最终结论。
type Reviewer interface {
	// Review 返回违规=true（机审失败按放行处理=fail-open，避免外部抖动阻断对话）
	Review(text string) (violation bool, reason string, err error)
}

var (
	reviewerMu sync.RWMutex
	reviewer   Reviewer
)

// SetReviewer 注册第三方机审（启动装配；E2E 注入 httptest 实现）
func SetReviewer(r Reviewer) {
	reviewerMu.Lock()
	reviewer = r
	reviewerMu.Unlock()
}

// CheckFull 词库 + 第三方机审组合检查
func CheckFull(text string) Result {
	res := Check(text)
	reviewerMu.RLock()
	rev := reviewer
	reviewerMu.RUnlock()
	if rev == nil || text == "" {
		return res
	}
	violation, reason, err := rev.Review(text)
	if err != nil {
		log.Printf("[内容安全] 第三方机审异常（fail-open 放行）: %v", err)
		return res
	}
	if violation {
		res.Hit = true
		res.Level = LevelBlock
		res.Words = append(res.Words, "machine:"+reason)
	}
	return res
}
