// 微信支付 V3「平台证书」验签（E1-2，2026-09-24）。
//
// 补齐的是什么：回调此前只靠 AES-256-GCM 解密成功当"平台密钥持有证明"。那道证明不假，
// 但它是单点的——APIv3Key 一旦泄露（商户平台可查、运维脚本、日志串味），攻击者就能凭自己的
// 密钥构造"合法到账"报文给任意订单发货。官方口径是再加一层：回调带
// Wechatpay-Signature / Wechatpay-Serial / Timestamp / Nonce 四个头，
// 签名由**微信平台私钥**做 SHA256-RSA2048，商户用平台证书公钥验证。攻击者没有平台私钥，
// 泄露 APIv3Key 也签不出来——这一层才是"报文确实来自微信支付"的证据。
//
// 证书从 /v3/certificates 拉取，证书本身再用 APIv3Key 解密（同一把钥匙两种用途，别混淆：
// 拉证书用商户私钥签名，证书内容用 APIv3Key 加密）。微信约每 12 小时轮换、旧证书保留一段时间，
// 所以缓存必须按序列号存**多张**、遇到未知序列号时强制刷新一次——只留最新一张会在轮换窗口
// 把所有正常回调全判成伪造（这是接入验签最典型的一次性事故）。
//
// 施行政策（口径写清楚，别留给"看情况"）：
//   - 报文带了 Wechatpay-Signature → 一律验签，验不过就 403。**没有"带签名但跳过"的分支**，
//     否则开关就成了攻击者可以主动关闭的东西（他不发签名头即可）。
//   - 报文没带签名 → 由 pay_wechat_cert_verify 决定：开=403（严格态），关=按既有解密证明放行
//     （存量部署零感知）。默认关，与"开关未开即不改生产行为"的口径一致；真实商户号接入后应打开。
//   - 拿不到证书（商户凭证未配 / 网络故障）而又必须验签 → fail-closed 403，不回退解密放行。
package billing

