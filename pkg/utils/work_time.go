package utils

import (
	"ai-scrm/config"
	"math/rand"
	"strings"
	"time"
)

// ============================================================
// 工作时间与回复速度工具
// 用于模拟人工回复的延迟，让AI回复更像真人
// ============================================================

// 问题难度等级
const (
	DifficultySimple  = "simple"  // 简单：寒暄/确认/是或否
	DifficultyMedium  = "medium"  // 中等：参数/配置/功能咨询
	DifficultyComplex = "complex" // 复杂：价格/方案/个性化/投诉
)

// IsWorkTime 判断当前是否为工作时间
// 工作时间：工作日 + 上班时间内
// 非工作时间：回复延迟×2（下班后）或×3（周末）
func IsWorkTime() bool {
	now := time.Now()
	cfg := config.GlobalConfig.WorkTime

	// 判断是否为工作日
	weekday := int(now.Weekday()) // 周日=0, 周一=1, ..., 周六=6
	workDays := parseWorkDays(cfg.WorkDays)
	isWorkDay := false
	for _, d := range workDays {
		if d == weekday {
			isWorkDay = true
			break
		}
	}
	if !isWorkDay {
		return false
	}

	// 判断是否在上班时间内
	hour := now.Hour()
	return hour >= cfg.WorkStartHour && hour < cfg.WorkEndHour
}

// IsWeekend 判断当前是否为周末
func IsWeekend() bool {
	now := time.Now()
	weekday := int(now.Weekday())
	return weekday == 0 || weekday == 6
}

// GetReplyDelay 获取回复延迟时间（秒）
// 根据问题难度、紧迫等级、是否工作时间计算延迟
// 返回：延迟秒数
func GetReplyDelay(difficulty string, urgencyLevel string) int {
	cfg := config.GlobalConfig.ReplySpeed

	// 根据紧迫等级和难度获取基础延迟范围
	var minDelay, maxDelay int

	switch urgencyLevel {
	case "L3":
		switch difficulty {
		case DifficultySimple:
			minDelay, maxDelay = cfg.ReplySimpleL3[0], cfg.ReplySimpleL3[1]
		case DifficultyMedium:
			minDelay, maxDelay = cfg.ReplyMediumL3[0], cfg.ReplyMediumL3[1]
		case DifficultyComplex:
			minDelay, maxDelay = cfg.ReplyComplexL3[0], cfg.ReplyComplexL3[1]
		default:
			minDelay, maxDelay = cfg.ReplySimpleL3[0], cfg.ReplySimpleL3[1]
		}
	case "L2":
		switch difficulty {
		case DifficultySimple:
			minDelay, maxDelay = cfg.ReplySimpleL2[0], cfg.ReplySimpleL2[1]
		case DifficultyMedium:
			minDelay, maxDelay = cfg.ReplyMediumL2[0], cfg.ReplyMediumL2[1]
		case DifficultyComplex:
			minDelay, maxDelay = cfg.ReplyComplexL2[0], cfg.ReplyComplexL2[1]
		default:
			minDelay, maxDelay = cfg.ReplySimpleL2[0], cfg.ReplySimpleL2[1]
		}
	default: // L1
		switch difficulty {
		case DifficultySimple:
			minDelay, maxDelay = cfg.ReplySimpleL1[0], cfg.ReplySimpleL1[1]
		case DifficultyMedium:
			minDelay, maxDelay = cfg.ReplyMediumL1[0], cfg.ReplyMediumL1[1]
		case DifficultyComplex:
			minDelay, maxDelay = cfg.ReplyComplexL1[0], cfg.ReplyComplexL1[1]
		default:
			minDelay, maxDelay = cfg.ReplySimpleL1[0], cfg.ReplySimpleL1[1]
		}
	}

	// 最小延迟保护
	if minDelay < cfg.ReplyMinDelay {
		minDelay = cfg.ReplyMinDelay
	}

	// 在范围内随机
	delay := minDelay
	if maxDelay > minDelay {
		delay = minDelay + rand.Intn(maxDelay-minDelay)
	}

	// 非工作时间乘以系数（优化：从×2/×3降为×1.5/×2，避免下班后等2分钟才回）
	if !IsWorkTime() {
		if IsWeekend() {
			delay = delay * 3 / 2 // 周末：×1.5
		} else {
			delay = delay * 2 / 2 // 下班后：×1（不再额外加延迟，模拟人工也有手机）
		}
	}

	return delay
}

