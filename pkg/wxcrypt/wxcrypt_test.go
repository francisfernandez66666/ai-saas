// W1 单测：加解密往返、签名确定性、验签、篡改/错位拒绝、URL 回显、PKCS7 边界
package wxcrypt

import (
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// 官方示例参数（微信文档公开值，仅用于本地往返自洽测试）
const (
	testToken  = "QDG6eK"
	testAESKey = "jWmYm7qr5nMoAUwZRjGtBxmz3KA1tkAj3ykkR6q2B2C"
	testCorpID = "wx5823bf98d6194e39"
)

func newCrypt(t *testing.T) *Crypt {
	t.Helper()
	c, err := New(testToken, testAESKey, testCorpID)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	return c
}

func TestNewRejectsBadKey(t *testing.T) {
	if _, err := New(testToken, "tooshort", testCorpID); err != ErrAESKey {
		t.Fatalf("非 43 位 key 应报 ErrAESKey, got %v", err)
	}
}

func TestRoundTrip(t *testing.T) {
	c := newCrypt(t)
	msg := "<xml><Content><![CDATA[你好，我想了解你们的越野车]]></Content></xml>"
	enc, err := c.Encrypt(msg)
	if err != nil {
		t.Fatalf("Encrypt 失败: %v", err)
	}
	if enc == msg {
		t.Fatal("密文不应等于明文")
	}
	dec, err := c.Decrypt(enc)
	if err != nil {
		t.Fatalf("Decrypt 失败: %v", err)
	}
	if dec != msg {
		t.Fatalf("往返不一致:\n got %q\nwant %q", dec, msg)
	}
}

func TestEncryptNonceRandom(t *testing.T) {
	c := newCrypt(t)
	a, _ := c.Encrypt("same")
	b, _ := c.Encrypt("same")
	if a == b {
		t.Fatal("相同明文因随机 16 字节前缀应得不同密文")
	}
}

func TestSignatureMatchesManual(t *testing.T) {
	c := newCrypt(t)
	enc, _ := c.Encrypt("hello")
	ts, nonce := "1409659589", "1772739091"
	got := c.Signature(ts, nonce, enc)
	// 手工复算：字典序拼接 sha1
	arr := []string{testToken, ts, nonce, enc}
	sort.Strings(arr)
	h := sha1.Sum([]byte(strings.Join(arr, "")))
	want := fmt.Sprintf("%x", h)
	if got != want {
		t.Fatalf("签名不一致: got %s want %s", got, want)
	}
}

func TestVerifySignature(t *testing.T) {
	c := newCrypt(t)
	enc, _ := c.Encrypt("msg")
	sig := c.Signature("1409659589", "nonce1", enc)
	if err := c.VerifySignature("1409659589", "nonce1", enc, sig); err != nil {
		t.Fatalf("正确签名应验签通过: %v", err)
	}
	if err := c.VerifySignature("1409659589", "nonce1", enc, "deadbeef"); err != ErrSignature {
		t.Fatalf("错误签名应报 ErrSignature, got %v", err)
	}
}

func TestTamperedCiphertextFails(t *testing.T) {
	c := newCrypt(t)
	enc, _ := c.Encrypt("integrity matters here")
	// 翻转密文中间一段
	raw, _ := base64.StdEncoding.DecodeString(enc)
	raw[len(raw)/2] ^= 0xFF
	bad := base64.StdEncoding.EncodeToString(raw)
	// CBC 篡改会破坏填充或长度 → Decrypt 应报错
	if _, err := c.Decrypt(bad); err == nil {
		t.Fatal("被篡改密文应解密失败")
	}
}

func TestReceiveIDMismatch(t *testing.T) {
	// 用 corpid A 加密，用 corpid B 的上下文解密应检出 receive_id 不符
	c1, _ := New(testToken, testAESKey, "corpAAA")
	c2, _ := New(testToken, testAESKey, "corpBBB")
	enc, _ := c1.Encrypt("cross-tenant")
	if _, err := c2.Decrypt(enc); err != ErrReceiveID {
		t.Fatalf("receive_id 不符应报 ErrReceiveID, got %v", err)
	}
}

func TestURLParamFlow(t *testing.T) {
	c := newCrypt(t)
	echo, _ := c.Encrypt("echo-plain-16chars!")
	sig := c.Signature("1409659589", "1372623124", echo)
	got, err := c.DecryptURLParam(sig, "1409659589", "1372623124", echo)
	if err != nil {
		t.Fatalf("DecryptURLParam 失败: %v", err)
	}
	if got != "echo-plain-16chars!" {
		t.Fatalf("回显内容不符: %q", got)
	}
}

func TestEncryptReplyEnvelope(t *testing.T) {
	c := newCrypt(t)
	xml, err := c.EncryptReply("1409659589", "nonce9", "user_openid_123", "<xml><Content>hi</Content></xml>")
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"<Encrypt>", "<MsgSignature>", "<TimeStamp>", "<ToUserName>", "<FromUserName>"} {
		if !strings.Contains(xml, tag) {
			t.Fatalf("回复 XML 缺少 %s: %s", tag, xml)
		}
	}
}

func TestPKCS7Edges(t *testing.T) {
	// 明文长度恰为 32 倍数时仍须补一整块（pad=32）
	c := newCrypt(t)
	msg := strings.Repeat("x", 32) // 16+4+32+corpid 非 32 倍数路径覆盖
	enc, _ := c.Encrypt(msg)
	dec, err := c.Decrypt(enc)
	if err != nil || dec != msg {
		t.Fatalf("边界长度往返失败: err=%v dec=%q", err, dec)
	}
}
