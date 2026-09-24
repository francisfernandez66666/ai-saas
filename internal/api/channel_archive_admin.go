// 会话存档管理端接口（E8-3，2026-09-24）。
//
// 这一层只做四件事：**看状态**（开了没、密钥配了没、游标走到哪、读不了多少条）、
// **配凭据**（存档 secret 与企业自建 RSA 私钥，写入即密文）、
// **手动同步**（管理员点了就拉一轮，不等 ticker）、**查留痕**（名单摘要 + 详情全文）。
//
// 三条接口口径上的硬约束：
//  1. 密文与明文密钥**永不出接口**。存档私钥泄露等于交出全部客户聊天记录明文，
//     所以它连掩码都不给（掩码也会露长度），只回"配了没"+ 公钥指纹。
//  2. 列表不回正文全文。一次列表可能几十位客户的会话原文，进浏览器历史、进日志、
//     进截图的代价与它作为合规证据的价值不成比例；全文只在详情接口回显，
//     且详情每次写审计（谁在什么时候看了谁的会话，正是存档要留的那条痕）。
//  3. 通道不存在与跨租户同码同形（404），不回显"存在但不归你"。
package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ai-scrm/internal/channel"
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
)

// archiveDefaultPageSize 存档名单默认页大小（硬顶在 channel 包，两侧共用同一常数）。
const archiveDefaultPageSize = 20

// archiveTimeLayouts 存档时间筛选接受的两种入参形态：RFC3339（前端 datetime-local 原值）
// 与裸日期（管理员手敲 2026-09-01 表示"当天零点起"）。
var archiveTimeLayouts = []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"}

// parseArchiveTime 解析时间入参；ok=false 表示**写了但解析不出来**（要报 400），
// 空串是"不筛"（返回 nil,true），不能一并当非法——否则缺省查询也 400。
func parseArchiveTime(raw string) (*time.Time, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, true
	}
	for _, layout := range archiveTimeLayouts {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return &t, true
		}
	}
	return nil, false
}

// archiveConfigReq 存档凭据写入参。指针/空串=不改动，与通道凭据同口径
// （不留空即不动，免得管理员只改开关就把 secret 清成空串）。
type archiveConfigReq struct {
	Enabled       *bool  `json:"enabled"`
	ArchiveSecret string `json:"archive_secret"`
	PrivateKeyPEM string `json:"private_key_pem"`
	PublicKeyVer  *int   `json:"public_key_ver"`
}

// archiveKeyGenReq 密钥对生成入参（公钥版本默认沿用当前配置值，0=用 1）。
type archiveKeyGenReq struct {
	Bits         int `json:"bits"`           // 2048|4096，默认 2048
	PublicKeyVer int `json:"public_key_ver"` // 企微后台那一版的 ver
}

// archiveStatusView 存档状态（**不含任何密钥材料**，指纹是公钥派生的公开信息）。
type archiveStatusView struct {
	Enabled          bool       `json:"enabled"`
	KeyConfigured    bool       `json:"key_configured"`
	SecretConfigured bool       `json:"secret_configured"`
	PublicKeyVer     int        `json:"public_key_ver"`
	Fingerprint      string     `json:"fingerprint"`
	CursorSeq        int64      `json:"cursor_seq"`
	FetcherReady     bool       `json:"fetcher_ready"`
	LastMsgAt        *time.Time `json:"last_msg_at"`
	StoredTotal      int64      `json:"stored_total"`
	FailedTotal      int64      `json:"failed_total"`
	VerMismatchTotal int64      `json:"ver_mismatch_total"`
	// SDKReason 取数能力缺口的稳定原因码（""=已接入）。
	// 为什么单独给码而不是把 fetcher_ready=false 当成失败：管理员看到 0 条时，
	// "官方 SDK 还没编进来（正常，等接入）"和"接了但一条都没拉到（要排查）"是两件事。
	SDKReason string `json:"sdk_reason"`
}

