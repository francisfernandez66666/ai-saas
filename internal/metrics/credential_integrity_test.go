// 外部凭据面观测位单测（FIX-N，2026-09-28 .env 丢失处置批）
//
// 分工与存档探针一致：这里只测**判级与接线**（不碰库、真凭据由冒烟 §四十八 覆盖）。
// 两条纪律在文件里逐条落实：
// ① 判据读环境变量的，被测字段依赖的**每一个**键都显式 t.Setenv 钉住（含钉成空串）——
//
//	否则本机 source 过 .env 与 CI 干净环境会让同一条断言给相反结论；
//
// ② 新观测项必须验"它到底出不出现在被断言的那个载荷里"：只测构造函数等于没测，
//
//	所以每张用例都从 ComputeReadiness() 的返回值里按名取项并计恰一条。
package metrics

import (
	"errors"
	"strings"
	"testing"
)

// errProbeForAudit 探针取数失败的桩错误（只用于判级用例，不触碰真库）。
var errProbeForAudit = errors.New("probe: 取数失败（用例注入）")

// envAuditKeys 凭据体检读到的全部键（与 config/envaudit.go 的清单一一对应）。
// 本表**故意**重复列一遍键名：用例里逐个钉死后，若 config 侧新增键而这里没跟上，
// 用例仍会读到进程真实环境——所以另有一条反向锁直接问 config 的清单要键名。
func pinAllCredentialEnv(t *testing.T, over map[string]string) {
	t.Helper()
	keys := []string{
		"SILICONFLOW_API_KEY", "DEEPSEEK_API_KEY", "ZHIPU_API_KEY", "GLM_API_KEY",
		"EMBEDDING_API_URL", "EMBEDDING_API_KEY", "RERANK_API_URL", "RERANK_API_KEY",
		"LLM_GATEWAY_URL", "LLM_GATEWAY_TOKEN", "SMTP_HOST", "SMTP_USER", "SMTP_PASS", "SMTP_FROM",
		"HEALTH_TOKEN", "COLLECTOR_KEY", "AI_MOCK_MODE",
	}
	for _, k := range keys {
		v, ok := over[k]
		if !ok {
			v = ""
		}
		t.Setenv(k, v)
	}
}

// allBackfilledEnv 一份"全部回填"的环境值（只保证形态不是占位，不是真凭据）。
func allBackfilledEnv() map[string]string {
	return map[string]string{
		"SILICONFLOW_API_KEY": "sk-a1b2c3d4e5f6",
		"DEEPSEEK_API_KEY":    "sk-d7e8f9a0b1c2",
		"ZHIPU_API_KEY":       "zp-a1b2c3d4",
		"GLM_API_KEY":         "glm-a1b2c3d4",
		"EMBEDDING_API_URL":   "https://api.example.internal/embeddings",
		"EMBEDDING_API_KEY":   "sk-emb-a1b2c3",
		"RERANK_API_URL":      "https://api.example.internal/rerank",
		"RERANK_API_KEY":      "sk-rr-a1b2c3",
		"LLM_GATEWAY_URL":     "https://gateway.example.internal",
		"LLM_GATEWAY_TOKEN":   "gw-a1b2c3d4",
		"SMTP_HOST":           "smtp.example.internal",
		"SMTP_USER":           "bot@example.internal",
		"SMTP_PASS":           "smtp-a1b2c3",
		"SMTP_FROM":           "bot@example.internal",
		"HEALTH_TOKEN":        "ht-a1b2c3d4",
		"COLLECTOR_KEY":       "ck-a1b2c3d4",
		"AI_MOCK_MODE":        "true",
	}
}

// TestCredentialSurfaceWiredIntoReadiness 接线锁：两个新观测名各恰一条，
// 且都挂在 /status/detail 的 readiness 面（不在公开 /status 的清单里，那条由冒烟判）。
// 摘掉 computeReadinessChecks 里的那一行 append ⇒ 本条立刻红（这是本文件最值钱的一条）。
func TestCredentialSurfaceWiredIntoReadiness(t *testing.T) {
	pinAllCredentialEnv(t, allBackfilledEnv())
	ResetCredentialCipherProbe()
	checks := ComputeReadiness()
	for _, name := range []string{"credential_placeholders", "credential_cipher_integrity"} {
		if n := countCheck(checks, name); n != 1 {
			t.Fatalf("readiness 应恰含 1 条 %s，实得 %d 条", name, n)
		}
	}
	if ch := findCheck(checks, "credential_placeholders"); ch.Value != "all_backfilled" || ch.Status != StatusOK {
		t.Fatalf("全回填时应 OK/all_backfilled，实得 %s/%s", ch.Status, ch.Value)
	}
}

