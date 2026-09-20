// Package webhook — 出站事件回调（D6，2026-09-12）
// 业务关键事件扇出到租户自助配置的回调 URL：HMAC-SHA256 签名头 + 指数退避重试 + 连续失败熔断停用。
// 只依赖 db/model（禁 import internal/service，承接 D2 分包红线与避免环）。
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/pkg/crypto"
)

const (
	maxAttempts     = 5                // 单次投递最多重试次数，超过转 dead 死信
	circuitFailN    = 20               // 连续失败达此数 → 熔断停用该订阅
	deliveryTimeout = 10 * time.Second // 单次 HTTP 超时
	signatureHeader = "X-SCRM-Signature"
	timestampHeader = "X-SCRM-Timestamp"
	eventHeader     = "X-SCRM-Event"
)

var whHTTP = &http.Client{Timeout: deliveryTimeout}

// Emit 向订阅了 event 的所有启用中 webhook 投递一条事件（异步入队，不阻塞业务）。
// 返回入队条数；任何 DB 错误仅记日志（事件推送是旁路，绝不影响主流程）。
func Emit(tenantID uint, event string, payload map[string]interface{}) int {
	if tenantID == 0 || event == "" {
		return 0
	}
	body, _ := json.Marshal(payload)
	var subs []model.TenantWebhook
	// active + 未熔断 + events 命中（CSV 精确匹配）
	if err := db.DB.Where("tenant_id = ? AND active = ? AND disabled_at IS NULL", tenantID, true).Find(&subs).Error; err != nil {
		log.Printf("[webhook] 查询订阅失败 tenant=%d: %v", tenantID, err)
		return 0
	}
	now := time.Now()
	n := 0
	for i := range subs {
		s := &subs[i]
		if !subscribes(s.Events, event) {
			continue
		}
		d := model.WebhookDelivery{
			TenantID:    tenantID,
			WebhookID:   s.ID,
			Event:       event,
			Payload:     string(body),
			Status:      model.WebhookDeliveryPending,
			NextRetryAt: &now,
		}
		if err := db.DB.Create(&d).Error; err != nil {
			log.Printf("[webhook] 入队失败 webhook=%d: %v", s.ID, err)
			continue
		}
		n++
	}
	return n
}

// subscribes events CSV 是否命中 event（去空白精确匹配）
func subscribes(csv, event string) bool {
	for _, e := range strings.Split(csv, ",") {
		if strings.TrimSpace(e) == event {
			return true
		}
	}
	return false
}

// staleSendingTTL sending 预占行复活阈值：持有者崩溃超此时长未回写终态→回炉 pending 重投。
const staleSendingTTL = 5 * time.Minute

