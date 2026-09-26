// 微信支付 V3 适配器（P0-1a，2026-09-15，AUDIT_GAP_VERIFICATION §2 P0-1）。
//
// 此前 sdk 模式仅有 GatewayProvider（自约定 HMAC 通用 PSP 骨架），微信支付 V3 官方协议
// 未实现——目标客户要求官方商户号直连收款时无法上线。本文件补齐：
//
//	CreatePayment：V3 Native 下单（/v3/pay/transactions/native），SHA256withRSA 签名，
//	  返回 code_url 经 go-qrcode 转 data:image PNG（收银台 <img> 直接展示，用户即扫即付）。
//	Refund：V3 退款（/v3/refund/domestic/refunds），受理成功记 psp_ok（**受理不等于到账**）。
//	QueryRefundStatus：V3 按商户退款单号查单（/v3/refund/domestic/refunds/{out_refund_no}），
//	  把 psp_ok 收敛成 psp_success/psp_failed（FIX-1 第四步，2026-09-26）——微信退款结果是
//	  异步的，旧实现"受理即终态 + 无人回查"会让账面长期停在未经证实的"已出款"上。
//	DecryptWechatResource：回调 resource 字段（AES-256-GCM, APIv3Key）解密——供
//	  api.BillingWebhook 的 wechat 渠道分支取 out_trade_no/trade_state 后走既有幂等到账链。
//
// 资金安全红线不破：到账仍走 ConfirmOrderByChannel（MarkOrderPaid 条件转移 + 台账先行），
// 本文件只做"协议适配"，不触碰发放逻辑。BaseURL/HTTPClient 可注入便于 httptest 模拟微信端点。
package billing

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"ai-scrm/internal/model"

	qrcode "github.com/skip2/go-qrcode"
)

// wechatPayBase 微信支付 V3 官方端点
const wechatPayBase = "https://api.mch.weixin.qq.com"

// WechatPayProvider 微信支付 V3 配置与客户端。
// BaseURL 默认官方端点；HTTPClient/私钥可注入便于单测。
type WechatPayProvider struct {
	AppID      string // 公众号/小程序 appid（下单 body 用）
	MchID      string // 商户号
	SerialNo   string // 商户 API 证书序列号（Authorization 头 serial_no）
	PrivateKey *rsa.PrivateKey
	APIv3Key   string // 32 字节回调 resource 解密密钥
	NotifyURL  string // 支付成功异步通知地址（/api/v1/billing/webhook/wechat）
	BaseURL    string // 空=官方端点；测试注入 httptest 地址
	HTTPClient *http.Client
}

// Name 渠道标识（与订单 channel 列、回调 :channel 参数、渠道互验 L3 对齐）
func (w *WechatPayProvider) Name() string { return "wechat" }

// CreatePayment V3 Native 下单：返回二维码 data:image PNG。
// 签名规范：Authorization: WECHATPAY2-SHA256-RSA2048；签名串 = METHOD\nURL路径\n时间戳\n随机串\n请求体\n
func (w *WechatPayProvider) CreatePayment(order *model.BillingOrder) (string, error) {
	urlPath := "/v3/pay/transactions/native"
	body := map[string]interface{}{
		"appid":        w.AppID,
		"mchid":        w.MchID,
		"description":  fmt.Sprintf("AI-SCRM 订单 %s", order.OrderNo),
		"out_trade_no": order.OrderNo,
		"notify_url":   w.NotifyURL,
		"amount":       map[string]interface{}{"total": order.AmountCents, "currency": "CNY"},
	}
	data, err := w.doV3(urlPath, body)
	if err != nil {
		return "", err
	}
	var out struct {
		CodeURL string `json:"code_url"`
	}
	if err := json.Unmarshal(data, &out); err != nil || out.CodeURL == "" {
		return "", fmt.Errorf("微信下单响应缺少 code_url: %s", truncateResp(string(data)))
	}
	// code_url 是 weixin:// 链接，转 PNG data URI 供收银台 <img> 直接展示
	return qrToDataURL(out.CodeURL)
}

// Refund V3 退款：受理成功（200/202）即返回 nil（账面 refund_psp_status=psp_ok）。
//
// FIX-1（2026-09-26）：退款单号取 refundOutNo(order)——**稳定号**（已落库则用库里的，
// 未落库则 "RF"+订单号）。旧实现按秒拼时间戳，重试一次换一个号，微信侧视作两笔
// 独立退款请求 → 同一订单可出两次款（金额相同、单号不同，微信不去重）。
// 现在号恒定，微信按 out_refund_no 幂等：重复请求返回原单，不产生第二笔资金动作。
func (w *WechatPayProvider) Refund(order *model.BillingOrder, refundCents int) error {
	if w.PrivateKey == nil || w.MchID == "" {
		return ErrRefundNotWired
	}
	urlPath := "/v3/refund/domestic/refunds"
	body := map[string]interface{}{
		"out_trade_no":  order.OrderNo,
		"out_refund_no": refundOutNo(order),
		"amount": map[string]interface{}{
			"refund":   refundCents,
			"total":    order.AmountCents,
			"currency": "CNY",
		},
	}
	_, err := w.doV3(urlPath, body)
	return err
}

