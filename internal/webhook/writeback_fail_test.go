// FIX-5 反证单测（2026-09-29 端到端审计批）：webhook 投递终态回写失败必须**可见**，
// 且不能把"已经发出去的事件"在库里写成 delivered。
//
// 旧写法 `db.DB.Model(d).Updates(...)` 不查错，于是出现这样一种分叉：HTTP 200 真发出去了，
// 库里那行却还留在 sending；claimDueDeliveries 的陈旧复活路径把它转回 pending 再投一次
// （接收端收到两遍），而死信台账/投递记录里完全看不出发生过异常。
// 修法口径是**宁重投不可丢**（与通道出站"宁双发不漏发"同一取舍）：回写失败不回收已发出的
// 投递，但要计数 + WARN。所以本测钉三件事，缺一件就是没修：
//
//	① 注入失败后库里状态**不得**是 delivered（防"日志说成功、库里也说成功"的双面谎言）；
//	② 事件确实发出去了（hits≥1，防把这条修做成"回写失败就不投"——那是丢事件）；
//	③ 失败计数恰 +1（可见性本体；只看 /metrics 文本分不清"从没 + 过"和"渲染坏了"）。
//
// 正向对照 ④：注入摘掉后，把 sending 行拨老，claim 必须复活重投并最终落成 delivered
// ——证明"重投"是这条取舍的真实后果，不是测试臆想的分支。
//
// 变异口径（如实记）：把 ProcessDue 里 delivered 回写的 `if werr != nil` 那两条（计数+日志）
// 删掉，③ 必红；把回写整个改成不判错（回到旧写法），①③ 同时红。本轮未真跑这两刀
// （分类器拦"故意植入坏代码"类实验），判据形态按上述说明可复现。
package webhook

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"ai-scrm/internal/db"
	"ai-scrm/internal/metrics"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// injectedFailCallbackName 注入回调的注册名，摘除时按名 Remove（勿用通配，防把别的回调带走）
const injectedFailCallbackName = "test:fail_delivered_writeback"

// TestDeliveredWritebackFailureIsVisible 注入"delivered 终态回写失败"，验 ①②③ 与正向对照 ④
func TestDeliveredWritebackFailureIsVisible(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wid := newSub(t, tid, srv.URL, "fix5_secret", model.WebhookEventPaymentPaid)
	if n := Emit(tid, model.WebhookEventPaymentPaid, map[string]interface{}{"order_no": "FIX5-WB-1"}); n != 1 {
		t.Fatalf("应入队 1 条投递，实得 %d", n)
	}

	// 注入点只认"把投递行写成 delivered"这一条 UPDATE：claim 的预占(sending)/回炉(pending)
	// 目标值不同，不受影响——否则测的就不是回写失败，而是整条链路坏了。
	if err := db.DB.Callback().Update().Before("gorm:update").Register(injectedFailCallbackName, func(tx *gorm.DB) {
		if m, ok := tx.Statement.Dest.(map[string]interface{}); ok {
			if s, _ := m["status"].(string); s == model.WebhookDeliveryDelivered {
				tx.AddError(errors.New("注入：delivered 终态回写失败"))
			}
		}
	}); err != nil {
		t.Fatalf("注册注入回调失败: %v", err)
	}
	base := metrics.WebhookWritebackFailCount()

	ProcessDue() // 本轮：HTTP 发出去，终态回写被注入打断

	if err := db.DB.Callback().Update().Remove(injectedFailCallbackName); err != nil {
		t.Fatalf("摘除注入回调失败: %v", err)
	}

	// ② 事件真发出去了：回写失败不得反过来变成"不投"
	if got := atomic.LoadInt32(&hits); got < 1 {
		t.Fatalf("投递应已发出（宁重投不可丢），实得接收端命中 %d 次", got)
	}

	// ① 库里状态不得写成 delivered：留在 sending 才有"下一轮复活重投"的可能，
	//    也才是如实的"结果没落成"
	var d model.WebhookDelivery
	if err := db.DB.Where("webhook_id = ?", wid).Order("id DESC").First(&d).Error; err != nil {
		t.Fatalf("回读投递行失败: %v", err)
	}
	if d.Status == model.WebhookDeliveryDelivered {
		t.Fatalf("回写明明被注入失败，库里却落成 delivered——吞错未除根（双面谎言现场）")
	}
	if d.Status != model.WebhookDeliverySending {
		t.Errorf("注入失败后该行应仍停在 sending，实得 %s", d.Status)
	}

	// ③ 失败计数恰 +1：本轮只有一条 delivered 回写会撞注入
	if got := metrics.WebhookWritebackFailCount(); got != base+1 {
		t.Errorf("回写失败计数应恰 +1（基线 %d，实得 %d）——计数不在场即“重投不可见”", base, got)
	}

	// ④ 正向对照：拨老 sending 行 → claim 复活重投，最终必须落成 delivered
	if err := db.DB.Model(&model.WebhookDelivery{}).Where("id = ?", d.ID).
		UpdateColumn("updated_at", time.Now().Add(-staleSendingTTL-time.Minute)).Error; err != nil {
		t.Fatalf("拨老 updated_at 失败: %v", err)
	}
	hitsBefore := atomic.LoadInt32(&hits)
	delivered, _, _ := ProcessDue()
	if delivered < 1 {
		t.Errorf("陈旧 sending 行应被复活重投并计为 delivered，实得 %d", delivered)
	}
	if atomic.LoadInt32(&hits) <= hitsBefore {
		t.Errorf("接收端应收到第二次（宁重投不可丢的代价，必须真发生才算对照到位），命中数没涨")
	}
	var final model.WebhookDelivery
	db.DB.First(&final, d.ID)
	if final.Status != model.WebhookDeliveryDelivered {
		t.Errorf("注入摘掉后终态应能落成 delivered，实得 %s", final.Status)
	}
}
