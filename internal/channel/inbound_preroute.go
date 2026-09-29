// 通道入站的「入队前两层分流」执行段（FIX-9 能力缺口批，2026-09-27）
//
// 判据不在本文件——那一份在 chatflow.DecidePreRoute（web 与通道共用）。这里只有通道侧的
// **执行**：写完会话/客户状态后把话术丢进出站队列。为什么要拆成两个文件：
// inbound.go 已经装着 D5 合并队列的三分支裁决（等待者/被合并/持权者），再塞两层快速通道
// 会把"哪条消息该进批次"这件事和"哪条消息根本不该进批次"混在一屏里。
package channel

import (
	"log"
	"time"

	"ai-scrm/internal/chatflow"
	"ai-scrm/internal/db"
	"ai-scrm/internal/metrics"
	"ai-scrm/internal/model"
	"ai-scrm/internal/pii"
	"ai-scrm/internal/service"
)

// runInboundPreRoute 在消息进合并队列**之前**跑前两层分流；返回 true 表示本轮已由快速通道答完。
//
// 命中即返回的三条各自对应 web 的一条既有语义（文案与路由标记两侧同源）：
//   - 硬边界：0 延迟引导话术，不占合并窗口、不烧 token；
//   - 分支B（到店意图 + 句中带手机号）：当场留资 + 会话置待接管 + 确认话术；
//   - 分支C（到店意图 + 未留资）：关引导式反问 → 第一段接住意向 → 异步第二段收预约信息。
//
// 取不到客户行时返回 false 交回正常链路：这里"判不了"的正确出口是走原来的路，
// 而不是静默不回——静默不回等于把客户那句话吞掉（与 D7 相似抑制同一口径）。
func runInboundPreRoute(ch *model.Channel, in *InboundMessage, customerID uint, conv *model.Conversation) bool {
	tid := ch.TenantID
	var cust model.Customer
	if err := db.DB.First(&cust, customerID).Error; err != nil {
		log.Printf("[通道] 客户%d 读取失败，跳过入队前分流(交回合并队列): %v", customerID, err)
		return false
	}
	pre := chatflow.DecidePreRoute(tid, in.Content, cust.JourneyStage)
	if pre.CapturedSkipped {
		log.Printf("[通道] 客户%d 已是%s阶段，跳过到店快速通道，走正常流程", customerID, cust.JourneyStage)
	}
	switch pre.Kind {
	case chatflow.PreRouteOffTopic:
		log.Printf("[通道-硬边界] 客户%d 拦截无关话题(入队前): %q → %q",
			customerID, pii.MaskPhoneInText(in.Content), pii.MaskPhoneInText(pre.Reply))
		saveAndDeliver(ch, conv, cust.ID, pre.Reply, string(pre.Kind))
		return true
	case chatflow.PreRouteLeadConfirm:
		return channelLeadConfirmFast(ch, in, customerID, conv, &cust, pre)
	case chatflow.PreRouteStoreVisit:
		return channelStoreVisitFast(ch, in, conv, &cust, pre)
	}
	return false
}

// channelLeadConfirmFast 分支B：到店意图且消息里已有手机号 → 当场留资确认（不走 AI）。
//
// 留资本身不在此实现：DetectLeadCapture 已把"OneID 合并 / phone / 旅程阶段 / 轮询分配顾问 /
// FollowUp 线索 / MQ 事件 / webhook 扇出 / 归因回填"收在一处（web 的留资硬拦截、通道处理段
// 的硬拦截走的都是它）。本函数只补 web 分支B 相对硬拦截多出来的两件事：
//  1. 会话置 pending_handoff（顾问端"待接管"徽标 + 接管队列取数依据）；
//  2. 用裁决给的那句确认话术回客户。
//
// ⚠ OneID 合并会把会话迁到老客户名下：合并后必须用**新客户 ID** 投递，
// 否则出站消息挂在一个已被删除的 customer_id 上（顾问端看不到、归因也接不上）。
func channelLeadConfirmFast(ch *model.Channel, in *InboundMessage, customerID uint,
	conv *model.Conversation, cust *model.Customer, pre chatflow.PreRoute) bool {

	leadResult := chatflow.DetectLeadCapture(in.Content, cust)
	deliverCustomerID := customerID
	if leadResult > 0 {
		// >0 = 合并到老客户（DetectLeadCapture 已把 cust 重载为目标客户）
		deliverCustomerID = cust.ID
		log.Printf("[通道-已留资线索] 客户%d OneID 合并到老客户%d", customerID, deliverCustomerID)
	}
	if uerr := db.DB.Model(&model.Conversation{}).Where("id = ? AND tenant_id = ?", conv.ID, ch.TenantID).Updates(map[string]interface{}{
		"pending_handoff":         true,
		"guided_remaining_rounds": 0,
		"guided_disabled":         false,
		"handoff_notified_at":     time.Now(),
	}).Error; uerr != nil {
		// 待接管标记写不上不该让客户收不到确认：留痕后继续投递（与主链"辅助状态列旁路"同口径）
		log.Printf("[通道-告警] 会话%d 待接管标记落库失败: %v", conv.ID, uerr)
		metrics.IncChatPersistError("channel_lead_handoff_mark")
	}
	conv.PendingHandoff = true

	// 与 web 分支B 同款节奏：先等一下再回（回太快是机器人的味道）
	chatflow.CancellableSleep(deliverCustomerID, service.GetStoreVisitFirstDelay(ch.TenantID))
	log.Printf("[通道-已留资线索] 客户%d 留资完成: stage=%s assigned=%d",
		deliverCustomerID, cust.JourneyStage, cust.AssignedUserID)
	saveAndDeliver(ch, conv, deliverCustomerID, pre.Reply, string(pre.Kind))
	return true
}

