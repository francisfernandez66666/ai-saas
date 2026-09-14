// D9 行业包质量归因单测：快照、事件回填、聚合口径。
package attribution

import (
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// TestSnapshotFromBindingPrefersEnterprise 覆盖 SnapshotFromBindingPrefersEnterprise 相关行为与边界。
func TestSnapshotFromBindingPrefersEnterprise(t *testing.T) {
	entID := uint(7)
	bind := &model.TenantPackBinding{
		PackID: 1, PackCode: "auto", AppliedVersion: "1.0.0",
		EnterprisePackID: &entID, EnterpriseCode: "auto_rox", EnterpriseVersion: "1.1.0",
	}
	got := SnapshotFromBinding(bind)
	if got.PackCode != "auto_rox" || got.PackVersion != "1.1.0" {
		t.Fatalf("expected enterprise snapshot, got %+v", got)
	}
}

// TestReplyAttributionRecordMarkAndStats 覆盖 ReplyAttributionRecordMarkAndStats 相关行为与边界。
func TestReplyAttributionRecordMarkAndStats(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	entID := uint(8)
	if err := db.DB.Create(&model.TenantPackBinding{
		TenantID: tid, PackID: 1, PackCode: "auto", AppliedVersion: "1.0.0",
		EnterprisePackID: &entID, EnterpriseCode: "auto_rox", EnterpriseVersion: "1.1.0",
	}).Error; err != nil {
		t.Fatalf("create binding: %v", err)
	}
	convID := uint(time.Now().UnixNano() % 1000000000)
	custID := convID + 1
	aiMsg := model.Message{TenantID: tid, ConversationID: convID, CustomerID: custID, SenderType: "ai", Content: "AI 回复", TemplateID: "tpl_nothrow_001"}
	if err := db.DB.Create(&aiMsg).Error; err != nil {
		t.Fatalf("create ai msg: %v", err)
	}
	if err := RecordReply(RecordReplyInput{
		TenantID: tid, MessageID: aiMsg.ID, ConversationID: convID, CustomerID: custID,
		TemplateID: "tpl_nothrow_001", AnchorType: 0, RouteResult: "ai", IntentBefore: 0.2, IntentAfter: 0.45,
	}); err != nil {
		t.Fatalf("record reply: %v", err)
	}
	var row model.ReplyAttribution
	if err := db.DB.Where("tenant_id = ? AND message_id = ?", tid, aiMsg.ID).First(&row).Error; err != nil {
		t.Fatalf("read attribution: %v", err)
	}
	if row.PackCode != "auto_rox" || row.PackVersion != "1.1.0" {
		t.Fatalf("unexpected pack snapshot: %+v", row)
	}

	custMsg := model.Message{TenantID: tid, ConversationID: convID, CustomerID: custID, SenderType: "customer", Content: "多少钱"}
	if err := db.DB.Create(&custMsg).Error; err != nil {
		t.Fatalf("create customer msg: %v", err)
	}
	if err := MarkHookedBeforeMessage(tid, convID, custMsg.ID); err != nil {
		t.Fatalf("mark hooked: %v", err)
	}
	if err := MarkLeadCaptured(tid, convID, custID); err != nil {
		t.Fatalf("mark lead: %v", err)
	}
	if err := MarkPendingHuman(tid, convID, custID); err != nil {
		t.Fatalf("mark pending human: %v", err)
	}

	if err := db.DB.Where("id = ?", row.ID).First(&row).Error; err != nil {
		t.Fatalf("reload attribution: %v", err)
	}
	if !row.Hooked || !row.LeadCaptured || !row.PendingHuman {
		t.Fatalf("expected event flags to be set, got %+v", row)
	}

	qtid := tid
	stats, err := Stats(StatFilter{TenantID: &qtid, PackCode: "auto_rox", Days: 1})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected one stats row, got %+v", stats)
	}
	s := stats[0]
	if s.SampleCount != 1 || s.TemplateID != "tpl_nothrow_001" {
		t.Fatalf("unexpected stats sample: %+v", s)
	}
	if s.HookRate != 1 || s.LeadRate != 1 || s.PendingRate != 1 {
		t.Fatalf("unexpected rates: %+v", s)
	}
	if s.AvgIntentDelta < 0.24 || s.AvgIntentDelta > 0.26 {
		t.Fatalf("unexpected intent delta: %f", s.AvgIntentDelta)
	}
}

