// 行业语义读取层（泛行业化 P2.1，2026-09-03）
//
// 目标：把"汽车行业专属"的语义关键词/话术从代码硬编码收敛为行业配置键
// （industry.* 前缀），行业包切换时改配置即可，代码不动。
//
// 读取链：租户覆盖层(industry.*) → 系统默认层(tenant_id=0, industry.*) → 代码兜底。
// 当前兜底=汽车行业既有硬编码（carRelatedKeywords/offTopicKeywords/...），
// 保证未配置行业键时行为与改造前完全一致。
package service

import (
	"ai-scrm/internal/strategytypes"
	"encoding/json"
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
	IndustryPriceReplyLead   = "industry.price_reply_lead"   // 询价回复：已留资（体验后报价，不含"约试驾"）
	IndustryPriceReplyNoLead = "industry.price_reply_nolead" // 询价回复：未留资（引导到店后报价）
	IndustrySalesperson      = "industry.salesperson"      // 销售顾问人设（Prompt 人设兜底）
	IndustryDomainConstraint = "industry.domain_constraint" // 领域约束句子（Prompt 内"只聊X"指令）
	IndustryHumanReply       = "industry.human_reply"       // 人工接管/待接管话术（JSON 数组；P2-21）
)

// industryKeywordList 解析行业关键词列表（JSON 数组）
// 优先级：租户覆盖 → 系统默认(tenant_id=0) → fallback（代码内置）
func industryKeywordList(tenantID uint, key string, fallback []string) []string {
	if DefaultSystemConfigService == nil {
		return fallback
	}
	list := industryListFrom(DefaultSystemConfigService.GetStringForTenant(tenantID, key, ""))
	if len(list) > 0 {
		return list
	}
	return fallback
}

// industryListFrom 反序列化 JSON 数组字符串；非法/空数组返回 nil
func industryListFrom(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var list []string
	if err := json.Unmarshal([]byte(raw), &list); err != nil || len(list) == 0 {
		return nil
	}
	return list
}

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

// IndustryPriceKeywordsForTenant 租户级询价敏感词（行业包可配置；空回退汽车默认）
func IndustryPriceKeywordsForTenant(tenantID uint) []string {
	return industryKeywordList(tenantID, IndustryPriceKeywords, defaultPriceKeywords)
}

// defaultPriceKeywords 询价敏感词默认值（行业包缺省→汽车版关键词）
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
// P1-29 修复(2026-09-09)：询价硬拦截话术迁入行业键（JSON 数组）。
// lead=true 为已留资分支（体验后报价，严禁"约试驾"——P1-30），lead=false 为未留资引导分支。
// 行业包未配置时回退内置汽车版文案（与改造前一致）。
func IndustryPriceRepliesForTenant(tenantID uint, lead bool) []string {
	key := IndustryPriceReplyNoLead
	fallback := defaultPriceRepliesNoLead
	if lead {
		key = IndustryPriceReplyLead
		fallback = defaultPriceRepliesLead
	}
	return industryKeywordList(tenantID, key, fallback)
}

// defaultPriceRepliesLead 已留资询价回复（体验后报价，不约试驾，与 prompt 硬规则一致）
var defaultPriceRepliesLead = []string{
	"价格得看具体配置和您的需求来定，您体验后就知道了",
	"车价跟配置和选装方案有关，确认好后我按您的需求出个详细报价",
	"具体价格看您选什么配置，您定好了我按需求给您报价",
}

// defaultPriceRepliesNoLead 未留资询价回复（引导到店试驾后报价）
var defaultPriceRepliesNoLead = []string{
	"要不帮您约个试驾，体验过后我再根据您的配置需求做个报价，怎么样呀",
	"价格得看配置来定，要不先帮您约个试驾，您试完车我按您的需求做个详细报价，行不",
	"车价跟具体配置有关，要不我帮您安排个试驾，体验好了我按您的需求出个报价，您看咋样",
}

// IndustrySalespersonForTenant 租户级销售顾问人设（行业包可配置；空=空串由调用方回退内置）
func IndustrySalespersonForTenant(tenantID uint) string {
	if DefaultSystemConfigService == nil {
		return ""
	}
	return DefaultSystemConfigService.GetStringForTenant(tenantID, IndustrySalesperson, "")
}

// IndustryDomainConstraintForTenant 租户级领域约束句子（Prompt 内"只聊X"指令；空=空串由调用方回退内置）
func IndustryDomainConstraintForTenant(tenantID uint) string {
	if DefaultSystemConfigService == nil {
		return ""
	}
	return DefaultSystemConfigService.GetStringForTenant(tenantID, IndustryDomainConstraint, "")
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
func GetOffTopicReplyForTenant(tenantID uint, content string) string {
	replies := industryKeywordList(tenantID, IndustryOffTopicReplies, defaultOffTopicReplies)
	idx := len([]rune(content)) % len(replies)
	return replies[idx]
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