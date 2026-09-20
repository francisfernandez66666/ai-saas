// 出站事件 webhook 单测（D6，2026-09-12）：签名校验 / 退避重试至成功 / 连续失败熔断。
// 全程 httptest 本地假接收端，不依赖外网。
package webhook

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// TestSignVerifyRoundTrip 覆盖 SignVerifyRoundTrip 相关行为与边界。
func TestSignVerifyRoundTrip(t *testing.T) {
	ts, body := "1700000000", `{"a":1}`
	sig := "sha256=" + Sign("s3cr3t", ts, body)
	if err := VerifySignature("s3cr3t", ts, body, sig); err != nil {
		t.Fatalf("正确签名应通过: %v", err)
	}
	if err := VerifySignature("wrong", ts, body, sig); err == nil {
		t.Fatal("错误密钥应拒绝")
	}
	if err := VerifySignature("s3cr3t", ts, `{"a":2}`, sig); err == nil {
		t.Fatal("篡改 body 应拒绝")
	}
}

// newSub 创建测试租户 Webhook 订阅。
func newSub(t *testing.T, tid uint, url, secret, events string) uint {
	t.Helper()
	w := model.TenantWebhook{TenantID: tid, Name: "test", URL: url, Secret: secret, Events: events, Active: true}
	if err := db.DB.Create(&w).Error; err != nil {
		t.Fatal(err)
	}
	return w.ID
}

// 签名投递 + 接收端校验：Emit→ProcessDue，服务端应收到合法签名与事件头
func TestEmitDeliversSignedPayload(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	secret := "top_secret"
	var gotSigValid atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sig := r.Header.Get(signatureHeader)
		ts := r.Header.Get(timestampHeader)
		err := VerifySignature(secret, ts, string(body), sig)
		gotSigValid.Store(err == nil)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	newSub(t, tid, srv.URL, secret, model.WebhookEventPaymentPaid)
	n := Emit(tid, model.WebhookEventPaymentPaid, map[string]interface{}{"order_no": "ORD1"})
	if n != 1 {
		t.Fatalf("应入队 1 条，实际 %d", n)
	}
	delivered, _, _ := ProcessDue()
	if delivered != 1 {
		t.Fatalf("应成功投递 1 条，实际 %d", delivered)
	}
	if v, _ := gotSigValid.Load().(bool); !v {
		t.Fatal("接收端签名校验应通过")
	}
	// 投递记录置 delivered
	var d model.WebhookDelivery
	db.DB.Where("webhook_id = (SELECT id FROM tenant_webhooks WHERE tenant_id = ? LIMIT 1)", tid).
		Order("id DESC").First(&d)
	if d.Status != model.WebhookDeliveryDelivered {
		t.Fatalf("状态应 delivered，实际 %s", d.Status)
	}
}

// 事件未订阅 → 不入队
func TestEmitSkipsUnsubscribed(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	newSub(t, tid, "http://127.0.0.1:1", "s", model.WebhookEventPaymentPaid)
	if n := Emit(tid, model.WebhookEventOrderRefunded, map[string]interface{}{}); n != 0 {
		t.Fatalf("未订阅事件不应入队，实际 %d", n)
	}
}

// 前两次失败第三次成功 → 退避重试最终 delivered，attempts 递增
func TestRetryThenSuccess(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k := atomic.AddInt32(&hits, 1)
		if k < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wid := newSub(t, tid, srv.URL, "s", model.WebhookEventHumanAssigned)
	Emit(tid, model.WebhookEventHumanAssigned, map[string]interface{}{"x": 1})

	// 反复 ProcessDue，并把 next_retry_at 提前（测试免等真实退避）
	for i := 0; i < 8; i++ {
		db.DB.Model(&model.WebhookDelivery{}).Where("status = ?", model.WebhookDeliveryPending).
			Update("next_retry_at", time.Now().Add(-time.Minute))
		delivered, _, _ := ProcessDue()
		if delivered == 1 {
			var d model.WebhookDelivery
			db.DB.Where("webhook_id = ?", wid).Order("id DESC").First(&d)
			if d.Attempts < 2 {
				t.Fatalf("成功前应有重试次数，attempts=%d", d.Attempts)
			}
			return
		}
	}
	t.Fatal("三次后应最终投递成功")
}

