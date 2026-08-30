// 平台级健康监控：返回结构化探针（DB/合并队列深度/24h严重事件/goroutine），含阈值分级与告警态，供超管哨兵使用。
package api

import (
	"ai-scrm/internal/service"

	"github.com/gin-gonic/gin"
)

// SuperMonitorHealth 平台级健康探测详情（P1-4，2026-08-29）
// 返回结构化探针：DB/合并队列深度/24h 严重事件/goroutine，含阈值分级与告警触发态
func SuperMonitorHealth(c *gin.Context) {
	snap := service.ComputeHealth()
	respOK(c, snap)
}
