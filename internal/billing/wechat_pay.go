// 微信支付 V3 适配器（P0-1a，2026-09-15，AUDIT_GAP_VERIFICATION §2 P0-1）。
//
// 此前 sdk 模式仅有 GatewayProvider（自约定 HMAC 通用 PSP 骨架），微信支付 V3 官方协议
// 未实现——目标客户要求官方商户号直连收款时无法上线。本文件补齐：
//
//	CreatePayment：V3 Native 下单（/v3/pay/transactions/native），SHA256withRSA 签名，
//	  返回 code_url 经 go-qrcode 转 data:image PNG（收银台 <img> 直接展示，用户即扫即付）。
//	Refund：V3 退款（/v3/refund/domestic/refunds），受理成功即 psp_ok（与 GatewayProvider
//	  语义一致；退款结果异步通知留平台侧人工核对）。
//	DecryptWechatResource：回调 resource 字段（AES-256-GCM, APIv3Key）解密——供
//	  api.BillingWebhook 的 wechat 渠道分支取 out_trade_no/trade_state 后走既有幂等到账链。
//
// 资金安全红线不破：到账仍走 ConfirmOrderByChannel（MarkOrderPaid 条件转移 + 台账先行），
// 本文件只做"协议适配"，不触碰发放逻辑。BaseURL/HTTPClient 可注入便于 httptest 模拟微信端点。
package billing

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
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
// 微信退款结果异步通知（退款成功/失败）走平台侧人工核对，本批不自动改账——
// 与既有 MarkOrderRefunded 的"账面回收先行、PSP 出款显式分离"语义一致。
func (w *WechatPayProvider) Refund(order *model.BillingOrder, refundCents int) error {
	if w.PrivateKey == nil || w.MchID == "" {
		return ErrRefundNotWired
	}
	urlPath := "/v3/refund/domestic/refunds"
	body := map[string]interface{}{
		"out_trade_no":  order.OrderNo,
		"out_refund_no": fmt.Sprintf("RF%s%s", time.Now().Format("20060102150405"), order.OrderNo),
		"amount": map[string]interface{}{
			"refund":   refundCents,
			"total":    order.AmountCents,
			"currency": "CNY",
		},
	}
	_, err := w.doV3(urlPath, body)
	return err
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
	auth, err := w.buildAuthHeader(http.MethodPost, urlPath, string(body))
	if err != nil {
		return nil, err
	}
	base := w.BaseURL
	if base == "" {
		base = wechatPayBase
	}
	req, err := http.NewRequest(http.MethodPost, base+urlPath, bytes.NewReader(body))
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
// 解密成功即证明回调方持有 APIv3Key（平台密钥持有证明）；
// 官方"平台证书验 Wechatpay-Signature"需商户侧定期拉取平台证书，未配置时以解密为准（安全注释位，
// 真实商户号接入后建议补平台证书验签防伪造报文）。
func DecryptWechatResource(apiv3Key string, ciphertext, nonce, associatedData string) (WechatNotify, error) {
	if apiv3Key == "" {
		return WechatNotify{}, errors.New("微信支付 APIv3Key 未配置，无法解密回调")
	}
	block, err := aes.NewCipher([]byte(apiv3Key))
	if err != nil {
		return WechatNotify{}, fmt.Errorf("APIv3Key 长度非法（须 32 字节）: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return WechatNotify{}, err
	}
	ct, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return WechatNotify{}, fmt.Errorf("回调密文 base64 解码失败: %w", err)
	}
	// 防御（2026-09-15）：GCM 对非 12 字节 nonce 会直接 panic（crypto/cipher 内部断言），
	// 恶意/畸形回调会把 403 变 500——必须先校验长度 fail-closed
	if len(nonce) != 12 {
		return WechatNotify{}, fmt.Errorf("回调 resource.nonce 长度非法（须 12 字节，实得 %d）", len(nonce))
	}
	if len(ct) < 16 {
		return WechatNotify{}, errors.New("回调密文过短（不足 GCM tag 长度）")
	}
	plain, err := gcm.Open(nil, []byte(nonce), ct, []byte(associatedData))
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
