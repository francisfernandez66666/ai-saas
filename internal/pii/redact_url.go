// 本文件补 URL/错误串里的凭据脱敏（2026-09-26 全量审计 FIX-2）。
package pii

import "regexp"

// ============================================================
// 凭据型 URL 脱敏（P0-2，2026-09-26）
//
// 为什么单独一张表：Go 的传输层错误（*url.Error）Error() 里带**完整 URL 含 query**，
// 而通道侧换取 access_token 的 URL 形如
// `https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid=ww...&corpsecret=abcdef`，
// 于是"取 token 失败"这一条错误串一旦落库/打日志/回显，客户的应用密钥就明文出现在
// 管理端死信列表与 ai-scrm.log 里（本仓实锤链路见 FIX-2 §〇.2 第 3 条）。
//
// 口径：
//   - 只替换**参数值**，保留 scheme/host/path 与其它参数（运维要靠它判断是 token 失效、
//     网络不可达还是 SSRF 闸拒绝——一刀切成 *** 会让"为什么发不出去"变成猜）；
//   - 按参数名精确匹配，不做"看起来像密钥"的长度/字符集猜测（防误伤正文里的数字串）；
//   - 值的终止集合含 `&`、空白、引号、反斜杠、`}`、`]`——url.Error 用双引号包住 URL，
//     JSON 打印又可能带转义，终止符少算一种就会脱敏失败。
// ============================================================

// secretParamRE 匹配 query 里"参数名=值"形式的凭据字段（大小写不敏感）。
// 参数名单：企微 corpsecret/appsecret、公众号 secret、各类 access_token 与 jsapi_ticket、
// 以及通用 password/api_key/apikey。刻意不含 `key` 单词——通道层有 cache_key/page_key 等
// 非凭据参数，宽匹配会把排障线索一起抹掉。
var secretParamRE = regexp.MustCompile(`(?i)([?&;](?:corpsecret|appsecret|secret|access_token|jsapi_ticket|password|passwd|api_key|apikey)=)([^&;\s"'\\\}\]]+)`)

// RedactSecretURL 把文本里凭据参数的**值**替成 ***，其余原样保留。
// 输入可以是完整 URL，也可以是包着 URL 的错误串/JSON 片段——它按参数名匹配，不要求整体可解析。
// 幂等：已是 *** 的再跑一次仍是 ***（不叠加星号）。
func RedactSecretURL(s string) string {
	if s == "" {
		return s
	}
	return secretParamRE.ReplaceAllString(s, "${1}***")
}

// ContainsSecretParam 判定文本是否仍含**非空**凭据参数值，供护栏/清洗脚本自检用
// （脱敏后应为 false；脱敏前的死信行为 true）。值为空串（`secret=`）不算泄露。
func ContainsSecretParam(s string) bool {
	if s == "" {
		return false
	}
	for _, m := range secretParamRE.FindAllStringSubmatch(s, -1) {
		if len(m) > 2 && m[2] != "" && m[2] != "***" {
			return true
		}
	}
	return false
}
