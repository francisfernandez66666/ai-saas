// Package cdp CDP 数据底座：OneID 身份归并、画像/标签/事件写收口与分群引擎（四维标签体系）。
package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/mq"
	"ai-scrm/internal/service"

	"gorm.io/gorm"
)

// ============================================================
// CDP 摄入消费者（SAAS_PLAN §16.8 写收口唯一入口）
//
// 数据流：业务层发布 user_event → 本消费者 →
//   Inbox 幂等抢占 → ID-Mapping 锚点归并 → 画像确保 → Raw事件落库
//   → 四维原子标签计算（首批：行为/身份维；态度维严禁行为硬推）
//
// 模式说明：MQ_TYPE=kafka 走真实消费者组；log 模式下 LogCenter 兼作
// 进程内总线（发布即本地分发），两种模式消费者代码完全一致
// ============================================================

// builtinTagDefs 首批原子标签定义（启动时 upsert 进 cdp_tag_definitions）
var builtinTagDefs = []model.CdpTagDefinition{
	{Code: "idm_guest", Name: "访客身份", Category: "identity"},
	{Code: "beh_lead_captured", Name: "已留资", Category: "behavior"},
	{Code: "beh_msg_active", Name: "会话活跃", Category: "behavior"},
	// M2 补齐（2026-08-25）：payment 子事件此前发布后零消费——CDP 记账收口
	{Code: "tran_order_paid", Name: "付费转化", Category: "behavior"},
	// P1-3（2026-08-29）四维补齐：环境维 + 态度维
	// 环境维：由事件携带的渠道/路由/发生时段时间派生（非行为硬推，属上下文信号）
	{Code: "env_route", Name: "交互路由", Category: "environment"},
	{Code: "env_time_slot", Name: "到访时段", Category: "environment"},
	{Code: "env_channel", Name: "来源渠道", Category: "environment"},
	{Code: "env_device", Name: "访问设备", Category: "environment"},
	{Code: "env_referrer", Name: "引荐来源", Category: "environment"},
	{Code: "env_geo", Name: "地理位置", Category: "environment"},
	{Code: "env_language", Name: "语言偏好", Category: "environment"},
	// 态度维：严禁由行为推导——仅当事件属性显式携带 NLP/零方情绪（emotion）时才打，否则不打
	{Code: "att_emotion", Name: "情绪态度", Category: "attitude"},
	{Code: "att_intent", Name: "表达意向", Category: "attitude"},
	{Code: "att_satisfaction", Name: "满意度态度", Category: "attitude"},
	{Code: "att_objection", Name: "异议态度", Category: "attitude"},
	// P1-2（2026-08-30）运营闭环：付费成功→开通欢迎流程落标记
	{Code: "mkt_welcome_sent", Name: "已付费待欢迎", Category: "behavior"},
	// P3（2026-08-30）行为维补齐：到访/试驾/深度兴趣/价格探询（事件驱动，非行为硬推）
	{Code: "beh_visit", Name: "到店到访", Category: "behavior"},
	{Code: "beh_testdrive", Name: "已试驾", Category: "behavior"},
	{Code: "beh_deep_interest", Name: "深度兴趣", Category: "behavior"},
	{Code: "beh_price_inquiry", Name: "价格探询", Category: "behavior"},
	// P1-1（2026-08-30）运营级原子标签补齐至 30+：运营闭环行为/态度/环境维扩展
	{Code: "beh_viewed_model", Name: "浏览车型", Category: "behavior"},   // 事件 model_view 或属性 model_id
	{Code: "beh_complained", Name: "投诉反馈", Category: "behavior"},     // 事件 complaint
	{Code: "beh_shared", Name: "分享转发", Category: "behavior"},         // 事件 share
	{Code: "beh_referral", Name: "转介绍", Category: "behavior"},        // 事件 referral（老带新）
	{Code: "beh_followed", Name: "关注加微", Category: "behavior"},       // 事件 follow
	{Code: "beh_repeat_visit", Name: "复访", Category: "behavior"},     // 事件 store_visit 且属性 repeat=true
	{Code: "beh_booked", Name: "已预约", Category: "behavior"},          // 事件 booking
	{Code: "att_loyalty", Name: "忠诚态度", Category: "attitude"},        // 属性 loyalty（零方/运营标记）
	{Code: "att_churn_risk", Name: "流失风险", Category: "attitude"},     // 属性 churn_risk
	{Code: "att_advocate", Name: "推荐意愿", Category: "attitude"},       // 属性 advocate
	{Code: "env_utm_source", Name: "UTM来源", Category: "environment"}, // 属性 utm_source
	{Code: "env_landing_page", Name: "落地页", Category: "environment"}, // 属性 landing_page
	{Code: "env_browser", Name: "浏览器", Category: "environment"},      // 属性 browser
}

