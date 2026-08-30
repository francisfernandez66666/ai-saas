// 数据飞轮采集器：进程内批量缓冲 + 脱敏上报（休眠式，URL 空则零外发）。
package service

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"sync"
	"time"

	"ai-scrm/config"
)

// CollectorEvent 数据飞轮上报事件（脱敏后）
// ID 用于接收端幂等去重；Payload 内不得含明文 PII（发送前经 AnonymizePayload 处理）
type CollectorEvent struct {
	ID       string         `json:"id"`        // 事件唯一ID，用于接收端幂等去重
	TenantID uint           `json:"tenant_id"` // 租户ID，用于多租户数据隔离
	Kind     string         `json:"kind"`      // 事件类型：cdp_event/material/audit_increment/state_change
	Payload  map[string]any `json:"payload"`   // 脱敏后的事件载荷（PII已处理）
	Ts       int64          `json:"ts"`        // 事件发生时间戳（Unix秒）
}

// batchCollector 进程内批量缓冲（休眠式：URL 空则不发任何外部请求）
// 采用生产者-消费者模式，事件先缓冲在内存中，达到阈值或定时批量发送
type batchCollector struct {
	mu      sync.Mutex       // 互斥锁，保护缓冲区并发安全
	buf     []CollectorEvent // 事件缓冲区
	maxBuf  int              // 缓冲区最大容量，超出触发flush
	flushMs int64            // 自动刷新间隔（毫秒）
}

var defaultCollector = &batchCollector{maxBuf: 2000, flushMs: 300000} // 5分钟批次

// RandEventID 生成事件唯一 ID（接收端幂等用）
func RandEventID() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	return fmt.Sprintf("%d-%x", time.Now().UnixNano(), n.Int64())
}

// Collect 上报一条脱敏事件（Kind + 租户 + 载荷）；载荷内PII自动脱敏
// 未配置 COLLECTOR_URL 时整体丢弃（休眠式，零外部请求）
func Collect(kind string, tenantID uint, payload map[string]any) {
	if config.GlobalConfig == nil || config.GlobalConfig.Collector.URL == "" {
		return
	}
	payload = AnonymizePayload(payload)
	defaultCollector.mu.Lock()
	defaultCollector.buf = append(defaultCollector.buf, CollectorEvent{
		ID: RandEventID(), TenantID: tenantID, Kind: kind, Payload: payload, Ts: time.Now().Unix(),
	})
	// 缓冲溢出则立即触发一次 flush，避免内存无限增长
	if len(defaultCollector.buf) >= defaultCollector.maxBuf {
		go defaultCollector.flush()
	}
	defaultCollector.mu.Unlock()
}

// AnonymizePayload 递归脱敏 map 中的字符串值（手机号/邮箱/姓名）
func AnonymizePayload(p map[string]any) map[string]any {
	if p == nil {
		return nil
	}
	out := make(map[string]any, len(p))
	for k, v := range p {
		switch val := v.(type) {
		case string:
			out[k] = AnonymizeText(val)
		case map[string]any:
			out[k] = AnonymizePayload(val)
		case []any:
			arr := make([]any, len(val))
			for i, it := range val {
				if s, ok := it.(string); ok {
					arr[i] = AnonymizeText(s)
				} else {
					arr[i] = it
				}
			}
			out[k] = arr
		default:
			out[k] = v
		}
	}
	return out
}

// AnonymizeText 文本级脱敏（手机号+邮箱）
func AnonymizeText(s string) string {
	s = MaskPhoneInText(s)
	s = MaskEmailAddr(s)
	return s
}

// flush 把缓冲批量 POST 到 COLLECTOR_URL（带鉴权头）
func (bc *batchCollector) flush() {
	bc.mu.Lock()
	if len(bc.buf) == 0 {
		bc.mu.Unlock()
		return
	}
	batch := bc.buf
	bc.buf = nil
	bc.mu.Unlock()

	body, err := json.Marshal(map[string]any{"events": batch})
	if err != nil {
		log.Printf("[Collector] 序列化失败: %v", err)
		return
	}
	req, err := http.NewRequest(http.MethodPost, config.GlobalConfig.Collector.URL, bytes.NewReader(body))
	if err != nil {
		log.Printf("[Collector] 构造请求失败: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if config.GlobalConfig.Collector.Key != "" {
		req.Header.Set("X-Collector-Key", config.GlobalConfig.Collector.Key)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[Collector] 上报失败(将丢弃批次): %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("[Collector] 上报返回非成功码 %d", resp.StatusCode)
		return
	}
	log.Printf("[Collector] 已上报 %d 条事件 → %s", len(batch), config.GlobalConfig.Collector.URL)
}

// StartCollector 启动周期 flush（main.go 调用；URL 空时为空转）
func StartCollector() {
	go func() {
		ticker := time.NewTicker(time.Duration(defaultCollector.flushMs) * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			defaultCollector.flush()
		}
	}()
}

// ============================================================
// 数据飞轮 - 调参行为审计上报（P0-1，2026-08-30）
//
// 目标：管理员修改策略参数 → 审计落库 → collector → 素材池
// 数据流：admin_config.BatchUpdate → ReportTuningBehavior → Collect → 素材池
//
// 载荷结构：
//
//	{
//	  "action": "config_update",
//	  "operator": "admin",
//	  "tenant_id": 1,
//	  "changes": [{"key": "tau", "old": "0.8", "new": "0.9"}],
//	  "category": "strategy"
//	}
// ============================================================

// TuningChange 单项配置变更记录
type TuningChange struct {
	Key  string `json:"key"`  // 配置键名
	Old  string `json:"old"`  // 旧值
	New  string `json:"new"`  // 新值
	Kind string `json:"kind"` // 变更类型：strategy/reply_speed/mental_stage/ai_chain
}

// ReportTuningBehavior 上报调参行为审计（供数据飞轮素材池消费）
// 调用时机：admin_config.BatchUpdateSystemConfig / ResetSystemConfig / ForceInitSystemConfig
// 载荷经 AnonymizePayload 脱敏后上报
func ReportTuningBehavior(tenantID uint, operator string, category string, changes []TuningChange) {
	if len(changes) == 0 {
		return
	}
	payload := map[string]any{
		"action":    "config_update",
		"operator":  operator,
		"tenant_id": tenantID,
		"category":  category,
		"changes":   changes,
	}
	Collect("tuning_behavior", tenantID, payload)
	log.Printf("[DataFlywheel] 调参行为上报 tenant=%d operator=%s category=%s changes=%d",
		tenantID, operator, category, len(changes))
}
