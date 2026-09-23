// 会话存档解密链（E8，2026-09-24）：把企微推来的密文消息还原成可读报文的纯函数层。
//
// 企微「会话内容存档」的加解密姿势和通道收发那套（EncodingAESKey/AES-GCM）**完全不是一回事**，
// 别指望复用 pkg/wxcrypt：
//  1. 企业自己生成 RSA 密钥对，**公钥上传企微后台**、私钥留在我们这里；
//  2. 企微用这把公钥加密一条随机的 AES 密钥，Base64 后放在 encrypt_random_key 里；
//  3. 消息正文用那把 AES 密钥做 AES-256-CBC + PKCS7，再 Base64 放在 encrypt_chat_msg 里。
//
// 两个必须写下来的坑（都是社区实现反复踩的地方）：
//   - RSA 解出来的不是 32 字节裸密钥，而是**一串 Base64 文本**，要再解一次 Base64 才是 AES 密钥；
//     少这一步的表现为"解密永远报 padding 错"，看错误根本想不到是编码问题。
//   - IV 按官方样例是**16 个 ASCII '0'**（不是随机、也不在密文前缀里）。个别 SDK 版本会把 IV
//     单独放在 encrypt_param 字段回传，所以这里"给了就用、没给用零 IV"，两条路都留测试。
//
// 为什么这层是纯函数：拉取要靠官方 C SDK（依赖存档许可 + 商用企业身份），本机/CI 都跑不了；
// 但**解什么、解不开怎么留痕**与凭据无关，可以完整测。把可测的部分和不可测的部分切开，
// 是这批唯一能保质交付的方式（另一条路是写一堆只能等人接入才跑得动的代码）。
package channel

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
)

// archiveZeroIV 官方样例的固定 IV：16 个字符 '0'。
var archiveZeroIV = []byte("0000000000000000")

// ArchiveCipherItem 一条待解密的存档信封（对应 SDK GetChatData 返回数组的一项）。
type ArchiveCipherItem struct {
	PublicKeyVer     int    `json:"publickey_ver"`      // 用哪一版公钥加的密
	EncryptRandomKey string `json:"encrypt_random_key"` // Base64(RSA(aesKey))
	EncryptChatMsg   string `json:"encrypt_chat_msg"`   // Base64(AES-CBC(正文))
	EncryptParam     string `json:"encrypt_param"`      // 可选：个别 SDK 版本单独回传的 IV（Base64）
	Seq              int64  `json:"seq"`                // 存档序号（SDK 侧字段）
}

// ArchiveMessage 解密并规整后的一条存档消息（落库与查询用的形态）。
type ArchiveMessage struct {
	MsgID        string    `json:"msgid"`
	Seq          int64     `json:"seq"`
	PublicKeyVer int       `json:"public_key_ver"`
	BizType      string    `json:"biz_type"`    // business|system
	Action       string    `json:"action"`      // send|recall|switch
	FromUser     string    `json:"from_user"`   // 发送者（企微 userid / 外部联系人 id）
	SenderName   string    `json:"sender_name"` // 发送者显示名
	ToList       []string  `json:"to_list"`     // 接收者列表
	ChatType     string    `json:"chat_type"`   // single|group（带 roomid 即群聊）
	ChatID       string    `json:"chatid"`
	MsgType      string    `json:"msg_type"`
	ContentText  string    `json:"content_text"` // 文本类正文（非文本类为空）
	MediaID      string    `json:"media_id"`     // 媒体类 sdkfileid（下载须许可+SDK）
	MsgTime      time.Time `json:"msg_time"`     // 零值=企微没给或给的非法时间戳，落库为 NULL
}

// GenerateArchiveKeyPair 生成企业存档用的 RSA 密钥对，返回 PEM（私钥 PKCS#1、公钥 PKIX）。
//
// 位长只准 2048/4096：企微后台只接受这两档且要求 ≥2048；1024 生成出来上传会直接报错，
// 与其让人在后台试错，不如在这里就拒。
func GenerateArchiveKeyPair(bits int) (privPEM string, pubPEM string, err error) {
	if bits != 2048 && bits != 4096 {
		return "", "", fmt.Errorf("存档密钥位长只准 2048 或 4096（实得 %d），企微后台拒绝更短的密钥", bits)
	}
	key, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		return "", "", fmt.Errorf("生成 RSA 密钥对失败: %w", err)
	}
	privBlock := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", "", fmt.Errorf("公钥序列化失败: %w", err)
	}
	pubBlock := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	return string(privBlock), string(pubBlock), nil
}

