// 会话存档服务层连库单测（E8-2，2026-09-24）：凭据只落密文、开关前置校验、
// 幂等锚去重、解密失败留痕且游标照样前移、游标单调不回退、SDK 未接入的稳定原因码。
//
// 为什么这些必须连库：本层的价值全在**不变式**上（撞锚不多落一行、失败行不阻塞游标），
// 而这些只有真实 PG 的部分唯一索引能证。用 sqlite 或 mock 会退化成"我自己写的规则我自己认"。
package channel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
	"ai-scrm/pkg/crypto"
)

// newArchiveChannel 建一个带存档私钥的通道（密钥对现场生成，密文列走真实加密链）。
func newArchiveChannel(t *testing.T, tid uint, name string) *model.Channel {
	t.Helper()
	privPEM, _, err := GenerateArchiveKeyPair(2048)
	if err != nil {
		t.Fatalf("生成密钥对失败: %v", err)
	}
	cipherPEM, err := crypto.Encrypt(privPEM)
	if err != nil {
		t.Fatalf("加密私钥失败: %v", err)
	}
	cipherSecret, err := crypto.Encrypt("archive_secret_" + name)
	if err != nil {
		t.Fatalf("加密存档 secret 失败: %v", err)
	}
	ch := model.Channel{
		TenantID:                tid,
		Type:                    model.ChannelTypeWecomApp,
		Name:                    name,
		Status:                  model.ChannelStatusActive,
		ArchiveEnabled:          true,
		ArchiveSecretCipher:     cipherSecret,
		ArchivePrivateKeyCipher: cipherPEM,
		ArchivePublicKeyVer:     1,
	}
	if err := db.DB.Create(&ch).Error; err != nil {
		t.Fatalf("建通道失败: %v", err)
	}
	t.Cleanup(func() {
		db.DB.Where("channel_id = ?", ch.ID).Delete(&model.ChatArchiveRecord{})
		db.DB.Unscoped().Delete(&model.Channel{}, ch.ID)
	})
	// GORM 建完把密文留在结构体里，但 ArchiveSeq 之类默认值要靠重读拿全
	var fresh model.Channel
	if err := db.DB.First(&fresh, ch.ID).Error; err != nil {
		t.Fatalf("重读通道失败: %v", err)
	}
	return &fresh
}

// newArchiveTenant 建测试租户并在结束时清理存档行。
func newArchiveTenant(t *testing.T) uint {
	t.Helper()
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	tid := testutil.CreateTenant(t)
	t.Cleanup(func() {
		db.DB.Where("tenant_id = ?", tid).Delete(&model.ChatArchiveRecord{})
		db.DB.Where("tenant_id = ?", tid).Unscoped().Delete(&model.Channel{})
		testutil.CleanupTenant(t, tid)
	})
	return tid
}

// makeArchiveItem 用通道自己的公钥加一条明文（复刻企微姿势，见 archive_crypto_test.go）。
// seq/ver 必须一起填：加密器只管密文，游标与版本判定吃的是信封上的这两个字段。
func makeArchiveItem(t *testing.T, ch *model.Channel, seq int64, ver int, plain string) ArchiveCipherItem {
	t.Helper()
	priv, _, err := LoadArchivePrivateKey(ch)
	if err != nil {
		t.Fatalf("取私钥失败: %v", err)
	}
	item := encryptArchivePlain(t, &priv.PublicKey, nil, nil, []byte(plain))
	item.Seq = seq
	item.PublicKeyVer = ver
	return item
}

