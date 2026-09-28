// 通道凭据可解性观测位（FIX-N，2026-09-28 .env 丢失处置批）
//
// 盯的是同一族"安静失败"：pkg/crypto 的密钥由 JWT_SECRET 派生
// （deriveKey = SHA-256(JWT_SECRET + "|scrm-secretbox/v1")），于是**轮换 JWT_SECRET 会让库里
// 已有的 gcm1: 密文永久解不开**。而界面上看不出区别——通道列表的凭据列本来就只显示掩码，
// 解不开时掩码函数回 "****"，管理员读到的是"这条通道还没配凭据"，
// 真实情况是"凭据在库里、但当前密钥读不出来"，两者要做的动作完全相反
// （前者去填，后者去重录同一份凭据并考虑把密钥换回去）。
//
// 依赖方向与存档探针一致：internal/channel 已经依赖 internal/metrics，
// 本文件**不得** import channel（成环），取数由 cmd/server/main.go 启动时注入闭包。
// 未装配（单测、gateway 等不跑通道的进程）判 not_wired 且恒 OK，绝不 panic 也不误报故障。
//
// 刻意不加 TTL 缓存（与存档探针不同）：本探针是一次走索引的小表扫描，
// 而"管理员刚重录完凭据"正是必须当场看到转绿的时刻——缓存它 5 分钟等于继续报旧态。
package metrics

import (
	"fmt"
	"strings"

	"ai-scrm/config"
)

// CredentialCipherAudit 凭据密文可解性总览（字段由 channel 侧填充，两侧不共用类型）。
type CredentialCipherAudit struct {
	Scanned          int64 // 扫过的密文单元格数（列清单 × 行数，含空值）
	WithCipher       int64 // 其中带 gcm1: 前缀（即真加密过）的单元格数
	Undecryptable    int64 // 用当前 JWT_SECRET 解不开的单元格数
	AffectedChannels int64 // 至少有一列解不开的通道行数（运维要的是"几条要重录"）
}

// credentialCipherFn 取数闭包接缝；nil 表示未装配。
var credentialCipherFn func() (CredentialCipherAudit, error)

// SetCredentialCipherProbe 装配凭据密文取数闭包（由 main.go 调用；传 nil 即卸载）。
func SetCredentialCipherProbe(fn func() (CredentialCipherAudit, error)) {
	credentialCipherFn = fn
}

// ResetCredentialCipherProbe 清空装配（测试用）。
func ResetCredentialCipherProbe() { SetCredentialCipherProbe(nil) }

// credentialCipherCheck 构造 credential_cipher_integrity 观测位。
// 判级口径：
//   - 未装配 → OK/not_wired（gateway 进程、单测都属此形态，不是故障）；
//   - 取数失败 → Warn/query_failed（探针失明只提醒，绝不因此判红，理由同存档探针）；
//   - 有解不开的密文 → Warn，值里给出「几列解不开 / 共几列」与通道数指向，
//     永不判 Crit：这一列可能本来就是历史脏行（如人工 SQL 插的测试通道），
//     把展示型缺口升级成群通知会把真正的资金/合规红灯稀释掉。
func credentialCipherCheck() HealthCheck {
	if credentialCipherFn == nil {
		return HealthCheck{
			Name: "credential_cipher_integrity", Status: StatusOK, Value: "not_wired",
			WarnAt: "-", CritAt: "-",
			Desc: "凭据密文探针未装配（gateway/单测进程不跑通道层）",
		}
	}
	a, err := credentialCipherFn()
	if err != nil {
		return readinessCheck("credential_cipher_integrity", false, "query_failed", StatusWarn,
			"凭据密文无法核验（取数失败：权限或库不可用？）——掩码列显示 **** 时无法分辨是没配还是解不开")
	}
	if a.WithCipher == 0 {
		return HealthCheck{
			Name: "credential_cipher_integrity", Status: StatusOK, Value: "no_cipher",
			WarnAt: "-", CritAt: "-",
			Desc: fmt.Sprintf("库内无 gcm1: 密文凭据（扫描 %d 个单元格）：尚未录入通道凭据，属全新库形态", a.Scanned),
		}
	}
	if a.Undecryptable == 0 {
		return HealthCheck{
			Name: "credential_cipher_integrity", Status: StatusOK,
			Value:  fmt.Sprintf("ok(%d/%d)", a.WithCipher, a.WithCipher),
			WarnAt: "-", CritAt: "-",
			Desc: "全部凭据密文可用当前 JWT_SECRET 解开（密钥派生自 JWT_SECRET，轮换即作废）",
		}
	}
	return readinessCheck("credential_cipher_integrity", false,
		fmt.Sprintf("%d_of_%d_undecryptable(channels=%d)", a.Undecryptable, a.WithCipher, a.AffectedChannels), StatusWarn,
		"这些凭据密文用当前 JWT_SECRET 解不开：AES 密钥派生自 JWT_SECRET，轮换或 .env 重建即让库内 gcm1: 密文永久作废。"+
			"界面上的 **** 是「解不开」不是「没配置」，须在后台重录该通道凭据（或把 JWT_SECRET 换回原值）")
}

