// 获客活码公开链路测试（获客批 · 批次2，2026-09-23）：读码/计数/建客归因三端点的 HTTP 语义。
//
// 与 internal/acquisition 那 21 例的分工：包内测的是**裁决与取数**（纯函数 + 单表读写），
// 本文件测的是**端点承诺**——状态码形态、无租户语境下能否走通、以及"归因失败不许打断建客"。
// 这两层各自会各自坏：谓词改对了但路由注册在 TenantResolver 之后，客户扫码直接 403；
// 路由对了但 handler 把 tenant_mismatch 当 500 抛出去，客户进不来。
//
// 依赖本地 PostgreSQL（testutil.SetupTestDB，DB 不可用自动 Skip）；不真实外呼 AI。
package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"ai-scrm/internal/acquisition"
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// newAcqPublicRouter 组装活码公开端点的最小路由。
// **故意不挂任何租户解析桩**：生产上这一组走 skipTenantPrefixes 免租户放行，
// 挂了桩就把"没有租户语境也能跑通"这条真实约束测没了。
func newAcqPublicRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v1/acquisition/:code", ResolveAcquisitionCode)
	r.POST("/api/v1/acquisition/:code/scan", RecordAcquisitionScan)
	return r
}

// acqSeedCode 建一个租户 + 一个启用中的活码，返回两者 ID 与码串。
func acqSeedCode(t *testing.T, semantic, name, channel string) (uint, uint, string) {
	t.Helper()
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenantCode(t, semantic)
	acq, err := acquisition.CreateCode(db.DB, acquisition.CreateInput{
		TenantID: tid, Name: name, Channel: channel,
	})
	if err != nil {
		t.Fatalf("建码失败: %v", err)
	}
	t.Cleanup(func() {
		db.DB.Where("code_id = ?", acq.ID).Delete(&model.AcquisitionScan{})
		db.DB.Where("id = ?", acq.ID).Delete(&model.AcquisitionCode{})
	})
	return tid, acq.ID, acq.Code
}

// acqEnvelope 公开端点统一信封（只取断言需要的字段）
type acqEnvelope struct {
	Code int `json:"code"`
	Data struct {
		Code        string `json:"code"`
		Channel     string `json:"channel"`
		LandingPath string `json:"landing_path"`
		Counted     bool   `json:"counted"`
		Acquisition *struct {
			Applied bool   `json:"applied"`
			Reason  string `json:"reason"`
			Code    string `json:"code"`
		} `json:"acquisition"`
	} `json:"data"`
}

// TestAcqResolveEndpoint 读码端点：启用中 200 带渠道名，停用/未知/非法形态回**同一个** 404。
// 三种失败必须不可区分——这是公开面上唯一能"猜码"的入口，回显"存在但停用了"就是给试探者的进度条。
func TestAcqResolveEndpoint(t *testing.T) {
	tid, _, code := acqSeedCode(t, "acq_pub_a", "抖音开屏", acquisition.ChannelDouyin)
	r := newAcqPublicRouter()

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/acquisition/"+code, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("启用中的码应 200，实得 %d body=%s", rec.Code, rec.Body.String())
	}
	var env acqEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析响应失败: %v body=%s", err, rec.Body.String())
	}
	if env.Data.Channel != acquisition.ChannelDouyin || env.Data.Code != code {
		t.Fatalf("渠道/码回错：%+v（租户 %d）", env.Data, tid)
	}
	if env.Data.LandingPath != acquisition.LandingPath {
		t.Fatalf("未下发展示路径，前端会自己猜一套：%s", env.Data.LandingPath)
	}

	// 停用后对外即不存在。先钉一条反面事实：ID 传 0 只能命中 0 行——
	// 若哪天条件写成 "id = ?" 的空串拼接或按 name 匹配，这里就会把整家租户的码一起停掉。
	if n, err := acquisition.SetCodeStatus(db.DB, tid, 0, false); err != nil || n != 0 {
		t.Fatalf("码 ID 传 0 必须零命中，实得 n=%d err=%v", n, err)
	}
	if n, err := acquisition.SetCodeStatus(db.DB, tid, mustIDOfCode(t, code), false); err != nil || n != 1 {
		t.Fatalf("停用失败: %v n=%d", err, n)
	}
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/api/v1/acquisition/"+code, nil))
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("停用码应 404，实得 %d", rec2.Code)
	}
	// 非法形态与真不存在的码：同为 404，且不落 DB 查询（形态在 handler 之前判掉）
	for _, bad := range []string{"short", "ABCDEFG0", "I/O/PATH", ""} {
		rec3 := httptest.NewRecorder()
		r.ServeHTTP(rec3, httptest.NewRequest(http.MethodGet, "/api/v1/acquisition/"+bad, nil))
		if rec3.Code != http.StatusNotFound {
			t.Fatalf("非法码 %q 应 404，实得 %d", bad, rec3.Code)
		}
	}
}