// TestUpdateArchiveConfigSecretsStayCiphered 凭据只以密文落列，且开关有前置校验。
func TestUpdateArchiveConfigSecretsStayCiphered(t *testing.T) {
	tid := newArchiveTenant(t)
	ch := model.Channel{TenantID: tid, Type: model.ChannelTypeWecomApp, Name: "arc_cfg", Status: model.ChannelStatusActive}
	if err := db.DB.Create(&ch).Error; err != nil {
		t.Fatalf("建通道失败: %v", err)
	}
	t.Cleanup(func() { db.DB.Unscoped().Delete(&model.Channel{}, ch.ID) })

	// 没私钥就开开关 → 拒（开了只会产 decrypt_error 空转链路）
	on := true
	if _, err := UpdateArchiveConfig(db.DB, tid, ch.ID, ArchiveConfigInput{Enabled: &on}); !errors.Is(err, ErrArchiveKeyMissing) {
		t.Fatalf("无密钥开启存档应报 ErrArchiveKeyMissing，实得 %v", err)
	}

	// 坏 PEM 必须被拒，而不是存进去等运行期炸
	if _, err := UpdateArchiveConfig(db.DB, tid, ch.ID, ArchiveConfigInput{PrivateKeyPEM: "-----BEGIN RSA PRIVATE KEY-----\nAAAA\n-----END RSA PRIVATE KEY-----"}); err == nil {
		t.Fatal("非法私钥应被拒")
	}

	privPEM, _, err := GenerateArchiveKeyPair(2048)
	if err != nil {
		t.Fatalf("生成密钥对失败: %v", err)
	}
	secret := "wx_arch_secret_PLAINTEXT"
	if _, err := UpdateArchiveConfig(db.DB, tid, ch.ID, ArchiveConfigInput{ArchiveSecret: secret, PrivateKeyPEM: privPEM}); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	var got model.Channel
	if err := db.DB.First(&got, ch.ID).Error; err != nil {
		t.Fatalf("重读失败: %v", err)
	}
	if strings.Contains(got.ArchiveSecretCipher, secret) || got.ArchiveSecretCipher == secret {
		t.Fatal("存档 secret 明文落列了")
	}
	if strings.Contains(got.ArchivePrivateKeyCipher, "PRIVATE KEY") {
		t.Fatal("私钥 PEM 明文落列了")
	}
	if !strings.HasPrefix(got.ArchiveSecretCipher, "gcm1:") || !strings.HasPrefix(got.ArchivePrivateKeyCipher, "gcm1:") {
		t.Fatalf("两列都应是 gcm1: 密文，实得 secret=%q key=%q", got.ArchiveSecretCipher[:8], got.ArchivePrivateKeyCipher[:8])
	}

	// 现在才允许开
	ver := 3
	if _, err := UpdateArchiveConfig(db.DB, tid, ch.ID, ArchiveConfigInput{Enabled: &on, PublicKeyVer: &ver}); err != nil {
		t.Fatalf("配好密钥后开启应成功: %v", err)
	}
	if err := db.DB.First(&got, ch.ID).Error; err != nil {
		t.Fatalf("重读失败: %v", err)
	}
	if !got.ArchiveEnabled || got.ArchivePublicKeyVer != 3 {
		t.Fatalf("开关/版本未落库: %+v", got)
	}
}

// TestIngestArchiveItemsStoresDecryptedRows 正向：文本消息解出后逐字段落库、游标前移。
func TestIngestArchiveItemsStoresDecryptedRows(t *testing.T) {
	tid := newArchiveTenant(t)
	ch := newArchiveChannel(t, tid, "arc_ok")
	item := makeArchiveItem(t, ch, 101, ch.ArchivePublicKeyVer,
		`{"msgid":"arc_m1","action":"send","from":"sales001","tolist":["ext_9"],"msgtype":"text","msgtime":1700000000000,"text":{"content":"哥，周末来试驾"}}`)

	res, err := IngestArchiveItems(db.DB, ch, []ArchiveCipherItem{item})
	if err != nil {
		t.Fatalf("入库失败: %v", err)
	}
	if res.Stored != 1 || res.DecryptFailed != 0 || res.DupSkipped != 0 {
		t.Fatalf("计数不符: %+v", res)
	}
	var rows []model.ChatArchiveRecord
	db.DB.Where("channel_id = ?", ch.ID).Find(&rows)
	if len(rows) != 1 {
		t.Fatalf("应恰好 1 行，实得 %d", len(rows))
	}
	r := rows[0]
	if r.TenantID != tid {
		t.Fatalf("租户归属丢失（C7 红线）: %+v", r)
	}
	if r.MsgID != "arc_m1" || r.ContentText != "哥，周末来试驾" || r.FromUser != "sales001" || r.ChatType != "single" {
		t.Fatalf("字段未按预期落库: %+v", r)
	}
	if r.DecryptError != "" {
		t.Fatalf("成功行不该带 decrypt_error: %q", r.DecryptError)
	}
	if r.MsgTime == nil || r.MsgTime.UnixMilli() != 1700000000000 {
		t.Fatalf("msg_time 未落: %+v", r.MsgTime)
	}
	if r.ToList != `["ext_9"]` {
		t.Fatalf("to_list 应为 JSON 文本，实得 %q", r.ToList)
	}
	var fresh model.Channel
	db.DB.First(&fresh, ch.ID)
	if fresh.ArchiveSeq != 101 {
		t.Fatalf("游标未推进到 101，实得 %d", fresh.ArchiveSeq)
	}
}

