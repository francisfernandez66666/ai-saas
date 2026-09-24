// 会话存档配置与入库服务层（E8-2，2026-09-24）。
//
// 这一层只管三件事：**存凭据**（存档 secret 与企业自建 RSA 私钥，均为密文列）、
// **解 + 落库**（把信封数组变成 chat_archive_records 行，并单调推进游标）、
// **声明取数缺口**（拉取要靠官方 C SDK + 存档许可，这里留注入点并把"没接上"说清楚）。
//
// 为什么不复用收发凭据（DecryptCredential/Credential）：存档是**合规能力**而非收发链路。
// 混在一列里时，"应用 secret 解不开要重录"的引导会把本来正常工作的收发一起拖进重录流程；
// 且存档私钥泄露的严重度远高于应用 secret（等于交出全部客户聊天记录明文），
// 所以它有自己的列、自己的接口、自己的开关。
//
// 句柄约定：本包函数收调用方传入的 *gorm.DB（api 层传 db.RQ(c)，后台 ticker 传 db.DB），
// 入口统一 Session 复位——RQ 句柄 clone=0 会就地累加 Where，而一次入库要跑
// "读通道 → 逐条插入 → 推游标"多条语句，不复位会把上一条条件带进下一条（触达批实锤缺陷）。
package channel

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"ai-scrm/internal/model"
	"ai-scrm/pkg/crypto"
)

// ErrArchiveSDKNotBuilt 官方存档取数（C SDK）未接入的稳定原因码。
//
// 为什么要稳定码而不是一句中文：Admin 页、冒烟脚本、readiness 都要按它分支。
// "存档开了但一条都没落库"有两种完全相反的解释——SDK 没编进来（正常，等接入）
// 和接入后链路坏了（异常，要告警）。没有这个码就分不开，前端只能显示一句模糊提示。
var ErrArchiveSDKNotBuilt = errors.New("archive_sdk_not_built")

// ErrArchiveKeyMissing 开了存档但没配 RSA 私钥（或私钥密文解不开/格式坏）。
var ErrArchiveKeyMissing = errors.New("archive_key_missing")

// archivePullLimit 单轮最多解多少条。首轮（游标 0）会命中企业全部历史存档，
// 不设上限等于拿一次同步去换内存。
const archivePullLimit = 1000

// ArchiveFetchFunc 存档取数注入点：返回按 seq 升序的信封数组（afterSeq 之后、至多 limit 条）。
//
// 做成包级变量与 outreach.SendHook / talkmining.GenerateDraftFunc 同一口径：领域层不 import
// 具体实现，由 main.go 装配。官方实现要 cgo 链 libwxwork（商用企业 + 存档许可才拿得到），
// 本机/CI 编不出来，所以**默认 nil**，链路其余部分（配置/解密/落库/查询）照常可跑可测。
type ArchiveFetchFunc func(ctx context.Context, ch *model.Channel, archiveSecret, privateKeyPEM string, afterSeq int64, limit int) ([]ArchiveCipherItem, error)

// ArchiveFetcher 当前生效的取数实现（nil=未接入，见 ErrArchiveSDKNotBuilt）。
var ArchiveFetcher ArchiveFetchFunc

// ArchiveIngestResult 一轮入库的计数（接口回显、日志、冒烟断言共用同一份真相）。
type ArchiveIngestResult struct {
	Fetched       int   `json:"fetched"`        // 信封条数
	Stored        int   `json:"stored"`         // 新落库行数（含解密失败留痕行）
	DupSkipped    int   `json:"dup_skipped"`    // 撞幂等锚跳过（企微重推/同批重拉）
	StaleSkipped  int   `json:"stale_skipped"`  // seq ≤ 游标，历史已消费
	DecryptFailed int   `json:"decrypt_failed"` // 解密失败留痕行数
	MaxSeq        int64 `json:"max_seq"`        // 游标推进到的位置
}

