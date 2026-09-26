// 通道后台协程记账的自身护栏（FIX-5/-race 收口批，2026-09-27）。
//
// 这套账本身也可能"记了等于没记"：若 trackBackground 只在子协程里 Add（或干脆漏记），
// 排空函数会立刻返回 true，测试与停机都以为干净了——所以这里必须**同时**证明
// ① 有在途时排空返回 false（说明计数真被看见），② 收尾后返回 true（说明计数真被归还）。
package channel

import (
	"testing"
	"time"
)

// drainInboundWorkers 等本包后台协程全部收尾，供用例在**复原全局量之前**调用。
//
// 为什么必须在这个时点：worker 会读 config.GlobalConfig 与 runtimecfg.DefaultSystemConfigService，
// 而这两样在用例结束时被换回原值；下一条用例的 SetupTestDB 又会重新 LoadConfig 写一遍。
// 旧写法没人等 worker（回调段一 return 就没人管它了），于是"本用例的后台"与"下一条用例的全局量
// 改写"并发——`-race` 报出来像测试互踩，实际是产品侧从没有一处知道这轮后台活有没有跑完。
func drainInboundWorkers(t *testing.T) {
	t.Helper()
	// 90s 预算：够覆盖「合并窗 1s + mock 生成 + 人类化延迟」的最慢正常收尾；超了就是真卡死。
	if !waitBackgroundDrainedWithin(90 * time.Second) {
		t.Errorf("通道后台协程未在 90s 内收尾：后续用例的全局量改写会与它并发（-race 会红，且这条用例的断言可能读到了半轮）")
	}
}

// TestBackgroundAccountingActuallyCounts 记账正反双向自证：不加计数的那条腿必须判"未排空"。
func TestBackgroundAccountingActuallyCounts(t *testing.T) {
	// 起点必须干净：本包若有别的用例留了在途协程，下面的"未排空"判据就不成立，
	// 因此先等一轮（预算给足），否则这条用例会在别人身上假红。
	if !waitBackgroundDrainedWithin(30 * time.Second) {
		t.Fatalf("用例开始前本包仍有在途后台协程，无法自证计数生效")
	}

	release := make(chan struct{})
	started := make(chan struct{})
	bgDone := trackBackground("记账自证")
	go func() {
		defer bgDone()
		close(started)
		<-release
	}()
	<-started

	// ① 在途：给 200ms 预算必须排不空——若 trackBackground 漏记，这里会误判"已排空"（假绿）
	if waitBackgroundDrainedWithin(200 * time.Millisecond) {
		close(release)
		t.Fatalf("后台协程仍在途时排空返回了 true：说明 Add 没发生在 go 之前（计数形同虚设）")
	}
	close(release)

	// ② 收尾后必须归零：Done 漏还（例如 panic 路径不归还）会永久抬高在途数，这里判红
	deadline := time.Now().Add(5 * time.Second)
	settled := false
	for !settled && !time.Now().After(deadline) {
		settled = waitBackgroundDrainedWithin(time.Second)
	}
	if !settled {
		t.Fatalf("协程已返回但计数未归还：Done 漏在某个出口路径上")
	}
}
