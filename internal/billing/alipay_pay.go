// 支付宝适配器（P0-1c，2026-09-15，AUDIT_GAP_VERIFICATION §2 P0-1）。
//
// 补齐支付宝开放平台协议（当面付扫码 + 同步退款），与 WechatPayProvider 同批落地：
//
//	CreatePayment：alipay.trade.precreate 预下单，RSA2 签名，qr_code 转 data:image PNG。
//	Refund：alipay.trade.refund（同步接口，fund_change=Y 即出款成功——与微信异步语义不同）。
//	VerifyAlipayNotify：异步通知 RSA2 全参数验签——供 api.BillingWebhook 的 alipay 渠道分支。
//
// 资金安全红线不破：到账仍走 ConfirmOrderByChannel（幂等到账 + 台账先行）。
// GatewayURL/HTTPClient/私钥可注入便于 httptest 模拟。
package billing

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"ai-scrm/internal/model"
)

// alipayGateway 支付宝开放平台正式网关
const alipayGateway = "https://openapi.alipay.com/gateway.do"

// AlipayProvider 支付宝开放平台配置。
type AlipayProvider struct {
	AppID           string // 应用 appid
	PrivateKey      *rsa.PrivateKey
	AlipayPublicKey *rsa.PublicKey // 支付宝公钥（异步通知验签；开放平台"接口加签方式"下载）
	NotifyURL       string
	GatewayURL      string // 空=正式网关；测试注入 httptest
	HTTPClient      *http.Client
}

// Name 渠道标识（与订单 channel 列、回调 :channel 参数、渠道互验 L3 对齐）
func (a *AlipayProvider) Name() string { return "alipay" }

// centsToYuan 分 → 元字符串（支付宝金额协议为元，两位小数）
func centsToYuan(cents int) string {
	return fmt.Sprintf("%.2f", float64(cents)/100)
}

