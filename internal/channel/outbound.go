// 出站统一队列（W6，2026-09-12）
// 把"回复产生"与"通道发送"解耦：AI/人工回复落库后 Enqueue 进 channel_outbound，
// main.go 后台 ticker `channel:outbound` 每 3s 批量取到期项→按通道适配器发送→指数退避重试 ≤5 次→失败进死信(failed)。
// 死信在 /admin 通道页可见并可人工重发（Retry）。
package channel

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/metrics"
	"ai-scrm/internal/model"
)

// maxRetries 最大自动重试次数（超过转 failed 死信）
const maxRetries = 5

// Enqueue 将一条回复投递到指定会话对应通道的出站队列（异步发送）。
func Enqueue(tenantID, channelID, customerID, conversationID uint, content, msgType string) error {
	if content == "" {
		return nil
	}
	if msgType == "" {
		msgType = "text"
	}
	now := time.Now()
	ob := model.ChannelOutbound{
		TenantID:       tenantID,
		ChannelID:      channelID,
		CustomerID:     customerID,
		ConversationID: conversationID,
		Content:        content,
		MsgType:        msgType,
		Status:         model.OutboundPending,
		NextRetryAt:    &now,
	}
	return db.DB.Create(&ob).Error
}

// nextBackoff 指数退避：3s * 2^retries（上限 ~5min）。
func nextBackoff(retries int) time.Duration {
	d := 3 * time.Second * time.Duration(math.Pow(2, float64(retries)))
	if d > 5*time.Minute {
		d = 5 * time.Minute
	}
	return d
}

// ProcessDueOutbound 取到期的 pending 批次并发送；返回本轮成功数、失败(仍重试)数、死信数。
// 由 main.go 后台 ticker（channel:outbound，3s）调用；多实例经 Redis 锁选主（此处仅本地循环，锁在调用侧）。
func ProcessDueOutbound(ctx context.Context) (sent, retried, dead int) {
	now := time.Now()
	var due []model.ChannelOutbound
	// 单次上限 50，避免长事务；status=pending 且到 next_retry_at
	if err := db.DB.Where("status = ? AND (next_retry_at IS NULL OR next_retry_at <= ?)", model.OutboundPending, now).
		Order("id ASC").Limit(50).Find(&due).Error; err != nil {
		log.Printf("[出站队列] 取到期项失败: %v", err)
		return
	}
	for i := range due {
		ob := &due[i]
		res := sendOne(ctx, ob)
		switch {
		case res.Err == nil && res.Sent:
			db.DB.Model(ob).Updates(map[string]interface{}{"status": model.OutboundSent, "sent_at": time.Now(), "error": ""})
			sent++
		case res.Fatal:
			db.DB.Model(ob).Updates(map[string]interface{}{"status": model.OutboundFailed, "error": truncateErr(res.Err)})
			log.Printf("[出站队列] 不可重试错误转死信 id=%d: %v", ob.ID, res.Err)
			metrics.IncChannelDeadLetter("fatal") // F6：死信速率指标
			dead++
		default:
			// 可重试：退避递增，超限转死信
			ob.Retries++
			if ob.Retries > maxRetries {
				db.DB.Model(ob).Updates(map[string]interface{}{"status": model.OutboundFailed, "error": truncateErr(res.Err)})
				log.Printf("[出站队列] 超过最大重试转死信 id=%d: %v", ob.ID, res.Err)
				metrics.IncChannelDeadLetter("exhausted") // F6：死信速率指标
				dead++
			} else {
				nt := time.Now().Add(nextBackoff(ob.Retries))
				db.DB.Model(ob).Updates(map[string]interface{}{"retries": ob.Retries, "next_retry_at": nt, "error": truncateErr(res.Err)})
				retried++
			}
		}
	}
	if len(due) > 0 {
		// F6：本轮有处理即刷新死信积压水位（gauge），供 /metrics 告警阈值消费。
		var pending int64
		db.DB.Model(&model.ChannelOutbound{}).Where("status = ?", model.OutboundFailed).Count(&pending)
		metrics.SetChannelDeadLetterPending(pending)
	}
	return
}

// sendOne 取通道+凭据+适配器执行一次发送；token -1 失效强制刷新重试一次。
func sendOne(ctx context.Context, ob *model.ChannelOutbound) SendResult {
	var ch model.Channel
	if err := db.DB.First(&ch, ob.ChannelID).Error; err != nil {
		return SendResult{Fatal: true, Err: fmt.Errorf("通道不存在 id=%d: %w", ob.ChannelID, err)}
	}
	if ch.Status != model.ChannelStatusActive {
		return SendResult{Fatal: true, Err: errors.New("通道未启用")}
	}
	adapter, ok := AdapterFor(&ch)
	if !ok {
		return SendResult{Fatal: true, Err: fmt.Errorf("无对应适配器 type=%s", ch.Type)}
	}
	cred, err := DecryptCredential(&ch)
	if err != nil {
		return SendResult{Fatal: true, Err: err} // 凭据需重录，重试无意义
	}
	// externalID：按 (channel_id, customer_id) 反查渠道外部号
	externalID, err := externalIDFor(ob.ChannelID, ob.CustomerID)
	if err != nil {
		return SendResult{Fatal: true, Err: err}
	}
	res := adapter.SendText(ctx, cred, externalID, ob.Content, ob.MsgType)
	// token 失效(-1)：强制刷新后重试一次
	if res.Err != nil && isTokenInvalidErr(res.Err) {
		defaultTokenManager.Invalidate(ch.ID)
		res = adapter.SendText(ctx, cred, externalID, ob.Content, ob.MsgType)
	}
	return res
}

// truncateErr 截断错误文本，避免出站队列记录超长异常。
func truncateErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 480 {
		s = s[:480]
	}
	return s
}

// RetryDeadLetter 死信人工重发（/admin 通道页）：重置 pending + retries=0。
func RetryDeadLetter(tenantID, id uint) error {
	res := db.DB.Model(&model.ChannelOutbound{}).
		Where("id = ? AND tenant_id = ? AND status = ?", id, tenantID, model.OutboundFailed).
		Updates(map[string]interface{}{"status": model.OutboundPending, "retries": 0, "next_retry_at": time.Now(), "error": ""})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("死信不存在或非 failed 状态")
	}
	// F6：人工重发后刷新死信积压水位（低频管理员操作，重算全局 failed 计数成本可接受）
	var pending int64
	db.DB.Model(&model.ChannelOutbound{}).Where("status = ?", model.OutboundFailed).Count(&pending)
	metrics.SetChannelDeadLetterPending(pending)
	return nil
}

// ListDeadLetters 死信列表（/admin）。
func ListDeadLetters(tenantID uint) ([]model.ChannelOutbound, error) {
	var list []model.ChannelOutbound
	err := db.DB.Where("tenant_id = ? AND status = ?", tenantID, model.OutboundFailed).
		Order("id DESC").Limit(200).Find(&list).Error
	return list, err
}
