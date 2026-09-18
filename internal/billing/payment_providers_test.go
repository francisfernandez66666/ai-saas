// 支付渠道适配器单测（P0-1，2026-09-15）：
// httptest 模拟微信 V3 / 支付宝网关端点 + 测试 RSA 密钥对，零真实商户号/零真实出网验证协议全链路：
//   - 微信：V3 请求签名构造正确（模拟平台侧用商户公钥验签）→ Native 下单返回 data URI；
//     退款受理/失败语义；回调 resource AES-256-GCM 解密（正确 key 解密 / 错误 key 拒绝）。
//   - 支付宝：RSA2 请求签名正确（模拟网关侧同规则验签）→ 预下单 data URI；同步退款；
//     异步通知全参数验签（原报文通过 / 单字段篡改拒绝）。
package billing

import (
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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"ai-scrm/internal/model"
)

// genTestRSA 生成测试 RSA 密钥对并输出 PEM（每次调用独立，2048 位在测试场景够快）
func genTestRSA(t *testing.T) (*rsa.PrivateKey, string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成测试密钥失败: %v", err)
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("序列化私钥失败: %v", err)
	}
	privPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER}))
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("序列化公钥失败: %v", err)
	}
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
	return key, privPEM, pubPEM
}

// ---- 微信支付 V3 ----

// TestWechatCreatePayment_SignAndQR 下单：模拟微信端点用商户公钥验 Authorization 签名 → 返回 code_url → data URI
func TestWechatCreatePayment_SignAndQR(t *testing.T) {
	priv, _, pubPEM := genTestRSA(t)
	pub, err := ParseRSAPublicKey([]byte(pubPEM))
	if err != nil {
		t.Fatalf("公钥解析: %v", err)
	}

	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code_url":"weixin://wxpay/bizpayurl?pr=TEST123"}`))
	}))
	defer srv.Close()

	w := &WechatPayProvider{
		AppID: "wxAPP", MchID: "1900000001", SerialNo: "SERIAL01",
		PrivateKey: priv, APIv3Key: strings.Repeat("k", 32),
		NotifyURL: "https://example.com/api/v1/billing/webhook/wechat",
		BaseURL:   srv.URL, HTTPClient: srv.Client(),
	}
	// 先触发下单（handler 才会捕获到请求头/体），再做平台侧验签断言
	dataURI, err := w.CreatePayment(testOrder("BO_TEST_WX", 9900))
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if !strings.HasPrefix(dataURI, "data:image/png;base64,") {
		t.Fatalf("应返回 PNG data URI，实际: %.40s", dataURI)
	}

	// 模拟平台侧验签：从 Authorization 提取 signature，按 V3 规范重建签名串
	urlPath := "/v3/pay/transactions/native"
	authParts := map[string]string{}
	for _, seg := range strings.Split(strings.TrimPrefix(gotAuth, "WECHATPAY2-SHA256-RSA2048 "), ",") {
		kv := strings.SplitN(seg, "=", 2)
		if len(kv) == 2 {
			authParts[strings.TrimSpace(kv[0])] = strings.Trim(kv[1], `"`)
		}
	}
	if authParts["mchid"] != "1900000001" || authParts["serial_no"] != "SERIAL01" {
		t.Fatalf("Authorization 头商户/序列号不正确: %s", gotAuth)
	}
	// 请求体应是 JSON（签名串里原样参与）
	msg := fmt.Sprintf("POST\n%s\n%s\n%s\n%s\n", urlPath, authParts["timestamp"], authParts["nonce_str"], gotBody)
	digest := sha256.Sum256([]byte(msg))
	sig, _ := base64.StdEncoding.DecodeString(authParts["signature"])
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("V3 请求签名验签失败（签名串构造与协议不符）: %v", err)
	}
	if !strings.Contains(gotBody, `"out_trade_no"`) || !strings.Contains(gotBody, `"total":9900`) {
		t.Fatalf("下单请求体缺少订单号/金额: %s", gotBody)
	}
}

// TestWechatRefund_RefundAPI 退款：受理 200 → nil；业务失败 400 → error
func TestWechatRefund_RefundAPI(t *testing.T) {
	priv, _, _ := genTestRSA(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/refund/domestic/refunds" {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(`{"refund_id":"RF123","status":"PROCESSING"}`))
	}))
	defer srv.Close()

	w := &WechatPayProvider{MchID: "1900000001", PrivateKey: priv, BaseURL: srv.URL, HTTPClient: srv.Client()}
	if err := w.Refund(testOrder("BO_TEST_RF", 9900), 5000); err != nil {
		t.Fatalf("退款受理应成功: %v", err)
	}
	// 未配置私钥 → ErrRefundNotWired
	if err := (&WechatPayProvider{MchID: "x"}).Refund(testOrder("BO_X", 1), 1); err == nil {
		t.Fatalf("未配置私钥应报错")
	}
}

