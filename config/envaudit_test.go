// 外部凭据体检单测（FIX-N，2026-09-28）
// 纪律：每条判据都有正/反两向用例，且逐键 t.Setenv 钉死（含钉成空串）——
// 本包判据全部读环境变量，机器上 source 过 .env 与 CI 干净环境必须给同一结论。
package config

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// lookupOf 把 map 变成 lookup 函数；map 里没有的键一律回空串（显式钉死，不读进程真实环境）。
func lookupOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// reasonsOf 结论映射：键名 → 原因码列表（一个键可能同时命中多档，当前实现不会，但读起来方便）。
func reasonsOf(fs []CredentialFinding) map[string]string {
	out := map[string]string{}
	for _, f := range fs {
		out[f.Key] = f.Reason
	}
	return out
}

// cleanEnv 一份"全部已回填"的基线环境：用例只改自己要判的那个键。
func cleanEnv() map[string]string {
	return map[string]string{
		"AI_MOCK_MODE":        "true",
		"SILICONFLOW_API_KEY": "sk-real-siliconflow-key-abcdef",
		"DEEPSEEK_API_KEY":    "sk-real-deepseek-key",
		"EMBEDDING_API_URL":   "https://api.siliconflow.cn/v1/embeddings",
		"EMBEDDING_API_KEY":   "sk-real-embedding-key",
		"RERANK_API_URL":      "https://api.siliconflow.cn/v1/rerank",
		"RERANK_API_KEY":      "sk-real-rerank-key",
		"LLM_GATEWAY_URL":     "https://gateway.example.internal",
		"LLM_GATEWAY_TOKEN":   "a-real-gateway-token",
		"SMTP_HOST":           "smtp.qiye.aliyun.com",
		"SMTP_USER":           "bot@example.com",
		"SMTP_PASS":           "a-real-smtp-pass",
		"SMTP_FROM":           "bot@example.com",
		"HEALTH_TOKEN":        "a-real-health-token-value",
		"COLLECTOR_KEY":       "a-real-collector-key",
		"ZHIPU_API_KEY":       "a-real-zhipu-key",
		"GLM_API_KEY":         "a-real-glm-key",
	}
}

// TestAuditCredentialsCleanBaseline 正向基线：全回填时结论必须为空。
// 这条本身就是反证——若占位判据写得过宽（例如把 smtp.qiye.aliyun.com 当成占位），红在这里。
func TestAuditCredentialsCleanBaseline(t *testing.T) {
	fs := AuditCredentialPlaceholders(lookupOf(cleanEnv()))
	if len(fs) != 0 {
		t.Fatalf("全回填基线必须零结论，实得 %+v", fs)
	}
}

// TestAuditPlaceholderForms 占位形态逐个命中，且真值形态不误伤。
func TestAuditPlaceholderForms(t *testing.T) {
	for _, v := range []string{"", "   ", "your-zhipu-api-key", "sk-your-siliconflow-key",
		"change-me-gateway-token", "CHANGE_ME", "placeholder", "xxxx1234", "TODO", "<填这里>"} {
		if !IsPlaceholderCredential(v) {
			t.Fatalf("占位形态未命中：%q", v)
		}
	}
	for _, v := range []string{"smtp.qiye.aliyun.com", "sk-7f3a9b12c4", "a-real-key", "bot@example.com"} {
		if IsPlaceholderCredential(v) {
			t.Fatalf("真实值被误判成占位：%q", v)
		}
	}
}

// TestAuditPlaceholderReported 主力 Key 仍是模板值时必须点名，且**只点名不回值**。
func TestAuditPlaceholderReported(t *testing.T) {
	env := cleanEnv()
	env["SILICONFLOW_API_KEY"] = "sk-your-siliconflow-key"
	fs := AuditCredentialPlaceholders(lookupOf(env))
	if reasonsOf(fs)["SILICONFLOW_API_KEY"] != "placeholder" {
		t.Fatalf("占位 Key 未被点名，实得 %+v", fs)
	}
	for _, f := range fs {
		if strings.Contains(f.Key, "sk-") || f.Key == env["SILICONFLOW_API_KEY"] {
			t.Fatalf("结论泄露了值本身：%+v", fs)
		}
	}
}