// ArchiveConfigInput 存档配置写入参（指针/空串=不改动，与通道凭据同口径）。
type ArchiveConfigInput struct {
	Enabled       *bool
	ArchiveSecret string // 非空→重加密落 archive_secret_cipher
	PrivateKeyPEM string // 非空→先校验能解析，再密文覆盖
	PublicKeyVer  *int
}

// ArchiveKeyView 一次性回显给管理员的密钥信息（公钥要粘去企微后台；私钥永不出接口）。
type ArchiveKeyView struct {
	PublicKeyPEM string `json:"public_key_pem"`
	Fingerprint  string `json:"fingerprint"`
	PublicKeyVer int    `json:"public_key_ver"`
}

// ArchiveStatusView 存档状态摘要（Admin 页与 readiness 共用，绝不含密文/明文密钥）。
type ArchiveStatusView struct {
	Enabled          bool       `json:"enabled"`
	KeyConfigured    bool       `json:"key_configured"`
	SecretConfigured bool       `json:"secret_configured"`
	PublicKeyVer     int        `json:"public_key_ver"`
	Fingerprint      string     `json:"fingerprint"` // 空=私钥不可用（不报密钥内容）
	CursorSeq        int64      `json:"cursor_seq"`
	FetcherReady     bool       `json:"fetcher_ready"` // false=官方 SDK 未接入（不是故障）
	LastMsgAt        *time.Time `json:"last_msg_at"`
	StoredTotal      int64      `json:"stored_total"`
	FailedTotal      int64      `json:"failed_total"`       // decrypt_error 非空行数
	VerMismatchTotal int64      `json:"ver_mismatch_total"` // 加解密版本与当前配置不符的行数
}

// session 复位派生句柄（见文件头"句柄约定"）。
func session(gdb *gorm.DB) *gorm.DB {
	if gdb == nil {
		return nil
	}
	return gdb.Session(&gorm.Session{})
}

// LoadArchivePrivateKey 取通道私钥（密文解 + PEM 解析）。解不开即 ErrArchiveKeyMissing。
func LoadArchivePrivateKey(ch *model.Channel) (*rsa.PrivateKey, string, error) {
	if ch == nil || strings.TrimSpace(ch.ArchivePrivateKeyCipher) == "" {
		return nil, "", ErrArchiveKeyMissing
	}
	pemStr, err := crypto.Decrypt(ch.ArchivePrivateKeyCipher)
	if err != nil || strings.TrimSpace(pemStr) == "" {
		return nil, "", ErrArchiveKeyMissing
	}
	key, perr := ParseArchivePrivateKey(pemStr)
	if perr != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrArchiveKeyMissing, perr)
	}
	return key, pemStr, nil
}

// DecryptArchiveSecret 取存档 secret 明文（仅取数实现内部用，禁止出接口）。
func DecryptArchiveSecret(ch *model.Channel) (string, error) {
	if ch == nil || strings.TrimSpace(ch.ArchiveSecretCipher) == "" {
		return "", ErrArchiveKeyMissing
	}
	s, err := crypto.Decrypt(ch.ArchiveSecretCipher)
	if err != nil {
		return "", ErrCredentialRekey
	}
	return s, nil
}