// TestIngestArchiveItemsIdempotent 同一条重推不得覆盖已有正文（幂等锚是"跳过"不是"UPSERT"）。
func TestIngestArchiveItemsIdempotent(t *testing.T) {
	tid := newArchiveTenant(t)
	ch := newArchiveChannel(t, tid, "arc_dup")
	plain := `{"msgid":"arc_dup1","msgtype":"text","text":{"content":"原正文"}}`
	item := makeArchiveItem(t, ch, 200, ch.ArchivePublicKeyVer, plain)
	// 第二条同 msgid、不同正文：真实场景是企微重推 + 我们侧某次解错，
	// 若实现是 UPSERT 覆盖，这里"原正文"就没了——证据被毁。
	other := item
	other.Seq = 201
	if _, err := IngestArchiveItems(db.DB, ch, []ArchiveCipherItem{item}); err != nil {
		t.Fatalf("首入库失败: %v", err)
	}
	// 重放游标之前的同 seq：走 StaleSkipped，连库都不用碰
	replay := item
	replay.Seq = 200
	res2, err := IngestArchiveItems(db.DB, ch, []ArchiveCipherItem{replay})
	if err != nil {
		t.Fatalf("重放入库失败: %v", err)
	}
	if res2.StaleSkipped != 1 || res2.Stored != 0 {
		t.Fatalf("游标之前的 seq 应跳过，实得 %+v", res2)
	}
	// 同 msgid 换 seq 再推：撞幂等锚 → DupSkipped，且行数仍为 1
	res3, err := IngestArchiveItems(db.DB, ch, []ArchiveCipherItem{other})
	if err != nil {
		t.Fatalf("重推入库失败（幂等锚撞索引会把整批打死）: %v", err)
	}
	if res3.DupSkipped != 1 || res3.Stored != 0 {
		t.Fatalf("同 msgid 重推应计 DupSkipped，实得 %+v", res3)
	}
	var n int64
	db.DB.Model(&model.ChatArchiveRecord{}).Where("channel_id = ?", ch.ID).Count(&n)
	if n != 1 {
		t.Fatalf("重推后仍应只有 1 行，实得 %d", n)
	}
	var keep model.ChatArchiveRecord
	db.DB.Where("channel_id = ?", ch.ID).First(&keep)
	if keep.ContentText != "原正文" {
		t.Fatalf("正文被覆盖（幂等锚必须是跳过不是 UPSERT）: %q", keep.ContentText)
	}
	if keep.Seq != 200 {
		t.Fatalf("首条 seq 被改掉: %d", keep.Seq)
	}
	// 游标不因重复消息倒退也不因它停在原值之外的地方乱走
	var fresh model.Channel
	db.DB.First(&fresh, ch.ID)
	if fresh.ArchiveSeq < 200 {
		t.Fatalf("游标回退了: %d", fresh.ArchiveSeq)
	}
}

