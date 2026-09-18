// 渠道回调公开入口（W3-5，2026-09-12）——注册在 v1.Use(JWTAuth) 之前，靠签名验证保证安全。
// 路径含 :id 精确定位通道（多租户多通道无歧义）。GET=URL 验证回显，POST=消息/事件回调。
package api

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"ai-scrm/internal/channel"
	"ai-scrm/internal/db"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
)

// channelTenantUsable P1-7 修复(2026-09-15)：回调路径的租户状态闸。
// 旧实现回调只按 :id 取通道、skip 了租户解析——suspended/cancelled 租户的通道照常
// "入站→AI→出站"，烧的是已封禁租户的用量（webhook 投递域同款问题见注释）。
// 30s 进程缓存扛微信重推风暴；状态未知（查库失败）放行——可用性优先，
// 资金面不受此路径影响（AI 计量仍走三桶 fail-closed）。
var channelTenantGate = struct {
	sync.Mutex
	cache map[uint]struct {
		ok bool
		at time.Time
	}
}{cache: map[uint]struct {
	ok bool
	at time.Time
}{}}

// channelTenantUsable 入站闸：租户停用/注销/审核中一律不再处理微信侧消息（P1-7 复核批）。
// 带 30s 进程内缓存——回调高频（每条消息都查），全表查会放大 DB 压力；封禁生效延迟 ≤30s 可接受。
func channelTenantUsable(tenantID uint) bool {
	channelTenantGate.Lock()
	defer channelTenantGate.Unlock()
	if e, has := channelTenantGate.cache[tenantID]; has && time.Since(e.at) < 30*time.Second {
		return e.ok
	}
	ok := true
	var kt model.Tenant
	if err := db.DB.Select("id, status, cancel_at").First(&kt, tenantID).Error; err == nil {
		if kt.Status == "suspended" || kt.Status == "cancelled" || kt.Status == "expired" || kt.Status == "review" {
			ok = false
		}
		if kt.CancelAt != nil && time.Now().After(*kt.CancelAt) {
			ok = false // 注销生效时刻已过
		}
	}
	// 查不到/查库失败：放行（可用性优先；AI 计量侧三桶 fail-closed 兜底）
	channelTenantGate.cache[tenantID] = struct {
		ok bool
		at time.Time
	}{ok, time.Now()}
	return ok
}

// loadChannelForCallback 按路径 :id 取通道 + 解密凭据（不校验租户 JWT，靠后续签名验证）。
func loadChannelForCallback(c *gin.Context) (*model.Channel, *channel.Credential, bool) {
	n, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	ch, err := channel.GetByID(uint(n))
	if err != nil {
		c.String(http.StatusNotFound, "channel not found")
		return nil, nil, false
	}
	cred, err := channel.DecryptCredential(ch)
	if err != nil {
		log.Printf("[通道回调] channel=%d 凭据解密失败(密钥轮换?): %v", ch.ID, err)
		c.String(http.StatusOK, "success") // 不暴露内部错误给渠道
		return nil, nil, false
	}
	// P1-7 修复(2026-09-15)：租户状态闸——回调 skip 了 TenantResolver（匿名端点），
	// 旧行为下 suspended/过期租户的通道照常 入站→AI→出站，烧已封禁账号的用量。
	// 静默回 success 止重推（封禁租户的消息不配进对话链路）。
	if !channelTenantUsable(ch.TenantID) {
		log.Printf("[通道回调] channel=%d 归属租户%d已停用，消息丢弃", ch.ID, ch.TenantID)
		c.String(http.StatusOK, "success")
		return nil, nil, false
	}
	return ch, cred, true
}

// ChannelCallbackVerify GET /channel/callback/:id —— 接入配置阶段的 URL 校验（echostr 原样/解密回显）。
func ChannelCallbackVerify(c *gin.Context) {
	ch, cred, ok := loadChannelForCallback(c)
	if !ok {
		return
	}
	echo := c.Query("echostr")
	if echo == "" {
		c.String(http.StatusBadRequest, "missing echostr")
		return
	}
	var msgSig string
	if ch.Type == model.ChannelTypeWechatMP {
		msgSig = c.Query("signature")
	} else {
		msgSig = c.Query("msg_signature")
	}
	adapter, ok := channel.AdapterFor(ch)
	if !ok {
		c.String(http.StatusInternalServerError, "no adapter")
		return
	}
	plain, err := adapter.VerifyURLEcho(cred, msgSig, c.Query("timestamp"), c.Query("nonce"), echo)
	if err != nil {
		log.Printf("[通道回调] channel=%d URL验证失败: %v", ch.ID, err)
		c.String(http.StatusForbidden, "verify failed")
		return
	}
	c.String(http.StatusOK, plain)
}

// ChannelCallbackReceive POST /channel/callback/:id —— 消息/事件回调：验签解密→归一化入站→处理链。
// 无论业务结果如何都回 "success"（企微/微信约定：非 success 会重推），错误只落日志，避免重推风暴。
func ChannelCallbackReceive(c *gin.Context) {
	ch, cred, ok := loadChannelForCallback(c)
	if !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		c.String(http.StatusOK, "success")
		return
	}
	adapter, ok := channel.AdapterFor(ch)
	if !ok {
		log.Printf("[通道回调] channel=%d 无适配器 type=%s", ch.ID, ch.Type)
		c.String(http.StatusOK, "success")
		return
	}
	// 安全模式签名参数：企微用 msg_signature；公众号安全模式同样用 msg_signature，明文模式回落 signature。
	msgSig := c.Query("msg_signature")
	if msgSig == "" {
		msgSig = c.Query("signature")
	}
	in, err := adapter.DecryptInbound(cred, c.Query("timestamp"), c.Query("nonce"), msgSig, body)
	if err != nil {
		log.Printf("[通道回调] channel=%d 验签/解密失败: %v", ch.ID, err)
		c.String(http.StatusForbidden, "success") // 回 success 止重推，但 403 标记
		return
	}
	// P1-7 修复(2026-09-15)：记录原始信封摘要——MsgID 为空的老协议报文以此为去重锚，
	// 杜绝"抓一份有效报文无限重放 → 重复入站 + 重复 AI 出站"。
	if in != nil && in.MsgID == "" {
		sum := sha256.Sum256(body)
		in.EnvelopeID = "env:" + hex.EncodeToString(sum[:])
	}
	if in != nil {
		// E3(2026-09-19)：回调请求 trace 随消息进 headless worker（合并队列/AI 出站共用同一条链）
		in.TraceID = middleware.GetTraceID(c)
	}
	if err := channel.ProcessInbound(ch, in); err != nil {
		log.Printf("[通道回调] channel=%d 入站处理失败: %v", ch.ID, err)
	}
	c.String(http.StatusOK, "success")
}
