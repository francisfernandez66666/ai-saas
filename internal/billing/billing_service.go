// 收银台商业化服务：pay_mode 三态分发、订单幂等发放/到账、超时扫描关闭。
package billing

import (
	"ai-scrm/internal/runtimecfg"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-scrm/internal/model"
	"ai-scrm/internal/redisclient"
)

// ErrRefundNoRemaining 退款拒绝哨兵：订单权益已全部消耗/过期，无剩余可退
var ErrRefundNoRemaining = errors.New("该订单权益已全部消耗，无剩余可退")

// ErrRefundNotWired R8 哨兵(2026-09-11)：账面退款已完成但出款通道未配置/未接入——
// 必须显式区分"钱退了"与"权益回收了"，杜绝把账面 refunded 误当银库到账（此前 PaymentProvider
// 连 Refund 方法都没有，退款天然是账面假实现）。
var ErrRefundNotWired = errors.New("退款出款通道未配置，仅完成账面回收")

// ============================================================
// 收银台服务（商业化第一批 M1，2026-08-23）
//
// pay_mode 三态（源自翻译助手生产验证设计）：
//   mock        测试模拟到账（默认，跑通全链路；生产靠接口403双保险）
//   static_qr   静态收款码 + 租户「我已付费」→ critical 审计 + 催告超管 → 人工确认发放
//   sdk         商户号到位后切 wechat/alipay 适配器（本期只留适配器接口位）
//
// 幂等铁律：MarkOrderPaid 用条件 UPDATE（pending→paid），
// RowsAffected=0 视为已处理过，绝不二次发放权益
// ============================================================

// PaymentProvider 支付渠道适配器接口（M1 预留接口位）
// 商户号到位后实现 WechatProvider/AlipayProvider 即可切换，收银台代码零改动
type PaymentProvider interface {
	Name() string                                                      // wechat/alipay
	CreatePayment(order *model.BillingOrder) (qrURL string, err error) // 下单返回支付凭证
	// Refund R8 补齐(2026-09-11)：真实出款接口。此前接口无 Refund，订单退款只回收权益
	// 不触资金侧，账面 refunded 与"客户收到钱"完全脱钩——接真实 PSP 时此方法是必答题。
	Refund(order *model.BillingOrder, refundCents int) error
}

// MockProvider 模拟渠道：无真实收款，配合 mock-pay 接口跑通全链路
type MockProvider struct{}

// Name 渠道标识
func (MockProvider) Name() string { return "mock" }

// CreatePayment 生成模拟支付凭证（无真实收款，配合 mock-pay 跑通全链路）
func (MockProvider) CreatePayment(order *model.BillingOrder) (string, error) {
	return fmt.Sprintf("mock://pay/%s?amount=%d", order.OrderNo, order.AmountCents), nil
}

// Refund 模拟渠道退款：无资金侧，账面语义直接成功
func (MockProvider) Refund(order *model.BillingOrder, refundCents int) error { return nil }

// GatewayProvider 真实支付网关适配器（pay_mode=sdk 落点，P2 商业化）
// 对接任意支持「下单接口 + 异步回调」的 PSP（微信/支付宝/聚合码台），
// 通过 HMAC-SHA256 对请求体签名鉴权。商户号/密钥经系统配置（或环境变量）注入，
// 未配置则明确报错，绝不静默走 mock（防测试通道漏进生产）。
//
// 约定（与 webhook 校验对称）：
//
//	下单签名 sign = HMAC_SHA256(key, canonical(params))
//	回调验签 sign = HMAC_SHA256(key, order_no + "|" + trade_status)
//
// 具体字段随 PSP 调整——此处给出可投产的通用骨架，接真实商户号仅需填配置。
type GatewayProvider struct {
	Endpoint  string // 下单接口基址，如 https://api.mch.example.com/unifiedorder
	AppID     string // 商户/应用 ID
	Key       string // 签名密钥
	NotifyURL string // 异步回调地址（PSP 到账后回调 /api/v1/billing/webhook/gateway）
}

