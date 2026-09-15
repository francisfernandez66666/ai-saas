// P0-1 复核批（2026-09-15）回归：支付宝异步通知"验签通过后的业务参数核对"纯逻辑测试。
// 覆盖：跨 app 通知拒绝、金额分核对（元→分精确换算）、seller_id 可选核对、缺配置 fail-closed。
package billing

import (
	"encoding/json"
	"testing"
)

// TestAlipayYuanToCents 元→分换算：支付宝 total_amount 固定两位小数，禁止浮点乘法（精度陷阱）。
func TestAlipayYuanToCents(t *testing.T) {
	ok := []struct {
		in   string
		want int64
	}{
		{"1.00", 100}, {"0.01", 1}, {"10", 1000}, {"99.90", 9990},
		{"0.1", 10}, {"1.200", 120}, {"1.000", 100}, {"-1.00", -100}, {"0", 0},
	}
	for _, c := range ok {
		got, err := alipayYuanToCents(c.in)
		if err != nil || got != c.want {
			t.Errorf("alipayYuanToCents(%q)=%d,%v 期望 %d", c.in, got, err, c.want)
		}
	}
	bad := []string{"abc", "1.005", "1..2", "1.2.3", "x.5", ""}
	for _, s := range bad {
		if _, err := alipayYuanToCents(s); err == nil {
			t.Errorf("alipayYuanToCents(%q) 应报错，实际通过", s)
		}
	}
}

// TestValidateAlipayNotifyParams 回调三参数必核（P0-1 根因：平台公钥全网共用，只验签=可被
// 攻击者用自己的 app 以 ¥0.01 支付受害者订单号后转发真签名通知套现）。
func TestValidateAlipayNotifyParams(t *testing.T) {
	base := map[string]string{
		"app_id": "2021good", "seller_id": "SELLER9", "total_amount": "1.00",
		"out_trade_no": "BO1", "trade_status": "TRADE_SUCCESS",
	}
	// 全字段一致 → 通过
	if err := billingValidateForTest(base, "2021good", "SELLER9", 100); err != nil {
		t.Fatalf("一致参数应通过: %v", err)
	}
	// 跨 app 通知（攻击主路径）→ 拒
	if err := billingValidateForTest(map[string]string{"app_id": "2021evil", "total_amount": "1.00"}, "2021good", "", 100); err == nil {
		t.Error("app_id 不一致必须拒绝")
	}
	// 未配置本方 app_id → fail-closed 拒收（无核对锚不放行）
	if err := billingValidateForTest(base, "", "", 100); err == nil {
		t.Error("pay_alipay_app_id 未配置必须 fail-closed")
	}
	// 金额与订单不符（¥0.01 洗单）→ 拒
	if err := billingValidateForTest(map[string]string{"app_id": "2021good", "total_amount": "0.01"}, "2021good", "", 100); err == nil {
		t.Error("金额不一致必须拒绝")
	}
	// total_amount 缺失/非法 → 拒
	if err := billingValidateForTest(map[string]string{"app_id": "2021good"}, "2021good", "", 100); err == nil {
		t.Error("total_amount 缺失必须拒绝")
	}
	// seller_id：配置了但通知给了别的商户 → 拒；通知未带 seller_id → 跳过该项（当面付部分场景不下发）
	if err := billingValidateForTest(map[string]string{"app_id": "2021good", "seller_id": "OTHER", "total_amount": "1.00"}, "2021good", "SELLER9", 100); err == nil {
		t.Error("seller_id 不一致必须拒绝")
	}
	if err := billingValidateForTest(map[string]string{"app_id": "2021good", "total_amount": "1.00"}, "2021good", "SELLER9", 100); err != nil {
		t.Errorf("通知缺 seller_id 应跳过核对，实际拒绝: %v", err)
	}
}

// billingValidateForTest 薄封装：同包直调私有校验函数的公开形态，保持测试读式简洁。
func billingValidateForTest(params map[string]string, appID, sellerID string, cents int64) error {
	return ValidateAlipayNotifyParams(params, appID, sellerID, cents)
}

// jsonValidForTest 用标准库回读验证 JSON 合法性（round-trip 证据）。
func jsonValidForTest(s string) error {
	var m map[string]interface{}
	return json.Unmarshal([]byte(s), &m)
}

// TestPaymentDataJSON P2-11 回归：payment_data 必须经 encoding/json 产出合法 JSON——
// 旧 fmt.Sprintf 手拼遇到 channel 含引号即生成脏数据。
func TestPaymentDataJSON(t *testing.T) {
	got := paymentDataJSON(map[string]interface{}{"channel": `wx"evil\`, "late_payment_reopen": true})
	if got == "{}" || got == "" {
		t.Fatalf("应产出 JSON 而非兜底/空串: %q", got)
	}
	// 关键断言：可被标准库回读（round-trip 即合法性证据）
	if err := jsonValidForTest(got); err != nil {
		t.Fatalf("payment_data 非法 JSON: %v (%q)", err, got)
	}
}
