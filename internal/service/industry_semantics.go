// 行业语义读取层（泛行业化 P2.1，2026-09-03）
//
// 目标：把"汽车行业专属"的语义关键词/话术从代码硬编码收敛为行业配置键
// （industry.* 前缀），行业包切换时改配置即可，代码不动。
//
// 读取链：租户覆盖层(industry.*) → 系统默认层(tenant_id=0, industry.*) → 代码兜底。
// 当前兜底=汽车行业既有硬编码（carRelatedKeywords/offTopicKeywords/...），
// 保证未配置行业键时行为与改造前完全一致。
//
// FIX-14(2026-09-29 审计批)：Prompt 侧读取的函数真源已抽到叶子包
// internal/industrycfg（含 TenantUsesAutoTalk、询价话术、包 prompt/params/mindset），
// 让 internal/ai 不再 import service。本文件保留同名导出包装与私有别名，
// service 既有调用点与回归测试零改名零行为变化；判据仍是单点，只是住在下面。
package service

import (
	"ai-scrm/internal/industrycfg"
	"ai-scrm/internal/strategytypes"
	"strings"
)

// 行业语义配置键（industry.*，system_configs 内 category="industry"）
const (
	IndustryProductName      = "industry.product_name"       // 行业产品称呼（车/房/课程…）
	IndustryTopicKeywords    = "industry.topic_keywords"     // 主题白名单（命中即放行，防误拦）
	IndustryOffTopicKeywords = "industry.offtopic_keywords"  // 无关话题黑名单（命中即拦截）
	IndustryOffTopicReplies  = "industry.offtopic_replies"   // 无关话题兜底话术（JSON 数组）
	IndustryVisitKeywords    = "industry.visit_keywords"     // 到店/体验意图关键词
	IndustryPriceKeywords    = "industry.price_keywords"     // 询价敏感词（触发到店引导）
	IndustryStoreVisitFirst  = "industry.store_visit_first"  // 到店倾向第一段话术（JSON 数组）
	IndustryStoreVisitSecond = "industry.store_visit_second" // 到店倾向第二段话术（JSON 数组）
	// IndustryLeadCapturedConfirm 到店倾向「已留资确认」话术（JSON 数组）。
	// FIX-9(2026-09-27)：这三句原来硬编码在 internal/api/chat_main.go 的分支B里，
	// 内容是汽车口径（"有没有老车要置换"）——edu/wedding 租户的客户留个手机号就收到置换询问，
	// 与 P1-29（到店第一/二段改行业键）是同一类残量。而且通道入站补同一条快速通道时
	// 只能把这三句再抄一遍：文案从此有两份，改一处漏一处。收成行业键单点。
	IndustryLeadCapturedConfirm = "industry.lead_captured_confirm" // 到店倾向已留资确认话术（JSON 数组）
	IndustryHumanReply          = "industry.human_reply"           // 人工接管/待接管话术（JSON 数组；P2-21）
)

// FIX-14(2026-09-29)：以下四个键的真源在 internal/industrycfg（Prompt 侧读取所需），
// 此处为同源常量别名——键字符串字面量全仓仍只有一份定义，service 既有引用面零改动。
const (
	IndustryPriceReplyLead   = industrycfg.IndustryPriceReplyLead   // 询价回复：已留资（体验后报价，不含"约试驾"）
	IndustryPriceReplyNoLead = industrycfg.IndustryPriceReplyNoLead // 询价回复：未留资（引导到店后报价）
	IndustrySalesperson      = industrycfg.IndustrySalesperson      // 销售顾问人设（Prompt 人设兜底）
	IndustryDomainConstraint = industrycfg.IndustryDomainConstraint // 领域约束句子（Prompt 内"只聊X"指令）
)

// industryKeywordList 解析行业关键词列表（JSON 数组）
// 优先级：租户覆盖 → 系统默认(tenant_id=0) → fallback（代码内置）
// FIX-14：真源已迁 industrycfg.IndustryKeywordList，此别名让本文件与 mq_policy.go
// 的调用点零改动（判据单点，勿在此复制实现）。
var industryKeywordList = industrycfg.IndustryKeywordList

// containsKeyword 朴素子串命中（统一小写匹配）
func containsKeyword(text string, words []string) bool {
	lower := strings.ToLower(text)
	for _, w := range words {
		if strings.Contains(lower, strings.ToLower(w)) {
			return true
		}
	}
	return false
}

