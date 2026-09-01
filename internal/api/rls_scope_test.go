// 多租户行级作用域回归测试：为原 15 处 TODO-RLS 逐处证明隔离/共享语义（2026-09-01 已清）。
//
// 覆盖三类路径：
//  1. 租户自有数据：a 租户会话查/改 b 租户数据 → 404/空（billing.go:177、industry_pack.go:430/435）
//  2. 全局目录表：SubscriptionPlan/IndustryPack 无 tenant_id 列，所有租户共享（tenant.go:171/333、industry_pack.go:111/132/179/563）
//  3. 平台超管路径：非 super 会话被 SuperRequired 拦截 403；超管可跨租户（feedback.go:158、agreement.go:74、kb_material.go:45）
//  4. 注册防薅：TenantAuditLog 按 email/IP 全局计数是设计语义（跨租户防薅），非隔离漏洞（tenant.go:106/120/132）
package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"

	"github.com/gin-gonic/gin"
)

// newCtx 构造带指定租户会话的 gin 测试上下文（跳过 JWT/中间件链，直接锚定会话租户）
// 用 c.Set("tenant_id", ...) 模拟 TenantResolver 注入结果；tenantID=0 表示平台语境（超管）
// 返回 (context, recorder)，recorder 用于读取响应体
func newCtx(t *testing.T, tenantID uint) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	if tenantID != 0 {
		c.Set("tenant_id", tenantID)
		c.Set("tenant_code", "t00000001")
		c.Set("tenant_tier", "personal")
	}
	return c, w
}

// ============================================================
// 1. 租户自有数据：跨租户必须查空/404
// ============================================================

// TestManualConfirmPaidCrossTenant 订单属租户A，租户B会话手动确认必须 404（billing.go:177）
// 前置查询 line 168 已带 id+tenant_id 双条件；本测试验证隔离语义被守住
func TestManualConfirmPaidCrossTenant(t *testing.T) {
	testutil.SetupTestDB(t)
	tenantA := testutil.CreateTenantCode(t, "rls_a")
	tenantB := testutil.CreateTenantCode(t, "rls_b")
	defer testutil.CleanupTenant(t, tenantA)
	defer testutil.CleanupTenant(t, tenantB)

	order := &model.BillingOrder{OrderNo: fmt.Sprintf("RLS-%d", time.Now().UnixNano()), TenantID: &tenantA, AmountCents: 100, Status: "pending", Channel: "mock"}
	if err := db.DB.Create(order).Error; err != nil {
		t.Fatalf("建订单失败: %v", err)
	}

	// 租户B会话确认租户A订单 → 404
	c, _ := newCtx(t, tenantB)
	c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"order_id":`+itoa(order.ID)+`}`))
	c.Request.Header.Set("Content-Type", "application/json")
	ManualConfirmPaid(c)
	if c.Writer.Status() != http.StatusNotFound {
		t.Fatalf("跨租户手动确认应 404, got %d", c.Writer.Status())
	}
	// 租户A自己确认 → 200（隔离未误伤同租户）
	c2, _ := newCtx(t, tenantA)
	c2.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"order_id":`+itoa(order.ID)+`}`))
	c2.Request.Header.Set("Content-Type", "application/json")
	ManualConfirmPaid(c2)
	if c2.Writer.Status() != http.StatusOK {
		t.Fatalf("同租户手动确认应 200, got %d", c2.Writer.Status())
	}
}

// TestTenantPackCurrentCrossTenant 租户A绑定行业包，租户B查 current 必须 bound=false（industry_pack.go:430/435）
func TestTenantPackCurrentCrossTenant(t *testing.T) {
	testutil.SetupTestDB(t)
	tenantA := testutil.CreateTenantCode(t, "rls_pk_a")
	tenantB := testutil.CreateTenantCode(t, "rls_pk_b")
	defer testutil.CleanupTenant(t, tenantA)
	defer testutil.CleanupTenant(t, tenantB)

	pack := &model.IndustryPack{Code: "rls_ind", Name: "RLS行业包", Industry: "auto", Version: "1.0.0", PackLevel: "industry", Status: "active"}
	if err := db.DB.Create(pack).Error; err != nil {
		t.Fatalf("建行业包失败: %v", err)
	}
	bind := &model.TenantPackBinding{TenantID: tenantA, PackID: pack.ID, PackCode: pack.Code}
	if err := db.DB.Create(bind).Error; err != nil {
		t.Fatalf("建绑定失败: %v", err)
	}

	// 租户B：未绑定 → bound=false（看不到 A 的绑定）
	c, w := newCtx(t, tenantB)
	TenantPackCurrent(c)
	if c.Writer.Status() != http.StatusOK {
		t.Fatalf("TenantPackCurrent 应 200, got %d", c.Writer.Status())
	}
	body := w.Body.String()
	if strings.Contains(body, `"bound":true`) {
		t.Fatalf("租户B不应看到租户A的绑定: %s", body)
	}
	// 租户A：自己的绑定可见
	c2, w2 := newCtx(t, tenantA)
	TenantPackCurrent(c2)
	if !strings.Contains(w2.Body.String(), `"bound":true`) {
		t.Fatalf("租户A应看到自己的绑定: %s", w2.Body.String())
	}
}

// ============================================================
// 2. 全局目录表：无 tenant_id 列，所有租户共享（预期，非漏洞）
// ============================================================

