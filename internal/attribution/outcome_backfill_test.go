// 终局标签回填单测（批五 A，2026-09-23）：阶段推进回填、幂等、观察窗口语义、SyncPackStats 终局率聚合。
package attribution

import (
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// makeOutcomeFixture 建一个「租户+客户+一条已归因回复」的最小场景；ageDays>0 时把归因行
// created_at 拨回过去（模拟窗口已过的历史样本）。返回 (tid, customerID, attributionID)。
func makeOutcomeFixture(t *testing.T, stage string, ageDays int) (uint, uint, uint) {
	t.Helper()
	tid := testutil.CreateTenant(t)
	t.Cleanup(func() { testutil.CleanupTenant(t, tid) })

	cust := model.Customer{TenantID: tid, Name: "终局测试客户", JourneyStage: stage}
	if err := db.DB.Create(&cust).Error; err != nil {
		t.Fatalf("create customer: %v", err)
	}
	created := time.Now()
	convID := uint(created.UnixNano() % 1000000000)
	msg := model.Message{TenantID: tid, ConversationID: convID, CustomerID: cust.ID, SenderType: "ai", Content: "约到店回复", TemplateID: "tpl_outcome"}
	if err := db.DB.Create(&msg).Error; err != nil {
		t.Fatalf("create msg: %v", err)
	}
	row := model.ReplyAttribution{
		TenantID: tid, MessageID: msg.ID, ConversationID: convID, CustomerID: cust.ID,
		PackCode: "auto", PackVersion: "1.0.0", TemplateID: "tpl_outcome",
		CreatedAt: created.AddDate(0, 0, -ageDays), UpdatedAt: created.AddDate(0, 0, -ageDays),
	}
	if err := db.DB.Create(&row).Error; err != nil {
		t.Fatalf("create attribution: %v", err)
	}
	return tid, cust.ID, row.ID
}

// loadAttribution 读取归因行。
func loadAttribution(t *testing.T, id uint) model.ReplyAttribution {
	t.Helper()
	var row model.ReplyAttribution
	if err := db.DB.Where("id = ?", id).First(&row).Error; err != nil {
		t.Fatalf("load attribution %d: %v", id, err)
	}
	return row
}

// TestBackfillOutcomesArrivedAndIdempotent ①客户阶段推进到 arrived → 历史归因行回填 arrived_at、
// outcome_stage 快照正确；②二次回填不改已确立值（含客户阶段回退也不清空已写时间——
// 到店是既成事实，正向标签只进不退，回退多半是 CRM 误编辑）。
func TestBackfillOutcomesArrivedAndIdempotent(t *testing.T) {
	testutil.SetupTestDB(t)
	tid, custID, attrID := makeOutcomeFixture(t, "lead_captured", 40)

	// 阶段推进到已到店（模拟顾问在 CRM 里改阶段）
	if err := db.DB.Model(&model.Customer{}).Where("tenant_id = ? AND id = ?", tid, custID).
		Update("journey_stage", "arrived").Error; err != nil {
		t.Fatalf("advance stage: %v", err)
	}
	if n, err := BackfillOutcomes(100, tid); err != nil || n == 0 {
		t.Fatalf("expected backfill rows, got n=%d err=%v", n, err)
	}
	row := loadAttribution(t, attrID)
	if row.ArrivedAt == nil {
		t.Fatalf("arrived_at should be backfilled: %+v", row)
	}
	if row.OutcomeStage != "arrived" {
		t.Fatalf("outcome_stage snapshot = %q, want arrived", row.OutcomeStage)
	}
	if row.DealtAt != nil {
		t.Fatalf("dealt_at must stay nil (not ordered): %+v", row)
	}
	// 窗口已过（40 天前）且未成交 → 本轮终判
	if row.OutcomeCheckedAt == nil {
		t.Fatalf("window elapsed row should be finalized: %+v", row)
	}
	firstArrived := *row.ArrivedAt
	firstChecked := *row.OutcomeCheckedAt

	// 幂等：再跑一轮，已终判行出队，值不变、updated=0
	if n, err := BackfillOutcomes(100, tid); err != nil || n != 0 {
		t.Fatalf("second sweep should be a no-op, got n=%d err=%v", n, err)
	}
	// 阶段回退也不清空已写时间（取舍：正向既成事实只进不退）
	if err := db.DB.Model(&model.Customer{}).Where("tenant_id = ? AND id = ?", tid, custID).
		Update("journey_stage", "lead_captured").Error; err != nil {
		t.Fatalf("rollback stage: %v", err)
	}
	if _, err := BackfillOutcomes(100, tid); err != nil {
		t.Fatalf("third sweep: %v", err)
	}
	row = loadAttribution(t, attrID)
	if row.ArrivedAt == nil || !row.ArrivedAt.Equal(firstArrived) {
		t.Fatalf("arrived_at must survive stage rollback: %+v", row)
	}
	if row.OutcomeCheckedAt == nil || !row.OutcomeCheckedAt.Equal(firstChecked) {
		t.Fatalf("outcome_checked_at must not be rewritten: %+v", row)
	}
}

// TestBackfillOutcomesWindowSemantics ③窗口内的行不被终判：
// 未到店 → arrived_at 与 outcome_checked_at 都留 NULL（留待下轮巡检）；
// 已到店但未成交 → 正向补标立刻写（事实无需等窗口），但 checked 仍 NULL，窗口内还可能补上 deal。
func TestBackfillOutcomesWindowSemantics(t *testing.T) {
	testutil.SetupTestDB(t)

	// 窗口内、未到店
	tid1, _, attr1 := makeOutcomeFixture(t, "lead_captured", 2)
	// 窗口内、已到店
	tid2, _, attr2 := makeOutcomeFixture(t, "arrived", 2)
	if _, err := BackfillOutcomes(100, tid1); err != nil {
		t.Fatalf("backfill tid1: %v", err)
	}
	if _, err := BackfillOutcomes(100, tid2); err != nil {
		t.Fatalf("backfill tid2: %v", err)
	}
	row1 := loadAttribution(t, attr1)
	if row1.ArrivedAt != nil || row1.OutcomeCheckedAt != nil {
		t.Fatalf("in-window not-arrived row must stay pending: %+v", row1)
	}
	row2 := loadAttribution(t, attr2)
	if row2.ArrivedAt == nil {
		t.Fatalf("in-window arrived row should get positive label now: %+v", row2)
	}
	if row2.OutcomeCheckedAt != nil {
		t.Fatalf("in-window row must NOT be finalized even if arrived: %+v", row2)
	}

	// 成交即时定局：窗口内但阶段已到 ordered → dealt_at 写入且允许终判（完全定局不再占队列）
	tid3, cust3, attr3 := makeOutcomeFixture(t, "lead_captured", 2)
	if err := db.DB.Model(&model.Customer{}).Where("tenant_id = ? AND id = ?", tid3, cust3).
		Update("journey_stage", "ordered").Error; err != nil {
		t.Fatalf("advance to ordered: %v", err)
	}
	if _, err := BackfillOutcomes(100, tid3); err != nil {
		t.Fatalf("backfill tid3: %v", err)
	}
	row3 := loadAttribution(t, attr3)
	if row3.DealtAt == nil || row3.ArrivedAt == nil || row3.OutcomeCheckedAt == nil {
		t.Fatalf("dealt row should be fully labeled and finalized: %+v", row3)
	}
}

// TestBackfillOutcomesExpiredWithoutOutcome ④未到店未成交且窗口已过 → 终判，arrived_at 保持 NULL。
func TestBackfillOutcomesExpiredWithoutOutcome(t *testing.T) {
	testutil.SetupTestDB(t)
	tid, _, attrID := makeOutcomeFixture(t, "lead_captured", 45)
	if _, err := BackfillOutcomes(100, tid); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	row := loadAttribution(t, attrID)
	if row.OutcomeCheckedAt == nil {
		t.Fatalf("expired window row must be finalized: %+v", row)
	}
	if row.ArrivedAt != nil || row.DealtAt != nil {
		t.Fatalf("no-outcome verdict must keep NULLs (NULL=结论是没有，不写哨兵值): %+v", row)
	}
}

// TestSyncPackStatsOutcomeRates ⑤SyncPackStats 聚合终局率：4 行样本 1 行到店 → arrive_rate=0.25、
// deal_rate=0；Stats 视图与 pack_stats 快照口径一致。
func TestSyncPackStatsOutcomeRates(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	custArrived := model.Customer{TenantID: tid, Name: "到店客户", JourneyStage: "arrived"}
	custPlain := model.Customer{TenantID: tid, Name: "普通客户", JourneyStage: "lead_captured"}
	for _, c := range []*model.Customer{&custArrived, &custPlain} {
		if err := db.DB.Create(c).Error; err != nil {
			t.Fatalf("create customer: %v", err)
		}
	}
	old := time.Now().AddDate(0, 0, -40) // 全部拨出窗口，保证本轮回填即终判
	base := uint(old.UnixNano() % 1000000000)
	rows := []model.ReplyAttribution{
		{TenantID: tid, MessageID: base + 1, ConversationID: base, CustomerID: custArrived.ID, PackCode: "auto", PackVersion: "9.9.0", TemplateID: "tpl_rate", CreatedAt: old, UpdatedAt: old},
		{TenantID: tid, MessageID: base + 2, ConversationID: base, CustomerID: custPlain.ID, PackCode: "auto", PackVersion: "9.9.0", TemplateID: "tpl_rate", CreatedAt: old, UpdatedAt: old},
		{TenantID: tid, MessageID: base + 3, ConversationID: base, CustomerID: custPlain.ID, PackCode: "auto", PackVersion: "9.9.0", TemplateID: "tpl_rate", CreatedAt: old, UpdatedAt: old},
		{TenantID: tid, MessageID: base + 4, ConversationID: base, CustomerID: custPlain.ID, PackCode: "auto", PackVersion: "9.9.0", TemplateID: "tpl_rate", CreatedAt: old, UpdatedAt: old},
	}
	for i := range rows {
		if err := db.DB.Create(&rows[i]).Error; err != nil {
			t.Fatalf("create attribution %d: %v", i, err)
		}
	}
	if _, err := BackfillOutcomes(100, tid); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if err := SyncPackStats(); err != nil {
		t.Fatalf("sync pack stats: %v", err)
	}
	var snap model.PackStatSnapshot
	if err := db.DB.Where("tenant_id = ? AND pack_version = ? AND template_id = ?", tid, "9.9.0", "tpl_rate").First(&snap).Error; err != nil {
		t.Fatalf("read pack_stats: %v", err)
	}
	if snap.SampleCount != 4 {
		t.Fatalf("sample_count = %d, want 4", snap.SampleCount)
	}
	if snap.ArriveRate < 0.2499 || snap.ArriveRate > 0.2501 {
		t.Fatalf("arrive_rate = %v, want 0.25", snap.ArriveRate)
	}
	if snap.DealRate != 0 {
		t.Fatalf("deal_rate = %v, want 0", snap.DealRate)
	}
	// 视图口径（Stats 聚合）与快照一致
	qtid := tid
	stats, err := Stats(StatFilter{TenantID: &qtid, PackVersion: "9.9.0"})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if len(stats) != 1 || stats[0].ArriveRate < 0.2499 || stats[0].ArriveRate > 0.2501 {
		t.Fatalf("stats view arrive_rate mismatch: %+v", stats)
	}
}
