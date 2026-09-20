// P1-2 回调签名金额归属单元测试（2026-09-20 审计批）
// 覆盖：五段严格签名、空金额=存量兼容四段、篡改金额段即验签失败。
package billing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// signV2 测试内自算 HMAC，口径与 VerifyGatewaySignV2 对齐
func signV2(key, base string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(base))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyGatewaySignV2AmountSegment(t *testing.T) {
	key := "test-gw-key"
	orderNo, status, ts, nonce := "ORD1", "TRADE_SUCCESS", "1700000000", "n-1"

	// 五段：带金额签名，金额一致才通过
	amt := "9900"
	sig5 := signV2(key, orderNo+"|"+status+"|"+ts+"|"+nonce+"|"+amt)
	if !VerifyGatewaySignV2(key, orderNo, status, ts, nonce, amt, sig5) {
		t.Fatal("五段一致签名应通过")
	}
	// 篡改金额（¥0.01 套面额 99 元）：验签必须失败
	if VerifyGatewaySignV2(key, orderNo, status, ts, nonce, "1", sig5) {
		t.Fatal("金额被篡改仍验签通过=资金洞")
	}
	// 带金额入参却配四段旧签（攻击者去掉金额段重算）：不通过
	sig4 := signV2(key, orderNo+"|"+status+"|"+ts+"|"+nonce)
	if VerifyGatewaySignV2(key, orderNo, status, ts, nonce, amt, sig4) {
		t.Fatal("四段旧签在声明金额模式下不得通过")
	}
	// 存量兼容：amountCents 传空 = 显式声明旧四段口径（handler 侧由热开关放行）
	if !VerifyGatewaySignV2(key, orderNo, status, ts, nonce, "", sig4) {
		t.Fatal("空金额入参应回落四段兼容")
	}
	if VerifyGatewaySignV2(key, orderNo, status, ts, nonce, "", sig5) {
		t.Fatal("四段模式不得吞五段签")
	}
	// 参数缺失护栏：空 key/sign/timestamp/nonce 一律拒
	if VerifyGatewaySignV2("", orderNo, status, ts, nonce, amt, sig5) ||
		VerifyGatewaySignV2(key, orderNo, status, ts, nonce, amt, "") ||
		VerifyGatewaySignV2(key, orderNo, status, "", nonce, amt, sig5) ||
		VerifyGatewaySignV2(key, orderNo, status, ts, "", amt, sig5) {
		t.Fatal("空参数必须拒绝")
	}
}
