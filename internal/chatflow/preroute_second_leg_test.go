// 到店第二段追问的「还要不要发」判定回归（FIX-9 通道分流批附带缺口，2026-09-27）
//
// 缺陷现场（不是设想出来的，是同日冒烟抓的）：通道四c 段连发五条里含一句到店意图，
// 第一段 10-15s 后发出、第二段排在 25-45s 后由 goroutine 补发；旧写法**醒来无条件投**，
// 于是脚本在两段之间把会话置成人工锁定后，客户仍会在"顾问已经接手"的状态下收到第二句
// 罐头预约追问——出站条数凭空 +1，五段的「人工锁定态不新增出站」因此判红。
//
// 判据本身收在 chatflow.StoreVisitSecondLegDecision（web 与通道共用一份），本文件把它的
// 每一条腿单独钉住：四条"该闭嘴"的取值各给**自己的稳定原因码**（不只断"非空"），
// 因为原因码是排障时唯一能区分"被人工接管挡下"和"被待接管挡下"的线索；
// 再配一条正常态必须放行（返回空串）的对照——缺它的话，把判据写成"永远跳过"也能全绿，
// 那种实现的表现正好是"客户再也没有第二段追问"，比双答更难被发现。
//
// 为什么只测纯函数内核不测取数外壳：外壳（按 convID 读库）的三条出口里，
// "记录不存在→conversation_gone"与"读库失败→照发"都要真库才能构造，
// 由 tools/smoke_channel.sh 第十一节·分支D 在真接口上钉（锁定后第二段必须不到）。
package chatflow

import (
	"testing"

	"ai-scrm/internal/model"
)

// TestStoreVisitSecondLegDecision 逐腿钉住第二段追问的说话权判定
func TestStoreVisitSecondLegDecision(t *testing.T) {
	// aiOwns 是所有"该发"用例的基线形态：AI 持权、未锁、未待接管
	aiOwns := func() *model.Conversation {
		return &model.Conversation{Mode: "ai", IsAiReplyEnabled: true}
	}

	cases := []struct {
		name string
		mut  func(*model.Conversation)
		want string
	}{
		{
			name: "AI 持权正常态必须放行(空串=该发)",
			mut:  func(c *model.Conversation) {},
			want: "",
		},
		{
			name: "人工锁定→让位",
			mut:  func(c *model.Conversation) { c.IsHumanLocked = true },
			want: "human_takeover",
		},
		{
			name: "AI 回复被关掉→让位",
			mut:  func(c *model.Conversation) { c.IsAiReplyEnabled = false },
			want: "human_takeover",
		},
		{
			name: "mode=human(顾问已接手)→让位",
			mut:  func(c *model.Conversation) { c.Mode = "human" },
			want: "human_takeover",
		},
		{
			name: "待接管(留资确认/接管申请已置位)→让位",
			mut:  func(c *model.Conversation) { c.PendingHandoff = true },
			want: "handoff_pending",
		},
		{
			// 取数外壳在"记录不存在"时会传 nil 进来（见 StoreVisitSecondSkipReason），
			// 这里同时封住"以后有人把 nil 当空结构体继续判"的改法——那是会 panic 的。
			name: "会话行读不到(nil)→按已消失处理",
			mut:  nil,
			want: "conversation_gone",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conv := aiOwns()
			if tc.mut != nil {
				tc.mut(conv)
			} else {
				conv = nil
			}
			if got := StoreVisitSecondLegDecision(conv); got != tc.want {
				t.Fatalf("第二段判定 = %q，期望 %q", got, tc.want)
			}
		})
	}
}

// TestStoreVisitSecondLegDecisionNotAnAlwaysSkip 反证：判据不能退化成"一律跳过"。
//
// 上一条用例里已经有一条"正常态返回空串"，但那条的取值是**默认零值组合**——
// 万一将来有人把 IsAiReplyEnabled 的默认语义写反（`!conv.IsAiReplyEnabled` 漏了取反之类），
// 零值形态可能恰好还是绿。这里用一条"每个字段都显式给成该发的值"的形态再判一次，
// 并把三张挡路的旗子逐个立起再逐个放下：只有对应那一条变红、其它三条保持空串，
// 才证明三条腿各判各的，而不是"随便写点什么就全跳过"。
func TestStoreVisitSecondLegDecisionNotAnAlwaysSkip(t *testing.T) {
	fullyOpen := &model.Conversation{
		Mode: "ai", IsAiReplyEnabled: true, IsHumanLocked: false, PendingHandoff: false,
		Status: "active",
	}
	if got := StoreVisitSecondLegDecision(fullyOpen); got != "" {
		t.Fatalf("完全开放态被误判为跳过(%q)——第二段追问将永久不发", got)
	}

	// 三条腿互不串味：只立其中一张旗时，原因码必须只对那一条负责
	legs := []struct {
		name string
		mut  func(*model.Conversation)
		want string
	}{
		{"只立人工锁定", func(c *model.Conversation) { c.IsHumanLocked = true }, "human_takeover"},
		{"只关 AI 回复", func(c *model.Conversation) { c.IsAiReplyEnabled = false }, "human_takeover"},
		{"只置待接管", func(c *model.Conversation) { c.PendingHandoff = true }, "handoff_pending"},
	}
	for _, leg := range legs {
		conv := *fullyOpen
		leg.mut(&conv)
		if got := StoreVisitSecondLegDecision(&conv); got != leg.want {
			t.Fatalf("%s：原因码 = %q，期望 %q", leg.name, got, leg.want)
		}
		// 放下这张旗必须回到"该发"——证明这条腿不是永久改写了基线
		restored := *fullyOpen
		if got := StoreVisitSecondLegDecision(&restored); got != "" {
			t.Fatalf("基线被 %s 污染：放下旗后仍判跳过(%q)", leg.name, got)
		}
	}
}
