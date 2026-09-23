// 会话存档解密链单测（E8-1，2026-09-24）：纯函数层，不碰库、不需要企微凭据。
//
// 这批的价值全押在这里：拉取要靠官方 C SDK（本机/CI 都跑不动），但"给定密文能不能解对、
// 解不开会不会留下可诊断的错误"与凭据无关。测试用**自建加密器**反向复刻企微的姿势
// （RSA-PKCS1v15 加密 Base64(AES 密钥) + AES-256-CBC 零 IV + PKCS7），
// 因此断言的是协议实现本身，而不是"我自己写的两套代码互相认同"。
package channel

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"strings"
	"testing"
)

// mustArchiveKeyPair 生成一把 2048 位测试密钥对（每次新生成，保证"两把不同密钥"用例成立）。
func mustArchiveKeyPair(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成测试密钥对失败: %v", err)
	}
	return key
}

// toPemBlock 按给定 PEM 头打包 DER（负向用例要构造"头是对的、体是别的算法"）。
func toPemBlock(t *testing.T, typ string, der []byte) []byte {
	t.Helper()
	b := pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
	if b == nil {
		t.Fatalf("PEM 编码失败: %s", typ)
	}
	return b
}

// firstLine 取首行，只用于错误文案。
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// pkcs7Pad 测试侧填充（与实现里的 stripPKCS7 对偶，单独写一份以免"实现自证"）。
func pkcs7Pad(buf []byte, blockSize int) []byte {
	pad := blockSize - len(buf)%blockSize
	return append(buf, bytes.Repeat([]byte{byte(pad)}, pad)...)
}

// encryptArchivePlain 按企微姿势加密一段明文，返回可直接喂给 DecryptArchiveItem 的信封。
// iv 传 nil 走官方样例的零 IV；传非 nil 走 encrypt_param 单独回传 IV 的那条分支。
// key 允许非 32 字节，用于构造"密钥长度非法"的负向场景。
func encryptArchivePlain(t *testing.T, pub *rsa.PublicKey, key []byte, iv []byte, plain []byte) ArchiveCipherItem {
	t.Helper()
	if key == nil {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			t.Fatalf("随机 AES 密钥失败: %v", err)
		}
	}
	if iv == nil {
		iv = archiveZeroIV
	}
	aesKeyB64 := base64.StdEncoding.EncodeToString(key)
	rsaBlob, err := rsa.EncryptPKCS1v15(rand.Reader, pub, []byte(aesKeyB64))
	if err != nil {
		t.Fatalf("RSA 加密测试失败: %v", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("AES 分组器构造失败: %v", err)
	}
	padded := pkcs7Pad(plain, block.BlockSize())
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	item := ArchiveCipherItem{
		PublicKeyVer:     1,
		EncryptRandomKey: base64.StdEncoding.EncodeToString(rsaBlob),
		EncryptChatMsg:   base64.StdEncoding.EncodeToString(out),
	}
	if string(iv) != string(archiveZeroIV) {
		item.EncryptParam = base64.StdEncoding.EncodeToString(iv)
	}
	return item
}

