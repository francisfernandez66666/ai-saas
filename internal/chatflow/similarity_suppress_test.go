// A2 相似消息抑制跨链共用回归（2026-09-23 收尾批）：
// 本段逻辑此前只内联在 web 正式链，免登录 C 端链完全缺失 —— 同一客户连发两句近似话，
// 正式链合并一次回答、C 端链各答一遍。抽成 chatflow.FindSimilarInflightMessage 后，
// 这里把三条纪律钉死：①无在途批次绝不抑制（P2-1 丢答护栏）②只看 customer 发的消息
// ③租户+客户双条件过滤，跨租户/跨客户绝不互相抑制。
package chatflow

import (
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/testutil"
)

// lookbackCfgForTest 注入静态热配置（合并窗秒数），返回复原函数
func lookbackCfgForTest(system map[string]string, tenants map[uint]map[string]string) func() {
	old := runtimecfg.DefaultSystemConfigService
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(system, tenants)
	return func() { runtimecfg.DefaultSystemConfigService = old }
}

// mkCustomer 建一个显式盖章的测试客户（D6：db.DB.Create 必须字面量带 TenantID）
func mkCustomer(t *testing.T, tid uint, name string) uint {
	t.Helper()
	c := model.Customer{TenantID: tid, Name: name}
	if err := db.DB.Create(&c).Error; err != nil {
		t.Fatalf("建客户失败: %v", err)
	}
	t.Cleanup(func() { db.DB.Unscoped().Delete(&model.Customer{}, c.ID) })
	return c.ID
}

// mkCustMsg 建一条客户消息（senderType 可传 ai/human 以断言"非客户消息不参与抑制"）
func mkCustMsg(t *testing.T, tid, cid uint, senderType, content string) uint {
	t.Helper()
	m := model.Message{
		TenantID: tid, CustomerID: cid, ConversationID: cid, // 会话号复用客户号占位：本函数不读会话列
		SenderType: senderType, Content: content,
	}
	if err := db.DB.Create(&m).Error; err != nil {
		t.Fatalf("建消息失败: %v", err)
	}
	t.Cleanup(func() { db.DB.Unscoped().Delete(&model.Message{}, m.ID) })
	return m.ID
}

// TestMergeSuppressLookbackClamp 回看窗 = 合并窗×2，钳到 [2min, 10min]
// （窗口直接决定"多久前的相似句还会被吞"，放开上限会让历史消息永久压制新话题）
func TestMergeSuppressLookbackClamp(t *testing.T) {
	defer lookbackCfgForTest(map[string]string{"merge_window_seconds": "120"}, nil)()
	if got := MergeSuppressLookback(1); got != 4*time.Minute {
		t.Fatalf("合并窗120s 期望回看4min，实际 %v", got)
	}
	defer lookbackCfgForTest(map[string]string{"merge_window_seconds": "10"}, nil)()
	if got := MergeSuppressLookback(1); got != 2*time.Minute {
		t.Fatalf("超小合并窗期望钳到下界2min，实际 %v", got)
	}
	defer lookbackCfgForTest(map[string]string{"merge_window_seconds": "3600"}, nil)()
	if got := MergeSuppressLookback(1); got != 10*time.Minute {
		t.Fatalf("超大合并窗期望钳到上界10min，实际 %v", got)
	}
	// 租户覆盖优先于系统层（批六·补读写层对齐口径：缺覆盖时回落系统值）
	tid := uint(7777)
	defer lookbackCfgForTest(
		map[string]string{"merge_window_seconds": "30"},
		map[uint]map[string]string{tid: {"merge_window_seconds": "120"}},
	)()
	if got := MergeSuppressLookback(tid); got != 4*time.Minute {
		t.Fatalf("租户覆盖未生效：期望4min，实际 %v", got)
	}
}

// TestSimilarSuppressRequiresInflight P2-1 护栏：相似但无在途批次 → 绝不抑制
func TestSimilarSuppressRequiresInflight(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	cid := mkCustomer(t, tid, "A2抑制客户")

	const past = "极石01这款车空间大不大"
	const current = "极石01这款车空间大吗"
	mkCustMsg(t, tid, cid, "customer", past)

	if _, hit := FindSimilarInflightMessage(db.DB, tid, cid, current, false, 0); hit {
		t.Fatal("inflight=false 仍判抑制 —— 历史批次早已回完，这句没人接就是静默丢答（P2-1 回归）")
	}
	hitInfo, hit := FindSimilarInflightMessage(db.DB, tid, cid, current, true, 0)
	if !hit {
		t.Fatal("inflight=true 且高重叠应抑制，实际未命中")
	}
	if hitInfo.OverlapRate <= SimilarMergeThreshold {
		t.Fatalf("重叠度应严格大于阈值 %.2f，实际 %.4f", SimilarMergeThreshold, hitInfo.OverlapRate)
	}
	if hitInfo.PastContent != past {
		t.Fatalf("命中留痕应指向历史原文，实际 %q", hitInfo.PastContent)
	}
}

