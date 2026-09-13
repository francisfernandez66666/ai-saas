// Package crypto 敏感配置加密（D5，2026-09-12）
// 用途：企微/公众号通道凭据（corpid secret/token/aeskey，W2）、第三方机审密钥（C1）等
// 需落库的敏感串统一 AES-256-GCM 加密，明文只在创建时一次性回显。
// 密钥派生：SHA-256(JWT_SECRET + salt/v1)——不引入 KMS，与现有 JWT 信任根一致。
// 已知边界：JWT_SECRET 轮换后旧密文解不开，调用方须给出"凭据需重录"的明确错误而非 500。
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// keySalt 域分隔盐：确保加密密钥与 JWT 签名密钥即便同源也不可互换使用
const keySalt = "|scrm-secretbox/v1"

// ErrKeyNotConfigured JWT_SECRET 缺失/过短时拒绝启动加密（fail-closed，宁报错不裸存）
var ErrKeyNotConfigured = errors.New("crypto: JWT_SECRET 未配置或过短(<16字符)，无法加密敏感凭据")

// ErrDecrypt 密文无效或密钥已轮换（对外统一话术："凭据需重录"）
var ErrDecrypt = errors.New("crypto: 解密失败（密文损坏或密钥已轮换，需重新录入凭据）")

// deriveKey 从环境变量 JWT_SECRET 派生 32 字节密钥。
// 不做进程级缓存：一次 SHA-256 仅微秒级，而缓存会让"轮换密钥"与单测注入变得别扭。
func deriveKey() ([]byte, error) {
	secret := strings.TrimSpace(os.Getenv("JWT_SECRET"))
	if len(secret) < 16 {
		return nil, ErrKeyNotConfigured
	}
	sum := sha256.Sum256([]byte(secret + keySalt))
	return sum[:], nil
}

// Encrypt 明文 → "gcm1:" + base64(nonce||ciphertext||tag)。空串原样返回（允许字段留空）
func Encrypt(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	key, err := deriveKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("crypto: 初始化密码块失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("crypto: 初始化 GCM 失败: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("crypto: 生成随机 nonce 失败: %w", err)
	}
	ct := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return "gcm1:" + base64.StdEncoding.EncodeToString(ct), nil
}

// Decrypt 还原密文；非 gcm1: 前缀视为"历史明文"直接返回（平滑过渡，调用方可据此提示重录）
func Decrypt(cipherText string) (string, error) {
	if cipherText == "" {
		return "", nil
	}
	if !strings.HasPrefix(cipherText, "gcm1:") {
		return cipherText, nil // 兼容历史明文列
	}
	key, err := deriveKey()
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(cipherText, "gcm1:"))
	if err != nil {
		return "", ErrDecrypt
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", ErrDecrypt
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", ErrDecrypt
	}
	if len(raw) < gcm.NonceSize() {
		return "", ErrDecrypt
	}
	nonce, body := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return "", ErrDecrypt
	}
	return string(plain), nil
}

// MaskSecret 出接口展示用：保留头尾各 2 字符，中间打星（空串原样）
func MaskSecret(s string) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= 6 {
		return "****"
	}
	return string(r[:2]) + "****" + string(r[len(r)-2:])
}