// QueryRefundStatus 实现 RefundResultQuerier（FIX-1 第四步，2026-09-26）：
// GET /v3/refund/domestic/refunds/{out_refund_no} 按**商户退款单号**查这笔退款的终态。
//
// 微信退款是异步的：受理接口回 200/202 只表示"受理成功"，钱可能在几小时后才真出去，
// 也可能最终失败（ABNORMAL/CLOSED）。旧实现把受理当终态记 psp_ok 后就再无人过问，
// 财务看到的"已出款"里混着"其实没出"——本函数就是把它收敛成 psp_success/psp_failed。
//
// 映射只认微信 status 枚举；未知值一律按 processing 处理（不动状态、下轮再查），
// 因为"判错成 failed"会把一笔可能成功的退款从人工队列里摘掉，比"多查一轮"坏得多。
func (w *WechatPayProvider) QueryRefundStatus(order *model.BillingOrder, outRefundNo string) (string, error) {
	if w.PrivateKey == nil || w.MchID == "" {
		return "", ErrRefundNotWired
	}
	if outRefundNo == "" {
		return "", errors.New("退款单号为空，无法查单")
	}
	data, err := w.doV3Get(context.Background(), "/v3/refund/domestic/refunds/"+url.PathEscape(outRefundNo))
	if err != nil {
		return "", err
	}
	var out struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("微信退款查单响应解析失败: %w", err)
	}
	switch out.Status {
	case "SUCCESS":
		return "success", nil
	case "ABNORMAL", "CLOSED":
		return "failed", nil
	case "PROCESSING", "":
		// 空串＝微信没给 status 字段（协议变化/异常响应），按未知处理，不动账面
		return "processing", nil
	default:
		return "processing", nil
	}
}

// buildAuthHeader 构造 V3 请求签名头（下单/退款共用）。
// urlPath 为含 query 的路径。
func (w *WechatPayProvider) buildAuthHeader(method, urlPath, body string) (string, error) {
	if w.PrivateKey == nil {
		return "", errors.New("微信支付商户私钥未加载（pay_wechat_private_key 缺失或格式非法）")
	}
	ts := time.Now().Unix()
	nonce, err := randomHexStr(16)
	if err != nil {
		return "", err
	}
	// V3 签名串规范：METHOD\nURL路径\n时间戳\n随机串\n请求体\n（结尾必须 \n）
	message := fmt.Sprintf("%s\n%s\n%d\n%s\n%s\n", method, urlPath, ts, nonce, body)
	digest := sha256.Sum256([]byte(message))
	sig, err := rsa.SignPKCS1v15(rand.Reader, w.PrivateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("V3 签名失败: %w", err)
	}
	return fmt.Sprintf(`WECHATPAY2-SHA256-RSA2048 mchid="%s",nonce_str="%s",signature="%s",timestamp="%d",serial_no="%s"`,
		w.MchID, nonce, base64.StdEncoding.EncodeToString(sig), ts, w.SerialNo), nil
}

// doV3 发起 V3 请求（POST，JSON body），返回响应体字节。非 2xx 返回错误（含微信错误码）。
func (w *WechatPayProvider) doV3(urlPath string, reqBody interface{}) ([]byte, error) {
	body, _ := json.Marshal(reqBody)
	return w.doV3Raw(http.MethodPost, urlPath, string(body))
}

// doV3Get 发起 V3 GET 请求（签名串的请求体段为**空串**，不是 "{}"——
// 微信对 GET 的规范是 METHOD\nURL\n时间戳\n随机串\n\n，写错这一段会恒 401 SIGN_ERROR）。
func (w *WechatPayProvider) doV3Get(ctx context.Context, urlPath string) ([]byte, error) {
	return w.doV3RawCtx(ctx, http.MethodGet, urlPath, "")
}

// doV3Raw 无 ctx 的 POST 入口（下单/退款沿用调用方默认超时）。
func (w *WechatPayProvider) doV3Raw(method, urlPath, body string) ([]byte, error) {
	return w.doV3RawCtx(context.Background(), method, urlPath, body)
}

// doV3RawCtx V3 请求统一出口：签名头 + 非 2xx 报错（错误里带响应体，排障靠它）。
func (w *WechatPayProvider) doV3RawCtx(ctx context.Context, method, urlPath, body string) ([]byte, error) {
	auth, err := w.buildAuthHeader(method, urlPath, body)
	if err != nil {
		return nil, err
	}
	base := w.BaseURL
	if base == "" {
		base = wechatPayBase
	}
	var reader io.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, base+urlPath, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", auth)
	req.Header.Set("User-Agent", "ai-scrm-billing")
	client := w.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("微信支付请求失败: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("微信支付响应异常 http=%d body=%s", resp.StatusCode, truncateResp(string(data)))
	}
	return data, nil
}

