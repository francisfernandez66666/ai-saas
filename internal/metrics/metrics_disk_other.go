//go:build !linux && !darwin

// Package metrics 承载零依赖 Prometheus 文本指标采集与平台磁盘水位探测。
package metrics

// diskUsedRatio 非 unix 平台暂不支持（Windows 无 syscall.Statfs），返回 ok=false 不暴露该指标
func diskUsedRatio() (float64, bool) {
	return 0, false
}