import (
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ErrWechatSigMissing 回调未带 Wechatpay-Signature（严格态下即拒）。
var ErrWechatSigMissing = errors.New("wechat: 回调缺少 Wechatpay-Signature")

// ErrWechatCertUnavailable 无法取得平台证书（商户凭证未配置或拉取失败）：需要验签时 fail-closed。
var ErrWechatCertUnavailable = errors.New("wechat: 平台证书不可用，无法验签")

// wechatCertRefreshGap 同一商户两次拉 /v3/certificates 的最小间隔。
//
// 未知序列号是**外部输入**（报文头里随便填一个就能触发"缓存里没有→去刷新"），没有这个间隔
// 等于让攻击者按请求驱动我们打微信接口——那个端点每次都要商户私钥签名、且有限频。
const wechatCertRefreshGap = 60 * time.Second

// wechatCertSafetyMargin 证书有效期余量：距 NotAfter 不足此值即当作"该换新的了"去刷新，
// 而不是卡在边界上把最后一批正常回调判成伪造。微信平台证书约 12h 轮换、24h 失效，取 1h。
// （别照"证书最长寿命"填成 24h——那样每张证书一落地就永久判为临期，验签恒失败。）
const wechatCertSafetyMargin = time.Hour

// wechatPlatformCert 一张平台证书（公钥 + 序列号 + 有效期）。
type wechatPlatformCert struct {
	Serial    string
	PublicKey *rsa.PublicKey
	NotBefore time.Time
	NotAfter  time.Time
}

// wechatCertStore 按商户号分桶的证书缓存。
//
// 为什么按 mchid 而不是全局一份：一台机器上换商户号配置（沙箱↔生产、代运营切号）后，
// 旧商户的证书继续给新商户的回调验签，结果是"全部验不过"，且重启才恢复——很难往配置上想。
type wechatCertStore struct {
	mu        sync.Mutex
	byMch     map[string]*wechatCertBucket
	nowFunc   func() time.Time // 单测注入时钟
	fetchFunc func(context.Context, *WechatPayProvider) ([]wechatPlatformCert, error)
}

type wechatCertBucket struct {
	certs     map[string]wechatPlatformCert
	fetchedAt time.Time
}

var defaultWechatCertStore = &wechatCertStore{
	byMch:     map[string]*wechatCertBucket{},
	nowFunc:   time.Now,
	fetchFunc: fetchWechatPlatformCerts,
}

// ResetWechatCertCache 清空平台证书缓存（换商户凭证、单测隔离用）。channelID 无关，按商户号清。
func ResetWechatCertCache(mchID string) {
	defaultWechatCertStore.mu.Lock()
	if mchID == "" {
		defaultWechatCertStore.byMch = map[string]*wechatCertBucket{}
	} else {
		delete(defaultWechatCertStore.byMch, mchID)
	}
	defaultWechatCertStore.mu.Unlock()
}

// WechatCallbackHeaders 微信回调的四个验签头（大小写按微信实际下发）。
type WechatCallbackHeaders struct {
	Signature string // Wechatpay-Signature（base64）
	Serial    string // Wechatpay-Serial（平台证书序列号）
	Timestamp string // Wechatpay-Timestamp（unix 秒）
	Nonce     string // Wechatpay-Nonce
}

// HasSignature 是否带了签名头（缺签名与签名错是两条不同的政策分支，见文件头）。
func (h WechatCallbackHeaders) HasSignature() bool {
	return strings.TrimSpace(h.Signature) != ""
}

// VerifyWechatCallback 校验一条微信 V3 回调的平台签名。
//
// strict=false：无签名头时返回 ErrWechatSigMissing，调用方据此决定是否放行（解密兜底）；
// 带了签名头则无论 strict 与否都必验。strict 只影响"没带签名"这一种报文。
func VerifyWechatCallback(ctx context.Context, w *WechatPayProvider, h WechatCallbackHeaders, rawBody []byte, strict bool) error {
	if !h.HasSignature() {
		if strict {
			return ErrWechatSigMissing
		}
		return nil // 非严格态：交由调用方的解密证明兜底（既有口径）
	}
	if w == nil || w.PrivateKey == nil || w.MchID == "" || w.APIv3Key == "" {
		return ErrWechatCertUnavailable
	}
	if strings.TrimSpace(h.Timestamp) == "" || strings.TrimSpace(h.Nonce) == "" {
		return errors.New("wechat: 回调缺少 Wechatpay-Timestamp/Nonce，无法还原签名串")
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(h.Signature))
	if err != nil {
		return fmt.Errorf("wechat: 签名 base64 解码失败: %w", err)
	}
	cert, err := defaultWechatCertStore.get(ctx, w, h.Serial)
	if err != nil {
		return err
	}
	// 签名串规范：时间戳\n随机串\n报文原文\n（原文必须是**未改动的请求体字节**，
	// 任何重新序列化都会改变签名——JSON 空格/键序一动就验不过，这是接入侧第一大坑）
	message := fmt.Sprintf("%s\n%s\n%s\n", h.Timestamp, h.Nonce, string(rawBody))
	digest := sha256.Sum256([]byte(message))
	if err := rsa.VerifyPKCS1v15(cert.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
		return fmt.Errorf("wechat: 平台签名校验失败（报文被篡改或非微信支付下发）: %w", err)
	}
	return nil
}

// pick 从缓存里取一张"此刻就该拿来验签"的证书（序列号命中 + 在有效期内且未临期）。
func (b *wechatCertBucket) pick(serial string, now time.Time) (wechatPlatformCert, bool) {
	c, ok := b.certs[serial]
	if !ok {
		return wechatPlatformCert{}, false
	}
	if now.Before(c.NotBefore) || now.Add(wechatCertSafetyMargin).After(c.NotAfter) {
		return wechatPlatformCert{}, false
	}
	return c, true
}

// get 取用于验证该序列号签名的平台证书：命中缓存即直接返回（不打微信接口），
// 缺失或临期则刷新一次，刷新受 wechatCertRefreshGap 限流。
func (s *wechatCertStore) get(ctx context.Context, w *WechatPayProvider, serial string) (wechatPlatformCert, error) {
	serial = strings.TrimSpace(serial)
	if serial == "" {
		return wechatPlatformCert{}, errors.New("wechat: 回调缺少 Wechatpay-Serial，无法定位平台证书")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowFunc()
	if b := s.byMch[w.MchID]; b != nil {
		if c, ok := b.pick(serial, now); ok {
			return c, nil
		}
		if now.Sub(b.fetchedAt) < wechatCertRefreshGap {
			return wechatPlatformCert{}, fmt.Errorf("%w: 序列号 %s 不在证书集内，且距上次拉取不足 %v（防按请求打爆证书接口）",
				ErrWechatCertUnavailable, serial, wechatCertRefreshGap)
		}
	}
	certs, err := s.fetchFunc(ctx, w)
	if err != nil {
		return wechatPlatformCert{}, fmt.Errorf("%w: %v", ErrWechatCertUnavailable, err)
	}
	indexed := make(map[string]wechatPlatformCert, len(certs))
	for _, c := range certs {
		indexed[c.Serial] = c
	}
	s.byMch[w.MchID] = &wechatCertBucket{certs: indexed, fetchedAt: now}
	if c, ok := s.byMch[w.MchID].pick(serial, now); ok {
		return c, nil
	}
	// 刷新后仍没有这张证书：与"接口拉不到"同族（都拿不到可用公钥），统一带 sentinel 上抛，
	// 便于调用方按 ErrWechatCertUnavailable 分支；具体原因留在消息里供排障区分。
	return wechatPlatformCert{}, fmt.Errorf("%w: 平台证书集中找不到可用的序列号 %s", ErrWechatCertUnavailable, serial)
}

// fetchWechatPlatformCerts 调 GET /v3/certificates 并解密出证书公钥列表。
func fetchWechatPlatformCerts(ctx context.Context, w *WechatPayProvider) ([]wechatPlatformCert, error) {
	data, err := w.doV3Get(ctx, "/v3/certificates")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Certificates []struct {
			SerialNo    string `json:"serial_no"`
			EffectiveAt string `json:"effective_time"`
			ExpireAt    string `json:"expire_time"`
			Encrypt     struct {
				Algorithm  string `json:"algorithm"`
				Nonce      string `json:"nonce"`
				AAD        string `json:"associated_data"`
				Ciphertext string `json:"ciphertext"`
			} `json:"encrypt_certificate"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("平台证书列表解析失败: %w（响应 %s）", err, truncateResp(string(data)))
	}
	out := make([]wechatPlatformCert, 0, len(resp.Certificates))
	for _, item := range resp.Certificates {
		pemBytes, err := decryptAPIv3GCM(w.APIv3Key, item.Encrypt.Ciphertext, item.Encrypt.Nonce, item.Encrypt.AAD)
		if err != nil {
			return nil, fmt.Errorf("平台证书 %s 解密失败: %w", item.SerialNo, err)
		}
		cert, err := parsePlatformCert(pemBytes)
		if err != nil {
			return nil, err
		}
		if item.SerialNo != "" && cert.Serial != item.SerialNo {
			// 序列号对不上说明拿到的证书与列表条目不是一回事（配置错或响应被截）——
			// 以**证书自身**为准登记，但保留告警：静默按列表序列号索引会让验签永远找不到证书。
			return nil, fmt.Errorf("平台证书序列号不一致: 列表 %s 证书 %s", item.SerialNo, cert.Serial)
		}
		if item.EffectiveAt != "" {
			cert.NotBefore = parseWechatTime(item.EffectiveAt, cert.NotBefore)
		}
		if item.ExpireAt != "" {
			cert.NotAfter = parseWechatTime(item.ExpireAt, cert.NotAfter)
		}
		out = append(out, *cert)
	}
	return out, nil
}

// parsePlatformCert 解析 PEM 证书，取出 RSA 公钥与序列号（十六进制大写，与微信头一致）。
func parsePlatformCert(pemBytes []byte) (*wechatPlatformCert, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("平台证书 PEM 解码失败")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("平台证书解析失败: %w", err)
	}
	pub, ok := c.PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("平台证书公钥非 RSA 类型")
	}
	return &wechatPlatformCert{
		Serial:    strings.ToUpper(fmt.Sprintf("%x", c.SerialNumber)),
		PublicKey: pub,
		NotBefore: c.NotBefore,
		NotAfter:  c.NotAfter,
	}, nil
}

// parseWechatTime 解析微信的时间字段（RFC3339，如 2026-09-24T10:00:00+08:00）；解析失败回退原值。
func parseWechatTime(s string, fallback time.Time) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return fallback
}

// decryptAPIv3GCM 解密一段 APIv3Key AES-256-GCM 密文（回调 resource 与证书列表共用）。
// 密文口径：base64(密文 ‖ 16 字节 tag)，与微信一致。
func decryptAPIv3GCM(apiv3Key, ciphertext, nonce, associatedData string) ([]byte, error) {
	if apiv3Key == "" {
		return nil, errors.New("微信支付 APIv3Key 未配置")
	}
	block, err := aes.NewCipher([]byte(apiv3Key))
	if err != nil {
		return nil, fmt.Errorf("APIv3Key 长度非法（须 32 字节）: %w", err)
	}
	if len(nonce) != 12 {
		return nil, fmt.Errorf("nonce 长度非法（须 12 字节，实得 %d）", len(nonce))
	}
	ct, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return nil, fmt.Errorf("密文 base64 解码失败: %w", err)
	}
	if len(ct) < 16 {
		return nil, errors.New("密文过短（不足 GCM tag 长度）")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, []byte(nonce), ct, []byte(associatedData))
}
