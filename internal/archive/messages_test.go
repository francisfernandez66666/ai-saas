// 消息归档单测：CTE 搬运原子性 + 租户范围过滤 + 默认关闭零行为。
package archive

import (
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/testutil"
)

func TestRunMessagesArchiveScoped(t *testing.T) {
	testutil.SetupTestDB(t)
	runtimecfg.InitSystemConfigService()
	tid := testutil.CreateTenant(t)
	convID := uint(time.Now().UnixNano() % 1e9) // 随机化会话号避免并发测试互踩

	old := model.Message{TenantID: tid, ConversationID: convID, SenderType: "customer", Content: "老消息", CreatedAt: time.Now().AddDate(0, 0, -100)}
	fresh := model.Message{TenantID: tid, ConversationID: convID, SenderType: "customer", Content: "新消息", CreatedAt: time.Now().AddDate(0, 0, -1)}
	if err := db.DB.Create(&old).Error; err != nil {
		t.Fatalf("插入老消息失败: %v", err)
	}
	if err := db.DB.Create(&fresh).Error; err != nil {
		t.Fatalf("插入新消息失败: %v", err)
	}
	t.Cleanup(func() {
		db.DB.Unscoped().Where("tenant_id = ? AND conversation_id = ?", tid, convID).Delete(&model.Message{})
		db.DB.Exec("DELETE FROM messages_archive WHERE tenant_id = ? AND conversation_id = ?", tid, convID)
	})

	// 只圈本测试租户：30 天窗内只有 old 该被搬走（其他租户/更老存量不受波及）
	if moved := runMessagesArchive(30, []uint{tid}); moved != 1 {
		t.Fatalf("应归档 1 条，got %d", moved)
	}
	var hotOld, arcOld int64
	db.DB.Model(&model.Message{}).Where("id = ?", old.ID).Count(&hotOld)
	db.DB.Table("messages_archive").Where("id = ?", old.ID).Count(&arcOld)
	if hotOld != 0 || arcOld != 1 {
		t.Fatalf("搬运不原子：热表 %d 归档表 %d", hotOld, arcOld)
	}
	// 热数据原样留在 messages（含 created_at 内容字段）
	var freshBack model.Message
	if err := db.DB.First(&freshBack, fresh.ID).Error; err != nil || freshBack.Content != "新消息" {
		t.Fatalf("新消息被误搬或内容损坏: %v %+v", err, freshBack)
	}
	// 归档行带 archived_at 标记
	var marked int64
	db.DB.Table("messages_archive").Where("id = ? AND archived_at IS NOT NULL", old.ID).Count(&marked)
	if marked != 1 {
		t.Fatalf("归档行缺 archived_at")
	}
}

func TestRunMessagesOnceDefaultOff(t *testing.T) {
	testutil.SetupTestDB(t)
	runtimecfg.InitSystemConfigService()
	// message_archive_days 未配置（默认 0）：直接空转返回 0，不扫表
	if n := RunMessagesOnce(); n != 0 {
		t.Fatalf("默认关闭应返回 0，got %d", n)
	}
}
