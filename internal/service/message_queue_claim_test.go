// D5(2026-09-16) 合并队列"批次投递认领"回归锁定：通道并入合并队列后，
// 每批唯一回复对每通道必须恰好送达一次（ClaimReplyDelivery 是唯一的原子裁决点）。
package service

import (
	"errors"
	"testing"
	"time"

	"ai-scrm/internal/redisclient"
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

// P2-2(2026-09-19 批三)：Redis 三态必须区分裁决——旧实现把"Redis 故障"当成
// "他人已认领"返回 false，静默漏发（宁双发不漏发契约被反着执行）。
func TestClaimReplyDeliveryRedisFaultDegradesToLocal(t *testing.T) {
	oldEnabled, oldClaim := claimRedisEnabled, tryDeliveryClaim
	defer func() { claimRedisEnabled, tryDeliveryClaim = oldEnabled, oldClaim }()
	claimRedisEnabled = func() bool { return true }

	// 1) 拿锁成功：首个投；锁被他人持有（nil,nil）不投——跨实例互斥语义不变
	tryDeliveryClaim = func(string, time.Duration) (*redisclient.LockHandle, error) {
		return &redisclient.LockHandle{}, nil
	}
	svc := NewMessageQueueService()
	if !svc.ClaimReplyDelivery(21, 9521, 1, 7) {
		t.Fatal("Redis 正常拿锁应放行投递")
	}
	tryDeliveryClaim = func(string, time.Duration) (*redisclient.LockHandle, error) {
		return nil, nil // Redis 正常但锁被他人持有
	}
	if svc2 := NewMessageQueueService(); svc2.ClaimReplyDelivery(21, 9521, 1, 7) {
		t.Fatal("锁被他人持有应不投递（跨实例互斥语义不变）")
	}

	// 2) Redis 故障（nil,err）：降级本机 deliveredFor 裁决——本机首个仍投（不漏发），
	//    本机第二次被本地表挡住（单机内仍恰好一次）
	tryDeliveryClaim = func(string, time.Duration) (*redisclient.LockHandle, error) {
		return nil, errors.New("mock: redis down")
	}
	svc3 := NewMessageQueueService()
	if !svc3.ClaimReplyDelivery(22, 9522, 3, 9) {
		t.Fatal("Redis 故障应降级放行本机首个投递（P2-2 核心断言：旧实现此处漏发）")
	}
	if svc3.ClaimReplyDelivery(22, 9522, 3, 9) {
		t.Fatal("故障降级后本机仍须恰好一次")
	}
	// 故障期不同 (epoch,channel) 键互不占用
	if !svc3.ClaimReplyDelivery(22, 9522, 4, 9) {
		t.Fatal("新批次在降级路径应可认领")
	}
}
