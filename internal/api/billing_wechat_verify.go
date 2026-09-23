// 微信支付 V3 回调验签的接口层装配（E1-2，2026-09-24）。
//
// 为什么单开一个文件：验签本身是协议层的事（internal/billing/wechat_cert.go），
// 而"要不要验、拿什么身份去验、验不了怎么办"是接口层的政策。政策写在 handler 里
// 有三处会被后来人误改（strict 判定、fail-closed 分支、原文读取），所以连同常量一起
// 收在这里，handler 只留一次调用。
package api

import (
	"fmt"
	"os"
	"strings"

	"github.com/gin-gonic/gin"

	"ai-scrm/internal/billing"
	"ai-scrm/internal/runtimecfg"
)

// wechatCallbackBodyLimit 回调报文长度上限（1MB）。微信交易通知实际几百字节，
// 这条只为止血：验签要拿整段原文做哈希，不设上限等于让人用请求体换内存。
const wechatCallbackBodyLimit = 1 << 20

// wechatCertVerifyPolicyOn 是否处于"没带签名头也拒"的严格态。
//
// 默认关：存量部署（含 mock/自建 PSP 链路）此前从不做验签，打开即把全部回调判成伪造，
// 表现是"客户付了钱订单永远 pending"——这是最坏的一种上线事故，所以按项目一贯口径
// "开关未开即不改生产行为"，真实商户号接入后由运维显式打开。
// 注意它管不到"带了签名头"的情况：那种报文无论开关都必须验过（见 wechatCertVerify）。
func wechatCertVerifyPolicyOn() bool {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("PAY_WECHAT_CERT_VERIFY"))); v != "" {
		return v == "true" || v == "1"
	}
	return runtimecfg.SafeCfgBool("pay_wechat_cert_verify", false)
}

// wechatCertVerify 校验一条微信 V3 回调的平台签名。返回 nil 表示"可以继续走解密"。
//
// 三条分支的先后次序是刻意的：
//  1. 没带 Wechatpay-Signature → 严格态拒（ErrWechatSigMissing）、宽松态放行（解密证明兜底，既有口径）；
//  2. 带了签名但商户凭证装配不出来（没配私钥/商户号/APIv3Key）→ **fail-closed 拒**，
//     绝不回退成"那就只验解密"。回退等于把这条防线变成"配好了才有"，而配置漂移是常态；
//  3. 带了签名也配好了 → 真验，验不过一律拒（报文被篡改、序列号未知且刷不到证书，都算没过）。
func wechatCertVerify(c *gin.Context, rawBody []byte) error {
	headers := billing.WechatCallbackHeaders{
		Signature: c.GetHeader("Wechatpay-Signature"),
		Serial:    c.GetHeader("Wechatpay-Serial"),
		Timestamp: c.GetHeader("Wechatpay-Timestamp"),
		Nonce:     c.GetHeader("Wechatpay-Nonce"),
	}
	strict := wechatCertVerifyPolicyOn()
	if !headers.HasSignature() {
		if strict {
			return fmt.Errorf("回调缺少 Wechatpay-Signature（已开启平台证书严格验签）")
		}
		return nil
	}
	w, err := billing.LoadWechatProviderFromConf()
	if err != nil {
		return fmt.Errorf("微信支付商户凭证不可用，无法验证平台签名（fail-closed 拒绝）: %w", err)
	}
	if err := billing.VerifyWechatCallback(c.Request.Context(), w, headers, rawBody, strict); err != nil {
		return err
	}
	return nil
}
