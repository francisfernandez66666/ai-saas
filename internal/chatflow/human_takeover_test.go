// A1 人工接管态裁决唯一真相源回归（2026-09-22 全量修复批·批四）：
// 钉住"同一会话状态在正式链与免登录链得到同一个动作"——三态（继续 AI / 超时重开 AI 并落库 /
// 跳过 AI 并给出路由标记）在此逐分支断言，任一入口日后自行复制判定都会被本文件与
// api 侧的负向静态锁一起拦下。
package chatflow

import (
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/testutil"
)

// takeoverCfgForTest 注入静态热配置（超时 300s / 自动代答可切换），返回复原函数
func takeoverCfgForTest(t *testing.T, autoReply string) func() {
	t.Helper()
	old := runtimecfg.DefaultSystemConfigService
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(map[string]string{
		"assigned_lead_ai_timeout":    "300",
		"assigned_lead_ai_auto_reply": autoReply,
	}, nil)
	return func() { runtimecfg.DefaultSystemConfigService = old }
}

// mkLockedConv 建一条人工锁定会话（锁定态是裁决的公共前置）。
// 注意 GORM 坑：接管相关列带 `default:true/false` 标签，插入时**零值字段整列被省略**，
// 且 GORM 会把 DB 默认值回写进结构体（IsAiReplyEnabled=false 建完变 true）。
// 故建完必须用 map 显式覆写这几列，再按覆写后的值返回给用例。
func mkLockedConv(t *testing.T, tid uint, cid uint, mutate func(*model.Conversation)) model.Conversation {
	t.Helper()
	conv := model.Conversation{
		TenantID: tid, CustomerID: cid, Status: "active",
		Mode: "human", IsHumanLocked: true, IsAiReplyEnabled: true,
	}
	if mutate != nil {
		mutate(&conv)
	}
	// 先抓期望值：GORM Create 会把"被省略的默认值列"**回写进结构体**
	// （IsAiReplyEnabled=false 建完变 true），Create 之后再读 conv 就读到被污染的值了。
	wantMode, wantLocked := conv.Mode, conv.IsHumanLocked
	wantAI, wantPending, wantReplyAt := conv.IsAiReplyEnabled, conv.PendingHandoff, conv.LastHumanReplyAt
	if err := db.DB.Create(&conv).Error; err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	patch := map[string]interface{}{
		"mode":                wantMode,
		"is_human_locked":     wantLocked,
		"is_ai_reply_enabled": wantAI,
		"pending_handoff":     wantPending,
		"last_human_reply_at": wantReplyAt,
	}
	if err := db.DB.Model(&model.Conversation{}).Where("id = ?", conv.ID).Updates(patch).Error; err != nil {
		t.Fatalf("覆写接管列失败: %v", err)
	}
	conv.Mode, conv.IsHumanLocked = wantMode, wantLocked
	conv.IsAiReplyEnabled, conv.PendingHandoff, conv.LastHumanReplyAt = wantAI, wantPending, wantReplyAt
	t.Cleanup(func() { db.DB.Unscoped().Delete(&model.Conversation{}, conv.ID) })
	return conv
}

// TestHumanTakeoverReopensAIOnAdvisorTimeout 顾问超时未回：重开 AI 且**必须落库**
// （旧正式链只改内存，请求结束即丢 → 客户永久卡在"顾问正在赶来"，即 A1 的主缺陷）
func TestHumanTakeoverReopensAIOnAdvisorTimeout(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	defer takeoverCfgForTest(t, "true")()

	stale := time.Now().Add(-6 * time.Minute)
	conv := mkLockedConv(t, tid, 990001, func(c *model.Conversation) {
		c.IsAiReplyEnabled = false // 单人模式：AI 回复被关闭
		c.PendingHandoff = true
		c.LastHumanReplyAt = &stale
	})

	dec := HumanTakeoverDecide(&conv)
	if dec.Action != TakeoverAReply || !dec.Reopened {
		t.Fatalf("超时应判重开 AI，实际 %+v", dec)
	}
	var row model.Conversation
	if err := db.DB.First(&row, conv.ID).Error; err != nil {
		t.Fatalf("读回会话失败: %v", err)
	}
	if row.Mode != "ai" || row.IsHumanLocked || !row.IsAiReplyEnabled || row.PendingHandoff {
		t.Fatalf("重开 AI 必须四列落库，实际 mode=%s locked=%v ai=%v pending=%v",
			row.Mode, row.IsHumanLocked, row.IsAiReplyEnabled, row.PendingHandoff)
	}
	// 内存对象同步（调用方后续判定依赖它）
	if conv.Mode != "ai" || conv.IsHumanLocked {
		t.Fatalf("内存态须同步为已解锁，实际 mode=%s locked=%v", conv.Mode, conv.IsHumanLocked)
	}
}