// ContainsKeywordForTenant 租户级关键词命中判断（供语言层调用，行业包可配置关键词集）
func ContainsKeywordForTenant(text string, words []string) bool {
	return containsKeyword(text, words)
}

// IndustryPriceKeywordsForTenant 租户级询价敏感词（行业包可配置；空按租户族别回退）
// PLAN_FIX_2026-09-21 B1：与 IndustryPriceRepliesForTenant 的话术分流对齐——
// 此前话术已按 TenantUsesAutoTalk 分流（非 auto 包走中立口径），但**关键词仍恒回退
// 汽车词表**（含"落地价/车价/多少钱一辆/多少钱一台"），属同类点漏改：edu/wedding/realty
// 等租户的询价拦截用的是汽车语义词表，语义与话术口径自相矛盾。
func IndustryPriceKeywordsForTenant(tenantID uint) []string {
	return industryKeywordList(tenantID, IndustryPriceKeywords, priceKeywordsFallback(tenantID))
}

// priceKeywordsFallback 询价敏感词兜底口径分流（与 priceReplyFallback 同谓词）：
//   - tenantID == 0（无租户上下文：单测/内部调用）→ 沿用汽车版默认。
//     兼容性说明：`strategy.IsPriceInquiry`（route.go:57 走本函数）在 behavior_test.go
//     断言"落地价多少"=true，该调用无租户上下文；若此处改中立词表会打断既有行为断言。
//   - 绑定汽车族包 → 汽车版；其余（含非 auto 包租户）→ 行业中立版。
func priceKeywordsFallback(tenantID uint) []string {
	if tenantID == 0 {
		return defaultPriceKeywords
	}
	if TenantUsesAutoTalk(tenantID) {
		return defaultPriceKeywords
	}
	return neutralPriceKeywords
}

// neutralPriceKeywords 中立口径·询价敏感词（行业无关）
// 只保留"问价格"这一意图的通用表达，剔掉"落地价/车价/多少钱一辆/多少钱一台/售价多少"
// 等汽车交易专属词——非汽车行业命中它们既无意义，也会让词表与中立话术口径打架。
var neutralPriceKeywords = []string{
	"多少钱", "什么价", "报价", "价格", "价位", "售价", "贵不贵",
	"怎么卖", "怎么算", "优惠多少", "便宜多少", "费用", "收费标准",
}

// defaultPriceKeywords 询价敏感词默认值（汽车族租户缺省→汽车版关键词）
var defaultPriceKeywords = []string{
	"多少钱", "什么价", "报价", "价格", "价位", "售价", "贵不贵",
	"怎么卖", "怎么算", "落地价", "优惠多少", "便宜多少",
	"车价", "售价多少", "多少钱一辆", "多少钱一台",
}

// IndustryVisitKeywordsForTenant 租户级到店/体验意图关键词（行业包可配置；空=nil 走策略中立包规则）
func IndustryVisitKeywordsForTenant(tenantID uint) []string {
	return industryKeywordList(tenantID, IndustryVisitKeywords, nil)
}

// IndustryPriceRepliesForTenant 租户级询价回复话术
// FIX-14(2026-09-29)：真源在 industrycfg（Prompt 与策略两层共用一份判据），此包装让
// internal/llm 等既有 `service.IndustryPriceRepliesForTenant` 调用点零改动。
// 口径注释（P1-29/UATFOLLOWUP F2/批四 P2）随实现同住 industrycfg。
func IndustryPriceRepliesForTenant(tenantID uint, lead bool) []string {
	return industrycfg.IndustryPriceRepliesForTenant(tenantID, lead)
}

// priceReplyFallback 询价回复兜底口径分流别名（真源 industrycfg.PriceReplyFallback）。
// 保留私有名供 tenant_auto_talk_test.go 直接回归分流谓词。
var priceReplyFallback = industrycfg.PriceReplyFallback

// IndustrySalespersonForTenant 租户级销售顾问人设（行业包可配置；空=空串由调用方回退内置）
// FIX-14：真源在 industrycfg，此包装保持 service 对外 API 不变。
func IndustrySalespersonForTenant(tenantID uint) string {
	return industrycfg.IndustrySalespersonForTenant(tenantID)
}

// IndustryDomainConstraintForTenant 租户级领域约束句子（Prompt 内"只聊X"指令；空=空串由调用方回退内置）
// FIX-14：真源在 industrycfg，此包装保持 service 对外 API 不变。
func IndustryDomainConstraintForTenant(tenantID uint) string {
	return industrycfg.IndustryDomainConstraintForTenant(tenantID)
}