// Name 渠道标识
func (g GatewayProvider) Name() string { return "gateway" }

// CreatePayment 向 PSP 下单并返回支付跳转/二维码 URL（真实收款）
func (g GatewayProvider) CreatePayment(order *model.BillingOrder) (string, error) {
	if g.Endpoint == "" || g.AppID == "" || g.Key == "" {
		return "", fmt.Errorf("支付网关未配置（pay_gateway_url/app_id/key 缺失）")
	}
	params := map[string]string{
		"out_trade_no": order.OrderNo,
		"total_fee":    strconv.Itoa(order.AmountCents),
		"app_id":       g.AppID,
		"timestamp":    strconv.FormatInt(time.Now().Unix(), 10),
		"notify_url":   g.NotifyURL,
		"subject":      fmt.Sprintf("AI-SCRM 订单 %s", order.OrderNo),
	}
	params["sign"] = signParams(params, g.Key)
	body, _ := json.Marshal(params)
	req, err := http.NewRequest(http.MethodPost, g.Endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("支付网关请求失败: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		PayURL  string `json:"pay_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("支付网关响应解析失败: %w", err)
	}
	if out.Code != 0 {
		return "", fmt.Errorf("支付网关下单失败: %s", out.Message)
	}
	return out.PayURL, nil
}

// Refund R8(2026-09-11)：向 PSP 发起真实出款（契约与下单对称：HMAC 签名 + code=0 即成功）。
// 端点约定 {pay_gateway_url}/refund（可用 pay_gateway_refund_url 覆盖）。
// 未配置网关 → ErrRefundNotWired（调用方须把订单标记 psp_pending，账面与资金显式分离）。
func (g GatewayProvider) Refund(order *model.BillingOrder, refundCents int) error {
	if g.Endpoint == "" || g.AppID == "" || g.Key == "" {
		return ErrRefundNotWired
	}
	endpoint := g.Endpoint + "/refund"
	if v := getPlatformConf("pay_gateway_refund_url", "PAY_GATEWAY_REFUND_URL"); v != "" {
		endpoint = v
	}
	params := map[string]string{
		"out_trade_no":  order.OrderNo,
		"refund_fee":    strconv.Itoa(refundCents),
		"app_id":        g.AppID,
		"out_refund_no": fmt.Sprintf("RF%s%s", time.Now().Format("20060102150405"), order.OrderNo),
		"timestamp":     strconv.FormatInt(time.Now().Unix(), 10),
	}
	params["sign"] = signParams(params, g.Key)
	body, _ := json.Marshal(params)
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("退款出款请求失败: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("退款响应解析失败: %w", err)
	}
	if out.Code != 0 {
		return fmt.Errorf("支付网关退款失败: %s", out.Message)
	}
	return nil
}

// getPlatformConf 平台配置读取（系统层，env 兜底；nil 服务安全）
func getPlatformConf(sysKey, envKey string) string {
	if runtimecfg.DefaultSystemConfigService != nil {
		if v := runtimecfg.DefaultSystemConfigService.GetString(sysKey, ""); v != "" {
			return v
		}
	}
	return os.Getenv(envKey)
}

// signParams 按 key 字典序拼接 sign=HMAC_SHA256(key, k1=v1&k2=v2...)
func signParams(params map[string]string, key string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	for i, k := range keys {
		if k == "sign" {
			continue
		}
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(params[k])
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(b.Bytes())
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyGatewaySign 回调验签（与 CreatePayment 签名对称）：sign = HMAC_SHA256(key, order_no|status)
func VerifyGatewaySign(key, orderNo, status, sign string) bool {
	if key == "" || sign == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(orderNo + "|" + status))
	return hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(sign))
}

// ============================================================
// C6 修复(2026-09-14)：webhook 防重放
// 旧签名仅 HMAC(order_no|trade_status)——同一报文可无限重放。
// 今日靠 MarkOrderPaid 条件转移兜底，但 paid 副作用（webhook 扇出/奖励事件）
// 一旦增强即成资金口。新口径：HMAC(order_no|trade_status|timestamp|nonce)，
// 时间窗 ±5min + nonce 去重（Redis 可用走 SETNX，否则进程内存表）。
// ============================================================

// VerifyGatewaySignV2 新版回调验签（timestamp 为 unix 秒字符串）
// P1-2 修复(2026-09-20 审计批)：签名串扩为 `order_no|status|timestamp|nonce|amount_cents`
// ——旧四段串不含金额，共享密钥方（聚合网关）配错或被劫持可用 ¥0.01 实付按订单面额
// 全额发货。amountCents 传空 = 调用方显式声明存量兼容模式（gateway_webhook_amount_required
// =false 时才允许），否则 handler 直接拒。与微信 V3/支付宝分支的金额归属核对同口径。
func VerifyGatewaySignV2(key, orderNo, status, timestamp, nonce, amountCents, sign string) bool {
	if key == "" || sign == "" || timestamp == "" || nonce == "" {
		return false
	}
	base := orderNo + "|" + status + "|" + timestamp + "|" + nonce
	if amountCents != "" {
		base += "|" + amountCents
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(base))
	return hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(sign))
}