// TestAuditTemplateDefaultHealthToken HEALTH_TOKEN 逐字等于出厂默认时判 template_default，
// 改过一个字符就必须放行（这条防的是"体检把正常值吃掉"，也防"改了个尾缀蒙混"判不出来——
// 逐字比较是刻意的：出厂默认值本身就是公开信息）。
func TestAuditTemplateDefaultHealthToken(t *testing.T) {
	env := cleanEnv()
	env["HEALTH_TOKEN"] = templateDefaults["HEALTH_TOKEN"]
	if reasonsOf(AuditCredentialPlaceholders(lookupOf(env)))["HEALTH_TOKEN"] != "template_default" {
		t.Fatalf("HEALTH_TOKEN 仍是出厂默认却未被点名")
	}
	env["HEALTH_TOKEN"] = templateDefaults["HEALTH_TOKEN"] + "x"
	fs := AuditCredentialPlaceholders(lookupOf(env))
	if _, bad := reasonsOf(fs)["HEALTH_TOKEN"]; bad {
		t.Fatalf("已改动的 HEALTH_TOKEN 被误判：%+v", fs)
	}
}

// TestAuditMissingMainKeyOnlyWhenRealAI 主力供应商 Key 缺失只在"声明走真实 AI"时算缺口：
// AI_MOCK_MODE=true（本机九套冒烟形态）留空是设计内，不得报红。
// 摘掉这条 mock 短路 ⇒ 本机回归当场多一条结论，故反向对照必须同时在位。
func TestAuditMissingMainKeyOnlyWhenRealAI(t *testing.T) {
	env := cleanEnv()
	env["SILICONFLOW_API_KEY"] = ""
	env["AI_MOCK_MODE"] = "true"
	if fs := AuditCredentialPlaceholders(lookupOf(env)); len(fs) != 0 {
		t.Fatalf("模拟态空主力 Key 属设计内，不应有结论：%+v", fs)
	}
	env["AI_MOCK_MODE"] = "false"
	if reasonsOf(AuditCredentialPlaceholders(lookupOf(env)))["SILICONFLOW_API_KEY"] != "missing_main" {
		t.Fatalf("真实 AI + 空主力 Key 必须判 missing_main")
	}
}

// TestAuditMissingPair 三条增强链路的成对判据：URL 配了 Key 没配 ⇒ 缺的那半被点名；
// 整条都没配 ⇒ 无结论（"没这功能"不是配置错）。
func TestAuditMissingPair(t *testing.T) {
	env := cleanEnv()
	env["EMBEDDING_API_KEY"] = ""
	if reasonsOf(AuditCredentialPlaceholders(lookupOf(env)))["EMBEDDING_API_KEY"] != "missing_pair" {
		t.Fatalf("EMBEDDING 只配 URL 未配 Key 必须判 missing_pair")
	}
	// 反向对照：URL 也清掉 ⇒ 整条关闭，不该报任何东西
	env["EMBEDDING_API_URL"] = ""
	if fs := AuditCredentialPlaceholders(lookupOf(env)); len(fs) != 0 {
		t.Fatalf("整条链路未启用不应有结论：%+v", fs)
	}
	// 占位 Key 等同于没配：URL 真实 + Key=your-xxx 必须被点名，且**只点一次**
	// （同一条缺口在观测面刷两行会让"结论条数"这种等值断言失真，也让运维以为是两件事）
	env2 := cleanEnv()
	env2["RERANK_API_KEY"] = "your-rerank-key"
	fs2 := AuditCredentialPlaceholders(lookupOf(env2))
	hits := 0
	for _, f := range fs2 {
		if f.Key == "RERANK_API_KEY" {
			hits++
			if f.Reason != "placeholder" {
				t.Fatalf("RERANK 占位 Key 应报 placeholder（比 missing_pair 更有指向性），实得 %s", f.Reason)
			}
		}
	}
	if hits != 1 {
		t.Fatalf("RERANK 占位 Key 应恰好一条结论，实得 %d 条：%+v", hits, fs2)
	}
}

// TestAuditRerankFallsBackToEmbeddingKey RERANK 只配 URL、Key 留空是**文档写明的合法形态**
// （rerank.go 复用 EmbeddingKey，且有单测锁该行为）——观测位若把它报成半配，就是在给合法部署刷红。
// 反向对照：回落键也没配时必须报，且报的是缺的那一半（RERANK_API_KEY）。
func TestAuditRerankFallsBackToEmbeddingKey(t *testing.T) {
	env := cleanEnv()
	env["RERANK_API_KEY"] = ""
	if fs := AuditCredentialPlaceholders(lookupOf(env)); len(fs) != 0 {
		t.Fatalf("rerank 走回落键属合法形态，不应有结论：%+v", fs)
	}
	// 回落键本身是占位值 = 等于没配：必须报，否则"URL 真拨号 + 全程 401"又回到静默失败
	env["EMBEDDING_API_KEY"] = "your-embedding-key"
	fs := AuditCredentialPlaceholders(lookupOf(env))
	if reasonsOf(fs)["RERANK_API_KEY"] != "missing_pair" {
		t.Fatalf("回落键为占位时 rerank 必须判 missing_pair，实得 %+v", fs)
	}
}

