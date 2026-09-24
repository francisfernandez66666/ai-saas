// 会话存档端点测试（E8-3，2026-09-24）。
//
// 与 internal/channel 包内测试的分工：那边测裁决与落库（幂等锚、游标、留痕行），
// 这里测**端点承诺**——管理员从 HTTP 看到的东西是不是他说的那样：
//   - 私钥与密文永不出接口（连掩码都不给，掩码会露长度）；
//   - 列表只有摘要，全文只在详情接口出现，且详情读一次留一条审计；
//   - 空态是 [] 不是 null；page_size 回显的是实际生效值；
//   - 跨租户的通道与记录都是 404，且两种情况同形（探测不出"存在但不归你"）；
//   - SDK 没接入是 200 + ok=false + 稳定 reason 码，不是一个红色失败弹窗。
//
// 依赖本地 PostgreSQL（testutil.SetupTestDB，DB 不可用自动 Skip）；不外呼企微端点。
package api

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"ai-scrm/internal/channel"
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// newArchiveAdminRouter 组装存档管理端最小路由（鉴权整组闸在 smoke_perm.sh 覆盖，此处只设租户语境）。
func newArchiveAdminRouter(tid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	stub := func(c *gin.Context) { c.Set("tenant_id", tid) }
	r.GET("/api/v1/admin/channels/:id/archive", stub, GetChannelArchive)
	r.PUT("/api/v1/admin/channels/:id/archive", stub, UpdateChannelArchive)
	r.POST("/api/v1/admin/channels/:id/archive/key", stub, GenerateChannelArchiveKey)
	r.POST("/api/v1/admin/channels/:id/archive/sync", stub, SyncChannelArchive)
	r.GET("/api/v1/admin/channels/:id/archive/records", stub, ListChannelArchiveRecords)
	r.GET("/api/v1/admin/channel-archive/records/:id", stub, GetChannelArchiveRecord)
	return r
}

// archiveSeedChannel 建一条企微自建应用通道（存档列走默认值：开关关、无凭据）。
func archiveSeedChannel(t *testing.T, tid uint, name string) uint {
	t.Helper()
	ch := model.Channel{
		TenantID: tid, Type: model.ChannelTypeWecomApp, Name: name,
		CorpID: "wp_test_corp", Status: model.ChannelStatusActive,
	}
	if err := db.DB.Create(&ch).Error; err != nil { // 字面量显式盖章 TenantID（D6）
		t.Fatalf("建通道失败: %v", err)
	}
	t.Cleanup(func() {
		db.DB.Where("channel_id = ?", ch.ID).Delete(&model.ChatArchiveRecord{})
		db.DB.Unscoped().Delete(&model.Channel{}, ch.ID)
	})
	return ch.ID
}

// archiveDo 发一次请求，回状态码、data 段与原始 body（原始 body 是"有没有泄露密钥材料"的唯一判据）
func archiveDo(t *testing.T, r *gin.Engine, method, path, body string) (int, map[string]any, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	raw := rec.Body.String()
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return rec.Code, nil, raw
	}
	if env.Data == nil {
		env.Data = map[string]any{}
	}
	return rec.Code, env.Data, raw
}

// archiveMustStatus 读一次存档状态并断言结构在（响应缺 status 是"200 但页面全空"的形态）
func archiveMustStatus(t *testing.T, r *gin.Engine, cid uint) map[string]any {
	t.Helper()
	data := archiveMustData(t, r, http.MethodGet, fmt.Sprintf("/api/v1/admin/channels/%d/archive", cid), "")
	st, ok := data["status"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺 status 字段: %+v", data)
	}
	return st
}

// archiveMustData 断言 200 并回 data 段
func archiveMustData(t *testing.T, r *gin.Engine, method, path, body string) map[string]any {
	t.Helper()
	code, data, raw := archiveDo(t, r, method, path, body)
	if code != http.StatusOK {
		t.Fatalf("%s %s 应 200，实得 %d body=%s", method, path, code, raw)
	}
	return data
}