// WebhookTimestampFresh 时间窗校验（±300s，防截获后无限期重放）
func WebhookTimestampFresh(timestamp string) bool {
	secs, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	diff := time.Now().Unix() - secs
	if diff < 0 {
		diff = -diff
	}
	return diff <= 300
}

// 进程内 nonce 去重表（Redis 不可用时兜底；10min 窗口，量级极小直接全表清扫）
var (
	webhookNonceMu sync.Mutex
	webhookNonces  = map[string]time.Time{}
)

// WebhookNonceSeen nonce 是否已被消费（true=重放）。Redis 优先跨实例去重。
// P1-3 修复(2026-09-15)：原实现用 TryLock（把 Redis 故障 err 抹成 nil）——
// TryLockE 文档明确要求"调用方禁止把故障当没抢到"，故障期全部回调被误判重放(409)，
// wechat/alipay/gateway 三条到账线停摆：资金可用性不应把去重做成 Redis 单点。
// 改为 TryLockE 区分三态：拿到锁=首次；锁被占=重放；Redis 故障=降级内存轨（单实例语义，
// 多实例窗口内可能放过一次跨实例重放，但重放第二发仍被"条件 UPDATE 流转权"幂等兜死，
// 不会二次发货——fail-open 去重 + fail-closed 发货，方向正确）。
func WebhookNonceSeen(nonce string) bool {
	if nonce == "" {
		return true
	}
	if redisclient.IsEnabled() {
		// TryLock(SETNX+TTL)：抢不到即已见过；故意不 Unlock，留到 TTL 自然过期
		h, err := redisclient.TryLockE("billing:webhook:nonce:"+nonce, 10*time.Minute)
		if err != nil {
			log.Printf("[Billing][WARN] nonce 去重 Redis 故障，降级内存轨（到账不停摆，发货仍幂等）: %v", err)
		} else if h != nil {
			return false
		} else {
			return true
		}
	}
	webhookNonceMu.Lock()
	defer webhookNonceMu.Unlock()
	now := time.Now()
	for k, exp := range webhookNonces {
		if now.After(exp) {
			delete(webhookNonces, k)
		}
	}
	if _, dup := webhookNonces[nonce]; dup {
		return true
	}
	webhookNonces[nonce] = now.Add(10 * time.Minute)
	return false
}

