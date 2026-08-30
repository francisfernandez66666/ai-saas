// 数据飞轮聚合接收端：校验鉴权 Key 后按事件 ID 幂等去重，仅 COLLECTOR_KEY 已配置才开放，避免无鉴权写入口。
package api

import (
	"net/http"
	"sync"

	"ai-scrm/config"
	"ai-scrm/internal/service"

	"github.com/gin-gonic/gin"
)

/*
seenCollectorIDs 接收端幂等去重机制

设计说明：
  - 使用内存 map 记录已处理的事件 ID，实现幂等去重
  - 容量上限 100 万，超出后整体重置（近似 LRU，避免内存泄漏）
  - 适用于高并发场景，通过互斥锁保证并发安全
*/
var (
	seenMu           sync.Mutex
	seenCollectorIDs = make(map[string]struct{})
	seenCollectorCap = 1_000_000
)

/*
CollectorReceive 数据飞轮聚合接收端

POST /api/v1/collector

功能：接收外部数据事件，经鉴权后按事件 ID 幂等去重。

鉴权逻辑：
  1. 检查 COLLECTOR_KEY 是否配置（未配置则拒绝，避免成为无鉴权入口）
  2. 校验请求头 X-Collector-Key 是否匹配

幂等去重：
  - 事件 ID 为空时自动生成随机 ID
  - 已存在的事件 ID 直接跳过
  - 超出容量上限时重置去重 map

参数：请求体 JSON 格式 {"events": [...]}
返回：{"accepted": 已接受数, "total": 总请求数}
*/
func CollectorReceive(c *gin.Context) {
	// fail-closed 设计：未配置 Key 时拒绝所有请求
	key := config.GlobalConfig.Collector.Key
	if key == "" {
		RespErr(c, http.StatusUnauthorized, 40101, "接收端未启用（未配置 COLLECTOR_KEY）")
		return
	}
	// 鉴权校验：请求头必须携带有效 Key
	if c.GetHeader("X-Collector-Key") != key {
		RespErr(c, http.StatusUnauthorized, 40101, "鉴权失败")
		return
	}
	var req struct {
		Events []service.CollectorEvent `json:"events"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Events) == 0 {
		RespErr(c, http.StatusBadRequest, 40001, "参数错误: 需 events 数组")
		return
	}
	accepted := 0
	seenMu.Lock()
	for _, ev := range req.Events {
		// 事件 ID 为空时自动生成随机 ID
		if ev.ID == "" {
			ev.ID = service.RandEventID()
		}
		// 幂等去重：已存在的事件 ID 直接跳过
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
	RespOK(c, "ok", gin.H{"accepted": accepted, "total": len(req.Events)})
}