// 事件生产端现状（G-19 收口批 2026-09-24 逐项核实，防止下一轮审计把"消费端支持"当成"链路已通"）：
//   - store_visit / test_drive：api/advisor.go 顾问手动推进阶段（到店、试驾子阶段）
//   - booking：api/advisor.go 试驾单创建
//   - follow：channel/inbound.go 关注/加微事件
//   - complaint：api/feedback.go 低分反馈
//   - guest_created / payment：chat_guest.go / billing 到账
//   - model_view / share / referral：**只有消费端 case，没有生产端**——不是漏埋，是
//     当前产品面没有对应入口（C 端没有车型详情页，前端只调 /admin/knowledge/models；
//     分享/转介绍链路未建）。要打通得先有能力，硬造一个"没人访问的端点上的埋点"
//     只会得到一条永不命中的分支，比空白更容易骗人。
//
// 护栏落点（改这些分支时必须同步动它们，否则"链路已通"又是一句自查不了的话）：
//   - booking / test_drive / store_visit / complaint 四条：tools/uat_advisor.sh 的 G-19 段，
//     逐条断 event_logs 落库 + cdp_tag_assignments 标签，并含"同值 PATCH 不重复发""低分无文字不发投诉"两条反向；
//   - follow：internal/channel/inbound_follow_test.go（白名单外/无身份/空 external 三路必拒，
//     放行路逐字段核信封）；
//   - event_value 是 json 列，脚本里别用 LIKE 匹配它（会报 `json ~~ unknown` 且错误被吞成空串）。
//
// StartIngestConsumer 注册 user_event 订阅（main 启动时调用一次）
// 标签字典随启动 upsert，保证消费端可写标签
func StartIngestConsumer() {
	for i := range builtinTagDefs {
		def := &builtinTagDefs[i]
		var cnt int64
		gdb().Model(&model.CdpTagDefinition{}).Where("code = ?", def.Code).Count(&cnt)
		if cnt == 0 {
			gdb().Create(def)
		}
	}
	mq.Subscribe(mq.TopicUserEvent, func(ctx context.Context, env mq.Envelope) error {
		return mq.WithInbox(ctx, env, processEvent)
	})
	log.Println("[CDP] IngestConsumer 已订阅 user_event（写收口）")
}

// ingestEvent 信封解析后的摄入事件（纯数据视图，不含任何库操作）。
// 为什么要这个中间结构：解析段与写段的失败语义完全不同（见 parseIngestEnvelope），
// 把"兼容两层事件名"的口径收在解析处一次算好，后面各标签段只读最终 EventName，
// 不会出现某段拿顶层名、某段拿 data 名的分叉。
type ingestEvent struct {
	EventType  string         // 顶层事件类型（event_logs.event_type 列按原实现取这个）
	EventName  string         // 两层兼容后的最终事件名（标签计算 switch 的分派键）
	Attributes map[string]any // 事件属性（标签判据的唯一来源）
	CustomerID uint           // 属性里的 customer_id（宽松转换）
	Phone      string         // 身份锚：手机号
	Email      string         // 身份锚：注册邮箱（防薅v2：注册邮箱作为身份锚之一）
}

