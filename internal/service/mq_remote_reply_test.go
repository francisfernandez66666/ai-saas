// 跨实例「远程等待者」消费判据单测（2026-09-28，G-TAKEOVER-STRAND）
//
// 缺陷现场（本机 .env 打开 REDIS_ENABLED=true 后由 smoke_channel §四c 确定性复现）：
// 客户连发 5 条、合并上限 3 条时，第 4、5 条经 waitRemotely 转交后落在 mq:pending 列表里
// 等下一批；而处理者的 SetReply **先发布回复、后释放锁**，于是这两个等待者下一次轮询
// 先撞上"有新回复"，无条件把**上一批**（不含自己那句）的回复取走并离场——
// 那两句话从此永久滞留在 Redis 列表里，客户连发的第 4 条之后一条都收不到回复（静默丢答）。
//
// 判据收成一个纯函数不是为了好看：它是这段路径上唯一可机器判定的分歧点
// （"有新回复" ≠ "有回复里有我这一句"），把它从轮询里摘出来才能逐格反证。
package service

import "testing"

// TestShouldConsumeRemoteReply 四格真值表：只有"有新回复且我这句已不在待合并列表"才消费。
// 每格都写明它对应现场的哪一幕，缺任何一格都可能把"丢答"和"挂死"换着方向复发。
func TestShouldConsumeRemoteReply(t *testing.T) {
	cases := []struct {
		name            string
		newReply        bool
		ownStillPending bool
		want            bool
		why             string
	}{
		{
			name:            "新回复_我已不在列表_消费",
			newReply:        true,
			ownStillPending: false,
			want:            true,
			why:             "我这句已被某一批收走并交卷——这条回复就是它的答案（正常跨实例等待）",
		},
		{
			name:            "新回复_我还挂在列表_不得消费",
			newReply:        true,
			ownStillPending: true,
			want:            false,
			why:             "G-TAKEOVER-STRAND 现场：批次满员被退回的消息撞上上一批回复，取走即永久丢答",
		},
		{
			name:            "无新回复_不得凭空返回",
			newReply:        false,
			ownStillPending: false,
			want:            false,
			why:             "还没人交卷（持有者仍在生成/我已入批待答），返回空回复等于把空白当答复",
		},
		{
			name:            "无新回复_我还挂在列表_继续等或接管",
			newReply:        false,
			ownStillPending: true,
			want:            false,
			why:             "持锁实例死亡的情形，由锁活性判定走接管，与消费判据无关",
		},
	}
	for _, c := range cases {
		got := shouldConsumeRemoteReply(c.newReply, c.ownStillPending)
		if got != c.want {
			t.Errorf("%s：shouldConsumeRemoteReply(%v,%v)=%v 期望 %v（%s）",
				c.name, c.newReply, c.ownStillPending, got, c.want, c.why)
		}
	}
}

// TestShouldConsumeRemoteReplyRejectsStaleReply 反向单点自证：
// 把判据"退化"回旧实现（newReply 为真就消费）必须在这条用例上红——
// 否则上面的真值表可能只是在描述一个永远不会被走到的分支。
func TestShouldConsumeRemoteReplyRejectsStaleReply(t *testing.T) {
	// 旧实现等价式：只看"有没有新回复"
	legacy := func(newReply, ownStillPending bool) bool { return newReply }
	if legacy(true, true) == shouldConsumeRemoteReply(true, true) {
		t.Fatal("判据与旧实现（无条件消费）在同输入下同结果＝本轮修复没有改变任何行为")
	}
	// 其余三格新旧必须一致：修复只收窄"不含我这一句"的那一格，不改别的语义
	for _, c := range [][2]bool{{true, false}, {false, false}, {false, true}} {
		if legacy(c[0], c[1]) != shouldConsumeRemoteReply(c[0], c[1]) {
			t.Errorf("newReply=%v ownStillPending=%v 两实现结果不同：修复超出了它声称的范围", c[0], c[1])
		}
	}
}
