// D5(2026-09-16) 合并队列"批次投递认领"回归锁定：通道并入合并队列后，
// 每批唯一回复对每通道必须恰好送达一次（ClaimReplyDelivery 是唯一的原子裁决点）。
package service

import (
	"testing"
)

func TestClaimReplyDeliveryExactlyOnce(t *testing.T) {
	svc := NewMessageQueueService()
	tid, cid, chID := uint(11), uint(9511), uint(77)

	// 1) 同 (epoch,channel) 首个认领成功，后续全部拒绝
	if !svc.ClaimReplyDelivery(tid, cid, 1, chID) {
		t.Fatal("首次认领应成功")
	}
	if svc.ClaimReplyDelivery(tid, cid, 1, chID) {
		t.Fatal("同批同通道重复认领应被拒（否则微信双发）")
	}

	// 2) 不同通道互不占用（同一客户双通道批次各投一次）
	if !svc.ClaimReplyDelivery(tid, cid, 1, chID+1) {
		t.Fatal("不同通道应可各自认领")
	}

	// 3) 不同批次代际独立（下一批可再次投递）
	if !svc.ClaimReplyDelivery(tid, cid, 2, chID) {
		t.Fatal("新批次应可重新认领")
	}
	if svc.ClaimReplyDelivery(tid, cid, 2, chID) {
		t.Fatal("新批次二次认领仍应被拒")
	}

	// 4) epoch==0（旧协议/降级路径无配对代际）：放行——宁可极端双发，不可静默漏发
	if !svc.ClaimReplyDelivery(tid, cid, 0, chID) {
		t.Fatal("epoch=0 应放行")
	}
	if !svc.ClaimReplyDelivery(tid, cid, 0, chID) {
		t.Fatal("epoch=0 恒放行")
	}

	// 5) 并发抢认领：N 路只 1 路赢（worker 与等待者竞态的原子性底线）
	svc2 := NewMessageQueueService()
	const n = 16
	wins := make(chan bool, n)
	for i := 0; i < n; i++ {
		go func() { wins <- svc2.ClaimReplyDelivery(12, 9512, 5, 88) }()
	}
	ok := 0
	for i := 0; i < n; i++ {
		if <-wins {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("并发认领应恰好 1 胜，实际 %d", ok)
	}
}