// TestDecryptArchiveItemRoundTrip 正向回环：自建密文 → 解密 → 逐字节等值。
func TestDecryptArchiveItemRoundTrip(t *testing.T) {
	priv := mustArchiveKeyPair(t)
	want := []byte(`{"msgid":"m1","msgtype":"text","text":{"content":"你好"}}`)
	item := encryptArchivePlain(t, &priv.PublicKey, nil, nil, want)
	got, err := DecryptArchiveItem(priv, item)
	if err != nil {
		t.Fatalf("回环解密失败: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("明文不一致:\n got %q\nwant %q", got, want)
	}
}

// TestDecryptArchiveItemWrongKeyFails 私钥不匹配必须报错，不能返回乱码当明文。
func TestDecryptArchiveItemWrongKeyFails(t *testing.T) {
	priv := mustArchiveKeyPair(t)
	other := mustArchiveKeyPair(t)
	item := encryptArchivePlain(t, &priv.PublicKey, nil, nil, []byte(`{"msgid":"m1"}`))
	if _, err := DecryptArchiveItem(other, item); err == nil {
		t.Fatal("用另一把私钥解开了密文，说明密钥根本没参与解密")
	} else if !strings.Contains(err.Error(), "RSA 解密失败") {
		t.Fatalf("错误文案应指向密钥/公钥版本不匹配，实得: %v", err)
	}
}

// TestDecryptArchiveItemCustomIV 覆盖 encrypt_param 单独回传 IV 的分支（SDK 版本差异）。
func TestDecryptArchiveItemCustomIV(t *testing.T) {
	priv := mustArchiveKeyPair(t)
	iv := []byte("0123456789abcdef") // 16 字节，且刻意不等于零 IV
	want := []byte(`{"msgid":"m2","msgtype":"text","text":{"content":"群消息"}}`)
	item := encryptArchivePlain(t, &priv.PublicKey, nil, iv, want)
	if item.EncryptParam == "" {
		t.Fatal("加密器没把自定义 IV 放进 encrypt_param，用例失效")
	}
	got, err := DecryptArchiveItem(priv, item)
	if err != nil {
		t.Fatalf("自定义 IV 解密失败: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("自定义 IV 明文不一致: got %q want %q", got, want)
	}
	// 反向：把 IV 丢掉退回零 IV。CBC 下错 IV 只毁首个分组，所以明文不一定报错，
	// 但**一定不等于原文**——用它证明 encrypt_param 真进了密算，而不是被忽略。
	itemNoIV := item
	itemNoIV.EncryptParam = ""
	if got2, err2 := DecryptArchiveItem(priv, itemNoIV); err2 == nil && bytes.Equal(got2, want) {
		t.Fatal("去掉 encrypt_param 仍解出同一份明文，说明该字段被忽略")
	}
}

// TestDecryptArchiveItemBadIVLength 非法 IV 长度要报错，不能 panic（外部输入）。
func TestDecryptArchiveItemBadIVLength(t *testing.T) {
	priv := mustArchiveKeyPair(t)
	item := encryptArchivePlain(t, &priv.PublicKey, nil, nil, []byte(`{"msgid":"m3"}`))
	item.EncryptParam = base64.StdEncoding.EncodeToString([]byte("short"))
	_, err := DecryptArchiveItem(priv, item)
	if err == nil || !strings.Contains(err.Error(), "长度须 16") {
		t.Fatalf("IV 长度非法应报明确错误，实得: %v", err)
	}
}

// TestDecryptArchiveItemEnvelopeGuardrails 信封残缺/编码错/私钥未配的负向集合。
func TestDecryptArchiveItemEnvelopeGuardrails(t *testing.T) {
	priv := mustArchiveKeyPair(t)
	ok := encryptArchivePlain(t, &priv.PublicKey, nil, nil, []byte(`{"msgid":"m4"}`))
	cases := []struct {
		name string
		item ArchiveCipherItem
		priv *rsa.PrivateKey
		want string
	}{
		{"nil私钥", ok, nil, "私钥未配置"},
		{"缺随机密钥", func() ArchiveCipherItem { x := ok; x.EncryptRandomKey = "  "; return x }(), priv, "缺 encrypt_random_key"},
		{"缺正文", func() ArchiveCipherItem { x := ok; x.EncryptChatMsg = ""; return x }(), priv, "缺 encrypt_random_key/encrypt_chat_msg"},
		{"随机密钥非base64", func() ArchiveCipherItem { x := ok; x.EncryptRandomKey = "!!!not-base64!!!"; return x }(), priv, "base64 解码失败"},
		{"正文非base64", func() ArchiveCipherItem { x := ok; x.EncryptChatMsg = "###"; return x }(), priv, "encrypt_chat_msg base64"},
		{"IV非base64", func() ArchiveCipherItem { x := ok; x.EncryptParam = "@@@"; return x }(), priv, "encrypt_param(IV) base64"},
	}
	for _, tc := range cases {
		_, err := DecryptArchiveItem(tc.priv, tc.item)
		if err == nil {
			t.Fatalf("%s: 应报错却成功", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: 错误文案应含 %q，实得 %v", tc.name, tc.want, err)
		}
	}
}

// TestAesCBCDecryptPKCS7PaddingNegative 填充校验负向集：这些都吃外部密文字节，
// 少一条判据就是 panic（500）或把错钥解出的乱码当明文。
func TestAesCBCDecryptPKCS7PaddingNegative(t *testing.T) {
	key := make([]byte, 32)
	badKey := make([]byte, 31) // 非 16/24/32
	for i := range key {
		key[i] = byte(i)
	}
	copy(badKey, key[:31])

	// blockPayload 把裸字节对齐到 AES 分组长度——CryptBlocks 对非整块输入是 panic 而不是报错，
	// 负向用例造的是"填充非法"，不能自己先炸在填充对齐上。
	blockPayload := func(b []byte) []byte {
		t.Helper()
		if len(b)%aesBlockSize != 0 {
			t.Fatalf("用例造的载荷 %d 字节非整块，请补到 16 的倍数", len(b))
		}
		return b
	}
	// 造一段"明文 + 指定填充"的密文，便于精确控制填充字节
	encryptWithPad := func(payload []byte) []byte {
		block, err := aes.NewCipher(key)
		if err != nil {
			t.Fatalf("分组器构造失败: %v", err)
		}
		out := make([]byte, len(payload))
		cipher.NewCBCEncrypter(block, archiveZeroIV).CryptBlocks(out, payload)
		return out
	}

	cases := []struct {
		name  string
		key   []byte
		iv    []byte
		blob  []byte
		want  string // 非空=期望错误子串；空=期望解密成功并比对 plain
		plain string
	}{
		{"空密文", key, archiveZeroIV, nil, "不是块大小的整数倍", ""},
		{"长度非块倍数", key, archiveZeroIV, bytes.Repeat([]byte{7}, 17), "不是块大小的整数倍", ""},
		{"密钥长度非法", badKey, archiveZeroIV, bytes.Repeat([]byte{7}, 16), "密钥长度非法", ""},
		{"IV长度非法", key, []byte("short"), bytes.Repeat([]byte{7}, 16), "IV 长度须 16", ""},
		{"填充字节为0", key, archiveZeroIV, encryptWithPad(blockPayload(append(bytes.Repeat([]byte{'a'}, 15), 0))), "填充长度字节非法", ""},
		{"填充字节超界", key, archiveZeroIV, encryptWithPad(blockPayload(append(bytes.Repeat([]byte{'a'}, 15), 17))), "填充长度字节非法", ""},
		{"填充字节不一致", key, archiveZeroIV, encryptWithPad(blockPayload(append(bytes.Repeat([]byte{'a'}, 13), 'b', 'b', 3))), "填充字节不一致", ""},
		{"填充恰好整块", key, archiveZeroIV, encryptWithPad(blockPayload(append(bytes.Repeat([]byte{'a'}, 16), bytes.Repeat([]byte{16}, 16)...))), "", "aaaaaaaaaaaaaaaa"},
	}
	for _, tc := range cases {
		got, err := aesCBCDecryptPKCS7(tc.key, tc.iv, tc.blob)
		if tc.want == "" {
			if err != nil {
				t.Fatalf("%s: 合法整块填充应解出明文，实得错误 %v", tc.name, err)
			}
			// 整块填充（pad=16）是 PKCS7 的合法形态：内容长度必须是 32-16=16，多留或少削都是 bug
			if string(got) != tc.plain {
				t.Fatalf("%s: 明文应为 %q，实得 %q", tc.name, tc.plain, got)
			}
			continue
		}
		if err == nil {
			t.Fatalf("%s: 应报错却解出 %q", tc.name, got)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: 错误应含 %q，实得 %v", tc.name, tc.want, err)
		}
	}
}

// TestParseArchivePrivateKeyFormats 私钥解析要同时吃 PKCS#1 与 PKCS#8，
// 并把"粘错格式"与"根本不是 RSA"分档报清楚（文案误导会让人去重新生成密钥，历史存档就此永久解不开）。
func TestParseArchivePrivateKeyFormats(t *testing.T) {
	priv := mustArchiveKeyPair(t)
	pkcs1 := string(toPemBlock(t, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(priv)))
	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("PKCS#8 序列化失败: %v", err)
	}
	pkcs8 := string(toPemBlock(t, "PRIVATE KEY", pkcs8Bytes))

	for _, s := range []string{pkcs1, pkcs8} {
		got, err := ParseArchivePrivateKey(s)
		if err != nil {
			t.Fatalf("私钥解析失败: %v", err)
		}
		if got.Validate() != nil {
			t.Fatal("解析出的私钥自检失败")
		}
	}
	// 公钥 PEM 被当私钥粘进来：不能成功，也不能误报成"解码失败"
	pubDER, _ := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if _, err := ParseArchivePrivateKey(string(toPemBlock(t, "PUBLIC KEY", pubDER))); err == nil {
		t.Fatal("公钥被当成私钥解析成功")
	} else if strings.Contains(err.Error(), "解码失败") {
		t.Fatalf("应报格式不支持而非解码失败: %v", err)
	}
	if _, err := ParseArchivePrivateKey("随手粘的一段文字"); err == nil || !strings.Contains(err.Error(), "解码失败") {
		t.Fatalf("非 PEM 文本应报解码失败，实得: %v", err)
	}
	// 头部齐、体部不是合法 DER
	if _, err := ParseArchivePrivateKey("-----BEGIN RSA PRIVATE KEY-----\nAAAA\n-----END RSA PRIVATE KEY-----\n"); err == nil {
		t.Fatal("残缺 DER 应报错")
	}
}

// TestGenerateArchiveKeyPairRejectsShortBits 位长白名单：1024 生成出来上传企微后台才报错，
// 在那之前就拒掉，省一次"后台拒收→怀疑账号权限"的排查。
func TestGenerateArchiveKeyPairRejectsShortBits(t *testing.T) {
	if _, _, err := GenerateArchiveKeyPair(1024); err == nil {
		t.Fatal("1024 位应被拒绝")
	}
	if _, _, err := GenerateArchiveKeyPair(0); err == nil {
		t.Fatal("位长 0 应被拒绝")
	}
	privPEM, pubPEM, err := GenerateArchiveKeyPair(2048)
	if err != nil {
		t.Fatalf("2048 位生成失败: %v", err)
	}
	if !strings.Contains(privPEM, "BEGIN RSA PRIVATE KEY") || !strings.Contains(pubPEM, "BEGIN PUBLIC KEY") {
		t.Fatalf("PEM 头不对: priv=%q pub=%q", firstLine(privPEM), firstLine(pubPEM))
	}
	k, err := ParseArchivePrivateKey(privPEM)
	if err != nil {
		t.Fatalf("生成物回解析失败: %v", err)
	}
	if k.N.BitLen() != 2048 {
		t.Fatalf("密钥位长实为 %d", k.N.BitLen())
	}
}

// TestArchivePublicKeyFingerprint 指纹格式（大写冒号分隔 40 十六进制）与稳定性，
// 以及 nil 公钥不 panic——这是运维对着企微后台核对"库里这把是不是后台那把"的唯一依据。
func TestArchivePublicKeyFingerprint(t *testing.T) {
	priv := mustArchiveKeyPair(t)
	fp := ArchivePublicKeyFingerprint(&priv.PublicKey)
	if ArchivePublicKeyFingerprint(&priv.PublicKey) != fp {
		t.Fatal("同一把公钥两次指纹不一致")
	}
	if fp == strings.ToLower(fp) {
		t.Fatal("指纹应为大写十六进制")
	}
	groups := strings.Split(fp, ":")
	if len(groups) != 20 {
		t.Fatalf("SHA-1 指纹应 20 组，实得 %d 组: %s", len(groups), fp)
	}
	for _, g := range groups {
		if len(g) != 2 {
			t.Fatalf("指纹分组长度异常: %s", fp)
		}
	}
	if got := ArchivePublicKeyFingerprint(nil); got != "" {
		t.Fatalf("nil 公钥应返回空串，实得 %q", got)
	}
	// 与独立算出的 SHA-1 对齐（防止只是"格式像指纹"而内容随手拼）
	sum := sha1.Sum(x509.MarshalPKCS1PublicKey(&priv.PublicKey))
	want := strings.ToUpper(fmt.Sprintf("%x", sum))
	if strings.ReplaceAll(fp, ":", "") != want {
		t.Fatalf("指纹内容不等于 SHA-1(MarshalledPKCS1PublicKey)")
	}
	// 两把不同密钥指纹必须不同
	if ArchivePublicKeyFingerprint(&mustArchiveKeyPair(t).PublicKey) == fp {
		t.Fatal("不同公钥指纹相同")
	}
}

// TestNormalizeArchiveMessageTypes 各 msgtype 的抽取口径：文本类出正文、媒体类出 media_id、
// 群聊走 roomid、时间戳按毫秒还原。
func TestNormalizeArchiveMessageTypes(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		m, err := NormalizeArchiveMessage([]byte(`{"msgid":"m1","action":"send","from":"u1","tolist":["ext1"],`+
			`"msgtype":"text","msgtime":1700000000000,"text":{"content":"你好"}}`), 42, 1)
		if err != nil {
			t.Fatalf("text 规整失败: %v", err)
		}
		if m.ContentText != "你好" || m.MsgID != "m1" || m.FromUser != "u1" || m.Seq != 42 {
			t.Fatalf("text 抽取不符: %+v", m)
		}
		if m.ChatType != "single" || m.ChatID != "ext1" {
			t.Fatalf("单聊归属不符: %+v", m)
		}
		if m.MsgTime.UnixMilli() != 1700000000000 {
			t.Fatalf("msgtime 未按毫秒还原: %v", m.MsgTime)
		}
	})
	t.Run("mixed", func(t *testing.T) {
		m, err := NormalizeArchiveMessage([]byte(`{"msgid":"m2","msgtype":"mixed",`+
			`"mixed":{"item":[{"type":"text","text":{"content":"第一段"}},{"type":"image"},{"type":"text","text":{"content":"第二段"}}]}}`), 1, 1)
		if err != nil {
			t.Fatalf("mixed 规整失败: %v", err)
		}
		if m.ContentText != "第一段\n第二段" {
			t.Fatalf("mixed 应拼接文本段并跳过空段，实得 %q", m.ContentText)
		}
	})
	t.Run("media", func(t *testing.T) {
		for _, tc := range []struct{ typ, field string }{{"image", "image"}, {"voice", "voice"}, {"video", "video"}, {"file", "file"}} {
			m, err := NormalizeArchiveMessage([]byte(fmt.Sprintf(`{"msgid":"m-%s","msgtype":"%s","%s":{"sdkfileid":"sdk_%s"}}`, tc.typ, tc.typ, tc.field, tc.typ)), 7, 2)
			if err != nil {
				t.Fatalf("%s 规整失败: %v", tc.typ, err)
			}
			if m.MediaID != "sdk_"+tc.typ {
				t.Fatalf("%s 应抽出 sdkfileid，实得 %q", tc.typ, m.MediaID)
			}
			if m.ContentText != "" {
				t.Fatalf("%s 不应有正文，实得 %q", tc.typ, m.ContentText)
			}
			if m.PublicKeyVer != 2 {
				t.Fatalf("%s 公钥版本未透传: %+v", tc.typ, m)
			}
		}
	})
	t.Run("group", func(t *testing.T) {
		m, err := NormalizeArchiveMessage([]byte(`{"msgid":"m3","from":"u1","roomid":"room9","tolist":["a","b"],"msgtype":"text"}`), 3, 1)
		if err != nil {
			t.Fatalf("群聊规整失败: %v", err)
		}
		if m.ChatType != "group" || m.ChatID != "room9" {
			t.Fatalf("带 roomid 应判群聊，实得 %+v", m)
		}
		if m.Action != "send" {
			t.Fatalf("缺 action 应补 send，实得 %q", m.Action)
		}
		if !m.MsgTime.IsZero() {
			t.Fatalf("缺 msgtime 应留零值（落库 NULL），实得 %v", m.MsgTime)
		}
	})
	t.Run("非JSON", func(t *testing.T) {
		if _, err := NormalizeArchiveMessage([]byte("SDK 报错文本"), 1, 1); err == nil {
			t.Fatal("明文非 JSON 应报错而非静默落空记录")
		}
	})
}
