// D2/D4 回归锁定（2026-09-16B，见 AUDIT_UAT_VERIFY_2026-09-16B）：
// D2：OneID 合并事务化——迁移全量落库 + 合并后同客户仅保留 1 条 active 会话（收拢）；
// D4：CheckHumanTimeout 软/硬超时判定必须持久化（旧实现只改内存，下轮重载即漂移）。
package chatflow

import (
	"fmt"
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/testutil"
)

func TestMergeCustomerByPhoneTxAndCollapseActives(t *testing.T) {
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	tid := testutil.CreateTenant(t)
	phone := fmt.Sprintf("139%08d", time.Now().UnixNano()%100000000)

	survivor := model.Customer{TenantID: tid, Name: "老客户", Phone: phone, JourneyStage: model.JourneyAIConnected, Status: 1}
	guest := model.Customer{TenantID: tid, Name: "访客", JourneyStage: model.JourneyLeadCaptured, Status: 1}
	if err := db.DB.Create(&survivor).Error; err != nil {
		t.Fatalf("建老客户失败: %v", err)
	}
	if err := db.DB.Create(&guest).Error; err != nil {
		t.Fatalf("建访客失败: %v", err)
	}
	t.Cleanup(func() {
		for _, cid := range []uint{survivor.ID, guest.ID} {
			db.DB.Where("customer_id = ?", cid).Delete(&model.Message{})
			db.DB.Where("customer_id = ?", cid).Delete(&model.Conversation{})
			db.DB.Where("customer_id = ?", cid).Delete(&model.FollowUp{})
			db.DB.Where("customer_id = ?", cid).Delete(&model.CustomerIdentity{})
			db.DB.Delete(&model.Customer{}, cid)
		}
	})

	now := time.Now()
	mkConv := func(custID uint, status string, updated time.Time) model.Conversation {
		conv := model.Conversation{TenantID: tid, CustomerID: custID, Status: status, Mode: "ai", UpdatedAt: updated}
		if err := db.DB.Create(&conv).Error; err != nil {
			t.Fatalf("建会话失败: %v", err)
		}
		db.DB.Model(&conv).UpdateColumn("updated_at", updated) // UpdateColumn 跳过自动时间戳（Update 会被再次覆写成 now）
		return conv
	}
	// 访客 1 active（最新）+ 1 closed + 老客户 1 active（较旧）+ 1 closed：
	// 合并后 active 应只剩"最新的一条"（访客最新会话，updated_at 最大）。
	// G1 收口(2026-09-16C)：013 唯一索引 ux_conv_one_active 上线后，"同客户多条 active"
	// 在 DB 层已不可能建出（旧种子第 2 条访客 active 直接 23505），种子随之改为 closed——
	// 预收拢 SQL 的跨客户定序逻辑仍被完整覆盖（双方各 1 active → 关旧保新）。
	gNew := mkConv(guest.ID, "active", now.Add(-time.Minute))
	mkConv(guest.ID, "closed", now.Add(-2*time.Hour))
	mkConv(survivor.ID, "active", now.Add(-3*time.Hour))
	closedOld := mkConv(survivor.ID, "closed", now.Add(-4*time.Hour))
	msg := model.Message{TenantID: tid, CustomerID: guest.ID, ConversationID: gNew.ID, SenderType: "customer", Content: "留个手机号"}
	if err := db.DB.Create(&msg).Error; err != nil {
		t.Fatalf("建消息失败: %v", err)
	}

	target := MergeCustomerByPhone(&guest, phone)
	if target != survivor.ID {
		t.Fatalf("应返回老客户ID %d，实际 %d", survivor.ID, target)
	}

	var g model.Customer
	db.DB.First(&g, guest.ID)
	if g.Status != 0 {
		t.Errorf("访客应置 status=0，实际 %d", g.Status)
	}
	var s model.Customer
	db.DB.First(&s, survivor.ID)
	if s.JourneyStage != model.JourneyLeadCaptured {
		t.Errorf("阶段应取更高(lead_captured)，实际 %s", s.JourneyStage)
	}

	var activeCount int64
	db.DB.Model(&model.Conversation{}).Where("customer_id = ? AND status = ?", survivor.ID, "active").Count(&activeCount)
	if activeCount != 1 {
		var ids []uint
		db.DB.Model(&model.Conversation{}).Where("customer_id = ? AND status = ?", survivor.ID, "active").Pluck("id", &ids)
		t.Errorf("合并后应仅 1 条 active 会话(收拢)，实际 %d 条: %v", activeCount, ids)
	}
	var kept model.Conversation
	db.DB.Where("customer_id = ? AND status = ?", survivor.ID, "active").First(&kept)
	if kept.ID != gNew.ID {
		t.Errorf("应保留 updated_at 最新的会话 %d，实际 %d", gNew.ID, kept.ID)
	}
	var stillClosed model.Conversation
	db.DB.First(&stillClosed, closedOld.ID)
	if stillClosed.Status != "closed" {
		t.Errorf("原 closed 会话不应被改动，实际 %s", stillClosed.Status)
	}
	var migratedMsgs int64
	db.DB.Model(&model.Message{}).Where("id = ? AND customer_id = ?", msg.ID, survivor.ID).Count(&migratedMsgs)
	if migratedMsgs != 1 {
		t.Errorf("消息应迁至老客户名下，实际 %d", migratedMsgs)
	}

	// —— 反序场景（2026-09-16C）：老客户 active 更新 → 保留老客户会话，访客那条预收拢关账 ——
	phone2 := fmt.Sprintf("139%08d", (time.Now().UnixNano()+7)%100000000)
	survivor2 := model.Customer{TenantID: tid, Name: "老客户2", Phone: phone2, JourneyStage: model.JourneyAIConnected, Status: 1}
	guest2 := model.Customer{TenantID: tid, Name: "访客2", JourneyStage: model.JourneyAIConnected, Status: 1}
	if err := db.DB.Create(&survivor2).Error; err != nil {
		t.Fatalf("建老客户2失败: %v", err)
	}
	if err := db.DB.Create(&guest2).Error; err != nil {
		t.Fatalf("建访客2失败: %v", err)
	}
	t.Cleanup(func() {
		for _, cid := range []uint{survivor2.ID, guest2.ID} {
			db.DB.Where("customer_id = ?", cid).Delete(&model.Message{})
			db.DB.Where("customer_id = ?", cid).Delete(&model.Conversation{})
			db.DB.Delete(&model.Customer{}, cid)
		}
	})
	guest2Act := mkConv(guest2.ID, "active", now.Add(-5*time.Hour))
	surv2Act := mkConv(survivor2.ID, "active", now.Add(-time.Minute))
	if target2 := MergeCustomerByPhone(&guest2, phone2); target2 != survivor2.ID {
		t.Fatalf("反序场景应返回老客户2 ID %d，实际 %d", survivor2.ID, target2)
	}
	var kept2 model.Conversation
	var active2 []uint
	db.DB.Model(&model.Conversation{}).Where("customer_id = ? AND status = ?", survivor2.ID, "active").Pluck("id", &active2)
	if len(active2) != 1 || active2[0] != surv2Act.ID {
		t.Errorf("反序场景应仅保留老客户2会话 %d，实际 %v", surv2Act.ID, active2)
	}
	db.DB.First(&kept2, guest2Act.ID)
	if kept2.Status != "closed" || kept2.CustomerID != survivor2.ID {
		t.Errorf("访客2会话应关账并迁至老客户2，实际 %s/%d", kept2.Status, kept2.CustomerID)
	}
}

