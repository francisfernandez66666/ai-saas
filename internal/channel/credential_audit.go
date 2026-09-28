// 通道凭据密文可解性审计（FIX-N，2026-09-28 .env 丢失处置批）
//
// 为什么这段代码放在 channel 包：它要认的是通道自己的五列密文字段名
// （secret/token/aeskey/archive_secret/archive_private_key），而观测面在 metrics——
// channel 已经 import metrics，反向 import 即成环，故与存档探针同一桥接口径：
// 本文件出数，cmd/server/main.go 在启动时把闭包注进 metrics.SetCredentialCipherProbe。
//
// 判据本体只有一句：`gcm1:` 前缀的密文用**当前** JWT_SECRET 解一次，解不开就是作废。
// 之所以值得单独审计而不是等用户报"通道连不上"：AES 密钥派生自 JWT_SECRET
// （pkg/crypto deriveKey），轮换或从模板重建 .env 会让库里所有已录凭据同时变成解不开的字节串，
// 而管理台对这些列本来就只显掩码——"解不开"与"还没配"在界面上是同一个 ****。
package channel

import (
	"errors"
	"strings"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/pkg/crypto"

	"gorm.io/gorm"
)

// errAuditNoDB 数据库尚未装配（单测未建库、或 gateway 之外的空跑进程）时显式报错，
// 让调用方走 query_failed 分支——绝不返回零值冒充"一切正常"，
// 那正是本批要消灭的那类静默绿。
var errAuditNoDB = errors.New("channel: 凭据密文审计不可用（DB 未装配）")

// CredentialCipherSummary 凭据密文可解性总览（列名与 metrics 侧结构体一一对应，但不共用类型）。
type CredentialCipherSummary struct {
	CellsInspected   int64 // 检查过的单元格数（五列 × 行数，含空值）
	WithCipher       int64 // 其中带 gcm1: 前缀（真加密过）的单元格数
	Undecryptable    int64 // 用当前 JWT_SECRET 解不开的单元格数
	AffectedChannels int64 // 至少有一列解不开的行数（运维要的是"几条通道要重录"）
}

// cipherColumns 参与审计的密文列清单（见下方类型注释与反向锁用例）。
// cipherColumn 一列密文的取法：列名 + 取值器（列名进结构体是为了让反向锁能核对，
// 见 credential_audit_test.go 的 TestCipherColumnsCoverModel——
// 新增密文列却漏进这里，审计就会永远数不到那一列，观测位继续报"全部可解"这种假绿）。
type cipherColumn struct {
	Column string
	Get    func(model.Channel) string
}

var cipherColumns = []cipherColumn{
	{"secret_cipher", func(c model.Channel) string { return c.SecretCipher }},
	{"token_cipher", func(c model.Channel) string { return c.TokenCipher }},
	{"aeskey_cipher", func(c model.Channel) string { return c.AesKeyCipher }},
	{"archive_secret_cipher", func(c model.Channel) string { return c.ArchiveSecretCipher }},
	{"archive_private_key_cipher", func(c model.Channel) string { return c.ArchivePrivateKeyCipher }},
}

// AuditCredentialCiphers 扫描通道凭据密文并如实计数（不改写任何数据）。
// 取数走 db.DB 全表（行数量级＝租户数×通道数，一次索引扫描），
// 只在启动时与 /status/detail 被调；刻意不加缓存——重录凭据后必须当场看到转绿。
func AuditCredentialCiphers(gdb *gorm.DB) (CredentialCipherSummary, error) {
	var out CredentialCipherSummary
	if gdb == nil {
		gdb = db.DB
	}
	if gdb == nil {
		return out, errAuditNoDB
	}
	var rows []model.Channel
	// Select 只取审计清单里的密文列：不需要 corpid/名称，少读列就少拿一层明文面
	cols := make([]string, 0, len(cipherColumns)+1)
	cols = append(cols, "id")
	for _, c := range cipherColumns {
		cols = append(cols, c.Column)
	}
	if err := gdb.Model(&model.Channel{}).Select(cols).Find(&rows).Error; err != nil {
		return out, err
	}
	for _, c := range rows {
		out.CellsInspected += int64(len(cipherColumns))
		badInRow := false
		for _, col := range cipherColumns {
			v := strings.TrimSpace(col.Get(c))
			if v == "" {
				continue
			}
			if !strings.HasPrefix(v, "gcm1:") {
				// 非 gcm1 前缀＝历史明文或空值，crypto.Decrypt 原样返回，不算作废
				continue
			}
			out.WithCipher++
			if derr := decryptFailsForAudit(v); derr != nil {
				out.Undecryptable++
				badInRow = true
			}
		}
		if badInRow {
			out.AffectedChannels++
		}
	}
	return out, nil
}

// decryptFailsForAudit 解密试探的单点接缝：只回"成不成"，不把明文往外传。
// 为什么不内联 crypto.Decrypt：留一个显式名字，既让"判据是解密结果"这件事在代码里可读，
// 也让变异用例能精确替换这一处而不必改动调用形状（内联写法一改就顺手把 v 也带出去了）。
func decryptFailsForAudit(cipherStr string) error {
	_, err := crypto.Decrypt(cipherStr)
	return err
}
