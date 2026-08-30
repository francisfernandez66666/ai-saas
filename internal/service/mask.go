// Package service 提供 SCRM 业务服务层实现（计费/消息/配置/脱敏/监控/向量等）。
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
// 脱敏规则：保留前3位和后4位，中间4位用*替换
// 示例：13812345678 → 138****5678
func MaskPhone(phone string) string {
	if len(phone) != 11 {
		return phone // 非11位手机号原样返回，避免误伤
	}
	return phone[:3] + "****" + phone[7:]
}

// MaskPhoneInText 文本中所有连续 11 位数字（大陆手机号）就地脱敏
// 使用rune切片直接修改，避免字符串拼接开销
// 脱敏规则同MaskPhone，保留前3后4，中间4位替换为*
func MaskPhoneInText(text string) string {
	runes := []rune(text)
	digitRun := 0 // 连续数字计数器
	for i := 0; i <= len(runes); i++ {
		if i < len(runes) && runes[i] >= '0' && runes[i] <= '9' {
			digitRun++
			continue
		}
		// 遇到非数字或结尾，检查是否有11位连续数字（疑似手机号）
		if digitRun == 11 {
			// 脱敏中间4位：索引 [i-11, i-4) 区间
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
// 规则：a***b@domain.com
// 特殊处理：1字符邮箱、2字符邮箱、普通邮箱
func MaskEmail(email string) string {
	at := strings.Index(email, "@")
	if at <= 0 || at == len(email)-1 {
		return email // 非邮箱格式原样返回
	}
	local := email[:at]  // @前的本地部分
	domain := email[at:] // @及之后（域名通常为 ASCII）
	ru := []rune(local)
	switch {
	case len(ru) == 1:
		return local + "***" + domain // 单字符本地部分
	case len(ru) == 2:
		return string(ru[0]) + "*" + domain // 双字符本地部分
	default:
		// 普通邮箱：首字符 + *** + 末字符 + 域名
		return string(ru[0]) + "***" + string(ru[len(ru)-1]) + domain
	}
}

// MaskName 姓名脱敏：2字留尾，多字留首+尾，中间星号
// 规则：单字原样、2字留尾（*明）、多字留首+尾（张**明）
func MaskName(name string) string {
	r := []rune(name)
	switch len(r) {
	case 0:
		return ""
	case 1:
		return string(r) // 单字姓名原样
	case 2:
		return "*" + string(r[1]) // 双字姓名：*明
	default:
		// 多字姓名：张**明
		return string(r[0]) + "**" + string(r[len(r)-1])
	}
}