// TestIngestArchiveItemsDecryptFailureStillAdvancesCursor 解不开也建行、游标照样前移。
// 这条是整层最重要的不变式：停住=同一 seq 永久重拉=同步链路卡死。
func TestIngestArchiveItemsDecryptFailureStillAdvancesCursor(t *testing.T) {
	tid := newArchiveTenant(t)
	ch := newArchiveChannel(t, tid, "arc_fail")
	// 用**另一把**公钥加密：真实对应"密钥轮换后旧消息解不开"
	rogueKey := mustArchiveKeyPair(t)
	bad := encryptArchivePlain(t, &rogueKey.PublicKey, nil, nil, []byte(`{"msgid":"arc_bad"}`))
	bad.Seq = 300
	bad.PublicKeyVer = 9
	good := makeArchiveItem(t, ch, 301, ch.ArchivePublicKeyVer, `{"msgid":"arc_good","msgtype":"text","text":{"content":"后面这条还得解得开"}}`)

	res, err := IngestArchiveItems(db.DB, ch, []ArchiveCipherItem{bad, good})
	if err != nil {
		t.Fatalf("一批里有解不开的不应整批失败: %v", err)
	}
	if res.DecryptFailed != 1 || res.Stored != 2 {
		t.Fatalf("应 1 条失败留痕 + 2 行落库，实得 %+v", res)
	}
	var rows []model.ChatArchiveRecord
	db.DB.Where("channel_id = ?", ch.ID).Order("seq ASC").Find(&rows)
	if len(rows) != 2 {
		t.Fatalf("应两行（含留痕行），实得 %d", len(rows))
	}
	if rows[0].Seq != 300 || rows[0].DecryptError == "" {
		t.Fatalf("留痕行不符: %+v", rows[0])
	}
	if rows[0].ContentText != "" {
		t.Fatalf("留痕行不该有正文: %+v", rows[0])
	}
	if rows[0].PublicKeyVer != 9 {
		t.Fatalf("留痕行要留下公钥版本供排查，实得 %d", rows[0].PublicKeyVer)
	}
	if rows[1].MsgID != "arc_good" || rows[1].DecryptError != "" {
		t.Fatalf("解不开的那条把后面堵住了: %+v", rows[1])
	}
	var fresh model.Channel
	db.DB.First(&fresh, ch.ID)
	if fresh.ArchiveSeq != 301 {
		t.Fatalf("游标必须越过解不开的 seq，实得 %d", fresh.ArchiveSeq)
	}
}

// TestIngestArchiveItemsVersionMismatchCountedSeparately 信封公钥版本与配置不符：
// 正文照常落库（它确实读得出来），但版本不符要在状态里可见——混进 decrypt_error
// 会让人以为正文丢了，实际该做的是把 public_key_ver 改对。
func TestIngestArchiveItemsVersionMismatchCountedSeparately(t *testing.T) {
	tid := newArchiveTenant(t)
	ch := newArchiveChannel(t, tid, "arc_ver")
	item := makeArchiveItem(t, ch, 400, 7, `{"msgid":"arc_ver1","msgtype":"text","text":{"content":"版本对不上"}}`)
	res, err := IngestArchiveItems(db.DB, ch, []ArchiveCipherItem{item})
	if err != nil {
		t.Fatalf("入库失败: %v", err)
	}
	if res.DecryptFailed != 0 || res.Stored != 1 {
		t.Fatalf("版本不符不该被判成读不了: %+v", res)
	}
	var r model.ChatArchiveRecord
	db.DB.Where("channel_id = ? AND seq = ?", ch.ID, 400).First(&r)
	if r.ContentText != "版本对不上" || r.DecryptError != "" {
		t.Fatalf("正文应照常落库: %+v", r)
	}
	if r.PublicKeyVer != 7 {
		t.Fatalf("信封版本要如实留存（轮换排查的唯一线索）: %d", r.PublicKeyVer)
	}
	v, err := ArchiveStatus(db.DB, tid, ch.ID)
	if err != nil {
		t.Fatalf("状态查询失败: %v", err)
	}
	if v.VerMismatchTotal != 1 || v.FailedTotal != 0 {
		t.Fatalf("版本不符应单独计数: %+v", v)
	}
	// 配置版本对上之后不再计为不符（同一批数据、只改配置，证明判据用的是配置版本）
	ver7 := 7
	if _, err := UpdateArchiveConfig(db.DB, tid, ch.ID, ArchiveConfigInput{PublicKeyVer: &ver7}); err != nil {
		t.Fatalf("改版本配置失败: %v", err)
	}
	v2, err := ArchiveStatus(db.DB, tid, ch.ID)
	if err != nil {
		t.Fatalf("状态查询失败: %v", err)
	}
	if v2.VerMismatchTotal != 0 {
		t.Fatalf("配置对齐后仍计数: %+v", v2)
	}
}

