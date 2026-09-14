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
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"

	"ai-scrm/internal/model"
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

// selectProvider 按当前 pay_mode 选择渠道适配器
func selectProvider() (PaymentProvider, error) {
	switch GetPayMode() {
	case "sdk":
		g := loadGatewayProvider()
		if g.Endpoint == "" {
			return nil, fmt.Errorf("sdk 支付未配置网关（pay_gateway_url 缺失）")
		}
		return g, nil
	case "static_qr", "mock":
		return MockProvider{}, nil
	default:
		return MockProvider{}, nil
	}
}

// GetPayMode 读当前收款模式（系统配置热加载，默认 mock）
func GetPayMode() string {
	if runtimecfg.DefaultSystemConfigService == nil {
		return "mock"
	}
	mode := runtimecfg.DefaultSystemConfigService.GetString("pay_mode", "mock")
	if mode != "mock" && mode != "static_qr" && mode != "sdk" {
		return "mock" // 脏配置兜底
	}
	return mode
}

// GenerateOrderNo 订单号：秒级时间戳+6字节随机hex（UAT修复 2026-08-26）
// 原"UnixNano%10000"四位尾数在批量下单场景高频碰撞(23505)——改为 crypto/rand
// 8位hex后缀，碰撞概率≈1/2^32且与秒级字段解耦；总长仍≤32满足唯一索引
