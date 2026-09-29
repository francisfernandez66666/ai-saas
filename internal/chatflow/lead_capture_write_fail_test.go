// FIX-2（2026-09-29 审计批）回归锁：留资画像**落库失败不得对外宣称成功**。
//
// 钉住的事故形态：旧实现 `db.DB.Model(customer).Updates(updates)` 丢弃错误后，
// 照打「客户%d留资成功」、照同步内存镜像、照发 lead_captured/human_assigned webhook——
// 库里 phone/阶段/顾问一个都没写进去。表现是"顾问端永远看不见这条线索，
// 而商户系统收到了假事件、日志是一行成功"，线索静默蒸发且无从解释。
//
// 判别力（把检错改回吞错的变异必红在哪）：
//   - err 必回：吞错版返回 nil → ① 红；
//   - 内存镜像不得污染：吞错版把 customer.Phone 置成注入值 → ② 红；
//   - webhook_deliveries 必须零新增：吞错版照发 Emit → ③ 红；
//   - 正向对照锁"修完没把成功路径改坏"（镜像同步 + 两条投递行入队）。
package chatflow

import (
	"errors"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

var (
	errLeadUTInjected = errors.New("UT注入：留资画像写入失败")
	leadWriteFail     atomic.Bool
)

const leadWriteHookName = "ut:lead_capture_fail"

// installLeadWriteFault 只对 customers 表、且 Updates 映射里带 assignment_reason
// （留资画像写的独有键）的语句注错——别家对 customers 的更新（如标签/阶段推进）
// 不受影响，误伤面为零。
func installLeadWriteFault(t *testing.T) {
	t.Helper()
	if db.DB == nil {
		t.Fatalf("db.DB 未初始化（SetupTestDB 未跑？）")
	}
	if err := db.DB.Callback().Update().Before("gorm:update").Register(leadWriteHookName, func(tx *gorm.DB) {
		if !leadWriteFail.Load() {
			return
		}
		if _, ok := tx.Statement.Model.(*model.Customer); !ok {
			return
		}
		if m, ok := tx.Statement.Dest.(map[string]interface{}); ok {
			if _, has := m["assignment_reason"]; has {
				tx.AddError(errLeadUTInjected) // GORM v2：回调内 AddError 即中止本条 update
			}
		}
	}); err != nil {
		t.Fatalf("注册留资注错回调失败: %v", err)
	}
	t.Cleanup(func() {
		leadWriteFail.Store(false)
		_ = db.DB.Callback().Update().Remove(leadWriteHookName)
	})
}

func leadUpdates() map[string]interface{} {
	return map[string]interface{}{
		"phone":             "13800001111",
		"journey_stage":     model.JourneyLeadCaptured,
		"assignment_reason": "lead_captured",
		"assigned_user_id":  uint(7),
	}
}

func deliveriesCount(t *testing.T, tid uint) int64 {
	t.Helper()
	var n int64
	if err := db.DB.Model(&model.WebhookDelivery{}).Where("tenant_id = ?", tid).Count(&n).Error; err != nil {
		t.Fatalf("数投递行失败: %v", err)
	}
	return n
}

// TestLeadWriteFaultTeeth 前置自证：注错真的会炸（开关开→留资画像写必失败；
// 开关关→同一条写成功）。缺这一条，下面的"失败路径"断言可能全在成功路径上空转。
func TestLeadWriteFaultTeeth(t *testing.T) {
	testutil.SetupTestDB(t)
	installLeadWriteFault(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	cust := model.Customer{TenantID: tid, Name: "UT留资注错齿", JourneyStage: model.JourneyAIConnected}
	if err := db.DB.Create(&cust).Error; err != nil {
		t.Fatalf("建测试客户失败: %v", err)
	}
	defer db.DB.Delete(&model.Customer{}, cust.ID)

	leadWriteFail.Store(true)
	if err := db.DB.Model(&cust).Updates(leadUpdates()).Error; err == nil {
		t.Fatalf("开关开时留资画像写必须失败（注入没生效=后面全空转）")
	}
	leadWriteFail.Store(false)
	if err := db.DB.Model(&cust).Updates(leadUpdates()).Error; err != nil {
		t.Fatalf("开关关时同一条写应成功: %v", err)
	}
}

// TestApplyLeadCapturedUpdates_FailNoSideEffects 核心反证：写失败 →
// 返回 error、内存镜像不被污染、webhook 一条都不发、库里 phone 仍是空。
func TestApplyLeadCapturedUpdates_FailNoSideEffects(t *testing.T) {
	testutil.SetupTestDB(t)
	installLeadWriteFault(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	cust := model.Customer{TenantID: tid, Name: "UT留资吞错锁", JourneyStage: model.JourneyAIConnected}
	if err := db.DB.Create(&cust).Error; err != nil {
		t.Fatalf("建测试客户失败: %v", err)
	}
	defer db.DB.Delete(&model.Customer{}, cust.ID)
	// 订阅 lead_captured + human_assigned：失败路径若照发 Emit，投递行会留形
	wh := model.TenantWebhook{TenantID: tid, Name: "ut-lead",
		URL:    "http://127.0.0.1:9/none",
		Events: model.WebhookEventLeadCaptured + "," + model.WebhookEventHumanAssigned, Active: true}
	if err := db.DB.Create(&wh).Error; err != nil {
		t.Fatalf("建订阅失败: %v", err)
	}
	defer db.DB.Delete(&model.TenantWebhook{}, wh.ID)

	leadWriteFail.Store(true)
	err := applyLeadCapturedUpdates(&cust, "13800001111", leadUpdates())
	if err == nil {
		t.Fatalf("写库失败必须返回 error（旧吞错写法在此返回 nil 并继续报喜）")
	}
	if cust.Phone != "" {
		t.Fatalf("写失败后内存镜像不得被污染（假同步会让后续链路当留资已成）: phone=%q", cust.Phone)
	}
	if cust.JourneyStage != model.JourneyAIConnected {
		t.Fatalf("写失败后旅程阶段不得前移: stage=%q", cust.JourneyStage)
	}
	var got model.Customer
	if err := db.DB.First(&got, cust.ID).Error; err != nil {
		t.Fatalf("重读客户失败: %v", err)
	}
	if got.Phone != "" {
		t.Fatalf("库里 phone 必须仍为空: %q", got.Phone)
	}
	if n := deliveriesCount(t, tid); n != 0 {
		t.Fatalf("写失败不得外发任何 webhook（商户收到假 lead_captured 比收不到更糟），实得 %d 行", n)
	}
}

// TestApplyLeadCapturedUpdates_SuccessPathIntact 正向对照：修完检错，
// 成功路径的镜像同步与两条 webhook 入队必须原样在场（防"为了安全把副作用删没"）。
func TestApplyLeadCapturedUpdates_SuccessPathIntact(t *testing.T) {
	testutil.SetupTestDB(t)
	installLeadWriteFault(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	cust := model.Customer{TenantID: tid, Name: "UT留资正向", JourneyStage: model.JourneyAIConnected}
	if err := db.DB.Create(&cust).Error; err != nil {
		t.Fatalf("建测试客户失败: %v", err)
	}
	defer db.DB.Delete(&model.Customer{}, cust.ID)
	wh := model.TenantWebhook{TenantID: tid, Name: "ut-lead-ok",
		URL:    "http://127.0.0.1:9/none",
		Events: model.WebhookEventLeadCaptured + "," + model.WebhookEventHumanAssigned, Active: true}
	if err := db.DB.Create(&wh).Error; err != nil {
		t.Fatalf("建订阅失败: %v", err)
	}
	defer db.DB.Delete(&model.TenantWebhook{}, wh.ID)

	if err := applyLeadCapturedUpdates(&cust, "13800001111", leadUpdates()); err != nil {
		t.Fatalf("无注入时留资写应成功: %v", err)
	}
	if cust.Phone != "13800001111" || cust.JourneyStage != model.JourneyLeadCaptured || cust.AssignedUserID != 7 {
		t.Fatalf("成功后镜像应同步: phone=%q stage=%q assigned=%d", cust.Phone, cust.JourneyStage, cust.AssignedUserID)
	}
	var got model.Customer
	if err := db.DB.First(&got, cust.ID).Error; err != nil {
		t.Fatalf("重读客户失败: %v", err)
	}
	if got.Phone != "13800001111" || got.JourneyStage != model.JourneyLeadCaptured {
		t.Fatalf("成功后库里应写进 phone/阶段")
	}
	// lead_captured + human_assigned 两条入队（assigned=7>0）
	if n := deliveriesCount(t, tid); n != 2 {
		t.Fatalf("成功路径应恰入队 2 条投递，实得 %d", n)
	}
	db.DB.Where("tenant_id = ?", tid).Delete(&model.WebhookDelivery{})
}
