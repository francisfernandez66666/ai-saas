// Package wxcrypt 微信/企业微信回调消息加解密底座（W1，2026-09-12）
// 实现官方 WXMsgCrypt 算法（企业微信自建应用/微信客服/公众平台 安全模式共用同一套）：
//   - AESKey = base64decode(EncodingAESKey + "=") → 32 字节；IV = AESKey 前 16 字节；AES-256-CBC。
//   - 明文 = random(16) + msg_len(4, 网络字节序) + msg + receive_id；PKCS7 填充到 32 倍数。
//   - msg_signature = sha1( sort([token, timestamp, nonce, 密文base64]) 拼接 )。
//
// 纯计算、零外部依赖、可脱离真实凭证单测/做 mock（W8）。
package wxcrypt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strings"
)

var (
	// ErrSignature 验签失败（msg_signature 与本地计算不符）
	ErrSignature = errors.New("wxcrypt: 消息签名校验失败")
	// ErrAESKey EncodingAESKey 非法（非 43 位或 base64 解码后非 32 字节）
	ErrAESKey = errors.New("wxcrypt: EncodingAESKey 非法（应为 43 位，解码后 32 字节）")
	// ErrReceiveID 解密出的 receive_id 与预期 corp/appid 不一致
	ErrReceiveID = errors.New("wxcrypt: receive_id 不匹配")
)

// Crypt 封装一个通道的加解密上下文（token + 32 字节 AESKey + receiveId）。
// receiveId：企业微信填 corpid，公众平台填 appid。
type Crypt struct {
	token     string
	aesKey    []byte
	receiveID string
}

// New 由 token/encodingAESKey/receiveId 构造。encodingAESKey 为 43 位串。
func New(token, encodingAESKey, receiveID string) (*Crypt, error) {
	if len(encodingAESKey) != 43 {
		return nil, ErrAESKey
	}
	key, err := base64.StdEncoding.DecodeString(encodingAESKey + "=")
	if err != nil || len(key) != 32 {
		return nil, ErrAESKey
	}
	return &Crypt{token: token, aesKey: key, receiveID: receiveID}, nil
}

// aesCipher 返回 CBC 模式块（IV=AESKey 前 16 字节）
func (c *Crypt) aesCipher() (cipher.BlockMode, error) {
	block, err := aes.NewCipher(c.aesKey)
	if err != nil {
		return nil, err
	}
	return cipher.NewCBCDecrypter(block, c.aesKey[:16]), nil
}

// randBlock 返回 CBC 加密器（IV=AESKey 前 16 字节，与解密一致）
func (c *Crypt) randBlock() (cipher.BlockMode, error) {
	block, err := aes.NewCipher(c.aesKey)
	if err != nil {
		return nil, err
	}
	return cipher.NewCBCEncrypter(block, c.aesKey[:16]), nil
}

// Encrypt 明文 msg → base64 密文（供构造回调 mock 出站或加密被动回复）。
func (c *Crypt) Encrypt(msg string) (string, error) {
	bmsg := []byte(msg)
	// random(16) + len(4) + msg + receiveID
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	lenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBuf, uint32(len(bmsg)))
	plain := append(buf, lenBuf...)
	plain = append(plain, bmsg...)
	plain = append(plain, []byte(c.receiveID)...)
	// PKCS7 到 32 倍数
	plain = pkcs7Pad(plain, 32)

	enc, err := c.randBlock()
	if err != nil {
		return "", err
	}
	out := make([]byte, len(plain))
	enc.CryptBlocks(out, plain)
	return base64.StdEncoding.EncodeToString(out), nil
}

