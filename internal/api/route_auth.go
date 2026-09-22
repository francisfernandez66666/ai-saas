// 注册期鉴权记录：契约 golden 中 auth 字段的权威事实源（取代 cmd/apidump 按路径猜测的 authOf 启发式）。
// 在各 register*.go 的路由注册点（组级 .Use 与逐路由中间件）调用 RecordAuth 顺手记录真实中间件链，
// apidump 生成契约与 -check 对账时优先读取此处记录，而非事后按路径字符串推断。
package api

import "strings"

// routeAuthRegistry 注册期鉴权记录表。键为 "METHOD PATH"；其中 METHOD 为 "*" 时表示"组级"登记，
// 键形如 "* /prefix"，对该前缀下的所有方法生效。值为该路由真实挂载的鉴权/限流中间件名列表。
var routeAuthRegistry = map[string][]string{}

// RecordAuth 在路由注册点记录真实中间件链。method 为 HTTP 方法（如 "POST"），对整组共享中间件可传 "*"
// 表示前缀组级登记（path 为该组路径前缀）；path 为与 gin 路由表一致的完整路径（如 "/api/v1/chat/request-human"）。
// mws 中间件名口径：jwt（JWTAuth 登录态）/ admin_required（AdminRequired）/ super_required（SuperRequired）/
// org_manage（OrgManageRequired）/ readonly_write（ReadonlyWriteGuard）/ ip_limit（IPRateLimit 频控）/
// visitor_key（TurnstileGuard 人机验证）/ optional_jwt（OptionalJWTAuth）/ public（无鉴权）/
// api_key（OpenAPIAuth）/ collector_key（X-Collector-Key）/ channel_signature（渠道回调签名）/
// webhook_signature（支付回调验签）。注册点无任何中间件时传 "public"。
func RecordAuth(method, path string, mws ...string) {
	routeAuthRegistry[method+" "+path] = mws
}

// ResolveRouteAuth 解析某路由的真实鉴权链：合并所有前缀匹配的"组级"登记（method 为 "*"）
// 与该路由的精确登记（method 与 path 完全匹配），去重后返回。未登记任何中间件时返回 (nil, false)。
// 组级与精确登记叠加时，组级在前、精确在后，便于在组中追加单路由特例（如 admin 组的 super_required）。
func ResolveRouteAuth(method, path string) ([]string, bool) {
	seen := map[string]bool{}
	var out []string
	add := func(mws []string) {
		for _, m := range mws {
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	for k, mws := range routeAuthRegistry {
		// 组级登记：键形如 "* /prefix"
		if !strings.HasPrefix(k, "* ") {
			continue
		}
		prefix := strings.TrimPrefix(k, "* ")
		if matchPrefix(path, prefix) {
			add(mws)
		}
	}
	if exact, ok := routeAuthRegistry[method+" "+path]; ok {
		add(exact)
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// matchPrefix 判断 path 是否以 prefix 开头且落在路径段边界（避免 "/api/v1/admin" 误匹配 "/api/v1/administrators"）。
func matchPrefix(path, prefix string) bool {
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	if len(path) == len(prefix) {
		return true
	}
	return path[len(prefix)] == '/'
}
