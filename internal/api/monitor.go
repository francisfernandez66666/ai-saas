// 平台级健康监控：返回结构化探针（DB/合并队列深度/24h严重事件/goroutine），含阈值分级与告警态，供超管哨兵使用。
package api

import (
	"ai-scrm/internal/service"

	"github.com/gin-gonic/gin"
)

/*
SuperMonitorHealth 平台级健康探测详情（P1-4，2026-08-29）

用途：为运维/超管提供平台级健康状态快照，支持哨兵监控与故障定位。
返回数据包含：
  - DB 连接池状态
  - 合并队列深度（消息积压）
  - 24h 内严重事件数
  - 当前 goroutine 数量

阈值分级与告警态用于快速定位系统瓶颈。
*/
func SuperMonitorHealth(c *gin.Context) {
	// 调用 service 层计算平台健康快照（聚合多维度指标）
	snap := service.ComputeHealth()
	respOK(c, snap)
}