// IsOffTopicForTenant 租户级无关话题判定：白名单优先放行，黑名单拦截
func IsOffTopicForTenant(tenantID uint, content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}
	// 白名单优先（当前配置缺省回退汽车关键词）
	if containsKeyword(trimmed, industryKeywordList(tenantID, IndustryTopicKeywords, carRelatedKeywords)) {
		return false
	}
	// 黑名单拦截
	if containsKeyword(trimmed, industryKeywordList(tenantID, IndustryOffTopicKeywords, offTopicKeywords)) {
		return true
	}
	return false
}

// GetOffTopicReplyForTenant 租户级无关话题兜底话术（按长度散列保证确定性）
// F2 修复(2026-09-15)：无行业包绑定的租户回退中立口径（不再说"我是卖车的"），
// 有绑定租户保持汽车版不变；租户/系统层 industry.offtopic_replies 配置优先级最高。
// 批四 P2 修正：分流谓词由「有任意绑定」收紧为「绑定汽车族」——edu/wedding 等非 auto
// 包租户不再收到"聊车吧"汽车文案。
func GetOffTopicReplyForTenant(tenantID uint, content string) string {
	fallback := defaultOffTopicReplies
	if !TenantUsesAutoTalk(tenantID) {
		fallback = neutralOffTopicReplies
	}
	replies := industryKeywordList(tenantID, IndustryOffTopicReplies, fallback)
	idx := len([]rune(content)) % len(replies)
	return replies[idx]
}

// neutralOffTopicReplies 中立口径·无关话题兜底话术（行业无关，不暴露销售领域）
var neutralOffTopicReplies = []string{
	"这块我确实不太在行，咱们聊回正事吧，你想了解点啥？",
	"这个我还真不懂，说回你关心的产品吧，有啥想问的？",
	"哈哈这个超纲了，正事上我专业，你想了解哪方面？",
	"这块帮不上你，咱们还是说你关心的事吧，想了解啥？",
}

// TenantUsesAutoTalk 租户兜底话术是否走汽车领域口径（批四 P2，DEFECT_VERIFY_2026-09-20）。
// FIX-14(2026-09-29)：判据真源（含 30s 进程缓存与 boundPackCodes 依赖）已迁
// internal/industrycfg.TenantUsesAutoTalk；此包装保持 service 对外 API 与本文件
// priceKeywordsFallback/GetOffTopicReplyForTenant 的调用零改动。口径注释见真源。
func TenantUsesAutoTalk(tenantID uint) bool {
	return industrycfg.TenantUsesAutoTalk(tenantID)
}

// GetHumanTakeoverReplyForTenant 人工接管/待接管话术（P2-21）
// 行业键 industry.human_reply 可配置；缺省回退内置文案（与站内 chat_main 硬编码语义一致）。
func GetHumanTakeoverReplyForTenant(tenantID uint, content string) string {
	replies := industryKeywordList(tenantID, IndustryHumanReply, defaultHumanTakeoverReplies)
	idx := len([]rune(content)) % len(replies)
	return replies[idx]
}

// defaultHumanTakeoverReplies 人工接管兜底话术（P2-21；对齐站内"已收到，顾问在路上"语义）
var defaultHumanTakeoverReplies = []string{
	"已收到您的消息，销售顾问正在赶来的路上，请稍候~",
	"消息我收到了，顾问马上回复您，稍等一下哈",
}

// IsStoreVisitIntentForTenant 租户级到店/体验意图判定
// 行业包未配 visit_keywords 时回退策略中立包既有规则（汽车语气词）
func IsStoreVisitIntentForTenant(tenantID uint, text string) bool {
	kws := industryKeywordList(tenantID, IndustryVisitKeywords, nil)
	if len(kws) == 0 {
		return strategytypes.IsStoreVisitIntent(text)
	}
	return containsKeyword(text, kws)
}

// defaultOffTopicReplies 无关话题兜底话术（行业包缺省→汽车版文案）
var defaultOffTopicReplies = []string{
	"哈哈这块我确实不太行，咱们还是聊车吧？你平时用车都干啥？",
	"这个我还真不懂，不过你平时有没有自驾出行的需求？",
	"哈哈我是卖车的，专业对口才靠谱，你想了解哪款？",
	"这块超纲了哈，我对车倒是门儿清，有啥想了解的？",
}
