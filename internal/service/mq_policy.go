// 消息策略层（D2a 文件拆分 2026-09-12）：简单消息判定/预设回复、离题检测、到店两分支话术、类人延迟计算。
// 与合并队列引擎(message_queue.go)解耦——引擎只管窗口/批次/锁，本文件管"这类消息怎么回、延迟多久"。
package service

import (
	"ai-scrm/config"
	"log"
	"math/rand"
	"strings"
	"time"
)

// ============================================================
// 简单消息检测
// 短消息+关键词匹配，这类消息直接快速回复，不走合并窗口和AI大模型
// 业务规则：简单问题不需要AI思考，直接预设话术，45秒内返回
// 同时避免简单消息被合并窗口延迟，导致"回复永远是上一句"
// ============================================================

// simpleKeywords 简单消息关键词列表
var simpleKeywords = []string{
	"在吗", "在不在", "你好", "您好", "嗨", "hi", "hello",
	"好的", "好", "嗯", "哦", "行", "可以", "没问题",
	"那我撤了", "撤了", "先这样", "拜拜", "再见", "88",
	"谢谢", "感谢", "收到", "明白", "了解",
	"还在吗", "还在不", "人呢", "还在吗",
}

// IsSimpleMessage 判断是否为简单消息
// 规则：≤8个字 + 匹配关键词列表（去空格后精确匹配或包含匹配）
// 修复问题5：含车相关关键词的消息即使≤8字也不算简单消息，必须走AI
// 注意：使用已有的carRelatedKeywords白名单（与IsOffTopic共用）
func IsSimpleMessage(content string) bool {
	trimmed := strings.TrimSpace(content)

	// 修复问题5：含车相关关键词→不是简单消息，必须走AI正常回复
	// carRelatedKeywords已在IsOffTopic上方定义，此处复用
	for _, kw := range carRelatedKeywords {
		if strings.Contains(strings.ToLower(trimmed), strings.ToLower(kw)) {
			return false // 含车相关词→走AI，不走简单通道
		}
	}

	// 超过8个字不算简单消息
	if len([]rune(trimmed)) > 8 {
		return false
	}
	// 匹配关键词
	for _, kw := range simpleKeywords {
		if trimmed == kw || strings.Contains(trimmed, kw) {
			return true
		}
	}
	return false
}

// GetSimpleReplyDelay 简单消息的延迟时间
// 修复：用户明确要求简单消息快速通道固定8秒延迟
// 修复：改为从后台配置读取(simple_msg_delay)，无需发版即可调节
// 不看工作时间，不分工作日/周末，统一固定值——简单消息的核心是"快速接住"
// 简单消息（"你好"/"在吗"等）先独立回复，不跟正式消息混排队
func GetSimpleReplyDelay() time.Duration {
	sec := DefaultSystemConfigService.GetInt("simple_msg_delay", 8)
	return time.Duration(sec) * time.Second
}

// CalcHumanlikeDelay 计算AI回复的模拟真人延迟
// 公式：总回复时长 = 打字时长(40字/分) - 合并等待抵扣 + 线下偏移
//
//	打字时长：回复字数 / 40字每分钟
//	合并等待抵扣：合并了N条消息时，扣 mergeWindow*N（客户已经等了）
//	线下偏移（工作时间）：30s(简单) / 60s(中等) / 90s(复杂)
//	线下偏移（非工作时间）：60s(简单) / 120s(中等) / 180s(复杂)
//	到店倾向客户：线下偏移=0（客户要来店了，顾问得秒回，只算打字速度）
//	非工作时间：18:00-次日9:00及周末
func CalcHumanlikeDelay(tenantID uint, replyText string, mergeWaitDuration time.Duration, mergeCount int, isStoreVisit bool) time.Duration {
	// 修复：模拟模式(开发调试)跳过真人延迟。否则 mock 下仍会 sleep 满延迟
	// 叠加合并窗口超过客户端 60s 超时，开发联调时表现为"对话挂死"。
	// AI_MOCK_MODE=true 是开发调试信号，延迟无意义，直接返回 0。
	if config.GlobalConfig.AI.MockMode {
		log.Printf("[模拟延迟] 模拟模式开启，跳过真人延迟直接返回(0s)")
		return 0
	}

	// 2026-09-09 用户决策：大幅缩短回复延迟，固定 5~15s 随机模拟"输入节奏"。
	// 背景：旧公式 = 打字时长(40字/分) + 线下偏移(30~180s，按闲忙/早晚分档)，最慢可达 75s+，
	// 叠加 25s 合并窗口与 AI 调用后，客户端实际要干等近 2 分钟——触发前端 60s
	// "顾问可能正在忙碌中，请稍候" 占位，且旧实现让延迟阻塞 AI 回复落库与 WS 推送
	// （回复生成后不能实时入库，进程崩溃即丢）。现改为固定小延迟，回复生成即实时可见。
	// 保留参数签名以降低调用方改动面；旧配置键 reply_min_delay/max_reply_delay/
	// offline_offset_* 在本路径不再读取。
	minSec, maxSec := 5, 15
	sec := minSec + rand.Intn(maxSec-minSec+1)
	delay := time.Duration(sec) * time.Second

	log.Printf("[模拟延迟] 固定小延迟 %ds（区间 %d~%ds，瞬时需 reply_delay_mode=instant）", sec, minSec, maxSec)
	return delay
}

