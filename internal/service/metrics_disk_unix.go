//go:build linux || darwin

// Package service 提供 SCRM 业务服务层实现（计费/消息/配置/脱敏/监控/向量等）。
package service

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