func toArchiveStatusView(v channel.ArchiveStatusView) archiveStatusView {
	reason := ""
	if !v.FetcherReady {
		reason = channel.ErrArchiveSDKNotBuilt.Error()
	}
	return archiveStatusView{
		Enabled: v.Enabled, KeyConfigured: v.KeyConfigured, SecretConfigured: v.SecretConfigured,
		PublicKeyVer: v.PublicKeyVer, Fingerprint: v.Fingerprint, CursorSeq: v.CursorSeq,
		FetcherReady: v.FetcherReady, LastMsgAt: v.LastMsgAt,
		StoredTotal: v.StoredTotal, FailedTotal: v.FailedTotal, VerMismatchTotal: v.VerMismatchTotal,
		SDKReason: reason,
	}
}

// archiveChannel 取本租户的通道（不存在/跨租户统一 404 不回显差别）。
func archiveChannel(c *gin.Context) (*model.Channel, bool) {
	id := chanID(c)
	ch, err := channel.Get(tenantIDOf(c), id)
	if err != nil {
		RespErr(c, http.StatusNotFound, 404, "通道不存在")
		return nil, false
	}
	return ch, true
}

// GetChannelArchive GET /admin/channels/:id/archive
// apidump:ts ArchiveStatusResp
// GetChannelArchive 返回单通道会话存档状态摘要。
func GetChannelArchive(c *gin.Context) {
	tid := tenantIDOf(c)
	if _, ok := archiveChannel(c); !ok {
		return
	}
	st, err := channel.ArchiveStatus(db.RQ(c), tid, chanID(c))
	if err != nil {
		archiveWriteErr(c, err, "存档状态读取失败")
		return
	}
	RespOK(c, "ok", gin.H{"status": toArchiveStatusView(st)})
}

// UpdateChannelArchive PUT /admin/channels/:id/archive
// apidump:ts ArchiveStatusResp
// UpdateChannelArchive 写入存档凭据与开关（密文列，响应只回状态摘要）。
func UpdateChannelArchive(c *gin.Context) {
	tid := tenantIDOf(c)
	var req archiveConfigReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespErrBind(c, err)
		return
	}
	if req.PublicKeyVer != nil && *req.PublicKeyVer < 0 {
		RespErr(c, http.StatusBadRequest, 400, "public_key_ver 不能为负")
		return
	}
	if _, err := channel.UpdateArchiveConfig(db.RQ(c), tid, chanID(c), channel.ArchiveConfigInput{
		Enabled: req.Enabled, ArchiveSecret: req.ArchiveSecret,
		PrivateKeyPEM: req.PrivateKeyPEM, PublicKeyVer: req.PublicKeyVer,
	}); err != nil {
		archiveWriteErr(c, err, "存档配置更新失败")
		return
	}
	// 写后立刻读一次状态回显：管理员要确认的是"开关真的按我点的了吗、密钥真的收下了吗"，
	// 只回一句"已更新"就得再刷一次页面，而刷新走的正是同一个接口——不如一次给全。
	st, err := channel.ArchiveStatus(db.RQ(c), tid, chanID(c))
	if err != nil {
		archiveWriteErr(c, err, "存档状态读取失败")
		return
	}
	writeAuditSimple(c, tid, "channel_archive_config", "channel:"+strconv.FormatUint(uint64(chanID(c)), 10))
	RespOK(c, "已更新（凭据只以密文落库，不回显）", gin.H{"status": toArchiveStatusView(st)})
}