// GetSimpleReply 简单消息的预设快速回复
// 根据消息内容返回合适的预设话术，不走AI大模型
func GetSimpleReply(content string) string {
	trimmed := strings.TrimSpace(content)

	// 打招呼类
	if trimmed == "你好" || trimmed == "您好" || trimmed == "嗨" ||
		strings.EqualFold(trimmed, "hi") || strings.EqualFold(trimmed, "hello") {
		return "你好呀，有啥想了解的？"
	}

	// 确认类
	if trimmed == "好的" || trimmed == "好" || trimmed == "嗯" || trimmed == "哦" ||
		trimmed == "行" || trimmed == "可以" || trimmed == "没问题" ||
		trimmed == "收到" || trimmed == "明白" || trimmed == "了解" {
		return "👌"
	}

	// 在线确认类
	// 修复问题5：改"在的，你说"为更自然的回复，避免触及快速回复策略时显得敷衍
	if trimmed == "在吗" || trimmed == "在不在" || trimmed == "还在吗" ||
		trimmed == "还在不" || trimmed == "人呢" {
		return "在呢，你想了解啥？"
	}

	// 离开类
	if trimmed == "那我撤了" || trimmed == "撤了" || trimmed == "先这样" ||
		trimmed == "拜拜" || trimmed == "再见" || trimmed == "88" {
		return "好嘞，有问题随时找我"
	}

	// 感谢类
	if trimmed == "谢谢" || trimmed == "感谢" {
		return "不客气~"
	}

	// 兜底
	return "在的，你说"
}