// TestAuditSMTPHalfConfigured SMTP 只填 host/user 不填口令是本批最直接的那类缺陷：
// readiness 的 smtp 位按 HOST&&USER 判"configured"，真实发信却在登录那步天天失败且不报错
// （到期邮件/用量预警/催缴三条链路全走这里）。所以口令缺口必须进成对判据。
// 反向对照：整条没配（host 也空）属设计内关闭，不得报。
func TestAuditSMTPHalfConfigured(t *testing.T) {
	env := cleanEnv()
	env["SMTP_PASS"] = ""
	if reasonsOf(AuditCredentialPlaceholders(lookupOf(env)))["SMTP_PASS"] != "missing_pair" {
		t.Fatalf("host 已配而口令为空必须判 missing_pair")
	}
	env["SMTP_HOST"] = ""
	env["SMTP_USER"] = ""
	env["SMTP_FROM"] = ""
	if fs := AuditCredentialPlaceholders(lookupOf(env)); len(fs) != 0 {
		t.Fatalf("SMTP 整条未启用不应有结论：%+v", fs)
	}
}

// TestTemplateDefaultsMatchEnvExample 出厂默认表必须与 .env.example 的字面量逐字相等。
// 立这条的原因就是本批踩的坑：templateDefaults 凭记忆写成 local-dev-health-token-change-me，
// 而模板真值是 local-dev-health-2026——两边不等 ⇒ "整片没改过"的 HEALTH_TOKEN 被判成已回填，
// 体检在这个最该红的键上永远绿。改模板必须同步改表，否则这条红。
func TestTemplateDefaultsMatchEnvExample(t *testing.T) {
	src, err := os.ReadFile("../.env.example")
	if err != nil {
		t.Fatalf("读取 .env.example 失败：%v", err)
	}
	re := regexp.MustCompile(`(?m)^([A-Z0-9_]+)=(.*)$`)
	vals := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		vals[m[1]] = strings.TrimSpace(m[2])
	}
	for k, want := range templateDefaults {
		got, ok := vals[k]
		if !ok {
			t.Fatalf("模板 .env.example 里没有键 %s，templateDefaults 却在判它——清单与模板漂移", k)
		}
		if got != want {
			t.Fatalf("templateDefaults[%s]=%q 与 .env.example 真值 %q 不一致：出厂默认没被改过这件事将永远判不出来", k, want, got)
		}
		// 出厂默认值本身必须真的"看起来像默认值"（含 change/me/dev 之类哨兵或年份尾巴），
		// 否则说明这张表被填成了某个真值，逐字比较就变成"要求所有人改凭据"。
		if !strings.Contains(strings.ToLower(got), "dev") && !strings.Contains(strings.ToLower(got), "change") {
			t.Fatalf("templateDefaults[%s] 不含出厂哨兵词，疑似误写真凭据：%q", k, got)
		}
	}
}

// TestAuditNilLookupFallsBackToProcessEnv lookup 传 nil 时回落 os.Getenv，
// 且回落前必须把被测键钉死（否则本机导过 .env 会让结论漂移）。
func TestAuditNilLookupFallsBackToProcessEnv(t *testing.T) {
	t.Setenv("SILICONFLOW_API_KEY", "sk-your-siliconflow-key")
	t.Setenv("AI_MOCK_MODE", "true")
	for _, k := range []string{"DEEPSEEK_API_KEY", "ZHIPU_API_KEY", "GLM_API_KEY", "EMBEDDING_API_URL",
		"EMBEDDING_API_KEY", "RERANK_API_URL", "RERANK_API_KEY", "LLM_GATEWAY_URL", "LLM_GATEWAY_TOKEN",
		"SMTP_HOST", "SMTP_USER", "SMTP_PASS", "SMTP_FROM", "HEALTH_TOKEN", "COLLECTOR_KEY"} {
		t.Setenv(k, "")
	}
	fs := AuditCredentialPlaceholders(nil)
	if reasonsOf(fs)["SILICONFLOW_API_KEY"] != "placeholder" {
		t.Fatalf("nil lookup 未回落进程环境：%+v", fs)
	}
}