// GenerateChannelArchiveKey POST /admin/channels/:id/archive/key
// apidump:ts ArchiveKeyResp
// GenerateChannelArchiveKey 生成 RSA 密钥对：私钥密文落库，**只有公钥出接口**。
func GenerateChannelArchiveKey(c *gin.Context) {
	tid := tenantIDOf(c)
	if _, ok := archiveChannel(c); !ok {
		return
	}
	var req archiveKeyGenReq
	_ = c.ShouldBindJSON(&req) // 允许空体：全用默认值

	bits := req.Bits
	if bits == 0 {
		bits = 2048
	}
	if bits != 2048 && bits != 4096 {
		RespErr(c, http.StatusBadRequest, 400, "bits 仅 2048|4096")
		return
	}
	ver := req.PublicKeyVer
	if ver == 0 {
		// 没显式给版本号就落在"当前配置 +1"：轮换密钥后新旧公钥必须能区分，
		// 沿用旧版本号会让刚解得开的消息一夜之间全变成版本对不上。
		if cur, err := channel.ArchiveStatus(db.RQ(c), tid, chanID(c)); err == nil {
			ver = cur.PublicKeyVer + 1
		} else {
			ver = 1
		}
	}
	view, err := channel.GenerateArchiveKey(db.RQ(c), tid, chanID(c), bits, ver)
	if err != nil {
		archiveWriteErr(c, err, "存档密钥生成失败")
		return
	}
	// 审计记的是"谁生成了第几版密钥"，不是密钥本身。
	writeAuditSimple(c, tid, "channel_archive_keygen", "channel:"+strconv.FormatUint(uint64(chanID(c)), 10)+":ver:"+strconv.Itoa(view.PublicKeyVer))
	RespOK(c, "密钥对已生成，私钥已加密入库；请把公钥上传企微后台「会话内容存档」公钥配置", gin.H{
		"public_key_pem":   view.PublicKeyPEM,
		"fingerprint":      view.Fingerprint,
		"public_key_ver":   view.PublicKeyVer,
		"private_key_echo": false, // 显式回一个 false，免得管理员以为"页面漏显示了"而反复重试
	})
}

// SyncChannelArchive POST /admin/channels/:id/archive/sync
// apidump:ts ArchiveSyncResp
// SyncChannelArchive 手动触发一轮存档同步（与后台 ticker 同一入口）。
func SyncChannelArchive(c *gin.Context) {
	tid := tenantIDOf(c)
	ch, ok := archiveChannel(c)
	if !ok {
		return
	}
	res, err := channel.SyncArchiveOnce(c.Request.Context(), db.RQ(c), ch)
	if err != nil {
		// SDK 未接入/缺密钥是**配置与环境状态**，不是这次请求写错了：
		// 与 VerifyChannel 同口径回 200 + ok=false + 稳定 reason 码，让页面把它显示成
		// 一条待办引导而不是一个红色失败弹窗（真故障仍走 5xx 脱敏）。
		if errors.Is(err, channel.ErrArchiveSDKNotBuilt) || errors.Is(err, channel.ErrArchiveKeyMissing) ||
			errors.Is(err, channel.ErrCredentialRekey) {
			RespOK(c, "本轮未同步", gin.H{"ok": false, "reason": archiveReasonOf(err), "result": res})
			return
		}
		RespErrInternal(c, err, "存档同步失败")
		return
	}
	if !ch.ArchiveEnabled {
		RespOK(c, "存档未开启", gin.H{"ok": false, "reason": "archive_disabled", "result": res})
		return
	}
	writeAuditSimple(c, tid, "channel_archive_sync", "channel:"+strconv.FormatUint(uint64(ch.ID), 10))
	RespOK(c, "ok", gin.H{"ok": true, "reason": "", "result": res})
}

// archiveReasonOf 把已知稳定错误映射为对外原因码（未识别的返回 ""，交由 5xx 分支）。
func archiveReasonOf(err error) string {
	switch {
	case errors.Is(err, channel.ErrArchiveSDKNotBuilt):
		return channel.ErrArchiveSDKNotBuilt.Error()
	case errors.Is(err, channel.ErrArchiveKeyMissing):
		return channel.ErrArchiveKeyMissing.Error()
	case errors.Is(err, channel.ErrCredentialRekey):
		return "archive_secret_rekey_required"
	default:
		return ""
	}
}