// truncateStr 截断字符串到指定rune长度（日志用，避免超长回复刷屏）
func truncateStr(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

// ============================================================
// 话题硬边界拦截（代码层硬约束，不靠提示词软约束）
// 为什么需要？只靠提示词告诉AI"不要聊无关话题"不够，
// AI可能还是会回答算法题、火箭发射等无关内容
// 硬边界 = 代码层拦截，命中关键词直接返回引导话术，不走AI
// ============================================================

// offTopicKeywords 无关话题关键词黑名单
// 命中任何一个 = 判定为无关话题，直接拦截
// 设计原则：宁可漏判（AI靠提示词兜底），不可误判（不能把聊车的话题拦了）
var offTopicKeywords = []string{
	// 编程/技术无关
	"算法题", "leetcode", "力扣", "写代码", "编程题", "代码实现",
	"python", "java", "javascript", "golang", "c++",
	"git", "docker", "k8s", "linux命令",
	// 学术/学科无关（数学/物理/化学等课本内容，跟买车无关）
	"高数", "微积分", "线性代数", "概率论", "数学题", "解方程",
	"物理题", "化学题", "考研", "考公", "四级", "六级", "雅思",
	"托福", "gre", "gmat", "作业", "考试题", "期末考试",
	// 学术/科普无关
	"火箭发射", "航天", "太空探索", "量子计算", "核聚变",
	"相对论", "量子力学", "黑洞",
	// 金融无关
	"股票量化", "量化交易", "期货策略", "期权定价",
	"对冲基金", "高频交易", "k线图",
	// 政治敏感
	"总统选举", "政治立场", "意识形态",
	// 娱乐无关（明确不聊的）
	"明星八卦", "综艺", "选秀",
	// 明确要AI做非销售的事
	"帮我写", "帮我生成", "帮我翻译", "帮我算", "帮我做",
	"帮我解", "求解", "解个题", "算一下",
}

// carRelatedKeywords 车相关关键词白名单
// 如果消息同时命中黑名单和白名单，白名单优先（不拦截）
// 比如客户说"帮我写个试驾报告"→命中"帮我写"但含"试驾"→放行
var carRelatedKeywords = []string{
	"车", "驾", "SUV", "越野", "四驱", "底盘", "动力",
	"续航", "充电", "电池", "油", "油耗", "油耗",
	"品牌", "车型", "配置", "选配", "内饰", "外观",
	"价格", "优惠", "首付", "贷款", "金融", "保险",
	"试驾", "到店", "看车", "提车", "订车", "交付",
	"保养", "维修", "质保", "售后",
	"极石", "坦克", "方程豹", "哈弗", "比亚迪",
	"空间", "安全", "智能", "辅助驾驶", "L2", "自动泊车",
	"竞品", "对比", "评测", "口碑",
	"家用", "通勤", "自驾", "露营", "旅行",
}

// IsOffTopic 检测用户输入是否为无关话题
// 返回：true=无关话题应拦截，false=正常话题放行
// 逻辑：先查白名单（车相关→放行），再查黑名单（无关→拦截）
// 泛行业化：委托行业语义读取层（系统默认层），行业包可配置白/黑名单
func IsOffTopic(content string) bool {
	return IsOffTopicForTenant(0, content)
}

// GetOffTopicReply 返回无关话题的硬拦截引导话术
// 不走AI，0延迟，直接返回预设话术
// 话术设计原则：自然引导回车，不生硬拒绝，不暴露拦截机制
// 泛行业化：委托行业语义读取层（系统默认层），行业包可配置话术集合
func GetOffTopicReply(content string) string {
	return GetOffTopicReplyForTenant(0, content)
}

// ============================================================
// 到店倾向两段式快速回复
// 为什么需要？客户说"试驾""到店"等高意向信号，必须快速接住
// 设计思路：
//   1. 第一段（10-15秒）：快速接住，确认试驾意向，类似简单消息
//   2. 第二段（25-45秒）：追问预约信息（姓名/电话/人数/时间/需求）
//   3. 前端收到后先显示第一段，延时后自动显示第二段（follow_up机制）
// ============================================================

// storeVisitFirstReplies 到店倾向第一段回复模板
// 核心目标：快速接住意向，让客户感受到顾问响应积极
var storeVisitFirstReplies = []string{
	"好的，那我给您约个到店试驾体验吧",
	"好嘞，我帮您安排个试驾体验",
	"没问题，给您约个到店试驾吧",
	"可以呀，试驾体验安排起来",
}

// storeVisitSecondReplies 到店倾向第二段追问模板
// 核心字段：姓名、电话、预计人数、试驾时间、其他需求——不能丢
var storeVisitSecondReplies = []string{
	"麻烦您发一下姓名和电话，预计几位过来，方便的试驾时间，还有其他想了解的也一块说下，我来给您预约",
	"您方便留个姓名和手机号吗？另外大概几位过来、啥时候方便试驾，有啥特别想了解的也告诉我，我给您安排好",
	"帮我留个姓名电话呗，还有大概几个人来、想啥时候试驾，有啥特别需求也一起说，我这边给您约好",
	"您发我一下姓名和联系方式吧，还有几个人来、方便的时间，以及其他想了解的，我来安排预约",
}

// GetStoreVisitFirstReply 获取到店倾向第一段回复（快速接住意向）
// 修复：到店倾向客户不走合并队列，直接走快速通道
// P1-29 修复(2026-09-09)：改读行业键 industry.store_visit_first（JSON 数组，租户覆盖→系统默认→
// 代码兜底）。此前硬编码"到店试驾/看车"话术，edu/realty/wedding 等租户收到汽车话术——打脸泛行业化。
func GetStoreVisitFirstReply(tenantID uint, content string) string {
	replies := industryKeywordList(tenantID, IndustryStoreVisitFirst, storeVisitFirstReplies)
	idx := rand.Intn(len(replies))
	return replies[idx]
}

// GetStoreVisitSecondReply 获取到店倾向第二段追问（预约信息收集）
// 核心字段：姓名、电话、预计人数、试驾时间、其他需求
// P1-29 修复：同步读行业键 industry.store_visit_second
func GetStoreVisitSecondReply(tenantID uint, content string) string {
	replies := industryKeywordList(tenantID, IndustryStoreVisitSecond, storeVisitSecondReplies)
	idx := rand.Intn(len(replies))
	return replies[idx]
}

// GetStoreVisitFirstDelay 到店倾向第一段回复延迟
// 修复：改为从后台配置读取(store_visit_first_delay)，[min,max]区间随机
// 到店倾向必须快速接住，第一段延迟默认10-15秒
// 比简单消息(8秒)略长——因为需要表现"确认安排"的思考感
func GetStoreVisitFirstDelay() time.Duration {
	// 秒回模式下到店快速通道零延迟
	mode := DefaultSystemConfigService.GetString("reply_delay_mode", "normal")
	if mode == "instant" {
		return 0
	}
	sec := DefaultSystemConfigService.GetInterval("store_visit_first_delay", 10, 15)
	return time.Duration(sec) * time.Second
}

// GetStoreVisitSecondDelay 到店倾向第二段追问延迟
// 修复：改为从后台配置读取(store_visit_second_delay)，[min,max]区间随机
// 第二段在第一段之后发出，模拟顾问"查档后追问"，默认25-45秒
func GetStoreVisitSecondDelay() time.Duration {
	// 秒回模式下到店快速通道零延迟
	mode := DefaultSystemConfigService.GetString("reply_delay_mode", "normal")
	if mode == "instant" {
		return 0
	}
	sec := DefaultSystemConfigService.GetInterval("store_visit_second_delay", 25, 45)
	return time.Duration(sec) * time.Second
}