// 连续失败达阈值 → 订阅熔断（active=false, disabled_at 非空）
func TestCircuitBreakerDisablesSubscription(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	wid := newSub(t, tid, srv.URL, "s", model.WebhookEventLeadCaptured)
	// 预置失败计数到熔断阈值-1，再打一次失败即触发
	db.DB.Model(&model.TenantWebhook{}).Where("id = ?", wid).Update("fail_count", circuitFailN-1)
	Emit(tid, model.WebhookEventLeadCaptured, map[string]interface{}{"y": 2})
	db.DB.Model(&model.WebhookDelivery{}).Where("webhook_id = ?", wid).
		Update("next_retry_at", time.Now().Add(-time.Minute))
	ProcessDue()

	var w model.TenantWebhook
	db.DB.First(&w, wid)
	if w.Active || w.DisabledAt == nil {
		t.Fatalf("达阈值连续失败应熔断停用，active=%v disabled_at=%v", w.Active, w.DisabledAt)
	}
}

// maxAttempts 超限 → 死信 dead（防退避无限堆积）
func TestMaxAttemptsGoesDead(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	wid := newSub(t, tid, srv.URL, "s", model.WebhookEventPaymentPaid)
	Emit(tid, model.WebhookEventPaymentPaid, map[string]interface{}{"z": 3})
	// 直接构造一条接近上限的 pending 投递，跑一次即应转 dead
	var d model.WebhookDelivery
	db.DB.Where("webhook_id = ?", wid).Order("id DESC").First(&d)
	db.DB.Model(&d).Updates(map[string]interface{}{
		"attempts": maxAttempts, "next_retry_at": time.Now().Add(-time.Minute),
	})
	_, _, dead := ProcessDue()
	if dead < 1 {
		t.Fatal("超上限应转死信")
	}
	db.DB.First(&d, d.ID)
	if d.Status != model.WebhookDeliveryDead {
		t.Fatalf("状态应为 dead，实际 %s", d.Status)
	}
}

// TestClaimDueDeliveriesP23 P2-3(2026-09-20 批三)行级 claim 单测：
// ①取单即置 sending——同轮二次 claim 不得再拿到同一行（双投封堵）；
// ②超 staleSendingTTL 的 sending 行（持有者崩溃模拟）必须被复活并可再次领取；
// ③退避回炉路径状态回到 pending（否则预占态泄漏，消息永远卡在 sending）。
func TestClaimDueDeliveriesP23(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	wid := newSub(t, tid, "http://127.0.0.1:1/never", "s", model.WebhookEventHumanAssigned)
	Emit(tid, model.WebhookEventHumanAssigned, map[string]interface{}{"p23": 1})

	// ① 两轮 claim：第一轮全部预占为 sending，第二轮必须空
	first := claimDueDeliveries(time.Now())
	if len(first) == 0 {
		t.Fatal("首轮 claim 应取到到期投递")
	}
	for _, d := range first {
		if d.WebhookID != wid {
			t.Fatalf("取到非本测试投递 id=%d", d.ID)
		}
	}
	// Emit 可能带出其它测试租户遗留行？SetupTestDB 事务回滚模型按表清理——只断言本订阅行被锁
	var st string
	db.DB.Model(&model.WebhookDelivery{}).Where("webhook_id = ? ", wid).Select("status").Scan(&st)
	if st != model.WebhookDeliverySending {
		t.Fatalf("取单后本订阅投递应处 sending 预占，实际 %s", st)
	}
	second := claimDueDeliveries(time.Now())
	for _, d := range second {
		if d.WebhookID == wid {
			t.Fatal("同轮二次 claim 不得重复取到已预占行（双投）")
		}
	}

	// ③（先做，复活依赖回炉语义独立）退避回炉：模拟发送失败路径写回 pending
	db.DB.Model(&model.WebhookDelivery{}).Where("webhook_id = ?", wid).
		Update("status", model.WebhookDeliveryPending)
	again := claimDueDeliveries(time.Now())
	found := false
	for _, d := range again {
		if d.WebhookID == wid {
			found = true
		}
	}
	if !found {
		t.Fatal("pending 回炉后应可再次领取")
	}

	// ② 崩溃复活：把 sending 行的 updated_at 拨到 staleSendingTTL 之前，claim 应复活并重取
	db.DB.Model(&model.WebhookDelivery{}).Where("webhook_id = ?", wid).
		UpdateColumn("updated_at", time.Now().Add(-staleSendingTTL-time.Minute))
	revived := claimDueDeliveries(time.Now())
	found = false
	for _, d := range revived {
		if d.WebhookID == wid {
			found = true
		}
	}
	if !found {
		t.Fatal("超时 sending 行必须被复活领取（崩溃窗口自愈）")
	}
	var cnt int64
	db.DB.Model(&model.WebhookDelivery{}).Where("webhook_id = ? AND status = ?", wid, model.WebhookDeliveryPending).Count(&cnt)
	if cnt != 0 {
		t.Fatalf("复活领取后不应残留 pending，残留 %d", cnt)
	}
}