// archiveEncryptor 独立实现一遍企微侧加密（**不复用被测代码的 helper**）：
// 与被测解密对得上才算真验过链路口径，自己加密自己解只能证明代码没写反。
func archiveEncryptor(t *testing.T, pubPEM string, ver int, seq int64, plain []byte) channel.ArchiveCipherItem {
	t.Helper()
	block, _ := pem.Decode([]byte(pubPEM))
	if block == nil {
		t.Fatalf("公钥 PEM 解不开: %s", pubPEM[:40])
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("公钥解析失败: %v", err)
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("公钥不是 RSA: %T", pub)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("取随机 AES 密钥失败: %v", err)
	}
	rk, err := rsa.EncryptPKCS1v15(rand.Reader, rsaPub, []byte(base64.StdEncoding.EncodeToString(key)))
	if err != nil {
		t.Fatalf("RSA 加密失败: %v", err)
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("AES 装配失败: %v", err)
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(padded))
	cbcEncrypt(out, padded, b, []byte("0000000000000000"))
	return channel.ArchiveCipherItem{
		PublicKeyVer:     ver,
		Seq:              seq,
		EncryptRandomKey: base64.StdEncoding.EncodeToString(rk),
		EncryptChatMsg:   base64.StdEncoding.EncodeToString(out),
	}
}

// cbcEncrypt 测试侧 CBC 加密（与标准库 CryptBlocks 同义，逐块异或链）。
func cbcEncrypt(dst, src []byte, blk cipher.Block, iv []byte) {
	prev := iv
	for off := 0; off < len(src); off += blk.BlockSize() {
		x := make([]byte, blk.BlockSize())
		for i := range x {
			x[i] = src[off+i] ^ prev[i]
		}
		blk.Encrypt(dst[off:off+blk.BlockSize()], x)
		prev = dst[off : off+blk.BlockSize()]
	}
}

// archiveSeedMessagePlain 造一条企微存档明文报文（字段形态照官方 business/msg 样例）。
func archiveSeedMessagePlain(msgID, from, text string, seq int64) []byte {
	msg := map[string]any{
		"msgid":         msgID,
		"action":        "send",
		"from":          from,
		"tolist":        []string{"ext_alice"},
		"msgtime":       time.Now().Add(-time.Duration(seq) * time.Second).UnixMilli(),
		"msgtype":       "text",
		"publickey_ver": 1,
		"text":          map[string]any{"content": text},
	}
	b, _ := json.Marshal(msg)
	return b
}

// TestArchiveEndpointStatusShape 状态端点出厂形态：一切默认值 + SDK 未接入给出稳定 reason 码。
func TestArchiveEndpointStatusShape(t *testing.T) {
	tid := archiveTenant(t, "arch_ep1")
	r := newArchiveAdminRouter(tid)
	cid := archiveSeedChannel(t, tid, "存档通道A")
	st := archiveMustStatus(t, r, cid)
	if st["enabled"] != false {
		t.Fatalf("存档开关出厂必须为 false（默认开=忘了关就把客户聊天记录抄进我们库），实得 %v", st["enabled"])
	}
	if st["key_configured"] != false || st["secret_configured"] != false {
		t.Fatalf("新通道不该被报成已配凭据: %v", st)
	}
	if st["fetcher_ready"] != false {
		t.Fatalf("官方 SDK 未编入时 fetcher_ready 必须 false: %v", st)
	}
	if st["sdk_reason"] != channel.ErrArchiveSDKNotBuilt.Error() {
		t.Fatalf("sdk_reason 应为稳定码 %q，实得 %v", channel.ErrArchiveSDKNotBuilt.Error(), st["sdk_reason"])
	}
	if v, ok := st["stored_total"].(float64); !ok || v != 0 {
		t.Fatalf("stored_total 应为 0，实得 %v", st["stored_total"])
	}
}

