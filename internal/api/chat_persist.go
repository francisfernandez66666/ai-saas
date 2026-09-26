// 对话主链落库错误口径（FIX-5，2026-09-27）：必查/旁路两类读写失败的统一收口点。
//
// 已收口的链路（2026-09-27 二批全部落地，不再留"下批"）：
//
//	正式链 /chat（chat_main.go）、免登录链 /chat/test（chat_unauthorized.go）、
//	访客欢迎（chat_guest.go）、人工回复与接管（chat_human.go）、顾问台（advisor.go）、
//	通道入站（internal/channel/inbound.go 的客户消息与会话状态列）、
//	顾问端 /chat/history 的两处读。
//
// 结构守卫 chat_persist_guard_test.go 现在**按文件**盯这五份 Go 文件：
// 既不许再出现"深度 0 的裸 DB 语句"，也不许各文件的口径调用点数低于基线。
// 判据函数在这里是单点，新增读写点只是逐点分类（必查/旁路），不是重写机制。
package api

import (
	"ai-scrm/internal/logx"
	"ai-scrm/internal/metrics"
	"ai-scrm/internal/pii"
	"log"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ============================================================
// 主链落库错误口径（与 C7 红线同级，改对话链之前先读这段）
//
// 现象（用户视角）：客户发的话落库失败时接口照样回 200、AI 照样答，于是网页、
// 顾问端、E8 会话存档三处都看不见那句话——事后没人能解释"他到底说过什么"。
//
// 根因形态：chat_main.go / chat_unauthorized.go 里一批
// `db.RQ(s.c).Create(...)`、`db.RQ(s.c).Model(...).Update(...)` 是**裸语句**，
// GORM 的 error 被就地丢掉，"写失败"在代码层面和"写成功"完全同形。
//
// 口径分两类，不许混：
//
//	必查（persistRequired / persistRequiredMsg）——失败即回 500：
//	  会进对话历史、且其 ID 或状态会被当作本次结果回给客户端的那些读写。
//	  客户入站句 customer_inbound / 被合并抑制的入站句 suppressed_inbound /
//	  正式链与顾问代答的 AI 回复 ai_reply / 快速通道回复 offtopic_reply、simple_reply、
//	  store_visit_reply、lead_captured_reply / 访客秒回 welcome_reply /
//	  顾问真人消息 human_reply / 人工接管态 human_takeover_lock、transfer_to_human、
//	  transfer_to_ai、ai_reply_toggle、content_safety_takeover /
//	  试驾单 test_drive_create / 聊天记录读 chat_history_read、last_customer_msg_read。
//	  文案分两种：对话链统一「消息处理失败，请重试」；动作类走 persistRequiredMsg
//	  说清是哪个动作没生效——「接管失败，请重试」比「消息处理失败」更接近事实。
//	  理由：这些行的 ID 就是响应体里的 message / customer_msg_id。落库失败却回一个
//	  ID=0 的幻行，等于告诉前端"这条存在"而库里没有——前端后续的撤回、重发、
//	  已读回执全部指向空气。免登录链的 suppressed_inbound 已按同一口径收过（G-13），
//	  本次把正式链对齐，两条入口同一句话只有一种结局。
//
//	旁路留痕（persistBypass / persistBypassErr）——失败只 [对话-告警] 日志 + 计数，不打断答复：
//	  状态列与辅助查询：visitor_key_backfill、journey_stage、t_vector、
//	  guided_disabled、conversation_state、customer_profile、lead_followup、
//	  oneid_reload、oneid_conversation_reload、assignee_lookup、assignee_load、flow_start、
//	  inbound_conversation_link、inbound_readback、active_conversation_read、stage_reload。
//	  顾问台"读得到就显示、读不到就留空"那一类：advisor_stats_read、advisor_detail_read、
//	  advisor_list_enrich、advisor_dict_read、followup_readback_enrich、tag_readback、
//	  template_recommend_read、welcome_dedup_count、kb_material，
//	  以及权限判定读 org_scope_check_read（读失败=拒绝，方向本就是安全侧，不能报 500）。
//	  理由：留资标记与顾问分配走到这一点上多半已经落成功，此时回 500 会把
//	  "其实做完了"的动作对客户报成失败，客户重试即二次分配——比缺一行状态更糟。
//	  但"不打断"不等于"不数"：计数器是这类路径唯一的线上可见性，
//	  一个只会打日志并返回成功的守卫等于没有守卫。
//
//	通道侧同一口径（internal/channel/inbound.go，2026-09-27 同批）：
//	  channel_inbound（必查语义——写不进即本轮判 failed，台账 attempts<5 时重推会重新认领）、
//	  channel_conversation_touch（旁路，会话排序用状态列）。
//	  通道链没有 gin 上下文可回 500，"显式失败"的出口是入站台账与计数器，不是响应体。
//
// 一句 DB 抖动会不会让客户看到 500？会，但只在"这句话根本没能进历史"的那些点上——
// 那才是唯一诚实的答复。真故障时入站句那一处先红，后面的旁路点根本走不到。
//
// 结构守卫见 chat_persist_guard_test.go：chat_main.go 内不得再出现以 `db.` 开头的裸语句行，
// 且经这两个函数收口的调用点数不得低于基线（把"会报错的写库"改回"丢掉错误的裸写"会直接红）。
// ============================================================

// chatPersistFailMsg 必查类落库失败时对客户端的统一文案。
// 真实错误只落日志（RespErrInternal 的既有约定），响应体里不带 SQLSTATE/表名。
const chatPersistFailMsg = "消息处理失败，请重试"

// persistRequired 必查类落库失败收口：留痕 + 计数 + 回 500。
// 返回 true 表示本函数已经应答客户端，调用方必须立刻 return true，
// 不得继续把那条并不存在的行拼进成功响应。
func persistRequired(c *gin.Context, kind string, customerID, conversationID uint, res *gorm.DB) bool {
	return persistRequiredMsg(c, kind, customerID, conversationID, res, chatPersistFailMsg)
}

// persistRequiredMsg 同 persistRequired，只把对外文案交给调用方——
// 对话链之外还有"转人工/切回 AI/发人工消息"这类**动作**：它们失败时对客户说
// "消息处理失败，请重试"是错的话术（点按钮的人看到的是"接管没生效，请重试"）。
// 单独开一个文案入口、而不是让调用方自己 RespErr，是为了让留痕格式、计数器、
// "无响应器不得 panic"三件事仍然只有一份实现（口径漂移就是这么发生的）。
func persistRequiredMsg(c *gin.Context, kind string, customerID, conversationID uint, res *gorm.DB, msg string) bool {
	if res == nil || res.Error == nil {
		return false
	}
	// 错误详情可能带列值（唯一约束冲突会回显冲突值，本表里有手机号列），先脱敏再截断。
	detail := pii.MaskPhoneInText(logx.Safe(res.Error.Error(), 200))
	log.Printf("[对话-告警] 必查落库失败(kind=%s) 客户%d 会话%d: %s", kind, customerID, conversationID, detail)
	metrics.IncChatPersistError(kind)
	// 只有"确实能写响应"时才应答：c 为 nil、或响应器未就绪（单测里直接调阶段方法的形态）时
	// 走 else——RespErr/RespErrInternal 内部都是 c.JSON，喂 nil 进去是 panic 而不是报错，
	// 那等于把一次可诊断的落库失败升级成一次 500 变 502 的进程崩溃。
	// 无论能否应答都返回 true：调用方一律不得继续把那条并不存在的行拼进成功响应。
	if c != nil && c.Request != nil && c.Writer != nil {
		RespErrInternal(c, res.Error, msg)
	} else {
		log.Printf("[对话-告警] 必查落库失败但无响应器可回(kind=%s)：调用方仍须立即中止本次答复", kind)
	}
	return true
}

// persistBypass 旁路类落库失败收口：只留痕（告警 + 计数），不给客户端报错。
func persistBypass(kind string, customerID, conversationID uint, res *gorm.DB) {
	if res == nil {
		return
	}
	persistBypassErr(kind, customerID, conversationID, res.Error)
}

// persistBypassErr 同上，用于非 GORM 返回值的旁路动作（如流程引擎 StartFlow）。
// 单独收一个入口是为了让所有旁路失败共用同一份日志形态与同一个计数器。
func persistBypassErr(kind string, customerID, conversationID uint, err error) {
	if err == nil {
		return
	}
	detail := pii.MaskPhoneInText(logx.Safe(err.Error(), 200))
	log.Printf("[对话-告警] 旁路操作失败(kind=%s) 客户%d 会话%d: %s", kind, customerID, conversationID, detail)
	metrics.IncChatPersistError(kind)
}
