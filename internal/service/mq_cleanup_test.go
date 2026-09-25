// 消息中心留存数据清理单测（G-15②，2026-09-24）：
// 钉住"死信不参与按天数清理"这条口径。
//
// 为什么值得单测：dead_letter 行是那条事件的**唯一副本**——Kafka 侧重试耗尽后 offset 已提交，
// 进程内总线（MQ_TYPE=log，默认形态）根本没有重投机制。清理器以前对台账一刀切（created_at
// 超过 30 天就删），等于把"我们丢过什么、为什么丢、载荷是什么"按保留期准时销毁：
// 事后既查不到，也重放不了。这里断言两件事——
//  1. 默认（mq_dead_letter_retention_days=0）：普通审计行删掉，死信行必须还在；
//  2. 显式配了天数：死信才允许被删（给人一个"确认处理完了可以清"的出口，而不是偷偷留）。
//
// 全程自建自清（用 ut_mqclean_ 前缀定位），不碰真实租户的审计数据。
package service

import (
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/mq"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/testutil"
)

// seedOldRecord 直插一条指定状态、指定年龄的台账行
func seedOldRecord(t *testing.T, eventID, status string, age time.Duration) {
	t.Helper()
	rec := model.MessageEventRecord{
		TenantID:  1, // 显式设值：清理器只按 created_at/status 定位，与归属无关
		OneID:     "ut_mqclean",
		EventID:   eventID,
		EventType: "lead_captured",
		Topic:     "ut",
		Payload:   `{"event_type":"lead_captured","data":{}}`,
		Status:    status,
		CreatedAt: time.Now().Add(-age),
	}
	if err := db.DB.Create(&rec).Error; err != nil {
		t.Fatalf("预置台账失败 status=%s: %v", status, err)
	}
}

// countByEvent 数台账行（0=已被清理）
func countByEvent(t *testing.T, eventID string) int64 {
	t.Helper()
	var n int64
	if err := db.DB.Model(&model.MessageEventRecord{}).Where("event_id = ?", eventID).Count(&n).Error; err != nil {
		t.Fatalf("计数失败: %v", err)
	}
	return n
}

// setRetentionCfg 写/删系统层保留天数配置并刷新内存缓存（用例结束自动复原）
func setRetentionCfg(t *testing.T, value string) {
	t.Helper()
	if runtimecfg.DefaultSystemConfigService == nil {
		runtimecfg.InitSystemConfigService()
	}
	db.DB.Where("tenant_id = 0 AND key = ?", "mq_dead_letter_retention_days").
		Delete(&model.SystemConfig{})
	if value != "" {
		cfg := model.SystemConfig{
			TenantID: 0, Category: "mq", Key: "mq_dead_letter_retention_days",
			Value: value, ValueType: "number", Description: "死信保留天数（单测写入）", DefaultValue: "0",
		}
		if err := db.DB.Create(&cfg).Error; err != nil {
			t.Fatalf("写入保留天数配置失败: %v", err)
		}
	}
	runtimecfg.DefaultSystemConfigService.Reload()
	t.Cleanup(func() {
		db.DB.Where("tenant_id = 0 AND key = ?", "mq_dead_letter_retention_days").Delete(&model.SystemConfig{})
		runtimecfg.DefaultSystemConfigService.Reload()
	})
}

// TestCleanupKeepsDeadLetters 默认配置下：陈旧普通行被删、陈旧死信行必须留下
func TestCleanupKeepsDeadLetters(t *testing.T) {
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	setRetentionCfg(t, "") // 恢复"永不删死信"的出厂语义（清掉可能存在的历史配置行）

	old := 40 * 24 * time.Hour
	sentEv, dlEv, consumedEv := "ut_mqclean_sent", "ut_mqclean_dl", "ut_mqclean_consumed"
	for _, e := range []string{sentEv, dlEv, consumedEv} {
		db.DB.Where("event_id = ?", e).Delete(&model.MessageEventRecord{})
	}
	seedOldRecord(t, sentEv, "sent", old)
	seedOldRecord(t, dlEv, mq.StatusDeadLetter, old)
	seedOldRecord(t, consumedEv, mq.StatusConsumed, old)
	t.Cleanup(func() {
		db.DB.Where("event_id IN ?", []string{sentEv, dlEv, consumedEv}).Delete(&model.MessageEventRecord{})
	})

	if n := countByEvent(t, sentEv); n != 1 {
		t.Fatalf("前置自检失败：陈旧 sent 行没插进去（n=%d），整段等式会在 0==0 上假绿", n)
	}

	CleanupMQTables()

	if n := countByEvent(t, sentEv); n != 0 {
		t.Errorf("陈旧 sent 审计行应被清理，实际残留 %d 行", n)
	}
	if n := countByEvent(t, consumedEv); n != 0 {
		t.Errorf("陈旧 consumed 审计行应被清理，实际残留 %d 行", n)
	}
	if n := countByEvent(t, dlEv); n != 1 {
		t.Errorf("死信行被按天数删掉了（残留 %d 行）——那条事件的唯一副本从此消失，无法追查也无法重放", n)
	}
}

// TestCleanupDeadLetterRetentionExplicit 显式配 retention 天数后死信才可清（逃生阀要真的能用）
func TestCleanupDeadLetterRetentionExplicit(t *testing.T) {
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	dlEv := "ut_mqclean_dl_expire"
	db.DB.Where("event_id = ?", dlEv).Delete(&model.MessageEventRecord{})
	seedOldRecord(t, dlEv, mq.StatusDeadLetter, 40*24*time.Hour)
	t.Cleanup(func() { db.DB.Where("event_id = ?", dlEv).Delete(&model.MessageEventRecord{}) })

	setRetentionCfg(t, "7") // 40 天前的死信 > 7 天保留期 → 这一轮应被清掉
	CleanupMQTables()

	if n := countByEvent(t, dlEv); n != 0 {
		t.Errorf("配了 mq_dead_letter_retention_days=7 却仍不清（残留 %d 行），逃生阀形同虚设", n)
	}
}

// TestCleanupDeadLetterRetentionKeepsYoungRows 保留期内（配 90 天）死信不得被删
func TestCleanupDeadLetterRetentionKeepsYoungRows(t *testing.T) {
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	dlEv := "ut_mqclean_dl_young"
	db.DB.Where("event_id = ?", dlEv).Delete(&model.MessageEventRecord{})
	seedOldRecord(t, dlEv, mq.StatusDeadLetter, 10*24*time.Hour)
	t.Cleanup(func() { db.DB.Where("event_id = ?", dlEv).Delete(&model.MessageEventRecord{}) })

	setRetentionCfg(t, "90")
	CleanupMQTables()

	if n := countByEvent(t, dlEv); n != 1 {
		t.Errorf("10 天龄死信在 90 天保留期内被删（残留 %d 行）：保留天数没进 DELETE 条件", n)
	}
}