// WebhookNonceRelease 释放已消费的 nonce（P1-7，2026-09-18）。
// 背景：原实现 nonce 在落账前消费且不可逆——ConfirmOrderByChannel 遇瞬时故障（DB 抖动等）
// 后 PSP 同 nonce 重推一律 409，钱到不了账形成死局。落账失败路径调用本函数把去重名额还回去，
// 让重试可再进入；发货安全不受影响（MarkOrderPaid 条件 UPDATE 幂等仍是 fail-closed 兜底）。
// 注意：仅"基础设施失败"路径释放；验签/归属核对拒绝（403）不释放，避免给探测者反复让路。
func WebhookNonceRelease(nonce string) {
	if nonce == "" {
		return
	}
	if redisclient.IsEnabled() {
		redisclient.Del("billing:webhook:nonce:" + nonce)
	}
	webhookNonceMu.Lock()
	delete(webhookNonces, nonce)
	webhookNonceMu.Unlock()
}

// loadGatewayProvider 从系统配置/环境变量装配网关（热加载，缺省环境变量兜底）
func loadGatewayProvider() GatewayProvider {
	get := func(sysKey, envKey string) string {
		if runtimecfg.DefaultSystemConfigService != nil {
			if v := runtimecfg.DefaultSystemConfigService.GetString(sysKey, ""); v != "" {
				return v
			}
		}
		return os.Getenv(envKey)
	}
	return GatewayProvider{
		Endpoint:  get("pay_gateway_url", "PAY_GATEWAY_URL"),
		AppID:     get("pay_gateway_app_id", "PAY_GATEWAY_APP_ID"),
		Key:       get("pay_gateway_key", "PAY_GATEWAY_KEY"),
		NotifyURL: get("pay_gateway_notify_url", "PAY_GATEWAY_NOTIFY_URL"),
	}
}

// selectProvider 按当前 pay_mode 选择渠道适配器。
// sdk 模式按平台配置 pay_provider 分发：wechat=微信支付V3 / alipay=支付宝 / gateway=通用HMAC网关（默认，兼容现状）。
func selectProvider() (PaymentProvider, error) {
	switch GetPayMode() {
	case "sdk":
		return loadSDKProvider()
	case "static_qr", "mock":
		return MockProvider{}, nil
	default:
		return MockProvider{}, nil
	}
}

// loadSDKProvider 装配 sdk 模式渠道适配器（P0-1，2026-09-15）。
// pay_provider 未配置时默认 gateway——存量部署（已配 pay_gateway_* 的租户）零感知升级。
func loadSDKProvider() (PaymentProvider, error) {
	prov := strings.ToLower(strings.TrimSpace(getPlatformConf("pay_provider", "PAY_PROVIDER")))
	if prov == "" {
		prov = "gateway"
	}
	switch prov {
	case "wechat":
		w, err := LoadWechatProviderFromConf()
		if err != nil {
			return nil, err
		}
		return w, nil
	case "alipay":
		a, err := LoadAlipayProviderFromConf()
		if err != nil {
			return nil, err
		}
		return a, nil
	case "gateway":
		g := loadGatewayProvider()
		if g.Endpoint == "" {
			return nil, fmt.Errorf("sdk 支付未配置网关（pay_gateway_url 缺失）")
		}
		return g, nil
	default:
		return nil, fmt.Errorf("未知支付渠道 pay_provider=%s（支持 wechat/alipay/gateway）", prov)
	}
}

// providerForChannel 按订单渠道装配退款出款适配器。
// R8 隐患修复(2026-09-15)：MarkOrderRefunded 原实现固定 loadGatewayProvider 出款——
// wechat/alipay 渠道订单退款会被打到通用网关端点（协议不对，出款必败误标 psp_pending）。
// 现按订单 channel 分发；gateway/未知渠道回落通用网关。
func providerForChannel(channel string) (PaymentProvider, error) {
	switch channel {
	case "wechat":
		return LoadWechatProviderFromConf()
	case "alipay":
		return LoadAlipayProviderFromConf()
	default:
		g := loadGatewayProvider()
		if g.Endpoint == "" {
			return nil, ErrRefundNotWired
		}
		return g, nil
	}
}