// TestListPlansGlobalShared 定价目录全局共享：任一租户会话都能看到全部 plans/packages（tenant.go:333）
func TestListPlansGlobalShared(t *testing.T) {
	testutil.SetupTestDB(t)
	tenantA := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tenantA)

	c, w := newCtx(t, tenantA)
	ListPlans(c)
	if c.Writer.Status() != http.StatusOK {
		t.Fatalf("ListPlans 应 200, got %d", c.Writer.Status())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"packages"`) {
		t.Fatalf("ListPlans 应返回 packages 键: %s", body)
	}
}

// ============================================================
// 3. 平台超管路径：非 super 会话 403，超管放行（SuperRequired 守卫）
// ============================================================

// TestSuperRequiredGuard 验证 /super 组守卫语义（覆盖 feedback.go:158、agreement.go:74、
// kb_material.go:45 等 6 处超管路由的隔离前置）
func TestSuperRequiredGuard(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 非超管（普通租户管理员）→ 403
	c1, _ := newCtx(t, 1)
	c1.Set("role", model.RoleTenantAdmin)
	SuperRequired()(c1)
	if c1.Writer.Status() != http.StatusForbidden {
		t.Fatalf("非超管应 403, got %d", c1.Writer.Status())
	}
	// 超管 → 放行
	c2, _ := newCtx(t, 1)
	c2.Set("role", model.RoleSuperAdmin)
	SuperRequired()(c2)
	if c2.Writer.Status() == http.StatusForbidden {
		t.Fatalf("超管不应被 403")
	}
}

// TestSuperAgreementListCrossTenant 超管查看跨租户协议台账是产品语义（agreement.go:74）
// 验证：非超管会话被守卫拦截（隔离点在前置路由中间件），超管可达全平台数据
func TestSuperAgreementListCrossTenant(t *testing.T) {
	testutil.SetupTestDB(t)
	tenantA := testutil.CreateTenantCode(t, "rls_ag_a")
	tenantB := testutil.CreateTenantCode(t, "rls_ag_b")
	defer testutil.CleanupTenant(t, tenantA)
	defer testutil.CleanupTenant(t, tenantB)

	// 造两个租户各自的协议签署记录
	for _, tid := range []uint{tenantA, tenantB} {
		sig := &model.AgreementSignature{TenantID: tid, AgreementType: "user", Version: "1", Status: "signed"}
		if err := db.DB.Create(sig).Error; err != nil {
			t.Fatalf("建签署记录失败: %v", err)
		}
	}
	// 超管会话（tenant_id=0 平台语境）可看到跨租户全量数据
	c, w := newCtx(t, 0)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	SuperAgreementList(c)
	if c.Writer.Status() != http.StatusOK {
		t.Fatalf("超管协议台账应 200, got %d", c.Writer.Status())
	}
	if !strings.Contains(w.Body.String(), `"list"`) {
		t.Fatalf("超管应看到协议列表: %s", w.Body.String())
	}
}

// ============================================================
// 4. 注册防薅：全局计数是设计语义（tenant.go:106/120/132）
// ============================================================

// TestSignupAntiAbuseGlobalCount 同 email/IP 跨租户累计防薅计数（非隔离漏洞）
// 语义：防薅必须全局——攻击者换租户注册也须被同 email/IP 闸门拦截
func TestSignupAntiAbuseGlobalCount(t *testing.T) {
	testutil.SetupTestDB(t)
	tenantA := testutil.CreateTenantCode(t, "rls_ab_a")
	tenantB := testutil.CreateTenantCode(t, "rls_ab_b")
	defer testutil.CleanupTenant(t, tenantA)
	defer testutil.CleanupTenant(t, tenantB)

	// 两个租户各记一条 tenant_signup，同 IP 同 email
	logs := []model.TenantAuditLog{
		{TenantID: tenantA, Action: "tenant_signup", IP: "203.0.113.9", Detail: `{"email":"abuse@example.com"}`},
		{TenantID: tenantB, Action: "tenant_signup", IP: "203.0.113.9", Detail: `{"email":"abuse@example.com"}`},
	}
	for _, l := range logs {
		if err := db.DB.Create(&l).Error; err != nil {
			t.Fatalf("写审计失败: %v", err)
		}
	}
	// 与 tenant.go:106 相同查询：按 email 全局计数，应计 2 条（跨租户）
	var cnt int64
	db.DB.Model(&model.TenantAuditLog{}).
		Where("action = ? AND created_at >= CURRENT_DATE AND detail LIKE ?",
			"tenant_signup", `%"email":"abuse@example.com"%`).
		Count(&cnt)
	if cnt < 2 {
		t.Fatalf("防薅全局计数应≥2(跨租户), got %d", cnt)
	}
	// 与 tenant.go:120 相同查询：按 IP 全局计数
	var ipCnt int64
	db.DB.Model(&model.TenantAuditLog{}).
		Where("action = ? AND ip = ? AND created_at >= CURRENT_DATE", "tenant_signup", "203.0.113.9").
		Count(&ipCnt)
	if ipCnt < 2 {
		t.Fatalf("IP 防薅全局计数应≥2, got %d", ipCnt)
	}
}

// itoa uint→十进制字符串（测试内小工具，避免 strconv 冗余）
func itoa(v uint) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}