// parseIngestEnvelope 解包 mq 信封并抽出身份锚字段。
// 为什么公共前置单独成段：它的失败路径与写段相反——脏消息（JSON 解析永久失败）必须
// 立即跳过 + 死信日志、回 ok=false 让上层 ack（P2-81：格式错不会自愈，return err 只会
// 让 Kafka 白重试 5 次），而后续写段失败要上抛触发重试；两种错误混在一处必然有一类被带偏。
func parseIngestEnvelope(env mq.Envelope) (*ingestEvent, bool) {
	// 信封解包：mq.Publish 包装为 {event_type, data:{UserEvent}}
	var payload struct {
		EventType string `json:"event_type"`
		EventName string `json:"event_name"`
		Data      struct {
			EventType  string         `json:"event_type"`
			EventName  string         `json:"event_name"`
			AnchorType string         `json:"anchor_type"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		// P2-81 修复(2026-09-09)：脏消息(JSON 解析永久失败)立即 ack 跳过+死信日志。
		// 原 return err → Kafka 重试 5 次全废才放弃——格式错不会自愈，徒耗 broker 与审计回放。
		log.Printf("[CDP][DEAD-LETTER] 事件 %s 信封 JSON 无法解析 (topic=%s, keyscope=%s)，跳过: %v",
			env.Header.EventID, env.Topic, env.Key, err)
		return nil, false
	}
	// 兼容两层事件名（顶层 eventType 与 UserEvent.event_name）
	eventName := payload.Data.EventName
	if eventName == "" {
		eventName = payload.EventType
	}
	attrPhone, _ := payload.Data.Attributes["phone"].(string)
	attrEmail, _ := payload.Data.Attributes["email"].(string) // 防薅v2：注册邮箱作为身份锚之一
	return &ingestEvent{
		EventType:  payload.EventType,
		EventName:  eventName,
		Attributes: payload.Data.Attributes,
		CustomerID: toUintAny(payload.Data.Attributes["customer_id"]),
		Phone:      attrPhone,
		Email:      attrEmail,
	}, true
}

// applyBehaviorTagsTx 行为/身份维原子标签计算：按最终事件名分派的白名单表（首批规则；扩展走 cdp_tag_definitions 配置）。
// 为什么单独成段：这是唯一按"事件名"分支的一段，其余标签段只按属性存在性判定；
// 红线是"事件驱动、非行为硬推"（如复访必须属性显式 repeat=true 才打），新增事件类型的
// 改动面因此收敛在本段。失败路径与全事务一致——ApplyTagTx 内部对未定义标签 WARN 容忍，
// 不回错误也不中断后面的段（与原实现一致）。
func applyBehaviorTagsTx(tx *gorm.DB, ev *ingestEvent, oneID string, tid, profileID uint) {
	switch ev.EventName {
	case "guest_created":
		ApplyTagTx(tx, tid, profileID, "idm_guest", "1")
	case "lead_captured":
		ApplyTagTx(tx, tid, profileID, "beh_lead_captured", "1")
		ApplyTagTx(tx, tid, profileID, "beh_deep_interest", "1")
	case "conversation_msg":
		ApplyTagTx(tx, tid, profileID, "beh_msg_active", "1")
		// 价格探询：仅当事件属性携带消息正文且命中价格关键词（零方文本，非行为硬推态度）
		var msgText string
		if c, _ := ev.Attributes["content"].(string); c != "" {
			msgText = c
		}
		if t, _ := ev.Attributes["text"].(string); t != "" {
			msgText = strings.TrimSpace(msgText + " " + t)
		}
		if isPriceInquiry(msgText) {
			ApplyTagTx(tx, tid, profileID, "beh_price_inquiry", "1")
		}
	case "store_visit":
		ApplyTagTx(tx, tid, profileID, "beh_visit", "1")
		// 复访：事件属性显式携带 repeat=true 才打（避免每次到访都算复访）
		if rpt, _ := ev.Attributes["repeat"].(bool); rpt {
			ApplyTagTx(tx, tid, profileID, "beh_repeat_visit", "1")
		}
	case "test_drive":
		ApplyTagTx(tx, tid, profileID, "beh_testdrive", "1")
		ApplyTagTx(tx, tid, profileID, "beh_deep_interest", "1")
	case "payment":
		// M2（2026-08-25）：付费事实入 CDP——订单确认到账事件此前发布即沉底，
		// 编排层心跳消费者不认此事件名。CDP 定位"记录事实"，流程联动（欢迎流）留待专项。
		ApplyTagTx(tx, tid, profileID, "tran_order_paid", "1")
		ApplyTagTx(tx, tid, profileID, "beh_deep_interest", "1")
		log.Printf("[CDP] payment 事件已记账 tenant=%d one=%s", tid, oneID)
	case "model_view":
		// P1-1 浏览车型：模型详情页访问（事件驱动，非行为硬推）
		ApplyTagTx(tx, tid, profileID, "beh_viewed_model", "1")
		ApplyTagTx(tx, tid, profileID, "beh_deep_interest", "1")
	case "complaint":
		ApplyTagTx(tx, tid, profileID, "beh_complained", "1")
	case "share":
		ApplyTagTx(tx, tid, profileID, "beh_shared", "1")
	case "referral":
		ApplyTagTx(tx, tid, profileID, "beh_referral", "1")
	case "follow":
		ApplyTagTx(tx, tid, profileID, "beh_followed", "1")
	case "booking":
		ApplyTagTx(tx, tid, profileID, "beh_booked", "1")
	}
}

// applyEnvironmentTagsTx 环境维标签：渠道路由 + 到访时段（上下文信号，非行为推导）。
// 为什么单独成段：判据与行为段不同——只认属性显式携带的上下文（route/channel/device/…，
// 携带则打、否则不打），且到访时段由"事件发生时刻"派生；route/slot 两个值还要供
// 事务提交后的 collector 遥测复用，所以段尾返回给调用方，语义与原实现捕获外层变量一致。
func applyEnvironmentTagsTx(tx *gorm.DB, attrs map[string]any, tid, profileID uint) (route, slot string) {
	// P1-3 环境维标签：渠道路由 + 到访时段（上下文信号，非行为推导）
	route, _ = attrs["route"].(string)
	if route != "" {
		ApplyTagTx(tx, tid, profileID, "env_route", route)
	}
	slot = "rest"
	if h := time.Now().Hour(); h >= 9 && h < 18 {
		slot = "work"
	}
	ApplyTagTx(tx, tid, profileID, "env_time_slot", slot)

	// P3 环境维补齐：渠道/设备/引荐/地理/语言（事件属性携带则打，否则不打）
	if v, _ := attrs["channel"].(string); v != "" {
		ApplyTagTx(tx, tid, profileID, "env_channel", v)
	}
	if v, _ := attrs["device"].(string); v != "" {
		ApplyTagTx(tx, tid, profileID, "env_device", v)
	}
	if v, _ := attrs["referrer"].(string); v != "" {
		ApplyTagTx(tx, tid, profileID, "env_referrer", v)
	}
	if v, _ := attrs["geo"].(string); v != "" {
		ApplyTagTx(tx, tid, profileID, "env_geo", v)
	}
	if v, _ := attrs["language"].(string); v != "" {
		ApplyTagTx(tx, tid, profileID, "env_language", v)
	}
	if v, _ := attrs["utm_source"].(string); v != "" {
		ApplyTagTx(tx, tid, profileID, "env_utm_source", v)
	}
	if v, _ := attrs["landing_page"].(string); v != "" {
		ApplyTagTx(tx, tid, profileID, "env_landing_page", v)
	}
	if v, _ := attrs["browser"].(string); v != "" {
		ApplyTagTx(tx, tid, profileID, "env_browser", v)
	}
	return route, slot
}

// attitudeSignals 态度维四路零方信号（事务提交后供 collector 遥测复用，与原实现的四变量同构）。
type attitudeSignals struct {
	emotion, intent, satisfaction, objection string
}

// applyAttitudeTagsTx 态度维标签：仅当事件属性显式携带 NLP/零方情绪/意向/异议（emotion/intent/objection）时打——
// 红线：严禁由行为硬推态度；chat.go 发布 conversation_msg 时携带 strategy 情绪即合规。
// 为什么单独成段：这段带着全链路最严的判据约束（只认零方显式信号），最容易被"顺手加个
// 行为推导"破坏，独立成段让红线只盯一处；忠诚/流失/推荐意愿（P1-1 扩展）同守此红线。
// emo/intent/sat/obj 四值还要供事务提交后的 collector 遥测，故以结构体返回而非吞在段内。
func applyAttitudeTagsTx(tx *gorm.DB, attrs map[string]any, tid, profileID uint) attitudeSignals {
	var att attitudeSignals
	if att.emotion, _ = attrs["emotion"].(string); att.emotion != "" {
		ApplyTagTx(tx, tid, profileID, "att_emotion", att.emotion)
	}
	if att.intent, _ = attrs["intent"].(string); att.intent != "" {
		ApplyTagTx(tx, tid, profileID, "att_intent", att.intent)
	}
	if att.satisfaction, _ = attrs["satisfaction"].(string); att.satisfaction != "" {
		ApplyTagTx(tx, tid, profileID, "att_satisfaction", att.satisfaction)
	}
	if att.objection, _ = attrs["objection"].(string); att.objection != "" {
		ApplyTagTx(tx, tid, profileID, "att_objection", att.objection)
	}
	// P1-1 态度维扩展：忠诚/流失风险/推荐意愿——仅零方或运营显式标记（emotion/intent 同红线，严禁行为硬推）
	if loy, _ := attrs["loyalty"].(string); loy != "" {
		ApplyTagTx(tx, tid, profileID, "att_loyalty", loy)
	}
	if churn, _ := attrs["churn_risk"].(string); churn != "" {
		ApplyTagTx(tx, tid, profileID, "att_churn_risk", churn)
	}
	if adv, _ := attrs["advocate"].(string); adv != "" {
		ApplyTagTx(tx, tid, profileID, "att_advocate", adv)
	}
	return att
}

// processEvent 单事件处理全流程
//
// 拆分说明（2026-09-28 纯结构重构，行为零变化）：公共前置（信封解析/脏消息跳过）收进
// parseIngestEnvelope，三维标签计算各收进 applyBehaviorTagsTx / applyEnvironmentTagsTx /
// applyAttitudeTagsTx。**事务边界仍在本函数的 db.DB.Transaction 闭包内，未跨函数拆散**；
// 各段共用同一个 tx 且只经 ApplyTagTx/UpsertAnchorTx/EnsureProfileTx 既有写入口——
// 没有新增第二次落库路径，幂等仍由上层 mq.WithInbox 抢占裁决，租户核对仍是 SetTenantRLS 一条。
func processEvent(ctx context.Context, env mq.Envelope) error {
	ev, ok := parseIngestEnvelope(env)
	if !ok {
		return nil
	}

	oneID := env.Header.OneID
	tid := env.Header.TenantID

	// 4) Collector 遥测素材（事务外组装；attrs 供事务内标签计算复用）
	var route, slot string
	var att attitudeSignals

	// 1)~3) 写入段：锚点归并 + 画像确保 + 事件落库 + 标签计算
	// P2-2 RLS热路径接入：CDP 摄入写收口在「单事务 + SET LOCAL 租户隔离」内完成——
	// RLS_ENABLED=true 时 DB 强制按租户收敛，与应用层 db.RQ 双重保险；
	// profile 捕获到事务外供 collector 遥测引用。
	var profile *model.CdpProfile
	err := db.DB.Transaction(func(tx *gorm.DB) error {
		// 事务内激活租户行级隔离（提交/回滚自动失效）
		if r := db.SetTenantRLS(tx, tid); r.Error != nil {
			return r.Error
		}

		// 1) 身份锚点归并（手机号锚存在时建立映射）
		if ev.Phone != "" {
			UpsertAnchorTx(tx, tid, "phone", ev.Phone, oneID)
		}
		if ev.Email != "" {
			UpsertAnchorTx(tx, tid, "email", ev.Email, oneID)
		}

		// 2) 确保画像主体存在
		profile = EnsureProfileTx(tx, tid, oneID, ev.CustomerID)
		if profile == nil {
			// P1-33 修复(2026-09-09)：画像创建失败返回错误而不是静默 nil——
			// 否则事件被标记 done 永久丢失标签计算；LOG 模式由上层重试
			return fmt.Errorf("画像主体创建失败 one=%s", oneID)
		}

		// 3) Raw Zone：不可变事件日志
		logEvent := model.EventLog{
			TenantID: tid, CustomerID: ev.CustomerID,
			EventType: ev.EventType, EventKey: ev.EventName,
			EventValue: string(env.Payload), Source: "scrm",
		}
		if err := tx.Create(&logEvent).Error; err != nil {
			log.Printf("[CDP] 事件落库失败: %v", err)
		}

		// 4) 原子标签计算（首批规则表；扩展走 cdp_tag_definitions 配置）
		applyBehaviorTagsTx(tx, ev, oneID, tid, profile.ID)

		// P1-3 环境维标签：渠道路由 + 到访时段（上下文信号，非行为推导）
		route, slot = applyEnvironmentTagsTx(tx, ev.Attributes, tid, profile.ID)

		// P1-3 态度维标签：仅当事件属性显式携带 NLP/零方情绪/意向/异议时打（红线见段注释）
		att = applyAttitudeTagsTx(tx, ev.Attributes, tid, profile.ID)
		return nil
	})
	if err != nil {
		log.Printf("[CDP] 摄入事务失败: %v", err)
		return err
	}

	// P2 collector：CDP 事件进数据飞轮（脱敏在 Collect 内完成；URL 空则丢弃）
	service.Collect("cdp_event", tid, map[string]any{
		"one_id":       oneID,
		"route":        route,
		"time_slot":    slot,
		"emotion":      att.emotion,
		"intent":       att.intent,
		"satisfaction": att.satisfaction,
		"objection":    att.objection,
	})
	return nil
}

// toUintAny any→uint 宽松转换
func toUintAny(v any) uint {
	switch x := v.(type) {
	case float64:
		return uint(x)
	case uint:
		return x
	case int:
		return uint(x)
	case json.Number:
		n, _ := x.Int64()
		return uint(n)
	}
	return 0
}

// isPriceInquiry 判断文本是否含价格探询意图（零方文本关键词，非行为硬推）
func isPriceInquiry(text string) bool {
	if text == "" {
		return false
	}
	low := strings.ToLower(text)
	priceKW := []string{"贵", "便宜", "优惠", "降价", "价格", "多少钱", "预算", "首付", "分期", "贷款", "折扣", "议价", "price", "cost", "discount"}
	for _, kw := range priceKW {
		if strings.Contains(low, kw) {
			return true
		}
	}
	return false
}
