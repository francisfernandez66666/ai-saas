//go:build linux || darwin

// Package metrics 承载零依赖 Prometheus 文本指标采集与平台磁盘水位探测。
package metrics

import "syscall"

// diskUsedRatio 返回进程工作目录所在文件系统的已用比例（0~1）；失败时 ok=false
func diskUsedRatio() (float64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(".", &st); err != nil {
		return 0, false
	}
	if st.Blocks == 0 {
		return 0, false
	}
	used := st.Blocks - st.Bavail
	return float64(used) / float64(st.Blocks), true
}