// ParseArchivePrivateKey 解析私钥 PEM，兼容 PKCS#1（我们自己生成的形态）与 PKCS#8（运维手搓的形态）。
//
// 只解一种的代价很实在：粘错格式时报"非法私钥"，人第一反应是"密钥坏了"去重新生成一对，
// 而**重新生成等于历史存档永久解不开**（旧密文是旧公钥加的密）。这种把人往绝路上引的
// 误导性失败必须堵住——所以两种格式都吃，并且错误文案直接说清该检查什么。
func ParseArchivePrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(pemStr)))
	if block == nil {
		return nil, errors.New("私钥 PEM 解码失败（确认是否粘全了 -----BEGIN/END----- 两行）")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		if verr := k.Validate(); verr != nil {
			return nil, fmt.Errorf("私钥自检失败: %w", verr)
		}
		return k, nil
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("私钥格式不支持（须 RSA PKCS#1 或 PKCS#8 PEM）")
	}
	rk, ok := key.(*rsa.PrivateKey)
	if !ok || rk == nil {
		return nil, errors.New("PEM 里没有 RSA 私钥（可能是 ECC 或其它算法）")
	}
	if verr := rk.Validate(); verr != nil {
		return nil, fmt.Errorf("私钥自检失败: %w", verr)
	}
	return rk, nil
}

// ArchivePublicKeyFingerprint 公钥 SHA-1 指纹（大写、冒号分隔）。
//
// 用途很窄但很值：企微后台「会话存档」页显示当前生效公钥的指纹，运维配完密钥对需要确认
// "我们库里这把就是后台那把"。没有这一步时配错密钥的表现为"存档一条都解不开"，
// 而排查会先怀疑许可、网络、SDK——一条能对着看的指纹省掉半天。
func ArchivePublicKeyFingerprint(pub *rsa.PublicKey) string {
	if pub == nil {
		return ""
	}
	sum := sha1.Sum(x509.MarshalPKCS1PublicKey(pub))
	hexStr := strings.ToUpper(fmt.Sprintf("%x", sum))
	var parts []string
	for i := 0; i < len(hexStr); i += 2 {
		parts = append(parts, hexStr[i:i+2])
	}
	return strings.Join(parts, ":")
}

// DecryptArchiveItem 解一条存档密文，返回**未规整的原始明文 JSON 字节**。
//
// 解不开（私钥与 publickey_ver 不匹配、信封残缺、密文被截）一律返回错误，由调用方落一条
// decrypt_error 留痕行并把游标推过去。注意**不要在这里重试**：解不开的密文永远解不开，
// 重试只会白烧 CPU 并把同步链路卡在同一 seq 上。
func DecryptArchiveItem(priv *rsa.PrivateKey, item ArchiveCipherItem) ([]byte, error) {
	if priv == nil {
		return nil, errors.New("存档私钥未配置，无法解密")
	}
	if strings.TrimSpace(item.EncryptRandomKey) == "" || strings.TrimSpace(item.EncryptChatMsg) == "" {
		return nil, errors.New("存档信封缺 encrypt_random_key/encrypt_chat_msg")
	}
	rsaBlob, err := base64.StdEncoding.DecodeString(strings.TrimSpace(item.EncryptRandomKey))
	if err != nil {
		return nil, fmt.Errorf("encrypt_random_key base64 解码失败: %w", err)
	}
	// RSA 私解出来的是一段 Base64 文本（不是裸密钥）——少这一步就是"padding 错"的经典现场
	aesKeyB64, err := rsa.DecryptPKCS1v15(nil, priv, rsaBlob)
	if err != nil {
		return nil, fmt.Errorf("encrypt_random_key RSA 解密失败（私钥与 publickey_ver=%d 不匹配？）: %w", item.PublicKeyVer, err)
	}
	aesKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(aesKeyB64)))
	if err != nil {
		return nil, fmt.Errorf("AES 密钥 base64 解码失败: %w", err)
	}
	msgBlob, err := base64.StdEncoding.DecodeString(strings.TrimSpace(item.EncryptChatMsg))
	if err != nil {
		return nil, fmt.Errorf("encrypt_chat_msg base64 解码失败: %w", err)
	}
	iv := archiveZeroIV
	if strings.TrimSpace(item.EncryptParam) != "" {
		// 个别 SDK 版本把 IV 单独回传：给了就用给的，没给按官方样例的零 IV
		got, ierr := base64.StdEncoding.DecodeString(strings.TrimSpace(item.EncryptParam))
		if ierr != nil {
			return nil, fmt.Errorf("encrypt_param(IV) base64 解码失败: %w", ierr)
		}
		if len(got) != 16 {
			return nil, fmt.Errorf("encrypt_param(IV) 长度须 16 字节，实得 %d", len(got))
		}
		iv = got
	}
	return aesCBCDecryptPKCS7(aesKey, iv, msgBlob)
}

// aesBlockSize AES 分组长度（PKCS7 填充校验的上界）。
const aesBlockSize = 16