// UpdateArchiveConfig 写存档凭据/开关，返回更新后的通道行（不含任何明文）。
//
// 私钥先 ParseArchivePrivateKey 校验再落库：存进去一把解不开的私钥，表现是"存档开了、
// 每条都 decrypt_error"，等于让人等到有客户投诉时才发现配置本来就是坏的。
func UpdateArchiveConfig(gdb *gorm.DB, tenantID, id uint, in ArchiveConfigInput) (*model.Channel, error) {
	gdb = session(gdb)
	var ch model.Channel
	if err := gdb.Where("id = ? AND tenant_id = ? AND deleted_at IS NULL", id, tenantID).First(&ch).Error; err != nil {
		return nil, ErrChannelNotFound
	}
	upd := map[string]any{}
	if in.Enabled != nil && *in.Enabled != ch.ArchiveEnabled {
		if *in.Enabled {
			// 开关要的是"能跑"：没私钥就开等于起一条只会产 decrypt_error 的链路
			if _, err := archivePrivateKeyPEM(&ch); err != nil {
				return nil, err
			}
		}
		upd["archive_enabled"] = *in.Enabled
	}
	if s := strings.TrimSpace(in.ArchiveSecret); s != "" {
		c, err := crypto.Encrypt(s)
		if err != nil {
			return nil, err
		}
		upd["archive_secret_cipher"] = c
	}
	if p := strings.TrimSpace(in.PrivateKeyPEM); p != "" {
		if _, err := ParseArchivePrivateKey(p); err != nil {
			return nil, err
		}
		c, err := crypto.Encrypt(p)
		if err != nil {
			return nil, err
		}
		upd["archive_private_key_cipher"] = c
	}
	if in.PublicKeyVer != nil {
		upd["archive_public_key_ver"] = *in.PublicKeyVer
	}
	if len(upd) == 0 {
		return &ch, nil
	}
	if err := gdb.Model(&model.Channel{}).Where("id = ? AND tenant_id = ?", id, tenantID).Updates(upd).Error; err != nil {
		return nil, fmt.Errorf("更新存档配置失败: %w", err)
	}
	if err := gdb.Where("id = ?", id).First(&ch).Error; err != nil {
		return nil, ErrChannelNotFound
	}
	return &ch, nil
}

// archivePrivateKeyPEM 只判断"密文列里是否有一把可用私钥"（开开关前的闸）。
func archivePrivateKeyPEM(ch *model.Channel) (string, error) {
	if ch == nil || strings.TrimSpace(ch.ArchivePrivateKeyCipher) == "" {
		return "", ErrArchiveKeyMissing
	}
	pemStr, err := crypto.Decrypt(ch.ArchivePrivateKeyCipher)
	if err != nil || strings.TrimSpace(pemStr) == "" {
		return "", ErrArchiveKeyMissing
	}
	if _, perr := ParseArchivePrivateKey(pemStr); perr != nil {
		return "", fmt.Errorf("%w: %v", ErrArchiveKeyMissing, perr)
	}
	return pemStr, nil
}

// GenerateArchiveKey 生成密钥对、把私钥密文落到该通道，返回公钥材料给管理员上传企微后台。
//
// 为什么生成即落库：把私钥回显出来让管理员再粘回去，等于让一把能读全部客户聊天记录的
// 私钥走一遍浏览器历史、剪贴板和截图。**只有公钥出接口**。
// 落库同时把 public_key_ver 一并写入（调用方给的版本号），否则解密时按版本挑私钥无从依据。
func GenerateArchiveKey(gdb *gorm.DB, tenantID, id uint, bits, publicKeyVer int) (ArchiveKeyView, error) {
	var v ArchiveKeyView
	privPEM, pubPEM, err := GenerateArchiveKeyPair(bits)
	if err != nil {
		return v, err
	}
	key, err := ParseArchivePrivateKey(privPEM)
	if err != nil { // 自己生成的东西解不开=环境异常，宁可报错也不落一把坏钥匙
		return v, err
	}
	ver := publicKeyVer
	if _, err := UpdateArchiveConfig(gdb, tenantID, id, ArchiveConfigInput{PrivateKeyPEM: privPEM, PublicKeyVer: &ver}); err != nil {
		return v, err
	}
	v.PublicKeyPEM = pubPEM
	v.PublicKeyVer = ver
	v.Fingerprint = ArchivePublicKeyFingerprint(&key.PublicKey)
	return v, nil
}

