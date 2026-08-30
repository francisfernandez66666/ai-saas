// Package api 错误码推导单元测试（P2-3 错误码全量迁移，2026-08-30）
package api

import "testing"

// TestCodeNameFromCode 语义码与存量魔数 → 机器可读错误码推导
func TestCodeNameFromCode(t *testing.T) {
	cases := []struct {
		code int
		want string
	}{
		{0, ""},                // 成功不出 error_code
		{40001, "param_error"}, // 语义码直接映射
		{40101, "unauthorized"},
		{40301, "forbidden"},
		{40401, "not_found"},
		{42901, "rate_limited"},
		{42001, "biz_error"},
		{50001, "internal_error"},
		{400, "param_error"},    // 存量魔数归类
		{401, "unauthorized"},   //
		{403, "forbidden"},      //
		{404, "not_found"},      //
		{409, "biz_error"},      // 幂等冲突
		{429, "rate_limited"},   //
		{500, "internal_error"}, //
		{502, "internal_error"}, // 第三方通信失败
	}
	for _, c := range cases {
		if got := codeNameFromCode(c.code); got != c.want {
			t.Errorf("codeNameFromCode(%d)=%q 期望 %q", c.code, got, c.want)
		}
	}
}