// aesCBCDecryptPKCS7 AES-256-CBC 解密并去 PKCS7 填充。
//
// 密钥长度只认 16/24/32（企微存档实际是 32）；填充字节非法时报明确错误而不是 panic——
// 这一步吃的是**外部输入**，越界的 padLen 直接切片会把 500 抛出来。
func aesCBCDecryptPKCS7(key, iv, blob []byte) ([]byte, error) {
	if len(blob) == 0 || len(blob)%aesBlockSize != 0 {
		return nil, fmt.Errorf("密文长度 %d 不是块大小的整数倍（数据被截或编码不对）", len(blob))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("存档 AES 密钥长度非法: %w", err)
	}
	if len(iv) != block.BlockSize() {
		return nil, fmt.Errorf("IV 长度须 %d 字节", block.BlockSize())
	}
	out := make([]byte, len(blob))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, blob)
	plain, err := stripPKCS7(out)
	if err != nil {
		return nil, fmt.Errorf("明文填充非法（密钥不对或报文被改）: %w", err)
	}
	return plain, nil
}

// stripPKCS7 去掉 PKCS7 填充，逐条校验填充字节（含全零填充这一下游攻击面）。
func stripPKCS7(buf []byte) ([]byte, error) {
	n := len(buf)
	if n == 0 {
		return nil, errors.New("明文为空")
	}
	pad := int(buf[n-1])
	if pad == 0 || pad > aesBlockSize {
		return nil, fmt.Errorf("填充长度字节非法: %d", pad)
	}
	if subtle.ConstantTimeCompare(buf[n-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) != 1 {
		// 不逐字节短路比较：填充错误是可被观测的侧信道（padding oracle 的标准姿势）
		return nil, errors.New("填充字节不一致")
	}
	return buf[:n-pad], nil
}

// NormalizeArchiveMessage 把明文 JSON 规整成落库形态（纯函数，不碰库）。
//
// 企微的正文结构按 msgtype 各不相同（text.content / mixed.itemlist / image.sdkfileid…），
// 这里只保证**文本类抽出正文、媒体类抽出 media_id**，其余类型原样留 msg_type 让前端显示
// "该类型暂不支持预览"。不在后端猜前端文案。
func NormalizeArchiveMessage(plain []byte, seq int64, publicKeyVer int) (ArchiveMessage, error) {
	var raw struct {
		MsgID   string   `json:"msgid"`
		Act     string   `json:"action"`
		From    string   `json:"from"`
		Tolist  []string `json:"tolist"`
		RoomID  string   `json:"roomid"`
		MsgType string   `json:"msgtype"`
		MsgTime int64    `json:"msgtime"` // 毫秒
		Text    struct {
			Content string `json:"content"`
		} `json:"text"`
		Mixed struct {
			Item []struct {
				Type string `json:"type"`
				Text struct {
					Content string `json:"content"`
				} `json:"text"`
			} `json:"item"`
		} `json:"mixed"`
		Image struct {
			SDKFileID string `json:"sdkfileid"`
		} `json:"image"`
		Voice struct {
			SDKFileID string `json:"sdkfileid"`
		} `json:"voice"`
		Video struct {
			SDKFileID string `json:"sdkfileid"`
		} `json:"video"`
		File struct {
			SDKFileID string `json:"sdkfileid"`
			FileName  string `json:"filename"`
		} `json:"file"`
		SenderName string `json:"sendername"`
	}
	// 明文不是 JSON 时不能 panic 也不能吞：那说明拿到的根本不是存档正文（例如 SDK 把报错文本
	// 当成一条消息传了进来），报出来比静默落一条空记录有用。
	if err := json.Unmarshal(plain, &raw); err != nil {
		return ArchiveMessage{}, fmt.Errorf("存档明文 JSON 解析失败: %w", err)
	}
	m := ArchiveMessage{
		MsgID:        raw.MsgID,
		Seq:          seq,
		PublicKeyVer: publicKeyVer,
		BizType:      "business",
		Action:       strings.ToLower(strings.TrimSpace(raw.Act)),
		FromUser:     raw.From,
		SenderName:   raw.SenderName,
		ToList:       raw.Tolist,
		MsgType:      raw.MsgType,
	}
	if m.Action == "" {
		m.Action = "send" // 企微事件类报文可缺 action，按最常见的语义补齐
	}
	if raw.RoomID != "" {
		m.ChatType, m.ChatID = "group", raw.RoomID
	} else {
		m.ChatType = "single"
		m.ChatID = strings.Join(raw.Tolist, ",")
	}
	if raw.MsgTime > 0 {
		m.MsgTime = time.UnixMilli(raw.MsgTime)
	}
	switch raw.MsgType {
	case "text":
		m.ContentText = raw.Text.Content
	case "mixed":
		var sb strings.Builder
		for _, it := range raw.Mixed.Item {
			if t := strings.TrimSpace(it.Text.Content); t != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(t)
			}
		}
		m.ContentText = sb.String()
	case "image":
		m.MediaID = raw.Image.SDKFileID
	case "voice":
		m.MediaID = raw.Voice.SDKFileID
	case "video":
		m.MediaID = raw.Video.SDKFileID
	case "file":
		m.MediaID = raw.File.SDKFileID
	}
	return m, nil
}