// Decrypt 密文(base64) → 明文 msg，并校验 receiveID。
func (c *Crypt) Decrypt(cipherB64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(cipherB64)
	if err != nil {
		return "", fmt.Errorf("wxcrypt: 密文 base64 解码失败: %w", err)
	}
	if len(raw)%aes.BlockSize != 0 {
		return "", errors.New("wxcrypt: 密文长度非块大小整数倍")
	}
	dec, err := c.aesCipher()
	if err != nil {
		return "", err
	}
	plain := make([]byte, len(raw))
	dec.CryptBlocks(plain, raw)
	plain, err = pkcs7Unpad(plain, 32)
	if err != nil {
		return "", err
	}
	// 结构：random(16) + len(4) + msg + receiveID
	if len(plain) < 20 {
		return "", errors.New("wxcrypt: 明文过短")
	}
	msgLen := int(binary.BigEndian.Uint32(plain[16:20]))
	if msgLen < 0 || 20+msgLen > len(plain) {
		return "", errors.New("wxcrypt: 长度字段越界")
	}
	msg := string(plain[20 : 20+msgLen])
	receiveID := string(plain[20+msgLen:])
	if receiveID != c.receiveID {
		return "", ErrReceiveID
	}
	return msg, nil
}

// Signature 计算 msg_signature（4 参字典序拼接后 sha1 hex）。
// 与官方一致：encrypt 为密文 base64 串。
func (c *Crypt) Signature(timestamp, nonce, encrypt string) string {
	arr := []string{c.token, timestamp, nonce, encrypt}
	sort.Strings(arr)
	h := sha1.Sum([]byte(strings.Join(arr, "")))
	return fmt.Sprintf("%x", h)
}

// VerifySignature 校验传入签名与本地计算是否一致。
func (c *Crypt) VerifySignature(timestamp, nonce, encrypt, given string) error {
	if c.Signature(timestamp, nonce, encrypt) != given {
		return ErrSignature
	}
	return nil
}

// DecryptURLParam 处理 URL 验证（GET echostr）：先验签再解密回显。
func (c *Crypt) DecryptURLParam(msgSignature, timestamp, nonce, echostr string) (string, error) {
	if err := c.VerifySignature(timestamp, nonce, echostr, msgSignature); err != nil {
		return "", err
	}
	return c.Decrypt(echostr)
}

// EncryptReply 构造加密的被动回复 XML（明文模式可直接回 XML，安全模式用此）。
// toUser=对方 openid/userid，fromUser=本方 corpid/appid（即 receiveID）。
func (c *Crypt) EncryptReply(timestamp, nonce, toUser, plainMsgXML string) (string, error) {
	enc, err := c.Encrypt(plainMsgXML)
	if err != nil {
		return "", err
	}
	sig := c.Signature(timestamp, nonce, enc)
	var b strings.Builder
	b.WriteString("<xml>")
	fmt.Fprintf(&b, "<Encrypt><![CDATA[%s]]></Encrypt>", enc)
	fmt.Fprintf(&b, "<MsgSignature><![CDATA[%s]]></MsgSignature>", sig)
	fmt.Fprintf(&b, "<TimeStamp>%s</TimeStamp>", timestamp)
	fmt.Fprintf(&b, "<Nonce><![CDATA[%s]]></Nonce>", nonce)
	if toUser != "" {
		fmt.Fprintf(&b, "<ToUserName><![CDATA[%s]]></ToUserName>", toUser)
	}
	fmt.Fprintf(&b, "<FromUserName><![CDATA[%s]]></FromUserName>", c.receiveID)
	b.WriteString("</xml>")
	return b.String(), nil
}

// ============================================================
// PKCS7（块大小 32，微信规范）
// ============================================================

func pkcs7Pad(data []byte, block int) []byte {
	pad := block - len(data)%block
	return append(data, bytes.Repeat([]byte{byte(pad)}, pad)...)
}

func pkcs7Unpad(data []byte, block int) ([]byte, error) {
	n := len(data)
	if n == 0 || n%block != 0 {
		return nil, errors.New("wxcrypt: 填充长度非法")
	}
	pad := int(data[n-1])
	if pad <= 0 || pad > block {
		return nil, errors.New("wxcrypt: PKCS7 填充值越界")
	}
	if !bytes.Equal(data[n-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
		return nil, errors.New("wxcrypt: PKCS7 填充校验失败")
	}
	return data[:n-pad], nil
}
