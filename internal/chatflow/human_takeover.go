// 人工锁定态裁决唯一真相源（A1 实装，2026-09-22 全量修复批·批四）：
// 三条 C 端入口（web 正式链 chat_main / 免登录链 chat_unauthorized / 通道入站）此前对
// "会话处于人工锁定态时这一轮到底谁来答"各自表述：
//   - chat_main 只查 CheckHumanTimeout 并把 Mode/IsHumanLocked **改在内存里**（不落库），
//     随后无条件回"顾问正在赶来"——锁定态永不解除，客户被卡在无人应答的会话里；
//   - chat_unauthorized 才有完整语义（超时重开 AI 并落库 / 顾问刚回则跳过 AI / 否则 AI 代答）。
//
// 同一客户状态在两条链给出不同动作，reply_attributions 里同样本的可比性就不成立
// （批五择臂是在噪声上学），故先把它收成一处。
package chatflow

import (
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"log"
	"time"
)

// TakeoverAction 人工锁定态下本轮的裁决结果种类。
type TakeoverAction int

const (
	// TakeoverAReply 由 AI 继续生成回复（非锁定态、或锁定已因超时解除）
	TakeoverAReply TakeoverAction = iota
	// TakeoverSkipAI 本轮不出 AI 回复：顾问刚回过，或会话处于"仅人工"单人模式
	TakeoverSkipAI
)

// 跳过 AI 时的路由标记（沿用免登录链既有字面量，前端/归因口径不变）。
const (
	// RouteHumanLockedNoAI 单人模式（AI 回复被关闭）且未到超时——纯等人
	RouteHumanLockedNoAI = "human_locked_no_ai"
	// RouteHumanSkipAI 顾问在超时窗内已回复——让人的话先落地
	RouteHumanSkipAI = "human_skip_ai"
)

// TakeoverDecision 裁决输出：动作 + 是否发生了"超时自动重开 AI"。
type TakeoverDecision struct {
	Action   TakeoverAction // 本轮由 AI 答还是跳过 AI
	Route    string         // 跳过 AI 时的路由标记；AI 答复时为空
	Reopened bool           // true=本轮解除了人工锁定（已落库，调用方无需再写）
}

// HumanTakeoverDecide 判定人工锁定态下这一轮由谁应答（A1 唯一真相实现）。
//
// 语义（逐条对齐原免登录链实现，行为等价、两条链统一）：
//  1. 非 (mode=human && is_human_locked) → 直接放行 AI；
//  2. AI 回复被关（is_ai_reply_enabled=false，即顾问开"仅人工"）：
//     待接管 + 顾问有过回复且已超 assigned_lead_ai_timeout → 自动重开 AI（**落库**）后放行；
//     否则跳过 AI，路由 human_locked_no_ai；
//  3. AI 回复开着且 assigned_lead_ai_auto_reply=true 且顾问在超时窗内回过 → 跳过 AI，
//     路由 human_skip_ai（人的话优先，AI 不抢答）；
//  4. 其余（顾问从未回 / 回了但已超时 / 自动代答开关关）→ AI 代答。
//
// 落库纪律：重开 AI 时本函数**自己**做字段级 Updates（同 CheckHumanTimeout 的 D4 口径），
// 调用方只负责应答——这修掉了正式链"只改内存、锁永不解除"的原缺陷。
// where 带 tenant_id 双保险，且只写接管相关四列，绝不动 mode 以外无关字段。
func HumanTakeoverDecide(conv *model.Conversation) TakeoverDecision {
	return HumanTakeoverDecideAt(conv, time.Now())
}

// HumanTakeoverDecideAt 带时钟注入的裁决本体，单测据此稳定复现"超时/未超时"边界。
func HumanTakeoverDecideAt(conv *model.Conversation, now time.Time) TakeoverDecision {
	if conv == nil || conv.ID == 0 {
		return TakeoverDecision{Action: TakeoverAReply}
	}
	if conv.Mode != "human" || !conv.IsHumanLocked {
		return TakeoverDecision{Action: TakeoverAReply}
	}
	// 与免登录链同源的两个热配键：超时阈值默认 300s、AI 自动代答默认开
	timeout := time.Duration(runtimecfg.SafeCfgInt("assigned_lead_ai_timeout", 300)) * time.Second
	autoReply := runtimecfg.SafeCfgBool("assigned_lead_ai_auto_reply", true)

	if !conv.IsAiReplyEnabled {
		// 单人模式：只有"待接管且顾问超时无响应"才打破（客户已交到人手上，不能无限等）
		if conv.PendingHandoff && conv.LastHumanReplyAt != nil &&
			now.Sub(*conv.LastHumanReplyAt) >= timeout {
			reopenConversationAI(conv)
			log.Printf("[人工接管] 会话%d 顾问超时%d秒未回复，自动重开AI回复", conv.ID, int(timeout.Seconds()))
			return TakeoverDecision{Action: TakeoverAReply, Reopened: true}
		}
		return TakeoverDecision{Action: TakeoverSkipAI, Route: RouteHumanLockedNoAI}
	}

	if autoReply && conv.LastHumanReplyAt != nil && now.Sub(*conv.LastHumanReplyAt) < timeout {
		return TakeoverDecision{Action: TakeoverSkipAI, Route: RouteHumanSkipAI}
	}
	return TakeoverDecision{Action: TakeoverAReply}
}

// reopenConversationAI 解除人工锁定并重开 AI 回复：内存回写 + 字段级落库。
// 落库失败只记 WARN 不回滚业务（下轮请求会重新判定并再写一次，语义幂等）。
func reopenConversationAI(conv *model.Conversation) {
	conv.Mode = "ai"
	conv.IsHumanLocked = false
	conv.IsAiReplyEnabled = true
	conv.PendingHandoff = false
	if err := db.DB.Model(&model.Conversation{}).
		Where("id = ? AND tenant_id = ?", conv.ID, conv.TenantID).
		Updates(map[string]interface{}{
			"is_ai_reply_enabled": true,
			"is_human_locked":     false,
			"mode":                "ai",
			"pending_handoff":     false,
		}).Error; err != nil {
		log.Printf("[人工接管][WARN] 会话%d 超时重开AI落库失败(下轮将重复判定): %v", conv.ID, err)
	}
}
