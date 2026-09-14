// PIPL 删除权单测（C2，2026-09-12）：入队幂等 + 匿名化断内容保数值 + 二次执行无副作用。
package privacy

import (
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// seedCustomerWithChat 构造测试种子数据。
func seedCustomerWithChat(t *testing.T, tid uint) (uint, []uint) {
	t.Helper()
	cu := model.Customer{
		TenantID: tid, Name: "张三", Phone: "13800001111", WechatID: "wx_zhang",
		VisitorKey: "vk_secret_abc", IntentScore: 0.55, JourneyStage: "lead_captured",
	}
	if err := db.DB.Create(&cu).Error; err != nil {
		t.Fatal(err)
	}
	msgs := []model.Message{
		{TenantID: tid, CustomerID: cu.ID, SenderType: "customer", Content: "我手机13800001111，想约周末看车", IntentScore: 0.55},
		{TenantID: tid, CustomerID: cu.ID, SenderType: "ai", Content: "好的，周末到店可以吗", IntentScore: 0.6},
	}
	var ids []uint
	for i := range msgs {
		if err := db.DB.Create(&msgs[i]).Error; err != nil {
			t.Fatal(err)
		}
		ids = append(ids, msgs[i].ID)
	}
	db.DB.Create(&model.CdpProfile{TenantID: tid, CustomerID: cu.ID, CdpId: "cdp_" + time.Now().Format("150405.000"), ProfileName: "张三", ProfileData: `{"phone":"13800001111"}`})
	return cu.ID, ids
}

// TestDeletionEnqueueIdempotentAndAnonymize 覆盖 DeletionEnqueueIdempotentAndAnonymize 相关行为与边界。
func TestDeletionEnqueueIdempotentAndAnonymize(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	custID, msgIDs := seedCustomerWithChat(t, tid)

	// 1) 入队：首次成功
	row, err := Enqueue(tid, model.DeletionScopeCustomer, custID, 0, time.Hour)
	if err != nil {
		t.Fatalf("首次受理应成功: %v", err)
	}
	if row.Status != model.DeletionStatusPending {
		t.Fatalf("初始状态应 pending，实际 %s", row.Status)
	}

	// 2) 重复入队：幂等返回原行 + ErrAlreadyPending，不产生第二条 pending
	row2, err := Enqueue(tid, model.DeletionScopeCustomer, custID, 0, time.Hour)
	if err != ErrAlreadyPending {
		t.Fatalf("重复受理应返回 ErrAlreadyPending，实际 %v", err)
	}
	if row2.ID != row.ID {
		t.Fatalf("重复受理应返回同一行，%d vs %d", row2.ID, row.ID)
	}
	var pendingCount int64
	db.DB.Model(&model.DeletionRequest{}).Where("tenant_id = ? AND customer_id = ? AND status = ?", tid, custID, model.DeletionStatusPending).Count(&pendingCount)
	if pendingCount != 1 {
		t.Fatalf("pending 行应唯一，实际 %d", pendingCount)
	}

	// 3) 执行匿名化
	if err := ExecuteDeletion(row.ID); err != nil {
		t.Fatalf("执行匿名化失败: %v", err)
	}

	// 3a) 客户 PII 清空
	var cu model.Customer
	db.DB.First(&cu, custID)
	if cu.Phone != "" || cu.Name != "" || cu.VisitorKey != "" || cu.WechatID != "" {
		t.Fatalf("客户 PII 应清空，实际 phone=%q name=%q vk=%q wx=%q", cu.Phone, cu.Name, cu.VisitorKey, cu.WechatID)
	}
	// 3b) 数值统计列保留（经营分析/反哺不失真）
	if cu.IntentScore != 0.55 || cu.JourneyStage != "lead_captured" {
		t.Fatalf("统计/阶段列应保留，实际 intent=%v stage=%q", cu.IntentScore, cu.JourneyStage)
	}

	// 3c) 消息内容断链（anon: 前缀），但行数与数值列不变
	var msgs []model.Message
	db.DB.Where("id IN ?", msgIDs).Find(&msgs)
	if len(msgs) != 2 {
		t.Fatalf("消息行数应保留（2），实际 %d", len(msgs))
	}
	for _, m := range msgs {
		if len(m.Content) < 5 || m.Content[:5] != anonPrefix {
			t.Fatalf("消息内容应被匿名化（anon: 前缀），实际 %q", m.Content)
		}
	}
	if msgs[0].IntentScore != 0.55 {
		t.Fatalf("消息数值列应保留，实际 %v", msgs[0].IntentScore)
	}

	// 4) 二次执行幂等：无错、内容不再二次哈希、状态仍 anonymized
	before := msgs[0].Content
	if err := ExecuteDeletion(row.ID); err != nil {
		t.Fatalf("二次执行应幂等无错: %v", err)
	}
	var m2 model.Message
	db.DB.First(&m2, msgIDs[0])
	if m2.Content != before {
		t.Fatalf("二次执行不应改变已匿名内容，%q -> %q", before, m2.Content)
	}
	var req model.DeletionRequest
	db.DB.First(&req, row.ID)
	if req.Status != model.DeletionStatusAnonymized || req.ProcessedAt == nil {
		t.Fatalf("执行后状态应 anonymized 且 processed_at 非空，实际 %s", req.Status)
	}
}

// TestProcessExpiredOnlyDue 覆盖 ProcessExpiredOnlyDue 相关行为与边界。
func TestProcessExpiredOnlyDue(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	// 一条已到期、一条未到期（Enqueue 对 grace<=0 归一化为默认 15d，故到期直接改 deadline 模拟）
	cuID1, _ := seedCustomerWithChat(t, tid)
	cuID2, _ := seedCustomerWithChat(t, tid)
	r1, _ := Enqueue(tid, model.DeletionScopeCustomer, cuID1, 0, time.Hour)
	db.DB.Model(&model.DeletionRequest{}).Where("id = ?", r1.ID).Update("deadline", time.Now().Add(-time.Minute))
	r2, _ := Enqueue(tid, model.DeletionScopeCustomer, cuID2, 0, 24*time.Hour)

	n := ProcessExpired()
	if n < 1 {
		t.Fatalf("至少应处理 1 条到期请求，实际 %d", n)
	}
	var s1, s2 model.DeletionRequest
	db.DB.First(&s1, r1.ID)
	db.DB.First(&s2, r2.ID)
	if s1.Status != model.DeletionStatusAnonymized {
		t.Fatalf("到期请求应被处理为 anonymized，实际 %s", s1.Status)
	}
	if s2.Status != model.DeletionStatusPending {
		t.Fatalf("未到期请求应仍 pending，实际 %s", s2.Status)
	}
}
