// ListBillingOrders 退款/发票字段回归测试（UATFOLLOWUP F1，2026-09-15）。
//
// 背景：F1 修复前 Select 白名单只取 10 列，漏 refund_amount_cents/refunded_at/
// original_amount_cents/invoice_* 等——退款成功后 DB 有值但列表接口恒返回 0/null，
// Admin 订单列表与财务对账视图被误导（UAT 字节级探针实测复现）。
// 本测试锁定修复语义：退款订单经列表接口必须原样带回退款金额、退款时间与原价字段。
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// TestListBillingOrdersRefundFields 退款后订单列表必须含退款金额/退款时间/原价
func TestListBillingOrdersRefundFields(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenantCode(t, "lst_o")
	defer testutil.CleanupTenant(t, tid)

	now := time.Now()
	order := &model.BillingOrder{
		OrderNo:             "BOLISTUT" + now.Format("150405"),
		TenantID:            &tid,
		AmountCents:         9900,
		OriginalAmountCents: 19900, // 模拟升级抵扣后的实付≠原价
		Status:              "refunded",
		RefundAmountCents:   9900,
		RefundedAt:          &now,
		Channel:             "mock",
	}
	// C7 红线口径：事务外直写 + 显式 model.TenantID（租户盖章回调对非零值不覆写）
	if err := db.DB.Create(order).Error; err != nil {
		t.Fatalf("构造退款订单失败: %v", err)
	}

	c, w := newCtx(t, tid)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/billing/orders", nil)
	ListBillingOrders(c)
	if w.Code != http.StatusOK {
		t.Fatalf("列表接口状态码 %d，期望 200", w.Code)
	}
	var body struct {
		Code int `json:"code"`
		Data []struct {
			OrderNo             string     `json:"order_no"`
			Status              string     `json:"status"`
			AmountCents         int        `json:"amount_cents"`
			OriginalAmountCents int        `json:"original_amount_cents"`
			RefundAmountCents   int        `json:"refund_amount_cents"`
			RefundedAt          *time.Time `json:"refunded_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应解析失败: %v body=%s", err, w.Body.String())
	}
	var hit *struct {
		OrderNo             string     `json:"order_no"`
		Status              string     `json:"status"`
		AmountCents         int        `json:"amount_cents"`
		OriginalAmountCents int        `json:"original_amount_cents"`
		RefundAmountCents   int        `json:"refund_amount_cents"`
		RefundedAt          *time.Time `json:"refunded_at"`
	}
	for i := range body.Data {
		if body.Data[i].OrderNo == order.OrderNo {
			hit = &body.Data[i]
			break
		}
	}
	if hit == nil {
		t.Fatalf("列表未返回本单 order_no=%s body=%s", order.OrderNo, w.Body.String())
	}
	if hit.RefundAmountCents != 9900 {
		t.Errorf("refund_amount_cents=%d，期望 9900（F1：白名单漏列则恒为0）", hit.RefundAmountCents)
	}
	if hit.RefundedAt == nil {
		t.Error("refunded_at 不应为空（F1：白名单漏列则恒为null）")
	}
	if hit.OriginalAmountCents != 19900 {
		t.Errorf("original_amount_cents=%d，期望 19900（F1：白名单漏列则恒为0）", hit.OriginalAmountCents)
	}
	if hit.Status != "refunded" {
		t.Errorf("status=%s，期望 refunded", hit.Status)
	}
}