// TestIngestArchiveItemsEmptyMsgidAllowed 事件类报文（无 msgid）不得撞部分唯一索引。
//
// 这是"部分索引 + ON CONFLICT 靶"最容易写错的一处：漏掉 TargetWhere 时 PG 会直接报
// no unique or exclusion constraint matching，一条事件报文打死整批同步。
func TestIngestArchiveItemsEmptyMsgidAllowed(t *testing.T) {
	tid := newArchiveTenant(t)
	ch := newArchiveChannel(t, tid, "arc_evt")
	i1 := makeArchiveItem(t, ch, 500, ch.ArchivePublicKeyVer, `{"action":"switch","from":"sales001"}`)
	i2 := makeArchiveItem(t, ch, 501, ch.ArchivePublicKeyVer, `{"action":"switch","from":"sales002"}`)
	res, err := IngestArchiveItems(db.DB, ch, []ArchiveCipherItem{i1, i2})
	if err != nil {
		t.Fatalf("两条空 msgid 事件报文应都能落库: %v", err)
	}
	if res.Stored != 2 || res.DupSkipped != 0 {
		t.Fatalf("计数不符: %+v", res)
	}
	var n int64
	db.DB.Model(&model.ChatArchiveRecord{}).Where("channel_id = ? AND msgid = ''", ch.ID).Count(&n)
	if n != 2 {
		t.Fatalf("空 msgid 行应 2 条，实得 %d", n)
	}
}

// TestAdvanceArchiveSeqNeverRewinds 游标只前进：多实例并发同步不得互相拽回。
func TestAdvanceArchiveSeqNeverRewinds(t *testing.T) {
	tid := newArchiveTenant(t)
	ch := newArchiveChannel(t, tid, "arc_cursor")
	if err := AdvanceArchiveSeq(db.DB, ch.ID, 900); err != nil {
		t.Fatalf("推进失败: %v", err)
	}
	if err := AdvanceArchiveSeq(db.DB, ch.ID, 800); err != nil { // 更小的值：另一实例落后的一轮
		t.Fatalf("回退调用不应报错（GREATEST 自然吞掉）: %v", err)
	}
	var fresh model.Channel
	db.DB.First(&fresh, ch.ID)
	if fresh.ArchiveSeq != 900 {
		t.Fatalf("游标被拽回：实得 %d", fresh.ArchiveSeq)
	}
}

// TestSyncArchiveOnceGates SDK 未接入 / 开关关闭 / 正常注入三条分支的区分度。
func TestSyncArchiveOnceGates(t *testing.T) {
	tid := newArchiveTenant(t)
	ch := newArchiveChannel(t, tid, "arc_sync")
	old := ArchiveFetcher
	t.Cleanup(func() { ArchiveFetcher = old })

	ArchiveFetcher = nil
	if _, err := SyncArchiveOnce(context.Background(), db.DB, ch); !errors.Is(err, ErrArchiveSDKNotBuilt) {
		t.Fatalf("未接入 SDK 应报稳定原因码，实得 %v", err)
	}

	// 开关关掉：什么都不该发生，也不报错（ticker 每秒问一次，报错就是刷屏）
	off := *ch
	off.ArchiveEnabled = false
	called := false
	ArchiveFetcher = func(context.Context, *model.Channel, string, string, int64, int) ([]ArchiveCipherItem, error) {
		called = true
		return nil, nil
	}
	if _, err := SyncArchiveOnce(context.Background(), db.DB, &off); err != nil || called {
		t.Fatalf("关闭态不该取数也不该报错: called=%v err=%v", called, err)
	}

	// 注入假实现：afterSeq 必须带上当前游标（否则每轮从头重拉历史）
	var sawSeq int64 = -1
	ArchiveFetcher = func(_ context.Context, _ *model.Channel, secret, privPEM string, afterSeq int64, limit int) ([]ArchiveCipherItem, error) {
		sawSeq = afterSeq
		if secret == "" || !strings.Contains(privPEM, "PRIVATE KEY") || limit <= 0 {
			return nil, errors.New("注入实现收到的凭据不对")
		}
		return []ArchiveCipherItem{
			makeArchiveItem(t, ch, 600, ch.ArchivePublicKeyVer, `{"msgid":"arc_s1","msgtype":"text","text":{"content":"同步来的"}}`),
		}, nil
	}
	res, err := SyncArchiveOnce(context.Background(), db.DB, ch)
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if res.Stored != 1 {
		t.Fatalf("应落 1 行: %+v", res)
	}
	if sawSeq != 0 {
		t.Fatalf("首轮游标应为 0，实得 %d", sawSeq)
	}
	if _, err := SyncArchiveOnce(context.Background(), db.DB, ch); err != nil {
		t.Fatalf("第二轮同步失败: %v", err)
	}
	if sawSeq != 600 {
		t.Fatalf("第二轮必须带上新游标 600，实得 %d", sawSeq)
	}
}