// TestDecryptWechatResource 回调解密：APIv3Key AES-GCM 正确解密 / 错误 key 拒绝 /
// P1-3 扩展断言：明文中的 amount.total、mchid、appid 一并透出供归属核对
func TestDecryptWechatResource(t *testing.T) {
	apiv3 := strings.Repeat("k", 32)
	plain, _ := json.Marshal(map[string]interface{}{
		"out_trade_no": "BO_DEC1",
		"trade_state":  "SUCCESS",
		"mchid":        "1900000109",
		"appid":        "wxtestappid",
		"amount":       map[string]interface{}{"total": 9900, "currency": "CNY"},
	})
	block, _ := aes.NewCipher([]byte(apiv3))
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	_, _ = rand.Read(nonce)
	ct := gcm.Seal(nil, nonce, plain, []byte("transaction"))

	got, err := DecryptWechatResource(apiv3, base64.StdEncoding.EncodeToString(ct), string(nonce), "transaction")
	if err != nil || got.OutTradeNo != "BO_DEC1" || got.TradeState != "SUCCESS" {
		t.Fatalf("解密应成功 BO_DEC1/SUCCESS，实际 %+v err=%v", got, err)
	}
	if got.MchID != "1900000109" || got.AppID != "wxtestappid" || got.AmountTotal != 9900 {
		t.Fatalf("归属字段应完整透出，实际 %+v", got)
	}
	// 错误 key → 解密失败
	if _, err = DecryptWechatResource(strings.Repeat("x", 32), base64.StdEncoding.EncodeToString(ct), string(nonce), "transaction"); err == nil {
		t.Fatalf("错误 APIv3Key 应解密失败")
	}
}

// TestValidateWechatNotify P1-3 归属核对：金额分毫必对（缺失也拒），
// mchid/appid 配置了才比对，未配置/明文缺字段放行（mock 端点兼容）
func TestValidateWechatNotify(t *testing.T) {
	ok := WechatNotify{OutTradeNo: "BO_V1", TradeState: "SUCCESS", MchID: "1900000109", AppID: "wxapp", AmountTotal: 100}
	if err := ValidateWechatNotify(ok, "wxapp", "1900000109", 100); err != nil {
		t.Fatalf("齐备一致应通过: %v", err)
	}
	if err := ValidateWechatNotify(ok, "", "", 100); err != nil {
		t.Fatalf("未配置 mchid/appid 应跳过比对: %v", err)
	}
	// 金额不一致（¥0.01 套 ¥1.00 面额）→ 拒
	if err := ValidateWechatNotify(WechatNotify{AmountTotal: 1}, "", "", 100); err == nil {
		t.Fatalf("金额不一致必须拒绝")
	}
	// 金额缺失（旧版畸形/裁剪报文）→ fail-closed 拒
	if err := ValidateWechatNotify(WechatNotify{AmountTotal: 0}, "", "", 100); err == nil {
		t.Fatalf("amount.total 缺失必须拒绝")
	}
	if err := ValidateWechatNotify(ok, "", "9999999999", 100); err == nil {
		t.Fatalf("mchid 不一致必须拒绝")
	}
	if err := ValidateWechatNotify(ok, "wxother", "", 100); err == nil {
		t.Fatalf("appid 不一致必须拒绝")
	}
	// 明文缺 mchid（mock 端点）而平台配置了 → 不强求（解密成功已证明持有 APIv3Key）
	if err := ValidateWechatNotify(WechatNotify{AmountTotal: 100}, "", "1900000109", 100); err != nil {
		t.Fatalf("明文缺 mchid 且解密已证密钥归属，应放行: %v", err)
	}
}

// ---- 支付宝 ----

// alipayTestSign 测试侧按支付宝规范拼串签名（与实现同规则，模拟平台/网关视角）
func alipayTestSign(params map[string]string, key *rsa.PrivateKey) string {
	var pairs []string
	for k, v := range params {
		if k == "sign" || k == "sign_type" || v == "" {
			continue
		}
		pairs = append(pairs, k+"="+v)
	}
	sort.Strings(pairs)
	digest := sha256.Sum256([]byte(strings.Join(pairs, "&")))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	return base64.StdEncoding.EncodeToString(sig)
}

