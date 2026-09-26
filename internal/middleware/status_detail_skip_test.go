// Package middleware 平台级观测端点免租户解析白名单测试（FIX-7，2026-09-27）。
package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-scrm/internal/db"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 为什么单独钉这一条
//
// /status/detail 是"部署体检清单"，运维脚本与冒烟都靠它读配置红灯。但它注册在**根路由**上、
// 又不在 skipTenantPaths 里，于是 release 模式下按 IP/未绑定域名探针打进来时
// resolveTenant 返回 nil → 403「无法识别访问租户」——观测面自己变成不可观测。
// 本地从没人发现，因为 9090 那台跑 debug，isLocalDevHost 兜底把这条腿永久遮住了；
// 冒烟也就从来没红过。这与当年 /metrics 被 Host 解析拦成 403 是同一类错。
//
// 判据选择：TenantResolver 在 db.DB==nil 时只放行 skipTenantPaths 里的路径（其余 503），
// 所以"是否在白名单里"可以在**零数据库**条件下精确读出——不需要真连库、不需要真租户，
// 也就没有环境依赖导致的假绿/假红。503 与 403 的区分本身就是"走没走租户解析"的证据。
// 反证（删掉白名单条目即红）：见 smoke_redis.sh 里"detail 解析器能读到已知检查名"那条前置自检，
// 以及本用例的 200/503 对照——两者都只依赖白名单成员身份。
// ============================================================

// newSkipPathEngine 构造只挂 TenantResolver 的最小引擎：所有被测路径回 200，
// 于是响应码完全由中间件决定（200=放行，503=因"无 DB 且不在白名单"被拒）。
func newSkipPathEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(TenantResolver())
	ok := func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"code": 0, "data": "passed"}) }
	r.GET("/status", ok)
	r.GET("/status/detail", ok)
	r.GET("/metrics", ok)
	r.GET("/health", ok)
	r.GET("/api/v1/customers", ok)
	return r
}

// TestPlatformObservabilityPathsSkipTenantResolution 断言平台级观测端点免租户解析，
// 而租户作用域端点必须被拦（对照组防"把整张网放开"这种绿着出事的实现）。
func TestPlatformObservabilityPathsSkipTenantResolution(t *testing.T) {
	// 本用例只验白名单成员身份，故意在"无 DB"下跑：非白名单路径会被 fail-closed 成 503。
	prev := db.DB
	db.DB = nil
	t.Cleanup(func() { db.DB = prev })

	r := newSkipPathEngine()
	cases := []struct {
		path   string
		want   int
		reason string
	}{
		// 平台级观测面：自身有鉴权（/status 公开无清单、/status/detail 靠 X-Health-Token、/metrics 靠 METRICS_TOKEN），
		// 响应里没有任何租户字段，绝不该再叠一层"按 Host 猜租户"。
		{"/status", http.StatusOK, "公开存活探针"},
		{"/status/detail", http.StatusOK, "全量体检清单（FIX-7 新增：release 下按 IP 探针必须可达）"},
		{"/metrics", http.StatusOK, "Prometheus 抓取端点"},
		{"/health", http.StatusOK, "存活探针"},
		// 对照组：租户作用域路径在无 DB 时必须被 fail-closed 拦住，
		// 否则上面四条"放行"就只是"整个中间件被拆了"的假绿。
		{"/api/v1/customers", http.StatusServiceUnavailable, "租户业务数据端点"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		req.Host = "10.0.0.8:9093" // 刻意用裸 IP：生产探针就是这个形态，不靠 localhost 兜底
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != c.want {
			t.Fatalf("%s 期望 %d，实际 %d（body=%s）", c.path, c.want, w.Code, w.Body.String())
		}
	}
}

// TestStatusDetailIsNotPrefixWhitelisted 负向封堵：放行 /status/detail 必须是**精确匹配**，
// 不能写成 "/status" 前缀放行——后者会把 /status/anything 全族一起放过租户解析。
// （白名单里已经有一批前缀放行项，多一条前缀很容易，所以这里显式钉住。）
func TestStatusDetailIsNotPrefixWhitelisted(t *testing.T) {
	prev := db.DB
	db.DB = nil
	t.Cleanup(func() { db.DB = prev })

	r := newSkipPathEngine()
	r.GET("/status/leak", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"code": 0}) })
	req := httptest.NewRequest(http.MethodGet, "/status/leak", nil)
	req.Host = "10.0.0.8:9093"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Fatal("/status/leak 被放行了：白名单把 /status 当作了前缀匹配，放行面比设计更宽")
	}
}