// archiveTenant 建单测租户（语义码互不相同，避免复用成同一家致跨租用例退化成 A/A）
func archiveTenant(t *testing.T, code string) uint {
	t.Helper()
	testutil.SetupTestDB(t)
	return testutil.CreateTenantCode(t, code)
}

// TestArchiveEndpointKeyNeverEchoes 密钥生成与凭据写入：私钥只落库，一次也不出接口。
func TestArchiveEndpointKeyNeverEchoes(t *testing.T) {
	tid := archiveTenant(t, "arch_ep2")
	r := newArchiveAdminRouter(tid)
	cid := archiveSeedChannel(t, tid, "存档通道B")

	code, data, raw := archiveDo(t, r, http.MethodPost, fmt.Sprintf("/api/v1/admin/channels/%d/archive/key", cid), `{"bits":2048,"public_key_ver":3}`)
	if code != http.StatusOK {
		t.Fatalf("生成密钥对应 200，实得 %d body=%s", code, raw)
	}
	if strings.Contains(raw, "BEGIN RSA PRIVATE KEY") {
		t.Fatalf("响应里出现了私钥 PEM——泄露即等于交出全部客户聊天记录明文: %s", raw)
	}
	if pemStr, _ := data["public_key_pem"].(string); !strings.Contains(pemStr, "BEGIN PUBLIC KEY") {
		t.Fatalf("响应应含公钥 PEM（管理员要粘去企微后台），实得 %q", data["public_key_pem"])
	}
	if fp, _ := data["fingerprint"].(string); !strings.Contains(fp, ":") {
		t.Fatalf("fingerprint 应为冒号分隔指纹，实得 %q", fp)
	}
	if v, _ := data["public_key_ver"].(float64); v != 3 {
		t.Fatalf("public_key_ver 应按入参落 3，实得 %v", data["public_key_ver"])
	}
	if data["private_key_echo"] != false {
		t.Fatalf("必须显式回 private_key_echo=false，免得管理员以为页面漏显示: %v", data["private_key_echo"])
	}

	st := archiveMustStatus(t, r, cid)
	if st["key_configured"] != true {
		t.Fatalf("生成后应报 key_configured=true: %v", st)
	}
	if st["fingerprint"] == "" {
		t.Fatalf("状态里应带公钥指纹供比对企微后台: %v", st)
	}

	// 坏 PEM 是"入参不对"（400 + 稳定码），不是"环境坏了"（5xx）：文案指错方向会让人去重新生成一把，
	// 而重新生成会让历史存档永久解不开。
	code, _, raw = archiveDo(t, r, http.MethodPut, fmt.Sprintf("/api/v1/admin/channels/%d/archive", cid),
		`{"private_key_pem":"-----BEGIN RSA PRIVATE KEY-----\n bm5hLW5vdC1hLWtleQ== \n-----END RSA PRIVATE KEY-----\n"}`)
	if code != http.StatusBadRequest || !strings.Contains(raw, "archive_key_format") {
		t.Fatalf("坏私钥应 400 + archive_key_format，实得 %d body=%s", code, raw)
	}
	// 明文 secret 只进密文列：响应里不得回显，也不得出现密文本体。
	if _, err := channel.UpdateArchiveConfig(db.DB, tid, cid, channel.ArchiveConfigInput{ArchiveSecret: "arch_secret_plain"}); err != nil {
		t.Fatalf("直写存档 secret 失败: %v", err)
	}
	st = archiveMustStatus(t, r, cid)
	if st["secret_configured"] != true {
		t.Fatalf("secret 写入后应报 secret_configured=true: %v", st)
	}
	_, _, raw = archiveDo(t, r, http.MethodGet, fmt.Sprintf("/api/v1/admin/channels/%d/archive", cid), "")
	if strings.Contains(raw, "arch_secret_plain") {
		t.Fatalf("明文存档 secret 出现在响应里: %s", raw)
	}
}

// mustJSON 解析原始 body 为 map（失败即 Fatal，把 body 打出来）
func mustJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("响应不是合法 JSON: %v body=%s", err, raw)
	}
	return m
}

