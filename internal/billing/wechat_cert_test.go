// 微信支付 V3 平台证书验签单测（E1-2，2026-09-24）。
//
// 这里覆盖的是"验签层"的全部判定分支：签名对/错/缺、序列号命中/未知/临期、
// 证书缓存命中与刷新限流、/v3/certificates 的解析与解密、以及 GET 签名串口径。
// 真实商户号证书拿不到，所以用**自签测试证书 + httptest 模拟证书端点**：
// 协议形态（签名串拼接、base64、AES-GCM 密文、序列号大写十六进制）与微信一致，
// 唯一替换的是"谁持有平台私钥"——那是信任根，本来就不该在单测里出现真的。
package billing

import (
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// wechatAPIv3TestKey 32 字节 APIv3Key（长度是硬约束，aes.NewCipher 会校验）。
const wechatAPIv3TestKey = "unit_test_apiv3_key_0123456789ab" // 32 字节，APIv3Key 长度硬约束

// testWechatCert 一张自签"平台证书"及其私钥。
type testWechatCert struct {
	Serial string
	Key    *rsa.PrivateKey
	PEM    []byte
	Cert   wechatPlatformCert
}

// newTestWechatCert 生成 RSA 密钥对与自签证书。life 从 now 起算，用于构造临期/过期场景。
func newTestWechatCert(t *testing.T, serial int64, now time.Time, life time.Duration) testWechatCert {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成测试私钥失败: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "wechatpay-unit-test-platform"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(life),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("自签证书失败: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return testWechatCert{
		Serial: strings.ToUpper(fmt.Sprintf("%x", big.NewInt(serial))),
		Key:    key,
		PEM:    pemBytes,
		Cert: wechatPlatformCert{
			Serial:    strings.ToUpper(fmt.Sprintf("%x", big.NewInt(serial))),
			PublicKey: &key.PublicKey,
			NotBefore: now.Add(-time.Minute),
			NotAfter:  now.Add(life),
		},
	}
}

// signCallback 按微信规范签一条回调：SHA256-RSA2048 over "时间戳\n随机串\n报文原文\n"。
func signCallback(t *testing.T, key *rsa.PrivateKey, ts, nonce, body string) string {
	t.Helper()
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\n%s\n%s\n", ts, nonce, body)))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("签名失败: %v", err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

// encryptCertPEM 用 APIv3Key 把证书 PEM 封成 /v3/certificates 的 encrypt_certificate 结构。
func encryptCertPEM(t *testing.T, pemBytes []byte) (ciphertext, nonce, aad string) {
	t.Helper()
	nonce = "certnonce123" // 12 字节
	block, err := aes.NewCipher([]byte(wechatAPIv3TestKey))
	if err != nil {
		t.Fatalf("APIv3Key 非法: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("构造 GCM 失败: %v", err)
	}
	ct := gcm.Seal(nil, []byte(nonce), pemBytes, []byte("certificate"))
	return base64.StdEncoding.EncodeToString(ct), nonce, "certificate"
}

// useCertStore 把全局证书缓存换成注入源（fetch 可为 nil 表示"永远拉不到"），用例结束还原。
// 返回当前 fetch 调用次数指针，用于断言缓存/限流行为而不是只看错误与否。
func useCertStore(t *testing.T, now time.Time, fetch func(context.Context, *WechatPayProvider) ([]wechatPlatformCert, error)) *int {
	t.Helper()
	s := defaultWechatCertStore
	origByMch, origNow, origFetch := s.byMch, s.nowFunc, s.fetchFunc
	calls := new(int)
	s.byMch = map[string]*wechatCertBucket{}
	s.nowFunc = func() time.Time { return now }
	s.fetchFunc = func(ctx context.Context, w *WechatPayProvider) ([]wechatPlatformCert, error) {
		*calls++
		if fetch == nil {
			return nil, errors.New("模拟：证书接口不可用")
		}
		return fetch(ctx, w)
	}
	t.Cleanup(func() {
		s.byMch, s.nowFunc, s.fetchFunc = origByMch, origNow, origFetch
	})
	return calls
}

// newTestWechatProvider 只填验签所需字段（MchID/PrivateKey/APIv3Key），BaseURL 指向 mock。
func newTestWechatProvider(serial string, key *rsa.PrivateKey) *WechatPayProvider {
	return &WechatPayProvider{
		AppID:      "wx_test_appid",
		MchID:      "1900000109",
		SerialNo:   serial,
		PrivateKey: key,
		APIv3Key:   wechatAPIv3TestKey,
		NotifyURL:  "https://example.com/hook",
	}
}

// TestVerifyWechatCallbackAcceptsValidPlatformSignature 平台证书签名正确时应验过，并附反证：签名串少拼一个换行即验不过（防断言吃的是"任何签名都算数"）。
func TestVerifyWechatCallbackAcceptsValidPlatformSignature(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	pc := newTestWechatCert(t, 0x1ABCDEF012345678, now, 24*time.Hour)
	calls := useCertStore(t, now, func(context.Context, *WechatPayProvider) ([]wechatPlatformCert, error) {
		return []wechatPlatformCert{pc.Cert}, nil
	})
	w := newTestWechatProvider(pc.Serial, pc.Key)
	body := `{"resource":{"ciphertext":"x"}}`
	ts, nonce := "1771034400", "abcdef012345"
	h := WechatCallbackHeaders{Signature: signCallback(t, pc.Key, ts, nonce, body), Serial: pc.Serial, Timestamp: ts, Nonce: nonce}
	if err := VerifyWechatCallback(context.Background(), w, h, []byte(body), true); err != nil {
		t.Fatalf("合法签名应验过，实际: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("首次验签应拉一次证书接口，实际 %d 次", *calls)
	}
	// 反证：签名串少拼末尾那个 \n（微信规范为 ts\nnonce\nbody\n）就验不过——
	// 证明上面的通过吃的是拼接口径，不是"任何签名都算数"。
	badMsg := fmt.Sprintf("%s\n%s\n%s", ts, nonce, body)
	badDigest := sha256.Sum256([]byte(badMsg))
	badSig, err := rsa.SignPKCS1v15(rand.Reader, pc.Key, crypto.SHA256, badDigest[:])
	if err != nil {
		t.Fatalf("构造反证签名失败: %v", err)
	}
	badHdr := WechatCallbackHeaders{Signature: base64.StdEncoding.EncodeToString(badSig), Serial: pc.Serial, Timestamp: ts, Nonce: nonce}
	if err := VerifyWechatCallback(context.Background(), w, badHdr, []byte(body), true); err == nil {
		t.Fatalf("签名串拼接错误却验过，护栏空转")
	}
}

// TestVerifyWechatCallbackRejectsTamperedBody 同一签名配被篡改的报文必须拒；配原文必须过——两条一起才说明失败原因真是"报文被改"。
func TestVerifyWechatCallbackRejectsTamperedBody(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	pc := newTestWechatCert(t, 0x1ABCDEF012345678, now, 24*time.Hour)
	useCertStore(t, now, func(context.Context, *WechatPayProvider) ([]wechatPlatformCert, error) {
		return []wechatPlatformCert{pc.Cert}, nil
	})
	w := newTestWechatProvider(pc.Serial, pc.Key)
	signed := `{"out_trade_no":"BO1","amount":{"total":100}}`
	tampered := `{"out_trade_no":"BO1","amount":{"total":1}}`
	ts, nonce := "1771034400", "n1"
	h := WechatCallbackHeaders{Signature: signCallback(t, pc.Key, ts, nonce, signed), Serial: pc.Serial, Timestamp: ts, Nonce: nonce}
	// 微信按原文签名，落账读的是收到的原文：这里把"改过的报文"配上"原报文签名"送进来
	if err := VerifyWechatCallback(context.Background(), w, h, []byte(tampered), true); err == nil {
		t.Fatalf("篡改报文必须验不过")
	}
	// 同一签名对原文本身必须过（否则上面那条失败是"什么都验不过"的假绿）
	if err := VerifyWechatCallback(context.Background(), w, h, []byte(signed), true); err != nil {
		t.Fatalf("原文验签失败，篡改用例即无效: %v", err)
	}
}

// TestVerifyWechatCallbackUsesRawBodyBytes 签名对象是未改动的原始字节：带空格的原文验过、Unmarshal 再 Marshal 的结果必须验不过。
func TestVerifyWechatCallbackUsesRawBodyBytes(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	pc := newTestWechatCert(t, 0x1122334455667788, now, 24*time.Hour)
	useCertStore(t, now, func(context.Context, *WechatPayProvider) ([]wechatPlatformCert, error) {
		return []wechatPlatformCert{pc.Cert}, nil
	})
	w := newTestWechatProvider(pc.Serial, pc.Key)
	// 微信实发报文带空格与换行；若调用方 Unmarshal 后再 Marshal（键序/空白变了）就永远验不过
	raw := "{\n  \"resource\": { \"ciphertext\": \"AAA\" },\n  \"event_type\": \"TRANSACTION.SUCCESS\"\n}"
	ts, nonce := "1771034400", "n2"
	h := WechatCallbackHeaders{Signature: signCallback(t, pc.Key, ts, nonce, raw), Serial: pc.Serial, Timestamp: ts, Nonce: nonce}
	if err := VerifyWechatCallback(context.Background(), w, h, []byte(raw), true); err != nil {
		t.Fatalf("原始字节验签应通过: %v", err)
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("样例报文非合法 JSON: %v", err)
	}
	re, _ := json.Marshal(v)
	if err := VerifyWechatCallback(context.Background(), w, h, re, true); err == nil {
		t.Fatalf("重新序列化后的报文竟验过——说明签名串没吃原文字节")
	}
}

// TestVerifyWechatCallbackUnknownSerialRateLimitsRefresh 未知序列号来自外部报文头，5 次攻击式请求只准触发 1 次证书拉取（刷新限流），且一律 fail-closed。
func TestVerifyWechatCallbackUnknownSerialRateLimitsRefresh(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	pc := newTestWechatCert(t, 0x1AAAABBBBCCCCDDD, now, 24*time.Hour)
	calls := useCertStore(t, now, func(context.Context, *WechatPayProvider) ([]wechatPlatformCert, error) {
		return []wechatPlatformCert{pc.Cert}, nil
	})
	w := newTestWechatProvider(pc.Serial, pc.Key)
	body := `{"a":1}`
	// 序列号是外部输入：第一次未知 → 刷新一次；第二次仍在 60s 间隔内 → 不得再打接口
	for i := 0; i < 5; i++ {
		h := WechatCallbackHeaders{Signature: "SGVsbG8=", Serial: "DEADBEEFDEADBEEF", Timestamp: "1771034400", Nonce: "n"}
		err := VerifyWechatCallback(context.Background(), w, h, []byte(body), true)
		if !errors.Is(err, ErrWechatCertUnavailable) {
			t.Fatalf("未知序列号必须归到证书不可用（fail-closed），实际: %v", err)
		}
	}
	if *calls != 1 {
		t.Fatalf("5 次未知序列号请求只准打 1 次证书接口，实际 %d 次", *calls)
	}
}

// TestVerifyWechatCallbackVerifiesOldSerialDuringRotation 轮换窗口一次拉取返回新旧两张证书，两张都要能验签且只打一次接口（只留最新一张会把真回调全判伪造）。
func TestVerifyWechatCallbackVerifiesOldSerialDuringRotation(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	oldC := newTestWechatCert(t, 0x000000000000AAAA, now, 24*time.Hour)
	newC := newTestWechatCert(t, 0x000000000000BBBB, now, 24*time.Hour)
	calls := useCertStore(t, now, func(context.Context, *WechatPayProvider) ([]wechatPlatformCert, error) {
		// 轮换窗口：接口一次返回两张（新旧并存），缓存必须两张都留
		return []wechatPlatformCert{oldC.Cert, newC.Cert}, nil
	})
	w := newTestWechatProvider(newC.Serial, newC.Key)
	body := `{"out_trade_no":"BO9"}`
	for i, c := range []testWechatCert{oldC, newC, oldC} {
		ts := fmt.Sprintf("177103440%d", i)
		h := WechatCallbackHeaders{Signature: signCallback(t, c.Key, ts, "n", body), Serial: c.Serial, Timestamp: ts, Nonce: "n"}
		if err := VerifyWechatCallback(context.Background(), w, h, []byte(body), true); err != nil {
			t.Fatalf("序列号 %s 应能验签（轮换窗口不得误判伪造）: %v", c.Serial, err)
		}
	}
	if *calls != 1 {
		t.Fatalf("两张证书应复用同一次拉取，实际 %d 次", *calls)
	}
}

// TestVerifyWechatCallbackRejectsExpiringCert 距失效不足安全余量的证书不得再用于验签，且不会反复拉接口。
func TestVerifyWechatCallbackRejectsExpiringCert(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	// 距失效不足安全余量（1h）→ 不该再拿来验签，否则会卡在边界把真回调放进来
	pc := newTestWechatCert(t, 0x000000000000CCCC, now, 30*time.Minute)
	calls := useCertStore(t, now, func(context.Context, *WechatPayProvider) ([]wechatPlatformCert, error) {
		return []wechatPlatformCert{pc.Cert}, nil
	})
	w := newTestWechatProvider(pc.Serial, pc.Key)
	body := `{"x":1}`
	ts := "1771034400"
	h := WechatCallbackHeaders{Signature: signCallback(t, pc.Key, ts, "n", body), Serial: pc.Serial, Timestamp: ts, Nonce: "n"}
	err := VerifyWechatCallback(context.Background(), w, h, []byte(body), true)
	if !errors.Is(err, ErrWechatCertUnavailable) && err == nil {
		t.Fatalf("临期证书必须拒验")
	}
	if err == nil {
		t.Fatalf("临期证书被用于验签")
	}
	if *calls != 1 {
		t.Fatalf("临期证书只会拉一次，实际 %d 次", *calls)
	}
}

// TestVerifyWechatCallbackAbsentSignaturePolicy 缺签名头：严格态 ErrWechatSigMissing、宽松态放行交由解密证明兜底，两种都不该外呼证书接口。
func TestVerifyWechatCallbackAbsentSignaturePolicy(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	calls := useCertStore(t, now, nil)
	w := newTestWechatProvider("SERIAL", nil)
	body := []byte(`{"x":1}`)
	// 严格态：无签名头即拒（连证书都不必拉）
	if err := VerifyWechatCallback(context.Background(), w, WechatCallbackHeaders{Timestamp: "1", Nonce: "n"}, body, true); !errors.Is(err, ErrWechatSigMissing) {
		t.Fatalf("严格态缺签名应返回 ErrWechatSigMissing，实际: %v", err)
	}
	// 非严格态：放行，由调用方的解密证明兜底（存量部署零感知）
	if err := VerifyWechatCallback(context.Background(), w, WechatCallbackHeaders{Timestamp: "1", Nonce: "n"}, body, false); err != nil {
		t.Fatalf("非严格态缺签名应放行，实际: %v", err)
	}
	if *calls != 0 {
		t.Fatalf("缺签名不应打证书接口，实际 %d 次", *calls)
	}
}

// TestVerifyWechatCallbackFailClosedWithoutMerchantCreds 带了签名但商户凭证装配不出来（nil/缺商户号/缺私钥/缺 APIv3Key）一律 fail-closed，绝不回退成"只验解密"。
func TestVerifyWechatCallbackFailClosedWithoutMerchantCreds(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	calls := useCertStore(t, now, func(context.Context, *WechatPayProvider) ([]wechatPlatformCert, error) {
		return nil, errors.New("不该被调用")
	})
	body := []byte(`{"x":1}`)
	h := WechatCallbackHeaders{Signature: "YWJj", Serial: "S1", Timestamp: "1", Nonce: "n"}
	for name, w := range map[string]*WechatPayProvider{
		"nil 提供商":    nil,
		"缺商户号":       {APIv3Key: wechatAPIv3TestKey},
		"缺私钥":        {MchID: "1900000109", APIv3Key: wechatAPIv3TestKey},
		"缺 APIv3Key": {MchID: "1900000109", PrivateKey: &rsa.PrivateKey{}},
	} {
		if err := VerifyWechatCallback(context.Background(), w, h, body, true); !errors.Is(err, ErrWechatCertUnavailable) {
			t.Fatalf("%s：带签名却验不了必须 fail-closed，实际: %v", name, err)
		}
	}
	if *calls != 0 {
		t.Fatalf("凭证不全时不应外呼证书接口，实际 %d 次", *calls)
	}
}

// TestVerifyWechatCallbackGarbageSignatureAndMissingFields 签名非 base64、缺 Timestamp/Nonce、缺 Serial、非平台私钥所签：四条都在本地判定阶段拒掉，不误发外呼。
func TestVerifyWechatCallbackGarbageSignatureAndMissingFields(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	pc := newTestWechatCert(t, 0x0000000012345678, now, 24*time.Hour)
	calls := useCertStore(t, now, func(context.Context, *WechatPayProvider) ([]wechatPlatformCert, error) {
		return []wechatPlatformCert{pc.Cert}, nil
	})
	w := newTestWechatProvider(pc.Serial, pc.Key)
	body := []byte(`{"x":1}`)
	// base64 非法：不必外呼即可拒
	if err := VerifyWechatCallback(context.Background(), w, WechatCallbackHeaders{Signature: "!!!not-base64!!!", Serial: pc.Serial, Timestamp: "1", Nonce: "n"}, body, true); err == nil {
		t.Fatalf("非法 base64 签名必须报错")
	}
	if *calls != 0 {
		t.Fatalf("签名解码失败不应打证书接口，实际 %d 次", *calls)
	}
	// 缺时间戳/随机串：签名串无法还原
	if err := VerifyWechatCallback(context.Background(), w, WechatCallbackHeaders{Signature: "YWJj", Serial: pc.Serial}, body, true); err == nil {
		t.Fatalf("缺 Timestamp/Nonce 必须报错")
	}
	// 缺序列号：给出可定位的配置类错误，而不是"证书集里找不到"
	err := VerifyWechatCallback(context.Background(), w, WechatCallbackHeaders{Signature: "YWJj", Timestamp: "1", Nonce: "n"}, body, true)
	if err == nil || !strings.Contains(err.Error(), "Wechatpay-Serial") {
		t.Fatalf("缺序列号应报明确定位错误，实际: %v", err)
	}
	// 合法 base64 但非本证书所签
	if err := VerifyWechatCallback(context.Background(), w, WechatCallbackHeaders{Signature: base64.StdEncoding.EncodeToString([]byte("wrong")), Serial: pc.Serial, Timestamp: "1", Nonce: "n"}, body, true); err == nil {
		t.Fatalf("非平台私钥签名必须验不过")
	}
}

// TestVerifyWechatCallbackFetchErrorIsFailClosed 证书接口拉不到时，即便报文签名本身正确也拒收——宁可让微信重推，不放行不可验证的到账。
func TestVerifyWechatCallbackFetchErrorIsFailClosed(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	pc := newTestWechatCert(t, 0x00000000CAFEBABE, now, 24*time.Hour)
	useCertStore(t, now, nil) // 拉取恒失败
	w := newTestWechatProvider(pc.Serial, pc.Key)
	body := `{"x":1}`
	ts, nonce := "1771034400", "n"
	h := WechatCallbackHeaders{Signature: signCallback(t, pc.Key, ts, nonce, body), Serial: pc.Serial, Timestamp: ts, Nonce: nonce}
	// 网络抖动 + 报文本身签名正确 → 依然拒收（宁可让微信重推，也不放行不可验证的到账）
	if err := VerifyWechatCallback(context.Background(), w, h, []byte(body), true); !errors.Is(err, ErrWechatCertUnavailable) {
		t.Fatalf("证书拉取失败必须 fail-closed，实际: %v", err)
	}
}

// TestResetWechatCertCacheScopedByMerchant 清缓存按商户号隔离：清 M1 不得连带清 M2，空商户号才是全清。
func TestResetWechatCertCacheScopedByMerchant(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	calls := useCertStore(t, now, func(context.Context, *WechatPayProvider) ([]wechatPlatformCert, error) {
		return nil, errors.New("boom")
	})
	s := defaultWechatCertStore
	s.byMch["M1"] = &wechatCertBucket{certs: map[string]wechatPlatformCert{"S": {}}, fetchedAt: now}
	s.byMch["M2"] = &wechatCertBucket{certs: map[string]wechatPlatformCert{"S": {}}, fetchedAt: now}
	ResetWechatCertCache("M1")
	if _, ok := s.byMch["M1"]; ok {
		t.Fatalf("指定商户号应被清除")
	}
	if _, ok := s.byMch["M2"]; !ok {
		t.Fatalf("清 M1 不得连带清 M2（多商户共用进程）")
	}
	ResetWechatCertCache("")
	if len(s.byMch) != 0 {
		t.Fatalf("空商户号=全清，实际残留 %d 桶", len(s.byMch))
	}
	if *calls != 0 {
		t.Fatalf("清缓存不应外呼")
	}
}

// TestFetchWechatPlatformCertsAgainstMockEndpoint 走真实 HTTP 路径：GET 签名口径 + 证书解密 + 序列号索引。
func TestFetchWechatPlatformCertsAgainstMockEndpoint(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	merchKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成商户私钥失败: %v", err)
	}
	pc := newTestWechatCert(t, 0x0000FEEDFACE0BAD, now, 24*time.Hour)
	ct, nonce, aad := encryptCertPEM(t, pc.PEM)

	var gotAuth string
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(wr http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		body := map[string]any{
			"data": []map[string]any{{
				"serial_no":      pc.Serial,
				"effective_time": now.Add(-time.Minute).Format(time.RFC3339),
				"expire_time":    now.Add(24 * time.Hour).Format(time.RFC3339),
				"encrypt_certificate": map[string]string{
					"algorithm":       "AEAD_AES_256_GCM",
					"nonce":           nonce,
					"associated_data": aad,
					"ciphertext":      ct,
				},
			}},
		}
		wr.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(wr).Encode(body)
	}))
	defer srv.Close()

	w := newTestWechatProvider("MERCH_SERIAL_1", merchKey)
	w.BaseURL = srv.URL
	certs, err := fetchWechatPlatformCerts(context.Background(), w)
	if err != nil {
		t.Fatalf("拉取证书失败: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/v3/certificates" {
		t.Fatalf("应为 GET /v3/certificates，实际 %s %s", gotMethod, gotPath)
	}
	if len(certs) != 1 || certs[0].Serial != pc.Serial {
		t.Fatalf("证书列表解析异常: %+v", certs)
	}
	if certs[0].PublicKey == nil || certs[0].PublicKey.N.Cmp(pc.Key.PublicKey.N) != 0 {
		t.Fatalf("解密出的公钥与测试证书不一致")
	}
	// 微信 effective/expire 覆盖证书自身有效期
	if !certs[0].NotAfter.Equal(now.Add(24 * time.Hour).Truncate(time.Second)) {
		t.Fatalf("expire_time 未生效，实际 %v", certs[0].NotAfter)
	}
	assertV3GetSignatureValid(t, gotAuth, w, http.MethodGet, "/v3/certificates", merchKey)
}

// assertV3GetSignatureValid 按 GET 规范（请求体段为空）校验 Authorization 里的商户签名。
// 这一条挡的是"GET 用 POST 的签名串（带 {}）"——真机上表现为恒 401 SIGN_ERROR，
// 而单测若只断"证书解出来了"就完全看不见。
func assertV3GetSignatureValid(t *testing.T, auth string, w *WechatPayProvider, method, path string, pubKeySource *rsa.PrivateKey) {
	t.Helper()
	fields := map[string]string{}
	for _, kv := range strings.Split(strings.TrimPrefix(auth, `WECHATPAY2-SHA256-RSA2048 `), ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if !ok {
			continue
		}
		fields[k] = strings.Trim(v, `"`)
	}
	if fields["mchid"] != w.MchID {
		t.Fatalf("Authorization 缺商户号或不一致: %v", fields)
	}
	sig, err := base64.StdEncoding.DecodeString(fields["signature"])
	if err != nil {
		t.Fatalf("签名非 base64: %v", err)
	}
	check := func(body string) error {
		msg := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n", method, path, fields["timestamp"], fields["nonce_str"], body)
		digest := sha256.Sum256([]byte(msg))
		return rsa.VerifyPKCS1v15(&pubKeySource.PublicKey, crypto.SHA256, digest[:], sig)
	}
	if err := check(""); err != nil {
		t.Fatalf("GET 签名（空请求体）校验失败，签名串口径不对: %v", err)
	}
	// 反证：按 POST 口径（body="{}"）拼出来的串必须校验失败，否则上面的通过是空转
	if err := check("{}"); err == nil {
		t.Fatalf("GET 与 POST 签名串无差别——断言未能区分请求体段")
	}
}

// TestFetchWechatPlatformCertsRejectsSerialMismatch 证书列表声明的序列号与证书自身不一致时必须拒收，否则按列表索引后验签永远找不到证书。
func TestFetchWechatPlatformCertsRejectsSerialMismatch(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	merchKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	pc := newTestWechatCert(t, 0x0000000000FF00FF, now, 24*time.Hour)
	ct, nonce, aad := encryptCertPEM(t, pc.PEM)
	srv := httptest.NewServer(http.HandlerFunc(func(wr http.ResponseWriter, r *http.Request) {
		body := map[string]any{"data": []map[string]any{{
			"serial_no":           "000000000000DEAD", // 列表说的与证书里的不是一回事
			"encrypt_certificate": map[string]string{"algorithm": "AEAD_AES_256_GCM", "nonce": nonce, "associated_data": aad, "ciphertext": ct},
		}}}
		_ = json.NewEncoder(wr).Encode(body)
	}))
	defer srv.Close()
	w := newTestWechatProvider("MS", merchKey)
	w.BaseURL = srv.URL
	if _, err := fetchWechatPlatformCerts(context.Background(), w); err == nil {
		t.Fatalf("序列号不一致必须拒收（否则按列表索引后验签永远找不到证书）")
	} else if !strings.Contains(err.Error(), "序列号不一致") {
		t.Fatalf("应报序列号不一致，实际: %v", err)
	}
}

// TestFetchWechatPlatformCertsPropagatesHTTPErrors 非 2xx 要把微信错误码带上抛（排障全靠它），不能只回一句"拉取失败"。
func TestFetchWechatPlatformCertsPropagatesHTTPErrors(t *testing.T) {
	merchKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv := httptest.NewServer(http.HandlerFunc(func(wr http.ResponseWriter, r *http.Request) {
		wr.WriteHeader(http.StatusUnauthorized)
		_, _ = wr.Write([]byte(`{"code":"SIGN_ERROR","message":"签名错误"}`))
	}))
	defer srv.Close()
	w := newTestWechatProvider("MS", merchKey)
	w.BaseURL = srv.URL
	_, err := fetchWechatPlatformCerts(context.Background(), w)
	if err == nil || !strings.Contains(err.Error(), "SIGN_ERROR") {
		t.Fatalf("非 2xx 应带微信错误码上抛便于排障，实际: %v", err)
	}
}

// TestDecryptAPIv3GCMGuardrails 解密前置校验全家桶：密钥空/长度非法、nonce 非 12 字节（旧实现会 panic）、密文非 base64/短于 tag、AAD 不匹配。
func TestDecryptAPIv3GCMGuardrails(t *testing.T) {
	nonce := "plainnonce12"
	block, _ := aes.NewCipher([]byte(wechatAPIv3TestKey))
	gcm, _ := cipher.NewGCM(block)
	plain := `{"out_trade_no":"BO1"}`
	ct := base64.StdEncoding.EncodeToString(gcm.Seal(nil, []byte(nonce), []byte(plain), []byte("transaction")))

	out, err := decryptAPIv3GCM(wechatAPIv3TestKey, ct, nonce, "transaction")
	if err != nil {
		t.Fatalf("合法密文解密失败: %v", err)
	}
	if string(out) != plain {
		t.Fatalf("解密结果不符: %s", out)
	}
	cases := []struct {
		name                          string
		key, ciphertext, n, assocData string
	}{
		{"密钥为空", "", ct, nonce, "transaction"},
		{"密钥长度非法", "shortkey", ct, nonce, "transaction"},
		{"nonce 长度非法（旧实现会 panic）", wechatAPIv3TestKey, ct, "tooshort", "transaction"},
		{"nonce 过长", wechatAPIv3TestKey, ct, nonce + "x", "transaction"},
		{"密文非 base64", wechatAPIv3TestKey, "!!", nonce, "transaction"},
		{"密文短于 tag", wechatAPIv3TestKey, base64.StdEncoding.EncodeToString([]byte("tiny")), nonce, "transaction"},
		{"AAD 不匹配", wechatAPIv3TestKey, ct, nonce, "other"},
	}
	for _, tc := range cases {
		if _, err := decryptAPIv3GCM(tc.key, tc.ciphertext, tc.n, tc.assocData); err == nil {
			t.Fatalf("%s：应解密失败但通过了", tc.name)
		}
	}
}
