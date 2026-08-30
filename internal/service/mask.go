package service

// ============================================================
// PII 脱敏集中工具（P2 脱敏，2026-08-29）
//
// 统一手机号/邮箱/姓名脱敏，替代此前散落于 advisor.go / notifier.go / feedback.go 的重复实现。
// 规则：
//   手机：11 位大陆号 → 138****5678（中间 4 位掩码）
//   邮箱：保留首尾字符 + 域名，a***b@domain.com
//   姓名：2 字留尾、多字留首+尾
// 所有对外展示/入库的敏感字段须经此处，避免明文泄露。
// ============================================================

import "strings"

// MaskPhone 单手机号脱敏（非 11 位原样返回，防误伤）
func MaskPhone(phone string) string {
	if len(phone) != 11 {
		return phone
	}
	return phone[:3] + "****" + phone[7:]
}

// MaskPhoneInText 文本中所有连续 11 位数字（大陆手机号）就地脱敏
func MaskPhoneInText(text string) string {
	runes := []rune(text)
	digitRun := 0
	for i := 0; i <= len(runes); i++ {
		if i < len(runes) && runes[i] >= '0' && runes[i] <= '9' {
			digitRun++
			continue
		}
		if digitRun == 11 {
			for j := i - 11; j < i; j++ {
				if j >= i-7 && j < i-4 {
					runes[j] = '*'
				}
			}
		}
		digitRun = 0
	}
	return string(runes)
}

// MaskEmail 邮箱脱敏：保留首字符+末字符+域名，中间星号
func MaskEmail(email string) string {
	at := strings.Index(email, "@")
	if at <= 0 || at == len(email)-1 {
		return email // 非邮箱原样
	}
	local := email[:at]
	domain := email[at:] // @及之后（域名通常为 ASCII）
	ru := []rune(local)
	switch {
	case len(ru) == 1:
		return local + "***" + domain
	case len(ru) == 2:
		return string(ru[0]) + "*" + domain
	default:
		return string(ru[0]) + "***" + string(ru[len(ru)-1]) + domain
	}
}

// MaskName 姓名脱敏：2 字留尾，多字留首+尾，中间星号
func MaskName(name string) string {
	r := []rune(name)
	switch len(r) {
	case 0:
		return ""
	case 1:
		return string(r)
	case 2:
		return "*" + string(r[1])
	default:
		return string(r[0]) + "**" + string(r[len(r)-1])
	}
}
