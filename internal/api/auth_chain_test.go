// 鉴权链结构断言（2026-09-24 收口「结构性弱点 4：鉴权链没有 CI 断言」）
//
// 这一层此前只有两份"人写的清单"：route_auth.go 的 RecordAuth（注册期登记）与
// smoke_perm.sh 的角色矩阵。人写的清单会漂——新增端点忘了 RecordAuth、或者登记成
// "jwt" 却把注册语句挪到了 v1.Use(JWTAuth()) 之前（gin 按注册时刻快照中间件链，
// 挪一行就等于把端点变成匿名可读面，而登记与契约看起来都正常）。本文件钉住三件事：
//
//  1. 覆盖：路由表里每一条都必须在注册期登记过鉴权链（ResolveRouteAuth 命中）。
//     未登记的端点在契约里会回退成"按路径猜"，那正是当初立 RecordAuth 要拆掉的东西。
//  2. 策略：每条端点至少有一道闸（登录态 / 角色 / api_key / 签名 / 独立 Key）；
//     确实要公开的必须逐条出现在 openSurface 里并写明**为什么**能公开。
//     这份表就是匿名攻击面清单——新增可读面若无人签字，这里就红。
//  3. 对照：登记为 jwt 的端点，空手打不进去。测的不是清单，是**运行时中间件链本身**。
//
// 不依赖数据库：断言全在 JWTAuth 之内收口，所以无 PG 的机器上同样有效（不是"跳过即绿"）。
package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// gateTokens 认可的"闸"，口径与 route_auth.go 的登记词表一致。
// 有意不含 optional_jwt 与 readonly_write：前者放行匿名（真正的门在 handler 内自证身份），
// 后者只是写保护（能读）。这两类端点必须逐条进 openSurface 说清楚，不能靠一个
// "看起来像鉴权"的名字蒙过去。
var gateTokens = []string{
	"jwt",
	"admin_required",
	"super_required",
	"org_manage",
	"api_key",
	"collector_key",
	"channel_signature",
	"webhook_signature",
}

// openSurface 匿名可达面：键 "METHOD /path"，值为什么可以公开（写不出理由就别加进来）。
var openSurface = map[string]string{
	"POST /api/v1/auth/login":                "登录入口本身；防爆破由三重限流+账号锁定承担",
	"POST /api/v1/auth/register":             "注册入口；防薅由 Turnstile+IP 限流+registration_review 承担",
	"GET /api/v1/auth/register-config":       "只回注册开关形态，无业务数据",
	"POST /api/v1/auth/email-code":           "注册邮箱验证码下发，Turnstile+限流",
	"POST /api/v1/auth/reset-password":       "找回密码入口，只向已绑定邮箱发码",
	"POST /api/v1/auth/verify-reset-code":    "校验重置码，成功才换发 token",
	"GET /api/v1/auth/reset-channel":         "只回通道种类 smtp|log",
	"POST /api/v1/tenant/signup":             "租户入驻申请，限流+审核态全拦",
	"GET /api/v1/tenant/check-code":          "企业码存在性校验，只回布尔",
	"GET /api/v1/plans":                      "公开定价",
	"GET /api/v1/packages":                   "公开商业包目录",
	"GET /api/v1/public/branding":            "白标品牌配置，按 Host 解析且只回公开字段",
	"POST /api/v1/chat/unauthorized":         "C 端匿名对话：visitor_key 自证 + Turnstile + IP 限流",
	"POST /api/v1/chat/test":                 "上一条的 deprecated 别名，同闸同限流",
	"POST /api/v1/chat/guest":                "访客身份签发，建的是访客客户",
	"POST /api/v1/chat/welcome":              "会话欢迎语，秒回无 AI",
	"GET /api/v1/chat/history":               "匿名须 visitor_key 自证（OptionalJWTAuth 只用于 B 端放行）",
	"POST /api/v1/chat/clear-delay":          "匿名须 visitor_key 自证；登录态须为归属顾问",
	"POST /api/v1/chat/request-human":        "C 端找人工，visitor_key 自证 + 限流",
	"POST /api/v1/privacy/deletion-request":  "PIPL 删除权受理，visitor_key 自证（C 端入口）",
	"POST /api/v1/client-errors":             "前端异常上报，只入库、采样、脱敏",
	"GET /api/v1/turnstile/sitekey":          "人机验证站点键本就是公开展示用",
	"GET /api/v1/knowledge/brands":           "公开产品目录（visibility=public 已在 SQL 层收敛）",
	"GET /api/v1/knowledge/models":           "公开产品目录",
	"GET /api/v1/knowledge/models/:id":       "公开产品目录详情",
	"GET /api/v1/knowledge/compares":         "公开竞品对比",
	"GET /api/v1/knowledge/fragments/search": "公开片段检索（私有可见性已排除）",
	"GET /api/v1/acquisition/:code":          "活码解析：码即凭证，只回最小跳转字段 + IP 限流",
	"POST /api/v1/acquisition/:code/scan":    "扫码计数：只写事件表，IP 限流",
	"GET /api/v1/openapi/spec":               "E10 对外接口文档规格，内容本身即公开物",
	"GET /api/v1/ws/advisor":                 "WS 握手：token 在 handler 内按 query 手动校验（浏览器 WS 带不了 Authorization）",
	"GET /api/v1/ws/client":                  "WS 握手：同上，访客身份亦在 handler 内自证",
}

