// 外部凭据回填体检（FIX-N，2026-09-28 .env 丢失处置批）
//
// 为什么会需要这个文件：本机 .env 被误删后只能从模板重建，于是**所有外部凭据一夜回到占位值**
// （模型 Key、SMTP、embedding、独立网关 token），而系统照常启动、照常 200——
// 唯一的症状是"第一次真调 AI/真发信时才报错"。这类失败安静且滞后：
// 冒烟全程跑在 AI_MOCK_MODE=true 上照样全绿，运维从 `/status` 看到的是
// "ai_real_call=real"（因为 mock 关掉了）、"smtp=configured"（因为环境变量**存在**），
// 而真实值是 `sk-your-siliconflow-key` 这种模板串。**存在≠已回填**，旧判据只判"非空"。
//
// 本文件把"非空"升级成三档判据，并且**只回键名与原因码、绝不回任何值**（值可能是真凭据，
// 一旦进日志/接口就是泄露面）。三档分别是：
//   - placeholder：值仍是模板占位形态（your-/sk-your/change-me/xxxx/placeholder/TODO 等）；
//   - template_default：值与 .env.example 出厂默认逐字相同（HEALTH_TOKEN 这类，改了值才算数）；
//   - missing_pair：URL 与 Key 只配了一半（EMBEDDING/RERANK/LLM_GATEWAY 三条链路共用该形态，
//     后果不是"没这功能"而是"看着要用了、每请求必失败"）。
//
// 判据本体是纯函数（lookup 可注入），单测逐键 t.Setenv 钉死——凡"从环境变量装配再判定"的
// 用例，被测字段依赖的每个键都要显式钉住（含钉成空串），否则本机导过 .env 与 CI 干净环境
// 会给同一条断言相反结论。
package config

import (
	"os"
	"sort"
	"strings"
)

// CredentialFinding 一条凭据体检结论：键名 + 原因码（不含值）。
type CredentialFinding struct {
	Key    string `json:"key"`    // 环境变量名，如 SILICONFLOW_API_KEY
	Reason string `json:"reason"` // placeholder / template_default / missing_pair
}

// placeholderPrefixes 模板占位形态（小写比较，命中即判未回填）。
// 口径：只收录 .env.example 里真的出现过、或人手动留哨兵的写法，别把正常域名误判成占位。
var placeholderPrefixes = []string{
	"your-", "your_", "sk-your", "change-me", "change_me",
	"changeme", "placeholder", "xxxx", "todo", "<",
}

// templateDefaults 出厂默认值逐字表：这些字符串一旦等于现值，说明键从没被改过。
// 只收"哨兵型"默认值（HEALTH_TOKEN / LLM_GATEWAY_TOKEN），DB_PASSWORD=dev123 之类
// 已由 deploy_preflight 的资金/主机面单独判级，此处不重复造第二套口径。
// ⚠ 值必须与 .env.example 的**字面量逐字相同**（2026-09-28 首校对：原先凭记忆写成
// local-dev-health-token-change-me / change-me-gateway-token，两个都不是模板里的真值，
// 于是"整片没改过"的 HEALTH_TOKEN 被判成已回填——观测位自己在撒谎）。
// envaudit_test.go 有一条直接读 .env.example 的对账用例钉住这件事，改模板必须同步改这里。
var templateDefaults = map[string]string{
	"HEALTH_TOKEN":      "local-dev-health-2026",
	"LLM_GATEWAY_TOKEN": "change-me-gateway-shared-secret",
}

// credentialKeys 需要判"占位"的外部凭据键（键名与 config.go 的读取点一一对应，
// 改读取点必须同步这里——envaudit_test.go 有反向锁：出现在读取点却没进体检清单即判红）。
var credentialKeys = []string{
	"SILICONFLOW_API_KEY",
	"DEEPSEEK_API_KEY",
	"ZHIPU_API_KEY",
	"GLM_API_KEY",
	"EMBEDDING_API_KEY",
	"RERANK_API_KEY",
	"SMTP_HOST",
	"SMTP_USER",
	"SMTP_PASS",
	"SMTP_FROM",
	"HEALTH_TOKEN",
	"LLM_GATEWAY_TOKEN",
	"COLLECTOR_KEY",
}

// pairedChains "必须成对配置"的链路：[0]=URL/宿主键，[1]=凭据键，[2]=该凭据键的**回落键**（可空）。
// 声明了 URL（表示想用这条增强）却没配 Key，或反过来配了 Key 却忘了 URL，
// 都比"整条没配"更坏——前者实现会真的去拨号，每个请求吃一次失败。
// [2] 不是可选项而是**语义**：RERANK_API_KEY 留空时代码复用 EMBEDDING_API_KEY
// （internal/service/rerank.go 的 token 取值链，有单测锁），只配 embedding Key 的 rerank
// 是文档写明的合法形态——把它判成半配就是观测位自己造红。
// SMTP 那一对照着来：notifier 只按 HOST/USER 判"已配置"（readiness 的 smtp 位同判据），
// 于是 host+user 填了、口令忘了写的机器会天天报"SMTP 已配置"而每一封邮件都在登录那步失败。
var pairedChains = [][3]string{
	{"EMBEDDING_API_URL", "EMBEDDING_API_KEY", ""},
	{"RERANK_API_URL", "RERANK_API_KEY", "EMBEDDING_API_KEY"},
	{"LLM_GATEWAY_URL", "LLM_GATEWAY_TOKEN", ""},
	{"SMTP_HOST", "SMTP_PASS", ""},
}

