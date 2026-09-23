// 获客活码管理端测试（获客批 · 批次3，2026-09-23）：CRUD 契约、出图、下钻与卡片数字同源。
//
// 与包内测试的分工：包内测裁决与取数，这里测**端点承诺**——
// 空态是不是 [] 而不是 null、拒绝有没有带稳定 reason 码、跨租户 ID 是不是 404、
// 以及最要紧的一条：从 HTTP 读到的漏斗数字与从 HTTP 读到的名单 total 必须相等
// （同源这件事在包内是同一次函数调用，看不出接线问题；只有两侧都走端点才算真验过）。
//
// 依赖本地 PostgreSQL（testutil.SetupTestDB，DB 不可用自动 Skip）；不真实外呼 AI。
package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"ai-scrm/internal/acquisition"
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// newAcqAdminRouter 组装管理端最小路由（生产另有 JWTAuth + AdminRequired + OrgResolve 整组闸，
// 权限矩阵在 smoke_perm.sh 覆盖；本处只设租户语境，专注测端点自身的契约）。
func newAcqAdminRouter(tid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	stub := func(c *gin.Context) { c.Set("tenant_id", tid) }
	r.GET("/api/v1/admin/acquisition/codes", stub, ListAcquisitionCodes)
	r.POST("/api/v1/admin/acquisition/codes", stub, CreateAcquisitionCode)
	r.POST("/api/v1/admin/acquisition/codes/:id/status", stub, SetAcquisitionCodeStatus)
	r.GET("/api/v1/admin/acquisition/codes/:id/qr.png", stub, GetAcquisitionCodeQR)
	r.GET("/api/v1/admin/acquisition/codes/:id/customers", stub, DrillAcquisitionCustomers)
	return r
}

// acqTenant 建单测租户并返回 ID（语义码必须互不相同：testutil 同码复用同一租户，
// 跨租家用例若撞码就退化成同一家的 A/A，主闸会在自己身上假绿）
func acqTenant(t *testing.T, semantic string) uint {
	t.Helper()
	testutil.SetupTestDB(t)
	return testutil.CreateTenantCode(t, semantic)
}