// TestArchiveEndpointEnableGate 没密钥就开开关必须被拒（400 + archive_key_missing），开关不被半开。
func TestArchiveEndpointEnableGate(t *testing.T) {
	tid := archiveTenant(t, "arch_ep3")
	r := newArchiveAdminRouter(tid)
	cid := archiveSeedChannel(t, tid, "存档通道C")
	code, _, raw := archiveDo(t, r, http.MethodPut, fmt.Sprintf("/api/v1/admin/channels/%d/archive", cid), `{"enabled":true}`)
	if code != http.StatusBadRequest || !strings.Contains(raw, channel.ErrArchiveKeyMissing.Error()) {
		t.Fatalf("无密钥开开关应 400 + %s，实得 %d body=%s", channel.ErrArchiveKeyMissing.Error(), code, raw)
	}
	if archiveMustStatus(t, r, cid)["enabled"] != false {
		t.Fatalf("被拒的这次不得把开关留下（半开=一条只产 decrypt_error 的链路）")
	}
	// 配好密钥后同一请求应成功，且状态如实翻成已开。
	if _, err := channel.GenerateArchiveKey(db.DB, tid, cid, 2048, 1); err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	code, _, raw = archiveDo(t, r, http.MethodPut, fmt.Sprintf("/api/v1/admin/channels/%d/archive", cid), `{"enabled":true}`)
	if code != http.StatusOK {
		t.Fatalf("有密钥后开开关应 200，实得 %d body=%s", code, raw)
	}
	if archiveMustStatus(t, r, cid)["enabled"] != true {
		t.Fatalf("开关应已置 true")
	}
	// 关开关不需要密钥可用（关掉永远该被允许——这是逃生舱）。
	code, _, raw = archiveDo(t, r, http.MethodPut, fmt.Sprintf("/api/v1/admin/channels/%d/archive", cid), `{"enabled":false}`)
	if code != http.StatusOK {
		t.Fatalf("关开关应 200，实得 %d body=%s", code, raw)
	}
}

