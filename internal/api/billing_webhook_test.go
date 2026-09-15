// BillingWebhook 渠道分支单测（P0-1b 配套，2026-09-15）：
// 只覆盖协议前置闸（时间窗/参数校验/公钥验签），不触 DB——幂等到账链路
// （ConfirmOrderByChannel 起步）由 smoke_pay/uat E2E 断言兜底。
// 单元环境 runtimecfg 未 Init → getPayConf 走环境变量注入测试公钥。
package api

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// newWebhookRouter 构建仅挂 BillingWebhook 的最小路由（绕开 TenantResolver——回调本就免租户）
func newWebhookRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/billing/webhook/:channel", BillingWebhook)
	return r
}

// uniqNonce 防重放表进程内记录 nonce——测试用唯一值避免 -count 重跑撞 409
func uniqNonce(prefix string) string {
	return prefix + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

// TestWebhookWechat_MissingTimestamp wechat 分支：无/过期 Wechatpay-Timestamp → 403（±5min 窗口闸）
func TestWebhookWechat_MissingTimestamp(t *testing.T) {
	r := newWebhookRouter()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/billing/webhook/wechat", strings.NewReader(`{"resource":{"ciphertext":"x"}}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("缺时间戳应 403，实际 %d", w.Code)
	}
}

// TestWebhookWechat_MissingCiphertext wechat 分支：时间窗内但缺 resource.ciphertext → 400
func TestWebhookWechat_MissingCiphertext(t *testing.T) {
	r := newWebhookRouter()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/billing/webhook/wechat", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Wechatpay-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	req.Header.Set("Wechatpay-Nonce", uniqNonce("nc-wx"))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺 ciphertext 应 400，实际 %d", w.Code)
	}
}

// TestWebhookAlipay_NoPublicKey alipay 分支：平台公钥未配置 → 503（fail-closed，不裸收通知）
func TestWebhookAlipay_NoPublicKey(t *testing.T) {
	t.Setenv("PAY_ALIPAY_PUBLIC_KEY", "")
	r := newWebhookRouter()
	form := url.Values{}
	form.Set("out_trade_no", "BO_X")
	form.Set("notify_id", uniqNonce("nc-ali"))
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/billing/webhook/alipay", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("公钥未配置应 503，实际 %d", w.Code)
	}
}

// TestWebhookAlipay_BadSignature alipay 分支：报文用"另一把私钥"签名（公钥不匹配/报文被篡改）→ 403
func TestWebhookAlipay_BadSignature(t *testing.T) {
	// 注入的公钥来自 signerPriv，但报文实际用 otherPriv 签——模拟公钥不匹配
	signerPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成密钥: %v", err)
	}
	otherPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成密钥: %v", err)
	}
	pubDER, _ := x509.MarshalPKIXPublicKey(&signerPriv.PublicKey)
	t.Setenv("PAY_ALIPAY_PUBLIC_KEY", string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})))

	params := map[string]string{
		"app_id": "20210001", "out_trade_no": "BO_BADSIG", "trade_status": "TRADE_SUCCESS",
		"notify_id": uniqNonce("nc-ali"), "sign_type": "RSA2",
	}
	var pairs []string
	for k, v := range params {
		if k == "sign" || k == "sign_type" || v == "" {
			continue
		}
		pairs = append(pairs, k+"="+v)
	}
	sort.Strings(pairs)
	digest := sha256.Sum256([]byte(strings.Join(pairs, "&")))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, otherPriv, crypto.SHA256, digest[:])
	params["sign"] = base64.StdEncoding.EncodeToString(sig)

	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	r := newWebhookRouter()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/billing/webhook/alipay", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("伪造签名应 403，实际 %d body=%s", w.Code, w.Body.String())
	}
}