// TestSyncPackStatsMaterializesRows 覆盖 SyncPackStatsMaterializesRows 相关行为与边界。
func TestSyncPackStatsMaterializesRows(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	entID := uint(9)
	if err := db.DB.Create(&model.TenantPackBinding{
		TenantID: tid, PackID: 1, PackCode: "auto", AppliedVersion: "1.0.0",
		EnterprisePackID: &entID, EnterpriseCode: "auto_rox", EnterpriseVersion: "1.2.0",
	}).Error; err != nil {
		t.Fatalf("create binding: %v", err)
	}
	convID := uint(time.Now().UnixNano() % 1000000000)
	msg := model.Message{TenantID: tid, ConversationID: convID, CustomerID: convID + 1, SenderType: "ai", Content: "AI 回复", TemplateID: "tpl_nothrow_001"}
	if err := db.DB.Create(&msg).Error; err != nil {
		t.Fatalf("create ai msg: %v", err)
	}
	if err := RecordReply(RecordReplyInput{TenantID: tid, MessageID: msg.ID, ConversationID: convID, CustomerID: msg.CustomerID, TemplateID: msg.TemplateID, IntentBefore: 0.1, IntentAfter: 0.4}); err != nil {
		t.Fatalf("record reply: %v", err)
	}
	if err := SyncPackStats(); err != nil {
		t.Fatalf("sync pack stats: %v", err)
	}
	var snap model.PackStatSnapshot
	if err := db.DB.Where("tenant_id = ? AND pack_code = ? AND pack_version = ? AND template_id = ?", tid, "auto_rox", "1.2.0", "tpl_nothrow_001").First(&snap).Error; err != nil {
		t.Fatalf("read pack_stats: %v", err)
	}
	if snap.SampleCount != 1 || snap.AvgIntentDelta < 0.29 || snap.AvgIntentDelta > 0.31 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
}

// TestScoreAndAlertPackQuality 覆盖 ScoreAndAlertPackQuality 相关行为与边界。
func TestScoreAndAlertPackQuality(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	oldScoreFn, oldAlertFn := ReplyScoreFunc, PackAlertFunc
	ReplyScoreFunc = func(content string, anchors, forbidden []string) (int, []string) { return 40, []string{"low"} }
	var alertPack, alertMsg string
	PackAlertFunc = func(packCode, message string) { alertPack, alertMsg = packCode, message }
	defer func() {
		ReplyScoreFunc, PackAlertFunc = oldScoreFn, oldAlertFn
		packAlertMu.Lock()
		lastPackAlert = map[string]time.Time{}
		packAlertMu.Unlock()
	}()
	packAlertMu.Lock()
	lastPackAlert = map[string]time.Time{}
	packAlertMu.Unlock()

	base := uint(time.Now().UnixNano() % 1000000000)
	for i := 0; i < 3; i++ {
		msg := model.Message{TenantID: tid, ConversationID: base, CustomerID: base + 1, SenderType: "ai", Content: "这是一条用于离线评分的质量测试回复"}
		if err := db.DB.Create(&msg).Error; err != nil {
			t.Fatalf("create msg %d: %v", i, err)
		}
		if err := db.DB.Create(&model.ReplyAttribution{
			TenantID: tid, MessageID: msg.ID, ConversationID: base, CustomerID: base + 1,
			PackCode: "auto_rox", PackVersion: "1.3.0", TemplateID: "tpl_low", EvalScore: -1,
		}).Error; err != nil {
			t.Fatalf("create attribution %d: %v", i, err)
		}
	}
	if scored, err := ScoreReplyAttributions(10); err != nil || scored != 3 {
		t.Fatalf("expected 3 scored, got %d err=%v", scored, err)
	}
	alerts, err := CheckPackQualityAlerts(AlertFilter{Days: 1, MinSamples: 3, Threshold: 60, Consecutive: 3})
	if err != nil {
		t.Fatalf("check alerts: %v", err)
	}
	if len(alerts) != 1 || alertPack != "auto_rox" || alertMsg == "" {
		t.Fatalf("unexpected alerts=%v pack=%q msg=%q", alerts, alertPack, alertMsg)
	}
}