// ListChannelArchiveRecords GET /admin/channels/:id/archive/records
// apidump:ts ArchiveRecordListResp
// ListChannelArchiveRecords 存档消息名单（只给摘要，全文走详情接口）。
func ListChannelArchiveRecords(c *gin.Context) {
	tid := tenantIDOf(c)
	if _, ok := archiveChannel(c); !ok {
		return
	}
	since, ok1 := parseArchiveTime(c.Query("since"))
	until, ok2 := parseArchiveTime(c.Query("until"))
	if !ok1 || !ok2 {
		RespErr(c, http.StatusBadRequest, 400, "since/until 时间格式不合法（RFC3339 或 2006-01-02）")
		return
	}
	page, size := channel.NormalizeArchivePage(archiveQueryInt(c, "page", 1), archiveQueryInt(c, "page_size", archiveDefaultPageSize))
	q := channel.ArchiveListQuery{
		Since: since, Until: until,
		MsgType:  c.Query("msg_type"),
		ChatType: c.Query("chat_type"),
		FromUser: c.Query("from_user"),
		ChatID:   c.Query("chatid"),
		Keyword:  c.Query("keyword"),
		Page:     page,
		PageSize: size,
	}
	if v := strings.TrimSpace(c.Query("failed")); v != "" {
		b := v == "true" || v == "1"
		q.Failed = &b
	}
	list, total, err := channel.ListArchiveRecords(db.RQ(c), tid, chanID(c), q)
	if err != nil {
		archiveWriteErr(c, err, "存档名单读取失败")
		return
	}
	RespOK(c, "ok", gin.H{
		"list": list, "total": total,
		// 回显**实际生效**的分页：page_size=1000 的请求回原值，前端会算出"共 1 页"而每页只有 100 行。
		"page": page, "page_size": size, "page_size_cap": channel.ArchivePageSizeCap,
		"note": "列表仅回显正文摘要（120 字），全文请走详情接口——每次读取全文都会写审计。",
	})
}

// GetChannelArchiveRecord GET /admin/channel-archive/records/:id
// apidump:ts ArchiveRecordDetailResp
// GetChannelArchiveRecord 单条存档详情（含正文全文，读取即留痕）。
//
// 为什么挂在独立父节点而不是 /admin/channels/:id/archive/records/:id：gin 在同一层
// 不允许静态段与参数段并存（`/admin/channels/dead-letters` 与 `/:id` 就是这么冲突的，
// 见 routes_admin.go 的原注释），二级嵌套还会再撞一次同层冲突。
func GetChannelArchiveRecord(c *gin.Context) {
	tid := tenantIDOf(c)
	id, ok := PathUintID(c)
	if !ok {
		return
	}
	detail, err := channel.GetArchiveRecord(db.RQ(c), tid, id)
	if err != nil {
		archiveWriteErr(c, err, "存档记录读取失败")
		return
	}
	// 合规留痕这条能力本身也要留痕：谁看了哪一条。
	writeAuditSimple(c, tid, "channel_archive_record_read", "archive_record:"+strconv.FormatUint(uint64(id), 10))
	RespOK(c, "ok", gin.H{"record": detail})
}

// archiveQueryInt 取整型 query 参数（非法/缺省回默认值）。
func archiveQueryInt(c *gin.Context, key string, def int) int {
	v := strings.TrimSpace(c.Query(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// archiveWriteErr 存档接口的错误出口：已知状态码 → 404/400 稳定文案，其余 5xx 脱敏。
//
// 为什么内部错误一律 RespErrInternal：存档表名、列宽、SQLSTATE 都是探测面，
// 一条超长错误回显出去等于把 schema 送出去（P1-1 同口径）。
func archiveWriteErr(c *gin.Context, err error, safeMsg string) {
	switch {
	case errors.Is(err, channel.ErrChannelNotFound):
		RespErr(c, http.StatusNotFound, 404, "通道不存在")
	case errors.Is(err, channel.ErrArchiveRecordNotFound):
		RespErr(c, http.StatusNotFound, 404, "存档记录不存在")
	case errors.Is(err, channel.ErrArchiveKeyMissing):
		RespErr(c, http.StatusBadRequest, 400, channel.ErrArchiveKeyMissing.Error()+"：开启存档前请先生成或上传 RSA 私钥")
	case errors.Is(err, channel.ErrCredentialRekey):
		RespErr(c, http.StatusBadRequest, 400, "archive_secret_rekey_required：存档凭据需重新录入（加密密钥已轮换）")
	case errors.Is(err, channel.ErrArchiveKeyFormat):
		// 粘错格式与"库里那把坏了"必须是两个文案：前者改入参就好，后者按提示去重新生成
		// 一把反而会永久丢掉历史存档（旧密文是旧公钥加的密）。
		RespErr(c, http.StatusBadRequest, 400, "archive_key_format：私钥格式不合法，需 PEM 编码的 RSA 私钥（PKCS#1 或 PKCS#8）")
	default:
		RespErrInternal(c, err, safeMsg)
	}
}
