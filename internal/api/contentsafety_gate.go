// 内容安全出站闸门（C1，2026-09-12）——API 层统一收口
// 放在 api 层（非 llm）：BLOCK 需要触发"转人工"（写会话态），属编排职责，红线 llm 层不碰 DB 会话。
package api

import (
	"log"

	"ai-scrm/internal/contentsafety"
	"ai-scrm/internal/service"
)

// GateAction 闸门对一条 AI 回复的处置结论
type GateAction int

const (
	GatePass    GateAction = iota // 原样放行（含 shadow 命中只计数）
	GateRewrite                   // MASK 改写：用 Out 替换原文本
	GateBlock                     // BLOCK（enforce）：调用方转人工、发无感知退场话术
)

// ContentsafetyGate 检查一条即将出站的 AI 回复。
// enabled/mode 读平台级热开关；shadow 恒放行（仅告警+计数）；enforce 才改写/拦截。
// 返回处置动作与（如需）改写后的文本。conversation 仅日志用。
func ContentsafetyGate(reply string, convID uint) (action GateAction, out string) {
	if reply == "" {
		return GatePass, reply
	}
	cfg := service.DefaultSystemConfigService
	if !cfg.GetBoolForTenant(0, "contentsafety_enabled", true) {
		return GatePass, reply
	}
	mode := cfg.GetStringForTenant(0, "contentsafety_mode", "shadow")
	res := contentsafety.CheckFull(reply)
	if !res.Hit {
		return GatePass, reply
	}
	service.IncContentSafetyHit()
	log.Printf("[内容安全] 会话%d 命中 level=%s words=%v mode=%s", convID, res.Level, res.Words, mode)
	if mode != "enforce" {
		return GatePass, reply // shadow：只观察不改写
	}
	switch res.Level {
	case contentsafety.LevelBlock:
		service.IncContentSafetyBlock()
		return GateBlock, ""
	default:
		return GateRewrite, res.Cleaned
	}
}

// SafetyHandoffReply BLOCK 时无感知转人工：发一句人设内退场话术（去 AI 味、不露审查痕迹），
// 关闭 AI 回复等顾问接管。返回可直接入库的替本文本；会话态由调用方按既有 RouteHuman 逻辑置。
func SafetyHandoffReply() string {
	return "这个我帮你确认下，稍等一下我回你"
}