// IsPlaceholderCredential 判值是否仍是模板占位形态（空串同样算"没回填"）。
// 只判形态不判内容长度，长度由调用方的强度校验负责（JWT_SECRET 有独立守卫）。
func IsPlaceholderCredential(v string) bool {
	s := strings.TrimSpace(v)
	if s == "" {
		return true
	}
	low := strings.ToLower(s)
	for _, p := range placeholderPrefixes {
		if strings.HasPrefix(low, p) {
			return true
		}
	}
	return false
}

// AuditCredentialPlaceholders 外部凭据体检（纯函数，lookup 注入便于逐键钉死）。
// 返回按键名排序的结论列表；无结论即凭据面干净（空切片而非 nil，调用方可直接判长度）。
func AuditCredentialPlaceholders(lookup func(string) string) []CredentialFinding {
	if lookup == nil {
		lookup = os.Getenv
	}
	out := []CredentialFinding{}
	seen := map[string]bool{}
	// add 单点去重：同一个键只留**第一条**结论（判序=占位 → 出厂默认 → 真实 AI 缺主力 Key → 成对缺口）。
	// 为什么必须去重（2026-09-28 首跑即撞）：占位 Key 会**同时**被"URL/Key 成对"判据算成缺失的一半，
	// 于是 RERANK_API_KEY 这种键在观测面出现两行同键不同因；而"仍是模板占位"比"没配对"更有指向性，
	// 故让占位档先写、成对档在同一键上让位。缺口的**条数**也是冒烟等值断言的口径，重复即失真。
	add := func(k, reason string) {
		if seen[k] {
			return
		}
		seen[k] = true
		out = append(out, CredentialFinding{Key: k, Reason: reason})
	}
	// 1) 占位/未回填：只报"仍是模板形态"的键，空串留给成对判据与业务侧决定
	//（DEEPSEEK 留空是设计内——降级链只有一路时本来就少一个兜底，不该报成配置错）。
	for _, k := range credentialKeys {
		v := strings.TrimSpace(lookup(k))
		if v == "" {
			continue
		}
		if IsPlaceholderCredential(v) {
			add(k, "placeholder")
			continue
		}
		if def, ok := templateDefaults[k]; ok && strings.EqualFold(v, def) {
			add(k, "template_default")
		}
	}
	// 2) 主力供应商 Key：AI_MOCK_MODE=true 时留空属设计内（本机九套冒烟即此形态），
	// 一旦声明走真实 AI（mock=false）而主力 Key 仍为空/占位，就是"全站回复必失败"——
	// 单独给 missing_main 原因码，便于观测面把它与"只是没配兜底"分开表述。
	aiMock := strings.EqualFold(strings.TrimSpace(lookup("AI_MOCK_MODE")), "true")
	if !aiMock {
		mainKey := strings.TrimSpace(lookup("SILICONFLOW_API_KEY"))
		if mainKey == "" || IsPlaceholderCredential(mainKey) {
			add("SILICONFLOW_API_KEY", "missing_main")
		}
	}
	// 3) 成对判据（含回落键：回落键可用即视为这一半已满足）
	for _, pair := range pairedChains {
		urlV := strings.TrimSpace(lookup(pair[0]))
		keyV := strings.TrimSpace(lookup(pair[1]))
		if pair[2] != "" {
			if fb := strings.TrimSpace(lookup(pair[2])); fb != "" && !IsPlaceholderCredential(fb) &&
				(keyV == "" || IsPlaceholderCredential(keyV)) {
				// 只有"本键没配、回落键配了"才借；本键配了占位值不借——占位档已在第 1 步点名
				keyV = fb
			}
		}
		urlSet := urlV != ""
		keySet := keyV != "" && !IsPlaceholderCredential(keyV)
		if urlSet != keySet {
			// 报"缺的那一半"，键名对运维才有指向性
			missing := pair[1]
			if !urlSet {
				missing = pair[0]
			}
			add(missing, "missing_pair")
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key == out[j].Key {
			return out[i].Reason < out[j].Reason
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// AuditCredentialPlaceholderKeys 便捷取键名列表（观测面展示用，只含键名与原因码拼接）。
func AuditCredentialPlaceholderKeys(lookup func(string) string) []string {
	findings := AuditCredentialPlaceholders(lookup)
	names := make([]string, 0, len(findings))
	for _, f := range findings {
		names = append(names, f.Key+":"+f.Reason)
	}
	return names
}