// mustIDOfCode 按码串取 ID（测试辅助，不作为被测行为）
func mustIDOfCode(t *testing.T, code string) uint {
	t.Helper()
	var acq model.AcquisitionCode
	if err := db.DB.Where("code = ?", code).First(&acq).Error; err != nil {
		t.Fatalf("查码失败: %v", err)
	}
	return acq.ID
}

// TestAcqScanEndpointDedupe 扫码计数端点：同访客窗口内第二发 counted=false 但仍 200，
// 且事件行落在**码所属租户**名下（不是请求 Host 的租户——这一发根本没有租户语境）。
func TestAcqScanEndpointDedupe(t *testing.T) {
	tid, codeID, code := acqSeedCode(t, "acq_pub_b", "门店海报", acquisition.ChannelStore)
	r := newAcqPublicRouter()

	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/acquisition/"+code+"/scan", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(rec, req)
		return rec
	}
	first := post(`{"visitor_key":"vk-http-1"}`)
	if first.Code != http.StatusOK {
		t.Fatalf("首扫应 200，实得 %d body=%s", first.Code, first.Body.String())
	}
	var e1 acqEnvelope
	_ = json.Unmarshal(first.Body.Bytes(), &e1)
	if !e1.Data.Counted {
		t.Fatalf("首扫 counted 应为 true：%s", first.Body.String())
	}
	second := post(`{"visitor_key":"vk-http-1"}`)
	if second.Code != http.StatusOK {
		t.Fatalf("窗口内重复扫码不得报错（重复打开是常态）: %d", second.Code)
	}
	var e2 acqEnvelope
	_ = json.Unmarshal(second.Body.Bytes(), &e2)
	if e2.Data.Counted {
		t.Fatalf("窗口内重复扫码不得再计数")
	}
	// 空体（爬虫/链接预览）也要 200：没有去重依据时每发都记
	if rec := post(``); rec.Code != http.StatusOK {
		t.Fatalf("空体扫码应 200，实得 %d", rec.Code)
	}
	var n int64
	db.DB.Model(&model.AcquisitionScan{}).Where("code_id = ?", codeID).Count(&n)
	// 1（首扫）+ 0（窗口内重复）+ 1（空体无去重依据）= 2 行
	if n != 2 {
		t.Fatalf("事件行数应为 2，实得 %d", n)
	}
	var tenantRows int64
	db.DB.Model(&model.AcquisitionScan{}).Where("code_id = ? AND tenant_id <> ?", codeID, tid).Count(&tenantRows)
	if tenantRows != 0 {
		t.Fatalf("有 %d 行落在了码之外，租户归属被请求侧污染", tenantRows)
	}
}

// TestAcqScanUnknownCode404 未知/停用码的扫码请求：404 且不写任何事件行（计数面不能成为垃圾写入面）。
func TestAcqScanUnknownCode404(t *testing.T) {
	_, codeID, _ := acqSeedCode(t, "acq_pub_c", "展会物料", acquisition.ChannelOther)
	r := newAcqPublicRouter()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/acquisition/QQQQQQQQ/scan", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知码应 404，实得 %d", rec.Code)
	}
	var n int64
	db.DB.Model(&model.AcquisitionScan{}).Where("code_id = ?", codeID).Count(&n)
	if n != 0 {
		t.Fatalf("未知码请求不该给别的码记数，实得 %d 行", n)
	}
}

// newGuestRouterWithTenant 组装 C 端建档入口（生产还挂 TurnstileGuard + IPRateLimit，
// 单测不挂：人机验证在别处已覆盖，限流桶会在这个文件的多个用例间互相触发频控）。
func newGuestRouterWithTenant(tid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/chat/guest",
		func(c *gin.Context) { c.Set("tenant_id", tid) },
		CreateGuest)
	return r
}