// TestAuditSortsAndNeverLeaksValues 结论按键名有序（观测面读数可比对），且任何结论串里
// 都不允许出现环境变量的真实值——这是"体检只报键名"这条纪律的机器锁。
func TestAuditSortsAndNeverLeaksValues(t *testing.T) {
	env := cleanEnv()
	env["SMTP_PASS"] = "s3cret-smtp-pass"
	env["LLM_GATEWAY_URL"] = "" // 造成 URL 缺、Token 在 ⇒ missing_pair
	env["LLM_GATEWAY_TOKEN"] = "s3cret-gateway-token"
	fs := AuditCredentialPlaceholders(lookupOf(env))
	names := make([]string, 0, len(fs))
	for _, f := range fs {
		names = append(names, f.Key+"|"+f.Reason)
		if strings.Contains(f.Key, env["SMTP_PASS"]) || strings.Contains(f.Reason, env["SMTP_PASS"]) {
			t.Fatalf("结论里出现了凭据值：%+v", fs)
		}
	}
	// 键名序（sort.Slice 按 Key）：抽出键名单独验
	keys := []string{}
	for _, f := range fs {
		keys = append(keys, f.Key)
	}
	asc := append([]string(nil), keys...)
	sort.Strings(asc)
	for i := range keys {
		if keys[i] != asc[i] {
			t.Fatalf("结论未按键名排序：%v", keys)
		}
	}
}

// TestCredentialKeysCoverEnvReads 反向锁（本文件最有价值的一条）：
// config.go 里读取的每一个"凭据型"键，都必须出现在体检清单（credentialKeys/pairedChains）
// 或显式豁免表里。缺它就会出现"加了新供应商 Key，体检永远看不见它"这种结构性假绿。
func TestCredentialKeysCoverEnvReads(t *testing.T) {
	src, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatalf("读取 config.go 失败：%v", err)
	}
	// 只认 getEnv("KEY", …) 的第一参数，且键名以凭据后缀结尾或含 PASSWORD/SECRET/TOKEN/KEY
	re := regexp.MustCompile(`getEnv\("([A-Z0-9_]+)"`)
	credSuffix := regexp.MustCompile(`(_KEY|_TOKEN|_PASSWORD|_PASS|_SECRET)$`)
	covered := map[string]bool{}
	for _, k := range credentialKeys {
		covered[k] = true
	}
	for _, p := range pairedChains {
		covered[p[0]] = true
		covered[p[1]] = true
	}
	// 豁免表：逐条写理由（与鉴权链白名单同一口径——不留理由的豁免等于没审）
	exempt := map[string]string{
		"JWT_SECRET":     "另有独立强度守卫（弱密钥占位判据 + config 层最小长度），不重复口径",
		"DB_PASSWORD":    "deploy_preflight 按模板值 dev123 单独判级（主机面，非外部凭据面）",
		"REDIS_PASSWORD": "可选组件口令，空值属设计内单机形态（多实例必开 Redis 由 readiness 的 redis_declared_but_down 判）",
		"COLLECTOR_URL":  "URL 不是凭据，且采集链路无成对 Key 语义",
	}
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		k := m[1]
		if !credSuffix.MatchString(k) {
			continue
		}
		seen[k] = true
		if covered[k] {
			continue
		}
		if _, ok := exempt[k]; ok {
			continue
		}
		t.Fatalf("config.go 读取了凭据键 %s，但体检清单没有它（既不在 credentialKeys/pairedChains，也无豁免理由）", k)
	}
	// 反向对照：豁免表里**带凭据后缀**的键必须真在源码里被读到，
	// 防止"为了过锁随手加一条豁免"（不带凭据后缀的键本来就不会进判定，无需反向核对）。
	for k := range exempt {
		if !credSuffix.MatchString(k) {
			continue
		}
		if !seen[k] {
			t.Fatalf("豁免表登记了 %s，但 config.go 并不读它——豁免已失效，请删除该条", k)
		}
	}
	// 体检清单里的键也必须真被读取，防止清单留着已退役的键名让观测面报出无意义缺口。
	// 取数面是**并集**：config.go 的 getEnv("K") 与 internal/notify 的 os.Getenv("K")
	// （邮件四件套不进 config 结构体，由 notifier 直读进程环境）。
	mustRead := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		mustRead[m[1]] = true
	}
	notifySrc, err := os.ReadFile("../internal/notify/notifier.go")
	if err != nil {
		t.Fatalf("读取 notifier.go 失败：%v", err)
	}
	for _, m := range regexp.MustCompile(`os\.Getenv\("([A-Z0-9_]+)"\)`).FindAllStringSubmatch(string(notifySrc), -1) {
		mustRead[m[1]] = true
	}
	for _, k := range credentialKeys {
		if !mustRead[k] {
			t.Fatalf("体检清单里的 %s 在 config.go / internal/notify 均不读取，清单需与取数点对齐", k)
		}
	}
}
