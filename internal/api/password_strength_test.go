// 密码强度校验单测（残项收口 2026-09-19）：长度口径从"字节"改"字符(rune)"后钉死行为，
// 重点封堵原 len() 按字节判定的漏洞——中文/emoji 3 字符合 9 字节却只有 3 位的弱密码。
package api

import "testing"

// TestValidatePasswordStrengthRuneCount 锁定口令强度按 rune 而非字节计长（防中文 3 字符 9 字节被旧字节口径误判达标）。
func TestValidatePasswordStrengthRuneCount(t *testing.T) {
	cases := []struct {
		name string
		pwd  string
		ok   bool
	}{
		{"ASCII 8位达标", "abcd1234", true},
		{"ASCII 7位拒绝", "abcd123", false},
		{"中文3字符9字节：rune 口径必须拒绝（原字节口径误放行）", "中中中1a", false},
		{"中文+ASCII 混合恰好8字符", "密码abc123", true},
		{"emoji 4字符超字节但不足8位", "🙂🙂🙂🙂1a", false},
		{"纯字母8位无数字拒绝", "abcdefgh", false},
		{"纯数字8位无字母拒绝", "12345678", false},
		{"8字符含字母数字+符号达标", "p@ssw0rd", true},
		{"空串拒绝", "", false},
	}
	for _, tc := range cases {
		err := validatePasswordStrength(tc.pwd)
		if tc.ok && err != nil {
			t.Errorf("%s: 应通过，实际被拒: %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: 应被拒绝，实际放行", tc.name)
		}
	}
}