// TestArchiveEndpointSyncReasonAndCounts 同步端点：未接入 SDK 给稳定码，接入后给真实计数。
func TestArchiveEndpointSyncReasonAndCounts(t *testing.T) {
	tid := archiveTenant(t, "arch_ep4")
	r := newArchiveAdminRouter(tid)
	cid := archiveSeedChannel(t, tid, "存档通道D")

	// 开关关：不调用取数、不报错，reason=archive_disabled（"没开"与"坏了"是两个事实）。
	code, data, raw := archiveDo(t, r, http.MethodPost, fmt.Sprintf("/api/v1/admin/channels/%d/archive/sync", cid), "")
	if code != http.StatusOK || data["reason"] != "archive_disabled" || data["ok"] != false {
		t.Fatalf("未开存档同步应 200 + archive_disabled，实得 %d body=%s", code, raw)
	}

	// 开了但 SDK 未接入：仍是 200 + 稳定码（Admin 页把它显示成待办引导而非红色失败）。
	if _, err := channel.GenerateArchiveKey(db.DB, tid, cid, 2048, 1); err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	if _, err := channel.UpdateArchiveConfig(db.DB, tid, cid, channel.ArchiveConfigInput{
		ArchiveSecret: "s", Enabled: boolPtr(true),
	}); err != nil {
		t.Fatalf("开存档失败: %v", err)
	}
	code, data, raw = archiveDo(t, r, http.MethodPost, fmt.Sprintf("/api/v1/admin/channels/%d/archive/sync", cid), "")
	if code != http.StatusOK || data["reason"] != channel.ErrArchiveSDKNotBuilt.Error() {
		t.Fatalf("SDK 未接入应 200 + %s，实得 %d body=%s", channel.ErrArchiveSDKNotBuilt.Error(), code, raw)
	}

	// 注入取数实现后走真实一轮：三条信封（其中一条用旧版公钥加的密，必然解不开）→
	// stored=3、decrypt_failed=1、游标推进到最大 seq。
	view, err := channel.GenerateArchiveKey(db.DB, tid, cid, 2048, 2)
	if err != nil {
		t.Fatalf("轮换密钥失败: %v", err)
	}
	items := []channel.ArchiveCipherItem{
		archiveEncryptor(t, view.PublicKeyPEM, 2, 501, archiveSeedMessagePlain("m_501", "zhang", "我要订两台", 501)),
		archiveEncryptor(t, view.PublicKeyPEM, 2, 502, archiveSeedMessagePlain("m_502", "ext_alice", "好的，明天发报价", 502)),
		{PublicKeyVer: 1, Seq: 503, EncryptRandomKey: "cmFuZG9tLW5vLWtleQ==", EncryptChatMsg: "YWJjZA=="},
	}
	var gotLimit int
	prev := channel.ArchiveFetcher
	channel.ArchiveFetcher = func(ctx context.Context, ch *model.Channel, secret, pemStr string, afterSeq int64, limit int) ([]channel.ArchiveCipherItem, error) {
		gotLimit = limit
		if secret == "" || pemStr == "" {
			t.Errorf("取数实现没拿到凭据: secret=%q pem 长度=%d", secret, len(pemStr))
		}
		if afterSeq != 0 {
			return nil, nil // 第二轮起没有新消息
		}
		return items, nil
	}
	t.Cleanup(func() { channel.ArchiveFetcher = prev })

	code, data, raw = archiveDo(t, r, http.MethodPost, fmt.Sprintf("/api/v1/admin/channels/%d/archive/sync", cid), "")
	if code != http.StatusOK || data["ok"] != true {
		t.Fatalf("同步应 200 + ok=true，实得 %d body=%s", code, raw)
	}
	res, _ := data["result"].(map[string]any)
	if res["fetched"].(float64) != 3 || res["stored"].(float64) != 3 {
		t.Fatalf("计数应为 fetched=3 stored=3，实得 %v", res)
	}
	if res["decrypt_failed"].(float64) != 1 {
		t.Fatalf("旧版公钥那条应记 decrypt_failed=1（但仍落库留痕），实得 %v", res)
	}
	if res["max_seq"].(float64) != 503 {
		t.Fatalf("游标应推进到 503，实得 %v", res)
	}
	if gotLimit != 1000 {
		t.Fatalf("单轮上限应传 1000，实得 %d", gotLimit)
	}

	// 再点一次：游标已越过全部 seq，全部判陈旧，不重复落库。
	_, data, raw = archiveDo(t, r, http.MethodPost, fmt.Sprintf("/api/v1/admin/channels/%d/archive/sync", cid), "")
	_ = raw
	if res2, _ := data["result"].(map[string]any); res2["stored"].(float64) != 0 {
		t.Fatalf("第二轮不该再落库: %v", res2)
	}
}