func TestCheckHumanTimeoutPersists(t *testing.T) {
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	tid := testutil.CreateTenant(t)
	old := time.Now().Add(-time.Hour)

	// CheckHumanTimeout 读热配置，测试注入静态服务（同 channel worker 测试口径）
	oldRC := runtimecfg.DefaultSystemConfigService
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(map[string]string{
		"human_timeout_seconds": "180",
	}, nil)
	defer func() { runtimecfg.DefaultSystemConfigService = oldRC }()

	// 硬超时：mode=human 无锁无回复记录 → AI 接管且必须落库
	hc := model.Conversation{TenantID: tid, CustomerID: 999991, Status: "active", Mode: "human", LastMessageAt: &old}
	if err := db.DB.Create(&hc).Error; err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	defer db.DB.Delete(&model.Conversation{}, hc.ID)
	if !CheckHumanTimeout(&hc) {
		t.Fatal("1小时无人工回复应判超时")
	}
	var hcRow model.Conversation
	db.DB.First(&hcRow, hc.ID)
	if hcRow.Mode != "ai" || hcRow.IsHumanLocked {
		t.Errorf("硬超时须持久化 mode=ai/未锁定，实际 mode=%s locked=%v", hcRow.Mode, hcRow.IsHumanLocked)
	}

	// 软超时：pending_handoff 且通知超 1h → 回退纯 AI 且清标记落库
	sc := model.Conversation{TenantID: tid, CustomerID: 999992, Status: "active", Mode: "ai",
		PendingHandoff: true, HandoffNotifiedAt: &old}
	if err := db.DB.Create(&sc).Error; err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	defer db.DB.Delete(&model.Conversation{}, sc.ID)
	if CheckHumanTimeout(&sc) {
		t.Fatal("软超时分支不应返回切人工")
	}
	var scRow model.Conversation
	db.DB.First(&scRow, sc.ID)
	if scRow.PendingHandoff || scRow.HandoffNotifiedAt != nil {
		t.Errorf("软超时须持久化清 pending_handoff/notified_at，实际 %v/%v", scRow.PendingHandoff, scRow.HandoffNotifiedAt)
	}

	// 未超时不回写：pending_handoff 刚通知，保持原样
	fresh := time.Now()
	fc := model.Conversation{TenantID: tid, CustomerID: 999993, Status: "active", Mode: "ai",
		PendingHandoff: true, HandoffNotifiedAt: &fresh}
	if err := db.DB.Create(&fc).Error; err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	defer db.DB.Delete(&model.Conversation{}, fc.ID)
	if CheckHumanTimeout(&fc) {
		t.Fatal("刚通知不应判超时")
	}
	var fcRow model.Conversation
	db.DB.First(&fcRow, fc.ID)
	if !fcRow.PendingHandoff {
		t.Error("未超时时 pending_handoff 不应被清（内存判定勿践踏接管态）")
	}
}