// TestSimilarSuppressNegativeCases 不该抑制的四种情形逐一封堵
func TestSimilarSuppressNegativeCases(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	cid := mkCustomer(t, tid, "A2负向客户")
	otherCid := mkCustomer(t, tid, "A2同租户他客")
	// 必须用 CreateTenantCode 另起语义码：CreateTenant 按 code 复用，同进程内两次调用返回**同一个**租户，
	// 直接拿它当"另一个租户"会让本断言在前置上就假（实测第一次跑即报"跨租户抑制"，实为同租户命中）。
	otherTid := testutil.CreateTenantCode(t, "unit_test_tenant_b")
	defer testutil.CleanupTenant(t, otherTid)
	if otherTid == tid {
		t.Fatalf("两个租户竟然是同一个（%d），跨租户断言失去意义", otherTid)
	}

	const past = "极石01这款车空间大不大"
	mkCustMsg(t, tid, cid, "customer", past)

	// ① 换个客户问同一句：不得被别人的历史句吞掉
	if _, hit := FindSimilarInflightMessage(db.DB, tid, otherCid, past, true, 0); hit {
		t.Fatal("跨客户互相抑制 —— customer_id 谓词丢失")
	}
	// ② 换个租户的同内容消息：同上，租户谓词不得丢
	//    （用 cid 这个"属于 tid 的客户号"配 otherTid 查询，等价于跨租户读到他人历史句）
	if _, hit := FindSimilarInflightMessage(db.DB, otherTid, cid, past, true, 0); hit {
		t.Fatal("跨租户互相抑制 —— tenant_id 谓词丢失")
	}
	// ③ 历史句来自 AI 自己：AI 复述同一件事不算客户重复提问
	mkCustMsg(t, tid, otherCid, "ai", past)
	if _, hit := FindSimilarInflightMessage(db.DB, tid, otherCid, past, true, 0); hit {
		t.Fatal("sender_type=ai 的历史消息参与了抑制判定")
	}
	// ④ 不相关新话题：重叠度为 0，必须正常入队
	if _, hit := FindSimilarInflightMessage(db.DB, tid, cid, "你们售后保养一次多少钱", true, 0); hit {
		t.Fatal("无关话题被误抑制")
	}
}

// TestSimilarSuppressWindowAndSelfExclusion 时间窗与"先存后判"自排除
func TestSimilarSuppressWindowAndSelfExclusion(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	cid := mkCustomer(t, tid, "A2窗口客户")

	const past = "极石01这款车空间大不大"
	const current = "极石01这款车空间大吗"

	// 回看窗外（30min 前，上界仅 10min）：不参与判定
	old := mkCustMsg(t, tid, cid, "customer", past)
	if err := db.DB.Model(&model.Message{}).Where("id = ?", old).
		Update("created_at", time.Now().Add(-30*time.Minute)).Error; err != nil {
		t.Fatalf("回拨消息时间失败: %v", err)
	}
	if _, hit := FindSimilarInflightMessage(db.DB, tid, cid, current, true, 0); hit {
		t.Fatal("回看窗外的历史句仍参与抑制 —— 窗口未生效，历史相似句会永久压制新话题")
	}

	// 移入窗口内即可命中
	if err := db.DB.Model(&model.Message{}).Where("id = ?", old).
		Update("created_at", time.Now().Add(-30*time.Second)).Error; err != nil {
		t.Fatalf("前拨消息时间失败: %v", err)
	}
	if _, hit := FindSimilarInflightMessage(db.DB, tid, cid, current, true, 0); !hit {
		t.Fatal("窗口内相似句未命中")
	}

	// C 端链是"先入库再判定"：本条消息自己就是最近一条，必须用 excludeMsgID 排掉，
	// 否则它与自身重叠度=100%，任何首条消息都会被"自己和自己合并"直接吞掉。
	self := mkCustMsg(t, tid, cid, "customer", current)
	if _, hit := FindSimilarInflightMessage(db.DB, tid, cid, current, true, self); !hit {
		t.Fatal("排除自身后应仍命中窗口内的历史句")
	}
	solo := mkCustomer(t, tid, "A2孤句客户")
	soloMsg := mkCustMsg(t, tid, solo, "customer", current)
	if _, hit := FindSimilarInflightMessage(db.DB, tid, solo, current, true, soloMsg); hit {
		t.Fatal("唯一一条消息被自身抑制 —— 首条消息会被直接丢答")
	}
}

// TestSimilarSuppressNoKeywords 无关键词（纯停用词/标点）不参与判定，避免除零
func TestSimilarSuppressNoKeywords(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	cid := mkCustomer(t, tid, "A2空词客户")

	if _, hit := FindSimilarInflightMessage(db.DB, tid, cid, "的了是在", true, 0); hit {
		t.Fatal("纯停用词不应命中")
	}
	// customerID=0（匿名未识别）一律不抑制：无主消息不该影响任何人
	if _, hit := FindSimilarInflightMessage(db.DB, tid, 0, "极石01空间大吗", true, 0); hit {
		t.Fatal("customerID=0 不应参与抑制判定")
	}
}