// TestArchiveEndpointRecordsListAndDetail 列表只给摘要、详情给全文、跨租户不可见、空态为 []。
func TestArchiveEndpointRecordsListAndDetail(t *testing.T) {
	tid := archiveTenant(t, "arch_ep5")
	otherTid := archiveTenant(t, "arch_ep6")
	r := newArchiveAdminRouter(tid)
	cid := archiveSeedChannel(t, tid, "存档通道E")
	otherCid := archiveSeedChannel(t, otherTid, "存档通道F")

	// 空态：[] 不是 null（前端直接 map，null 会让整页崩）。
	data := archiveMustData(t, r, http.MethodGet, fmt.Sprintf("/api/v1/admin/channels/%d/archive/records", cid), "")
	if lst, ok := data["list"].([]any); !ok || len(lst) != 0 {
		t.Fatalf("空列表必须是 []，实得 %#v", data["list"])
	}
	if v, _ := data["page_size"].(float64); v != archiveDefaultPageSize {
		t.Fatalf("默认 page_size 应为 %d，实得 %v", archiveDefaultPageSize, data["page_size"])
	}
	// page_size 超上限：回显的是实际生效值，否则前端按原值算出"共 1 页"。
	data = archiveMustData(t, r, http.MethodGet, fmt.Sprintf("/api/v1/admin/channels/%d/archive/records?page_size=1000", cid), "")
	if v, _ := data["page_size"].(float64); v != float64(channel.ArchivePageSizeCap) {
		t.Fatalf("page_size 应被钳到 %d，实得 %v", channel.ArchivePageSizeCap, data["page_size"])
	}
	// 非法时间是 400（静默忽略等于管理员以为筛过了）。
	code, _, raw := archiveDo(t, r, http.MethodGet, fmt.Sprintf("/api/v1/admin/channels/%d/archive/records?since=昨天", cid), "")
	if code != http.StatusBadRequest {
		t.Fatalf("非法 since 应 400，实得 %d body=%s", code, raw)
	}

	view, err := channel.GenerateArchiveKey(db.DB, tid, cid, 2048, 1)
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	longText := "客户原话：" + strings.Repeat("这台车的续航到底多少", 20) // > 120 字，必触发摘要截断
	items := []channel.ArchiveCipherItem{
		archiveEncryptor(t, view.PublicKeyPEM, 1, 601, archiveSeedMessagePlain("m_601", "zhang", longText, 601)),
		archiveEncryptor(t, view.PublicKeyPEM, 1, 602, archiveSeedMessagePlain("m_602", "ext_alice", "明天上午方便到店吗", 602)),
		{PublicKeyVer: 1, Seq: 603, EncryptRandomKey: "cmFuZG9tLW5vLWtleQ==", EncryptChatMsg: "YWJjZA=="},
	}
	if _, err := channel.IngestArchiveItems(db.DB, mustChannel(t, cid), items); err != nil {
		t.Fatalf("入库失败: %v", err)
	}
	// 别人租户的一条：本租户任何接口都不该看得见。
	ov, err := channel.GenerateArchiveKey(db.DB, otherTid, otherCid, 2048, 1)
	if err != nil {
		t.Fatalf("对端生成密钥失败: %v", err)
	}
	if _, err := channel.IngestArchiveItems(db.DB, mustChannel(t, otherCid), []channel.ArchiveCipherItem{
		archiveEncryptor(t, ov.PublicKeyPEM, 1, 701, archiveSeedMessagePlain("m_701", "wang", "隔壁家的机密", 701)),
	}); err != nil {
		t.Fatalf("对端入库失败: %v", err)
	}

	code, data, raw = archiveDo(t, r, http.MethodGet, fmt.Sprintf("/api/v1/admin/channels/%d/archive/records", cid), "")
	if code != http.StatusOK {
		t.Fatalf("名单应 200，实得 %d body=%s", code, raw)
	}
	lst := data["list"].([]any)
	if len(lst) != 3 {
		t.Fatalf("应回 3 行（含留痕行），实得 %d", len(lst))
	}
	if v, _ := data["total"].(float64); v != 3 {
		t.Fatalf("total 应为 3，实得 %v", data["total"])
	}
	// 列表不得含全文：截断的那条正文尾部绝不能出现在 raw body 里。
	if strings.Contains(raw, "续航到底多少续航到底多少") {
		t.Fatalf("列表响应泄露了正文全文: %s", raw)
	}
	if strings.Contains(raw, "隔壁家的机密") {
		t.Fatalf("跨租户存档泄露")
	}
	first := lst[0].(map[string]any) // seq DESC：603 在最前
	if first["seq"].(float64) != 603 {
		t.Fatalf("名单应按 seq 倒序，首行应 603，实得 %v", first["seq"])
	}
	if first["decrypt_error"] == "" {
		t.Fatalf("留痕行的 decrypt_error 应在列表可见（管理员要能筛读不了的是哪些）: %v", first)
	}
	if _, has := first["content_text"]; has {
		t.Fatalf("列表不得序列化 content_text 字段（全文只走详情）: %v", first)
	}
	trunc := lst[1].(map[string]any) // 602
	if trunc["has_full_text"] != true {
		t.Fatalf("有正文的行应报 has_full_text=true: %v", trunc)
	}
	longRow := lst[2].(map[string]any) // 601
	prevText, _ := longRow["text_preview"].(string)
	if !strings.HasSuffix(prevText, "…") {
		t.Fatalf("超 120 字的正文应被摘要截断并补省略号，实得 %q", prevText)
	}
	if r2 := []rune(strings.TrimSuffix(prevText, "…")); len(r2) != 120 {
		t.Fatalf("摘要应恰为 120 字符（按字符不按字节，否则中文劈半个字前端乱码），实得 %d", len(r2))
	}

	// 详情：全文回显 + 读一次留一条审计。
	recID := uint(longRow["id"].(float64))
	detailData := archiveMustData(t, r, http.MethodGet, fmt.Sprintf("/api/v1/admin/channel-archive/records/%d", recID), "")
	rec, _ := detailData["record"].(map[string]any)
	if rec["content_text"] != longText {
		t.Fatalf("详情应回显正文全文")
	}
	// 审计是异步写（writeAuditSimple 起 goroutine，不拖慢读路径），故轮询等而不是立刻数：
	// 立即 Count 会在 goroutine 落库前读到 0，那是一条"看代码没问题但天天红"的假失败。
	auditCount := awaitAuditCount(t, tid, "channel_archive_record_read", fmt.Sprintf("archive_record:%d", recID))
	if auditCount != 1 {
		t.Fatalf("读详情应留一条审计（谁在什么时候看了哪条会话），实得 %d", auditCount)
	}

	// 跨租户详情：同 404 同文案，不回显"存在但不归你"。
	otherID := archiveOtherRecordID(t, otherCid)
	// 正向对照：对端自己读得到这一条。缺了它，下面的 404 可能只是"记录压根没落库"，
	// 隔离护栏就在 0==0 上假绿。
	code, _, raw = archiveDo(t, newArchiveAdminRouter(otherTid), http.MethodGet, fmt.Sprintf("/api/v1/admin/channel-archive/records/%d", otherID), "")
	if code != http.StatusOK || !strings.Contains(raw, "机密") {
		t.Fatalf("对端应读得到自己的记录（正向对照），实得 %d body=%s", code, raw)
	}
	// 本租户读它：404 且不回显任何内容。
	code, _, raw = archiveDo(t, r, http.MethodGet, fmt.Sprintf("/api/v1/admin/channel-archive/records/%d", otherID), "")
	if code != http.StatusNotFound || strings.Contains(raw, "机密") {
		t.Fatalf("跨租户记录应 404 不泄露，实得 %d body=%s", code, raw)
	}
	body404 := mustJSON(t, raw)
	code, _, raw = archiveDo(t, r, http.MethodGet, "/api/v1/admin/channel-archive/records/999999999", "")
	if code != http.StatusNotFound || mustJSON(t, raw)["message"] != body404["message"] {
		t.Fatalf("不存在与跨租户必须同码同形（否则自增 ID 可被用来探测别家留痕行数）: %d body=%s", code, raw)
	}

	// failed=true 只回留痕行；keyword 命中正文子串。
	data = archiveMustData(t, r, http.MethodGet, fmt.Sprintf("/api/v1/admin/channels/%d/archive/records?failed=true", cid), "")
	if lst = data["list"].([]any); len(lst) != 1 {
		t.Fatalf("failed=true 应只回 1 行，实得 %d", len(lst))
	}
	data = archiveMustData(t, r, http.MethodGet, fmt.Sprintf("/api/v1/admin/channels/%d/archive/records?keyword=%s", cid, "到店"), "")
	if v, _ := data["total"].(float64); v != 1 {
		t.Fatalf("keyword=到店 应命中 1 条，实得 %v", data["total"])
	}
	// 关键字里的 % 是 LIKE 通配符，必须当字面量：搜 "100%" 不该命中不含该字样的行。
	// （URL 里裸 % 是残缺转义、ParseQuery 会把整个 query 丢掉，那样 keyword 为空、
	// 断言就在"全部命中"上假绿——必须写成 %25 才是真的把 % 送进后端。）
	data = archiveMustData(t, r, http.MethodGet, fmt.Sprintf("/api/v1/admin/channels/%d/archive/records?keyword=%s", cid, "100%25"), "")
	if v, _ := data["total"].(float64); v != 0 {
		t.Fatalf("keyword 未转义通配符（100%% 命中了 %v 行）", v)
	}
}

