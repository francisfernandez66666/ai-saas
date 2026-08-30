// 数据飞轮聚合接收端：校验鉴权 Key 后按事件 ID 幂等去重，仅 COLLECTOR_KEY 已配置才开放，避免无鉴权写入口。
package api

import (
	"net/http"
	"sync"

	"ai-scrm/config"
	"ai-scrm/internal/service"

	"github.com/gin-gonic/gin"
)

// seenCollectorIDs 接收端幂等去重（内存有界，超出上限重置）
var (
	seenMu           sync.Mutex
	seenCollectorIDs = make(map[string]struct{})
	seenCollectorCap = 1_000_000
)

// CollectorReceive POST /api/v1/collector
// 数据飞轮聚合接收端：校验鉴权 Key + 按事件 ID 幂等去重
// 仅当配置了 COLLECTOR_KEY 才开放（空 Key 视为关闭接收，避免成为无鉴权入口）
func CollectorReceive(c *gin.Context) {
	key := config.GlobalConfig.Collector.Key
	if key == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"code": 40101, "message": "接收端未启用（未配置 COLLECTOR_KEY）"})
		return
	}
	if c.GetHeader("X-Collector-Key") != key {
		c.JSON(http.StatusUnauthorized, gin.H{"code": 40101, "message": "鉴权失败"})
		return
	}
	var req struct {
		Events []service.CollectorEvent `json:"events"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Events) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40001, "message": "参数错误: 需 events 数组"})
		return
	}
	accepted := 0
	seenMu.Lock()
	for _, ev := range req.Events {
		if ev.ID == "" {
			ev.ID = service.RandEventID()
		}
		if _, dup := seenCollectorIDs[ev.ID]; dup {
			continue
		}
		seenCollectorIDs[ev.ID] = struct{}{}
		// 超出容量上限时整体重置（近似 LRU，避免内存泄漏）
		if len(seenCollectorIDs) > seenCollectorCap {
			seenCollectorIDs = make(map[string]struct{})
		}
		accepted++
	}
	seenMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "ok", "data": gin.H{"accepted": accepted, "total": len(req.Events)}})
}
