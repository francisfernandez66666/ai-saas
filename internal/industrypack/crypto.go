package industrypack

// ============================================================
// 行业包密码学层（标准库实现，零第三方依赖）
//
// 威胁模型：防竞品直接解包抽取知识资产 + 防传输篡改；非军事级保密
// （二进制免费分发，密钥随引擎在客户侧，逆向属高成本侵权行为）
//
// 单对 RSA-2048 双职责：
//   平台侧私钥：签名 manifest（完整性+来源）+ 无
//   引擎侧公钥：验签
//   打包侧公钥：RSA-OAEP 封装随机 AES-256 会话密钥
//   引擎侧私钥：解封 AES key → AES-GCM 解包体
// 即：验签防篡改/防伪造；信封加密防直接解压抽取内容。
// ============================================================

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
)

// GenerateKeyPair 生成 RSA 密钥对，返回 PEM 编码 (private, public)
func GenerateKeyPair(bits int) ([]byte, []byte, error) {
	if bits < 2048 {
		bits = 2048
	}
	key, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		return nil, nil, err
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, nil, err
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	return privPEM, pubPEM, nil
}

// LoadPrivateKey 解析 PEM 私钥（PKCS1 或 PKCS8 兼容）
func LoadPrivateKey(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("私钥 PEM 解析失败")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("私钥解析失败: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("非 RSA 私钥")
	}
	return rk, nil
}

// LoadPublicKey 解析 PEM 公钥（PKIX）
func LoadPublicKey(data []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("公钥 PEM 解析失败")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("公钥解析失败: %w", err)
	}
	rk, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("非 RSA 公钥")
	}
	return rk, nil
}

// SignSHA256 RSA PKCS1v15 签名
func SignSHA256(priv *rsa.PrivateKey, data []byte) ([]byte, error) {
	h := sha256.Sum256(data)
	return rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, h[:])
}

// VerifySHA256 RSA 验签（篡改/伪造返回错误）
func VerifySHA256(pub *rsa.PublicKey, data, sig []byte) error {
	h := sha256.Sum256(data)
	return rsa.VerifyPKCS1v15(pub, crypto.SHA256, h[:], sig)
}

// Seal 混合加密封装：随机 AES-256-GCM 加密明文，AES key 经 RSA-OAEP(pub) 封装
// 返回 packet = [u32 blobLen][oaepBlob][12B nonce][ciphertext]
func Seal(pub *rsa.PublicKey, plaintext []byte) (packet []byte, aesKey []byte, err error) {
	aesKey = make([]byte, 32)
	if _, err = rand.Read(aesKey); err != nil {
		return
	}
	blob, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, aesKey, nil)
	if err != nil {
		return
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return
	}
	ct := gcm.Seal(nil, nonce, plaintext, nil)

	packet = make([]byte, 0, 4+len(blob)+len(nonce)+len(ct))
	packet = appendU32(packet, uint32(len(blob)))
	packet = append(packet, blob...)
	packet = append(packet, nonce...)
	packet = append(packet, ct...)
	return packet, aesKey, nil
}

// OpenSeal 解封装：拆 packet → OAEP 还原 AES key → GCM 解密
func OpenSeal(priv *rsa.PrivateKey, packet []byte) ([]byte, error) {
	if len(packet) < 4 {
		return nil, errors.New("信封数据不完整")
	}
	blobLen := int(readU32(packet))
	if 4+blobLen+12 > len(packet) {
		return nil, errors.New("信封长度非法")
	}
	blob := packet[4 : 4+blobLen]
	rest := packet[4+blobLen:]
	aesKey, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, priv, blob, nil)
	if err != nil {
		return nil, fmt.Errorf("会话密钥解封失败（密钥不匹配或包被替换）: %w", err)
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(rest) < gcm.NonceSize() {
		return nil, errors.New("nonce 缺失")
	}
	return gcm.Open(nil, rest[:gcm.NonceSize()], rest[gcm.NonceSize():], nil)
}

// appendU32 / readU32 大端序 u32 读写
func appendU32(b []byte, v uint32) []byte {
	return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func readU32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// ---- 小工具 ----

// sha256Hex 十六进制摘要
func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h)
}