// channelStoreVisitFast 分支C：到店意图但还没留资 → 两段式接住意向，推迟分配顾问。
//
// 第一段（"到店方便吗，几点去"）当场发；第二段（收预约信息）在 GetStoreVisitSecondDelay
// 之后异步发——与 web 同结构。第二段那条**必须真投出去**：web 侧第二段只落库+WS 推送，
// 通道侧没有"客户会自己刷新页面"这回事，只落库不投递等于客户永远等不到第二句。
func channelStoreVisitFast(ch *model.Channel, in *InboundMessage, conv *model.Conversation,
	cust *model.Customer, pre chatflow.PreRoute) bool {

	// 关掉引导式反问：这两段是罐头话术，不该再叠一层 AI 反问
	if uerr := db.DB.Model(&model.Conversation{}).Where("id = ? AND tenant_id = ?", conv.ID, ch.TenantID).
		Update("guided_disabled", true).Error; uerr != nil {
		log.Printf("[通道-告警] 会话%d 引导开关落库失败: %v", conv.ID, uerr)
		metrics.IncChatPersistError("channel_guided_disable")
	}
	conv.GuidedDisabled = true

	chatflow.CancellableSleep(cust.ID, service.GetStoreVisitFirstDelay(ch.TenantID))
	saveAndDeliver(ch, conv, cust.ID, pre.Reply, string(pre.Kind))

	// 第二段：异步补，收预约信息（客户在这 25-45s 里可能已经回手机号，那条走下一轮的分支B）
	secondReply := service.GetStoreVisitSecondReply(ch.TenantID, in.Content)
	tid, cid, convID, chID := ch.TenantID, cust.ID, conv.ID, ch.ID
	bgDone := trackBackground("到店第二段追问")
	go func() {
		defer bgDone()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[通道-告警] 到店第二段追问 panic 已恢复 客户%d: %v", cid, r)
				metrics.IncStoreVisitSecondFail()
			}
		}()
		sd := service.GetStoreVisitSecondDelay(tid)
		chatflow.CancellableSleep(cid, sd)
		// 醒来先复核"AI 还有没有说话权"：这 25-45s 里顾问可能已经接管、客户也可能已经留资。
		// 判据在 chatflow 单点（web 侧同一条），这里只执行"跳过就什么都不发"。
		if reason := chatflow.StoreVisitSecondSkipReason(convID); reason != "" {
			log.Printf("[通道-未留资线索] 客户%d 到店第二段已跳过(%s)", cid, reason)
			return
		}
		// 落库 + 出站一次做完（与第一段、与 web 侧同一段话术同一出口）。
		// 这里刻意**只传 id**：`ch`/`conv` 是请求期对象，goroutine 醒来时那边早已改写完毕，
		// 带着指针进后台既是数据竞争（-race 实测抓到），也会把"这一轮实际是谁的会话"读成旧值。
		saveAndDeliverIDs(tid, chID, convID, cid, secondReply, string(chatflow.PreRouteStoreVisit))
		log.Printf("[通道-未留资线索] 客户%d 到店第二段追问已发送", cid)
	}()
	return true
}
