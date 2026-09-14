// D5 单测：AES-256-GCM 凭据加解密往返、密钥缺失 fail-closed、轮换后解密失败、明文兼容、掩码
package crypto

import (
	"os"
	"strings"
	"testing"
)

const testKey = "unit-test-secret-key-32-chars!!!!!" // 仅测试用（≥16 字符门槛）

func setKey(t *testing.T, v string) {
	t.Helper()
	old, had := os.LookupEnv("JWT_SECRET")
	_ = os.Setenv("JWT_SECRET", v)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("JWT_SECRET", old)
		} else {
			_ = os.Unsetenv("JWT_SECRET")
		}
	})
}

// TestEncryptDecryptRoundTrip 覆盖 EncryptDecryptRoundTrip 相关行为与边界。
func TestEncryptDecryptRoundTrip(t *testing.T) {
	setKey(t, testKey)
	secret := "wecom_corp_secret_@@中文123"
	ct, err := Encrypt(secret)
	if err != nil {
		t.Fatalf("Encrypt 失败: %v", err)
	}
	if !strings.HasPrefix(ct, "gcm1:") {
		t.Fatalf("密文应带 gcm1: 前缀, got %q", ct)
	}
	if ct == secret || strings.Contains(ct, secret) {
		t.Fatal("密文不应包含明文")
	}
	pt, err := Decrypt(ct)
	if err != nil {
		t.Fatalf("Decrypt 失败: %v", err)
	}
	if pt != secret {
		t.Fatalf("往返不一致: got %q want %q", pt, secret)
	}
}

// TestEncryptNonceUnique 覆盖 EncryptNonceUnique 相关行为与边界。
func TestEncryptNonceUnique(t *testing.T) {
	setKey(t, testKey)
	a, _ := Encrypt("same-plain")
	b, _ := Encrypt("same-plain")
	if a == b {
		t.Fatal("相同明文两次加密应因随机 nonce 得到不同密文")
	}
}

// TestEncryptEmpty 覆盖 EncryptEmpty 相关行为与边界。
func TestEncryptEmpty(t *testing.T) {
	setKey(t, testKey)
	if got, err := Encrypt(""); err != nil || got != "" {
		t.Fatalf("空串应原样返回: got %q err %v", got, err)
	}
	if got, err := Decrypt(""); err != nil || got != "" {
		t.Fatalf("空密文应原样返回: got %q err %v", got, err)
	}
}

// TestKeyNotConfiguredFailClosed 覆盖 KeyNotConfiguredFailClosed 相关行为与边界。
func TestKeyNotConfiguredFailClosed(t *testing.T) {
	setKey(t, "")
	if _, err := Encrypt("x"); err == nil {
		t.Fatal("JWT_SECRET 缺失时 Encrypt 必须报错（fail-closed）")
	}
	setKey(t, "short")
	if _, err := Encrypt("x"); err == nil {
		t.Fatal("JWT_SECRET 过短时 Encrypt 必须报错")
	}
}

// TestKeyRotationYieldsDecryptError 覆盖 KeyRotationYieldsDecryptError 相关行为与边界。
func TestKeyRotationYieldsDecryptError(t *testing.T) {
	setKey(t, testKey)
	ct, _ := Encrypt("to-be-lost")
	// 换密钥后应报 ErrDecrypt（对外话术：凭据需重录）
	setKey(t, "another-secret-key-32-chars!!!!!!")
	if _, err := Decrypt(ct); err == nil {
		t.Fatal("密钥轮换后解密应失败")
	}
}

// TestDecryptLegacyPlainPassthrough 覆盖 DecryptLegacyPlainPassthrough 相关行为与边界。
func TestDecryptLegacyPlainPassthrough(t *testing.T) {
	setKey(t, testKey)
	// 非 gcm1: 前缀视为历史明文，原样返回（不报错，供平滑迁移）
	if got, err := Decrypt("legacy-plaintext-secret"); err != nil || got != "legacy-plaintext-secret" {
		t.Fatalf("历史明文应原样返回: got %q err %v", got, err)
	}
}

// TestTamperedCiphertextRejected 覆盖 TamperedCiphertextRejected 相关行为与边界。
func TestTamperedCiphertextRejected(t *testing.T) {
	setKey(t, testKey)
	ct, _ := Encrypt("integrity-check")
	raw := strings.TrimPrefix(ct, "gcm1:")
	b := []byte(raw)
	b[len(b)/2] ^= 'A' // 翻一个 bit
	if _, err := Decrypt("gcm1:" + string(b)); err == nil {
		t.Fatal("被篡改的密文必须解密失败（GCM 认证）")
	}
}

// TestMaskSecret 覆盖 MaskSecret 相关行为与边界。
func TestMaskSecret(t *testing.T) {
	cases := map[string]string{
		"":                 "",
		"abc":              "****",
		"abcdef":           "****",
		"sk_live_123456ab": "sk****ab",
	}
	for in, want := range cases {
		if got := MaskSecret(in); got != want {
			t.Errorf("MaskSecret(%q)=%q want %q", in, got, want)
		}
	}
}