// TestHumanTakeoverSkipRoutes 两类"本轮不该 AI 说话"的分支各返回自己的路由标记，且不动库
func TestHumanTakeoverSkipRoutes(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	defer takeoverCfgForTest(t, "true")()

	recent := time.Now().Add(-30 * time.Second)
	stale := time.Now().Add(-6 * time.Minute)

	cases := []struct {
		name      string
		cid       uint
		conv      func(*model.Conversation)
		wantRoute string
	}{
		{"单人模式·顾问未回过", 990002, func(c *model.Conversation) {
			c.IsAiReplyEnabled = false
		}, RouteHumanLockedNoAI},
		{"单人模式·顾问刚回", 990003, func(c *model.Conversation) {
			c.IsAiReplyEnabled = false
			c.PendingHandoff = true
			c.LastHumanReplyAt = &recent
		}, RouteHumanLockedNoAI},
		{"AI开着·顾问窗内已回", 990004, func(c *model.Conversation) {
			c.LastHumanReplyAt = &recent
		}, RouteHumanSkipAI},
	}
	for _, tc := range cases {
		conv := mkLockedConv(t, tid, tc.cid, tc.conv)
		dec := HumanTakeoverDecide(&conv)
		if dec.Action != TakeoverSkipAI || dec.Route != tc.wantRoute {
			t.Fatalf("%s: 期望 skip/%s，实际 %+v", tc.name, tc.wantRoute, dec)
		}
		var row model.Conversation
		db.DB.First(&row, conv.ID)
		if row.Mode != "human" || !row.IsHumanLocked {
			t.Fatalf("%s: 跳过 AI 不得改锁定态，实际 mode=%s locked=%v", tc.name, row.Mode, row.IsHumanLocked)
		}
	}
	// 超时但 AI 开着且非待接管：等顾问，不抢话
	conv := mkLockedConv(t, tid, 990005, func(c *model.Conversation) {
		c.IsAiReplyEnabled = false
		c.PendingHandoff = false // 无待接管标记 → 不触发自动重开
		c.LastHumanReplyAt = &stale
	})
	if dec := HumanTakeoverDecide(&conv); dec.Action != TakeoverSkipAI {
		t.Fatalf("非待接管的单人模式应继续等人，实际 %+v", dec)
	}
}

// TestHumanTakeoverProceedsWhenAdvisorAbsent 顾问从未回复：AI 自动代答（免登录链既有语义）
func TestHumanTakeoverProceedsWhenAdvisorAbsent(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	defer takeoverCfgForTest(t, "true")()

	conv := mkLockedConv(t, tid, 990006, nil) // 锁定 + AI 开着 + 无回复记录
	dec := HumanTakeoverDecide(&conv)
	if dec.Action != TakeoverAReply || dec.Reopened {
		t.Fatalf("顾问从未回复应 AI 代答且不写库，实际 %+v", dec)
	}
	var row model.Conversation
	db.DB.First(&row, conv.ID)
	if row.Mode != "human" || !row.IsHumanLocked {
		t.Fatalf("代答不改锁定态（人仍在线），实际 mode=%s locked=%v", row.Mode, row.IsHumanLocked)
	}

	// 自动代答关掉：顾问窗内回过也不该由 AI 抢答，但本函数只管"是否让 AI 说"，
	// 关闭代答时窗口语义退化为直接放行（与免登录链旧实现一致：条件含 aiAutoReply）
	defer takeoverCfgForTest(t, "false")()
	recent := time.Now().Add(-30 * time.Second)
	c2 := mkLockedConv(t, tid, 990007, func(c *model.Conversation) { c.LastHumanReplyAt = &recent })
	if dec := HumanTakeoverDecide(&c2); dec.Action != TakeoverAReply {
		t.Fatalf("ai_auto_reply=false 应放行 AI，实际 %+v", dec)
	}
}