// acqDo 发一次请求并解析统一信封
func acqDo(t *testing.T, r *gin.Engine, method, path, body string) (int, []byte) {
	t.Helper()
	rec := httptest.NewRecorder()
	var buf *bytes.Reader
	if body != "" {
		buf = bytes.NewReader([]byte(body))
	} else {
		buf = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, buf)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// acqCreate 建一个码并返回其出参行（断言失败即 t.Fatal，用例不必各自重复解析）
func acqCreate(t *testing.T, r *gin.Engine, name, channel string) map[string]any {
	t.Helper()
	code, body := acqDo(t, r, http.MethodPost, "/api/v1/admin/acquisition/codes",
		`{"name":"`+name+`","channel":"`+channel+`","remark":"物料备注"}`)
	if code != http.StatusOK {
		t.Fatalf("建码应 200，实得 %d body=%s", code, string(body))
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("解析建码响应失败: %v body=%s", err, string(body))
	}
	return env.Data
}

// TestAcqAdminCreateContractShape 建码出参形态：funnel 五键齐全且为 0、链接带落地路径、
// 状态默认启用。缺键的前端会渲染出 undefined，比渲染出 0 更像"功能坏了"。
func TestAcqAdminCreateContractShape(t *testing.T) {
	tid := acqTenant(t, "acq_adm_a")
	defer testutil.CleanupTenant(t, tid)
	r := newAcqAdminRouter(tid)
	row := acqCreate(t, r, "门店立牌", acquisition.ChannelStore)

	id, _ := row["id"].(float64)
	if id == 0 {
		t.Fatalf("建码未回 ID：%v", row)
	}
	link, _ := row["link"].(string)
	if link == "" || !bytes.Contains([]byte(link), []byte("code=")) {
		t.Fatalf("链接缺失或不含码参数：%q", link)
	}
	funnel, ok := row["funnel"].(map[string]any)
	if !ok {
		t.Fatalf("funnel 不是对象：%T", row["funnel"])
	}
	for _, m := range acquisition.FunnelMetricCodes {
		v, has := funnel[m]
		if !has {
			t.Fatalf("funnel 缺指标键 %s（前端该格会渲染 undefined）", m)
		}
		if pv, _ := v.(float64); pv != 0 {
			t.Fatalf("新建码 %s 应为 0，实得 %v", m, v)
		}
	}
	if row["status"] != model.AcquisitionStatusActive {
		t.Fatalf("出厂状态应为启用，实得 %v", row["status"])
	}
}

// TestAcqAdminCreateRejectsUnknownChannelWithReason 未知渠道位回 400 + reason=channel_unknown。
// reason 是稳定码：前端按它出文案、冒烟按它断言，改中文提示不许改码。
func TestAcqAdminCreateRejectsUnknownChannelWithReason(t *testing.T) {
	tid := acqTenant(t, "acq_adm_b")
	defer testutil.CleanupTenant(t, tid)
	r := newAcqAdminRouter(tid)

	status, body := acqDo(t, r, http.MethodPost, "/api/v1/admin/acquisition/codes",
		`{"name":"假渠道","channel":"telepathy"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("未知渠道应 400，实得 %d body=%s", status, string(body))
	}
	var env struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(body, &env)
	if env.Reason != "channel_unknown" {
		t.Fatalf("reason 应为 channel_unknown，实得 %q", env.Reason)
	}
	// 空名同样要拒（名字是后台唯一能看懂"这是哪张海报"的东西）
	if s2, _ := acqDo(t, r, http.MethodPost, "/api/v1/admin/acquisition/codes",
		`{"name":"   ","channel":"`+acquisition.ChannelWechat+`"}`); s2 != http.StatusBadRequest {
		t.Fatalf("空名应 400，实得 %d", s2)
	}
}

// TestAcqAdminListTenantScopedAndStatusToggle 列表只见本租户的码；启停回环可逆；跨租户 ID 一律 404。
func TestAcqAdminListTenantScopedAndStatusToggle(t *testing.T) {
	tidA := acqTenant(t, "acq_adm_c")
	defer testutil.CleanupTenant(t, tidA)
	tidB := acqTenant(t, "acq_adm_d")
	defer testutil.CleanupTenant(t, tidB)
	rA, rB := newAcqAdminRouter(tidA), newAcqAdminRouter(tidB)

	rowA := acqCreate(t, rA, "A家朋友圈", acquisition.ChannelWechat)
	idA, _ := rowA["id"].(float64)
	rowB := acqCreate(t, rB, "B家朋友圈", acquisition.ChannelWechat)
	idB, _ := rowB["id"].(float64)

	_, body := acqDo(t, rA, http.MethodGet, "/api/v1/admin/acquisition/codes", "")
	var env struct {
		Data struct {
			List   []map[string]any `json:"list"`
			Config map[string]any   `json:"config"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("解析列表失败: %v body=%s", err, string(body))
	}
	for _, row := range env.Data.List {
		if rid, _ := row["id"].(float64); uint(rid) == uint(idB) {
			t.Fatalf("A 家列表里出现了 B 家的码（租户闸失效）")
		}
	}
	if channels, ok := env.Data.Config["channels"].([]any); !ok || len(channels) == 0 {
		t.Fatalf("config.channels 必须下发非空渠道枚举（前端下拉要它）：%v", env.Data.Config)
	}
	if _, ok := env.Data.Config["labels"].(map[string]any); !ok {
		t.Fatalf("config.labels 必须下发指标中文口径名（前端不复写第二套文案）")
	}

	// 停用 → 列表按 status 过滤能查到，启用回环可逆
	if s, b := acqDo(t, rA, http.MethodPost, "/api/v1/admin/acquisition/codes/"+itoa(uint(idA))+"/status",
		`{"active":false}`); s != http.StatusOK || !bytes.Contains(b, []byte(`"disabled"`)) {
		t.Fatalf("停用应 200 且回 disabled，实得 %d body=%s", s, string(b))
	}
	_, lb := acqDo(t, rA, http.MethodGet, "/api/v1/admin/acquisition/codes?status=disabled", "")
	if !bytes.Contains(lb, []byte(rowA["code"].(string))) {
		t.Fatalf("停用后按 status=disabled 过滤应能读到该码（停用不是删除）：%s", string(lb))
	}
	if s, _ := acqDo(t, rA, http.MethodPost, "/api/v1/admin/acquisition/codes/"+itoa(uint(idA))+"/status",
		`{"active":true}`); s != http.StatusOK {
		t.Fatalf("重新启用应 200，实得 %d", s)
	}
	// 缺 active 不猜默认（含糊的可见性变更请求一律拒）
	if s, _ := acqDo(t, rA, http.MethodPost, "/api/v1/admin/acquisition/codes/"+itoa(uint(idA))+"/status",
		`{}`); s != http.StatusBadRequest {
		t.Fatalf("缺 active 应 400，实得 %d", s)
	}
	// 跨租户改别人的码：404，且不回显"存在但不可改"
	if s, _ := acqDo(t, rB, http.MethodPost, "/api/v1/admin/acquisition/codes/"+itoa(uint(idA))+"/status",
		`{"active":false}`); s != http.StatusNotFound {
		t.Fatalf("跨租户停用应 404，实得 %d", s)
	}
	var still string
	if err := db.DB.Model(&model.AcquisitionCode{}).Where("id = ?", uint(idA)).Select("status").Row().Scan(&still); err != nil {
		t.Fatalf("复查状态失败: %v", err)
	}
	if still != model.AcquisitionStatusActive {
		t.Fatalf("B 家的停用请求动到了 A 家的码：%s", still)
	}
}

// TestAcqAdminQRPNGAndCrossTenant 出图：PNG 魔数 + 内容类型；跨租户 404（不返回别人的码）。
func TestAcqAdminQRPNGAndCrossTenant(t *testing.T) {
	tidA := acqTenant(t, "acq_adm_e")
	defer testutil.CleanupTenant(t, tidA)
	tidB := acqTenant(t, "acq_adm_f")
	defer testutil.CleanupTenant(t, tidB)
	rA, rB := newAcqAdminRouter(tidA), newAcqAdminRouter(tidB)
	row := acqCreate(t, rA, "展台背板", acquisition.ChannelOther)
	id, _ := row["id"].(float64)

	rec := httptest.NewRecorder()
	newAcqAdminRouter(tidA).ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/admin/acquisition/codes/"+itoa(uint(id))+"/qr.png?size=240", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("出图应 200，实得 %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("内容类型应为 image/png，实得 %q", ct)
	}
	png := rec.Body.Bytes()
	if len(png) < 8 || png[0] != 0x89 || png[1] != 'P' || png[2] != 'N' || png[3] != 'G' {
		t.Fatalf("响应体不是 PNG（前 4 字节 % x）", png[:4])
	}
	rec2 := httptest.NewRecorder()
	rB.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet,
		"/api/v1/admin/acquisition/codes/"+itoa(uint(id))+"/qr.png", nil))
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("跨租户出图应 404，实得 %d", rec2.Code)
	}
}