// TestArchiveStatusNoKeyMaterial 状态摘要只报事实，绝不外泄任何密钥材料。
func TestArchiveStatusNoKeyMaterial(t *testing.T) {
	tid := newArchiveTenant(t)
	ch := newArchiveChannel(t, tid, "arc_status")
	item := makeArchiveItem(t, ch, 700, ch.ArchivePublicKeyVer, `{"msgid":"arc_st1","msgtype":"text","msgtime":1700000000000,"text":{"content":"状态"}}`)
	if _, err := IngestArchiveItems(db.DB, ch, []ArchiveCipherItem{item}); err != nil {
		t.Fatalf("入库失败: %v", err)
	}
	bad := encryptArchivePlain(t, &mustArchiveKeyPair(t).PublicKey, nil, nil, []byte(`{"msgid":"arc_st2"}`))
	bad.Seq = 701
	if _, err := IngestArchiveItems(db.DB, ch, []ArchiveCipherItem{bad}); err != nil {
		t.Fatalf("留痕入库失败: %v", err)
	}
	v, err := ArchiveStatus(db.DB, tid, ch.ID)
	if err != nil {
		t.Fatalf("状态查询失败: %v", err)
	}
	if !v.Enabled || !v.KeyConfigured || !v.SecretConfigured {
		t.Fatalf("配置事实不符: %+v", v)
	}
	if v.StoredTotal != 2 || v.FailedTotal != 1 {
		t.Fatalf("计数不符: %+v", v)
	}
	if v.LastMsgAt == nil || v.LastMsgAt.IsZero() {
		t.Fatalf("last_msg_at 应有值: %+v", v)
	}
	if v.Fingerprint == "" || strings.Contains(v.Fingerprint, "PRIVATE") {
		t.Fatalf("指纹应存在且不含密钥材料: %q", v.Fingerprint)
	}
	if v.CursorSeq != 701 {
		t.Fatalf("游标摘要不符: %+v", v)
	}
	// 跨租户查不到（不回显存在性差别）
	if _, err := ArchiveStatus(db.DB, tid+99999, ch.ID); !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("跨租户应 ErrChannelNotFound，实得 %v", err)
	}
}

// TestClipArchiveErrorKeepsUTF8 错误原因截断按字符不按字节（中文截半等于给库里塞坏 UTF-8）。
func TestClipArchiveErrorKeepsUTF8(t *testing.T) {
	long := "存档解密失败：" + strings.Repeat("中文错误详情", 100)
	got := clipArchiveError(errors.New(long))
	if len([]rune(got)) > 241 {
		t.Fatalf("截断后仍超长：%d 字符", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("超长应带省略号: %q", got)
	}
	if strings.ContainsAny(got, "\n") {
		t.Fatal("换行应压平（列值会被日志/CSV 直接消费）")
	}
	if clipArchiveError(nil) != "" {
		t.Fatal("nil 错误应得空串")
	}
	if clipArchiveError(errors.New("短")) != "短" {
		t.Fatal("未超长应原样")
	}
}

// TestIngestArchiveItemsRequiresKey 没配私钥时整轮明确报错（不静默返回 0 行假装成功）。
func TestIngestArchiveItemsRequiresKey(t *testing.T) {
	tid := newArchiveTenant(t)
	bare := model.Channel{TenantID: tid, Type: model.ChannelTypeWecomApp, Name: "arc_nokey", Status: model.ChannelStatusActive, ArchiveEnabled: true}
	if err := db.DB.Create(&bare).Error; err != nil {
		t.Fatalf("建通道失败: %v", err)
	}
	t.Cleanup(func() { db.DB.Unscoped().Delete(&model.Channel{}, bare.ID) })
	if _, err := IngestArchiveItems(db.DB, &bare, []ArchiveCipherItem{{Seq: 1}}); !errors.Is(err, ErrArchiveKeyMissing) {
		t.Fatalf("应报 ErrArchiveKeyMissing，实得 %v", err)
	}
	// 空批次不该报错也不该碰私钥（ticker 空轮是常态）
	if res, err := IngestArchiveItems(db.DB, &bare, nil); err != nil || res.Stored != 0 {
		t.Fatalf("空批次应静默通过: %+v err=%v", res, err)
	}
	var n int64
	db.DB.Model(&model.ChatArchiveRecord{}).Where("channel_id = ?", bare.ID).Count(&n)
	if n != 0 {
		t.Fatalf("失败路径不得留脏行: %d", n)
	}
	_ = time.Now()
}