// claimDueDeliveries P2-3 修复(2026-09-20 批三)：与出站队列同型——裸 SELECT 直查直发在
// 多实例/换主/崩溃窗口会双投（下游收到重复事件）。改同事务：复活超时 sending →
// FOR UPDATE SKIP LOCKED 取到期 pending → 置 sending 预占。
func claimDueDeliveries(now time.Time) []model.WebhookDelivery {
	var claimed []model.WebhookDelivery
	err := db.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.WebhookDelivery{}).
			Where("status = ? AND updated_at < ?", model.WebhookDeliverySending, now.Add(-staleSendingTTL)).
			Updates(map[string]interface{}{"status": model.WebhookDeliveryPending, "next_retry_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Raw(`SELECT * FROM webhook_deliveries
			WHERE status = ? AND (next_retry_at IS NULL OR next_retry_at <= ?)
			ORDER BY id ASC LIMIT 50 FOR UPDATE SKIP LOCKED`,
			model.WebhookDeliveryPending, now).Scan(&claimed).Error; err != nil {
			return err
		}
		if len(claimed) == 0 {
			return nil
		}
		ids := make([]uint, 0, len(claimed))
		for i := range claimed {
			ids = append(ids, claimed[i].ID)
		}
		return tx.Model(&model.WebhookDelivery{}).Where("id IN ?", ids).
			Update("status", model.WebhookDeliverySending).Error
	})
	if err != nil {
		log.Printf("[webhook] claim 失败（本轮跳过）: %v", err)
		return nil
	}
	return claimed
}

// ProcessDue worker：取到期的 pending 投递并发送，退避重试/死信/熔断。返回 (delivered, retried, dead)。
func ProcessDue() (delivered, retried, dead int) {
	now := time.Now()
	due := claimDueDeliveries(now)
	for i := range due {
		d := &due[i]
		var wh model.TenantWebhook
		if err := db.DB.First(&wh, d.WebhookID).Error; err != nil {
			markDead(d, "订阅不存在")
			dead++
			continue
		}
		if !wh.Active || wh.DisabledAt != nil {
			markDead(d, "订阅已停用")
			dead++
			continue
		}
		status, err := deliver(&wh, d.Event, d.Payload)
		if err == nil && status >= 200 && status < 300 {
			db.DB.Model(d).Updates(map[string]interface{}{
				"status": model.WebhookDeliveryDelivered, "delivered_at": now, "last_error": "",
			})
			// 成功清零该订阅连续失败计数
			db.DB.Model(&model.TenantWebhook{}).Where("id = ?", wh.ID).Update("fail_count", 0)
			delivered++
			continue
		}
		// 失败：累加订阅失败计数 + 投递退避
		msg := fmt.Sprintf("http=%d err=%v", status, err)
		d.Attempts++
		if d.Attempts > maxAttempts {
			markDead(d, "超过最大重试: "+msg)
			dead++
		} else {
			nt := now.Add(nextBackoff(d.Attempts))
			// P2-3：行取单时已置 sending，退避回炉必须显式写回 pending
			db.DB.Model(d).Updates(map[string]interface{}{
				"status": model.WebhookDeliveryPending, "attempts": d.Attempts, "next_retry_at": nt, "last_error": truncate(msg, 280),
			})
			retried++
		}
		// 订阅熔断
		if err := db.DB.Model(&model.TenantWebhook{}).Where("id = ?", wh.ID).
			UpdateColumn("fail_count", gorm.Expr("fail_count + 1")).Error; err == nil {
			var cnt int
			db.DB.Model(&model.TenantWebhook{}).Where("id = ?", wh.ID).Select("fail_count").Scan(&cnt)
			if cnt >= circuitFailN {
				dis := time.Now()
				db.DB.Model(&model.TenantWebhook{}).Where("id = ?", wh.ID).Updates(map[string]interface{}{
					"active": false, "disabled_at": dis,
				})
				log.Printf("[webhook] 订阅 id=%d 连续失败 %d 次已熔断停用", wh.ID, cnt)
			}
		}
	}
	return
}

// markDead 将投递记录标记为死信并写入失败原因。
func markDead(d *model.WebhookDelivery, reason string) {
	db.DB.Model(d).Updates(map[string]interface{}{
		"status": model.WebhookDeliveryDead, "last_error": truncate(reason, 280),
	})
}

// deliver 发送单次回调：签名头 + body，返回 HTTP 状态码与网络错误。
func deliver(wh *model.TenantWebhook, event, payload string) (int, error) {
	// P2-SSRF 修复(2026-09-15)：投递前复核 URL（历史行可能在白名单规则前写入）
	if verr := ValidateCallbackURL(wh.URL); verr != nil {
		return 0, verr
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req, err := http.NewRequest(http.MethodPost, wh.URL, strings.NewReader(payload))
	if err != nil {
		return 0, err
	}
	// secret 落库为密文（gcm1:），非前缀明文透传（兼容测试/历史），与 crypto 约定一致
	secret := wh.Secret
	if plain, derr := crypto.Decrypt(wh.Secret); derr == nil {
		secret = plain
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(timestampHeader, ts)
	req.Header.Set(eventHeader, event)
	req.Header.Set(signatureHeader, "sha256="+Sign(secret, ts, payload))
	resp, err := whHTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// Sign HMAC-SHA256(secret, timestamp + "." + body) → hex（不带前缀）。
func Sign(secret, timestamp, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "." + body))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature 供接收方（或单测）校验签名；容忍 constant-time 比较。
func VerifySignature(secret, timestamp, body, got string) error {
	got = strings.TrimPrefix(got, "sha256=")
	want := Sign(secret, timestamp, body)
	if !hmac.Equal([]byte(want), []byte(got)) {
		return errors.New("签名校验失败")
	}
	return nil
}

// SendTestPing 向指定 URL 发一条测试事件（Admin"测试 ping"按钮）；返回状态码与耗时。
func SendTestPing(url, secret string) (int, error) {
	payload := `{"event":"webhook.test","message":"这是一条来自 AI-SCRM 的连通性测试"}`
	wh := &model.TenantWebhook{URL: url, Secret: secret}
	return deliver(wh, "webhook.test", payload)
}

// nextBackoff 按重试次数计算指数退避间隔。
func nextBackoff(attempts int) time.Duration {
	// 指数退避：5s,10s,20s,40s,80s（封顶）
	d := time.Duration(1<<uint(attempts)) * 5 * time.Second
	if d > 80*time.Second {
		d = 80 * time.Second
	}
	return d
}

// truncate 按 rune 安全截断字符串到 n 个字符。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
