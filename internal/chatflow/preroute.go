// 入队前分流裁决（FIX-9 能力缺口批，2026-09-27）：三层分流的**前两层**只有一个判据源，
// web 与通道两条入站链共用；命中后各自负责投递（HTTP 响应 / 出站队列），执行段不在此文件。
package chatflow

import (
	"errors"
	"log"

	"gorm.io/gorm"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/service"
)

// ============================================================
// 为什么把"判"抽出来、把"做"留在两侧
//
// 原形态：硬边界与到店快速通道两层判据**内联在 internal/api/chat_main.go 的 handler 里**，
// 通道入站（internal/channel/inbound.go）从 D5 并入合并队列那天起就只接了"简单消息 + 合并队列"
// 两层——它自己的注释就写着"仍缺的是到店快速通道与硬边界第一层"（§7 计划后续手术，一直没做）。
// 后果不是"少个功能"这么轻：
//   - 客户在网页问"今天天气怎么样"得到 0 延迟的引导话术，在同一家企微里问同一句，
//     会被塞进合并窗口走完整 AI 链 → 25s 后收到一段真实生成回复（还可能带幻觉）。
//     **同一个租户、同一个客户、同一句话，两个入口两套语义**，这是双入口最容易丢客户信任的地方。
//   - 到店意图（"我周末过去看看"）在网页侧走快速通道：接住意向 + 10-15s 第一段 + 25-45s 第二段
//     预约信息收集；在通道侧走的是普通 AI 生成，既不发预约追问也不置 guided 关闭，
//     线索收集这条主线在微信里等于断了。
//
// 判据收在这里（哪个入口、什么输入、该命中哪一层，只有一个答案），
// 执行段留在两侧：web 要写 HTTP 响应与 WS 推送，通道要走出站队列与幂等台账，
// 强行合并只会把 handler 的响应语义塞进消息队列包。
// **两侧的执行段都必须在 smoke 里各自钉住**，否则"判据统一"会变成"看起来统一"。
// ============================================================

// PreRouteKind 入队前分流结论（值直接作为 messages.route_result 落库，故与既有路由标记同名）
type PreRouteKind string

const (
	// PreRouteNone 两层都没命中 → 交给简单消息判定与合并队列
	PreRouteNone PreRouteKind = ""
	// PreRouteOffTopic 第一层：话题硬边界（0 延迟、不走 AI、不进队列）
	PreRouteOffTopic PreRouteKind = "offtopic_hardbound"
	// PreRouteLeadConfirm 第二层·分支B：到店意图且消息里已有手机号 → 留资确认（不走 AI）
	PreRouteLeadConfirm PreRouteKind = "lead_captured_confirmed"
	// PreRouteStoreVisit 第二层·分支C：到店意图且未留资 → 两段式快速通道（推迟分配顾问）
	PreRouteStoreVisit PreRouteKind = "store_visit_fast"
)

// PreRoute 一次分流裁决的结果
type PreRoute struct {
	Kind PreRouteKind
	// Reply 命中时**立刻**发给客户的那句话术（硬边界引导语 / 到店第一段 / 已留资确认）；
	// PreRouteNone 时为空串。
	Reply string
	// Phone 仅 PreRouteLeadConfirm 非空：消息里命中的手机号原文（落库前由调用侧决定脱敏口径）。
	Phone string
	// CapturedSkipped 到店意图命中、但因客户已越过留资阶段而被本层放行的标记。
	// 判据仍然只在这里做，调用侧拿到的只是"要不要为这次放行留一行日志"——
	// 旧 web 实现有这行日志，收口判据时不能把它悄悄删掉（排障时它是唯一线索）。
	CapturedSkipped bool
}

// Hit 是否命中前两层（true 表示调用侧不应入合并队列，直接投 Reply 那条）
func (p PreRoute) Hit() bool { return p.Kind != PreRouteNone }

// CapturedStage 旅程阶段是否已越过"纯 AI 接洽"期（已留资/已到店/已成交/已交付）。
//
// 这个四阶段白名单在旧代码里逐处手抄了五遍（到店快速通道前置拦截、留资硬拦截、简单消息防漏、
// 通道防漏、IsLeadCaptured 的阶段腿），抄漏一处的表现是"已经成交的客户还被追问要不要留电话"。
// 收成单点：**新增终局阶段时只需要改这里**，五处判定一起跟上。
func CapturedStage(stage string) bool {
	switch stage {
	case model.JourneyLeadCaptured, model.JourneyArrived, model.JourneyOrdered, model.JourneyDelivered:
		return true
	default:
		return false
	}
}