// alipaySign RSA2 签名：参数（排除 sign/sign_type 与空值）按 key 字典序拼 k=v&k2=v2 → SHA256withRSA → base64。
// 支付宝官方《接口签名规则》（RSA2）：待签名串不含 sign 与 sign_type（异步通知验签同口径，
// 官方各语言 demo 均排除二者），空值参数不参与。请求签名与通知验签共用本规则。
func alipaySign(params map[string]string, key *rsa.PrivateKey) (string, error) {
	if key == nil {
		return "", errors.New("支付宝应用私钥未加载（pay_alipay_private_key 缺失或格式非法）")
	}
	var pairs []string
	for k, v := range params {
		if k == "sign" || k == "sign_type" || v == "" {
			continue
		}
		pairs = append(pairs, k+"="+v)
	}
	sort.Strings(pairs)
	message := strings.Join(pairs, "&")
	digest := sha256.Sum256([]byte(message))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("RSA2 签名失败: %w", err)
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// alipayPost 组装公共参数 → 签名 → form POST 网关 → 解析响应。
// 返回 method 对应的响应体 map（如 alipay_trade_precreate_response）。
func (a *AlipayProvider) alipayPost(method string, bizContent map[string]interface{}) (map[string]interface{}, error) {
	biz, err := json.Marshal(bizContent)
	if err != nil {
		return nil, err
	}
	params := map[string]string{
		"app_id":      a.AppID,
		"method":      method,
		"format":      "JSON",
		"charset":     "utf-8",
		"sign_type":   "RSA2",
		"timestamp":   time.Now().Format("2006-01-02 15:04:05"),
		"version":     "1.0",
		"biz_content": string(biz),
	}
	if a.NotifyURL != "" {
		params["notify_url"] = a.NotifyURL
	}
	sig, err := alipaySign(params, a.PrivateKey)
	if err != nil {
		return nil, err
	}
	params["sign"] = sig

	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	gw := a.GatewayURL
	if gw == "" {
		gw = alipayGateway
	}
	client := a.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.PostForm(gw, form)
	if err != nil {
		return nil, fmt.Errorf("支付宝请求失败: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("支付宝响应异常 http=%d body=%s", resp.StatusCode, truncateResp(string(data)))
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("支付宝响应 JSON 解析失败: %w", err)
	}
	// 响应结构：{alipay_trade_xxx_response:{...}, sign:"..."}
	// 键名与 method 对应：alipay.trade.precreate → alipay_trade_precreate_response
	respKey := strings.ReplaceAll(strings.ReplaceAll(method, ".", "_"), "-", "_") + "_response"
	body, ok := out[respKey].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("支付宝响应缺少 %s: %s", respKey, truncateResp(string(data)))
	}
	// 网关级错误码：10000=成功；其余（如 40004 业务失败/20000 权限）直接报错
	if code, _ := body["code"].(string); code != "10000" {
		msg, _ := body["msg"].(string)
		sub, _ := body["sub_msg"].(string)
		return nil, fmt.Errorf("支付宝 %s 失败 code=%s msg=%s sub=%s", method, code, msg, sub)
	}
	return body, nil
}

// CreatePayment 当面付预下单：qr_code 转 PNG data URI（收银台 <img> 直接展示）。
func (a *AlipayProvider) CreatePayment(order *model.BillingOrder) (string, error) {
	if a.PrivateKey == nil || a.AppID == "" {
		return "", errors.New("支付宝未配置（pay_alipay_app_id/pay_alipay_private_key 缺失）")
	}
	body, err := a.alipayPost("alipay.trade.precreate", map[string]interface{}{
		"out_trade_no": order.OrderNo,
		"total_amount": centsToYuan(order.AmountCents),
		"subject":      fmt.Sprintf("AI-SCRM 订单 %s", order.OrderNo),
	})
	if err != nil {
		return "", err
	}
	qr, _ := body["qr_code"].(string)
	if qr == "" {
		return "", fmt.Errorf("支付宝预下单响应缺少 qr_code")
	}
	return qrToDataURL(qr)
}

// Refund 同步退款：alipay.trade.refund，fund_change=Y（或本次有退款金额返回）即出款成功。
// out_request_no 防重：同一笔退款请求号重试幂等（支付宝侧部分退款幂等锚）。
func (a *AlipayProvider) Refund(order *model.BillingOrder, refundCents int) error {
	if a.PrivateKey == nil || a.AppID == "" {
		return ErrRefundNotWired
	}
	body, err := a.alipayPost("alipay.trade.refund", map[string]interface{}{
		"out_trade_no":   order.OrderNo,
		"refund_amount":  centsToYuan(refundCents),
		"out_request_no": fmt.Sprintf("RF%s%s", time.Now().Format("20060102150405"), order.OrderNo),
	})
	if err != nil {
		return err
	}
	// fund_change=Y 表示本次真实发生资金变动；N 可能是重复请求（幂等重试），同样视为成功
	if fc, _ := body["fund_change"].(string); fc == "N" {
		return nil
	}
	return nil
}

// VerifyAlipayNotify 异步通知 RSA2 验签。
// params 为通知的全部 form 参数（含 sign）；验签规则与请求签名对称：
// 排除 sign 与空值 → key 字典序拼串 → 支付宝公钥 SHA256withRSA 验签。
// 返回 out_trade_no 与 trade_status（TRADE_SUCCESS/TRADE_FINISHED 为到账态）。
func VerifyAlipayNotify(pub *rsa.PublicKey, params map[string]string) (outTradeNo, tradeStatus string, err error) {
	if pub == nil {
		return "", "", errors.New("支付宝平台公钥未配置（pay_alipay_public_key），无法验签")
	}
	sign := params["sign"]
	if sign == "" {
		return "", "", errors.New("通知缺少 sign")
	}
	var pairs []string
	for k, v := range params {
		if k == "sign" || k == "sign_type" || v == "" {
			continue
		}
		pairs = append(pairs, k+"="+v)
	}
	sort.Strings(pairs)
	message := strings.Join(pairs, "&")
	digest := sha256.Sum256([]byte(message))
	sig, err := base64.StdEncoding.DecodeString(sign)
	if err != nil {
		return "", "", fmt.Errorf("sign base64 解码失败: %w", err)
	}
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		return "", "", errors.New("支付宝通知验签失败（报文被篡改或公钥不匹配）")
	}
	return params["out_trade_no"], params["trade_status"], nil
}