// TestCredentialPlaceholderWarnsByKeyName 占位键必须被点名，且**只点键名不点值**。
// 反向对照：把该键改回真实形态 ⇒ 该键必须从清单里消失。
func TestCredentialPlaceholderWarnsByKeyName(t *testing.T) {
	env := allBackfilledEnv()
	secret := "s3cr3t-siliconflow-value"
	env["SILICONFLOW_API_KEY"] = secret
	pinAllCredentialEnv(t, env)
	// 占位形态才有意义：这里用真实形态的字符串会被判"已回填"，
	// 因此先把值改成模板占位（键名断言用 SILICONFLOW，值不进断言）
	t.Setenv("SILICONFLOW_API_KEY", "sk-your-siliconflow-key")
	ResetCredentialCipherProbe()
	ch := findCheck(ComputeReadiness(), "credential_placeholders")
	if ch.Status != StatusWarn {
		t.Fatalf("占位凭据应判 Warn，实得 %s", ch.Status)
	}
	if !strings.Contains(ch.Value, "SILICONFLOW_API_KEY:placeholder") {
		t.Fatalf("占位键未被点名：%s", ch.Value)
	}
	if strings.Contains(ch.Value, secret) || strings.Contains(ch.Desc, secret) {
		t.Fatalf("观测面泄露了凭据值")
	}
	// 反向对照：改回真实形态 ⇒ 回到 OK/all_backfilled
	t.Setenv("SILICONFLOW_API_KEY", env["SILICONFLOW_API_KEY"]+"x")
	ch2 := findCheck(ComputeReadiness(), "credential_placeholders")
	if ch2.Value != "all_backfilled" {
		t.Fatalf("回填后仍报缺口：%s", ch2.Value)
	}
}

// TestCredentialMissingMainCritOnlyInStrict 声明走真实 AI 却缺主力 Key：
// 生产严格态 + 已装配成 release ⇒ Crit（全站回复必失败）；
// 模拟态 ⇒ 至多 Warn（本机九套冒烟形态不得被刷成红灯堆）。
func TestCredentialMissingMainCritOnlyInStrict(t *testing.T) {
	env := allBackfilledEnv()
	env["AI_MOCK_MODE"] = "false"
	env["SILICONFLOW_API_KEY"] = ""
	pinAllCredentialEnv(t, env)
	ResetCredentialCipherProbe()

	// 显式 debug（config.IsDevModeConfirmed=true）：判据仍是 Warn，且 debug 统一降档
	t.Setenv("GIN_MODE", "debug")
	ch := findCheck(ComputeReadiness(), "credential_placeholders")
	if ch.Status == StatusCrit {
		t.Fatalf("显式 debug 不应把缺主力 Key 判 Crit：%s", ch.Status)
	}
	if !strings.Contains(ch.Value, "missing_main") {
		t.Fatalf("真实 AI 缺主力 Key 必须点名 missing_main：%s", ch.Value)
	}

	// 严格态 + release：本项应升 Crit（降档分支不再起作用）
	t.Setenv("GIN_MODE", "release")
	deployIsRelease = true
	t.Cleanup(func() { deployIsRelease = false })
	strict := findCheck(ComputeReadiness(), "credential_placeholders")
	if strict.Status != StatusCrit {
		t.Fatalf("release+真实 AI+空主力 Key 必须 Crit，实得 %s", strict.Status)
	}
}

// TestCredentialCipherCheckStates 密文可解性探针逐档判级（正向 + 反向各一对）：
// 未装配/取数失败/无密文/全可解/有作废 五档，每档都断"该红才红、不该红不许红"。
func TestCredentialCipherCheckStates(t *testing.T) {
	type cs struct {
		name string
		fn   func() (CredentialCipherAudit, error)
		want HealthStatus
		msg  string
	}
	cases := []cs{
		{"not_wired", nil, StatusOK, "未装配判 ok"},
		{"query_failed", func() (CredentialCipherAudit, error) {
			return CredentialCipherAudit{}, errProbeForAudit
		}, StatusWarn, "探针失明只提醒"},
		{"no_cipher", func() (CredentialCipherAudit, error) {
			return CredentialCipherAudit{Scanned: 10, WithCipher: 0}, nil
		}, StatusOK, "全新库无凭据不是故障"},
		{"all_readable", func() (CredentialCipherAudit, error) {
			return CredentialCipherAudit{Scanned: 10, WithCipher: 4, Undecryptable: 0}, nil
		}, StatusOK, "全部可解"},
		{"undecryptable", func() (CredentialCipherAudit, error) {
			return CredentialCipherAudit{Scanned: 10, WithCipher: 4, Undecryptable: 2, AffectedChannels: 1}, nil
		}, StatusWarn, "有作废必须点名"},
	}
	ResetCredentialCipherProbe()
	t.Cleanup(ResetCredentialCipherProbe)
	for _, c := range cases {
		SetCredentialCipherProbe(c.fn)
		ch := credentialCipherCheck()
		if ch.Name != "credential_cipher_integrity" {
			t.Fatalf("%s：观测名漂移 %s", c.name, ch.Name)
		}
		if ch.Status != c.want {
			t.Fatalf("%s：%s 应判 %s，实得 %s", c.name, c.msg, c.want, ch.Status)
		}
	}
	// 作废档的 Value 形态锁：要写出「几列解不开/共几列」，运维据此判断影响面
	SetCredentialCipherProbe(func() (CredentialCipherAudit, error) {
		return CredentialCipherAudit{Scanned: 10, WithCipher: 4, Undecryptable: 2, AffectedChannels: 1}, nil
	})
	if v := credentialCipherCheck().Value; v != "2_of_4_undecryptable(channels=1)" {
		t.Fatalf("作废档 Value 形态不符：%s", v)
	}
	// 反向对照：全可解时不得出现 undecryptable 字样（防"永远 warn"型实现）
	SetCredentialCipherProbe(func() (CredentialCipherAudit, error) {
		return CredentialCipherAudit{Scanned: 10, WithCipher: 4}, nil
	})
	if v := credentialCipherCheck().Value; strings.Contains(v, "undecryptable") {
		t.Fatalf("全可解却报作废：%s", v)
	}
}