// randomHexStr 生成 n 字节随机数的 hex 串（V3 nonce_str）
func randomHexStr(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}

// truncateResp 截断响应文本（错误信息防刷屏）
func truncateResp(s string) string {
	const n = 300
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// qrToDataURL 把支付链接（weixin:// code_url / 支付宝 qr_code）转成 PNG data URI，
// 收银台前端 isImg 分支（Billing.tsx）命中即渲染 <img>，用户直接扫码。
func qrToDataURL(content string) (string, error) {
	png, err := qrcode.Encode(content, qrcode.Medium, 260)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}

// WechatNotify 微信 V3 交易回调解密明文中的业务字段。
// P1-3 扩展(2026-09-18，AUDIT_VERIFY_2026-09-18)：此前只回传 out_trade_no/trade_state，
// 金额与商户归属（mchid/appid）从未核对——解密成功虽证明持有 APIv3Key，但纵深上仍须
// 与订单面额分毫比对（对齐 alipay 复核批口径），封堵异常报文/配置错乱按订单面额全额发货。
type WechatNotify struct {
	OutTradeNo  string
	TradeState  string
	MchID       string
	AppID       string
	AmountTotal int64 // amount.total，单位分
}

// DecryptWechatResource 解密微信 V3 回调 resource（AES-256-GCM，APIv3Key）。
// 解密成功只证明"回调方持有 APIv3Key"，不证明"报文由微信支付签发"——后者见
// VerifyWechatCallback（平台证书验签，E1-2 批 2026-09-24）。
func DecryptWechatResource(apiv3Key string, ciphertext, nonce, associatedData string) (WechatNotify, error) {
	plain, err := decryptAPIv3GCM(apiv3Key, ciphertext, nonce, associatedData)
	if err != nil {
		return WechatNotify{}, fmt.Errorf("回调 resource 解密失败（APIv3Key 不匹配或报文被篡改）: %w", err)
	}
	var out struct {
		OutTradeNo string `json:"out_trade_no"`
		TradeState string `json:"trade_state"`
		MchID      string `json:"mchid"`
		AppID      string `json:"appid"`
		Amount     struct {
			Total int64 `json:"total"`
		} `json:"amount"`
	}
	if err := json.Unmarshal(plain, &out); err != nil {
		return WechatNotify{}, fmt.Errorf("回调明文 JSON 解析失败: %w", err)
	}
	return WechatNotify{
		OutTradeNo:  out.OutTradeNo,
		TradeState:  out.TradeState,
		MchID:       out.MchID,
		AppID:       out.AppID,
		AmountTotal: out.Amount.Total,
	}, nil
}

// ValidateWechatNotify 回调归属核对（P1-3，2026-09-18，与 ValidateAlipayNotifyParams 同口径）：
//   - amount.total 必与订单面额分毫相等且 >0（缺失/为 0 视为畸形报文直接拒）；
//   - mchid/appid 配置了才比对（本地 mock 端点可能不带这两字段，不强求）——
//     配置齐备却与实际商户不一致即疑似跨商户伪造，拒绝并回 403。
func ValidateWechatNotify(n WechatNotify, expectedAppID, expectedMchID string, orderAmountCents int64) error {
	if n.AmountTotal <= 0 {
		return errors.New("回调缺少 amount.total，无法核对到账金额")
	}
	if n.AmountTotal != orderAmountCents {
		return fmt.Errorf("通知金额(%d分)与订单金额(%d分)不一致", n.AmountTotal, orderAmountCents)
	}
	if expectedMchID != "" && n.MchID != "" && n.MchID != expectedMchID {
		return fmt.Errorf("通知 mchid(%s) 与配置商户号(%s)不一致", n.MchID, expectedMchID)
	}
	if expectedAppID != "" && n.AppID != "" && n.AppID != expectedAppID {
		return fmt.Errorf("通知 appid(%s) 与配置应用(%s)不一致", n.AppID, expectedAppID)
	}
	return nil
}

// ParseRSAPrivateKey 从 PEM 字节解析商户私钥（兼容 PKCS8/PKCS1——商户平台下载为 PKCS8，历史导出可能 PKCS1）。
func ParseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("PEM 解码失败（私钥内容非 PEM 格式）")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rk, ok := k.(*rsa.PrivateKey); ok {
			return rk, nil
		}
		return nil, errors.New("私钥非 RSA 类型")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	return nil, errors.New("私钥解析失败（须 PKCS8/PKCS1 RSA）")
}

// ParseRSAPublicKey 从 PEM 字节解析公钥（支付宝平台公钥等验签用）。
func ParseRSAPublicKey(pemBytes []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("PEM 解码失败（公钥内容非 PEM 格式）")
	}
	if pub, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		if rk, ok := pub.(*rsa.PublicKey); ok {
			return rk, nil
		}
		return nil, errors.New("公钥非 RSA 类型")
	}
	if k, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		return k, nil
	}
	return nil, errors.New("公钥解析失败（须 PKIX/PKCS1 RSA）")
}