// credentialPlaceholdersMax 观测面最多列几条键名缺口（超出如实标 +N，不静默截断）。
// 口径与标签字典 ?all=1 的超限一致：截断本身必须被看出来，否则"清单齐全"是假的。
const credentialPlaceholdersMax = 12

// appendCredentialSurfaceChecks 外部凭据面两条观测位（FIX-N，2026-09-28 .env 丢失处置批）：
// credential_placeholders＝"键存在但值仍是模板占位"；credential_cipher_integrity＝"库里密文当前解不开"。
// 为什么单独成组、不进 appendDeployEnvChecks：同组那两条判的是"开关有没有声明"，
// 本组判的是"凭据到底能不能用"，且其中一条要取库——把 DB 取数塞进"只读 os.Getenv"的分组，
// 会让该组的单测口径（只受 t.Setenv 影响）失效。
// 判级永不 Crit（除"真实 AI + 空主力 Key"这一种）：开发机跑模拟态、占位键是常态，
// 每次本机冒烟都刷一条 crit 会把真正的资金/合规红灯稀释掉（debug 态本也会 Crit→Warn 降档）。
func appendCredentialSurfaceChecks(checks []HealthCheck) []HealthCheck {
	findings := config.AuditCredentialPlaceholders(nil)
	if len(findings) == 0 {
		checks = append(checks, HealthCheck{
			Name: "credential_placeholders", Status: StatusOK, Value: "all_backfilled",
			WarnAt: "-", CritAt: "-",
			Desc: "外部凭据均已回填（判据只看形态：非空且不是 .env.example 的占位值；只列键名永不列值）",
		})
	} else {
		names := make([]string, 0, len(findings))
		for i, f := range findings {
			if i >= credentialPlaceholdersMax {
				names = append(names, fmt.Sprintf("+%d_more", len(findings)-credentialPlaceholdersMax))
				break
			}
			names = append(names, f.Key+":"+f.Reason)
		}
		// 唯一该升 Crit 的形态：声明走真实 AI（AI_MOCK_MODE=false）却连主力供应商 Key 都没有
		// ——后果不是"少个兜底"，是全站 AI 回复必失败而 /status 仍显示 ai_real_call=real。
		level := StatusWarn
		if readinessProdStrict() {
			for _, f := range findings {
				if f.Reason == "missing_main" {
					level = StatusCrit
				}
			}
		}
		checks = append(checks, readinessCheck("credential_placeholders", false,
			strings.Join(names, ","), level,
			"这些外部凭据仍是模板占位/半成品（键名:原因码，值不回显）：placeholder=模板值没改、template_default=出厂默认没动、"+
				"missing_main=声明真实 AI 却没主力供应商 Key、missing_pair=URL 与 Key 只配一半。"+
				".env 丢失后从 .env.example 重建最容易整片回到这个状态，且系统照常启动、照常 200"))
	}
	return append(checks, credentialCipherCheck())
}