// newAuthProbeRouter 用与生产完全相同的入口装配路由树（RegisterRoutes 内部就是那台
// 顺序敏感的鉴权机器），测试不额外加也不减任何中间件。
func newAuthProbeRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterRoutes(r)
	return r
}

// fakeParam 把 gin 的路径参数占位替换成可请求的字面值（探针只关心能否穿过鉴权链）。
func fakeParam(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if strings.HasPrefix(p, ":") {
			parts[i] = "1"
		} else if strings.HasPrefix(p, "*") {
			parts[i] = "x"
		}
	}
	return strings.Join(parts, "/")
}

// undeclaredRoutes 列出路由表中未在注册期登记鉴权链的端点。
func undeclaredRoutes(r *gin.Engine) []string {
	var out []string
	for _, rt := range r.Routes() {
		if _, ok := ResolveRouteAuth(rt.Method, rt.Path); !ok {
			out = append(out, rt.Method+" "+rt.Path)
		}
	}
	return out
}

// ungatedRoutes 列出既无鉴权闸、也未登记公开理由的端点；stale 反向列出清单里已不存在的条目。
func openSurfaceGaps(r *gin.Engine) (ungated, stale []string) {
	routes := map[string]bool{}
	for _, rt := range r.Routes() {
		key := rt.Method + " " + rt.Path
		routes[key] = true
		mws, _ := ResolveRouteAuth(rt.Method, rt.Path)
		if slices.ContainsFunc(mws, func(m string) bool { return slices.Contains(gateTokens, m) }) {
			continue
		}
		if openSurface[key] == "" {
			ungated = append(ungated, fmt.Sprintf("%s（登记链: %s）", key, strings.Join(mws, ",")))
		}
	}
	for key := range openSurface {
		if !routes[key] {
			stale = append(stale, key)
		}
	}
	return
}

// jwtLeaks 对每条登记为 jwt 的端点空手打一枪，返回真的拿到 2xx 的那些（= 数据可被匿名读取）。
// 第二个返回值是被探测的条数，用于反证"探针没有空转"。
func jwtLeaks(r *gin.Engine) (leaked []string, checked int) {
	for _, rt := range r.Routes() {
		mws, ok := ResolveRouteAuth(rt.Method, rt.Path)
		if !ok || !slices.Contains(mws, "jwt") {
			continue
		}
		checked++
		req := httptest.NewRequest(rt.Method, fakeParam(rt.Path), strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code == http.StatusUnauthorized {
			continue
		}
		// 非 401 也可能是更靠前的闸拦下（角色闸/限流 429），那同样是"没进去"；
		// 只有 2xx 才算真漏——不带 token 就读到了数据。
		if w.Code >= 200 && w.Code < 300 {
			leaked = append(leaked, fmt.Sprintf("%s %s -> %d %s", rt.Method, rt.Path, w.Code, trimBody(w.Body.String())))
		}
	}
	return
}

// TestAuthChainEveryRouteDeclared 覆盖断言：路由表里没有"未登记鉴权"的端点。
func TestAuthChainEveryRouteDeclared(t *testing.T) {
	missing := undeclaredRoutes(newAuthProbeRouter(t))
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("%d 条端点未在注册期登记鉴权链（请在其 register* 处补 RecordAuth，口径见 route_auth.go）: %v", len(missing), missing)
	}
}