// DecidePreRoute 入队前两层分流的唯一判据源。
//
// 顺序铁律（与 web 旧实现逐字一致，勿调序）：
//  1. 硬边界先判——无关话题连"到店关键词"都不该参与判定，否则"今天天气不错，想去看看车"
//     这类混合句会先进到店通道再被 AI 纠正，白丢一次 0 延迟拦截的机会；
//  2. 到店意图其次——已留资客户（CapturedStage）跳过本层返回 None，交回合并队列正常流程
//     （等价旧代码里的 `goto skipStoreVisitFast`，这不是"漏判"而是刻意：留过资的客户再喊到店
//     是真客户在推进，该走策略引擎给实质回复，不该再吃一次罐头预约话术）。
//
// 纯判定无副作用：不写库、不动会话、不发 MQ，故可被任何入口安全调用。
func DecidePreRoute(tenantID uint, content, journeyStage string) PreRoute {
	// 第一层：话题硬边界
	if service.IsOffTopicForTenant(tenantID, content) {
		return PreRoute{
			Kind:  PreRouteOffTopic,
			Reply: service.GetOffTopicReplyForTenant(tenantID, content),
		}
	}
	// 第二层：到店倾向快速通道
	if service.IsStoreVisitIntentForTenant(tenantID, content) {
		if CapturedStage(journeyStage) {
			// 命中到店意图但客户已越过留资期 → 不放行到快速通道，交回合并队列走正常流程。
			// 判据在此，日志留给调用侧（web 旧实现有这一行「已是X阶段，跳过」的排障线索）。
			return PreRoute{CapturedSkipped: true}
		}
		if phone := PhoneRegex.FindString(content); phone != "" {
			// 分支B：消息里已经带手机号 → 当场留资确认（确认话术走行业键单点）
			return PreRoute{
				Kind:  PreRouteLeadConfirm,
				Reply: service.GetLeadCapturedConfirmReply(tenantID),
				Phone: phone,
			}
		}
		// 分支C：未留资 → 第一段接住意向，第二段追问由调用侧异步补（web/通道各自实现）
		return PreRoute{
			Kind:  PreRouteStoreVisit,
			Reply: service.GetStoreVisitFirstReply(tenantID, content),
		}
	}
	return PreRoute{}
}

// StoreVisitSecondSkipReason 到店第二段追问在延迟醒来后「还要不要发」的判定（web 与通道共用一份）。
//
// 返回空串表示照原计划发；非空是稳定原因码，调用侧只负责留痕与改道，不再自己判。
//
// 为什么需要这一层（2026-09-27 冒烟实测抓到的）：第二段是 `CancellableSleep(25~45s)` 之后
// 由 goroutine 补发的，而旧写法在醒来后**无条件**投递。于是这 25-45 秒里发生的人工接管完全不
// 作数——顾问在微信里把会话接过去了，客户 30 秒后仍会收到一句"留个姓名电话呗"。这既违背
// MIGRATION_GUIDE 那条「转人工无感知」，也和同一个入口里第一段的闸门自相矛盾：
// 入站时 `conv.IsHumanLocked || !conv.IsAiReplyEnabled` 会让 AI 闭嘴（inbound.go 第二段闸），
// 延迟追问却绕过它。同一条"AI 还有没有说话权"的判据只能有一份，所以收在这里。
//
// pending_handoff 也算：置位只有两条路——留资确认（分支B）与人工接管申请，两条都意味着
// "接下来由人说话"。此时再补一句罐头预约追问，就是在顾问面前抢话（分支C 自己不会置这个位，
// 所以第二段正常路径不会被它误伤）。
//
// 读库失败时返回空串（照发）：这里的两个失败出口不对称——判据读不到就静默丢掉一段预约追问，
// 表现是"客户说了到店、只收到一句就再没人理"，比多问一句更难解释。留痕交给调用侧。
func StoreVisitSecondSkipReason(convID uint) string {
	var conv model.Conversation
	err := db.DB.Select("id, is_human_locked, is_ai_reply_enabled, mode, pending_handoff").
		First(&conv, convID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "conversation_gone"
	}
	if err != nil {
		log.Printf("[到店快速通道-告警] 会话%d 接管状态读取失败，第二段按原计划发送: %v", convID, err)
		return ""
	}
	return StoreVisitSecondLegDecision(&conv)
}

// StoreVisitSecondLegDecision 上面那条判定的**纯函数内核**（给定会话现状，第二段该不该发）。
//
// 拆出来的理由不是好看：本文件所在的 chatflow 判据层历来是"无库也能跑"的纯逻辑
// （preroute_test.go 靠注入词表覆盖三层的每一条腿），把接管判定留在纯函数里，
// 反证用例才能不依赖数据库就把四张腿各自踩一遍——否则"AI 已让位却还在说话"这条
// 只能靠真库集成测试兜，而集成测试最容易在改判据时悄悄空转。
func StoreVisitSecondLegDecision(conv *model.Conversation) string {
	if conv == nil {
		return "conversation_gone"
	}
	if conv.IsHumanLocked || !conv.IsAiReplyEnabled || conv.Mode == "human" {
		return "human_takeover"
	}
	if conv.PendingHandoff {
		return "handoff_pending"
	}
	return ""
}