// IngestArchiveItems 解一批信封并落库，随后单调推进游标。
//
// 三条不变式：
//  1. **幂等锚 (channel_id, msgid)**：撞锚跳过，不做 UPSERT 覆盖——已解出的正文被一次
//     解错的空值盖掉等于毁证据（企微重推、同批重拉都是常态）。
//  2. **解不开也建行**：只留 decrypt_error。游标按 seq 单调前移；遇错停住会让同一 seq
//     反复重拉、整条同步链路永久卡死。
//  3. **游标只前进**：GREATEST 写，多实例并发同步不会互相把游标拽回去重抄历史。
func IngestArchiveItems(gdb *gorm.DB, ch *model.Channel, items []ArchiveCipherItem) (ArchiveIngestResult, error) {
	var res ArchiveIngestResult
	if ch == nil || len(items) == 0 {
		return res, nil
	}
	gdb = session(gdb)
	priv, _, err := LoadArchivePrivateKey(ch)
	if err != nil {
		return res, err
	}
	res.Fetched = len(items)
	maxSeen := ch.ArchiveSeq
	for _, it := range items {
		if it.Seq > 0 && it.Seq <= ch.ArchiveSeq {
			res.StaleSkipped++
			continue // 游标之前的事实：企微按 seq 段重发时会命中，已消费过
		}
		row, failReason := buildArchiveRow(ch, priv, it)
		if failReason != "" {
			res.DecryptFailed++
		}
		// 目标唯一索引是部分的（WHERE msgid <> ''），冲突靶必须带同样的谓词，
		// 否则空 msgid 的事件类报文会撞不上任何索引而 PG 直接报"no unique or exclusion
		// constraint matching"——那是一条 SQL 就把整批同步打死。
		create := gdb.Clauses(clause.OnConflict{
			Columns:     []clause.Column{{Name: "channel_id"}, {Name: "msgid"}},
			TargetWhere: clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "msgid <> ''"}}},
			DoNothing:   true,
		}).Create(&row)
		if create.Error != nil {
			return res, fmt.Errorf("存档落库失败: %w", create.Error)
		}
		if create.RowsAffected == 0 {
			res.DupSkipped++
		} else {
			res.Stored++
		}
		if it.Seq > maxSeen {
			maxSeen = it.Seq
		}
	}
	if maxSeen > ch.ArchiveSeq {
		if err := AdvanceArchiveSeq(gdb, ch.ID, maxSeen); err != nil {
			return res, err
		}
		ch.ArchiveSeq = maxSeen
	}
	res.MaxSeq = maxSeen
	return res, nil
}

// buildArchiveRow 解一条信封 → 落库行；第二返回值非空即解密/规整失败原因（留痕行）。
//
// 解不开时仍然把信封里的 seq/publickey_ver/msgid 写进去：管理员排查"哪些读不了"时，
// 需要的是**定位信息**，不是正文。
func buildArchiveRow(ch *model.Channel, priv *rsa.PrivateKey, it ArchiveCipherItem) (model.ChatArchiveRecord, string) {
	row := model.ChatArchiveRecord{
		TenantID:     ch.TenantID, // 后台任务无请求 ctx，必须显式盖章（C7 红线）
		ChannelID:    ch.ID,
		Seq:          it.Seq,
		PublicKeyVer: it.PublicKeyVer,
		ToList:       "[]",
		MediaStatus:  model.ArchiveMediaNone,
	}
	plain, err := DecryptArchiveItem(priv, it)
	if err != nil {
		row.DecryptError = clipArchiveError(err)
		return row, "decrypt"
	}
	msg, err := NormalizeArchiveMessage(plain, it.Seq, it.PublicKeyVer)
	if err != nil {
		row.DecryptError = clipArchiveError(err)
		return row, "normalize"
	}
	row.MsgID = msg.MsgID
	row.BizType = msg.BizType
	row.Action = msg.Action
	row.FromUser = msg.FromUser
	row.SenderName = msg.SenderName
	row.ChatType = msg.ChatType
	row.ChatID = msg.ChatID
	row.MsgType = msg.MsgType
	row.ContentText = msg.ContentText
	row.MediaID = msg.MediaID
	if !msg.MsgTime.IsZero() {
		t := msg.MsgTime
		row.MsgTime = &t
	}
	if b, jerr := json.Marshal(msg.ToList); jerr == nil && len(msg.ToList) > 0 {
		row.ToList = string(b)
	}
	return row, ""
}

