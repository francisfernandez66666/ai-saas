// C7 配额建客「租户盖章」回归测试（2026-09-15）。
//
// 背景：C7 修复把配额分支（max_customers>0）的"复查计数 + 插入"收进单个事务，
// 但最初用了裸 db.DB.Transaction——派生的 tx 会话不带请求 context，
// 写入自动盖章回调 TenantFromContext(tx.Statement.Context)=0，导致新建客户被盖成
// tenant_id=0（归属丢失 → 跨租户归属错乱 / 本租户 /chat 反查不到自己客户 → 全链 404）。
// 该泄露只在配额租户触发，smoke.sh 走无限量 acme（非配额路径）漏检。
//
// 本测试直接锁定修复语义：配额分支建客后，DB 行的 tenant_id 必须等于本租户（绝不为 0），
// 并顺带验证隔离（B 租户看不到 A 租户客户）与配额上限（超额 403）。
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"

	"github.com/gin-gonic/gin"
)

// createCustomerAndParseID 在指定租户会话下调用 CreateCustomer 建客并回传新客 ID。
func createCustomerAndParseID(t *testing.T, tenantID uint, name string) (uint, int) {
	t.Helper()
	c, w := newCtx(t, tenantID)
	c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"`+name+`"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	CreateCustomer(c)
	code := c.Writer.Status()
	if code != http.StatusOK {
		return 0, code
	}
	var resp struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析建客响应失败: %v body=%s", err, w.Body.String())
	}
	return resp.Data.ID, code
}

// TestCreateCustomerQuotaStampsTenantID 配额分支（max_customers>0）建客必须落到本租户。
func TestCreateCustomerQuotaStampsTenantID(t *testing.T) {
	testutil.SetupTestDB(t)
	tenantA := testutil.CreateTenantCode(t, "c7q_a")
	tenantB := testutil.CreateTenantCode(t, "c7q_b")
	defer testutil.CleanupTenant(t, tenantA)
	defer testutil.CleanupTenant(t, tenantB)

	// 给 A 设配额上限=2 → 强制走 C7 事务分支（这是当年泄露的唯一触发路径）
	if err := db.DB.Model(&model.Tenant{}).Where("id = ?", tenantA).Update("max_customers", 2).Error; err != nil {
		t.Fatalf("设配额失败: %v", err)
	}

	cid, status := createCustomerAndParseID(t, tenantA, "C7配额客户")
	if status != http.StatusOK {
		t.Fatalf("配额内建客应 200, got %d", status)
	}

	// 核心断言：DB 行的 tenant_id 必须等于 A（修复前为 0）
	var stored model.Customer
	if err := db.DB.First(&stored, cid).Error; err != nil {
		t.Fatalf("按 id 取客户失败: %v", err)
	}
	if stored.TenantID == 0 {
		t.Fatalf("C7 回归：配额建客被盖成 tenant_id=0（隔离泄露），应为租户 %d", tenantA)
	}
	if stored.TenantID != tenantA {
		t.Fatalf("建客租户归属错误：期望 %d 实际 %d", tenantA, stored.TenantID)
	}

	// 隔离断言：B 租户会话（带租户过滤）查不到 A 的客户 → 404
	c2, _ := newCtx(t, tenantB)
	c2.Params = gin.Params{{Key: "id", Value: itoa(cid)}}
	c2.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	GetCustomer(c2)
	if c2.Writer.Status() != http.StatusNotFound {
		t.Fatalf("跨租户读客户应 404, got %d", c2.Writer.Status())
	}
}

// TestCreateCustomerQuotaEnforced 配额满后第 N+1 单必须 403（证明事务分支上限判定仍生效）。
func TestCreateCustomerQuotaEnforced(t *testing.T) {
	testutil.SetupTestDB(t)
	tenantA := testutil.CreateTenantCode(t, "c7q_quota")
	defer testutil.CleanupTenant(t, tenantA)
	if err := db.DB.Model(&model.Tenant{}).Where("id = ?", tenantA).Update("max_customers", 1).Error; err != nil {
		t.Fatalf("设配额失败: %v", err)
	}
	if _, st := createCustomerAndParseID(t, tenantA, "配额内首客"); st != http.StatusOK {
		t.Fatalf("首客应建成功, got %d", st)
	}
	// 第二单超额 → 403
	if _, st := createCustomerAndParseID(t, tenantA, "超额客"); st != http.StatusForbidden {
		t.Fatalf("超配额应 403, got %d", st)
	}
}
