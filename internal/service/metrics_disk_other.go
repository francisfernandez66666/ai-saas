//go:build !linux && !darwin

// Package service 提供 SCRM 业务服务层实现（计费/消息/配置/脱敏/监控/向量等）。
package service

// diskUsedRatio 非 unix 平台暂不支持（Windows 无 syscall.Statfs），返回 ok=false 不暴露该指标
func diskUsedRatio() (float64, bool) {
	return 0, false
}