// LoadWechatProviderFromConf 从系统配置/env 装配微信支付 V3 客户端。
// 私钥为 PEM 文本（PKCS8/PKCS1），经系统配置 pay_wechat_private_key 或环境变量 PAY_WECHAT_PRIVATE_KEY 注入。
func LoadWechatProviderFromConf() (*WechatPayProvider, error) {
	mchID := getPlatformConf("pay_wechat_mch_id", "PAY_WECHAT_MCH_ID")
	keyPEM := getPlatformConf("pay_wechat_private_key", "PAY_WECHAT_PRIVATE_KEY")
	if mchID == "" || keyPEM == "" {
		return nil, fmt.Errorf("微信支付未配置（pay_wechat_mch_id / pay_wechat_private_key 缺失）")
	}
	priv, err := ParseRSAPrivateKey([]byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("微信支付商户私钥解析失败: %w", err)
	}
	notify := getPlatformConf("pay_wechat_notify_url", "PAY_WECHAT_NOTIFY_URL")
	if notify == "" {
		return nil, fmt.Errorf("微信支付缺少回调地址（pay_wechat_notify_url）——Native 下单必填，须外网可达")
	}
	return &WechatPayProvider{
		AppID:      getPlatformConf("pay_wechat_app_id", "PAY_WECHAT_APP_ID"),
		MchID:      mchID,
		SerialNo:   getPlatformConf("pay_wechat_serial_no", "PAY_WECHAT_SERIAL_NO"),
		PrivateKey: priv,
		APIv3Key:   getPlatformConf("pay_wechat_apiv3_key", "PAY_WECHAT_APIV3_KEY"),
		NotifyURL:  notify,
	}, nil
}

// LoadAlipayProviderFromConf 从系统配置/env 装配支付宝客户端。
// 平台公钥缺失不阻塞下单/退款（仅验签用，回调分支会显式报错），返回 nil 公钥。
func LoadAlipayProviderFromConf() (*AlipayProvider, error) {
	appID := getPlatformConf("pay_alipay_app_id", "PAY_ALIPAY_APP_ID")
	keyPEM := getPlatformConf("pay_alipay_private_key", "PAY_ALIPAY_PRIVATE_KEY")
	if appID == "" || keyPEM == "" {
		return nil, fmt.Errorf("支付宝未配置（pay_alipay_app_id / pay_alipay_private_key 缺失）")
	}
	priv, err := ParseRSAPrivateKey([]byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("支付宝应用私钥解析失败: %w", err)
	}
	a := &AlipayProvider{
		AppID:      appID,
		PrivateKey: priv,
		NotifyURL:  getPlatformConf("pay_alipay_notify_url", "PAY_ALIPAY_NOTIFY_URL"),
	}
	if pubPEM := getPlatformConf("pay_alipay_public_key", "PAY_ALIPAY_PUBLIC_KEY"); pubPEM != "" {
		if pub, perr := ParseRSAPublicKey([]byte(pubPEM)); perr == nil {
			a.AlipayPublicKey = pub
		}
	}
	return a, nil
}

// GetPayMode 读当前收款模式（系统配置热加载，默认 mock）
func GetPayMode() string {
	if runtimecfg.DefaultSystemConfigService == nil {
		// 2026-09-22 复核 P0-1：服务未就绪时不得回退到最宽松的 mock（=可 0 元白嫖），
		// 回退到安全侧 static_qr（拿不到收款码就收不到钱，也不会凭空发权益）。
		return "static_qr"
	}
	mode := runtimecfg.DefaultSystemConfigService.GetString("pay_mode", "static_qr")
	if mode != "mock" && mode != "static_qr" && mode != "sdk" {
		// 脏配置兜底同样站在安全侧：越权的默认值等于把后门焊死在出厂位。
		return "static_qr"
	}
	return mode
}

// GenerateOrderNo 订单号：秒级时间戳+6字节随机hex（UAT修复 2026-08-26）
// 原"UnixNano%10000"四位尾数在批量下单场景高频碰撞(23505)——改为 crypto/rand
// 8位hex后缀，碰撞概率≈1/2^32且与秒级字段解耦；总长仍≤32满足唯一索引