// TestAcqAdminDrillTotalsEqualCardNumbers 本批端点层主断言：
// 列表读到的每格数字 == 同指标下钻读到的 total（两侧都过 HTTP，才算真验过接线）。
// 顺带钉住单位纪律：扫码次数不可下钻（400），未知码 404，缺 metric 不默认回某一份名单。
func TestAcqAdminDrillTotalsEqualCardNumbers(t *testing.T) {
	tid := acqTenant(t, "acq_adm_g")
	defer testutil.CleanupTenant(t, tid)
	r := newAcqAdminRouter(tid)
	row := acqCreate(t, r, "百度投放", acquisition.ChannelBaidu)
	id, _ := row["id"].(float64)
	code, _ := row["code"].(string)

	// 三个客户：都算新建、两个开过口、一个到留资及之后
	stage := []string{model.JourneyLeadCaptured, model.JourneyArrived, ""}
	for i, st := range stage {
		cust := model.Customer{TenantID: tid, Name: "获客客户" + string(rune('甲'+i)),
			CustomerType: "potential", JourneyStage: st, Source: acquisition.ChannelBaidu,
			VisitorKey: model.GenerateVisitorKey(), Status: 1}
		if err := db.DB.Create(&cust).Error; err != nil {
			t.Fatalf("建客户失败: %v", err)
		}
		t.Cleanup(func() { db.DB.Where("id = ?", cust.ID).Delete(&model.Customer{}) })
		if _, err := acquisition.ApplyToGuest(db.DB, tid, cust.ID, code, ""); err != nil {
			t.Fatalf("归因失败: %v", err)
		}
		if st == model.JourneyLeadCaptured || st == model.JourneyArrived {
			msg := model.Message{TenantID: tid, CustomerID: cust.ID, SenderType: "customer",
				Content: "想了解下价格", MessageType: "text", CreatedAt: time.Now()}
			if err := db.DB.Create(&msg).Error; err != nil {
				t.Fatalf("建消息失败: %v", err)
			}
			t.Cleanup(func() { db.DB.Where("id = ?", msg.ID).Delete(&model.Message{}) })
		}
	}

	_, lb := acqDo(t, r, http.MethodGet, "/api/v1/admin/acquisition/codes", "")
	var listEnv struct {
		Data struct {
			List []struct {
				ID     float64          `json:"id"`
				Funnel map[string]int64 `json:"funnel"`
				Scans  int64            `json:"scans"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(lb, &listEnv); err != nil {
		t.Fatalf("解析列表失败: %v", err)
	}
	var card map[string]int64
	for _, item := range listEnv.Data.List {
		if uint(item.ID) == uint(id) {
			card = item.Funnel
		}
	}
	if card == nil {
		t.Fatalf("列表里找不到刚建的码：%s", string(lb))
	}
	if card[acquisition.MetricNew] != 3 {
		t.Fatalf("新建客户数应为 3，实得 %d", card[acquisition.MetricNew])
	}
	for _, m := range acquisition.FunnelMetricCodes {
		_, db2 := acqDo(t, r, http.MethodGet,
			"/api/v1/admin/acquisition/codes/"+itoa(uint(id))+"/customers?metric="+m, "")
		var dr struct {
			Data struct {
				Total  int64  `json:"total"`
				Metric string `json:"metric"`
			} `json:"data"`
		}
		if err := json.Unmarshal(db2, &dr); err != nil {
			t.Fatalf("解析下钻失败: %v body=%s", err, string(db2))
		}
		if dr.Data.Total != card[m] {
			t.Fatalf("指标 %s：名单 %d 条 ≠ 卡片 %d 个，两侧不同源了", m, dr.Data.Total, card[m])
		}
		if dr.Data.Metric != m {
			t.Fatalf("下钻回显指标错：%s ≠ %s", dr.Data.Metric, m)
		}
	}

	// 扫码次数不可下钻（单位是"次"不是"人"）
	if s, _ := acqDo(t, r, http.MethodGet,
		"/api/v1/admin/acquisition/codes/"+itoa(uint(id))+"/customers?metric="+acquisition.MetricScans, ""); s != http.StatusBadRequest {
		t.Fatalf("扫码次数下钻应 400，实得 %d", s)
	}
	// 缺 metric 不默认回一份名单（否则页面会把"全部客户"当成某一格的人数）
	if s, _ := acqDo(t, r, http.MethodGet,
		"/api/v1/admin/acquisition/codes/"+itoa(uint(id))+"/customers", ""); s != http.StatusBadRequest {
		t.Fatalf("缺 metric 应 400，实得 %d", s)
	}
	// 别人的码：404 且不回名单
	if s, _ := acqDo(t, newAcqAdminRouter(acqTenant(t, "acq_adm_h")), http.MethodGet,
		"/api/v1/admin/acquisition/codes/"+itoa(uint(id))+"/customers?metric="+acquisition.MetricNew, ""); s != http.StatusNotFound {
		t.Fatalf("跨租户下钻应 404，实得 %d", s)
	}
}

// TestAcqAdminDrillPageSizeClamp 分页口径两条（与 D4 同规）：page_size 硬顶 100 且**回显钳制后的值**、
// 越界页只回空名单但 total 如实。回显若用请求原值，前端会按"每页 1000 行"算页数，
// 于是页面显示"共 1 页"而实际只拿到 100 行——数字看着对，翻页却翻不动。
func TestAcqAdminDrillPageSizeClamp(t *testing.T) {
	tid := acqTenant(t, "acq_adm_i")
	defer testutil.CleanupTenant(t, tid)
	r := newAcqAdminRouter(tid)
	row := acqCreate(t, r, "门店立牌", acquisition.ChannelStore)
	id, _ := row["id"].(float64)
	code, _ := row["code"].(string)
	cust := model.Customer{TenantID: tid, Name: "分页客户", CustomerType: "potential",
		Source: acquisition.ChannelStore, VisitorKey: model.GenerateVisitorKey(), Status: 1}
	if err := db.DB.Create(&cust).Error; err != nil {
		t.Fatalf("建客户失败: %v", err)
	}
	t.Cleanup(func() { db.DB.Where("id = ?", cust.ID).Delete(&model.Customer{}) })
	if _, err := acquisition.ApplyToGuest(db.DB, tid, cust.ID, code, ""); err != nil {
		t.Fatalf("归因失败: %v", err)
	}

	drill := func(q string) (int, map[string]any) {
		s, body := acqDo(t, r, http.MethodGet,
			"/api/v1/admin/acquisition/codes/"+itoa(uint(id))+"/customers?"+q, "")
		var env map[string]any
		_ = json.Unmarshal(body, &env)
		d, _ := env["data"].(map[string]any)
		return s, d
	}
	// 超限请求：仍 200，但 page_size 回 100、total 如实
	if s, d := drill("metric=new&page_size=1000"); s != http.StatusOK || d["page_size"] != float64(acquisition.MaxDrillPageSize) || d["total"] != float64(1) {
		t.Fatalf("page_size 未钳到 %d 或 total 失真：status=%d data=%v", acquisition.MaxDrillPageSize, s, d)
	}
	// 越界页：名单空、total 不动（前端据此把页码收回来，而不是以为"这一格没人"）
	if s, d := drill("metric=new&page=99"); s != http.StatusOK {
		t.Fatalf("越界页应 200，实得 %d", s)
	} else {
		list, _ := d["list"].([]any)
		if len(list) != 0 || d["total"] != float64(1) {
			t.Fatalf("越界页应为空名单且 total 如实，实得 list=%v total=%v", list, d["total"])
		}
	}
	// 合法值原样保留（钳制不是"一律改 20"）
	if _, d := drill("metric=new&page_size=5"); d["page_size"] != float64(5) {
		t.Fatalf("page_size=5 被改写：%v", d["page_size"])
	}
}