// TestArchiveEndpointChannelCrossTenant404 别的租户的通道：存档五个端点一律 404。
func TestArchiveEndpointChannelCrossTenant404(t *testing.T) {
	tid := archiveTenant(t, "arch_ep7")
	otherTid := archiveTenant(t, "arch_ep8")
	cid := archiveSeedChannel(t, otherTid, "存档通道G")
	r := newArchiveAdminRouter(tid)
	paths := []struct{ method, path string }{
		{http.MethodGet, fmt.Sprintf("/api/v1/admin/channels/%d/archive", cid)},
		{http.MethodPut, fmt.Sprintf("/api/v1/admin/channels/%d/archive", cid)},
		{http.MethodPost, fmt.Sprintf("/api/v1/admin/channels/%d/archive/key", cid)},
		{http.MethodPost, fmt.Sprintf("/api/v1/admin/channels/%d/archive/sync", cid)},
		{http.MethodGet, fmt.Sprintf("/api/v1/admin/channels/%d/archive/records", cid)},
	}
	for _, p := range paths {
		code, _, raw := archiveDo(t, r, p.method, p.path, `{"enabled":true,"bits":2048}`)
		if code != http.StatusNotFound {
			t.Fatalf("%s %s 跨租户应 404，实得 %d body=%s", p.method, p.path, code, raw)
		}
	}
	// 且通道确实没被顺手改到：跨租户的 PUT 不能落任何写。
	if st, err := channel.ArchiveStatus(db.DB, otherTid, cid); err != nil || st.Enabled || st.KeyConfigured {
		t.Fatalf("跨租户请求不得改动对方配置: %+v err=%v", st, err)
	}
}