// clipArchiveError 错误原因入库前收尾（换行压平 + 按字符截到列长以内）。
//
// 为什么按 rune 而不是 byte：列长 256 是**字符**数，而企微错误里常带中文；
// 按字节切会把多字节字符劈成半个，PG 存进去的是坏 UTF-8，前端渲染时整页乱码。
// 超长若直接交给 PG 是 22001 报错——那是一条脏数据就把整批同步打死。
func clipArchiveError(err error) string {
	if err == nil {
		return ""
	}
	s := strings.ReplaceAll(strings.TrimSpace(err.Error()), "\n", " ")
	r := []rune(s)
	if len(r) <= 240 {
		return s
	}
	return string(r[:240]) + "…"
}

// AdvanceArchiveSeq 单调前移游标（GREATEST：并发同步互不拽回）。
func AdvanceArchiveSeq(gdb *gorm.DB, channelID uint, seq int64) error {
	return session(gdb).Model(&model.Channel{}).
		Where("id = ?", channelID).
		Update("archive_seq", gorm.Expr("GREATEST(archive_seq, ?)", seq)).Error
}

// SyncArchiveOnce 一轮完整同步（开关闸 → 取数 → 入库），由 main.go 后台 ticker 调用。
//
// 未接入 SDK 时返回 ErrArchiveSDKNotBuilt 而**不是静默返回 0**：Admin 页与 readiness 要能
// 区分"开了但没接"和"接了但没消息"，静默会让前者看起来像后者。
func SyncArchiveOnce(ctx context.Context, gdb *gorm.DB, ch *model.Channel) (ArchiveIngestResult, error) {
	var res ArchiveIngestResult
	if ch == nil || !ch.ArchiveEnabled {
		return res, nil
	}
	if ArchiveFetcher == nil {
		return res, ErrArchiveSDKNotBuilt
	}
	_, pemStr, err := LoadArchivePrivateKey(ch)
	if err != nil {
		return res, err
	}
	secret, err := DecryptArchiveSecret(ch)
	if err != nil {
		return res, err
	}
	items, err := ArchiveFetcher(ctx, ch, secret, pemStr, ch.ArchiveSeq, archivePullLimit)
	if err != nil {
		return res, err
	}
	return IngestArchiveItems(gdb, ch, items)
}

// ArchiveStatus 汇总单通道存档状态（多条查询，句柄已复位）。
func ArchiveStatus(gdb *gorm.DB, tenantID, id uint) (ArchiveStatusView, error) {
	var v ArchiveStatusView
	gdb = session(gdb)
	var ch model.Channel
	if err := gdb.Where("id = ? AND tenant_id = ? AND deleted_at IS NULL", id, tenantID).First(&ch).Error; err != nil {
		return v, ErrChannelNotFound
	}
	v.Enabled = ch.ArchiveEnabled
	v.KeyConfigured = strings.TrimSpace(ch.ArchivePrivateKeyCipher) != ""
	v.SecretConfigured = strings.TrimSpace(ch.ArchiveSecretCipher) != ""
	v.PublicKeyVer = ch.ArchivePublicKeyVer
	v.CursorSeq = ch.ArchiveSeq
	v.FetcherReady = ArchiveFetcher != nil
	if key, _, err := LoadArchivePrivateKey(&ch); err == nil {
		v.Fingerprint = ArchivePublicKeyFingerprint(&key.PublicKey)
	}
	// NULLS LAST 是必须的：PG 默认把 NULL 排在 DESC 的最前面，
	// 而解不开的事件类报文正是 msg_time 为空的那批——不加这句，"最近一条存档消息"
	// 永远返回 null，管理员看到的是"开了存档但一条都没有"。
	var last struct{ MsgTime *time.Time }
	gdb.Model(&model.ChatArchiveRecord{}).Where("channel_id = ?", ch.ID).
		Select("msg_time").Order("msg_time DESC NULLS LAST").Limit(1).Scan(&last)
	v.LastMsgAt = last.MsgTime
	gdb.Model(&model.ChatArchiveRecord{}).Where("channel_id = ?", ch.ID).Count(&v.StoredTotal)
	gdb.Model(&model.ChatArchiveRecord{}).Where("channel_id = ? AND decrypt_error <> ''", ch.ID).Count(&v.FailedTotal)
	// 版本不符不是"读不了"（读得了才会走到这一步），它是"库里这把钥匙不是后台那版"的信号，
	// 所以单独计数、不进 decrypt_error——混在一起会让人以为正文丢了，实际该做的是改 public_key_ver。
	if ch.ArchivePublicKeyVer > 0 {
		gdb.Model(&model.ChatArchiveRecord{}).
			Where("channel_id = ? AND public_key_ver <> ?", ch.ID, ch.ArchivePublicKeyVer).
			Count(&v.VerMismatchTotal)
	}
	return v, nil
}