// JudgeDifficulty 判断问题难度
// 简单：寒暄/确认/是或否（关键词匹配）
// 中等：参数/配置/功能咨询（知识库有直接答案）
// 复杂：价格/方案/个性化/投诉
func JudgeDifficulty(text string) string {
	textLower := strings.ToLower(text)
	textRunes := []rune(text)

	// ---- 复杂问题判断 ----
	// 价格类关键词
	priceKeywords := []string{
		"多少钱", "价格", "优惠", "便宜", "贵", "预算", "落地价",
		"首付", "贷款", "月供", "金融", "利息", "置换", "补贴",
		"报价", "底价", "最低价", "成交价", "全款",
	}
	if containsAny(textLower, priceKeywords) {
		return DifficultyComplex
	}

	// 投诉/负面情绪
	complaintKeywords := []string{
		"投诉", "差评", "垃圾", "骗", "坑", "套路", "忽悠",
		"不满意", "不行", "不好", "失望", "生气", "退款",
		"维权", "赔偿", "质量问题", "故障", "坏了",
	}
	if containsAny(textLower, complaintKeywords) {
		return DifficultyComplex
	}

	// 方案类（个性化需求）
	solutionKeywords := []string{
		"推荐", "怎么选", "哪个好", "对比", "区别", "建议",
		"方案", "配置选择", "选哪个", "买哪个", "纠结",
	}
	if containsAny(textLower, solutionKeywords) {
		return DifficultyComplex
	}

	// ---- 简单问题判断 ----
	// 很短的消息（10字以内）大概率是简单问题
	if len(textRunes) <= 10 {
		simpleKeywords := []string{
			"你好", "您好", "在吗", "嗯", "哦", "好的", "知道了",
			"对", "是的", "没错", "可以", "行", "没问题",
			"谢谢", "感谢", "哈哈", "呵呵", "ok", "OK",
		}
		if containsAny(textLower, simpleKeywords) {
			return DifficultySimple
		}
		// 很短且不是复杂问题，算简单
		if len(textRunes) <= 5 {
			return DifficultySimple
		}
	}

	// 纯确认/是或否问题
	if strings.HasSuffix(textLower, "吗") ||
		strings.HasSuffix(textLower, "么") ||
		strings.HasSuffix(textLower, "吧") {
		if len(textRunes) <= 15 {
			return DifficultySimple
		}
	}

	// ---- 默认中等 ----
	// 参数/配置/功能咨询默认中等
	return DifficultyMedium
}

// ============================================================
// 辅助函数
// ============================================================

// parseWorkDays 解析工作日配置字符串为数字数组
// 如 "1,2,3,4,5" → [1,2,3,4,5]（周日=0）
func parseWorkDays(workDaysStr string) []int {
	if workDaysStr == "" {
		return []int{1, 2, 3, 4, 5} // 默认周一到周五
	}
	parts := strings.Split(workDaysStr, ",")
	result := make([]int, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if d, err := time.Parse(time.RFC3339, part+"T00:00:00Z"); err == nil {
			result = append(result, int(d.Weekday()))
		}
		// 直接解析数字
		if len(part) > 0 && part[0] >= '0' && part[0] <= '9' {
			n := 0
			for _, c := range part {
				if c >= '0' && c <= '9' {
					n = n*10 + int(c-'0')
				}
			}
			if n >= 0 && n <= 6 {
				result = append(result, n)
			}
		}
	}
	if len(result) == 0 {
		return []int{1, 2, 3, 4, 5}
	}
	return result
}

// containsAny 判断text是否包含keywords中的任何一个
func containsAny(text string, keywords []string) bool {
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}