// TestAuthChainOpenSurfacePolicy 策略断言：无闸端点必须逐条写明公开理由；
// 同时 openSurface 里不得躺着已不存在的条目——清单腐烂与清单缺失一样危险。
func TestAuthChainOpenSurfacePolicy(t *testing.T) {
	ungated, stale := openSurfaceGaps(newAuthProbeRouter(t))
	if len(ungated) > 0 {
		sort.Strings(ungated)
		t.Fatalf("%d 条端点既无鉴权闸也未登记公开理由——匿名可达面必须逐条评审: %v", len(ungated), ungated)
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Fatalf("openSurface 里 %d 条已对不上路由表（端点已改名/下线，清单要同步）: %v", len(stale), stale)
	}
}

// TestAuthChainJWTGateActuallyFires 对照断言：登记为 jwt 的端点，空手打不进去。
// 这条才是"鉴权链 CI 断言"本体——它跑的是真实注册顺序，登记与链不符立刻现形。
func TestAuthChainJWTGateActuallyFires(t *testing.T) {
	r := newAuthProbeRouter(t)
	leaked, checked := jwtLeaks(r)
	// 反向自证：探针确实覆盖了规模级别的端点。若登记口径整体失守（一条 jwt 都没匹配上），
	// 下面的循环会安静地零请求零失败——那比不测更糟。
	if checked*2 < len(r.Routes()) {
		t.Fatalf("登记为 jwt 的端点 %d 条，不足路由总数 %d 的一半：登记口径或路由装配已变，本用例等于空转", checked, len(r.Routes()))
	}
	if len(leaked) > 0 {
		sort.Strings(leaked)
		t.Fatalf("%d 条登记为登录态的端点在无 Token 时返回了 2xx: %v", len(leaked), leaked)
	}
}

// TestAuthChainDetectorsAreNotVacuous 反证用例：三条检测器各自在"故意做坏"的路由上必须真的红。
// 只跑正向用例分不出"链是好的"与"检测器根本没在看"——和 internal/outreach 的 isolate 反向测同理。
func TestAuthChainDetectorsAreNotVacuous(t *testing.T) {
	// ① 新增端点不登记鉴权 → 覆盖检测必须报
	r1 := newAuthProbeRouter(t)
	r1.POST("/api/v1/__probe_undeclared", func(c *gin.Context) { c.JSON(200, gin.H{"code": 0}) })
	if got := undeclaredRoutes(r1); len(got) != 1 || !strings.Contains(got[0], "__probe_undeclared") {
		t.Fatalf("未登记端点没被覆盖检测抓到: %v", got)
	}

	// ② 新增公开端点不写理由 → 策略检测必须报
	r2 := newAuthProbeRouter(t)
	RecordAuth("POST", "/api/v1/__probe_open", "public")
	defer delete(routeAuthRegistry, "POST /api/v1/__probe_open")
	r2.POST("/api/v1/__probe_open", func(c *gin.Context) { c.JSON(200, gin.H{"code": 0}) })
	if ungated, _ := openSurfaceGaps(r2); !strings.Contains(strings.Join(ungated, "|"), "__probe_open") {
		t.Fatalf("匿名可达的新端点没被策略检测抓到: %v", ungated)
	}

	// ③ 登记成 jwt 但链上其实没挂 JWTAuth → 行为探针必须看到 2xx 泄露
	r3 := newAuthProbeRouter(t)
	RecordAuth("GET", "/api/v1/__probe_moved", "jwt")
	defer delete(routeAuthRegistry, "GET /api/v1/__probe_moved")
	r3.GET("/api/v1/__probe_moved", func(c *gin.Context) { c.JSON(200, gin.H{"code": 0, "data": "客户名单"}) })
	leaked, checked := jwtLeaks(r3)
	if !strings.Contains(strings.Join(leaked, "|"), "__probe_moved") {
		t.Fatalf("假登记的登录态端点空手打进了 2xx，行为探针却没报（探针空转？）: %v", leaked)
	}
	// 探针真的走了一遍新端点：探测条数必须比干净路由表多 1（否则登记没被读到，红也是假红）
	_, cleanChecked := jwtLeaks(newAuthProbeRouter(t))
	if checked != cleanChecked+1 {
		t.Fatalf("探针覆盖条数 %d ≠ 干净路由 %d + 新端点 1，说明登记未生效", checked, cleanChecked)
	}
}

// trimBody 摘要化响应体，失败日志里只留前 120 字节。
func trimBody(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}
