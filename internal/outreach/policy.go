// Package outreach 主动触达领域层：把"到点给某客户发一句话"变成可排期、可审计、
// 可撤回的任务，并在派发前完成合规裁决（可达通道、微信 48h 窗口、周内频次上限、静默时段）。
//
// 分包位置（D2b 分包红线）：领域代码不进 internal/service，出站投递只通过 channel.Enqueue
// 复用既有通道出站队列，本包不自建第二条出站通路。
//
// 依赖方向：outreach → db/model/runtimecfg/contentsafety；调用方是 api 层与 main.go 后台 ticker。
// 本包**不 import internal/channel**——派发时由调用方注入 SendFunc，避免 api↔outreach↔channel 成环。
package outreach

import (
	"strconv"
	"strings"
	"time"

	"ai-scrm/internal/contentsafety"
	"ai-scrm/internal/model"
)

// maxContentRunes 触达正文长度上限（按字素数计，中文一条销售话术远够用）。
// 为什么要限：正文会原样进 channel_outbound 并投递到微信/企微，超长文本在公众号侧会被截断，
// 用户看到的是一条不完整的话术——不如创建时就拒掉。
const maxContentRunes = 500

// clockMinutes 把 "HH:MM" 解析成当日分钟数；格式非法返回 ok=false。
func clockMinutes(spec string) (int, bool) {
	spec = strings.TrimSpace(spec)
	colon := strings.IndexByte(spec, ':')
	if colon <= 0 {
		return 0, false
	}
	h, err1 := strconv.Atoi(strings.TrimSpace(spec[:colon]))
	m, err2 := strconv.Atoi(strings.TrimSpace(spec[colon+1:]))
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// inQuietSpan 判断当日分钟数是否落在静默段内。
// 跨午夜段（start>end，如 21:00-09:00）取"或"语义；同日段（start<end）取区间语义；
// start==end 视为不设静默段（否则等于全天禁发，是明显的配置错误而非产品意图）。
func inQuietSpan(minOfDay, start, end int) bool {
	if start == end {
		return false
	}
	if start > end {
		return minOfDay >= start || minOfDay < end
	}
	return minOfDay >= start && minOfDay < end
}

// DeferPastQuiet 把落在静默时段内的计划时间顺延到该时段结束时刻。
//
// 为什么要顺延而不是直接拒：门店说"明天上午联系客户"，配置恰好让 08:00 处于静默段——
// 直接拒会让运营以为功能坏了，顺延到 09:00 才是他们要的语义（不吵客户，但事照办）。
// 时段串非法/为空一律原样返回（fail-open：配置写错不该让排期能力整体失效）。
func DeferPastQuiet(at time.Time, spec string, loc *time.Location) time.Time {
	spec = strings.TrimSpace(spec)
	if spec == "" || !strings.Contains(spec, "-") {
		return at
	}
	parts := strings.SplitN(spec, "-", 2)
	start, ok1 := clockMinutes(parts[0])
	end, ok2 := clockMinutes(parts[1])
	if !ok1 || !ok2 || start == end {
		return at
	}
	if loc == nil {
		loc = time.Local
	}
	local := at.In(loc)
	minOfDay := local.Hour()*60 + local.Minute()
	if !inQuietSpan(minOfDay, start, end) {
		return at
	}
	// 顺延锚点：当天（或跨午夜段的次日）的 end 时刻
	day := local
	if start > end && minOfDay >= start {
		// 处于跨午夜段的"前半段"（如 22:00，窗口 21:00-09:00）→ 结束点在明天
		day = local.AddDate(0, 0, 1)
	}
	deferred := time.Date(day.Year(), day.Month(), day.Day(), end/60, end%60, 0, 0, loc)
	return deferred
}

// WindowAllows 判定该通道类型此刻能否主动发消息。
//
// 返回 (allowed, reason)：reason 为 model.OutreachReason* 稳定字面量，供任务留痕与冒烟断言。
// 规则按通道真实约束分档（不是"一套窗口感全部"）：
//   - wecom_app：企微自建应用消息可由企业主动下发，无微信客服 48h 会话窗限制 → 放行；
//   - wecom_kf / wechat_mp：微信侧只在客户来句后 48h 内允许被动式客服主动发送，
//     超窗渠道直接拒收（适配器已把 errcode 判为 Fatal 免退避），这里提前裁决省一次死信；
//   - 未知类型：按最严的 48h 窗口处理（宁可少发，不可对客户静默失败）。
func WindowAllows(chType string, lastInbound, now time.Time, windowHours int) (bool, string) {
	if chType == "" {
		return false, model.OutreachReasonNoChannel
	}
	if chType == "wecom_app" {
		return true, ""
	}
	if windowHours <= 0 {
		windowHours = 48
	}
	if lastInbound.IsZero() {
		return false, model.OutreachReasonOutOfWindow
	}
	if now.Sub(lastInbound) > time.Duration(windowHours)*time.Hour {
		return false, model.OutreachReasonOutOfWindow
	}
	return true, ""
}

// ValidateContent 校验并清洗触达正文：空/超长直接拒，命中 BLOCK 词拒，命中 MASK 词脱敏后放行。
// 复用内容安全词库（C1）——触达是我们主动开口，说错话比被动回复更伤，绝不能绕过闸门。
func ValidateContent(content string) (cleaned string, reason string) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", model.OutreachReasonContentFlagged
	}
	if len([]rune(trimmed)) > maxContentRunes {
		return "", model.OutreachReasonContentFlagged
	}
	res := CheckFunc(trimmed)
	if res.Hit && res.Level == contentsafety.LevelBlock {
		return "", model.OutreachReasonContentFlagged
	}
	if res.Cleaned != "" {
		return res.Cleaned, ""
	}
	return trimmed, ""
}