// TestHumanTakeoverNonLockedProceeds 非锁定态一律放行，零写库
func TestHumanTakeoverNonLockedProceeds(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	defer takeoverCfgForTest(t, "true")()

	conv := model.Conversation{TenantID: tid, CustomerID: 990008, Status: "active", Mode: "ai"}
	if err := db.DB.Create(&conv).Error; err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	defer db.DB.Unscoped().Delete(&model.Conversation{}, conv.ID)
	dec := HumanTakeoverDecide(&conv)
	if dec.Action != TakeoverAReply || dec.Reopened || dec.Route != "" {
		t.Fatalf("AI 态应原样放行，实际 %+v", dec)
	}
	// 空指针/零 ID 会话不得 panic（fail-open 纪律）
	if d := HumanTakeoverDecide(nil); d.Action != TakeoverAReply {
		t.Fatalf("nil 会话应放行，实际 %+v", d)
	}
}

// TestHumanTakeoverSameStateSameVerdictAcrossEntries A1 的"两入口全等"锁：
// 同一份会话状态（锁定 + 待接管 + 顾问超时）分别喂给两条链拿到的句柄（模拟 web 正式链
// 与免登录 C 端链先后加载同一会话），裁决、落库列、幂等性必须逐项相同——
// 否则批五择臂拿到的 reply_attributions 样本就不可比。
func TestHumanTakeoverSameStateSameVerdictAcrossEntries(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)
	defer takeoverCfgForTest(t, "true")()

	stale := time.Now().Add(-10 * time.Minute)
	mk := func(cid uint) model.Conversation {
		return mkLockedConv(t, tid, cid, func(c *model.Conversation) {
			c.IsAiReplyEnabled = false
			c.PendingHandoff = true
			c.LastHumanReplyAt = &stale
		})
	}

	// 入口A=正式链视角，入口B=免登录链视角：状态相同则结果必须相同
	a, b := mk(990021), mk(990022)
	decA, decB := HumanTakeoverDecide(&a), HumanTakeoverDecide(&b)
	if decA != decB {
		t.Fatalf("同状态两入口裁决不等: %+v vs %+v", decA, decB)
	}
	if decA.Action != TakeoverAReply || !decA.Reopened {
		t.Fatalf("超时应判重开 AI，实际 %+v", decA)
	}

	readRow := func(id uint) model.Conversation {
		var row model.Conversation
		if err := db.DB.First(&row, id).Error; err != nil {
			t.Fatalf("读回会话 %d 失败: %v", id, err)
		}
		return row
	}
	ra, rb := readRow(a.ID), readRow(b.ID)
	if ra.Mode != rb.Mode || ra.IsHumanLocked != rb.IsHumanLocked ||
		ra.IsAiReplyEnabled != rb.IsAiReplyEnabled || ra.PendingHandoff != rb.PendingHandoff {
		t.Fatalf("两入口落库结果不等: %+v vs %+v", ra, rb)
	}
	if ra.Mode != "ai" || ra.IsHumanLocked || !ra.IsAiReplyEnabled || ra.PendingHandoff {
		t.Fatalf("重开 AI 须四列同时落库，实际 mode=%s locked=%v ai=%v pending=%v",
			ra.Mode, ra.IsHumanLocked, ra.IsAiReplyEnabled, ra.PendingHandoff)
	}

	// 幂等：重载已解除锁定的会话再裁决一次 → 仍放行 AI、Reopened=false、不重复写库
	again := readRow(a.ID)
	if d := HumanTakeoverDecide(&again); d.Action != TakeoverAReply || d.Reopened {
		t.Fatalf("解锁后二次裁决应纯放行且不重复落库，实际 %+v", d)
	}
	if second := readRow(a.ID); second.Mode != ra.Mode || second.IsHumanLocked != ra.IsHumanLocked {
		t.Fatalf("二次裁决不应改动接管列，实际 %+v", second)
	}
}
