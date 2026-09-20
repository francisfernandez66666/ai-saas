// 数据飞轮聚合接收端：校验鉴权 Key 后按事件 ID 幂等去重，仅 COLLECTOR_KEY 已配置才开放，避免无鉴权写入口。
package api

import (
	"log"
	"net/http"
	"sync"
	"time"

	"ai-scrm/config"
	"ai-scrm/internal/redisclient"
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

// collectorSeenTTL P2-6(2026-09-20 批三)：Redis 幂等键保留期——重推窗口（微信侧分钟级/
// 采集端小时级批重试）远小于此值，7 天后同 ID 再报视为新事件可接受。
const collectorSeenTTL = 7 * 24 * time.Hour

// claimCollectorEvent P2-6 修复(2026-09-20 批三)：幂等去重从"仅本机内存 map"升为 Redis 双轨——
// 旧实现在进程重启后全量失忆、多副本各有一套 seen 表，同一事件重投/打到另一副本即二次落库。
// 返回 true=本事件为新（已认领）。Redis 出错退回内存表兜底（与 ClaimReplyDelivery 同口径：
// 故障≠他人已认领，宁可依赖单机去重也不能整批拒收）。
func claimCollectorEvent(id string) bool {
	if redisclient.IsEnabled() {
		acquired, err := redisclient.SetNXExE("coll:seen:"+id, "1", collectorSeenTTL)
		if err == nil {
			return acquired
		}
		log.Printf("[Collector] Redis 幂等键写入失败，降级内存去重: %v", err)
	}
	seenMu.Lock()
	defer seenMu.Unlock()
	if _, dup := seenCollectorIDs[id]; dup {
		return false
	}
	seenCollectorIDs[id] = struct{}{}
	if len(seenCollectorIDs) > seenCollectorCap {
		seenCollectorIDs = make(map[string]struct{})
	}
	return true
}

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
// CollectorReceive 接收数据飞轮批量上报并完成脱敏入库。
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
	var fresh []service.CollectorEvent
	for _, ev := range req.Events {
		// 事件 ID 为空时自动生成随机 ID
		if ev.ID == "" {
			ev.ID = service.RandEventID()
		}
		// 幂等去重：P2-6 起走 Redis 双轨认领（重启/多副本不再失忆）
		if !claimCollectorEvent(ev.ID) {
			continue
		}
		accepted++
		fresh = append(fresh, ev)
	}
	// P1-11 修复(2026-09-09)：接收端不再只做内存去重计数——把去重后的事件持久化到
	// kb_feedback_materials 素材池（走既有 evals 审核流），对外"数据飞轮聚合接收端"真正闭环。
	ingested, ingestErr := service.IngestCollectorEvents(fresh)
	if ingestErr != nil {
		log.Printf("[Collector] 素材落库失败: %v", ingestErr)
	}
	RespOK(c, "ok", gin.H{"accepted": accepted, "total": len(req.Events), "ingested": ingested})
}
