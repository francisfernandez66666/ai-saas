// FIX-6 反证单测（2026-09-29 审计批二）：updateKfCursor 的失败必须"返回值可判"。
// 旧写法吞掉 Update 的 error——游标推进失败时下轮从旧位重拉虽是正确退化（靠 channel_inbound_msgs
// 幂等吸收），但"游标永远停滞"在界面上与"没有新消息"是同一个形状，只有客户报同步卡住才会发现。
// 三条用例各锁一个分支：① 行不存在（RowsAffected=0 不是成功）；② 真 DB 错误（超长值撞 varchar(256)
// → 22001）；③ 正常通道写成功且逐字可读回（防"改成恒报错"的伪修）。
package channel

import (
	"strings"
	"testing"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// TestUpdateKfCursorMissingChannel 通道行不存在时必须返回 error（静默 no-op 即吞错本体）
func TestUpdateKfCursorMissingChannel(t *testing.T) {
	testutil.SetupTestDB(t)
	if err := updateKfCursor(999999999, "cursor-for-dead-channel"); err == nil {
		t.Error("写不存在的通道游标必须回错——RowsAffected=0 不是成功，否则游标丢失不可见")
	}
}

// TestUpdateKfCursorRealDBError 超长游标撞列宽（PG 22001）必须原样回错，不落进静默分支
func TestUpdateKfCursorRealDBError(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	ch := &model.Channel{TenantID: tid, Type: model.ChannelTypeWecomKf, Name: "fix6-长游标用例"}
	if err := db.DB.Create(&ch).Error; err != nil {
		t.Fatalf("建测试通道失败: %v", err)
	}
	// 行必须在（软删会让 Update 匹配 0 行走①的分支，就测不到"真 DB 错误原样上抛"这条腿了）
	if err := updateKfCursor(ch.ID, strings.Repeat("c", 300)); err == nil {
		t.Error("300 字符游标写 varchar(256) 列必被 PG 拒绝，回 nil 即吞错未除根")
	}
}

// TestUpdateKfCursorSuccessRoundTrip 正常路径：写成功回 nil 且 kf_cursor 逐字可读回
func TestUpdateKfCursorSuccessRoundTrip(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	ch := &model.Channel{TenantID: tid, Type: model.ChannelTypeWecomKf, Name: "fix6-游标回环"}
	if err := db.DB.Create(&ch).Error; err != nil {
		t.Fatalf("建测试通道失败: %v", err)
	}
	const cur = "next-cursor-abc123"
	if err := updateKfCursor(ch.ID, cur); err != nil {
		t.Fatalf("正常通道游标推进应回 nil, got %v", err)
	}
	var got model.Channel
	if err := db.DB.Where("id = ?", ch.ID).First(&got).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if got.KfCursor != cur {
		t.Errorf("游标应逐字回环 %q, got %q", cur, got.KfCursor)
	}
}
