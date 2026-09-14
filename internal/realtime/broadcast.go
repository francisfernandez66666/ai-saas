// 跨实例 WS 广播：Redis 可用时将本地推送扇出到其他实例，单机模式保持原行为。
package realtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"ai-scrm/internal/redisclient"

	"github.com/redis/go-redis/v9"
)

// BroadcastEnvelope 跨实例 WS 事件信封（仅转发路由作用域与最终 payload）。
type BroadcastEnvelope struct {
	Origin     string `json:"origin"`      // 发送实例 ID（防回环）
	TenantID   uint   `json:"tenant_id"`   // 租户作用域（接收实例仍按此过滤）
	CustomerID uint   `json:"customer_id"` // 客户端定向目标（顾问端按租户全收）
	Data       []byte `json:"data"`        // 已序列化后的 WS 文本帧
}

// RemoteBroadcaster 跨实例广播出口（Redis 实现可在启动时注入，测试可注入 fake）。
type RemoteBroadcaster interface {
	Broadcast(BroadcastEnvelope) error
}

// wsChannel Redis Pub/Sub 频道（独立命名空间，避免与锁/列表键冲突）。
const wsChannel = "ai_scrm:ws:realtime:broadcast"

// instanceID 本进程实例标识（crypto/rand，初始化一次）。
var (
	instanceID     string
	broadcasterMu  sync.RWMutex
	broadcasterRef RemoteBroadcaster
)

// init 初始化当前包的注册表、客户端或默认配置。
func init() {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		log.Printf("[WS广播] 实例 ID 生成失败: %v", err)
	}
	instanceID = hex.EncodeToString(b)
}

// SetBroadcaster 注入跨实例广播器；传 nil 表示关闭远程广播。
func SetBroadcaster(b RemoteBroadcaster) {
	broadcasterMu.Lock()
	broadcasterRef = b
	broadcasterMu.Unlock()
}

// currentBroadcaster 读取当前广播器（读写锁，支持运行期热替换）。
func currentBroadcaster() RemoteBroadcaster {
	broadcasterMu.RLock()
	defer broadcasterMu.RUnlock()
	return broadcasterRef
}

// broadcast 旁路投递到远端；失败仅日志，不影响本地连接和 HTTP 轮询兜底。
func (h *Hub) broadcast(tenantID, customerID uint, data []byte) {
	b := currentBroadcaster()
	if b == nil || len(data) == 0 {
		return
	}
	if err := b.Broadcast(BroadcastEnvelope{
		Origin:     instanceID,
		TenantID:   tenantID,
		CustomerID: customerID,
		Data:       append([]byte(nil), data...),
	}); err != nil {
		log.Printf("[WS广播] Redis 发布失败 tenant=%d customer=%d err=%v（本地投递已完成，继续轮询兜底）", tenantID, customerID, err)
	}
}

// DeliverRemote 将其它实例转来的事件投递到本地 hub；禁止再次广播，避免环形扇出。
func (h *Hub) DeliverRemote(env BroadcastEnvelope) {
	if h == nil || env.Origin == instanceID || len(env.Data) == 0 {
		return
	}
	h.deliver(env.TenantID, env.CustomerID, env.Data)
}

// redisBroadcaster go-redis Pub/Sub 出口实现。
type redisBroadcaster struct{}

// Broadcast 序列化信封后发布；短超时避免拖慢 WS 调用方。
func (redisBroadcaster) Broadcast(env BroadcastEnvelope) error {
	client := redisclient.Client()
	if client == nil {
		return errors.New("Redis 未启用")
	}
	env.Origin = instanceID // 强制使用本地实例 ID，防测试/fake 伪造回环标记
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return client.Publish(ctx, wsChannel, body).Err()
}

// StartRedisBroadcast 在 Redis 可用时启用 WS 跨实例广播（未启用保持单实例行为）。
func StartRedisBroadcast(h *Hub) {
	if h == nil || !redisclient.IsEnabled() {
		return
	}
	client := redisclient.Client()
	if client == nil {
		return
	}
	SetBroadcaster(redisBroadcaster{})
	go subscribeRedisBroadcast(h, client)
	log.Printf("[WS广播] 已启用 Redis 跨实例扇出 channel=%s instance=%s", wsChannel, instanceID)
}

// subscribeRedisBroadcast 长连接订阅远端事件；断开后指数退避重连，恢复期靠轮询兜底。
func subscribeRedisBroadcast(h *Hub, client *redis.Client) {
	backoff := time.Second
	for {
		ctx, cancel := context.WithCancel(context.Background())
		pubsub := client.Subscribe(ctx, wsChannel)
		ch := pubsub.Channel()
		for msg := range ch {
			var env BroadcastEnvelope
			if err := json.Unmarshal([]byte(msg.Payload), &env); err != nil {
				log.Printf("[WS广播] 非法远端信封: %v", err)
				continue
			}
			h.DeliverRemote(env)
		}
		_ = pubsub.Close()
		cancel()
		log.Printf("[WS广播] Redis 订阅断开，%s 后重连", backoff)
		time.Sleep(backoff)
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}