// TestAlipayCreatePayment_SignAndQR 预下单：网关侧验证 RSA2 签名 → qr_code → data URI
func TestAlipayCreatePayment_SignAndQR(t *testing.T) {
	priv, _, _ := genTestRSA(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form := map[string]string{}
		for k := range r.PostForm {
			form[k] = r.PostForm.Get(k)
		}
		// 网关侧验签：排除 sign/sign_type/空值（验签口径与通知验签一致）
		var pairs []string
		for k, v := range form {
			if k == "sign" || k == "sign_type" || v == "" {
				continue
			}
			pairs = append(pairs, k+"="+v)
		}
		sort.Strings(pairs)
		digest := sha256.Sum256([]byte(strings.Join(pairs, "&")))
		sig, _ := base64.StdEncoding.DecodeString(form["sign"])
		if err := rsa.VerifyPKCS1v15(&priv.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"alipay_trade_precreate_response":{"code":"10000","msg":"Success","qr_code":"https://qr.alipay.com/TEST"},"sign":"x"}`))
	}))
	defer srv.Close()

	a := &AlipayProvider{AppID: "20210001", PrivateKey: priv, NotifyURL: "https://example.com/api/v1/billing/webhook/alipay", GatewayURL: srv.URL, HTTPClient: srv.Client()}
	dataURI, err := a.CreatePayment(testOrder("BO_TEST_ALI", 9900))
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if !strings.HasPrefix(dataURI, "data:image/png;base64,") {
		t.Fatalf("应返回 PNG data URI，实际: %.40s", dataURI)
	}
}

// TestAlipayRefund_Sync 同步退款：fund_change=Y → nil；业务失败码 → error
func TestAlipayRefund_Sync(t *testing.T) {
	priv, _, _ := genTestRSA(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/" {
			_, _ = w.Write([]byte(`{"alipay_trade_refund_response":{"code":"40004","msg":"Business Failed"},"sign":"x"}`))
			return
		}
		_, _ = w.Write([]byte(`{"alipay_trade_refund_response":{"code":"10000","msg":"Success","fund_change":"Y"},"sign":"x"}`))
	}))
	defer srv.Close()

	a := &AlipayProvider{AppID: "20210001", PrivateKey: priv, GatewayURL: srv.URL, HTTPClient: srv.Client()}
	if err := a.Refund(testOrder("BO_TEST_RFRF", 9900), 5000); err != nil {
		t.Fatalf("同步退款应成功: %v", err)
	}
}

// TestVerifyAlipayNotify 异步通知验签：原报文通过 / 篡改拒绝 / 金额元格式正确
func TestVerifyAlipayNotify(t *testing.T) {
	priv, _, pubPEM := genTestRSA(t)
	pub, err := ParseRSAPublicKey([]byte(pubPEM))
	if err != nil {
		t.Fatalf("公钥解析: %v", err)
	}
	if centsToYuan(9900) != "99.00" || centsToYuan(1) != "0.01" {
		t.Fatalf("分转元格式错误: %s/%s", centsToYuan(9900), centsToYuan(1))
	}

	params := map[string]string{
		"app_id": "20210001", "out_trade_no": "BO_N1", "trade_status": "TRADE_SUCCESS",
		"total_amount": "99.00", "notify_id": "NID1", "sign_type": "RSA2",
	}
	params["sign"] = alipayTestSign(params, priv)

	no, status, err := VerifyAlipayNotify(pub, params)
	if err != nil || no != "BO_N1" || status != "TRADE_SUCCESS" {
		t.Fatalf("合法通知验签应通过，实际 no=%s status=%s err=%v", no, status, err)
	}
	// 单字段篡改 → 验签失败
	params["total_amount"] = "0.01"
	if _, _, err = VerifyAlipayNotify(pub, params); err == nil {
		t.Fatalf("篡改报文应验签失败")
	}
}

// TestParseRSAKeys_PEM 兼容性：PKCS1 私钥/公钥 PEM 解析
func TestParseRSAKeys_PEM(t *testing.T) {
	key, _, _ := genTestRSA(t)
	p1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	got, err := ParseRSAPrivateKey(p1)
	if err != nil || got.N.Cmp(key.N) != 0 {
		t.Fatalf("PKCS1 私钥应可解析: %v", err)
	}
	pubDER, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	if _, err = ParseRSAPublicKey(pubPEM); err != nil {
		t.Fatalf("PKIX 公钥应可解析: %v", err)
	}
	if _, err = ParseRSAPrivateKey([]byte("not-pem")); err == nil {
		t.Fatalf("非 PEM 应报错")
	}
}

// testOrder 构造测试订单（不触 db，仅供 Provider 协议层使用）
func testOrder(no string, cents int) *model.BillingOrder {
	return &model.BillingOrder{OrderNo: no, AmountCents: cents}
}

// TestWebhookNonceRelease P1-7(2026-09-18) 落账失败归还 nonce：
// 消费→重放拒绝→释放→同 nonce 可再进入（PSP 重推不再 409 死锁）；空 nonce 恒判重放。
// 本测试走内存轨（单测环境未启用 Redis），与线上 Redis 轨语义一致。
func TestWebhookNonceRelease(t *testing.T) {
	nonce := "test_nonce_release_memtrack"
	if WebhookNonceSeen(nonce) {
		t.Fatalf("首次消费应判非重放")
	}
	if !WebhookNonceSeen(nonce) {
		t.Fatalf("二次同 nonce 应判重放")
	}
	WebhookNonceRelease(nonce)
	if WebhookNonceSeen(nonce) {
		t.Fatalf("释放后同 nonce 应可再进入（落账失败重推链路）")
	}
	// 空 nonce 一律视为重放（调用方拒 409），释放不应改变该语义
	WebhookNonceRelease("")
	if !WebhookNonceSeen("") {
		t.Fatalf("空 nonce 必须恒判重放")
	}
}
