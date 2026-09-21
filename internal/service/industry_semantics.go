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
	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/strategytypes"
	"encoding/json"
	"strings"
	"sync"
	"time"
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
	IndustrySalesperson      = "industry.salesperson"        // 销售顾问人设（Prompt 人设兜底）
	IndustryDomainConstraint = "industry.domain_constraint"  // 领域约束句子（Prompt 内"只聊X"指令）
	IndustryHumanReply       = "industry.human_reply"        // 人工接管/待接管话术（JSON 数组；P2-21）
)

// industryKeywordList 解析行业关键词列表（JSON 数组）
// 优先级：租户覆盖 → 系统默认(tenant_id=0) → fallback（代码内置）
func industryKeywordList(tenantID uint, key string, fallback []string) []string {
	if runtimecfg.DefaultSystemConfigService == nil {
		return fallback
	}
	list := industryListFrom(runtimecfg.DefaultSystemConfigService.GetStringForTenant(tenantID, key, ""))
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
// P1-29 修复(2026-09-09)：询价硬拦截话术迁入行业键（JSON 数组）。
// lead=true 为已留资分支（体验后报价，严禁"约试驾"——P1-30），lead=false 为未留资引导分支。
// UATFOLLOWUP F2 修复(2026-09-15)：兜底按「有无行业包绑定」分流——
// 有绑定（车企等既有租户）→ 保持汽车口径不变（兼容既有行为与 UAT 断言）；
// 无绑定（general/新行业未上架包）→ 行业中立口径，不再让通用行业租户
// 收到"约试驾/车价"等汽车销售话术（UAT 字节级实测复现：general 租户询价
// 硬拦截返回试驾话术）。行业键已配置时两口径均被覆盖，优先级不变。
// 批四 P2 修正(DEFECT_VERIFY_2026-09-20)：分流谓词收紧为「汽车族绑定」（TenantUsesAutoTalk），
// 非 auto 包（edu/wedding/realty/…）绑定租户同走中立口径。
func IndustryPriceRepliesForTenant(tenantID uint, lead bool) []string {
	key := IndustryPriceReplyNoLead
	if lead {
		key = IndustryPriceReplyLead
	}
	if list := industryKeywordList(tenantID, key, nil); len(list) > 0 {
		return list
	}
	return priceReplyFallback(tenantID, lead)
}

// priceReplyFallback 询价回复兜底口径分流（F2）：
// 绑定汽车族包 → 汽车版（兼容既有车企租户）；无绑定/非 auto 包 → 行业中立版（批四 P2）。
func priceReplyFallback(tenantID uint, lead bool) []string {
	if TenantUsesAutoTalk(tenantID) {
		if lead {
			return defaultPriceRepliesLead
		}
		return defaultPriceRepliesNoLead
	}
	if lead {
		return neutralPriceRepliesLead
	}
	return neutralPriceRepliesNoLead
}

// neutralPriceRepliesLead 中立口径·已留资询价回复（不提试驾/车，行业无关）
var neutralPriceRepliesLead = []string{
	"价格得看具体方案和你的需求来定，你体验之后就清楚了",
	"费用跟方案组合有关，确认好需求我按你的情况出个详细报价",
	"具体价格看你选什么方案，你定好了我按需求给你报价",
}

// neutralPriceRepliesNoLead 中立口径·未留资询价回复（引导进一步沟通，不提行业专属动作）
var neutralPriceRepliesNoLead = []string{
	"价格得看你的需求来定，要不我先了解下你的情况，再给你做个详细报价",
	"费用要看具体方案，你说说主要想解决什么，我按需求给你报个准数",
	"价格跟方案配置有关，我先帮你捋一下需求，然后给你个合适的报价",
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
	if runtimecfg.DefaultSystemConfigService == nil {
		return ""
	}
	return runtimecfg.DefaultSystemConfigService.GetStringForTenant(tenantID, IndustrySalesperson, "")
}

// IndustryDomainConstraintForTenant 租户级领域约束句子（Prompt 内"只聊X"指令；空=空串由调用方回退内置）
func IndustryDomainConstraintForTenant(tenantID uint) string {
	if runtimecfg.DefaultSystemConfigService == nil {
		return ""
	}
	return runtimecfg.DefaultSystemConfigService.GetStringForTenant(tenantID, IndustryDomainConstraint, "")
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
// 旧口径「绑定任意行业包即汽车话术」对非 auto 包错位：包加载器（pack_kb.go）只写
// pack_prompts/params/mindset 键、从不写 industry.*，于是绑了 edu/wedding/realty 包的租户
// 人设是"越野SUV销售"、跑题回"聊车吧"、询价回"约试驾"。
// 新口径按绑定包在 industry_packs.industry 字段的族别分流：仅汽车族（auto/auto_rox/
// auto_rox_sales 等 industry=auto 的行业包及其企业/部门子包）保留汽车兜底；
// 非 auto 包与无绑定同走行业中立口径。db 未初始化（单测环境）按中立兜底；结果 30s 进程缓存。
func TenantUsesAutoTalk(tenantID uint) bool {
	if tenantID == 0 || db.DB == nil {
		return false
	}
	if v, ok := autoTalkCache.Load(tenantID); ok {
		if e, good := v.(autoTalkCacheEntry); good && time.Now().Before(e.expireAt) {
			return e.auto
		}
	}
	auto := false
	if codes := boundPackCodes(tenantID); len(codes) > 0 {
		var n int64
		// 任一绑定包（行业或企业码）属汽车族即算汽车租户；查询失败按中立（保守向，宁中性不错位）
		if err := db.DB.Model(&model.IndustryPack{}).
			Where("code IN ? AND industry = ?", codes, "auto").Count(&n).Error; err == nil {
			auto = n > 0
		}
	}
	autoTalkCache.Store(tenantID, autoTalkCacheEntry{auto: auto, expireAt: time.Now().Add(30 * time.Second)})
	return auto
}

// autoTalkCache 汽车族判定短TTL进程缓存（与 boundPackCodes 同 30s 口径，检索/建 prompt 高频调用）
type autoTalkCacheEntry struct {
	auto     bool
	expireAt time.Time
}

var autoTalkCache sync.Map // tenantID(uint) → autoTalkCacheEntry

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
