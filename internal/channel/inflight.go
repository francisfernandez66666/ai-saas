// 通道后台协程记账（FIX-5/-race 收口批，2026-09-27）
//
// 为什么要这一份：入站链路有两处 `go`（headless worker + 到店第二段追问），它们都读**运行期
// 可变的全局状态**（config.GlobalConfig、runtimecfg.DefaultSystemConfigService）和数据库。
// 回调段 ack success 就返回了，goroutine 却还在跑——单测里下一条用例紧接着改写这些全局量，
// 于是 `-race` 报的是"测试之间互相踩"，看起来像测试层的噪声，实际是**产品代码里没有任何人
// 知道这一轮后台工作有没有做完**。生产上同样要这个数：停机时不等待，正在生成的那条 AI 回复
// 就会被进程退出吃掉（客户那句话落库了、回复永远不来）。
//
// 口径：Add 必须在 `go` 语句**之前**于父协程里发生（否则 Wait 可能早于 Add 而立即返回）；
// Done 用 defer 注册在被派发的协程里。两处都用 trackBackground() 一行包住，防以后新增
// 后台协程时漏记账——漏记的那条不会报错，只会在停机/测试时以"偶发竞态"的形式回来。
package channel

import (
	"log"
	"sync"
	"time"
)

// inboundBackground 累计本包在途的后台协程（worker + 到店第二段追问）。
var inboundBackground sync.WaitGroup

// trackBackground 在**父协程**里登记一个即将派发的后台协程，返回它的收尾函数。
//
// 用法固定为两行：`bgDone := trackBackground("名字")` 紧跟 `go func(){ defer bgDone(); … }()`。
// Add 必须发生在 go 语句之前（在子协程里 Add，Wait 可能早于 Add 而立即返回，等于没记）；
// Done 用 defer 注册在子协程体第一行，保证 panic 路径也归还计数（漏还一次就永久抬高在途数，
// 停机等待会白等满超时）。
func trackBackground(what string) func() {
	inboundBackground.Add(1)
	started := time.Now()
	return func() {
		inboundBackground.Done()
		if d := time.Since(started); d > 60*time.Second {
			// 只在异常长尾时留痕：合并窗 + AI 预算 + 人类化延迟的正常上限远小于 60s，
			// 超了说明有协程卡在锁/延迟里，这正是要在停机日志里看见的那一类。
			log.Printf("[通道-告警] 后台协程 %s 运行 %s 才收尾（疑似卡死）", what, d.Round(time.Second))
		}
	}
}

// waitBackgroundDrainedWithin 阻塞到本包后台协程全部收尾，或超过调用方给的预算。
// 返回 true=已排空，false=仍有在途（调用方据此决定留痕还是判红）。
// 停机走 DrainBackgroundFor，单测走 drainInboundWorkers（inflight_test.go）。
func waitBackgroundDrainedWithin(budget time.Duration) bool {
	done := make(chan struct{})
	go func() {
		inboundBackground.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(budget):
		return false
	}
}

// DrainBackgroundFor 供进程停机流程调用：给在途的通道后台协程（合并队列等待 + AI 生成 + 出站）
// 一个有限预算收尾，超时只留痕不阻塞退出。
//
// 为什么需要：HTTP 已停、合并队列已排空，但通道 worker 可能正卡在"人类化延迟"里等醒来投递——
// 不等它，进程退出就把这一轮吃掉，客户那句话落库了而回复永远不来（且台账停在 processing，
// 重启后要等锁超时自愈）。这是**停机窗口的丢答**，不是测试竞态。
func DrainBackgroundFor(budget time.Duration) bool {
	if budget <= 0 {
		budget = 5 * time.Second
	}
	ok := waitBackgroundDrainedWithin(budget)
	if !ok {
		log.Printf("[通道] 后台协程未在 %s 内收尾，仍有在途轮次可能丢答（见 worker 卡死告警）", budget)
	}
	return ok
}