// ---- 存档查询面（E8-3，2026-09-24）----

// ArchivePageSizeCap 存档名单每页硬顶。下钻是核对用的（"这段时间到底说了什么"），
// 导数走 /admin/export，不给一次拉全量的口子。
const ArchivePageSizeCap = 100

// archiveListPreviewRunes 列表页正文摘要长度（字符数）。全文只在详情接口回显，
// 而详情接口每次写审计——摘要与全文的分工就是"随手翻"和"取证看"的分工。
const archiveListPreviewRunes = 120

// ArchiveRecordView 存档列表行视图：**不含正文全文**。
// 为什么不用 model.ChatArchiveRecord 直接序列化：它的 json tag 把 content_text 带上了，
// 一次列表请求就会把几十位客户的聊天原文整批推给浏览器（进日志、进插件、进截图）。
type ArchiveRecordView struct {
	ID           uint       `json:"id"`
	ChannelID    uint       `json:"channel_id"`
	MsgID        string     `json:"msgid"`
	Seq          int64      `json:"seq"`
	PublicKeyVer int        `json:"public_key_ver"`
	BizType      string     `json:"biz_type"`
	Action       string     `json:"action"`
	FromUser     string     `json:"from_user"`
	SenderName   string     `json:"sender_name"`
	ChatType     string     `json:"chat_type"`
	ChatID       string     `json:"chatid"`
	MsgType      string     `json:"msg_type"`
	MediaID      string     `json:"media_id"`
	MediaStatus  string     `json:"media_status"`
	DecryptError string     `json:"decrypt_error"`
	MsgTime      *time.Time `json:"msg_time"`
	TextPreview  string     `json:"text_preview"`
	HasFullText  bool       `json:"has_full_text"`
}

// ArchiveDetail 存档详情（含正文全文，仅此接口回显，调用方必须写审计）。
type ArchiveDetail struct {
	ArchiveRecordView
	ToList      string `json:"to_list"`
	ContentText string `json:"content_text"`
}

// ArchiveListQuery 存档名单筛选入参（零值=不筛）。
type ArchiveListQuery struct {
	Since    *time.Time
	Until    *time.Time
	MsgType  string
	ChatType string
	FromUser string
	ChatID   string
	Keyword  string // 正文子串（ILIKE，转义过 % 和 _）
	Failed   *bool  // true=只看解密失败留痕行
	Page     int
	PageSize int
}