// createGuestWithCode 走一次建档，返回信封
func createGuestWithCode(t *testing.T, tid uint, code string) acqEnvelope {
	t.Helper()
	r := newGuestRouterWithTenant(tid)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat/guest",
		bytes.NewBufferString(`{"channel":"web","device":"mobile","code":"`+code+`"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("建档应 200，实得 %d body=%s", rec.Code, rec.Body.String())
	}
	var env acqEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析建档响应失败: %v", err)
	}
	return env
}

// TestAcqGuestAttributionAppliedWithCode 带码建档：来源列被改成渠道位、归因码落库、
// 响应回带 applied=true——三件事缺一件，后台数字与前端可见事实就对不上。
func TestAcqGuestAttributionAppliedWithCode(t *testing.T) {
	tid, _, code := acqSeedCode(t, "acq_pub_d", "小红书笔记", acquisition.ChannelXiaohong)
	env := createGuestWithCode(t, tid, code)
	if env.Data.Acquisition == nil || !env.Data.Acquisition.Applied {
		t.Fatalf("带码建档应回 applied=true：%+v", env.Data.Acquisition)
	}
	var cust model.Customer
	// 访客 ID 从响应 data 里没有单独字段，用最新一条该码归因的客户定位
	if err := db.DB.Where("tenant_id = ? AND acquisition_code = ?", tid, code).
		Order("id DESC").First(&cust).Error; err != nil {
		t.Fatalf("未按码查到访客: %v (env=%+v)", err, env)
	}
	if cust.Source != acquisition.ChannelXiaohong {
		t.Fatalf("来源应写成渠道位 %s，实得 %q", acquisition.ChannelXiaohong, cust.Source)
	}
	if cust.JourneyStage != model.JourneyAIConnected {
		t.Fatalf("归因不得顺手改旅程阶段，实得 %s", cust.JourneyStage)
	}
	if cust.AssignedUserID != 0 {
		t.Fatalf("未留资访客不得分配顾问（线索两分支铁律），实得 %d", cust.AssignedUserID)
	}
}

// TestAcqGuestCrossTenantCodeRefusesButStillChats 别的租户的码：**建客照常成功**、
// 归因一行不写、响应如实回 reason。这条是本批最重要的行为——
// 没有独立域名的租户共用主域分发时，走的就是这条路，它必须"少记一笔"而不是"进不来"或"记错家"。
func TestAcqGuestCrossTenantCodeRefusesButStillChats(t *testing.T) {
	tidA, _, codeA := acqSeedCode(t, "acq_pub_e", "A家物料", acquisition.ChannelDouyin)
	tidB, _, _ := acqSeedCode(t, "acq_pub_f", "B家物料", acquisition.ChannelBaidu)
	if tidA == tidB {
		t.Fatalf("用例需要两个不同租户，testutil 复用了同一个（语义码撞了）")
	}
	env := createGuestWithCode(t, tidB, codeA)
	if env.Data.Acquisition == nil || env.Data.Acquisition.Applied {
		t.Fatalf("跨租户码应回 applied=false：%+v", env.Data.Acquisition)
	}
	if env.Data.Acquisition.Reason != acquisition.ApplyReasonTenantMismatch {
		t.Fatalf("原因码应为 %s，实得 %q", acquisition.ApplyReasonTenantMismatch, env.Data.Acquisition.Reason)
	}
	var n int64
	db.DB.Model(&model.Customer{}).Where("tenant_id = ? AND acquisition_code <> ''", tidB).Count(&n)
	if n != 0 {
		t.Fatalf("B 家名下出现了 %d 个带码客户，跨租户闸失效", n)
	}
	db.DB.Model(&model.Customer{}).Where("tenant_id = ? AND source = ?", tidB, acquisition.ChannelDouyin).Count(&n)
	if n != 0 {
		t.Fatalf("B 家新客户来源被写成了 A 家渠道，实得 %d 行", n)
	}
}

// TestAcqGuestWithoutCodeHasNoAcquisitionKey 不带码（自然流量/老链接）：响应里干脆没有这个键，
// 而不是回一个 applied=false 的假失败——前端据此渲染"扫码成功/失败"，
// 无码回 false 会让正常客户看到一个莫名的错误提示。
func TestAcqGuestWithoutCodeHasNoAcquisitionKey(t *testing.T) {
	tid, _, _ := acqSeedCode(t, "acq_pub_g", "对照物料", acquisition.ChannelStore)
	env := createGuestWithCode(t, tid, "")
	if env.Data.Acquisition != nil {
		t.Fatalf("无码请求不该带 acquisition 键：%+v", env.Data.Acquisition)
	}
}
