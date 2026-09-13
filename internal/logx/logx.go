// Package logx 日志脱敏工具（C3，2026-09-12）
// 背景：UAT 实证 GORM logger.Info 与业务 log.Printf 会把客户消息正文/手机号明文写盘——合规风险。
// 本包自包含（不依赖 service，避免循环）：正则手机号/身份证/邮箱就地掩码。
// 出口：
//   - logx.Safe(text, max)    业务日志：>max 字截断 + PII 掩码，替换裸 log.Printf(content)
//   - logx.Mask(text)         纯掩码（GORM SQL 包装层用）
//   - logx.NewGormLogger(...) 脱敏版 GORM logger（Info 态 SQL 参数含手机号也不落盘）
package logx

import (
	"regexp"
	"strings"
)

var (
	// 手机号：11 位大陆号，\b 词边界防误伤长数字串内部且支持相邻匹配（RE2 支持 \b）
	phoneRe = regexp.MustCompile(`\b(1[3-9][0-9]{9})\b`)
	// 身份证：18 位（末位可 X）
	idRe = regexp.MustCompile(`\b([0-9]{17}[0-9Xx])\b`)
	// 邮箱：粗粒度 local@domain
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
)

// Mask 就地掩码文本中的手机号/身份证/邮箱，保留可辨识首尾。多次调用幂等。
func Mask(text string) string {
	if text == "" {
		return text
	}
	text = maskByRe(phoneRe, text, maskPhone)
	text = maskByRe(idRe, text, maskID)
	text = emailRe.ReplaceAllStringFunc(text, maskEmailLocal)
	return text
}

// maskByRe 用正则找 PII 段，仅掩码捕获组 group=1 命中的子串，保留其前后边界字符。
func maskByRe(re *regexp.Regexp, text string, fn func(s []byte) []byte) string {
	var b strings.Builder
	last := 0
	for _, loc := range re.FindAllStringSubmatchIndex(text, -1) {
		// loc[2:4] 是第一捕获组（PII 本体）的起止，前后是边界字符
		span := loc[2:4]
		if span[0] < last {
			continue // 重叠命中跳过（前一段已吞并）
		}
		b.WriteString(text[last:span[0]])
		b.Write(fn([]byte(text[span[0]:span[1]])))
		last = span[1]
	}
	b.WriteString(text[last:])
	return b.String()
}

func maskPhone(p []byte) []byte { // p 为 11 位号段
	if len(p) != 11 {
		return p
	}
	out := make([]byte, 0, 11)
	out = append(out, p[:3]...)
	out = append(out, '*', '*', '*', '*')
	out = append(out, p[7:]...)
	return out
}

func maskID(p []byte) []byte { // p 为 18 位号段
	if len(p) != 18 {
		return p
	}
	out := make([]byte, 0, 18)
	out = append(out, p[:4]...)
	out = append(out, strings.Repeat("*", 10)...)
	out = append(out, p[14:]...)
	return out
}

func maskEmailLocal(s string) string {
	at := strings.Index(s, "@")
	if at <= 0 {
		return "***" + s
	}
	r := []rune(s[:at])
	var head string
	if len(r) >= 2 {
		head = string(r[0]) + "***" + string(r[len(r)-1])
	} else {
		head = string(r[0]) + "***"
	}
	return head + s[at:]
}

// Safe 业务日志安全化：>max 字截断（保留前缀 + …）后做 PII 掩码。
// 用于替换所有 log.Printf("...content: %s", 客户原文) 之类明文落盘点。
func Safe(text string, max int) string {
	if max <= 0 {
		max = 20
	}
	r := []rune(text)
	if len(r) > max {
		text = string(r[:max]) + "…"
	}
	return Mask(text)
}