// ListArchiveRecords 按通道列存档消息（租户锚定 + 摘要不回显全文）。
//
// tenant_id 与 channel_id 双条件而不是"先查通道归属再查消息"：
// 中间那一跳的窗口里通道可以被改属/软删，只按 channel_id 查就会跨租户读到别人的会话原文。
func ListArchiveRecords(gdb *gorm.DB, tenantID, channelID uint, q ArchiveListQuery) ([]ArchiveRecordView, int64, error) {
	gdb = session(gdb)
	qq := gdb.Model(&model.ChatArchiveRecord{}).
		Where("tenant_id = ? AND channel_id = ?", tenantID, channelID)
	if q.Since != nil {
		qq = qq.Where("msg_time >= ?", *q.Since)
	}
	if q.Until != nil {
		qq = qq.Where("msg_time <= ?", *q.Until)
	}
	for col, val := range map[string]string{
		"msg_type":  strings.TrimSpace(q.MsgType),
		"chat_type": strings.TrimSpace(q.ChatType),
		"from_user": strings.TrimSpace(q.FromUser),
		"chatid":    strings.TrimSpace(q.ChatID),
	} {
		if val != "" {
			qq = qq.Where(col+" = ?", val)
		}
	}
	if k := strings.TrimSpace(q.Keyword); k != "" {
		qq = qq.Where("content_text ILIKE ?", `%`+escapeLikePattern(k)+`%`)
	}
	if q.Failed != nil {
		if *q.Failed {
			qq = qq.Where("decrypt_error <> ''")
		} else {
			qq = qq.Where("decrypt_error = ''")
		}
	}
	var total int64
	if err := qq.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("存档名单统计失败: %w", err)
	}
	page, size := NormalizeArchivePage(q.Page, q.PageSize)
	var rows []model.ChatArchiveRecord
	// seq DESC 而不是 msg_time DESC：解不开的留痕行没有 msg_time，
	// 按时间排会让它们长期霸占或彻底消失在某一页；seq 恒有值且就是企微的原始顺序。
	if err := qq.Order("seq DESC").Order("id DESC").
		Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("存档名单读取失败: %w", err)
	}
	views := make([]ArchiveRecordView, 0, len(rows))
	for _, r := range rows {
		views = append(views, toArchiveRecordView(r))
	}
	return views, total, nil
}

// GetArchiveRecord 取一条存档详情（租户锚定；查不到/跨租户同为 ErrArchiveRecordNotFound）。
func GetArchiveRecord(gdb *gorm.DB, tenantID, recordID uint) (ArchiveDetail, error) {
	var d ArchiveDetail
	gdb = session(gdb)
	var r model.ChatArchiveRecord
	// 不回显"这条存在但不在你的租户"与"根本没这条"的差别：存档 ID 是自增，
	// 能探测就等于把别家的留痕行数分布送给外人。
	if err := gdb.Where("id = ? AND tenant_id = ?", recordID, tenantID).First(&r).Error; err != nil {
		return d, ErrArchiveRecordNotFound
	}
	d.ArchiveRecordView = toArchiveRecordView(r)
	d.ContentText = r.ContentText
	d.ToList = r.ToList
	return d, nil
}

// ErrArchiveRecordNotFound 存档记录不存在或不属于本租户（两种情况同码同形，不回显差别）。
var ErrArchiveRecordNotFound = errors.New("archive_record_not_found")

// toArchiveRecordView 行 → 视图（正文降为摘要）。
func toArchiveRecordView(r model.ChatArchiveRecord) ArchiveRecordView {
	return ArchiveRecordView{
		ID: r.ID, ChannelID: r.ChannelID, MsgID: r.MsgID, Seq: r.Seq,
		PublicKeyVer: r.PublicKeyVer, BizType: r.BizType, Action: r.Action,
		FromUser: r.FromUser, SenderName: r.SenderName, ChatType: r.ChatType, ChatID: r.ChatID,
		MsgType: r.MsgType, MediaID: r.MediaID, MediaStatus: r.MediaStatus,
		DecryptError: r.DecryptError, MsgTime: r.MsgTime,
		TextPreview: archivePreview(r.ContentText, archiveListPreviewRunes),
		HasFullText: r.ContentText != "",
	}
}

// archivePreview 按**字符**截断摘要（企微正文里中文占多数，按字节切会劈出半个字，
// 前端拿到就是乱码方块），并在截断时补省略号。
func archivePreview(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// NormalizeArchivePage 分页入参归一（越界页只回空列表，total 如实——见 D4 同口径）。
// 导出是给接口层回显用的：响应里的 page_size 必须是**实际生效**的那个，
// 各写各的钳制规则迟早对不上（前端据此算总页数）。
func NormalizeArchivePage(page, size int) (int, int) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	if size > ArchivePageSizeCap {
		size = ArchivePageSizeCap
	}
	return page, size
}

// escapeLikePattern 转义 ILIKE 通配符：不转义的话关键字里的 % 变成"任意字符串"，
// 管理员搜"100%"会得到一堆不含该字样的行。
func escapeLikePattern(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
