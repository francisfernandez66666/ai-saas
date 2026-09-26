// Package api 分页上限口径回归（FIX-4，2026-09-27）
//
// 钉住两件事：
//  1. `/advisor/customers` 的页长必须并入全仓单点 schema.NormalizePageSize（默认 20、硬顶 100）。
//     旧实现只兜 `<=0 → 20`、**没有上限**，`?page_size=50000` 原样回显 50000——
//     P2-9 的防拖库红线在这一个端点上被单独绕过，一个登录用户即可让服务端单次拉全量客户行。
//     断的是**生效值**（等值），不是"请求 200"：状态码 200 在修复前后都一样。
//  2. `/admin/tags?all=1` 是"字典全量"语义，与"列表分页"分开：
//     租户标签 >100 时旧凑法（前端把 page_size 写成 500）会被静默截断成 100，
//     多出来的标签在界面上再也选不到且不报错——用户只会以为"标签丢了"。
//     all=1 必须一次给出该租户可见的**全部**标签（上限内），且只回 id/name/code 三列。
//
// 依赖本地 PostgreSQL（testutil.SetupTestDB，DB 不可用自动 Skip）。
package api

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/middleware"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// initPaginationRouter 组装本批两条列表路由（与生产中间件顺序等价：JWTAuth → AdminRequired）
func initPaginationRouter(tid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	grp := r.Group("/api/v1")
	grp.Use(func(c *gin.Context) { c.Set("tenant_id", tid) }, middleware.JWTAuth())
	grp.GET("/advisor/customers", GetAdvisorCustomers)
	admin := grp.Group("/admin")
	admin.Use(middleware.AdminRequired())
	admin.GET("/tags", GetTagList)
	return r
}

// adminToken 造一个租户管理员 token（过 JWTAuth + AdminRequired）
func adminToken(t *testing.T, tid uint) string {
	t.Helper()
	tok, err := middleware.GenerateToken(901, "pgadmin", model.RoleTenantAdmin, tid)
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}
	return tok
}

// getPage 打一发 GET 并把信封里的 data 解出来
func getPage(t *testing.T, r *gin.Engine, tok, path string) (map[string]interface{}, int) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s 应 200，实得 %d body=%s", path, w.Code, w.Body.String())
	}
	var env struct {
		Code int                    `json:"code"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析响应失败: %v body=%s", err, w.Body.String())
	}
	if env.Data == nil {
		t.Fatalf("响应没有 data 体: %s", w.Body.String())
	}
	return env.Data, w.Code
}

// TestAdvisorListPageSizeClamped 顾问客户列表页长钳制（含"合法值不得被误伤"反向对照）
func TestAdvisorListPageSizeClamped(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenantCode(t, "pgclamp")
	defer testutil.CleanupTenant(t, tid)

	r := initPaginationRouter(tid)
	tok := adminToken(t, tid)

	cases := []struct {
		in   string
		want float64
		why  string
	}{
		{"50000", 100, "超大页长必须钳到 100（防拖库红线）"},
		{"101", 100, "刚过界也钳（口径边界）"},
		{"100", 100, "正好 100 不动"},
		{"50", 50, "窗口内的值不得被顺手改成 100"},
		{"0", 20, "缺省仍是本端点的 20，不是通用默认的 10"},
		{"-5", 20, "负数按缺省"},
	}
	for _, cs := range cases {
		data, _ := getPage(t, r, tok, "/api/v1/advisor/customers?page_size="+cs.in)
		got, ok := data["page_size"]
		if !ok {
			t.Fatalf("page_size=%s：响应没回显 page_size，前端无从判断实际生效值", cs.in)
		}
		if f, _ := got.(float64); f != cs.want {
			t.Errorf("page_size=%s 应回显 %v（%s），实得 %v", cs.in, cs.want, cs.why, got)
		}
	}
}

// TestTagDictAllMode 标签字典 all=1：一次给全 + 只回三列 + 与分页模式的截断形成对照
func TestTagDictAllMode(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenantCode(t, "tagdict")
	defer testutil.CleanupTenant(t, tid)

	// 合成 105 条标签：跨过 100 这个分页硬顶，"截断"与"不截断"才有可观测的差
	const seeds = 105
	rows := make([]model.Tag, 0, seeds)
	for i := 0; i < seeds; i++ {
		rows = append(rows, model.Tag{
			TenantID: tid,
			Name:     fmt.Sprintf("字典标签%03d", i),
			Code:     fmt.Sprintf("dict_tag_%03d", i),
			Category: "smoke",
			Status:   1,
		})
	}
	if err := db.DB.CreateInBatches(&rows, 200).Error; err != nil {
		t.Fatalf("合成标签失败: %v", err)
	}

	r := initPaginationRouter(tid)
	tok := adminToken(t, tid)

	// 分页模式：page_size=500 被钳到 100，且**这一页装不下全部标签**——
	// 这正是要给用户交代的那个现象（旧前端在这里写 500，以为拿到了全量）。
	paged, _ := getPage(t, r, tok, "/api/v1/admin/tags?page_size=500")
	total, _ := paged["total"].(float64)
	pagedList, _ := paged["list"].([]interface{})
	if p, _ := paged["page_size"].(float64); p != 100 {
		t.Fatalf("分页模式 page_size 应钳到 100，实得 %v", paged["page_size"])
	}
	if total <= 100 {
		t.Fatalf("前置自检失败：可见标签只有 %v 条，跨不过 100 硬顶，下面的对比是空转", total)
	}
	if float64(len(pagedList)) >= total {
		t.Fatalf("前置自检失败：分页这一页已含全部 %v 条，测不出截断", total)
	}

	// all=1：条数=total（本租户可见的全部），不再是 100
	dict, _ := getPage(t, r, tok, "/api/v1/admin/tags?all=1")
	dictList, _ := dict["list"].([]interface{})
	if float64(len(dictList)) != total {
		t.Fatalf("all=1 应给全 %v 条可见标签，实得 %v 条", total, len(dictList))
	}
	if d, _ := dict["total"].(float64); d != total {
		t.Fatalf("all=1 的 total 与分页模式不一致（筛选条件没共用？）：%v vs %v", d, total)
	}
	first, ok := dictList[0].(map[string]interface{})
	if !ok {
		t.Fatalf("all=1 列表项形态异常: %T", dictList[0])
	}
	if len(first) != 3 {
		t.Fatalf("all=1 只应回 id/name/code 三列，实得 %d 列: %v", len(first), first)
	}
	for _, k := range []string{"id", "name", "code"} {
		if _, has := first[k]; !has {
			t.Fatalf("all=1 缺少字段 %s，实得 %v", k, first)
		}
	}
	// 字典模式不得把 description 之类的冗余列带出去（下拉用不到，白占响应体）
	for _, leak := range []string{"description", "created_at", "updated_at", "tenant_id"} {
		if _, has := first[leak]; has {
			t.Fatalf("all=1 回显了冗余列 %s（字典只该给 id/name/code）", leak)
		}
	}
}

// TestTagDictAllLimitIsBounded all=1 自己有上限，不能变成"另一个不限长度的拖库口"。
// 上限数值由上面的截断/给全对照承担，这里只锁"上限是一个有限的、量级合理的常数"，
// 防止有人把它改成 math.MaxInt 之类的"事实无上限"。
func TestTagDictAllLimitIsBounded(t *testing.T) {
	if tagDictAllLimit < 100 || tagDictAllLimit > 5000 {
		t.Fatalf("all=1 上限取值异常（%d）：太低等于没解决问题，太高等于没上限", tagDictAllLimit)
	}
}
