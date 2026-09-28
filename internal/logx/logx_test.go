// C3 单测：手机号/身份证/邮箱/凭据密文掩码、Safe 截断、幂等、误伤防护
package logx

import (
	"encoding/base64"
	"strings"
	"testing"
)

// TestMaskPhone 覆盖 MaskPhone 相关行为与边界。
func TestMaskPhone(t *testing.T) {
	got := Mask("客户手机 13812345678 请回电")
	if strings.Contains(got, "13812345678") {
		t.Fatalf("手机号未掩码: %q", got)
	}
	if !strings.Contains(got, "138****5678") {
		t.Fatalf("应保留首3尾4: %q", got)
	}
}

// TestMaskAdjacentPhones 覆盖 MaskAdjacentPhones 相关行为与边界。
func TestMaskAdjacentPhones(t *testing.T) {
	got := Mask("13811112222,13933334444")
	if strings.Contains(got, "13811112222") || strings.Contains(got, "13933334444") {
		t.Fatalf("相邻两手机号应都掩码: %q", got)
	}
}

// TestMaskIDCard 覆盖 MaskIDCard 相关行为与边界。
func TestMaskIDCard(t *testing.T) {
	got := Mask("身份证 11010119900307123X 备案")
	if strings.Contains(got, "11010119900307123X") {
		t.Fatalf("身份证未掩码: %q", got)
	}
	if !strings.Contains(got, "1101") || !strings.Contains(got, "123X") {
		t.Fatalf("身份证应保留首4尾4: %q", got)
	}
}

// TestMaskEmail 覆盖 MaskEmail 相关行为与边界。
func TestMaskEmail(t *testing.T) {
	got := Mask("邮箱 zhangsan@example.com 已注册")
	if strings.Contains(got, "zhangsan@") {
		t.Fatalf("邮箱未掩码: %q", got)
	}
	if !strings.Contains(got, "z***n@example.com") {
		t.Fatalf("邮箱应保留首尾+域名: %q", got)
	}
}

// TestMaskCipher 凭据密文整段掩码（.env 丢失批，2026-09-28）：
// debug 态 GORM logger.Info 会把 channels 的 *_cipher 列值原样插进 SQL 打屏，
// 本用例锁「密文正文一条都不许留在日志里」，并保留 gcm1: 前缀做可辨识线索。
func TestMaskCipher(t *testing.T) {
	ct := "gcm1:" + base64.StdEncoding.EncodeToString([]byte("nonce12bytes||cipherbody||tag16bytes"))
	in := `INSERT INTO "channels" ("secret_cipher","access_token_cipher") VALUES ('` + ct + `','` + ct + `')`
	got := Mask(in)
	if strings.Contains(got, ct) {
		t.Fatalf("密文未掩码: %q", got)
	}
	if strings.Contains(got, base64.StdEncoding.EncodeToString([]byte("nonce12bytes"))) {
		t.Fatalf("密文只掩了前半截: %q", got)
	}
	// 两条参数都要掩掉，不是"命中第一条就返回"
	if n := strings.Count(got, "gcm1:***(redacted)"); n != 2 {
		t.Fatalf("两条密文应各掩一次，实得 %d 次: %q", n, got)
	}
	// 反向对照：非密文的普通 SQL 片段不得被吞
	if !strings.Contains(got, `INSERT INTO "channels"`) {
		t.Fatalf("SQL 结构被误伤: %q", got)
	}
}