// archiveOtherRecordID 取某通道最新一条存档记录 ID
func archiveOtherRecordID(t *testing.T, cid uint) uint {
	t.Helper()
	var rec model.ChatArchiveRecord
	if err := db.DB.Where("channel_id = ?", cid).Order("id DESC").First(&rec).Error; err != nil {
		t.Fatalf("取对端记录失败: %v", err)
	}
	return rec.ID
}

// mustChannel 按 ID 读通道行（入库函数入参要的是整行）
func mustChannel(t *testing.T, cid uint) *model.Channel {
	t.Helper()
	var ch model.Channel
	if err := db.DB.First(&ch, cid).Error; err != nil {
		t.Fatalf("读通道 %d 失败: %v", cid, err)
	}
	return &ch
}

// boolPtr 取 bool 指针（PUT 入参区分"没传"与"传 false"）
func boolPtr(v bool) *bool { return &v }

// awaitAuditCount 等异步审计落库后回数（最多 ~2s）。
//
// 审计写入是 goroutine（不拖慢读路径），立刻 Count 会在落库前读到 0——
// 那是一条"代码没问题但天天红"的假失败，等到有为止；超时仍为 0 才是真缺陷。
func awaitAuditCount(t *testing.T, tid uint, action, resource string) int64 {
	t.Helper()
	var n int64
	for i := 0; i < 40; i++ {
		db.DB.Model(&model.TenantAuditLog{}).
			Where("tenant_id = ? AND action = ? AND resource = ?", tid, action, resource).
			Count(&n)
		if n > 0 {
			return n
		}
		time.Sleep(50 * time.Millisecond)
	}
	return n
}
