// AI 错误分类：认证类 vs 其它（P2-58 熔断策略差异化）。
package ai

import "strings"

// errClass 错误类别枚举（小型内部类型，不做导出）
type errClass int

const (
	errClassUnknown errClass = iota
	errClassAuth             // 认证/账户级错误（401/403/invalid key/unauthorized）——不会自愈，应熔断
)

// classifyAIError 分类 AI 调用错误
// 仅区分"认证财务级"与"其它"，429/超时/网络类仍走常规短冷却累加。
func classifyAIError(err error) errClass {
	if err == nil {
		return errClassUnknown
	}
	lower := strings.ToLower(err.Error())
	for _, marker := range []string{
		"401", "403", "unauthorized", "auth failed", "invalid key", "invalid api key",
		"authentication", "permission denied", "credential", "signature mismatch",
	} {
		if strings.Contains(lower, marker) {
			return errClassAuth
		}
	}
	return errClassUnknown
}