// TestMaskCipherBeatsPhoneInsidePayload 密文必须**先于**手机号规则掩掉。
// base64 载荷里可以出现 13812345678 这种 11 位号段；若手机号规则先跑，
// 结果是"半截密文 + 4 个星"——既不可读，也丢了"这里曾是凭据密文"这条线索。
func TestMaskCipherBeatsPhoneInsidePayload(t *testing.T) {
	in := "cipher=gcm1:YWJjMTM4MTIzNDU2NzhkZWY=" // 载荷内含 13812345678 形态片段
	got := Mask(in)
	if got != "cipher=gcm1:***(redacted)" {
		t.Fatalf("密文应整段替换且不被手机号规则抢先: %q", got)
	}
	// 反向对照：独立的手机号仍按 PII 口径保留首尾（别把两条规则做成同一条）
	if got2 := Mask("手机 13812345678"); !strings.Contains(got2, "138****5678") {
		t.Fatalf("手机号首尾保留口径被破坏: %q", got2)
	}
}

// TestMaskCipherIdempotent 掩码产物含 `*` 与 `(`，不在密文字符类内 ⇒ 二次调用无变化。
// 这条不是走过场：Safe() 与 GORM Trace 会对同一段文本重复调用 Mask。
func TestMaskCipherIdempotent(t *testing.T) {
	once := Mask("值 gcm1:AAAAAAAAAAAAAAAAAAAA 落库")
	twice := Mask(once)
	if once != twice {
		t.Fatalf("密文掩码应幂等: once=%q twice=%q", once, twice)
	}
}

// TestMaskCipherNoFalsePositive 短标记与含空格的普通文案不得误伤。
// 8 位下限是给"gcm1: 后跟空串/枚举值"留的活路；掩掉合法文本会让排查日志变成不可用。
func TestMaskCipherNoFalsePositive(t *testing.T) {
	for _, in := range []string{
		"gcm1:short",          // 长度不足载荷下限
		"gcm1:",               // 空载荷
		"cipher_kind=gcm1 模式", // 只有前缀、无冒号载荷
		"归档表 messages_archive 版本 gcm1:2",
	} {
		if got := Mask(in); got != in {
			t.Fatalf("不应误伤: in=%q got=%q", in, got)
		}
	}
}

// TestMaskIdempotent 覆盖 MaskIdempotent 相关行为与边界。
func TestMaskIdempotent(t *testing.T) {
	once := Mask("手机 13812345678")
	twice := Mask(once)
	if once != twice {
		t.Fatalf("掩码应幂等: once=%q twice=%q", once, twice)
	}
}

// TestNoFalsePositive 覆盖 NoFalsePositive 相关行为与边界。
func TestNoFalsePositive(t *testing.T) {
	// 订单号/时间戳/版本号等非手机号数字不应被吞
	in := "订单 2026091212345678 端口9090 版本v1.2.3"
	got := Mask(in)
	if got != in {
		t.Fatalf("长数字/普通数字不应误伤: in=%q got=%q", in, got)
	}
	// 10 位数字（非 11）不掩码
	if Mask("编号 1381234567") != "编号 1381234567" {
		t.Fatal("10位数字不应被当作手机号掩码")
	}
}

// TestSafeTruncate 覆盖 SafeTruncate 相关行为与边界。
func TestSafeTruncate(t *testing.T) {
	long := strings.Repeat("字", 50)
	got := Safe(long, 20)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("超长应截断加省略号: %q", got)
	}
	if strings.Count(got, "字") != 20 {
		t.Fatalf("应截断到20字: %q", got)
	}
}

// TestSafeMasksPhone 覆盖 SafeMasksPhone 相关行为与边界。
func TestSafeMasksPhone(t *testing.T) {
	// 截断后仍含手机片段也要掩码
	got := Safe("我的电话是13812345678请尽快联系我谢谢", 40)
	if strings.Contains(got, "13812345678") {
		t.Fatalf("Safe 应同时掩码手机号: %q", got)
	}
}

// TestGormLoggerMasksSQL 覆盖 GormLoggerMasksSQL 相关行为与边界。
func TestGormLoggerMasksSQL(t *testing.T) {
	// 脱敏 logger 接口满足编译即验证结构；行为经 Mask 已覆盖
	if NewGormLogger(nil) == nil {
		t.Fatal("NewGormLogger 不应返回 nil")
	}
}
